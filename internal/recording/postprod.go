package recording

import (
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/media"
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
	return len(removed) > 0
}

// removeDerived drops one recording's derived files. Best effort and never
// fatal: the master is already gone, and refusing to finish the deletion over a
// leftover thumbnail would leave the index disagreeing with the disk.
func (m *Manager) removeDerived(filename string) {
	if err := media.Remove(m.dir, filename); err != nil {
		m.log.Warn("cannot remove derived media", "file", filename, "err", err)
	}
}
