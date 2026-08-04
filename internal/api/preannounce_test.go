package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// These drive s.preannounceOnce directly rather than through a handler, which
// is why testServer's nil engine is fine here: nothing reaches refuseIfSilent.
//
// They replace ingestForFn and rescheduleFn rather than stubbing Graph, because
// internal/oauth's graph base is unexported -- the same reason those seams
// exist at all. See the comments on them in api.go.

// announced is what the seams recorded, so each test asserts on what the sweep
// DID rather than on it having run.
type announced struct {
	creates     []oauth.IngestOptions
	reschedules []time.Time
	key         string
	broadcastID string
	err         error
}

func stubAnnounce(s *Server, rec *announced) {
	s.ingestForFn = func(ctx context.Context, p oauth.Provider, clientID string,
		acct *db.PlatformAccount, opts oauth.IngestOptions) (*oauth.Ingest, string, error) {
		rec.creates = append(rec.creates, opts)
		if rec.err != nil {
			return nil, "", rec.err
		}
		return &oauth.Ingest{URL: "rtmps://live.example/rtmp", Key: rec.key}, rec.broadcastID, nil
	}
	s.rescheduleFn = func(ctx context.Context, acct *db.PlatformAccount,
		broadcastID string, at time.Time) error {
		rec.reschedules = append(rec.reschedules, at)
		return rec.err
	}
}

func seedDestination(t *testing.T, s *Server, store *db.DB, platform db.Platform, name string) *db.Destination {
	t.Helper()
	acctID := connectAccount(t, store, s.box, platform, name)
	d, err := store.CreateDestination(&db.Destination{
		Name: name, Kind: "rtmp", Platform: platform,
		URL: "rtmps://live.example/rtmp", StreamKey: "original-key",
		AccountID: &acctID,
	})
	if err != nil {
		t.Fatalf("create %s destination: %v", platform, err)
	}
	return d
}

// Developer credentials, because announceOne reads them for the client id and
// returns early without them -- a sweep with none would silently do nothing and
// every test here would pass for the wrong reason.
func seedCreds(t *testing.T, s *Server, store *db.DB, platform db.Platform) {
	t.Helper()
	if err := store.PutPlatformCreds(s.box, platform, "client-id", "client-secret"); err != nil {
		t.Fatalf("put %s creds: %v", platform, err)
	}
}

func seedStartSchedule(t *testing.T, store *db.DB, kind scheduler.Kind, at time.Time, destIDs ...int64) {
	t.Helper()
	sc := &scheduler.Schedule{
		Name: "show", Enabled: true, Action: scheduler.ActionStart, Kind: kind,
		DestinationIDs: destIDs, TZ: "UTC",
	}
	switch kind {
	case scheduler.KindOnce:
		sc.RunAt = at
	default:
		sc.AtMinutes = at.UTC().Hour()*60 + at.UTC().Minute()
		sc.Days = []time.Weekday{at.UTC().Weekday()}
	}
	if _, err := store.CreateSchedule(sc); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
}

func TestASchedulesNextOccurrenceGetsAnEventPage(t *testing.T) {
	s, _, store := testServer(t, config.Config{})
	rec := &announced{key: "key-from-the-broadcast", broadcastID: "777"}
	stubAnnounce(s, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	at := time.Now().Add(3 * 24 * time.Hour).Truncate(time.Second)
	seedStartSchedule(t, store, scheduler.KindOnce, at, d.ID)

	s.preannounceOnce(context.Background(), time.Now())

	if len(rec.creates) != 1 {
		t.Fatalf("created %d broadcasts, want 1", len(rec.creates))
	}
	// Asserted on the OPTION, because that is what carries the schedule into
	// the Graph call. A create with a zero ScheduledFor is a LIVE_NOW broadcast
	// and no event page at all.
	if !rec.creates[0].ScheduledFor.Equal(at) {
		t.Errorf("ScheduledFor = %v, want the occurrence %v", rec.creates[0].ScheduledFor, at)
	}
	got, err := store.GetDestination(d.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.Facebook.BroadcastID != "777" {
		t.Errorf("BroadcastID = %q, want 777", got.Facebook.BroadcastID)
	}
	if !got.Facebook.ScheduledFor.Equal(at) {
		t.Errorf("marker = %v, want %v", got.Facebook.ScheduledFor, at)
	}
}

// THE INVARIANT. If the stored key is not the pre-created broadcast's key, the
// event page people were notified about stays empty while the stream goes
// somewhere else.
func TestThePreCreatedKeyIsWrittenToTheDestination(t *testing.T) {
	s, _, store := testServer(t, config.Config{})
	rec := &announced{key: "key-from-the-broadcast", broadcastID: "777"}
	stubAnnounce(s, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	seedStartSchedule(t, store, scheduler.KindOnce, time.Now().Add(2*24*time.Hour), d.ID)

	s.preannounceOnce(context.Background(), time.Now())

	got, _ := store.GetDestination(d.ID)
	if got.StreamKey != "key-from-the-broadcast" {
		t.Fatalf("StreamKey = %q, want the pre-created broadcast's key. The encoder "+
			"would publish somewhere the announced event page is not.", got.StreamKey)
	}
}

// The case a boolean marker gets wrong in one direction.
func TestASecondSweepForTheSameOccurrenceCreatesNothing(t *testing.T) {
	s, _, store := testServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777"}
	stubAnnounce(s, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	seedStartSchedule(t, store, scheduler.KindOnce, time.Now().Add(2*24*time.Hour), d.ID)

	s.preannounceOnce(context.Background(), time.Now())
	s.preannounceOnce(context.Background(), time.Now())

	if len(rec.creates) != 1 {
		t.Fatalf("created %d broadcasts across two sweeps, want 1. At a 5-minute "+
			"tick this is a new public Facebook event every 5 minutes.", len(rec.creates))
	}
	// The creates count ALONE does not catch this. Once a broadcast exists the
	// sweep takes the reschedule branch, so removing the AnnouncedFor skip
	// leaves creates at 1 and quietly re-POSTs the same start time to Facebook
	// every five minutes forever. Measured: that mutation did not turn this
	// test red until this assertion was added.
	if len(rec.reschedules) != 0 {
		t.Fatalf("rescheduled %d times for an occurrence already announced, want 0",
			len(rec.reschedules))
	}
}

// Empty DestinationIDs means every destination, and it is the commonest shape.
func TestAScheduleThatNamesNoDestinationsStillAnnouncesTheFacebookOnes(t *testing.T) {
	s, _, store := testServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777"}
	stubAnnounce(s, rec)

	seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	seedStartSchedule(t, store, scheduler.KindOnce, time.Now().Add(2*24*time.Hour))

	s.preannounceOnce(context.Background(), time.Now())

	if len(rec.creates) != 1 {
		t.Fatalf(`created %d broadcasts, want 1. "Start the show" usually names no `+
			`destinations, so a rule that skipped this shape would switch the `+
			`feature off for most installs.`, len(rec.creates))
	}
}

func TestAnOccurrenceBeyondSevenDaysIsNotAnnounced(t *testing.T) {
	s, _, store := testServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777"}
	stubAnnounce(s, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	seedStartSchedule(t, store, scheduler.KindOnce, time.Now().Add(23*24*time.Hour), d.ID)

	s.preannounceOnce(context.Background(), time.Now())

	if len(rec.creates) != 0 {
		t.Fatalf("created %d broadcasts beyond Facebook's seven-day bound, want 0",
			len(rec.creates))
	}
}

// Best-effort has to be provably best-effort, and the marker must not be
// written for a broadcast that does not exist.
func TestAGraphFailureLeavesTheDestinationUntouched(t *testing.T) {
	s, _, store := testServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777", err: errors.New("graph said no")}
	stubAnnounce(s, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	seedStartSchedule(t, store, scheduler.KindOnce, time.Now().Add(2*24*time.Hour), d.ID)

	s.preannounceOnce(context.Background(), time.Now())

	got, _ := store.GetDestination(d.ID)
	if got.StreamKey != "original-key" {
		t.Errorf("StreamKey changed to %q on a failed create", got.StreamKey)
	}
	if got.Facebook.BroadcastID != "" || !got.Facebook.ScheduledFor.IsZero() {
		t.Error("the marker was written for a broadcast that was never created; " +
			"every later sweep for this occurrence would now be skipped")
	}
}

func TestANonFacebookDestinationOnTheSameScheduleIsUntouched(t *testing.T) {
	s, _, store := testServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777"}
	stubAnnounce(s, rec)

	seedDestination(t, s, store, db.PlatformTwitch, "tw")
	seedCreds(t, s, store, db.PlatformTwitch)
	seedStartSchedule(t, store, scheduler.KindOnce, time.Now().Add(2*24*time.Hour))

	s.preannounceOnce(context.Background(), time.Now())

	if len(rec.creates) != 0 {
		t.Fatalf("created %d broadcasts for a non-Facebook destination, want 0",
			len(rec.creates))
	}
}

// A stop schedule has nothing to announce; an event page for one would
// advertise a show ending.
func TestAStopScheduleAnnouncesNothing(t *testing.T) {
	s, _, store := testServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777"}
	stubAnnounce(s, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	sc := &scheduler.Schedule{
		Name: "off air", Enabled: true, Action: scheduler.ActionStop,
		Kind: scheduler.KindOnce, RunAt: time.Now().Add(2 * 24 * time.Hour),
		DestinationIDs: []int64{d.ID}, TZ: "UTC",
	}
	if _, err := store.CreateSchedule(sc); err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	s.preannounceOnce(context.Background(), time.Now())

	if len(rec.creates) != 0 {
		t.Fatalf("created %d broadcasts for a STOP schedule, want 0", len(rec.creates))
	}
}

// The case a boolean marker gets wrong in the OTHER direction: next week needs
// its own start time, and it must MOVE the existing broadcast rather than
// create a second one. A second create leaves the first as a public event page
// people are still subscribed to.
func TestTheNextOccurrenceMovesTheBroadcastRatherThanDuplicatingIt(t *testing.T) {
	s, _, store := testServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777"}
	stubAnnounce(s, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	now := time.Now()
	seedStartSchedule(t, store, scheduler.KindWeekly, now.Add(24*time.Hour), d.ID)

	s.preannounceOnce(context.Background(), now)
	// A week on, Next() returns a different occurrence, so the marker no
	// longer matches and the sweep must act again.
	s.preannounceOnce(context.Background(), now.Add(8*24*time.Hour))

	if len(rec.creates) != 1 {
		t.Errorf("created %d broadcasts, want 1 -- the second occurrence must MOVE "+
			"the first, not orphan it", len(rec.creates))
	}
	if len(rec.reschedules) != 1 {
		t.Errorf("rescheduled %d times, want 1", len(rec.reschedules))
	}
}

// It WARNS. It never refuses -- the schedule works either way, and what the
// seven-day bound limits is only the pre-announced Facebook event page.
func TestADistantOnceScheduleSavesAndWarnsAboutTheEventPage(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)
	seedDestination(t, s, store, db.PlatformFacebook, "fb")

	var view scheduleView
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/schedules", map[string]any{
		"name": "far off", "action": "start", "kind": "once",
		"runAt": time.Now().Add(23 * 24 * time.Hour).Format(time.RFC3339),
		"tz":    "UTC", "enabled": true,
	}, http.StatusCreated), &view)

	if len(view.Warnings) == 0 {
		t.Fatal("a once schedule 23 days out was saved with no warning; the " +
			"operator has no way to know no event page will exist")
	}
	if !strings.Contains(view.Warnings[0], "seven days") {
		t.Errorf("the warning does not name the limit: %q", view.Warnings[0])
	}
}

// The negative case: inside the window there is nothing to say.
func TestAScheduleInsideTheWindowIsNotWarnedAbout(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)
	seedDestination(t, s, store, db.PlatformFacebook, "fb")

	var view scheduleView
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/schedules", map[string]any{
		"name": "soon", "action": "start", "kind": "once",
		"runAt": time.Now().Add(3 * 24 * time.Hour).Format(time.RFC3339),
		"tz":    "UTC", "enabled": true,
	}, http.StatusCreated), &view)

	if len(view.Warnings) != 0 {
		t.Fatalf("warned about a schedule that will get an event page: %v", view.Warnings)
	}
}

// Daily and weekly CANNOT exceed the bound: the next occurrence of a weekly
// schedule is at most seven days away by definition. Warning on them would be
// noise that teaches people to skip the warning that matters.
func TestAWeeklyScheduleIsNeverWarnedAbout(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)
	seedDestination(t, s, store, db.PlatformFacebook, "fb")

	var view scheduleView
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/schedules", map[string]any{
		"name": "weekly show", "action": "start", "kind": "weekly",
		"atMinutes": 1200, "days": []int{0, 3}, "tz": "UTC", "enabled": true,
	}, http.StatusCreated), &view)

	if len(view.Warnings) != 0 {
		t.Fatalf("warned about a weekly schedule, which can never exceed the "+
			"seven-day bound: %v", view.Warnings)
	}
}

// No Facebook destination, nothing to warn about.
func TestADistantScheduleWithNoFacebookDestinationIsNotWarnedAbout(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)
	seedDestination(t, s, store, db.PlatformTwitch, "tw")

	var view scheduleView
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/schedules", map[string]any{
		"name": "far off", "action": "start", "kind": "once",
		"runAt": time.Now().Add(23 * 24 * time.Hour).Format(time.RFC3339),
		"tz":    "UTC", "enabled": true,
	}, http.StatusCreated), &view)

	if len(view.Warnings) != 0 {
		t.Fatalf("warned about a schedule with no Facebook destination: %v", view.Warnings)
	}
}
