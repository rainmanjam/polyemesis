package recording

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

/* THE SWEEP DELETED THE SUBTITLES OF RECORDINGS THAT STILL EXIST.
 *
 * sweepTranscripts builds its surviving set as "recording filename minus its
 * extension" -- rec-20260101-143000 -- and keeps any transcript whose name has
 * one of those as a prefix ENDING AT A DOT. Its comment states the naming rule
 * it relies on: "Transcripts are named after their recording minus the
 * extension."
 *
 * THAT RULE HOLDS FOR EXACTLY ONE OF THE THREE FILES THE TRANSCRIBER WRITES.
 * internal/transcribe/worker.go:592 emits one subtitle file PER TRACK, named
 * after the speaker:
 *
 *     fmt.Sprintf("%s-%s%s", base, fileSafe(tt.Speaker, tt.Track), f.Ext())
 *
 * and a merged "<base>-all.srt" beside them. The boundary before the speaker is
 * a HYPHEN, and the prefix walk only ever tests boundaries at a DOT -- so
 * "rec-20260101-143000-host.srt" is compared as "…-host.srt" and "…-host",
 * never as "rec-20260101-143000". Neither matches, so it is an orphan, so it is
 * removed.
 *
 * The .json transcript IS "<base>.json", so it matches and survives. The
 * asymmetry is what hid this: the sweep leaves the machine-readable file and
 * deletes every human-readable subtitle track, for a recording sitting right
 * there in the library. The function's own comment says deleting a referenced
 * transcript "loses the only searchable record of what was said" and that it
 * errs towards keeping -- it errs the other way for the normal case.
 *
 * Not an edge case: every diarized transcript is named this way, and no test
 * covered sweepTranscripts at all.
 */

// transcriptNamesFor builds the set transcribe writes for one recording, in the
// shape worker.go:583-600 writes it: one JSON, one subtitle file per track named
// "<base>-<speaker>", and a merged "<base>-all" once there is more than one
// track. Extensions come from transcribe's own Format values rather than string
// literals, so a format rename cannot leave this testing names nothing emits.
//
// The speakers are lower-case ASCII on purpose: fileSafe is the identity for
// those, so the joined name here is byte-for-byte what the worker produces
// without this test having to reimplement an unexported function.
func transcriptNamesFor(base string, speakers []string) []string {
	names := []string{base + transcribe.FormatJSON.Ext()}
	for _, s := range speakers {
		names = append(names, base+"-"+s+transcribe.FormatSRT.Ext())
	}
	if len(speakers) > 1 {
		names = append(names, base+"-all"+transcribe.FormatSRT.Ext())
	}
	return names
}

func TestTheSweepKeepsTheSubtitlesOfARecordingThatStillExists(t *testing.T) {
	m, dir, _ := newManager(t)

	tdir := filepath.Join(dir, transcribe.TranscriptsSubdir)
	if err := os.MkdirAll(tdir, 0o755); err != nil {
		t.Fatalf("mkdir transcripts: %v", err)
	}

	const surviving = "rec-20260101-143000"
	const deleted = "rec-20251231-090000"

	want := transcriptNamesFor(surviving, []string{"host", "guest"})
	gone := transcriptNamesFor(deleted, []string{"host"})

	for _, n := range append(append([]string{}, want...), gone...) {
		if err := os.WriteFile(filepath.Join(tdir, n), []byte("1\n"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", n, err)
		}
	}

	// Only the first recording is still in the index. The second aged out.
	m.sweepTranscripts(map[string]bool{surviving + ".mkv": true})

	for _, n := range want {
		if _, err := os.Stat(filepath.Join(tdir, n)); err != nil {
			t.Errorf("%s was deleted, but its recording %s.mkv is still in the "+
				"library. The prefix walk only tests boundaries at a dot, and the "+
				"speaker suffix is joined with a hyphen, so a transcript of a "+
				"SURVIVING recording reads as an orphan.", n, surviving)
		}
	}
	for _, n := range gone {
		if _, err := os.Stat(filepath.Join(tdir, n)); err == nil {
			t.Errorf("%s survived, but its recording is gone — the sweep is no "+
				"longer removing the orphans it exists to remove", n)
		}
	}
}

// The widened match must not become "keep everything".
func TestTheSweepStillRemovesTranscriptsThatShareNoRecording(t *testing.T) {
	m, dir, _ := newManager(t)

	tdir := filepath.Join(dir, transcribe.TranscriptsSubdir)
	if err := os.MkdirAll(tdir, 0o755); err != nil {
		t.Fatalf("mkdir transcripts: %v", err)
	}
	for _, n := range []string{
		"unrelated.srt",
		"rec-20260101-143000-host.srt", // its recording is NOT in the set below
		"notes.txt",
	} {
		if err := os.WriteFile(filepath.Join(tdir, n), []byte("1\n"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", n, err)
		}
	}

	if got := m.sweepTranscripts(map[string]bool{"rec-20990101-000000.mkv": true}); got != 3 {
		t.Errorf("removed %d orphaned transcripts, want 3 — widening the prefix "+
			"match must not stop the sweep doing its job", got)
	}
}
