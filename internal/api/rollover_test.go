package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/eventstream/streamtest"
	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// The rollover coordinator is tested against an in-memory store and recording fakes for the platform, the engine and
// the webhook. What matters here is which destination was touched, with which call, and what was written afterwards:
// a rollover that acted on the wrong destination, or on a platform with no action, or that wrote a result the operator
// would believe, is the failure these tests exist to catch.

type fakeRolloverStore struct {
	mu        sync.Mutex
	dests     map[int64]*db.Destination
	settings  map[int64]db.RolloverSettings
	templates map[string]*scheduler.BroadcastTemplate // "destID/broadcastID"
	results   []string
	timeline  []db.TimelineEvent
	thumbs    map[int64][]byte // schedule id -> PNG bytes
}

func newFakeRolloverStore() *fakeRolloverStore {
	return &fakeRolloverStore{
		dests:     map[int64]*db.Destination{},
		settings:  map[int64]db.RolloverSettings{},
		templates: map[string]*scheduler.BroadcastTemplate{},
	}
}

func (f *fakeRolloverStore) ListEnabledRollovers() ([]db.RolloverSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []db.RolloverSettings
	for _, s := range f.settings {
		if s.Enabled {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeRolloverStore) GetRollover(id int64) (db.RolloverSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.settings[id]; ok {
		return s, nil
	}
	return db.RolloverSettings{DestinationID: id, MarginMinutes: db.DefaultRolloverMarginMinutes}, nil
}

func (f *fakeRolloverStore) SetRolloverSession(id int64, broadcastID string, startedAt, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.settings[id]
	s.DestinationID = id
	s.SessionBroadcastID, s.SessionStartedAt = broadcastID, startedAt
	f.settings[id] = s
	return nil
}

func (f *fakeRolloverStore) RecordRolloverResult(id int64, trigger, result string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.settings[id]
	s.DestinationID = id
	s.LastTrigger, s.LastResult, s.LastRolloverAt = trigger, result, at
	f.settings[id] = s
	f.results = append(f.results, result)
	return nil
}

func (f *fakeRolloverStore) GetDestination(id int64) (*db.Destination, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.dests[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	cp := *d
	return &cp, nil
}

func (f *fakeRolloverStore) UpdateLifecycle(id int64, apply func(*db.Destination) bool) (*db.Destination, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.dests[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	if apply(d) {
		return d, nil
	}
	return d, nil
}

func (f *fakeRolloverStore) RolloverTemplateFor(id int64, broadcastID string) (*scheduler.BroadcastTemplate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.templates[fmt.Sprintf("%d/%s", id, broadcastID)]; ok {
		return t, nil
	}
	return nil, db.ErrNotFound
}

func (f *fakeRolloverStore) RebindOccurrenceEvent(id int64, from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.templates[fmt.Sprintf("%d/%s", id, from)]; ok {
		delete(f.templates, fmt.Sprintf("%d/%s", id, from))
		f.templates[fmt.Sprintf("%d/%s", id, to)] = t
	}
	return nil
}

func (f *fakeRolloverStore) RecordTimelineEvent(ev db.TimelineEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.timeline = append(f.timeline, ev)
	return nil
}

func (f *fakeRolloverStore) timelineRows() []db.TimelineEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]db.TimelineEvent(nil), f.timeline...)
}

func (f *fakeRolloverStore) BroadcastThumbnail(scheduleID int64) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.thumbs[scheduleID]; ok {
		return b, "image/png", nil
	}
	return nil, "", nil
}

func (f *fakeRolloverStore) GetSchedule(id int64) (*scheduler.Schedule, error) {
	return nil, db.ErrNotFound
}

// fakeYouTube records what a rollover asked of the platform.
type fakeYouTube struct {
	mu          sync.Mutex
	rolls       []oauth.RolloverRequest
	err         error
	outcome     *oauth.RolloverOutcome
	sessionFrom time.Time
	// ctxErr is the context error the platform call saw. A manual run must not hand the platform a context
	// the browser's disconnect has already cancelled.
	ctxErr error
	// startErr makes the live-start read fail, the first platform call a manual run makes.
	startErr error
}

func (y *fakeYouTube) RollOverBroadcast(ctx context.Context, _ string, req oauth.RolloverRequest) (*oauth.RolloverOutcome, error) {
	y.mu.Lock()
	defer y.mu.Unlock()
	y.ctxErr = ctx.Err()
	y.rolls = append(y.rolls, req)
	if y.err != nil {
		return nil, y.err
	}
	if y.outcome != nil {
		return y.outcome, nil
	}
	return &oauth.RolloverOutcome{NewBroadcastID: "new-1", OldEnded: true}, nil
}

func (y *fakeYouTube) RolloverSessionStart(_ context.Context, _, _ string) (time.Time, error) {
	if y.startErr != nil {
		return time.Time{}, y.startErr
	}
	return y.sessionFrom, nil
}

// rolloverRig wires a coordinator to the fakes and records every announcement and restart.
type rolloverRig struct {
	store      *fakeRolloverStore
	yt         *fakeYouTube
	restarts   []int64
	announced  []hooks.Event
	delivering map[int64]bool
	now        time.Time
	c          *rolloverCoordinator
}

func newRolloverRig(t *testing.T) *rolloverRig {
	t.Helper()
	r := &rolloverRig{
		store:      newFakeRolloverStore(),
		yt:         &fakeYouTube{},
		delivering: map[int64]bool{},
		now:        time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
	}
	host := rolloverHost{
		tokenFor: func(context.Context, int64) (*db.PlatformAccount, error) {
			return &db.PlatformAccount{AccessToken: "tok", AccountRef: "rig-ref"}, nil
		},
		youtube: func() (youtubeRollover, bool) { return r.yt, true },
		restart: func(id int64) error {
			r.restarts = append(r.restarts, id)
			return nil
		},
		delivering: func(id int64) bool { return r.delivering[id] },
		announce:   func(ev hooks.Event) { r.announced = append(r.announced, ev) },
	}
	r.c = newRolloverCoordinator(slog.New(slog.NewTextHandler(io.Discard, nil)), r.store, host)
	r.c.now = func() time.Time { return r.now }
	return r
}

func (r *rolloverRig) addDest(id int64, p db.Platform, bid string) {
	acct := int64(1)
	r.store.dests[id] = &db.Destination{
		ID: id, Name: fmt.Sprintf("dest-%d", id), Platform: p, Enabled: true, AccountID: &acct,
		Lifecycle: db.BroadcastControl{BroadcastID: bid},
	}
	r.delivering[id] = true
}

func (r *rolloverRig) enable(id int64, scene string) {
	r.store.settings[id] = db.RolloverSettings{
		DestinationID: id, Enabled: true, MarginMinutes: 30, OBSScene: scene,
	}
}

// TestTheSweepLeavesAnIdleDestinationAlone checks the most basic guard. A destination that is not delivering has no
// live broadcast to replace, so no platform call and no restart may happen, however old its session.
func TestTheSweepLeavesAnIdleDestinationAlone(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	r.enable(1, "")
	r.delivering[1] = false
	r.store.settings[1] = db.RolloverSettings{DestinationID: 1, Enabled: true, MarginMinutes: 30,
		SessionBroadcastID: "live-1", SessionStartedAt: r.now.Add(-100 * time.Hour)}
	r.c.sweepOnce(context.Background())
	if len(r.yt.rolls) != 0 || len(r.restarts) != 0 {
		t.Errorf("an idle destination was acted on: youtube=%d restarts=%d", len(r.yt.rolls), len(r.restarts))
	}
	if got := r.store.settings[1].SessionStartedAt; !got.IsZero() {
		t.Errorf("the session of an idle destination was not ended: %s", got)
	}
}

// TestAYouTubeBroadcastInItsMarginWindowIsReplacedAndTheDestinationPointsAtIt is the main path for the platform
// with an action. The session counts from the platform's start, the margin trips, the replacement is requested, and
// the destination's record moves to the new broadcast.
func TestAYouTubeBroadcastInItsMarginWindowIsReplacedAndTheDestinationPointsAtIt(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	r.enable(1, "")
	// The platform says the show started 11 h 40 min ago: inside the 12-hour cap less the 30-minute margin.
	r.yt.sessionFrom = r.now.Add(-(11*time.Hour + 40*time.Minute))

	r.c.sweepOnce(context.Background())

	if len(r.yt.rolls) != 1 || r.yt.rolls[0].OldBroadcastID != "live-1" {
		t.Fatalf("rollovers = %+v, want one for live-1", r.yt.rolls)
	}
	if got := r.store.dests[1].Lifecycle.BroadcastID; got != "new-1" {
		t.Errorf("destination points at %q, want the replacement new-1", got)
	}
	if got := r.store.dests[1].Lifecycle.Generation; got != 1 {
		t.Errorf("generation = %d after a rollover, want 1: a lifecycle sweep holding the old row must not overwrite the new id", got)
	}
	if !hasEvent(r.announced, hooks.TriggerRolloverCompleted) {
		t.Errorf("no rollover.completed was announced: %+v", r.announced)
	}
	if !hasEvent(r.announced, hooks.TriggerRolloverStarted) {
		t.Errorf("no rollover.started was announced before the platform was asked")
	}
}

// TestNothingHappensBeforeTheMarginAndTheDestinationIsNotTouched checks the other side of the cap: a session with time
// left is planned, not rolled over, and its record is left exactly as it was.
func TestNothingHappensBeforeTheMarginAndTheDestinationIsNotTouched(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	r.enable(1, "")
	r.yt.sessionFrom = r.now.Add(-time.Hour)
	r.c.sweepOnce(context.Background())
	if len(r.yt.rolls) != 0 {
		t.Errorf("a broadcast an hour into a 12-hour cap was rolled over")
	}
	if r.store.dests[1].Lifecycle.BroadcastID != "live-1" {
		t.Errorf("the destination was repointed without a rollover")
	}
}

// TestAFailedRolloverLeavesTheOldBroadcastRecordedAndSaysSo is the rule that a failed rollover never stops a
// stream. The destination keeps the old broadcast, the failure is recorded, and rollover.failed is announced.
func TestAFailedRolloverLeavesTheOldBroadcastRecordedAndSaysSo(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	r.enable(1, "")
	r.yt.sessionFrom = r.now.Add(-(11*time.Hour + 45*time.Minute))
	r.yt.err = errors.New("the replacement would not start")

	r.c.sweepOnce(context.Background())

	if got := r.store.dests[1].Lifecycle.BroadcastID; got != "live-1" {
		t.Errorf("a failed rollover moved the destination to %q; it must keep live-1", got)
	}
	if !hasEvent(r.announced, hooks.TriggerRolloverFailed) {
		t.Errorf("no rollover.failed was announced")
	}
	if len(r.store.results) != 1 || !strings.HasPrefix(r.store.results[0], "failed: ") {
		t.Errorf("results = %v, want one failure recorded", r.store.results)
	}
}

// TestATwitchDestinationIsReconnectedAndNothingElseIs checks the two platforms with an action and the many without
// one. Twitch is restarted through the engine. Facebook and Kick are countdown only: no restart, no platform call.
func TestATwitchDestinationIsReconnectedAndNothingElseIs(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformTwitch, "")
	r.addDest(2, db.PlatformFacebook, "fb-live")
	r.addDest(3, db.PlatformKick, "")
	r.enable(1, "")
	r.enable(2, "")
	r.enable(3, "")
	// Each one has been on air far past its cap, so every planner would have something to say.
	for _, id := range []int64{1, 2, 3} {
		r.store.settings[id] = db.RolloverSettings{DestinationID: id, Enabled: true, MarginMinutes: 30,
			SessionStartedAt: r.now.Add(-400 * time.Hour)}
	}

	r.c.sweepOnce(context.Background())

	if len(r.restarts) != 1 || r.restarts[0] != 1 {
		t.Errorf("restarts = %v, want exactly the Twitch destination", r.restarts)
	}
	if len(r.yt.rolls) != 0 {
		t.Errorf("a non-YouTube destination reached the YouTube rollover")
	}
	for _, ev := range r.announced {
		if ev.Destination != nil && ev.Destination.ID != 1 && ev.Trigger == hooks.TriggerRolloverStarted {
			t.Errorf("a countdown-only destination (%d) was announced as starting a rollover", ev.Destination.ID)
		}
	}
}

// TestAnOBSSyncSwitchIsNeverARolloverCue checks that a reconnect snapshot from OBS does not fire a rollover. OBS sends
// its current scene again after the link comes back; that is not the operator changing scene.
func TestAnOBSSyncSwitchIsNeverARolloverCue(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	r.enable(1, "Be Right Back")
	r.yt.sessionFrom = r.now.Add(-time.Hour)
	r.c.noteScene("Be Right Back", true, r.now)
	r.c.sweepOnce(context.Background())
	if len(r.yt.rolls) != 0 {
		t.Errorf("a sync (reconnect) scene read fired a rollover")
	}
}

// TestALiveSceneSwitchIsTheRolloverCue is the other half: a live switch to the configured scene, inside the session,
// fires a rollover.
func TestALiveSceneSwitchIsTheRolloverCue(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	r.enable(1, "Be Right Back")
	r.yt.sessionFrom = r.now.Add(-time.Hour)
	r.c.noteScene("Be Right Back", false, r.now.Add(-time.Minute))
	r.c.sweepOnce(context.Background())
	if len(r.yt.rolls) != 1 {
		t.Errorf("a live switch to the configured scene did not fire a rollover")
	}
}

// TestAManualRunOnAnIdleDestinationIsRefused checks the operator's button against a destination that is off air.
// There is no live broadcast to replace, so the call must refuse and touch nothing.
func TestAManualRunOnAnIdleDestinationIsRefused(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	r.delivering[1] = false
	err := r.c.RunNow(context.Background(), 1)
	if !errors.Is(err, errRolloverNotLive) {
		t.Fatalf("RunNow on an idle destination = %v, want errRolloverNotLive", err)
	}
	if len(r.yt.rolls) != 0 {
		t.Errorf("a refused manual run still reached the platform")
	}
}

// TestAManualRunOnAFacebookDestinationIsRefusedAsCountdownOnly keeps the countdown-only platforms out of the action
// path even when an operator asks for it by hand.
func TestAManualRunOnAFacebookDestinationIsRefusedAsCountdownOnly(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(2, db.PlatformFacebook, "fb-live")
	err := r.c.RunNow(context.Background(), 2)
	if !errors.Is(err, errRolloverNoAction) {
		t.Fatalf("RunNow on Facebook = %v, want errRolloverNoAction", err)
	}
}

// TestTheReplacementTakesItsTitleFromTheRecurringTemplate checks the template path. A broadcast that came from a
// recurring template keeps the template's title, rendered for the moment of the rollover, not the old broadcast's.
func TestTheReplacementTakesItsTitleFromTheRecurringTemplate(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	r.enable(1, "")
	r.yt.sessionFrom = r.now.Add(-(11*time.Hour + 45*time.Minute))
	r.store.templates["1/live-1"] = &scheduler.BroadcastTemplate{
		ScheduleID: 5, TitlePattern: "Thursday show {date}", Description: "Weekly notes", Privacy: "unlisted",
	}
	r.c.sweepOnce(context.Background())
	if len(r.yt.rolls) != 1 {
		t.Fatalf("rollovers = %d, want 1", len(r.yt.rolls))
	}
	got := r.yt.rolls[0]
	if got.Title != "Thursday show 2026-10-31" || got.Description != "Weekly notes" || got.Privacy != "unlisted" {
		t.Errorf("replacement request = %+v, want the template's title, description and privacy", got)
	}
}

func hasEvent(evs []hooks.Event, t hooks.Trigger) bool {
	for _, ev := range evs {
		if ev.Trigger == t {
			return true
		}
	}
	return false
}

// TestARolloverEmitsItsEventsOnTheStreamThroughTheRealProducer drives the coordinator's own announce path, the one the
// server wires, and reads the events back off the hub. The three types are the planned rollover's public events, and a
// test that publishes them by hand would prove nothing about the producer.
func TestARolloverEmitsItsEventsOnTheStreamThroughTheRealProducer(t *testing.T) {
	hub := eventstream.NewHub()
	sub := streamtest.Subscribe(t, hub)
	srv := &Server{stream: hub}

	r := newRolloverRig(t)
	r.c.host.announce = srv.publishRollover
	r.addDest(1, db.PlatformYouTube, "live-1")
	r.enable(1, "")
	r.yt.sessionFrom = r.now.Add(-(11*time.Hour + 45*time.Minute))
	r.c.sweepOnce(context.Background())
	streamtest.Expect(t, sub, eventstream.TypeRolloverStarted)
	streamtest.Expect(t, sub, eventstream.TypeRolloverCompleted)

	r.addDest(2, db.PlatformYouTube, "live-2")
	r.enable(2, "")
	r.yt.sessionFrom = r.now.Add(-(11*time.Hour + 45*time.Minute))
	r.yt.err = errors.New("the replacement would not start")
	r.c.sweepOnce(context.Background())
	streamtest.Expect(t, sub, eventstream.TypeRolloverFailed)
}

// TestARolloverWritesItsStartAndItsOutcomeToTheTimeline checks the operator's timeline, which the Monitoring page lists.
// A rollover that only reached the webhook and the stream would leave nothing for an operator reading history after the
// show. The rows name the destination by its alert key, so the page can link to it, and the failure row is a warning.
func TestARolloverWritesItsStartAndItsOutcomeToTheTimeline(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	r.enable(1, "")
	r.yt.sessionFrom = r.now.Add(-(11*time.Hour + 45*time.Minute))

	r.c.sweepOnce(context.Background())

	rows := r.store.timelineRows()
	if len(rows) != 2 {
		t.Fatalf("timeline rows = %+v, want the start and the completion", rows)
	}
	if rows[0].Type != string(hooks.TriggerRolloverStarted) || rows[1].Type != string(hooks.TriggerRolloverCompleted) {
		t.Errorf("timeline types = %q, %q; want rollover.started then rollover.completed", rows[0].Type, rows[1].Type)
	}
	for _, row := range rows {
		if row.Subject != "destination:1" {
			t.Errorf("timeline subject = %q, want destination:1 so the page can link the destination", row.Subject)
		}
	}

	r.addDest(2, db.PlatformYouTube, "live-2")
	r.enable(2, "")
	r.yt.sessionFrom = r.now.Add(-(11*time.Hour + 45*time.Minute))
	r.yt.err = errors.New("the replacement would not start")
	r.c.sweepOnce(context.Background())

	rows = r.store.timelineRows()
	last := rows[len(rows)-1]
	if last.Type != string(hooks.TriggerRolloverFailed) || last.Severity != "warning" {
		t.Errorf("failure row = %+v, want rollover.failed at warning severity", last)
	}
}

// TestAYouTubeSessionWithNoRecordedStartIsWaitedFor checks the rule the platform read states: a zero live start means
// YouTube has not recorded one yet, so nothing is counted from the moment polyemesis happened to look. Counting from a
// later moment moves the margin later, which is the wrong direction for a cap. The session is not stored, and the next
// sweep reads the start again and rolls over on the platform's own time.
func TestAYouTubeSessionWithNoRecordedStartIsWaitedFor(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	r.enable(1, "")
	r.yt.sessionFrom = time.Time{}

	r.c.sweepOnce(context.Background())

	if len(r.yt.rolls) != 0 {
		t.Fatalf("a rollover ran with no recorded start: %+v", r.yt.rolls)
	}
	if got := r.store.settings[1].SessionStartedAt; !got.IsZero() {
		t.Errorf("a guessed session start %s was stored; it must wait for the platform's own", got)
	}

	// Once YouTube records the start, the very next sweep counts from it and the margin trips.
	r.yt.sessionFrom = r.now.Add(-(11*time.Hour + 45*time.Minute))
	r.c.sweepOnce(context.Background())
	if len(r.yt.rolls) != 1 {
		t.Fatalf("rollovers after the start was recorded = %d, want 1", len(r.yt.rolls))
	}
}

// TestConcurrentSweepsAndSceneSwitchesRollTheSameBroadcastOnce is the race the coordinator's lock exists for. The OBS
// bridge writes scene switches while the sweep reads them, and a sweep that read its settings before another pass
// rolled the destination over must not roll it over again. Run under -race, the unguarded scene map is a crash; the
// assertion is that exactly one replacement is requested however the passes interleave.
func TestConcurrentSweepsAndSceneSwitchesRollTheSameBroadcastOnce(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	r.enable(1, "Be Right Back")
	r.yt.sessionFrom = r.now.Add(-(11*time.Hour + 45*time.Minute))

	// A manual run is part of the race too: it skips the planner, so only the debounce stops it rolling the broadcast a
	// sweep just created. Whichever of the three gets the lock first does the rollover, and the rest are refused.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			r.c.sweepOnce(context.Background())
		}()
		go func(i int) {
			defer wg.Done()
			r.c.noteScene("Be Right Back", false, r.now.Add(time.Duration(i)*time.Second))
		}(i)
		go func() {
			defer wg.Done()
			_ = r.c.RunNow(context.Background(), 1)
		}()
	}
	wg.Wait()

	if n := len(r.yt.rolls); n != 1 {
		t.Fatalf("replacements requested = %d, want exactly 1 for one due rollover", n)
	}
}

// TestAManualRunInsideTheDebounceIsRefusedAndRollsNothing checks that a click made just after a rollover is held to the
// same ten minutes the sweep waits. Without it, the operator's click would roll over the broadcast the rollover had only
// just created. The refusal is a 409 that says why, and the platform is not asked.
func TestAManualRunInsideTheDebounceIsRefusedAndRollsNothing(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	r.enable(1, "")
	r.yt.sessionFrom = r.now.Add(-(11*time.Hour + 45*time.Minute))
	r.c.sweepOnce(context.Background())
	if len(r.yt.rolls) != 1 {
		t.Fatalf("setup: rollovers = %d, want 1", len(r.yt.rolls))
	}

	r.now = r.now.Add(2 * time.Minute)
	err := r.c.RunNow(context.Background(), 1)
	if !errors.Is(err, errRolloverTooSoon) {
		t.Fatalf("manual run two minutes after a rollover: err = %v, want errRolloverTooSoon", err)
	}
	if len(r.yt.rolls) != 1 {
		t.Errorf("the platform was asked again inside the debounce: %d rollovers", len(r.yt.rolls))
	}

	r.now = r.now.Add(11 * time.Minute)
	if err := r.c.RunNow(context.Background(), 1); err != nil {
		t.Fatalf("manual run after the debounce: err = %v, want a rollover", err)
	}
	if len(r.yt.rolls) != 2 {
		t.Errorf("rollovers after the debounce = %d, want 2", len(r.yt.rolls))
	}
}

// TestAManualRunWithNoRecordedStartStillRollsOver checks the other side of the wait rule. The sweep waits for the
// platform's start, but an operator who asks for a rollover on a stream YouTube has not yet stamped must not be told
// "not delivering" for a destination that is. A manual run needs no countdown, so it goes ahead.
func TestAManualRunWithNoRecordedStartStillRollsOver(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	r.enable(1, "")
	r.yt.sessionFrom = time.Time{}

	if err := r.c.RunNow(context.Background(), 1); err != nil {
		t.Fatalf("manual run with no recorded start: err = %v, want the rollover to go ahead", err)
	}
	if len(r.yt.rolls) != 1 {
		t.Errorf("rollovers = %d, want 1", len(r.yt.rolls))
	}
}

// A show longer than two caps rolls over twice. The template is found by the live broadcast's id, so the occurrence
// must follow the show onto the replacement, or the second rollover copies the live broadcast and loses the
// template's category and thumbnail.
func TestASecondRolloverStillUsesTheRecurringTemplate(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	r.enable(1, "")
	r.yt.sessionFrom = r.now.Add(-(11*time.Hour + 45*time.Minute))
	r.store.templates["1/live-1"] = &scheduler.BroadcastTemplate{
		ScheduleID: 5, TitlePattern: "Thursday show {date}", Description: "Weekly notes", Privacy: "unlisted",
	}
	r.c.sweepOnce(context.Background())
	if len(r.yt.rolls) != 1 {
		t.Fatalf("rollovers = %d, want 1", len(r.yt.rolls))
	}
	if _, err := r.store.RolloverTemplateFor(1, "new-1"); err != nil {
		t.Fatalf("the template is not found by the replacement's id after the first rollover: %v", err)
	}
}

// TestAManualRunMayRetryAFailedRolloverInsideTheDebounce (w3h tail, #24): a failed rollover recorded its time, so the
// ten-minute debounce held back the operator's retry too, close to the platform's cap and with the old broadcast
// still live. The debounce guards the broadcast a rollover just created; a failed one created nothing the destination
// is on, so a manual run may retry it at once. The sweep still waits: whether it should retry sooner is a quota call.
//
// MUTATION, internal/api/rollover.go, in considerOne: drop the !retriesAFailedRollover(cfg, bid) term. Observed:
// FAIL -- "manual retry two minutes after a failed rollover: err = a rollover ran less than ten minutes ago".
func TestAManualRunMayRetryAFailedRolloverInsideTheDebounce(t *testing.T) {
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformYouTube, "live-1")
	r.enable(1, "")
	r.yt.sessionFrom = r.now.Add(-(11*time.Hour + 45*time.Minute))
	r.yt.err = errors.New("the replacement would not start")
	r.c.sweepOnce(context.Background())
	if len(r.yt.rolls) != 1 {
		t.Fatalf("setup: rollovers = %d, want 1", len(r.yt.rolls))
	}

	// The sweep keeps its debounce after a failure.
	r.now = r.now.Add(2 * time.Minute)
	r.c.sweepOnce(context.Background())
	if len(r.yt.rolls) != 1 {
		t.Fatalf("the sweep retried inside the debounce: %d rollovers", len(r.yt.rolls))
	}

	r.yt.err = nil
	if err := r.c.RunNow(context.Background(), 1); err != nil {
		t.Fatalf("manual retry two minutes after a failed rollover: err = %v, want a rollover", err)
	}
	if len(r.yt.rolls) != 2 {
		t.Fatalf("rollovers after the manual retry = %d, want 2", len(r.yt.rolls))
	}

	// That retry succeeded, so the next click is held again: it would roll over the broadcast just created.
	r.now = r.now.Add(time.Minute)
	if err := r.c.RunNow(context.Background(), 1); !errors.Is(err, errRolloverTooSoon) {
		t.Fatalf("manual run a minute after a good rollover: err = %v, want errRolloverTooSoon", err)
	}
}

// TestAManualRunAfterAFailureThatMovedTheBroadcastIsStillDebounced: a failure that ended the old broadcast and
// pointed the destination at a replacement that would not start changes the broadcast the destination is on. A click
// then would roll over that replacement, so the debounce still holds.
func TestAManualRunAfterAFailureThatMovedTheBroadcastIsStillDebounced(t *testing.T) {
	cfg := db.RolloverSettings{LastResult: rolloverFailedPrefix + "the replacement would not start", SessionBroadcastID: "live-1"}
	if !retriesAFailedRollover(cfg, "live-1") {
		t.Fatal("a failure on the unchanged broadcast is not a retry")
	}
	if retriesAFailedRollover(cfg, "new-1") {
		t.Fatal("a failure that moved the destination to its replacement was treated as a retry")
	}
	if retriesAFailedRollover(db.RolloverSettings{LastResult: "rolled over to new-1 from live-1", SessionBroadcastID: "live-1"}, "live-1") {
		t.Fatal("a successful rollover was treated as a failure to retry")
	}
}
