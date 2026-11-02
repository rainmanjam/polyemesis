package db

import (
	"errors"
	"testing"
	"time"
)

// A GROUPING MADE WHILE THE SWEEP WAS DECIDING IS NOT UNDONE BY IT.
//
// AssignRecording asks "is this recording in a session yet?", decides where it
// goes, and then claims it. A person (or the API) grouping the same recording
// between the question and the claim used to lose: the claim was an upsert
// that moved the recording out of the session it had just been put in, into
// an auto session of the sweep's making. On a loaded runner that is how
// TestASessionHasOneReviewQueueWhicheverFileIsOpened saw "2 scans for one
// session": the startup sweep took the session's first file back out of it.
//
// assignBeforeClaim puts the grouping in that window every time, so this
// fails on every run without the fix rather than once in a hundred.
func TestAssignRecordingDoesNotUndoAGroupingMadeMidDecision(t *testing.T) {
	plant := func(t *testing.T, d *DB, name string, at time.Time) int64 {
		t.Helper()
		if err := d.UpsertRecording(&Recording{Filename: name, StartedAt: at, DurationMS: 600_000}); err != nil {
			t.Fatal(err)
		}
		recs, err := d.ListRecordings()
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range recs {
			if r.Filename == name {
				return r.ID
			}
		}
		t.Fatalf("planted %s, cannot find it", name)
		return 0
	}
	// groupMidDecision is the person: it files the recording into a session
	// of their own while AssignRecording is between its check and its claim.
	groupMidDecision := func(t *testing.T, d *DB) *int64 {
		var mine int64
		assignBeforeClaim = func(recordingID int64) {
			s, err := d.CreateSession(Metadata{Title: "mine"}, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := d.SetSessionRecordings(s.ID, []int64{recordingID}); err != nil {
				t.Fatal(err)
			}
			mine = s.ID
		}
		t.Cleanup(func() { assignBeforeClaim = nil })
		return &mine
	}
	check := func(t *testing.T, d *DB, rec int64, got *Session, mine int64) {
		t.Helper()
		if mine == 0 {
			t.Fatal("the hook never ran: AssignRecording took a path this test does not cover")
		}
		now, err := d.SessionForRecording(rec)
		if err != nil {
			t.Fatal(err)
		}
		if now.ID != mine {
			t.Errorf("the recording is in session %d; it was grouped into %d while the sweep was deciding, and the sweep moved it", now.ID, mine)
		}
		if got == nil || got.ID != mine {
			t.Errorf("AssignRecording reported session %+v, want the one the recording is actually in (%d)", got, mine)
		}
	}

	t.Run("a new session", func(t *testing.T) {
		d := testDB(t)
		rec := plant(t, d, "rec-20261102-090000.mkv", time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC))
		mine := groupMidDecision(t, d)
		got, err := d.AssignRecording(rec, DefaultSessionRules())
		if err != nil {
			t.Fatal(err)
		}
		check(t, d, rec, got, *mine)
		// The auto session it made for nothing is not left behind as an
		// empty broadcast in the Library.
		all, err := d.ListSessions()
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != 1 {
			t.Errorf("%d sessions, want only the one the person made: %+v", len(all), all)
		}
	})

	t.Run("chaining onto the previous segment", func(t *testing.T) {
		d := testDB(t)
		at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
		first := plant(t, d, "rec-20261102-090000.mkv", at)
		if _, err := d.AssignRecording(first, DefaultSessionRules()); err != nil {
			t.Fatal(err)
		}
		next := plant(t, d, "rec-20261102-091000.mkv", at.Add(10*time.Minute))
		mine := groupMidDecision(t, d)
		got, err := d.AssignRecording(next, DefaultSessionRules())
		if err != nil {
			t.Fatal(err)
		}
		check(t, d, next, got, *mine)
	})
}

// The other two things that can happen in the same window, and what the sweep
// must do about each: say so, and leave the recording ungrouped for the next
// pass rather than reporting a session it is not in.
func TestAssignRecordingReportsAClaimItCouldNotMake(t *testing.T) {
	plantOne := func(t *testing.T) (*DB, int64) {
		t.Helper()
		d := testDB(t)
		if err := d.UpsertRecording(&Recording{Filename: "rec-20261102-090000.mkv",
			StartedAt: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC), DurationMS: 600_000}); err != nil {
			t.Fatal(err)
		}
		recs, err := d.ListRecordings()
		if err != nil || len(recs) != 1 {
			t.Fatalf("ListRecordings: %v %+v", err, recs)
		}
		return d, recs[0].ID
	}

	// Every engine's sweep prunes empty auto sessions, and the one this sweep
	// has just made IS empty until the claim lands.
	t.Run("its new session was pruned", func(t *testing.T) {
		d, rec := plantOne(t)
		assignBeforeClaim = func(int64) {
			if _, err := d.PruneEmptySessions(); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() { assignBeforeClaim = nil })
		if s, err := d.AssignRecording(rec, DefaultSessionRules()); !errors.Is(err, ErrNotFound) {
			t.Fatalf("AssignRecording into a pruned session: %+v, %v; want ErrNotFound", s, err)
		}
		if _, err := d.SessionForRecording(rec); !errors.Is(err, ErrNotFound) {
			t.Errorf("the recording was grouped anyway: %v", err)
		}
	})

	t.Run("the store failed", func(t *testing.T) {
		d, rec := plantOne(t)
		assignBeforeClaim = func(int64) { _ = d.Close() }
		t.Cleanup(func() { assignBeforeClaim = nil })
		if s, err := d.AssignRecording(rec, DefaultSessionRules()); err == nil {
			t.Fatalf("AssignRecording over a closed store reported %+v", s)
		}
	})
}
