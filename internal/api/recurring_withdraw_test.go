package api

import (
	"context"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// Withdrawing a show's events. docs/RECURRING-EVENTS.md tells the operator to switch the schedule off to stop a
// show; before this, only deleting the schedule or the template cancelled the events it had created, and switching
// it off left every public event page up, announcing shows that would never run.

func (f *recurringFixture) editSchedule(t *testing.T, mut func(*scheduler.Schedule)) {
	t.Helper()
	sc, err := f.store.GetSchedule(f.schedule)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	mut(sc)
	if _, err := f.store.UpdateSchedule(sc); err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
}

func TestSwitchingAScheduleOffWithdrawsItsUpcomingEvents(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	f.sweep(t)
	f.editSchedule(t, func(sc *scheduler.Schedule) { sc.Enabled = false })
	f.sweep(t)
	if got := f.fake.cancelled(); len(got) != 2 {
		t.Fatalf("switching the schedule off cancelled %v, want both upcoming events", got)
	}
}

func TestRemovingADestinationFromAScheduleWithdrawsItsEvents(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	f.sweep(t)
	other := seedDestination(t, f.s, f.store, "youtube", "yt-other")
	f.editSchedule(t, func(sc *scheduler.Schedule) { sc.DestinationIDs = []int64{other.ID} })
	f.sweep(t)
	cancelled := f.fake.cancelled()
	if len(cancelled) != 2 || cancelled[0] != "yt-occ-1" && cancelled[1] != "yt-occ-1" {
		t.Fatalf("removing the destination cancelled %v, want its two events", cancelled)
	}
}

func TestLoweringTheLookaheadWithdrawsTheEventsBeyondIt(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	f.sweep(t)
	f.attachTemplate(t, func(tpl *scheduler.BroadcastTemplate) { tpl.Lookahead = 1 })
	f.sweep(t)
	if got := f.fake.cancelled(); len(got) != 1 || got[0] != "yt-occ-2" {
		t.Fatalf("lowering the look-ahead to 1 cancelled %v, want only the farther event yt-occ-2", got)
	}
}

// The go-live mirror names the FIRST counted occurrence's event or nothing. When the first is still being created
// by another sweep (or its create failed), the mirror must not move to next week's event: this week's show would
// then go live on it.
func TestTheGoLiveMirrorNeverSkipsToALaterOccurrence(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	first := time.Date(2026, 11, 3, 19, 0, 0, 0, time.UTC)
	if _, err := f.store.ClaimOccurrence(scheduler.Occurrence{ScheduleID: f.schedule, DestinationID: f.dest.ID,
		At: first, State: scheduler.OccurrencePending}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// lookaheadFor directly: the claim was made on the real clock, and the full sweep's reconcile, on the
	// fixture's clock, would read it as abandoned and release it.
	tpl, err := f.store.GetBroadcastTemplate(f.schedule)
	if err != nil {
		t.Fatalf("GetBroadcastTemplate: %v", err)
	}
	sc, err := f.store.GetSchedule(f.schedule)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	d, err := f.store.GetDestination(f.dest.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	f.s.lookaheadFor(context.Background(), f.fake, *tpl, *sc, d, f.now)
	if f.fake.createCount() != 1 {
		t.Fatalf("created %d events, want only the second occurrence's", f.fake.createCount())
	}
	got, err := f.store.GetDestination(f.dest.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.Facebook.BroadcastID != "" {
		t.Fatalf("the go-live mirror names %q, the second occurrence's event, while the first is still pending",
			got.Facebook.BroadcastID)
	}
}
