package recording

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/media"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// The post-production tier's two obligations to the recordings directory.
//
// Both exist because a derived file has no row of its own. A proxy, a poster
// sheet and a scrub sprite are written beside their master under a subdirectory
// the index never scans, so nothing else in this package would ever consider
// them for retention — and sessions are derived from the index, so their spans
// go stale the moment a member is measured or swept.
//
// Neither of these owns a timer. They run on the sweep this package already
// performs, which is the only loop the recordings directory needs.

// groupSessions puts newly indexed recordings into a session and refreshes the
// spans of the ones that already existed.
//
// The segment hint comes from the configured segment length rather than the
// rules' default, or a ten-minute-segment install would be told it had been
// streaming for an hour between every pair of files whose duration was never
// measured.
func (m *Manager) groupSessions(s db.RecordingSettings) bool {
	rules := db.DefaultSessionRules()
	if s.SegmentSeconds > 0 {
		rules.SegmentHint = time.Duration(s.SegmentSeconds) * time.Second
	}

	changed := false
	ungrouped, err := m.store.UngroupedRecordings()
	if err != nil {
		m.log.Warn("cannot list ungrouped recordings", "err", err)
		return false
	}
	// OLDEST FIRST, and this is load-bearing. AssignRecording chains a segment
	// onto the session of the recording immediately before it, so walking the
	// newest-first listing in the order it arrives asks about each segment
	// before its own predecessor has a session to join — and every segment of
	// one continuous broadcast opens a session of its own. A 70-second test
	// recording came back as nine sessions before this loop was turned round.
	for i := len(ungrouped) - 1; i >= 0; i-- {
		r := ungrouped[i]
		if _, err := m.store.AssignRecording(r.ID, rules); err != nil {
			m.log.Warn("cannot group recording into a session", "file", r.Filename, "err", err)
			continue
		}
		changed = true
	}

	// The stored span is derived from its members, so it is wrong after a
	// segment is measured for the first time or swept by retention — both of
	// which happen on the same tick that got us here.
	if err := m.store.RecalcSessions(); err != nil {
		m.log.Warn("cannot recalculate session spans", "err", err)
	}
	// A session whose last member retention deleted is not an empty broadcast,
	// it is a row nobody wants to see in the library.
	if n, err := m.store.PruneEmptySessions(); err != nil {
		m.log.Warn("cannot prune empty sessions", "err", err)
	} else if n > 0 {
		changed = true
	}
	return changed
}

// sweepDerived deletes proxies, thumbnails and sprites whose master is gone.
//
// It follows the index rather than the directory listing, and an EMPTY index
// means "we cannot tell" rather than "everything is an orphan" — the same rule
// SweepStems follows, and for the same reason: a scan that failed halfway must
// not be read as permission to delete the lot.
func (m *Manager) sweepDerived() bool {
	recs, err := m.store.ListRecordings()
	if err != nil {
		m.log.Warn("cannot list recordings for the derived-media sweep", "err", err)
		return false
	}
	if len(recs) == 0 {
		return false
	}
	known := make(map[string]bool, len(recs))
	for _, r := range recs {
		known[r.Filename] = true
	}
	removed, err := media.Sweep(m.dir, known)
	if err != nil {
		m.log.Warn("derived-media sweep failed", "err", err)
		return false
	}
	if len(removed) > 0 {
		m.log.Info("removed derived media for recordings that are gone", "count", len(removed))
	}
	n := m.sweepTranscripts(known)
	return len(removed) > 0 || n > 0
}

// sweepTranscripts removes transcript files whose recording is gone.
//
// Retention capped the recordings directory but never looked in here, so an
// install that recorded and transcribed for months kept every transcript of
// every deleted recording forever. They are small individually, which is
// exactly why nobody notices until the volume is full.
//
// Transcripts are named after their recording minus the extension, which is
// what makes an orphan identifiable at all. The comparison is built from the
// index rather than by reconstructing an extension, the same direction
// media.Sweep uses and for the same reason: guessing ".mkv" would strand
// anything recorded to a different container.
//
// CLIPS ARE DELIBERATELY NOT SWEPT HERE. A clip is the thing an operator chose
// to keep — often the only reason the session was recorded at all — and
// deleting it because the hours-long master aged out would destroy the artifact
// rather than the byproduct. Clips need their own retention, on their own
// terms; inheriting the master's is worse than having none.
func (m *Manager) sweepTranscripts(known map[string]bool) int {
	if len(known) == 0 {
		return 0
	}
	dir := filepath.Join(m.dir, transcribe.TranscriptsSubdir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			m.log.Warn("cannot read the transcripts directory", "err", err)
		}
		return 0
	}

	survives := make(map[string]bool, len(known))
	for name := range known {
		base := filepath.Base(name)
		survives[strings.TrimSuffix(base, filepath.Ext(base))] = true
	}

	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// A transcript is "<recording base>.<format>", and the base may itself
		// contain dots, so the surviving set is consulted for every prefix
		// rather than assuming one extension.
		if transcriptBelongsToSurvivor(e.Name(), survives) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			m.log.Warn("cannot remove an orphaned transcript", "file", e.Name(), "err", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		m.log.Info("removed transcripts for recordings that are gone", "count", removed)
	}
	return removed
}

// transcriptBelongsToSurvivor reports whether name is a transcript of a
// recording that still exists.
//
// Every prefix is tested because a recording's base name may contain dots, so
// there is no single "strip the extension" that is right for all of them.
// Erring towards KEEPING an ambiguous file is deliberate: a stray transcript
// costs kilobytes, and deleting one that is still referenced loses the only
// searchable record of what was said.
//
// A PREFIX MAY ALSO END AT A HYPHEN, and leaving that out deleted the subtitles
// of recordings that still existed. The transcriber writes one subtitle file per
// track named after the speaker -- worker.go's
// fmt.Sprintf("%s-%s%s", base, fileSafe(...), f.Ext()) -- plus a merged
// "<base>-all". Those join the base with a HYPHEN, so testing only dot
// boundaries compared "rec-…-143000-host.srt" as "…-host.srt" and "…-host" and
// never as "rec-…-143000". Every one of them read as an orphan.
//
// The asymmetry is what hid it: "<base>.json" does end at a dot, so the sweep
// kept the machine-readable transcript and removed every human-readable
// subtitle track for a recording sitting in the library.
//
// Widening the boundary set only ever keeps MORE files, which is the direction
// this function already says it wants to err in. It can now keep a transcript
// whose own recording is gone when some shorter-named recording survives -- a
// few kilobytes, against the subtitles of a session the operator still has.
func transcriptBelongsToSurvivor(name string, survives map[string]bool) bool {
	for i := len(name); i > 0; i-- {
		if i < len(name) && name[i] != '.' && name[i] != '-' {
			continue
		}
		if survives[name[:i]] {
			return true
		}
	}
	return false
}

// removeDerived drops one recording's derived files. Best effort and never
// fatal: the master is already gone, and refusing to finish the deletion over a
// leftover thumbnail would leave the index disagreeing with the disk.
func (m *Manager) removeDerived(filename string) {
	if err := media.Remove(m.dir, filename); err != nil {
		m.log.Warn("cannot remove derived media", "file", filename, "err", err)
	}
}
