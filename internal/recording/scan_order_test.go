package recording

import (
	"os"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// A SEGMENT THAT ARRIVES WHILE A SCAN IS RUNNING IS NOT DE-INDEXED BY IT.
//
// Every engine's manager scans the one recordings directory, so a file and its
// row can appear between one scan's two snapshots. Scan used to list the
// directory first and the index second: the newcomer was then in the index but
// not in the listing, and was deleted as "disappeared from disk" with its file
// right there -- its row recreated next scan under a new id, unmeasured, its
// session membership, metadata and transcripts gone with the old one. That is
// how a Windows runner saw "SaveTranscript: FOREIGN KEY constraint failed":
// the transcript was saved against an id the startup scan had just deleted.
//
// scanBetweenReads puts the newcomer in that window every time.
//
// MUTATION: swap the two reads. Observed: FAIL, the row is gone.
func TestASegmentIndexedMidScanIsNotDroppedAsMissing(t *testing.T) {
	m, dir, store := newManager(t)
	name := segmentName(time.Date(2026, 11, 2, 9, 0, 0, 0, time.Local))
	scanBetweenReads = func() {
		writeFile(t, dir, name, 4096)
		if err := store.UpsertRecording(&db.Recording{Filename: name, Bytes: 4096, DurationMS: 60_000}); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { scanBetweenReads = nil })

	if _, err := m.Scan(); err != nil {
		t.Fatal(err)
	}
	recs, err := store.ListRecordings()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Filename != name || recs[0].DurationMS != 60_000 {
		t.Fatalf("after a scan that overlapped its arrival, the index holds %+v; want %s, still measured", recs, name)
	}
}

// A scan that cannot read the index stops there: it reports the failure and
// touches neither the index nor the directory, rather than reading an empty
// index as "nothing is indexed" and re-indexing (or dropping) on that basis.
func TestAScanThatCannotReadTheIndexChangesNothing(t *testing.T) {
	m, dir, store := newManager(t)
	path := writeFile(t, dir, segmentName(time.Date(2026, 11, 2, 9, 0, 0, 0, time.Local)), 4096)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	changed, err := m.Scan()
	if err == nil {
		t.Fatal("a scan over a closed index reported success")
	}
	if changed {
		t.Error("a scan that failed to read the index reported a change")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("the failed scan touched the segment on disk: %v", statErr)
	}
}
