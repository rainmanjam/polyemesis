package recording

import (
	"os"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/media"
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
