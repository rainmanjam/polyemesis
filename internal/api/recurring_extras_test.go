package api

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// The #18 backend extras: the conformance defect in createTemplateEvent, the skip-then-backfill rule
// for the look-ahead, and the reconcile and skip guards the security review asked for.

// A title the platform refuses AND a cancel the platform also refuses must not leave the occurrence
// upcoming with an empty title. Upcoming means "the event exists and is named"; an unnamed event that
// the sweep believes is fine is the placeholder the feature exists to prevent. The row keeps the event
// id so the sweep can retry the cancel, and it is not counted as an upcoming show.
func TestAFailedTitleAndAFailedCancelDoNotLeaveAnUpcomingRowWithAnEmptyTitle(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	f.fake.editErr = fmt.Errorf("YouTube refused the title")
	f.fake.cancelErr = fmt.Errorf("YouTube refused the cancel too")

	f.sweep(t)

	rows, err := f.store.ListOpenOccurrences()
	if err != nil {
		t.Fatalf("ListOpenOccurrences: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("no row survives a create whose cancel failed; the event id must be kept so the cancel is retried")
	}
	for _, r := range rows {
		if r.State == scheduler.OccurrenceUpcoming && r.Title == "" {
			t.Fatalf("occurrence %d is upcoming with an empty title and event %q; it must not be recorded as a show",
				r.ID, r.PlatformEventID)
		}
	}
}

// A skipped occurrence does not use up one of the N look-ahead slots. The next sweep backfills it with
// the occurrence after the window, so the operator always has N shows created ahead of time, and the
// skipped one itself is still never recreated.
func TestASkippedOccurrenceIsBackfilledByTheNextOneAndNotRecreated(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	f.sweep(t)
	rows, err := f.store.ListOpenOccurrences()
	if err != nil {
		t.Fatalf("ListOpenOccurrences: %v", err)
	}
	if err := f.s.skipOccurrence(context.Background(), rows[0].ID, f.now); err != nil {
		t.Fatalf("skipOccurrence: %v", err)
	}

	f.sweep(t)

	if got := f.fake.createCount(); got != 3 {
		t.Fatalf("created %d events in total, want 3: two, then one backfilling the skipped slot", got)
	}
	last := f.fake.creates[len(f.fake.creates)-1]
	want := time.Date(2026, 11, 17, 19, 0, 0, 0, time.UTC)
	if !last.ScheduledFor.Equal(want) {
		t.Fatalf("backfill scheduled for %v, want the occurrence after the window %v", last.ScheduledFor, want)
	}
}

// A show that has already started (or passed) cannot be skipped: its event may be the live broadcast
// or the page of a show that aired. The refusal is a clear error, and no platform call is made.
func TestSkippingAnOccurrenceThatHasAlreadyStartedIsRefused(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	past := time.Date(2026, 10, 27, 19, 0, 0, 0, time.UTC)
	id, err := f.store.ClaimOccurrence(scheduler.Occurrence{
		ScheduleID: f.schedule, DestinationID: f.dest.ID, At: past, State: scheduler.OccurrencePending,
	})
	if err != nil {
		t.Fatalf("ClaimOccurrence: %v", err)
	}
	if err := f.store.ResolveOccurrence(id, "yt-aired", "Tuesday live 2026-10-27 at 19:00"); err != nil {
		t.Fatalf("ResolveOccurrence: %v", err)
	}

	if err := f.s.skipOccurrence(context.Background(), id, f.now); err == nil {
		t.Fatalf("skipping a show that already started succeeded; it must be refused")
	}
	if got := f.fake.cancelled(); len(got) != 0 {
		t.Fatalf("a refused skip still cancelled %v on the platform", got)
	}
}

// A template or schedule that disappears must not delete a platform event whose show has already
// started. The local row goes; the platform event is left to the operator.
func TestAnOrphanedOccurrenceThatAlreadyStartedKeepsItsPlatformEvent(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	past := time.Date(2026, 10, 27, 19, 0, 0, 0, time.UTC)
	id, err := f.store.ClaimOccurrence(scheduler.Occurrence{
		ScheduleID: f.schedule, DestinationID: f.dest.ID, At: past, State: scheduler.OccurrencePending,
	})
	if err != nil {
		t.Fatalf("ClaimOccurrence: %v", err)
	}
	if err := f.store.ResolveOccurrence(id, "yt-aired", "Tuesday live 2026-10-27 at 19:00"); err != nil {
		t.Fatalf("ResolveOccurrence: %v", err)
	}
	if err := f.store.DeleteSchedule(f.schedule); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}

	f.sweep(t)

	for _, c := range f.fake.cancelled() {
		if c == "yt-aired" {
			t.Fatalf("the sweep cancelled the platform event of a show that already started")
		}
	}
}

// fakeGoLivePusher is a Twitch or Kick provider whose metadata write is recorded instead of sent. It
// embeds the real provider, so Platform and everything else stay the production answers.
type fakeGoLivePusher struct {
	oauth.Provider

	mu      sync.Mutex
	pushes  []oauth.Metadata
	pushErr error
	// warnings, when set, are the parts the platform refused that do not fail the write, such as a
	// category it could not find.
	warnings []string
}

func (f *fakeGoLivePusher) MetadataCaps() oauth.MetadataCaps {
	return oauth.MetadataCaps{Fields: []oauth.MetadataField{oauth.FieldTitle, oauth.FieldCategory}}
}

func (f *fakeGoLivePusher) PushMetadata(_ context.Context, _, _, _ string, m oauth.Metadata) (*oauth.MetadataResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushes = append(f.pushes, m)
	if f.pushErr != nil {
		return nil, f.pushErr
	}
	return &oauth.MetadataResult{Applied: []oauth.MetadataField{oauth.FieldTitle}, Warnings: f.warnings}, nil
}

func (f *fakeGoLivePusher) pushCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pushes)
}

// newGoLiveFixture is one enabled destination on a platform, on a weekly Tuesday 19:00 UTC start
// schedule, with a template that names a category. The destination is ON, because go-live is the moment
// the schedule turned it on.
func newGoLiveFixture(t *testing.T, platform db.Platform, pusher *fakeGoLivePusher) (*Server, *db.DB, *db.Destination, int64) {
	t.Helper()
	stub := newPlatformStub(t)
	set := stub.set().With(platform, pusher)
	s, _, store := testServerWith(t, Options{Config: config.Config{}, Providers: set})
	seedCreds(t, s, store, platform)
	d := seedDestination(t, s, store, platform, "live")
	schedID := seedStartSchedule(t, store, scheduler.KindWeekly, time.Date(2026, 11, 3, 19, 0, 0, 0, time.UTC), d.ID)
	if _, err := store.SaveBroadcastTemplate(scheduler.BroadcastTemplate{
		ScheduleID:   schedID,
		TitlePattern: "Tuesday live {date} at {time}",
		Category:     "Just Chatting",
	}); err != nil {
		t.Fatalf("save template: %v", err)
	}
	if err := store.SetDestinationEnabled(d.ID, true); err != nil {
		t.Fatalf("enable destination: %v", err)
	}
	return s, store, d, schedID
}

// THE TITLE AND CATEGORY ARRIVE AT GO-LIVE, ONCE. Twitch and Kick have no event to create ahead of
// time, so the template reaches them when the show starts: the first tick inside the window writes the
// rendered title and the category, and the next ticks write nothing, so an operator's edit made after
// the start is not overwritten.
func TestTwitchAndKickGetTheTemplateTitleAndCategoryAtGoLiveAndOnlyOnce(t *testing.T) {
	for _, platform := range []db.Platform{db.PlatformTwitch, db.PlatformKick} {
		t.Run(string(platform), func(t *testing.T) {
			pusher := &fakeGoLivePusher{Provider: oauth.NewTwitch()}
			s, _, _, _ := newGoLiveFixture(t, platform, pusher)

			s.goLiveTemplatesOnce(context.Background(), time.Date(2026, 11, 3, 19, 4, 0, 0, time.UTC))
			s.goLiveTemplatesOnce(context.Background(), time.Date(2026, 11, 3, 19, 10, 0, 0, time.UTC))

			if got := pusher.pushCount(); got != 1 {
				t.Fatalf("%s got %d go-live writes for one show, want exactly 1", platform, got)
			}
			want := oauth.Metadata{Title: "Tuesday live 2026-11-03 at 19:00", Category: "Just Chatting"}
			if got := pusher.pushes[0]; got.Title != want.Title || got.Category != want.Category {
				t.Fatalf("%s go-live write = %+v, want the template's title and category %+v", platform, got, want)
			}
		})
	}
}

// A destination turned on long after its show started is not given the template title: the operator
// chose that moment, and a title the template wrote over it would be a surprise.
func TestAShowThatStartedLongAgoIsNotWrittenToADestinationTurnedOnByHand(t *testing.T) {
	pusher := &fakeGoLivePusher{Provider: oauth.NewTwitch()}
	s, _, _, _ := newGoLiveFixture(t, db.PlatformTwitch, pusher)

	s.goLiveTemplatesOnce(context.Background(), time.Date(2026, 11, 3, 21, 0, 0, 0, time.UTC))

	if got := pusher.pushCount(); got != 0 {
		t.Fatalf("a show two hours old got %d go-live writes; it must get none", got)
	}
}

// A refused go-live write is logged and is not retried for the same show. The claim is kept, so the
// next tick inside the window does not send the same title again through a live show.
func TestAFailedGoLiveWriteIsNotRetriedForTheSameShow(t *testing.T) {
	pusher := &fakeGoLivePusher{Provider: oauth.NewTwitch(), pushErr: fmt.Errorf("Twitch refused the title")}
	s, _, _, _ := newGoLiveFixture(t, db.PlatformTwitch, pusher)

	s.goLiveTemplatesOnce(context.Background(), time.Date(2026, 11, 3, 19, 4, 0, 0, time.UTC))
	s.goLiveTemplatesOnce(context.Background(), time.Date(2026, 11, 3, 19, 10, 0, 0, time.UTC))

	if got := pusher.pushCount(); got != 1 {
		t.Fatalf("a refused write was attempted %d times for one show, want exactly 1", got)
	}
}

// The start instant is the most recent firing at or before now, read in the schedule's zone. Before the
// first firing of the week it is the previous week's, not a future one.
func TestTheLastStartInstantIsTheMostRecentFiringAtOrBeforeNow(t *testing.T) {
	sc := scheduler.Schedule{
		Kind: scheduler.KindWeekly, Action: scheduler.ActionStart, Enabled: true,
		TZ: "UTC", AtMinutes: 19 * 60, Days: []time.Weekday{time.Tuesday},
	}

	got, ok := lastOccurrence(sc, time.Date(2026, 11, 3, 19, 4, 0, 0, time.UTC))
	if !ok || !got.Equal(time.Date(2026, 11, 3, 19, 0, 0, 0, time.UTC)) {
		t.Fatalf("last start at 19:04 Tuesday = %v (ok %v), want that Tuesday 19:00", got, ok)
	}
	got, ok = lastOccurrence(sc, time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
	if !ok || !got.Equal(time.Date(2026, 10, 27, 19, 0, 0, 0, time.UTC)) {
		t.Fatalf("last start on Monday noon = %v (ok %v), want the previous Tuesday 19:00", got, ok)
	}
}

// A stranded event is cancelled on a later sweep once the platform takes the cancel again, and only then
// is its record removed. Until then it holds its slot, so no second event is created for the same show.
func TestAStrandedEventIsCancelledOnALaterSweepWhenTheCancelRecovers(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	f.fake.editErr = fmt.Errorf("YouTube refused the title")
	f.fake.cancelErr = fmt.Errorf("YouTube refused the cancel too")
	f.sweep(t)
	created := f.fake.createCount()

	f.fake.cancelErr = nil
	f.fake.editErr = nil
	f.sweep(t)

	if got := f.fake.cancelled(); len(got) < 2 {
		t.Fatalf("the stranded events were cancelled %d times once the platform recovered, want both", len(got))
	}
	if got := f.fake.createCount(); got-created != 2 {
		t.Fatalf("the sweep created %d events after the stranded ones were removed, want the two replacements", got-created)
	}
}

// A write that lands with a warning, such as a category the platform cannot match, still counts as the
// one attempt for the show. The warning is logged; the title is not held back for it.
func TestAGoLiveWriteThatLandsWithAWarningStillCountsAsTheOneAttempt(t *testing.T) {
	pusher := &fakeGoLivePusher{Provider: oauth.NewKick(), warnings: []string{"Kick has no category called Nope"}}
	s, _, _, _ := newGoLiveFixture(t, db.PlatformKick, pusher)

	s.goLiveTemplatesOnce(context.Background(), time.Date(2026, 11, 3, 19, 4, 0, 0, time.UTC))
	s.goLiveTemplatesOnce(context.Background(), time.Date(2026, 11, 3, 19, 10, 0, 0, time.UTC))

	if got := pusher.pushCount(); got != 1 {
		t.Fatalf("a write with a warning was attempted %d times, want 1", got)
	}
}

// A destination the operator has not turned on is not written at go-live: nothing is on air yet.
func TestAGoLiveTickWritesNothingToADestinationThatIsOff(t *testing.T) {
	pusher := &fakeGoLivePusher{Provider: oauth.NewTwitch()}
	s, store, d, _ := newGoLiveFixture(t, db.PlatformTwitch, pusher)
	if err := store.SetDestinationEnabled(d.ID, false); err != nil {
		t.Fatalf("disable destination: %v", err)
	}

	s.goLiveTemplatesOnce(context.Background(), time.Date(2026, 11, 3, 19, 4, 0, 0, time.UTC))

	if got := pusher.pushCount(); got != 0 {
		t.Fatalf("an off destination got %d go-live writes, want none", got)
	}
}

// A stranded event is not cancelled while its destination is on air: the destination is delivering, and
// removing the event under it is the one thing the sweep must not do. The row stays until it is off air.
func TestAStrandedEventIsKeptWhileItsDestinationIsOnAir(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	f.fake.editErr = fmt.Errorf("YouTube refused the title")
	f.fake.cancelErr = fmt.Errorf("YouTube refused the cancel too")
	f.sweep(t)
	before := len(f.fake.cancelled())

	f.fake.cancelErr = nil
	if err := f.store.SetDestinationEnabled(f.dest.ID, true); err != nil {
		t.Fatalf("enable destination: %v", err)
	}
	f.sweep(t)

	if got := len(f.fake.cancelled()); got != before {
		t.Fatalf("a stranded event was cancelled %d times while its destination was on air; it must wait", got-before)
	}
	rows, err := f.store.ListOpenOccurrences()
	if err != nil {
		t.Fatalf("ListOpenOccurrences: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d records survive while the destination is on air, want both stranded records kept", len(rows))
	}
}

// Skipping a show whose destination is on air is refused as well as one whose start has passed. The
// event may be the broadcast that is running, so nothing is cancelled.
func TestSkippingAShowOnAnAirDestinationIsRefused(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	f.sweep(t)
	rows, err := f.store.ListOpenOccurrences()
	if err != nil {
		t.Fatalf("ListOpenOccurrences: %v", err)
	}
	if err := f.store.SetDestinationEnabled(f.dest.ID, true); err != nil {
		t.Fatalf("enable destination: %v", err)
	}

	err = f.s.skipOccurrence(context.Background(), rows[0].ID, f.now)
	if !errors.Is(err, errOccurrenceRefused) {
		t.Fatalf("skip on an on-air destination = %v, want the not-skippable refusal", err)
	}
	if got := f.fake.cancelled(); len(got) != 0 {
		t.Fatalf("a refused skip still cancelled %v", got)
	}
}

// A stranded event whose show has passed is not held for ever by a cancel the platform keeps refusing.
// Once the show's time is past, its record is dropped and the event is named in an error for removal in
// the platform's studio. A show still to come keeps its record, so the cancel is retried.
func TestAStrandedEventWhoseShowHasPassedIsDroppedWhenItsCancelIsStillRefused(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	past := time.Date(2026, 10, 27, 19, 0, 0, 0, time.UTC)
	id, err := f.store.ClaimOccurrence(scheduler.Occurrence{
		ScheduleID: f.schedule, DestinationID: f.dest.ID, At: past, State: scheduler.OccurrencePending,
	})
	if err != nil {
		t.Fatalf("ClaimOccurrence: %v", err)
	}
	if err := f.store.StrandOccurrence(id, "yt-stranded"); err != nil {
		t.Fatalf("StrandOccurrence: %v", err)
	}
	f.fake.cancelErr = fmt.Errorf("YouTube refuses the delete of a live broadcast")

	f.sweep(t)

	rows, err := f.store.ListOpenOccurrences()
	if err != nil {
		t.Fatalf("ListOpenOccurrences: %v", err)
	}
	for _, r := range rows {
		if r.ID == id {
			t.Fatalf("the stranded record of a show that has passed survived a refused cancel; it must be dropped")
		}
	}
}
