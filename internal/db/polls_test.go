package db_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
)

// TestAPollIsStoredOpenedAndClosedAndListedNewestFirst walks one poll through
// the two writes the counter makes -- when it opens and when it closes -- and
// reads the history back the way the API does: newest first, bounded by time.
func TestAPollIsStoredOpenedAndClosedAndListedNewestFirst(t *testing.T) {
	d := dbtest.Open(t)
	t0 := time.Unix(1_800_000_000, 0)
	first, err := d.CreatePoll(db.Poll{Question: "Next map?", Options: []string{"Dust", "Mirage"},
		DurationS: 60, State: "open", StartedAt: t0, EndsAt: t0.Add(time.Minute),
		Result: json.RawMessage(`{"total":0}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpdatePoll(db.Poll{ID: first, State: "ended", EndedAt: t0.Add(time.Minute),
		Result: json.RawMessage(`{"total":7}`)}); err != nil {
		t.Fatal(err)
	}
	second, err := d.CreatePoll(db.Poll{Question: "Snack?", Options: []string{"a", "b", "c"},
		DurationS: 30, State: "open", StartedAt: t0.Add(time.Hour), EndsAt: t0.Add(time.Hour + 30*time.Second)})
	if err != nil {
		t.Fatal(err)
	}

	p, err := d.Poll(first)
	if err != nil {
		t.Fatal(err)
	}
	if p.State != "ended" || string(p.Result) != `{"total":7}` || !p.EndedAt.Equal(t0.Add(time.Minute)) ||
		len(p.Options) != 2 || p.Question != "Next map?" {
		t.Errorf("read back %+v", p)
	}

	all, err := d.Polls(db.PollQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != second || all[1].ID != first {
		t.Fatalf("history = %+v, want the second poll then the first", all)
	}
	// A poll created with no result reads back as an empty object, not "".
	if string(all[0].Result) != "{}" {
		t.Errorf("an unset result reads back as %q", all[0].Result)
	}

	ranged, err := d.Polls(db.PollQuery{Since: t0.Add(30 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(ranged) != 1 || ranged[0].ID != second {
		t.Errorf("since filter = %+v, want only the second poll", ranged)
	}
	ranged, _ = d.Polls(db.PollQuery{Until: t0.Add(30 * time.Minute)})
	if len(ranged) != 1 || ranged[0].ID != first {
		t.Errorf("until filter = %+v, want only the first poll", ranged)
	}
	limited, _ := d.Polls(db.PollQuery{Limit: 1})
	if len(limited) != 1 || limited[0].ID != second {
		t.Errorf("limit = %+v", limited)
	}

	if _, err := d.Poll(9999); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("a missing poll = %v, want ErrNotFound", err)
	}
	if err := d.UpdatePoll(db.Poll{ID: 9999, State: "ended"}); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("updating a missing poll = %v, want ErrNotFound", err)
	}
}

// TestAPollOpenWhenTheServerStoppedIsMarkedInterrupted: the counter lives in
// memory, so a restart mid-poll loses the votes it was holding. The row must
// not stay "open" for ever -- that would read as a poll still running -- and
// it must not be called "ended", which would present a partial count as a
// result.
func TestAPollOpenWhenTheServerStoppedIsMarkedInterrupted(t *testing.T) {
	d := dbtest.Open(t)
	t0 := time.Unix(1_800_000_000, 0)
	open, _ := d.CreatePoll(db.Poll{Question: "q", Options: []string{"a", "b"}, DurationS: 60,
		State: "open", StartedAt: t0, EndsAt: t0.Add(time.Minute)})
	done, _ := d.CreatePoll(db.Poll{Question: "q2", Options: []string{"a", "b"}, DurationS: 60,
		State: "open", StartedAt: t0, EndsAt: t0.Add(time.Minute)})
	_ = d.UpdatePoll(db.Poll{ID: done, State: "ended", EndedAt: t0.Add(time.Minute)})

	n, err := d.InterruptOpenPolls(t0.Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("InterruptOpenPolls = %d, %v; want 1", n, err)
	}
	p, _ := d.Poll(open)
	if p.State != "interrupted" || !p.EndedAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("the open poll is now %+v", p)
	}
	p, _ = d.Poll(done)
	if p.State != "ended" {
		t.Errorf("an ended poll was touched: %+v", p)
	}
}
