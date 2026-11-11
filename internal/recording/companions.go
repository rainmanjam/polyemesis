package recording

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/recording/segname"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// ============================================================================
// A RECORDING'S COMPANIONS: its stems and its transcript files.
//
// Neither is indexed, so neither goes with the row. The sweeps follow the
// index from the other side -- SweepStems deletes stems that start before the
// OLDEST surviving master, sweepTranscripts deletes transcripts whose
// recording is gone -- and both deliberately do nothing on an EMPTY index,
// because "we cannot tell" must not read as "everything is an orphan". So a
// recording the operator deleted by hand left its stems on disk until every
// older recording was gone (they still counted toward Usage and the size cap
// meanwhile), and when it was the last recording its transcripts and stems
// stayed for good. The delete dialog says proxies, thumbnails and transcript
// go with it.
//
// Delete knows exactly which recording the operator named, which the sweeps
// never do, so it removes its companions itself. The sweeps' empty-index
// guards stay as they are.
//
// THE ONE IRREVERSIBLE MISTAKE is deleting a NEIGHBOUR's audio or transcript,
// so every rule here errs toward keeping a file.
// ============================================================================

// removeCompanions deletes the stems and transcript files that belonged to
// deleted, a recording whose row and master are already gone. survivors is
// every other recording still indexed. Best-effort, like removeDerived: a
// leftover is logged, never a reason to fail the delete.
//
// THE DISK COUNTS AS A SURVIVOR TOO. survivors comes from the index, and a
// master the scan has not indexed yet is still somebody's recording; its
// stems and transcripts must not be read as this one's for want of a row. So
// every master still on disk is added to the set the ownership rules protect.
func (m *Manager) removeCompanions(deleted string, survivors []string) {
	protected := append([]string(nil), survivors...)
	if entries, err := os.ReadDir(m.dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && isRecording(e.Name()) && e.Name() != filepath.Base(deleted) {
				protected = append(protected, e.Name())
			}
		}
	}
	m.removeOwnStems(deleted, protected)
	if _, err := RemoveTranscriptFiles(m.dir, deleted, protected, nil); err != nil {
		m.log.Warn("cannot remove a deleted recording's transcripts", "file", deleted, "err", err)
	}
}

// removeOwnStems deletes the stems recorded beside deleted.
//
// NOT BY NAME. A stem cuts on its segment boundary while the master waits for
// the next keyframe, so a stem's timestamp can be up to a GOP EARLIER than its
// master's (see stemSweepSlack) and matching the master's exact stamp misses
// real stems. Each stem of the same programme is instead paired with the
// NEAREST master -- the deleted one and every survivor -- and removed only
// when that is the deleted one, by a clear margin, within stemSweepSlack. A
// tie keeps the stem. Correct for any segment length, where a "from this start
// to the next" window would wrongly keep stems of segments under a minute.
func (m *Manager) removeOwnStems(deleted string, survivors []string) {
	prefix, start, ok := segname.SplitMaster(deleted)
	if !ok {
		return
	}
	others := programmeStarts(prefix, survivors)
	dir := m.StemsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			m.log.Warn("cannot read the stems directory", "err", err)
		}
		return
	}
	for _, e := range entries {
		if !isOwnedStem(e, prefix, start, others) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			m.log.Warn("cannot remove a deleted recording's stem", "file", e.Name(), "err", err)
			continue
		}
		m.log.Info("deleted a stem with its recording", "file", e.Name())
	}
}

// programmeStarts is the start of every survivor of the same programme prefix.
func programmeStarts(prefix string, survivors []string) []time.Time {
	var others []time.Time
	for _, name := range survivors {
		if p, at, ok := segname.SplitMaster(name); ok && p == prefix {
			others = append(others, at)
		}
	}
	return others
}

// isOwnedStem reports whether a stems-directory entry belongs to the deleted
// recording: it is a stem of the same programme whose nearest master is the
// deleted one, by a clear margin, within stemSweepSlack.
func isOwnedStem(e os.DirEntry, prefix string, start time.Time, others []time.Time) bool {
	if e.IsDir() {
		return false
	}
	at, _, ok := ParseStemFilename(e.Name())
	if !ok {
		return false
	}
	base, _ := StemMasterBase(e.Name())
	if p, _, ok := segname.SplitMaster(base); !ok || p != prefix {
		return false
	}
	gap := absDuration(at.Sub(start))
	return gap <= stemSweepSlack && !nearerOrAsNear(at, gap, others)
}

// nearerOrAsNear reports whether any of others is at least as close to at as
// gap. As close is enough to keep: a stem two masters claim equally is not
// this delete's to take.
func nearerOrAsNear(at time.Time, gap time.Duration, others []time.Time) bool {
	for _, o := range others {
		if absDuration(at.Sub(o)) <= gap {
			return true
		}
	}
	return false
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// transcriptOwner returns which of bases a transcript file belongs to: the
// LONGEST base that is the whole name before a '.' or '-' boundary. Longest,
// because a recording's base may be a prefix of another's ("rec-a" and
// "rec-a-b"), and "rec-a-b.json" is the second one's even though it also
// starts "rec-a-". See transcriptBelongsToSurvivor for the boundary set.
func transcriptOwner(name string, bases map[string]bool) (string, bool) {
	for i := len(name); i > 0; i-- {
		if i < len(name) && name[i] != '.' && name[i] != '-' {
			continue
		}
		if bases[name[:i]] {
			return name[:i], true
		}
	}
	return "", false
}

// TranscriptTrackFiles names the per-track files a single-track transcript
// delete removes: the per-speaker files written for that track, by the
// speaker parts the worker would have used (transcribe.SidecarSpeakerPart).
type TranscriptTrackFiles struct {
	SpeakerParts []string
}

// RemoveTranscriptFiles deletes the files under recordingsDir/transcripts that
// belong to the recording filename and returns what it removed.
//
// others is every OTHER recording's filename, so a file that belongs to one of
// them -- a longer name that starts with this one's -- is never taken.
//
// With track nil, every file the recording owns goes: the JSON, the merged
// "-all" subtitles and every per-speaker file. With a track, the JSON and the
// merged "-all" files go too, because both carry the deleted track's words
// (the next transcription run writes them again), and of the per-speaker
// files only those named by track.SpeakerParts. A per-speaker file written
// under a label the operator has since renamed is not matched: the database
// keeps only the current label, and guessing at the old one risks another
// track's file.
func RemoveTranscriptFiles(recordingsDir, filename string, others []string, track *TranscriptTrackFiles) ([]string, error) {
	base := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	bases, ok := transcriptBases(base, others)
	if !ok {
		return nil, nil
	}
	dir := filepath.Join(recordingsDir, transcribe.TranscriptsSubdir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var removed []string
	var firstErr error
	for _, e := range entries {
		if e.IsDir() || !wantedTranscript(e.Name(), base, bases, track) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !os.IsNotExist(err) {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed = append(removed, e.Name())
	}
	return removed, firstErr
}

// transcriptBases is the set of recording base names transcripts can belong
// to, this one's included. ok is false when another recording shares base.
func transcriptBases(base string, others []string) (map[string]bool, bool) {
	bases := map[string]bool{base: true}
	for _, o := range others {
		ob := strings.TrimSuffix(filepath.Base(o), filepath.Ext(o))
		if ob == base {
			// Another recording with the same base name (a different
			// container): its transcripts and this one's are the same files,
			// and they are not this delete's to take.
			return nil, false
		}
		bases[ob] = true
	}
	return bases, true
}

// wantedTranscript reports whether the transcript file name belongs to base
// and is one the delete (of every track, or of track) removes.
func wantedTranscript(name, base string, bases map[string]bool, track *TranscriptTrackFiles) bool {
	if owner, ok := transcriptOwner(name, bases); !ok || owner != base {
		return false
	}
	if track == nil {
		return true
	}
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if stem == base || stem == base+"-all" {
		return true
	}
	for _, part := range track.SpeakerParts {
		if part != "" && stem == base+"-"+part {
			return true
		}
	}
	return false
}
