package db

import (
	"fmt"
	"testing"
	"time"
)

/* A LONG-LIVED LIBRARY LOOKED ERASED.
 *
 * Four queries build one bound parameter per recording id, from a list the
 * library handler takes straight out of ListRecordings -- which is unbounded.
 * SQLite accepts at most 32766 bound parameters; the 32767th fails the whole
 * statement with "too many SQL variables". Measured, not assumed: see the
 * threshold cases below.
 *
 * IT FAILED SILENTLY. The handler treats an error from any of these as "index
 * unavailable" and substitutes an empty map, so past the threshold every
 * session renders with no members and no poster, every recording drops into
 * Ungrouped, and every title, description and tag vanishes -- with the page
 * still returning 200. Nothing was actually lost, but there is no way for an
 * operator to tell that from looking.
 *
 * Reached by recording for long enough and nothing else. It never recovers,
 * because the list only grows.
 */
func TestTheIDQueriesSurviveMoreRecordingsThanSQLiteCanBind(t *testing.T) {
	d := testDB(t)

	// One over the limit is the interesting case: it is the first id that fails,
	// and it needs exactly two chunks.
	ids := make([]int64, maxSQLiteVariables+1)
	for i := range ids {
		ids[i] = int64(i + 1)
	}

	t.Run("session ids", func(t *testing.T) {
		if _, err := d.SessionIDsForRecordings(ids); err != nil {
			t.Errorf("SessionIDsForRecordings(%d ids): %v — the library shows every "+
				"session empty when this fails", len(ids), err)
		}
	})
	t.Run("recording meta", func(t *testing.T) {
		if _, err := d.ListRecordingMeta(ids); err != nil {
			t.Errorf("ListRecordingMeta(%d ids): %v — every title and tag "+
				"disappears when this fails", len(ids), err)
		}
	})
	t.Run("transcribed", func(t *testing.T) {
		if _, err := d.TranscribedRecordings(ids); err != nil {
			t.Errorf("TranscribedRecordings(%d ids): %v", len(ids), err)
		}
	})
	t.Run("ordering", func(t *testing.T) {
		tx, err := d.sql.Begin()
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback()
		if _, err := orderByStart(tx, ids); err != nil {
			t.Errorf("orderByStart(%d ids): %v — SetSessionRecordings cannot file "+
				"a broadcast when this fails", len(ids), err)
		}
	})
}

// The chunked ordering must agree with the single-statement ORDER BY it
// replaced, across a chunk boundary.
func TestTheChunkedOrderingStillSortsByStartThenID(t *testing.T) {
	d := testDB(t)

	// Seeded newest-first so an unsorted result is visibly wrong.
	const n = 5
	ids := make([]int64, 0, n)
	for i := n - 1; i >= 0; i-- {
		ids = append(ids, seedRecordingAt(t, d,
			fmt.Sprintf("rec-%d.mkv", i), base.Add(time.Duration(i)*time.Hour), time.Hour))
	}

	tx, err := d.sql.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	got, err := orderByStart(tx, ids)
	if err != nil {
		t.Fatalf("orderByStart: %v", err)
	}
	if len(got) != n {
		t.Fatalf("got %d ids, want %d", len(got), n)
	}
	for i := 1; i < len(got); i++ {
		var prev, cur int64
		if err := tx.QueryRow(`SELECT started_at FROM recordings WHERE id = ?`, got[i-1]).Scan(&prev); err != nil {
			t.Fatalf("read started_at: %v", err)
		}
		if err := tx.QueryRow(`SELECT started_at FROM recordings WHERE id = ?`, got[i]).Scan(&cur); err != nil {
			t.Fatalf("read started_at: %v", err)
		}
		if prev > cur {
			t.Errorf("position %d starts at %d, after position %d at %d — the "+
				"sort that replaced ORDER BY is not ordering by start time",
				i, cur, i-1, prev)
		}
	}
}
