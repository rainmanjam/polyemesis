package api

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strconv"
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

// announced configures the stub Graph API the sweep talks to, and reads back
// what the sweep actually sent.
//
// It used to install two closures on Server -- ingestForFn and rescheduleFn --
// which existed for one reason, and both said so: internal/oauth's graph base
// was unexported, so nothing in this package could point a provider at a stub.
// oauth.WithBaseURL is that seam, so the sweep now runs the REAL Facebook
// provider against a real HTTP server, and every assertion below reads the
// Graph request that came out of it. A provider that stopped putting
// event_params on the create fails these tests; a closure that received the
// right oauth.IngestOptions could not have noticed.
type announced struct {
	stub *platformStub
	// key is the stream key inside the ingest URL the create returns.
	key string
	// broadcastID is the live video id every create returns.
	broadcastID string
	// ids, when set, is handed out one per create so a test can tell two event
	// pages apart. Without it every create returns broadcastID, which is fine
	// for the tests that only ever expect one.
	ids []string
	// backups asks the stub to answer with a secondary ingest URL beside the
	// primary, the way Facebook does when backup ingest was requested.
	backups bool
	// err, when set, is the Graph refusal the create and the reschedule both
	// answer with.
	err string
}

func stubAnnounce(stub *platformStub, rec *announced) {
	rec.stub = stub
	stub.fbKey = rec.key
	if rec.broadcastID != "" {
		stub.fbLiveID = rec.broadcastID
	}
	stub.fbLiveIDs = rec.ids
	stub.fbBackups = rec.backups
	stub.fbCreateErr = rec.err
}

// fail and succeed change what Graph answers part-way through a test, which is
// how the consecutive-failure counter is driven. Both go through the stub's
// lock: a sweep can be running in another goroutine.
func (a *announced) fail(msg string) { a.stub.setCreateErr(msg) }
func (a *announced) succeed()        { a.stub.setCreateErr("") }

// onCreate runs inside the Graph call, which is where an operator edit lands in
// production: the sweep read the destination before it and writes after it.
func (a *announced) onCreate(fn func()) { a.stub.setDuringCreate(fn) }

// creates is one entry per live_videos POST, decoded back out of Graph's query
// parameters into the options that produced them.
//
// Decoded rather than recorded, and that is the whole difference from the
// closure this replaced: what these tests now assert on is the wire form that
// reached Facebook, not the struct the caller assembled one layer earlier.
func (a *announced) creates() []oauth.IngestOptions {
	var out []oauth.IngestOptions
	for _, c := range a.stub.calls() {
		if c.Method != http.MethodPost || !strings.HasSuffix(c.Path, "/live_videos") {
			continue
		}
		o := oauth.IngestOptions{BackupIngest: c.Query.Get("enable_backup_ingest") == "true"}
		if secs, err := strconv.ParseInt(c.Query.Get("event_params"), 10, 64); err == nil {
			o.ScheduledFor = time.Unix(secs, 0)
		}
		out = append(out, o)
	}
	return out
}

// reschedules is one entry per POST to a live video NODE carrying a new start
// time. The NODE, not the /live_videos edge: the edge creates a broadcast and
// the node edits one, and a test that could not tell them apart would read a
// duplicated event page as a moved one.
func (a *announced) reschedules() []time.Time {
	var out []time.Time
	for _, c := range a.stub.calls() {
		if c.Method != http.MethodPost || strings.HasSuffix(c.Path, "/live_videos") {
			continue
		}
		if secs, err := strconv.ParseInt(c.Query.Get("event_params"), 10, 64); err == nil {
			out = append(out, time.Unix(secs, 0))
		}
	}
	return out
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

// seedStartSchedule returns the stored schedule's id, because the announcement
// marker is keyed by it: a test that seeds the marker directly, or asserts which
// show a broadcast belongs to, needs the same id the sweep will use.
func seedStartSchedule(t *testing.T, store *db.DB, kind scheduler.Kind, at time.Time, destIDs ...int64) int64 {
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
	stored, err := store.CreateSchedule(sc)
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	return stored.ID
}

// Mutation: replace `cur.StreamKey = b.Ingest.Key` in announceOne with
// `_ = b.Ingest.Key`. Observed FAIL, here and in
// TestThePreCreatedKeyIsWrittenToTheDestination.
func TestASchedulesNextOccurrenceGetsAnEventPage(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "key-from-the-broadcast", broadcastID: "777"}
	stubAnnounce(stub, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	at := time.Now().Add(3 * 24 * time.Hour).Truncate(time.Second)
	seedStartSchedule(t, store, scheduler.KindOnce, at, d.ID)

	s.preannounceOnce(context.Background(), time.Now())

	if len(rec.creates()) != 1 {
		t.Fatalf("created %d broadcasts, want 1", len(rec.creates()))
	}
	// Asserted on the OPTION, because that is what carries the schedule into
	// the Graph call. A create with a zero ScheduledFor is a LIVE_NOW broadcast
	// and no event page at all.
	if !rec.creates()[0].ScheduledFor.Equal(at) {
		t.Errorf("ScheduledFor = %v, want the occurrence %v", rec.creates()[0].ScheduledFor, at)
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
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "key-from-the-broadcast", broadcastID: "777"}
	stubAnnounce(stub, rec)

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
//
// Mutation: `if false && d.Facebook.AnnouncedFor(at)` in preannounceOnce.
// Observed FAIL ("rescheduled 1 times for an occurrence already announced") --
// and note it is the reschedule count that catches it, not the create count.
func TestASecondSweepForTheSameOccurrenceCreatesNothing(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777"}
	stubAnnounce(stub, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	seedStartSchedule(t, store, scheduler.KindOnce, time.Now().Add(2*24*time.Hour), d.ID)

	s.preannounceOnce(context.Background(), time.Now())
	s.preannounceOnce(context.Background(), time.Now())

	if len(rec.creates()) != 1 {
		t.Fatalf("created %d broadcasts across two sweeps, want 1. At a 5-minute "+
			"tick this is a new public Facebook event every 5 minutes.", len(rec.creates()))
	}
	// The creates count ALONE does not catch this. Once a broadcast exists the
	// sweep takes the reschedule branch, so removing the AnnouncedFor skip
	// leaves creates at 1 and quietly re-POSTs the same start time to Facebook
	// every five minutes forever. Measured: that mutation did not turn this
	// test red until this assertion was added.
	if len(rec.reschedules()) != 0 {
		t.Fatalf("rescheduled %d times for an occurrence already announced, want 0",
			len(rec.reschedules()))
	}
}

// Empty DestinationIDs means every destination, and it is the commonest shape.
func TestAScheduleThatNamesNoDestinationsStillAnnouncesTheFacebookOnes(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777"}
	stubAnnounce(stub, rec)

	seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	seedStartSchedule(t, store, scheduler.KindOnce, time.Now().Add(2*24*time.Hour))

	s.preannounceOnce(context.Background(), time.Now())

	if len(rec.creates()) != 1 {
		t.Fatalf(`created %d broadcasts, want 1. "Start the show" usually names no `+
			`destinations, so a rule that skipped this shape would switch the `+
			`feature off for most installs.`, len(rec.creates()))
	}
}

// The bound is the PLATFORM's, read through ScheduledBroadcaster.ScheduleHorizon
// rather than from a constant this package keeps its own copy of.
//
// Mutation: replace `at.Sub(now) > sb.ScheduleHorizon()` in preannounceOnce with
// `at.Sub(now) > 90*24*time.Hour`. Observed FAIL ("created 1 broadcasts beyond
// Facebook's seven-day bound").
func TestAnOccurrenceBeyondSevenDaysIsNotAnnounced(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777"}
	stubAnnounce(stub, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	seedStartSchedule(t, store, scheduler.KindOnce, time.Now().Add(23*24*time.Hour), d.ID)

	s.preannounceOnce(context.Background(), time.Now())

	if len(rec.creates()) != 0 {
		t.Fatalf("created %d broadcasts beyond Facebook's seven-day bound, want 0",
			len(rec.creates()))
	}
}

// Best-effort has to be provably best-effort, and the marker must not be
// written for a broadcast that does not exist.
//
// Mutation: on the create-failure path, replace `cur.Facebook.Forget(sc.ID, "")`
// with `cur.Facebook.Announce(sc.ID, at, "failed-create", now)`. Observed FAIL
// ("the marker was written for a broadcast that was never created").
func TestAGraphFailureLeavesTheDestinationUntouched(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777", err: "graph said no"}
	stubAnnounce(stub, rec)

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

// The gate is the CAPABILITY, not the platform name.
//
// Mutation: replace the `sb, ok := s.providers.ScheduledBroadcastsFor(d.Platform)`
// lookup and its `if !ok { continue }` with
// `sb, _ := s.providers.ScheduledBroadcastsFor(db.PlatformFacebook)`. Observed
// FAIL ("created 1 broadcasts for a non-Facebook destination").
func TestANonFacebookDestinationOnTheSameScheduleIsUntouched(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777"}
	stubAnnounce(stub, rec)

	seedDestination(t, s, store, db.PlatformTwitch, "tw")
	seedCreds(t, s, store, db.PlatformTwitch)
	seedStartSchedule(t, store, scheduler.KindOnce, time.Now().Add(2*24*time.Hour))

	s.preannounceOnce(context.Background(), time.Now())

	if len(rec.creates()) != 0 {
		t.Fatalf("created %d broadcasts for a non-Facebook destination, want 0",
			len(rec.creates()))
	}
}

// A stop schedule has nothing to announce; an event page for one would
// advertise a show ending.
func TestAStopScheduleAnnouncesNothing(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777"}
	stubAnnounce(stub, rec)

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

	if len(rec.creates()) != 0 {
		t.Fatalf("created %d broadcasts for a STOP schedule, want 0", len(rec.creates()))
	}
}

// The case a boolean marker gets wrong in the OTHER direction: next week needs
// its own start time, and it must MOVE the existing broadcast rather than
// create a second one. A second create leaves the first as a public event page
// people are still subscribed to.
// Mutation: `case false && held && prev.BroadcastID != "":` in announceOne, which
// is the reschedule branch switched off. Observed FAIL ("rescheduled 0 times,
// want 1"), and also in TestABroadcastThatWillNotMoveIsEventuallyReplaced.
func TestTheNextOccurrenceMovesTheBroadcastRatherThanDuplicatingIt(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777"}
	stubAnnounce(stub, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	now := time.Now()
	seedStartSchedule(t, store, scheduler.KindWeekly, now.Add(24*time.Hour), d.ID)

	s.preannounceOnce(context.Background(), now)
	// A week on, Next() returns a different occurrence, so the marker no
	// longer matches and the sweep must act again.
	s.preannounceOnce(context.Background(), now.Add(8*24*time.Hour))

	if len(rec.creates()) != 1 {
		t.Errorf("created %d broadcasts, want 1 -- the second occurrence must MOVE "+
			"the first, not orphan it", len(rec.creates()))
	}
	if len(rec.reschedules()) != 1 {
		t.Errorf("rescheduled %d times, want 1", len(rec.reschedules()))
	}
}

// It WARNS. It never refuses -- the schedule works either way, and what the
// seven-day bound limits is only the pre-announced Facebook event page.
//
// The warning and the sweep's decision to skip now read the SAME bound, off the
// destination's own provider, so they cannot disagree. They used to share a
// facebookScheduleHorizon constant and each repeat the platform name beside it.
//
// Mutation: `13*sb.ScheduleHorizon()` in scheduleWarnings. Observed FAIL ("a
// once schedule 23 days out was saved with no warning").
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

// A stop schedule has no event page to be missing, so warning about one would
// be nonsense. This exists because the Action check is the only condition left
// guarding that after the redundant Kind test was removed.
func TestADistantStopScheduleIsNotWarnedAbout(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)
	seedDestination(t, s, store, db.PlatformFacebook, "fb")

	var view scheduleView
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/schedules", map[string]any{
		"name": "far off stop", "action": "stop", "kind": "once",
		"runAt": time.Now().Add(23 * 24 * time.Hour).Format(time.RFC3339),
		"tz":    "UTC", "enabled": true,
	}, http.StatusCreated), &view)

	if len(view.Warnings) != 0 {
		t.Fatalf("warned about a STOP schedule, which never creates an event "+
			"page to be missing: %v", view.Warnings)
	}
}

// The scheduled path creates Facebook broadcasts too, and a refresh-key-only
// implementation loses backup ingest for every pre-announced show -- the
// feature working on the path someone tested and not on the one they forgot.
//
// Mutation: replace `cur.BackupURL, cur.BackupStreamKey = firstBackup(b)` in
// announceOne with `_, _ = firstBackup(b)`. Observed FAIL ("the scheduled path
// did not store the backup endpoint").
func TestTheScheduledPathStoresTheBackupEndpointToo(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "primary-key", broadcastID: "777", backups: true}
	stubAnnounce(stub, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	d.Facebook.BackupIngest = true
	if _, err := store.UpdateDestination(d); err != nil {
		t.Fatalf("enable backup: %v", err)
	}
	seedCreds(t, s, store, db.PlatformFacebook)
	seedStartSchedule(t, store, scheduler.KindOnce, time.Now().Add(2*24*time.Hour), d.ID)

	s.preannounceOnce(context.Background(), time.Now())

	got, _ := store.GetDestination(d.ID)
	if got.BackupURL != "rtmps://backup.example/rtmp" || got.BackupStreamKey != "backup-key" {
		t.Fatalf("the scheduled path did not store the backup endpoint: %q / %q",
			got.BackupURL, got.BackupStreamKey)
	}
}

// The toggle has to reach the create call, not just the database.
//
// Mutation: hardcode `BackupIngest: false` in ingestOptionsFor. Observed FAIL
// ("the stored toggle did not reach the create call").
func TestTheScheduledPathAsksForBackupIngestWhenEnabled(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777"}
	stubAnnounce(stub, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	d.Facebook.BackupIngest = true
	if _, err := store.UpdateDestination(d); err != nil {
		t.Fatalf("enable backup: %v", err)
	}
	seedCreds(t, s, store, db.PlatformFacebook)
	seedStartSchedule(t, store, scheduler.KindOnce, time.Now().Add(2*24*time.Hour), d.ID)

	s.preannounceOnce(context.Background(), time.Now())

	if len(rec.creates()) != 1 {
		t.Fatalf("created %d broadcasts, want 1", len(rec.creates()))
	}
	if !rec.creates()[0].BackupIngest {
		t.Error("the stored toggle did not reach the create call, so Facebook was " +
			"never asked for a backup endpoint")
	}
}

// Issue #82: an operator deleted the scheduled video on Facebook, so the
// reschedule refuses forever and the event page never comes back.
//
// Solved by COUNTING rather than by reading the error. Telling "deleted" from
// "network blip" needs Graph's error codes for a deleted LiveVideo, and Graph
// documents no update surface for LiveVideo at all -- so there is nothing
// authoritative to match. Guessing wrong one way orphans a live event page;
// wrong the other way creates a duplicate people are also subscribed to.
func TestABroadcastThatWillNotMoveIsEventuallyReplaced(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777"}
	stubAnnounce(stub, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	now := time.Now()
	seedStartSchedule(t, store, scheduler.KindWeekly, now.Add(24*time.Hour), d.ID)

	// First sweep creates it.
	s.preannounceOnce(context.Background(), now)
	if got, _ := store.GetDestination(d.ID); got.Facebook.BroadcastID != "777" {
		t.Fatalf("setup: no broadcast was created")
	}

	// Now every reschedule refuses, as a deleted video does.
	rec.fail("(#100) Object does not exist")
	for i := 1; i <= staleBroadcastAfter; i++ {
		s.preannounceOnce(context.Background(), now.Add(time.Duration(7*i)*24*time.Hour))
	}

	got, _ := store.GetDestination(d.ID)
	if got.Facebook.BroadcastID != "" {
		t.Fatalf("after %d consecutive refusals the marker still names a broadcast "+
			"that will not move, so no fresh event page is ever created",
			staleBroadcastAfter)
	}
	if !got.Facebook.ScheduledFor.IsZero() {
		t.Error("the occurrence marker survived, so the next sweep would treat " +
			"this occurrence as already announced")
	}
}

// The half that stops the cure being worse than the disease. ONE failure must
// never clear the marker: a transient error would then orphan a perfectly good
// event page and create a duplicate beside it.
//
// Mutation: `< 1` in place of `< staleBroadcastAfter` in noteRescheduleFailure.
// Observed FAIL ("one transient failure discarded the broadcast").
func TestASingleFailedRescheduleChangesNothing(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777"}
	stubAnnounce(stub, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	now := time.Now()
	seedStartSchedule(t, store, scheduler.KindWeekly, now.Add(24*time.Hour), d.ID)
	s.preannounceOnce(context.Background(), now)

	rec.fail("dial tcp: i/o timeout")
	s.preannounceOnce(context.Background(), now.Add(8*24*time.Hour))

	got, _ := store.GetDestination(d.ID)
	if got.Facebook.BroadcastID != "777" {
		t.Fatal("one transient failure discarded the broadcast; the next sweep " +
			"would create a second event page and orphan a live one")
	}
}

// And the counter must be CONSECUTIVE, not cumulative -- the same distinction
// DestResilience.GiveUpAfter draws. A destination that fails once, recovers,
// and fails again must never accumulate its way to a verdict.
func TestASuccessfulRescheduleResetsTheCount(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777"}
	stubAnnounce(stub, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	now := time.Now()
	seedStartSchedule(t, store, scheduler.KindWeekly, now.Add(24*time.Hour), d.ID)
	s.preannounceOnce(context.Background(), now)

	// Fail, recover, fail again -- staleBroadcastAfter failures in total, but
	// never that many in a row.
	week := 0
	for i := 0; i < staleBroadcastAfter; i++ {
		rec.fail("transient")
		week++
		s.preannounceOnce(context.Background(), now.Add(time.Duration(7*week)*24*time.Hour))
		rec.succeed()
		week++
		s.preannounceOnce(context.Background(), now.Add(time.Duration(7*week)*24*time.Hour))
	}

	// Asserted on the CREATE COUNT, not on the marker still being set.
	//
	// The marker cannot see this: if the count did accumulate, the marker is
	// cleared and the very next sweep -- a successful one -- creates a fresh
	// broadcast and puts an id straight back. Measured: the mutation that
	// removes the reset left the marker assertion green. A second create is
	// the thing that actually happened, and the thing that would orphan an
	// event page in production.
	if len(rec.creates()) != 1 {
		t.Fatalf("created %d broadcasts; %d NON-consecutive failures discarded the "+
			"first one, so a destination that recovers between failures "+
			"accumulated its way to a verdict", len(rec.creates()), staleBroadcastAfter)
	}
	if got, _ := store.GetDestination(d.ID); got.Facebook.BroadcastID == "" {
		t.Error("the broadcast marker was discarded despite the failures never " +
			"being consecutive")
	}
}

// A7. TWO START SCHEDULES, ONE DESTINATION -- the shape that shipped broken and
// that nothing covered. A schedule naming no destinations names them all, which
// the code documents as the commonest shape, so both of these reach the one
// Facebook destination.
//
// With one marker per destination the second schedule saw the first's broadcast
// and RESCHEDULED it to its own occurrence; the next sweep moved it back. One
// Graph write every five minutes forever, a time-change notification to
// subscribers each way, and one of the two shows with no event page at all.
//
// Mutation: in internal/db/facebook.go, change AnnouncementFor's
// `if a.ScheduleID == scheduleID` to `if a.BroadcastID != ""`. The second
// schedule then finds the first's broadcast and moves it rather than creating
// one of its own. Observed: red -- 1 broadcast created where two shows need 2.
//
// The same defect from this package's side, so the guard does not depend on a
// mutation in another one: pass a literal 0 rather than sc.ID to both
// cur.Facebook.Announce calls in announceOne, which puts every schedule's
// broadcast under one shared marker. Observed FAIL ("rescheduled 1 times,
// want 0").
func TestTwoStartSchedulesEachGetTheirOwnEventPage(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "k", ids: []string{"tuesday-show", "thursday-show"}}
	stubAnnounce(stub, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	now := time.Now()
	tuesday := now.Add(24 * time.Hour).Truncate(time.Second)
	thursday := now.Add(3 * 24 * time.Hour).Truncate(time.Second)
	// Naming nothing: both schedules target every destination.
	seedStartSchedule(t, store, scheduler.KindOnce, tuesday)
	seedStartSchedule(t, store, scheduler.KindOnce, thursday)

	// Three sweeps, because the ping-pong needs two to be visible: A moves it
	// back on the sweep after B moved it.
	for range 3 {
		s.preannounceOnce(context.Background(), now)
	}

	if len(rec.creates()) != 2 {
		t.Fatalf("created %d broadcasts for two shows, want 2 -- one of them has no "+
			"event page at all", len(rec.creates()))
	}
	if len(rec.reschedules()) != 0 {
		t.Fatalf("rescheduled %d times, want 0. Two schedules are two shows; moving "+
			"one show's broadcast to the other's start time notifies its "+
			"subscribers of a time change every five minutes forever",
			len(rec.reschedules()))
	}
	got, _ := store.GetDestination(d.ID)
	if !got.Facebook.AnnouncedFor(tuesday) || !got.Facebook.AnnouncedFor(thursday) {
		t.Fatalf("both occurrences must read as announced: %+v", got.Facebook.Announcements)
	}
	seen := map[string]bool{}
	for _, a := range got.Facebook.Announcements {
		if seen[a.BroadcastID] {
			t.Errorf("both shows point at broadcast %q", a.BroadcastID)
		}
		seen[a.BroadcastID] = true
	}
}

// A6. The sweep reads every destination once, before any network I/O, and then
// makes a Graph call that can take thirty seconds. An operator edit landing in
// that window used to be reverted by the full-row write that followed -- and for
// destinations late in a sweep the window is the whole sweep.
//
// Mutation: in preannounce.go, change record's call to
// `s.store.UpdateAnnouncement(d.ID, func(cur *db.Destination) bool { *cur = *d; return apply(cur) })`,
// which restores writing the pre-Graph snapshot. Observed: red -- the donate
// charity the operator set during the call is gone. The rename survives even
// then, because the narrow column list is a second defence and does not carry
// `name` at all; the Facebook blob is the one the stale snapshot reaches.
func TestAnOperatorEditDuringTheGraphCallSurvives(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "key-from-the-broadcast", broadcastID: "777"}
	stubAnnounce(stub, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	seedStartSchedule(t, store, scheduler.KindOnce, time.Now().Add(2*24*time.Hour), d.ID)

	rec.onCreate(func() {
		row, err := store.GetDestination(d.ID)
		if err != nil {
			t.Errorf("read the destination mid-sweep: %v", err)
			return
		}
		row.Name = "renamed by the operator"
		row.AudioBitrate = 96
		row.Facebook.DonateCharityID = "999"
		if _, err := store.UpdateDestination(row); err != nil {
			t.Errorf("operator edit mid-sweep: %v", err)
		}
	})

	s.preannounceOnce(context.Background(), time.Now())

	got, _ := store.GetDestination(d.ID)
	if got.Name != "renamed by the operator" || got.AudioBitrate != 96 {
		t.Errorf("the sweep reverted an operator edit: name %q, bitrate %d",
			got.Name, got.AudioBitrate)
	}
	if got.Facebook.DonateCharityID != "999" {
		t.Error("the sweep reverted a Facebook setting it does not own; the donate " +
			"button the operator just configured is gone")
	}
	// And the announcement still landed. Preserving the edit by not writing at
	// all would be the wrong cure.
	if got.StreamKey != "key-from-the-broadcast" || got.Facebook.BroadcastID != "777" {
		t.Errorf("the announcement was not recorded: key %q, broadcast %q",
			got.StreamKey, got.Facebook.BroadcastID)
	}
}

// A6, the other half. The broadcast is created on Facebook BEFORE anything is
// stored, so a write that fails leaves a real public live_video with no local
// record -- and the next sweep creates a second one beside it. The intent is
// recorded first so that failure leaves evidence.
//
// Mutation: in preannounce.go, delete the record(...Announce(sc.ID, at, "",
// now)) block above the s.ingestFor call. Observed: red -- nothing is stored
// when the create is made.
func TestTheIntentIsRecordedBeforeTheBroadcastIsCreated(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777"}
	stubAnnounce(stub, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	at := time.Now().Add(2 * 24 * time.Hour).Truncate(time.Second)
	schedID := seedStartSchedule(t, store, scheduler.KindOnce, at, d.ID)

	var pending, announcedTooEarly bool
	rec.onCreate(func() {
		row, err := store.GetDestination(d.ID)
		if err != nil {
			t.Errorf("read the destination mid-create: %v", err)
			return
		}
		held, ok := row.Facebook.AnnouncementFor(schedID)
		pending = ok && held.BroadcastID == ""
		// And it must NOT read as announced: a marker claiming a broadcast that
		// does not exist yet would suppress every later attempt.
		announcedTooEarly = row.Facebook.AnnouncedFor(at)
	})

	s.preannounceOnce(context.Background(), time.Now())

	if !pending {
		t.Error("nothing recorded the attempt before the live_video was created, so " +
			"a failed write leaves an event page nothing here can find")
	}
	if announcedTooEarly {
		t.Error("the attempt read as an announcement before the broadcast existed")
	}
}

// And what that evidence is for: a create whose outcome never reached the
// database must not be made a second time. Two event pages in front of the same
// subscribers is worse than one nobody recorded.
//
// Mutation: in preannounce.go, delete the `case held:` arm from announceOne.
// Observed: red -- a second broadcast is created for a show that may already
// have one.
func TestACreateWhoseOutcomeWasNeverRecordedIsNotMadeTwice(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777"}
	stubAnnounce(stub, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	now := time.Now()
	at := now.Add(2 * 24 * time.Hour).Truncate(time.Second)
	schedID := seedStartSchedule(t, store, scheduler.KindOnce, at, d.ID)

	// The state a failed write leaves behind.
	d.Facebook.Announce(schedID, at, "", now)
	if _, err := store.UpdateDestination(d); err != nil {
		t.Fatalf("seed the in-flight marker: %v", err)
	}

	s.preannounceOnce(context.Background(), now)

	if len(rec.creates()) != 0 {
		t.Fatalf("created %d broadcasts for a show whose create was already in "+
			"flight, want 0", len(rec.creates()))
	}
}

// A5. Creating a broadcast issues a NEW stream key, and StreamKey is inside
// Target(), which is the first element of destSpec -- the engine's restart hash.
// Writing it under a running FFmpeg leaves the process publishing to the old key
// until some unrelated reconcile notices, and then cycles a LIVE destination.
//
// Mutation: in preannounce.go, delete the `if d.Enabled { continue }` block from
// preannounceOnce. Observed: red -- a broadcast is created and the live
// destination's key is replaced.
func TestAnAlreadyLiveDestinationIsLeftAlone(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "key-from-the-broadcast", broadcastID: "777"}
	stubAnnounce(stub, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	d.Enabled = true
	if _, err := store.UpdateDestination(d); err != nil {
		t.Fatalf("enable the destination: %v", err)
	}
	seedCreds(t, s, store, db.PlatformFacebook)
	seedStartSchedule(t, store, scheduler.KindOnce, time.Now().Add(2*24*time.Hour), d.ID)

	s.preannounceOnce(context.Background(), time.Now())

	if len(rec.creates()) != 0 {
		t.Fatalf("created %d broadcasts for a destination that is already live, want 0",
			len(rec.creates()))
	}
	got, _ := store.GetDestination(d.ID)
	if got.StreamKey != "original-key" {
		t.Errorf("StreamKey = %q -- the running FFmpeg is publishing to a key that "+
			"is no longer stored", got.StreamKey)
	}
}

// The same rule at the write, for the destination that goes live WHILE Facebook
// is being asked for a broadcast. The sweep decided against a snapshot taken
// before the call; the write is the last moment the answer can still change.
//
// Mutation: in preannounce.go, delete `if cur.Enabled { return false }` from the
// completion callback. Observed: red -- the live destination's key is replaced.
func TestADestinationThatGoesLiveDuringTheGraphCallKeepsItsKey(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "key-from-the-broadcast", broadcastID: "777"}
	stubAnnounce(stub, rec)

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	seedStartSchedule(t, store, scheduler.KindOnce, time.Now().Add(2*24*time.Hour), d.ID)

	rec.onCreate(func() {
		if err := store.SetDestinationEnabled(d.ID, true); err != nil {
			t.Errorf("go live mid-sweep: %v", err)
		}
	})

	s.preannounceOnce(context.Background(), time.Now())

	got, _ := store.GetDestination(d.ID)
	if got.StreamKey != "original-key" {
		t.Errorf("StreamKey = %q -- the destination went live during the Graph call "+
			"and the sweep changed the key it is publishing to", got.StreamKey)
	}
}

// B7. An ineligible account -- Facebook requires 60 days and 100 followers to
// schedule -- refuses forever, and the refusal was logged every sweep: 288
// identical warnings a day per destination for a condition that cannot resolve
// without the operator.
//
// Mutation: in preannounce.go, change `if s.noteAnnounceFailure(d.ID, sc.ID) == 1`
// on the create-failure path to `>= 1`, which is the suppression switched off.
// Observed: red, 6 warnings for the 2 the two destinations are owed.
func TestAnIdenticalRefusalIsLoggedOncePerRunOfFailures(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777", err: "(#100) ineligible"}
	stubAnnounce(stub, rec)

	var logs bytes.Buffer
	s.log = slog.New(slog.NewTextHandler(&logs, nil))

	first := seedDestination(t, s, store, db.PlatformFacebook, "fb-one")
	second := seedDestination(t, s, store, db.PlatformFacebook, "fb-two")
	seedCreds(t, s, store, db.PlatformFacebook)
	seedStartSchedule(t, store, scheduler.KindOnce, time.Now().Add(2*24*time.Hour))

	for range 3 {
		s.preannounceOnce(context.Background(), time.Now())
	}
	if len(rec.creates()) != 6 {
		t.Fatalf("attempted %d creates across three sweeps of two destinations, "+
			"want 6 -- suppressing the LOG must not suppress the retry", len(rec.creates()))
	}

	warnings := strings.Count(logs.String(), "could not create the broadcast")
	if warnings != 2 {
		t.Fatalf("logged the same refusal %d times, want one per destination (2). "+
			"At a five-minute tick every extra line is 288 a day: %s", warnings, logs.String())
	}
	// One line each, not one line for whichever destination lost the race: the
	// suppression is per show, not global.
	for _, name := range []string{first.Name, second.Name} {
		if !strings.Contains(logs.String(), name) {
			t.Errorf("%s's refusal was never reported at all", name)
		}
	}
}

// C8. A ticker's first fire is a whole period away, so a daemon restarted two
// minutes before a show announced nothing for five -- and the loop's behaviour
// after a restart differed from its behaviour in steady state, which is exactly
// the difference nothing was watching.
//
// Mutation: in preannounce.go, delete the s.preannounceOnce call above
// PreannounceLoop's for statement. Observed: red -- the create never arrives and
// the test fails on its own deadline rather than waiting five minutes.
func TestTheLoopSweepsBeforeItsFirstTick(t *testing.T) {
	s, _, store, stub := stubbedServer(t, config.Config{})
	rec := &announced{key: "k", broadcastID: "777"}
	stubAnnounce(stub, rec)

	// Signalled from inside the Graph call rather than counted afterwards: the
	// loop runs in its own goroutine, so the test has to be woken by the create
	// rather than poll for it.
	created := make(chan struct{}, 1)
	rec.onCreate(func() {
		select {
		case created <- struct{}{}:
		default:
		}
	})

	d := seedDestination(t, s, store, db.PlatformFacebook, "fb")
	seedCreds(t, s, store, db.PlatformFacebook)
	seedStartSchedule(t, store, scheduler.KindOnce, time.Now().Add(2*24*time.Hour), d.ID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.PreannounceLoop(ctx)

	select {
	case <-created:
	case <-time.After(10 * time.Second):
		t.Fatal("the loop announced nothing before its first tick, so a restart " +
			"inside the last five minutes before a show misses it")
	}
}
