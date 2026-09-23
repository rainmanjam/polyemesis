package recording

import (
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// A recording's programme must not depend on which manager scanned it last.
//
// Every engine runs its own recording manager and every one of them scans the
// SAME install-wide recordings directory. Scan used to stamp its own manager's
// programme on every file it saw, and the upsert wrote any non-null source_id
// over the stored one, so each manager re-labelled every file in turn: source
// 1's recording read 3, 1, 1, 3, 1 as the three managers' scans interleaved.
// clipTracks consumes that value to name a clip's tracks, so a clip cut from one
// programme was labelled with whichever programme's scanner had run last.
//
// The programme now travels in the filename the recorder writes, which is the
// one thing every scanner sees identically.
//
// Mutation: have Scan stamp a per-manager programme (engine.New used to pass
// its own via WithSourceID) instead of SourceFromName. Observed to fail with "re-labelled".
func TestEveryScannerAttributesASegmentToTheProgrammeThatRecordedIt(t *testing.T) {
	_, dir, store := newManager(t)
	for _, name := range []string{"Studio B", "Studio C"} {
		src := &db.Source{Name: name, Enabled: true, Ingest: db.DefaultSettings().Ingest}
		if err := store.CreateSource(src); err != nil {
			t.Fatalf("CreateSource: %v", err)
		}
	}
	srcs, err := store.ListSources()
	if err != nil || len(srcs) < 3 {
		t.Fatalf("ListSources: %d sources, %v", len(srcs), err)
	}
	recorder := srcs[0].ID

	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	name := fmt.Sprintf("rec-s%d-%s.mkv", recorder, at.Format("20060102-150405"))
	writeFile(t, dir, name, 1024)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	managers := make([]*Manager, 0, len(srcs))
	for range srcs {
		managers = append(managers, New(log, store, dir, nil))
	}
	for round := 0; round < 2; round++ {
		for i, m := range managers {
			if _, err := m.Scan(); err != nil {
				t.Fatalf("Scan: %v", err)
			}
			recs, err := store.ListRecordings()
			if err != nil || len(recs) != 1 {
				t.Fatalf("ListRecordings: %d rows, %v", len(recs), err)
			}
			got := recs[0].SourceID
			if got == nil || *got != recorder {
				t.Fatalf("after manager %d's scan, %s is attributed to %v, want %d: "+
					"a scanner re-labelled another programme's recording", i, name, deref(got), recorder)
			}
			if st := startTimeFromName(name, time.Time{}); !st.Equal(at) {
				t.Fatalf("start time of %s = %v, want %v", name, st, at)
			}
		}
	}
}

// The pattern the recorder is handed carries the programme, and parses back.
func TestSegmentPatternRoundTripsTheProgramme(t *testing.T) {
	p := SegmentPattern("/rec", 42)
	if filepath.Dir(p) != "/rec" {
		t.Fatalf("SegmentPattern put the file in %s", filepath.Dir(p))
	}
	name := "rec-s42-" + time.Date(2024, 1, 15, 14, 30, 0, 0, time.Local).Format("20060102-150405") + ".mkv"
	if filepath.Base(p) != "rec-s42-%Y%m%d-%H%M%S.mkv" {
		t.Fatalf("SegmentPattern = %s", p)
	}
	if id, ok := SourceFromName(name); !ok || id != 42 {
		t.Fatalf("SourceFromName(%s) = %d, %v", name, id, ok)
	}
	// A segment an earlier release wrote names no programme, and must not be
	// guessed at: the stored attribution, if any, stands.
	if _, ok := SourceFromName("rec-20240115-143000.mkv"); ok {
		t.Fatal("an untagged legacy segment was attributed to a programme")
	}
	for _, bad := range []string{"rec-s0-20240115-143000.mkv", "rec-sx-20240115-143000.mkv", "rec-s-20240115-143000.mkv", "clip-s4-20240115-143000.mkv"} {
		if _, ok := SourceFromName(bad); ok {
			t.Errorf("SourceFromName(%q) accepted a name the recorder never writes", bad)
		}
	}
}

func deref(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
