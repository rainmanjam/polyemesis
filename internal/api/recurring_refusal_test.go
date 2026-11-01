package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// Refusals for the recurring calendar. An operator may only remove an event that is still ahead of the
// clock and whose destination is not on air. Anything else is refused with a 409 that names the reason,
// and the platform is not called. A reconcile pass follows the same rule: an orphaned row for a show that
// has started or is live loses its local row only, and the platform event is left alone.

// pastOccurrence claims and resolves one occurrence an hour before the clock, so it has already started.
func pastOccurrence(t *testing.T, f *recurringFixture, eventID string) int64 {
	t.Helper()
	id, err := f.store.ClaimOccurrence(scheduler.Occurrence{ScheduleID: f.schedule, DestinationID: f.dest.ID,
		At: time.Now().Add(-time.Hour), State: scheduler.OccurrencePending})
	if err != nil {
		t.Fatalf("claim past occurrence: %v", err)
	}
	if err := f.store.ResolveOccurrence(id, eventID, "t"); err != nil {
		t.Fatalf("resolve past occurrence: %v", err)
	}
	return id
}

// A show whose start is already behind the clock cannot be skipped: its event is or was on air.
func TestSkippingAnOccurrenceWhoseShowHasStartedIsRefusedAndTheEventIsKept(t *testing.T) {
	f := newRecurringFixture(t)
	id := pastOccurrence(t, f, "yt-past-1")

	err := f.s.skipOccurrence(context.Background(), id, time.Now())
	if !errors.Is(err, errOccurrenceRefused) {
		t.Fatalf("skip of a started show returned %v, want errOccurrenceRefused", err)
	}
	if got := f.fake.cancelled(); len(got) != 0 {
		t.Fatalf("a refused skip still cancelled %v on the platform", got)
	}
}

// The calendar reports the refusal as 409, not as the 502 a platform failure gets.
func TestSkippingAStartedShowThroughTheCalendarAnswersConflict(t *testing.T) {
	f := newRecurringHTTP(t)
	id, err := f.store.ClaimOccurrence(scheduler.Occurrence{ScheduleID: f.schedule, DestinationID: f.dest.ID,
		At: time.Now().Add(-time.Hour), State: scheduler.OccurrencePending})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.store.ResolveOccurrence(id, "yt-past-2", "t"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	path := "/api/v1/recurring/occurrences/" + strconv.FormatInt(id, 10) + "/skip"
	send(t, f.h, f.sign, http.MethodPost, path, nil, http.StatusConflict)
	if got := f.fake.cancelled(); len(got) != 0 {
		t.Fatalf("a 409 skip still cancelled %v", got)
	}
}

// An occurrence whose event is still being created has no id to cancel yet. Refusing it is a conflict too.
func TestSkippingAnOccurrenceStillBeingCreatedAnswersConflict(t *testing.T) {
	f := newRecurringHTTP(t)
	id, err := f.store.ClaimOccurrence(scheduler.Occurrence{ScheduleID: f.schedule, DestinationID: f.dest.ID,
		At: time.Now().Add(72 * time.Hour), State: scheduler.OccurrencePending})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	path := "/api/v1/recurring/occurrences/" + strconv.FormatInt(id, 10) + "/skip"
	send(t, f.h, f.sign, http.MethodPost, path, nil, http.StatusConflict)
}

// A future show whose destination still has its event is cancelled as before: the refusal is only for
// shows that have started.
func TestSkippingAFutureOccurrenceStillCancelsItsEvent(t *testing.T) {
	f := newRecurringFixture(t)
	id, err := f.store.ClaimOccurrence(scheduler.Occurrence{ScheduleID: f.schedule, DestinationID: f.dest.ID,
		At: time.Now().Add(72 * time.Hour), State: scheduler.OccurrencePending})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.store.ResolveOccurrence(id, "yt-future-1", "t"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := f.s.skipOccurrence(context.Background(), id, time.Now()); err != nil {
		t.Fatalf("skip of a future show: %v", err)
	}
	if got := f.fake.cancelled(); len(got) != 1 || got[0] != "yt-future-1" {
		t.Fatalf("future skip cancelled %v, want yt-future-1", got)
	}
}

// When a schedule disappears, a row for a show that has already started loses only its local row. The
// platform event is a page that aired, or is airing, and must not be deleted by a sweep.
func TestAnOrphanedRowForAStartedShowLosesItsRowButNotItsPlatformEvent(t *testing.T) {
	f := newRecurringFixture(t)
	pastOccurrence(t, f, "yt-aired-1")

	f.s.reconcileOccurrences(context.Background(), time.Now(), map[int64]scheduler.Schedule{},
		map[int64]scheduler.BroadcastTemplate{})

	if got := f.fake.cancelled(); len(got) != 0 {
		t.Fatalf("reconcile deleted %v on the platform for a started show, want nothing", got)
	}
	rows, err := f.store.ListOpenOccurrences()
	if err != nil {
		t.Fatalf("ListOpenOccurrences: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("%d open rows survive reconcile; the orphaned row for a started show should be dropped locally", len(rows))
	}
}

// An orphaned row for a show still ahead is cancelled on the platform, as before.
func TestAnOrphanedRowForAFutureShowIsStillCancelledOnThePlatform(t *testing.T) {
	f := newRecurringFixture(t)
	id, err := f.store.ClaimOccurrence(scheduler.Occurrence{ScheduleID: f.schedule, DestinationID: f.dest.ID,
		At: time.Now().Add(72 * time.Hour), State: scheduler.OccurrencePending})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.store.ResolveOccurrence(id, "yt-orphan-1", "t"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	f.s.reconcileOccurrences(context.Background(), time.Now(), map[int64]scheduler.Schedule{},
		map[int64]scheduler.BroadcastTemplate{})
	if got := f.fake.cancelled(); len(got) != 1 || got[0] != "yt-orphan-1" {
		t.Fatalf("orphaned future event: cancelled %v, want yt-orphan-1", got)
	}
}

// The reason a look-ahead create failed is kept on the template, so the operator sees it in the UI and
// not only in the server log. The next successful create clears it.
func TestALookaheadCreateFailureIsKeptOnTheTemplateUntilTheNextSuccess(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	f.fake.ingestErr = errors.New("the channel has reached its limit of upcoming broadcasts")

	f.sweep(t)

	tpl, err := f.store.GetBroadcastTemplate(f.schedule)
	if err != nil {
		t.Fatalf("GetBroadcastTemplate: %v", err)
	}
	if !strings.Contains(tpl.LastError, "limit of upcoming broadcasts") {
		t.Fatalf("template LastError = %q, want the platform's refusal kept for the operator", tpl.LastError)
	}
	if tpl.LastErrorAt.IsZero() {
		t.Fatalf("template LastErrorAt is zero; the failure needs a time")
	}

	f.fake.ingestErr = nil
	f.now = f.now.Add(time.Hour) // past the backoff the refusal set
	f.sweep(t)
	tpl, err = f.store.GetBroadcastTemplate(f.schedule)
	if err != nil {
		t.Fatalf("GetBroadcastTemplate: %v", err)
	}
	if tpl.LastError != "" {
		t.Fatalf("template LastError = %q after a successful create, want it cleared", tpl.LastError)
	}
}

// A refused create is not retried on every five-minute tick. Each refusal doubles the wait before the next try, and
// YouTube's cap on upcoming broadcasts (userBroadcastsExceedLimit, docs-youtube B28), which no retry lifts, waits the
// longest. Before this a refusal was retried 288 times a day per occurrence, each one spending quota and, for a
// destination with an ingest of its own, creating another unused stream.
func TestARefusedLookaheadCreateBacksOffInsteadOfRetryingEveryTick(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	f.fake.ingestErr = errors.New("refused")
	tries := func() int {
		f.fake.mu.Lock()
		defer f.fake.mu.Unlock()
		return f.fake.nextID
	}

	f.sweep(t)
	if n := tries(); n != 1 {
		t.Fatalf("first sweep tried %d creates, want 1: the second occurrence waits behind the first refusal", n)
	}
	f.now = f.now.Add(recurringTick)
	f.sweep(t) // one tick later: the first wait is over
	if n := tries(); n != 2 {
		t.Fatalf("after one tick: %d tries, want 2", n)
	}
	f.now = f.now.Add(recurringTick)
	f.sweep(t) // the second refusal doubled the wait, so this tick is still held
	if n := tries(); n != 2 {
		t.Fatalf("one tick after the second refusal: %d tries, want still 2", n)
	}
	f.now = f.now.Add(recurringTick)
	f.sweep(t)
	if n := tries(); n != 3 {
		t.Fatalf("two ticks after the second refusal: %d tries, want 3", n)
	}
}

func TestTheUpcomingBroadcastCapWaitsTheLongestBackoff(t *testing.T) {
	capped := fmt.Errorf("refused (%w)", oauth.ErrUpcomingBroadcastLimit)
	if got := templateCreateBackoff(1, capped); got != templateCreateBackoffMax {
		t.Fatalf("cap backoff = %s, want %s", got, templateCreateBackoffMax)
	}
	if got := templateCreateBackoff(1, errors.New("x")); got != recurringTick {
		t.Fatalf("first refusal backoff = %s, want one tick", got)
	}
	if got := templateCreateBackoff(40, errors.New("x")); got != templateCreateBackoffMax {
		t.Fatalf("many refusals backoff = %s, want the cap %s", got, templateCreateBackoffMax)
	}
}
