package api

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// The recurring-events sweep (#18), driven directly as the preannounce tests drive preannounceOnce.
//
// The YouTube write path is replaced by fakeYouTubeEditor. That is deliberate and it is the one
// place this file departs from the stub-server pattern: the sweep's contract is about WHICH events
// it creates, WHAT it writes to them and WHEN it cancels them. The request shapes that carry those
// writes are pinned in internal/oauth/youtube_template_test.go against documented responses. Here
// the fake records the calls, so a test can say "two events, these two instants, these two titles"
// without parsing query strings a second time.

// fakeYouTubeEditor embeds the real YouTube provider, so ScheduleHorizon and everything else stay
// the production answers, and overrides only the scheduled-broadcast writes.
type fakeYouTubeEditor struct {
	*oauth.YouTube

	mu        sync.Mutex
	nextID    int
	creates   []oauth.IngestOptions
	edits     []oauth.ScheduledEdit
	editedIDs []string
	thumbs    []string
	cancels   []string
	// editErr, when set, is the refusal every title write answers with.
	editErr error
	// cancelErr, when set, is the refusal every cancel answers with.
	cancelErr error
}

func (f *fakeYouTubeEditor) IngestFor(_ context.Context, _, _, _ string, opts oauth.IngestOptions) (*oauth.Broadcast, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := fmt.Sprintf("yt-occ-%d", f.nextID)
	f.creates = append(f.creates, opts)
	return &oauth.Broadcast{ID: id, Target: "UCchannel",
		Ingest: oauth.Ingest{URL: "rtmp://a.example/live2", Key: "key-" + id}}, nil
}

func (f *fakeYouTubeEditor) EditScheduledBroadcast(_ context.Context, _, broadcastID string, e oauth.ScheduledEdit) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, e)
	f.editedIDs = append(f.editedIDs, broadcastID)
	return nil, f.editErr
}

func (f *fakeYouTubeEditor) SetScheduledThumbnail(_ context.Context, _, broadcastID string, _ []byte, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.thumbs = append(f.thumbs, broadcastID)
	return nil
}

func (f *fakeYouTubeEditor) CancelScheduledBroadcast(_ context.Context, _, broadcastID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, broadcastID)
	return f.cancelErr
}

func (f *fakeYouTubeEditor) createCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.creates)
}

func (f *fakeYouTubeEditor) cancelled() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cancels...)
}

// recurringFixture is a server whose YouTube provider is the fake, with a YouTube destination on a
// weekly Tuesday 19:00 UTC start schedule and a template attached to it. now is a Monday, so the
// next two occurrences are the 3rd and the 10th.
type recurringFixture struct {
	s        *Server
	store    *db.DB
	fake     *fakeYouTubeEditor
	dest     *db.Destination
	schedule int64
	now      time.Time
}

func newRecurringFixture(t *testing.T) *recurringFixture {
	t.Helper()
	stub := newPlatformStub(t)
	fake := &fakeYouTubeEditor{YouTube: oauth.NewYouTube(oauth.WithBaseURL(stub.URL))}
	set := stub.set().With(db.PlatformYouTube, fake)
	s, _, store := testServerWith(t, Options{Config: config.Config{}, Providers: set})
	seedCreds(t, s, store, db.PlatformYouTube)
	d := seedDestination(t, s, store, db.PlatformYouTube, "yt")
	tuesday := time.Date(2026, 11, 3, 19, 0, 0, 0, time.UTC)
	schedID := seedStartSchedule(t, store, scheduler.KindWeekly, tuesday, d.ID)
	return &recurringFixture{
		s: s, store: store, fake: fake, dest: d, schedule: schedID,
		now: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC),
	}
}

func (f *recurringFixture) attachTemplate(t *testing.T, mut ...func(*scheduler.BroadcastTemplate)) {
	t.Helper()
	tpl := scheduler.BroadcastTemplate{
		ScheduleID:   f.schedule,
		TitlePattern: "Tuesday live {date} at {time}",
		Description:  "Every week at seven.",
		Lookahead:    2,
	}
	for _, m := range mut {
		m(&tpl)
	}
	if _, err := f.store.SaveBroadcastTemplate(tpl); err != nil {
		t.Fatalf("save template: %v", err)
	}
}

func (f *recurringFixture) sweep(t *testing.T) {
	t.Helper()
	f.s.recurringOnce(context.Background(), f.now)
}

// The look-ahead is the feature: the next N occurrences exist as platform events before the show,
// each with its own rendered title, and the destination's go-live mirror points at the NEAREST one.
func TestTheLookaheadCreatesTheNextNOccurrencesWithTheirRenderedTitles(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)

	f.sweep(t)

	if got := f.fake.createCount(); got != 2 {
		t.Fatalf("created %d events, want 2 (the look-ahead)", got)
	}
	wantAt := []time.Time{
		time.Date(2026, 11, 3, 19, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 10, 19, 0, 0, 0, time.UTC),
	}
	for i, opts := range f.fake.creates {
		if !opts.ScheduledFor.Equal(wantAt[i]) {
			t.Fatalf("create %d scheduled for %v, want %v", i, opts.ScheduledFor, wantAt[i])
		}
	}
	wantTitles := []string{"Tuesday live 2026-11-03 at 19:00", "Tuesday live 2026-11-10 at 19:00"}
	for i, e := range f.fake.edits {
		if e.Title != wantTitles[i] {
			t.Fatalf("edit %d title = %q, want %q", i, e.Title, wantTitles[i])
		}
		if e.Description != "Every week at seven." {
			t.Fatalf("edit %d description = %q, want the template's", i, e.Description)
		}
	}
	rows, err := f.store.ListOpenOccurrences()
	if err != nil {
		t.Fatalf("ListOpenOccurrences: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("recorded %d occurrences, want 2", len(rows))
	}
	for _, r := range rows {
		if r.State != scheduler.OccurrenceUpcoming || r.PlatformEventID == "" {
			t.Fatalf("occurrence %d is %q with event %q, want upcoming with an event id", r.ID, r.State, r.PlatformEventID)
		}
	}

	// THE GO-LIVE BINDING. The lifecycle reads the destination's announcement mirror, so the mirror
	// must name the broadcast for the NEXT show, not the one after it.
	got, err := f.store.GetDestination(f.dest.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.Facebook.BroadcastID != "yt-occ-1" {
		t.Fatalf("go-live mirror names %q, want the nearest occurrence's broadcast yt-occ-1", got.Facebook.BroadcastID)
	}
}

// Every event gets the template's thumbnail, because the picture is part of what the show looks like
// before it starts. A thumbnail that failed to attach is logged and does not undo the event.
func TestEveryCreatedEventGetsTheTemplatesThumbnail(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	if err := f.store.SetBroadcastTemplateThumbnail(f.schedule, []byte("\x89PNG-fake"), "image/png"); err != nil {
		t.Fatalf("thumbnail: %v", err)
	}

	f.sweep(t)

	if len(f.fake.thumbs) != 2 {
		t.Fatalf("thumbnails attached to %d events, want 2", len(f.fake.thumbs))
	}
}

// A second sweep over the same window creates nothing. The occurrence rows are the record, so a
// sweep that runs again (every five minutes) must not mint a second public event page per show.
func TestASecondSweepCreatesNothingForOccurrencesAlreadyOnRecord(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	f.sweep(t)
	f.sweep(t)

	if got := f.fake.createCount(); got != 2 {
		t.Fatalf("two sweeps created %d events, want 2", got)
	}
}

// AN ENABLED DESTINATION IS OUT OF SCOPE. Creating an event may issue a new stream key, and the
// destination is publishing with the old one. The look-ahead must not touch it, the same rule the
// legacy pre-announce follows.
func TestAnEnabledDestinationGetsNoLookahead(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	if err := f.store.SetDestinationEnabled(f.dest.ID, true); err != nil {
		t.Fatalf("enable destination: %v", err)
	}

	f.sweep(t)

	if got := f.fake.createCount(); got != 0 {
		t.Fatalf("an enabled destination got %d events; it must get none", got)
	}
}

// A title the platform refuses must not leave a public event carrying the placeholder. The event is
// cancelled and the claim released, so the next sweep tries again instead of trusting a broken event.
func TestAFailedTitleWriteCancelsTheEventInsteadOfPublishingAPlaceholder(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	f.fake.editErr = fmt.Errorf("YouTube refused the title")

	f.sweep(t)

	if got := f.fake.cancelled(); len(got) != 2 {
		t.Fatalf("cancelled %d events after failed title writes, want both cancelled", len(got))
	}
	rows, err := f.store.ListOpenOccurrences()
	if err != nil {
		t.Fatalf("ListOpenOccurrences: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("%d occurrence rows survive a cancelled create; the claims must be released", len(rows))
	}
}

// Skipping one occurrence deletes its platform event and keeps the row, so the next sweep, over the
// same window, does not create it again. The window itself is unchanged, so nothing else is created.
func TestSkippingAnOccurrenceDeletesItsEventAndIsNotRecreated(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	f.sweep(t)
	rows, err := f.store.ListOpenOccurrences()
	if err != nil {
		t.Fatalf("ListOpenOccurrences: %v", err)
	}
	first := rows[0]

	if err := f.s.skipOccurrence(context.Background(), first.ID); err != nil {
		t.Fatalf("skipOccurrence: %v", err)
	}
	if got := f.fake.cancelled(); len(got) != 1 || got[0] != first.PlatformEventID {
		t.Fatalf("skip cancelled %v, want exactly the skipped event %s", got, first.PlatformEventID)
	}
	f.sweep(t)
	// The skip frees its slot, so the sweep backfills it with the occurrence after the window (see
	// TestASkippedOccurrenceIsBackfilledByTheNextOneAndNotRecreated). The skipped one is not recreated:
	// the only new create is the 17th.
	if got := f.fake.createCount(); got != 3 {
		t.Fatalf("after a skip a sweep created %d events in total, want 3: two, then the backfill", got)
	}
	if last := f.fake.creates[len(f.fake.creates)-1]; last.ScheduledFor.Day() != 17 {
		t.Fatalf("the sweep after a skip recreated occurrence %v; it must backfill the 17th", last.ScheduledFor)
	}
}

// Skipping the nearest show clears the go-live mirror, because the mirror would otherwise point at an
// event that no longer exists. The show's stream then has nothing to bind until the next sweep.
func TestSkippingTheNearestShowClearsTheGoLiveMirror(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	f.sweep(t)
	rows, err := f.store.ListOpenOccurrences()
	if err != nil {
		t.Fatalf("ListOpenOccurrences: %v", err)
	}
	if err := f.s.skipOccurrence(context.Background(), rows[0].ID); err != nil {
		t.Fatalf("skipOccurrence: %v", err)
	}
	got, err := f.store.GetDestination(f.dest.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.Facebook.BroadcastID == rows[0].PlatformEventID {
		t.Fatalf("the go-live mirror still names the skipped event %s", rows[0].PlatformEventID)
	}
}

// Deleting the template cancels every upcoming event it made. The schedule itself is untouched, so
// the show still runs; only the public event pages go.
func TestDeletingTheTemplateCancelsItsUpcomingEvents(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	f.sweep(t)

	if err := f.store.DeleteBroadcastTemplate(f.schedule); err != nil {
		t.Fatalf("DeleteBroadcastTemplate: %v", err)
	}
	f.sweep(t)

	if got := f.fake.cancelled(); len(got) != 2 {
		t.Fatalf("deleting the template cancelled %d events, want both upcoming ones", len(got))
	}
	rows, err := f.store.ListOpenOccurrences()
	if err != nil {
		t.Fatalf("ListOpenOccurrences: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("%d occurrence rows survive the template they came from", len(rows))
	}
}

// THE TWO SWEEPS MUST NOT BOTH CREATE. The legacy pre-announce would create the same YouTube event
// the template sweep already made. A templated schedule on a scheduled-editor platform is left to
// the template sweep alone.
func TestThePreannounceSweepLeavesTemplatedSchedulesToTheTemplateSweep(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	before := f.fake.createCount()

	// No template sweep has run yet, so the go-live mirror is still empty. The legacy path would
	// create here unless the guard stops it, which is the case this test exists to pin.
	f.s.preannounceOnce(context.Background(), f.now)

	if got := f.fake.createCount(); got != before {
		t.Fatalf("pre-announce created %d events on a templated schedule; it must create none", got-before)
	}
}

// A template whose schedule was deleted is an orphan. Its events are cancelled and its rows removed,
// so a deleted show does not leave public event pages behind.
func TestAnOrphanedTemplateIsCancelledWhenItsScheduleIsDeleted(t *testing.T) {
	f := newRecurringFixture(t)
	f.attachTemplate(t)
	f.sweep(t)

	if err := f.store.DeleteSchedule(f.schedule); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
	f.sweep(t)

	if got := f.fake.cancelled(); len(got) != 2 {
		t.Fatalf("deleting the schedule cancelled %d events, want both", len(got))
	}
}

// A destination with no scheduled-editor capability (Facebook today, which is PROVISIONAL) is not
// given template events. An event with no title would be the placeholder problem again.
func TestADestinationWithoutAScheduledEditorGetsNoTemplateEvents(t *testing.T) {
	stub := newPlatformStub(t)
	s, _, store := testServerWith(t, Options{Config: config.Config{}, Providers: stub.set()})
	seedCreds(t, s, store, db.PlatformFacebook)
	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	schedID := seedStartSchedule(t, store, scheduler.KindWeekly, time.Date(2026, 11, 3, 19, 0, 0, 0, time.UTC), d.ID)
	if _, err := store.SaveBroadcastTemplate(scheduler.BroadcastTemplate{
		ScheduleID: schedID, TitlePattern: "Show {date}",
	}); err != nil {
		t.Fatalf("save template: %v", err)
	}
	s.recurringOnce(context.Background(), time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))

	rows, err := store.ListOpenOccurrences()
	if err != nil {
		t.Fatalf("ListOpenOccurrences: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("a Facebook destination with no scheduled editor got %d template rows", len(rows))
	}
}
