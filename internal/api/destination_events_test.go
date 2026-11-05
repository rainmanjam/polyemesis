package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// #1 of the 2026-11-04 lingering-state audit: deleting a destination left its
// upcoming platform events up. The occurrence rows name a destination and no
// account, so once the row was gone nothing could cancel them, and the sweep
// retried a call that could never succeed until the row aged out 30 days after
// the show.
//
// These go through the real DELETE routes, which need an engine manager (the
// delete cuts a broadcast delay through it), so the recurring fixture is
// rebuilt on engineServer with the same fake YouTube editor.

type deleteEventsFixture struct {
	*recurringFixture
	h    http.Handler
	sign func(*http.Request)
}

// newDeleteEventsFixture is newRecurringFixture on a server with a manager.
// The schedule is two hours from NOW, weekly, because the delete handler reads
// the wall clock: an occurrence the fixture created for a fixed date would be
// in the past on the day the suite runs, and a past show is refused, not
// cancelled.
func newDeleteEventsFixture(t *testing.T) *deleteEventsFixture {
	t.Helper()
	stub := newPlatformStub(t)
	fake := &fakeYouTubeEditor{YouTube: oauth.NewYouTube(oauth.WithBaseURL(stub.URL))}
	set := stub.set().With(db.PlatformYouTube, fake)
	s, h, store, sign := engineServer(t, defaultTools(), Options{Providers: set})
	seedCreds(t, s, store, db.PlatformYouTube)
	d := seedDestination(t, s, store, db.PlatformYouTube, "yt")
	now := time.Now().UTC().Truncate(time.Minute)
	schedID := seedStartSchedule(t, store, scheduler.KindWeekly, now.Add(2*time.Hour), d.ID)
	return &deleteEventsFixture{
		recurringFixture: &recurringFixture{s: s, store: store, fake: fake, dest: d, schedule: schedID, now: now},
		h:                h, sign: sign,
	}
}

func (f *deleteEventsFixture) openEventIDs(t *testing.T) []string {
	t.Helper()
	rows, err := f.store.ListOpenOccurrences()
	if err != nil {
		t.Fatalf("ListOpenOccurrences: %v", err)
	}
	var ids []string
	for _, o := range rows {
		ids = append(ids, o.PlatformEventID)
	}
	slices.Sort(ids)
	return ids
}

type deleteReply struct {
	Status   string   `json:"status"`
	Warnings []string `json:"warnings"`
}

func TestDeletingADestinationCancelsItsUpcomingEvents(t *testing.T) {
	f := newDeleteEventsFixture(t)
	f.attachTemplate(t)
	f.sweep(t)
	before := f.openEventIDs(t)
	if len(before) != 2 {
		t.Fatalf("the look-ahead created %v, want two events to cancel", before)
	}

	var got deleteReply
	decodeInto(t, send(t, f.h, f.sign, http.MethodDelete, "/api/v1/destinations/"+itoa(f.dest.ID), nil,
		http.StatusOK), &got)

	cancelled := f.fake.cancelled()
	slices.Sort(cancelled)
	if !slices.Equal(cancelled, before) {
		t.Fatalf("the delete cancelled %v on the platform, want %v -- the events stay up announcing "+
			"shows no destination will air", cancelled, before)
	}
	if left := f.openEventIDs(t); len(left) != 0 {
		t.Errorf("occurrence rows survive the delete: %v", left)
	}
	// The only thing left to say is #10's: the schedule that named only this
	// destination was switched off. Nothing about an event left behind.
	for _, w := range got.Warnings {
		if strings.Contains(w, "scheduled event") {
			t.Errorf("a clean cancel reported an event left behind: %q", w)
		}
	}
}

// A platform that refuses never blocks the delete; the operator is told which
// event is left, and the sweep drops the row once rather than retrying for ever.
func TestARefusedCancelStillDeletesAndNamesTheEventLeftBehind(t *testing.T) {
	f := newDeleteEventsFixture(t)
	f.attachTemplate(t)
	f.sweep(t)
	f.fake.cancelErr = errors.New("youtube said no")

	var got deleteReply
	decodeInto(t, send(t, f.h, f.sign, http.MethodDelete, "/api/v1/destinations/"+itoa(f.dest.ID), nil,
		http.StatusOK), &got)
	if _, err := f.store.GetDestination(f.dest.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("a refused platform cancel kept the destination: %v", err)
	}
	joined := strings.Join(got.Warnings, "\n")
	if !strings.Contains(joined, "yt-occ-1") || !strings.Contains(joined, "YouTube") {
		t.Fatalf("warnings %q do not name the event left on YouTube", got.Warnings)
	}

	calls := len(f.fake.cancelled())
	f.sweep(t)
	if left := f.openEventIDs(t); len(left) != 0 {
		t.Errorf("the sweep kept rows whose destination is gone: %v", left)
	}
	if n := len(f.fake.cancelled()); n != calls {
		t.Errorf("the sweep called the platform %d more time(s) for a destination with no account left", n-calls)
	}
}

// The programme delete cascades to its destinations in SQL, where no Go code
// sees them go. Their events are cancelled before it.
func TestDeletingAProgrammeCancelsItsDestinationsEvents(t *testing.T) {
	f := newDeleteEventsFixture(t)
	src := &db.Source{Name: "Studio B", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := f.store.CreateSource(src); err != nil {
		t.Fatal(err)
	}
	acct := connectAccount(t, f.store, f.s.box, db.PlatformYouTube, "studio-b")
	d, err := f.store.CreateDestination(&db.Destination{Name: "yt b", Kind: "rtmp", Platform: db.PlatformYouTube,
		URL: "rtmps://live.example/rtmp", StreamKey: "k", AccountID: &acct, SourceID: &src.ID})
	if err != nil {
		t.Fatal(err)
	}
	f.schedule = seedStartSchedule(t, f.store, scheduler.KindWeekly, f.now.Add(3*time.Hour), d.ID)
	f.attachTemplate(t)
	f.sweep(t)
	before := f.openEventIDs(t)
	if len(before) != 2 {
		t.Fatalf("look-ahead on the second programme created %v, want two", before)
	}

	// 200, not 204, only because the schedule that named this programme's one
	// destination was switched off (#10) and that is said. No event is left.
	var got deleteReply
	decodeInto(t, send(t, f.h, f.sign, http.MethodDelete, "/api/v1/sources/"+itoa(src.ID),
		map[string]any{"confirm": true, "destinations": 1}, http.StatusOK), &got)
	for _, w := range got.Warnings {
		if strings.Contains(w, "scheduled event") {
			t.Errorf("the programme delete reported an event left behind: %q", w)
		}
	}
	cancelled := f.fake.cancelled()
	slices.Sort(cancelled)
	if !slices.Equal(cancelled, before) {
		t.Fatalf("the programme delete cancelled %v, want %v", cancelled, before)
	}
}

// A show that already aired keeps its row `upcoming` for 30 days
// (occurrenceRetention). The delete leaves it alone, as it leaves a past
// pre-announce marker, and says nothing about it: before, every recent show was
// reported "left on YouTube (start time has passed)".
func TestADestinationDeleteLeavesAShowThatAlreadyAiredAlone(t *testing.T) {
	f := newDeleteEventsFixture(t)
	f.attachTemplate(t)
	f.sweep(t)
	upcoming := f.openEventIDs(t)
	if len(upcoming) != 2 {
		t.Fatalf("the look-ahead created %v, want two upcoming events", upcoming)
	}
	aired, err := f.store.ClaimOccurrence(scheduler.Occurrence{ScheduleID: f.schedule, DestinationID: f.dest.ID,
		At: time.Now().Add(-2 * time.Hour), State: scheduler.OccurrencePending})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.ResolveOccurrence(aired, "yt-aired-1", "Last week"); err != nil {
		t.Fatal(err)
	}

	var got deleteReply
	decodeInto(t, send(t, f.h, f.sign, http.MethodDelete, "/api/v1/destinations/"+itoa(f.dest.ID), nil,
		http.StatusOK), &got)
	for _, w := range got.Warnings {
		if strings.Contains(w, "yt-aired-1") || strings.Contains(w, "has passed") {
			t.Errorf("the delete reported a show that already aired: %q", w)
		}
	}
	cancelled := f.fake.cancelled()
	slices.Sort(cancelled)
	if !slices.Equal(cancelled, upcoming) {
		t.Errorf("the delete cancelled %v, want only the upcoming %v", cancelled, upcoming)
	}
}

// A programme delete takes its renditions with it by CASCADE, and a public
// playout rung naming one stops starting. The delete says so in its response,
// where it used to be a log line under a 204. A programme that is not the
// published one says nothing about the public page.
func TestAProgrammeDeleteNamesThePlayoutRungsItStrands(t *testing.T) {
	f := newDeleteEventsFixture(t)
	src := &db.Source{Name: "Studio B", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := f.store.CreateSource(src); err != nil {
		t.Fatal(err)
	}
	rend, err := f.store.CreateRendition(&db.Rendition{Name: "b-720", Width: 1280, Height: 720, FPS: 30,
		VideoBitrate: 2500, GOPSeconds: 2, SourceID: &src.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpdateSettings(func(s *db.Settings) error {
		s.Playout.Variants = []db.PlayoutVariant{{Name: "720p", Enabled: true, RenditionID: &rend.ID}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var got deleteReply
	decodeInto(t, send(t, f.h, f.sign, http.MethodDelete, "/api/v1/sources/"+itoa(src.ID),
		map[string]any{"confirm": true, "destinations": 0}, http.StatusOK), &got)
	joined := strings.Join(got.Warnings, "\n")
	if !strings.Contains(joined, "1 playout rung named a rendition of the deleted programme") ||
		!strings.Contains(joined, `"720p"`) {
		t.Errorf("warnings %q do not name the stranded rung", got.Warnings)
	}
	if strings.Contains(joined, "public playout page") || strings.Contains(joined, "Public playout") {
		t.Errorf("a programme nobody published was reported as the published one: %q", got.Warnings)
	}
}

// The programme delete's id check, through the route. While the delete is on
// the platform cancelling events, one of the programme's destinations is
// deleted and another created: the count the operator confirmed is still the
// count, and only the ids say the new destination was never confirmed. It must
// be refused, not swept away with the programme.
func TestAProgrammeDeleteIsRefusedWhenADestinationIsSwappedDuringIt(t *testing.T) {
	f := newDeleteEventsFixture(t)
	src := &db.Source{Name: "Studio B", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := f.store.CreateSource(src); err != nil {
		t.Fatal(err)
	}
	acct := connectAccount(t, f.store, f.s.box, db.PlatformYouTube, "studio-b")
	mk := func(name string) *db.Destination {
		t.Helper()
		d, err := f.store.CreateDestination(&db.Destination{Name: name, Kind: "rtmp", Platform: db.PlatformYouTube,
			URL: "rtmps://live.example/rtmp", StreamKey: "k", AccountID: &acct, SourceID: &src.ID})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	withEvents := mk("yt b")
	other := mk("yt b2")
	f.schedule = seedStartSchedule(t, f.store, scheduler.KindWeekly, f.now.Add(3*time.Hour), withEvents.ID)
	f.attachTemplate(t)
	f.sweep(t)

	var swapped *db.Destination
	f.fake.onCancel = func() {
		if swapped != nil {
			return
		}
		if err := f.store.DeleteDestination(other.ID); err != nil {
			t.Errorf("swap: delete: %v", err)
		}
		swapped = mk("created mid-delete")
	}
	body := send(t, f.h, f.sign, http.MethodDelete, "/api/v1/sources/"+itoa(src.ID),
		map[string]any{"confirm": true, "destinations": 2}, http.StatusConflict)
	if !strings.Contains(string(body), "changed while it was being deleted") {
		t.Errorf("the refusal %q does not say the destinations changed", body)
	}
	if swapped == nil {
		t.Fatal("no cancel ran, so nothing was swapped: the fixture is not testing what it means to")
	}
	if _, err := f.store.GetSource(src.ID); err != nil {
		t.Fatalf("the refused delete removed the programme: %v", err)
	}
	if _, err := f.store.GetDestination(swapped.ID); err != nil {
		t.Fatalf("a destination nobody confirmed went with the programme: %v", err)
	}
}

// The fallback for what the delete could not reach. A row for a destination
// that no longer exists, on a schedule that would otherwise keep it (every
// destination, template present), is dropped by one sweep and the platform is
// not called: there is no account to call it with.
func TestTheSweepDropsAnEventWhoseDestinationIsGone(t *testing.T) {
	f := newRecurringFixture(t)
	all := seedStartSchedule(t, f.store, scheduler.KindWeekly, time.Date(2026, 11, 4, 19, 0, 0, 0, time.UTC))
	if _, err := f.store.SaveBroadcastTemplate(scheduler.BroadcastTemplate{ScheduleID: all,
		TitlePattern: "Wednesday", Lookahead: 2}); err != nil {
		t.Fatal(err)
	}
	ghost := f.dest.ID + 1000
	id, err := f.store.ClaimOccurrence(scheduler.Occurrence{ScheduleID: all, DestinationID: ghost,
		At: f.now.Add(50 * time.Hour), State: scheduler.OccurrencePending})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.ResolveOccurrence(id, "yt-ghost-1", "t"); err != nil {
		t.Fatal(err)
	}

	f.sweep(t)
	rows, err := f.store.ListOpenOccurrences()
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range rows {
		if o.PlatformEventID == "yt-ghost-1" {
			t.Fatal("the row of a deleted destination survives the sweep, which retries it every pass until it ages out")
		}
	}
	if slices.Contains(f.fake.cancelled(), "yt-ghost-1") {
		t.Error("the sweep asked the platform to cancel an event it has no account for")
	}
}

// The legacy pre-announce markers live on the destination row itself. YouTube
// can cancel its scheduled event; Facebook offers no call, so the event is
// named for the operator.
func TestDeletingADestinationWithdrawsItsPreAnnouncedEvents(t *testing.T) {
	f := newDeleteEventsFixture(t)
	fb := seedDestination(t, f.s, f.store, db.PlatformFacebook, "fb")
	future := f.now.Add(48 * time.Hour)
	mark := func(d *db.Destination, id string) {
		t.Helper()
		if _, err := f.store.UpdateAnnouncement(d.ID, func(cur *db.Destination) bool {
			cur.Facebook.Announce(f.schedule, future, id, f.now)
			return true
		}); err != nil {
			t.Fatal(err)
		}
	}
	mark(f.dest, "yt-legacy-1")
	mark(fb, "fb-video-1")

	var yt deleteReply
	decodeInto(t, send(t, f.h, f.sign, http.MethodDelete, "/api/v1/destinations/"+itoa(f.dest.ID), nil,
		http.StatusOK), &yt)
	if got := f.fake.cancelled(); !slices.Equal(got, []string{"yt-legacy-1"}) {
		t.Errorf("deleting the YouTube destination cancelled %v, want its pre-announced yt-legacy-1", got)
	}

	var fbReply deleteReply
	decodeInto(t, send(t, f.h, f.sign, http.MethodDelete, "/api/v1/destinations/"+itoa(fb.ID), nil,
		http.StatusOK), &fbReply)
	joined := strings.Join(fbReply.Warnings, "\n")
	if !strings.Contains(joined, "fb-video-1") || !strings.Contains(joined, "Facebook") {
		t.Fatalf("deleting the Facebook destination said %q; it must name the event left on Facebook", fbReply.Warnings)
	}
}

// Part B: the legacy path for a schedule with no template. Deleting the
// schedule withdraws what it pre-announced.
func TestDeletingAScheduleWithdrawsItsPreAnnouncedEvents(t *testing.T) {
	f := newRecurringFixture(t)
	fb := seedDestination(t, f.s, f.store, db.PlatformFacebook, "fb")
	sc, err := f.store.GetSchedule(f.schedule)
	if err != nil {
		t.Fatal(err)
	}
	sc.DestinationIDs = []int64{f.dest.ID, fb.ID}
	if _, err := f.store.UpdateSchedule(sc); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(48 * time.Hour)
	for _, m := range []struct {
		d  *db.Destination
		id string
	}{{f.dest, "yt-legacy-2"}, {fb, "fb-video-2"}} {
		if _, err := f.store.UpdateAnnouncement(m.d.ID, func(cur *db.Destination) bool {
			cur.Facebook.Announce(f.schedule, future, m.id, time.Now())
			return true
		}); err != nil {
			t.Fatal(err)
		}
	}
	h := f.s.Handler()
	sign := login(t, h)

	var got deleteReply
	decodeInto(t, send(t, h, sign, http.MethodDelete, "/api/v1/schedules/"+strconv.FormatInt(f.schedule, 10),
		nil, http.StatusOK), &got)

	if c := f.fake.cancelled(); !slices.Equal(c, []string{"yt-legacy-2"}) {
		t.Errorf("deleting the schedule cancelled %v on YouTube, want yt-legacy-2", c)
	}
	if !strings.Contains(strings.Join(got.Warnings, "\n"), "fb-video-2") {
		t.Errorf("the schedule delete said %q; it must name the Facebook event left behind", got.Warnings)
	}
	for _, id := range []int64{f.dest.ID, fb.ID} {
		d, err := f.store.GetDestination(id)
		if err != nil {
			t.Fatal(err)
		}
		if bs := d.Facebook.Broadcasts(); len(bs) != 0 {
			t.Errorf("destination %d still carries the deleted schedule's marker %+v, which suppresses a "+
				"replacement show at the same instant", id, bs)
		}
	}
}

// Switching the schedule off is withdrawn by the sweep on its next pass, the
// same as a delete. A marker with no schedule id (an older row's) is not.
func TestTheSweepWithdrawsTheEventsOfASwitchedOffSchedule(t *testing.T) {
	f := newRecurringFixture(t)
	sc, err := f.store.GetSchedule(f.schedule)
	if err != nil {
		t.Fatal(err)
	}
	sc.Enabled = false
	if _, err := f.store.UpdateSchedule(sc); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := f.store.UpdateAnnouncement(f.dest.ID, func(cur *db.Destination) bool {
		cur.Facebook.Announce(f.schedule, now.Add(48*time.Hour), "yt-legacy-3", now)
		cur.Facebook.Announce(0, now.Add(96*time.Hour), "yt-adoptable", now)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	f.s.preannounceOnce(context.Background(), now)
	if c := f.fake.cancelled(); !slices.Equal(c, []string{"yt-legacy-3"}) {
		t.Fatalf("the sweep cancelled %v, want only the switched-off schedule's yt-legacy-3", c)
	}
}
