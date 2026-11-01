package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// THE RECURRING-EVENT REVIEW FINDINGS AT THE TAIL OF THE W3H LIST (29-31).

// TestARetitleWhoseTemplateCannotBeReadIsAServerError pins the error mapping. Every template read error was answered
// 409 "this occurrence's template was deleted", so a database fault reached the operator as a deletion that never
// happened. Only a template that is gone is a conflict. The fault here reaches only the template read: the
// occurrence is read from its own table first and still answers.
//
// MUTATION, internal/api/recurring_handlers.go, in handleOccurrenceTitle: answer every template read error with the
// 409 again. Observed: FAIL -- status 409, want 500.
func TestARetitleWhoseTemplateCannotBeReadIsAServerError(t *testing.T) {
	f := newRecurringHTTP(t)
	send(t, f.h, f.sign, http.MethodPut, "/api/v1/schedules/"+strconv.FormatInt(f.schedule, 10)+"/template",
		templateBody(), http.StatusOK)
	id := planUpcoming(t, f, time.Now().Add(72*time.Hour).Truncate(time.Hour))
	if _, err := f.store.SQL().Exec(`ALTER TABLE broadcast_templates RENAME TO broadcast_templates_unreadable`); err != nil {
		t.Fatalf("break the template table: %v", err)
	}
	send(t, f.h, f.sign, http.MethodPost, "/api/v1/recurring/occurrences/"+strconv.FormatInt(id, 10)+"/title",
		map[string]any{"title": "New"}, http.StatusInternalServerError)
	if len(f.fake.edits) != 0 {
		t.Errorf("a retitle whose template could not be read still wrote %d edits", len(f.fake.edits))
	}
}

// TestASkipIsRefusedWhenTheDestinationWasSwitchedOnAfterItsCheck pins the second look the skip takes. skipOccurrence
// checks Enabled, then cancelOccurrence reads the destination again before the platform delete. The scheduler can
// switch the destination on in between to take this very show live, and the delete would then take the event out
// from under it. The fresh row is the one the skip is held to.
//
// MUTATION, internal/api/recurring.go, in cancelOccurrence: delete the !removeRow && d.Enabled refusal. Observed:
// FAIL -- cancelOccurrence on a switched-on destination = <nil>, want a refusal.
func TestASkipIsRefusedWhenTheDestinationWasSwitchedOnAfterItsCheck(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	f.s.deliveringFn = func(int64) bool { return false }
	now := time.Now()
	at := now.Add(72 * time.Hour).Truncate(time.Hour)
	id, err := f.store.ClaimOccurrence(scheduler.Occurrence{ScheduleID: f.schedule, DestinationID: f.dest.ID,
		At: at, State: scheduler.OccurrencePending})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.store.ResolveOccurrence(id, "yt-occ-skip", "Show"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	occ, err := f.store.GetOccurrence(id)
	if err != nil {
		t.Fatalf("read occurrence: %v", err)
	}
	// The scheduler's switch-on, landing after the skip's own check.
	if err := f.store.SetDestinationEnabled(f.dest.ID, true); err != nil {
		t.Fatalf("enable destination: %v", err)
	}

	err = f.s.cancelOccurrence(context.Background(), *occ, false, now)
	if !errors.Is(err, errOccurrenceRefused) {
		t.Fatalf("cancelOccurrence on a switched-on destination = %v, want a refusal", err)
	}
	if len(f.fake.cancels) != 0 {
		t.Fatalf("the event was cancelled on the platform while its destination was switched on: %v", f.fake.cancels)
	}
	if got, _ := f.store.GetOccurrence(id); got == nil || got.State != scheduler.OccurrenceUpcoming {
		t.Fatalf("the refused skip changed the occurrence: %+v", got)
	}

	// The sweep's withdraw is not held to Enabled: an always-on destination must still lose an event nothing airs.
	if err := f.s.cancelOccurrence(context.Background(), *occ, true, now); err != nil {
		t.Fatalf("the sweep's withdraw of an enabled destination's event = %v, want it cancelled", err)
	}
	if len(f.fake.cancels) != 1 {
		t.Fatalf("the sweep's withdraw made %d cancels, want 1", len(f.fake.cancels))
	}
}

// TestTheXPublishTitleIsTheShowThatStartedLast pins the choice between two shows. Two start schedules for one X
// destination that fire inside the go-live window of each other both match; the first template in the store's
// order won, so the second show published under the first one's title. The schedule that fired last is the show
// going live.
//
// MUTATION, internal/api/recurring_golive.go, in xGoLiveTitleAt: keep the first match (skip a candidate whenever
// best is set). Observed: FAIL -- the earlier show's title.
func TestTheXPublishTitleIsTheShowThatStartedLast(t *testing.T) {
	s, store, d, _ := newGoLiveFixture(t, db.PlatformX, &fakeGoLivePusher{Provider: oauth.NewX()})
	later := seedStartSchedule(t, store, scheduler.KindWeekly, time.Date(2026, 11, 3, 19, 20, 0, 0, time.UTC), d.ID)
	if _, err := store.SaveBroadcastTemplate(scheduler.BroadcastTemplate{
		ScheduleID: later, TitlePattern: "Late show at {time}",
	}); err != nil {
		t.Fatalf("save the later template: %v", err)
	}
	got := s.xGoLiveTitleAt(d, time.Date(2026, 11, 3, 19, 25, 0, 0, time.UTC))
	if got != "Late show at 19:20" {
		t.Fatalf("title with two shows in the window = %q, want the one that started last", got)
	}
	// Before the later one fires, the earlier one is still the show going live.
	if got := s.xGoLiveTitleAt(d, time.Date(2026, 11, 3, 19, 10, 0, 0, time.UTC)); got != "Tuesday live 2026-11-03 at 19:00" {
		t.Fatalf("title before the later show fired = %q, want the earlier show's", got)
	}
}
