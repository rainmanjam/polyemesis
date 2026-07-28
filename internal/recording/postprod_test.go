package recording

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/media"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// The bug this pins was visible only in a browser: one continuous 70-second
// test recording rendered in the library as nine separate sessions. The cause
// was the iteration order — AssignRecording chains a segment onto the session
// of the recording immediately BEFORE it, and the ungrouped listing arrives
// newest first, so every segment was asked about before its own predecessor had
// a session to join.
func TestGroupSessionsChainsAContinuousRecordingIntoOneSession(t *testing.T) {
	base := time.Date(2026, 3, 1, 20, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		gaps  []time.Duration // start offset of each segment from base
		want  int
		hint  int // segmentSeconds
		sizes []int64
	}{
		{
			name: "six back-to-back ten-second segments are one broadcast",
			gaps: []time.Duration{0, 10 * time.Second, 20 * time.Second, 30 * time.Second, 40 * time.Second, 50 * time.Second},
			want: 1, hint: 10,
		},
		{
			name: "a gap longer than the rule opens a second session",
			gaps: []time.Duration{0, 10 * time.Second, 2 * time.Hour, 2*time.Hour + 10*time.Second},
			want: 2, hint: 10,
		},
		{
			name: "a single recording is a session of one",
			gaps: []time.Duration{0},
			want: 1, hint: 10,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _, store := newManager(t)
			for _, off := range tc.gaps {
				at := base.Add(off)
				rec := &db.Recording{
					Filename:   segmentName(at),
					StartedAt:  at,
					DurationMS: int64(tc.hint) * 1000,
					Bytes:      1024,
				}
				if err := store.UpsertRecording(rec); err != nil {
					t.Fatalf("upsert: %v", err)
				}
			}

			m.groupSessions(db.RecordingSettings{SegmentSeconds: tc.hint})

			sessions, err := store.ListSessions()
			if err != nil {
				t.Fatalf("list sessions: %v", err)
			}
			if len(sessions) != tc.want {
				t.Errorf("%d recordings grouped into %d sessions, want %d",
					len(tc.gaps), len(sessions), tc.want)
			}
			// Every recording must be in exactly one of them; a segment that
			// fell through the grouping is invisible in the library.
			grouped := 0
			for _, s := range sessions {
				recs, err := store.SessionRecordings(s.ID)
				if err != nil {
					t.Fatalf("session recordings: %v", err)
				}
				grouped += len(recs)
			}
			if grouped != len(tc.gaps) {
				t.Errorf("%d of %d recordings ended up in a session", grouped, len(tc.gaps))
			}
		})
	}
}

// groupSessions runs on the same sweep the retention policy does, so it has to
// be idempotent: a second pass must not create a second set of sessions.
func TestGroupSessionsIsIdempotentAcrossSweeps(t *testing.T) {
	m, _, store := newManager(t)
	base := time.Date(2026, 3, 1, 20, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		at := base.Add(time.Duration(i) * 10 * time.Second)
		if err := store.UpsertRecording(&db.Recording{
			Filename: segmentName(at), StartedAt: at, DurationMS: 10_000, Bytes: 1024,
		}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	s := db.RecordingSettings{SegmentSeconds: 10}

	m.groupSessions(s)
	first, _ := store.ListSessions()
	m.groupSessions(s)
	m.groupSessions(s)
	after, _ := store.ListSessions()

	if len(after) != len(first) {
		t.Errorf("three sweeps produced %d sessions, the first produced %d", len(after), len(first))
	}
}

// The derived-media sweep follows SweepStems' rule: an EMPTY index means "we
// cannot tell", not "everything is an orphan". A scan that failed halfway must
// never be read as permission to delete the library's proxies.
func TestSweepDerivedTreatsAnEmptyIndexAsUnknownRatherThanOrphaned(t *testing.T) {
	m, dir, store := newManager(t)

	layout := media.LayoutFor(dir, "rec-20260301-200000.mkv")
	if err := os.MkdirAll(layout.Dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(layout.Proxy, make([]byte, 16), 0o644); err != nil {
		t.Fatalf("write proxy: %v", err)
	}
	exists := func() bool {
		_, err := os.Stat(layout.Proxy)
		return err == nil
	}

	// Nothing indexed at all.
	m.sweepDerived()
	if !exists() {
		t.Fatal("an empty index deleted derived media; a failed scan must not look like permission to delete")
	}

	// Now index a DIFFERENT recording, which makes the proxy a real orphan.
	if err := store.UpsertRecording(&db.Recording{
		Filename: "rec-20260301-210000.mkv", StartedAt: time.Now(), Bytes: 1,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	m.sweepDerived()
	if exists() {
		t.Error("a genuinely orphaned proxy survived the sweep")
	}

	// And the one whose master IS indexed survives.
	kept := media.LayoutFor(dir, "rec-20260301-210000.mkv")
	if err := os.MkdirAll(kept.Dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(kept.Proxy, make([]byte, 16), 0o644); err != nil {
		t.Fatalf("write proxy: %v", err)
	}
	m.sweepDerived()
	if _, err := os.Stat(kept.Proxy); err != nil {
		t.Error("the sweep deleted a proxy whose master is still indexed")
	}
}

func TestTranscriptsOfDeletedRecordingsAreSwept(t *testing.T) {
	m, _, store := newManager(t)

	dir := filepath.Join(m.dir, transcribe.TranscriptsSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// One transcript per format for a recording that still exists, and one for
	// a recording that is gone. Retention capped the recordings directory but
	// never looked in here, so the orphan used to live forever.
	for _, name := range []string{
		"rec-keep.srt", "rec-keep.vtt", "rec-keep.txt",
		"rec-gone.srt", "rec-gone.vtt",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	if _, err := store.SQL().Exec(
		`INSERT INTO recordings (filename, started_at) VALUES ('rec-keep.mkv', 1)`); err != nil {
		t.Fatalf("seed recording: %v", err)
	}

	m.sweepDerived()

	for _, name := range []string{"rec-keep.srt", "rec-keep.vtt", "rec-keep.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was deleted although its recording still exists", name)
		}
	}
	for _, name := range []string{"rec-gone.srt", "rec-gone.vtt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived although its recording is gone", name)
		}
	}
}

func TestAnEmptyIndexSweepsNoTranscripts(t *testing.T) {
	m, _, _ := newManager(t)
	dir := filepath.Join(m.dir, transcribe.TranscriptsSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rec.srt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// An empty index means "cannot tell", never "everything is an orphan" --
	// a scan that failed halfway must not be read as permission to delete the
	// lot. Same rule sweepDerived and SweepStems already follow.
	m.sweepDerived()
	if _, err := os.Stat(filepath.Join(dir, "rec.srt")); err != nil {
		t.Error("an empty recordings index was treated as permission to delete transcripts")
	}
}

func TestTranscriptSweepKeepsRecordingsWhoseNameContainsDots(t *testing.T) {
	survives := map[string]bool{"2026-07-27.session.1": true}
	// The base may itself contain dots, so there is no single "strip the
	// extension" that is right for every name. Ambiguity resolves towards
	// keeping: a stray transcript costs kilobytes, deleting a live one loses
	// the only searchable record of what was said.
	if !transcriptBelongsToSurvivor("2026-07-27.session.1.srt", survives) {
		t.Error("a transcript of a surviving dotted recording was treated as an orphan")
	}
	if transcriptBelongsToSurvivor("2026-07-27.session.2.srt", survives) {
		t.Error("a transcript of a different recording was treated as surviving")
	}
}

func TestClipsAreNotSweptWithTheirRecording(t *testing.T) {
	m, _, store := newManager(t)

	// A clip is the thing the operator chose to keep -- often the only reason
	// the session was recorded at all. Deleting it because the hours-long
	// master aged out would destroy the artifact rather than the byproduct.
	clipDir := filepath.Join(m.dir, "clips")
	if err := os.MkdirAll(clipDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	keep := filepath.Join(clipDir, "rec-gone-highlight.mp4")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := store.SQL().Exec(
		`INSERT INTO recordings (filename, started_at) VALUES ('rec-keep.mkv', 1)`); err != nil {
		t.Fatalf("seed recording: %v", err)
	}

	m.sweepDerived()

	if _, err := os.Stat(keep); err != nil {
		t.Error("a clip was deleted because its source recording is gone; " +
			"clips need their own retention, not the master's")
	}
}
