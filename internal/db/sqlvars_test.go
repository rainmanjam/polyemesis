package db

import (
	"errors"
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

/* eachIDChunk is where an off-by-one would live, so it is tested directly
 * rather than only through the four queries that use it. The boundary cases are
 * the point: exactly at the limit must stay ONE statement, and one past it must
 * become two -- because 32766 succeeds and 32767 is the id that fails.
 */
func TestEachIDChunkSplitsAtTheLimitAndNotBefore(t *testing.T) {
	ids := func(n int) []int64 {
		out := make([]int64, n)
		for i := range out {
			out[i] = int64(i + 1)
		}
		return out
	}

	for _, tc := range []struct {
		name  string
		in    []int64
		sizes []int
	}{
		{"none at all calls nothing", nil, nil},
		{"empty slice calls nothing", []int64{}, nil},
		{"one", ids(1), []int{1}},
		{"exactly the limit is one statement", ids(maxSQLiteVariables), []int{maxSQLiteVariables}},
		{"one past the limit splits", ids(maxSQLiteVariables + 1), []int{maxSQLiteVariables, 1}},
		{"twice the limit", ids(2 * maxSQLiteVariables), []int{maxSQLiteVariables, maxSQLiteVariables}},
		{"two and a bit", ids(2*maxSQLiteVariables + 5), []int{maxSQLiteVariables, maxSQLiteVariables, 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []int
			var seen []int64
			if err := eachIDChunk(tc.in, func(chunk []int64) error {
				got = append(got, len(chunk))
				seen = append(seen, chunk...)
				return nil
			}); err != nil {
				t.Fatalf("eachIDChunk: %v", err)
			}
			if len(got) != len(tc.sizes) {
				t.Fatalf("chunk sizes = %v, want %v", got, tc.sizes)
			}
			for i := range got {
				if got[i] != tc.sizes[i] {
					t.Fatalf("chunk sizes = %v, want %v", got, tc.sizes)
				}
			}
			// Every id must be visited exactly once, in order: a chunker that
			// drops or repeats one would silently lose recordings from a listing.
			if len(seen) != len(tc.in) {
				t.Fatalf("visited %d ids, want %d", len(seen), len(tc.in))
			}
			for i, v := range seen {
				if v != tc.in[i] {
					t.Fatalf("id at %d is %d, want %d — the chunks are not the "+
						"input in order", i, v, tc.in[i])
				}
			}
		})
	}
}

// A failing chunk stops immediately: the caller returns the error, and a
// listing must not half-populate from the chunks that happened to run first.
func TestEachIDChunkStopsAtTheFirstFailure(t *testing.T) {
	ids := make([]int64, 2*maxSQLiteVariables+1)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	want := errors.New("statement failed")

	calls := 0
	err := eachIDChunk(ids, func([]int64) error {
		calls++
		return want
	})
	if !errors.Is(err, want) {
		t.Errorf("error = %v, want the chunk's own error", err)
	}
	if calls != 1 {
		t.Errorf("ran %d chunks after the first failed, want 1 — continuing "+
			"would spend three statements to return an error either way", calls)
	}
}

/* A FAILED STATEMENT MUST NOT LOOK LIKE AN EMPTY ANSWER.
 *
 * Each of these builds a map across one or more statements. If one fails, the
 * error has to reach the caller -- because the library's whole problem in this
 * area is that a MISSING entry renders as "this recording has no session" or
 * "no title", never as an error. A partial map is worse than none: it reads as
 * complete. The handler substitutes an empty map deliberately and logs, which
 * it can only do if it is told.
 *
 * A closed handle is the cheapest way to make every statement fail, and it
 * exercises the error arm of each chunk closure.
 */
func TestAFailedStatementReturnsAnErrorRatherThanAPartialMap(t *testing.T) {
	ids := []int64{1, 2, 3}

	t.Run("session ids", func(t *testing.T) {
		d := testDB(t)
		closeForTest(t, d)
		got, err := d.SessionIDsForRecordings(ids)
		if err == nil {
			t.Fatalf("returned %v and no error against a closed database — the "+
				"caller cannot tell that from \"none of them are in a session\"", got)
		}
		if got != nil {
			t.Errorf("returned a map (%v) alongside the error; a partial answer "+
				"reads as complete", got)
		}
	})
	t.Run("recording meta", func(t *testing.T) {
		d := testDB(t)
		closeForTest(t, d)
		if _, err := d.ListRecordingMeta(ids); err == nil {
			t.Error("no error against a closed database — every title would " +
				"silently read as unset")
		}
	})
	t.Run("transcribed", func(t *testing.T) {
		d := testDB(t)
		closeForTest(t, d)
		if _, err := d.TranscribedRecordings(ids); err == nil {
			t.Error("no error against a closed database — every recording would " +
				"silently read as untranscribed")
		}
	})
}

func closeForTest(t *testing.T, d *DB) {
	t.Helper()
	if err := d.sql.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
