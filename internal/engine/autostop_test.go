package engine

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// THE CLOCK IS INJECTED. Every test here feeds step() or sweepIngestLoss() the
// time it wants; none sleeps.

var lossT0 = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

const lossWait = 2 * time.Minute

func lossDelivering(id int64) DestStatus {
	return DestStatus{ID: id, Enabled: true,
		Process: &supervisor.Status{State: supervisor.StateRunning, UptimeSec: 30}}
}

func startingNoData(id int64) DestStatus {
	return DestStatus{ID: id, Enabled: true,
		Process: &supervisor.Status{State: supervisor.StateRunning}}
}

func lossIn(primary, backup bool, dests ...DestStatus) lossInputs {
	return lossInputs{configured: true, primaryLive: primary, backupLive: backup, dests: dests, wait: lossWait}
}

// run drives the watch through one sweep per step, 2s apart like observeLoop,
// from t0, and returns the first time it fired (zero if it never did).
func lossRun(w *lossWatch, from time.Time, d time.Duration, i lossInputs) (fired time.Time, ids []int64) {
	for at := from; at.Sub(from) <= d; at = at.Add(2 * time.Second) {
		if got, _, fire := w.step(at, i); fire {
			return at, got
		}
	}
	return time.Time{}, nil
}

// THE BASE CASE: delivering, then both ingests go quiet for the whole wait.
func TestAutoStopFiresOnceWhenNeitherIngestArrivesForTheWait(t *testing.T) {
	var w lossWatch
	w.step(lossT0, lossIn(true, false, lossDelivering(1), lossDelivering(2)))
	at, ids := lossRun(&w, lossT0.Add(2*time.Second), 3*time.Minute, lossIn(false, false, lossDelivering(1), lossDelivering(2)))
	if at.IsZero() {
		t.Fatal("the fallback never fired")
	}
	// The loss clock starts at the first quiet sweep (t0+2s).
	if want := lossT0.Add(2*time.Second + lossWait); !at.Equal(want) {
		t.Errorf("fired at +%v, want +%v", at.Sub(lossT0), want.Sub(lossT0))
	}
	if !reflect.DeepEqual(ids, []int64{1, 2}) {
		t.Errorf("stopped %v, want [1 2]", ids)
	}
	// Once: it stays quiet however long the outage goes on.
	if again, _ := lossRun(&w, at.Add(2*time.Second), 10*time.Minute, lossIn(false, false, lossDelivering(1), lossDelivering(2))); !again.IsZero() {
		t.Error("it fired a second time for the same loss")
	}
}

// GUARD 1: NEVER AT BOOT, NEVER WHILE IDLE.
//
// A fresh watch whose ingest is silent from its first sweep has seen nothing
// arrive; destinations that merely exist do not make a loss. Mutation: start
// the watch armed (or drop the armed check). Observed to fail with "fired at
// boot".
func TestAutoStopNeverFiresAtBootOrWhileIdle(t *testing.T) {
	var w lossWatch
	if at, _ := lossRun(&w, lossT0, time.Hour, lossIn(false, false, lossDelivering(1))); !at.IsZero() {
		t.Fatalf("fired at boot, +%v, with an ingest that never arrived", at.Sub(lossT0))
	}
	// Idle: the ingest arrives but nothing is on air at all.
	w = lossWatch{}
	w.step(lossT0, lossIn(true, false))
	if at, _ := lossRun(&w, lossT0.Add(2*time.Second), time.Hour, lossIn(false, false)); !at.IsZero() {
		t.Fatalf("fired with no destinations, +%v", at.Sub(lossT0))
	}
}

// GUARD 1: ARMS ONLY ON A DESTINATION THAT HAS ACTUALLY RECEIVED DATA.
//
// A destination whose process is up but has reported no uptime has received
// nothing; an ingest that arrived while it was in that state armed nothing.
// Mutation: arm on any arriving ingest. Observed to fail with "armed on a
// destination that has no data".
func TestAutoStopArmsOnlyOnADestinationThatHasReceivedData(t *testing.T) {
	var w lossWatch
	w.step(lossT0, lossIn(true, false, startingNoData(1)))
	if at, _ := lossRun(&w, lossT0.Add(2*time.Second), time.Hour, lossIn(false, false, startingNoData(1))); !at.IsZero() {
		t.Fatal("armed on a destination that has no data")
	}
	// It disarms when everything it would stop is already off.
	w = lossWatch{}
	w.step(lossT0, lossIn(true, false, lossDelivering(1)))
	w.step(lossT0.Add(2*time.Second), lossIn(false, false)) // stopped by hand
	if at, _ := lossRun(&w, lossT0.Add(4*time.Second), time.Hour, lossIn(false, false, lossDelivering(1))); !at.IsZero() {
		t.Fatal("fired after the destinations had been stopped by hand")
	}
}

// GUARD 2: RECOVERY WITHIN THE WAIT CANCELS, AND THE CLOCK STARTS AGAIN.
//
// Two outages of just under the wait each, with a return between, are not one
// long one. Mutation: do not clear lostSince when the ingest arrives. Observed
// to fail with "outages that each recovered added up".
func TestAutoStopRecoveryCancelsAndTheClockRestarts(t *testing.T) {
	var w lossWatch
	d := lossDelivering(1)
	w.step(lossT0, lossIn(true, false, d))
	at := lossT0.Add(2 * time.Second)
	for i := 0; i < 3; i++ {
		if f, _ := lossRun(&w, at, lossWait-4*time.Second, lossIn(false, false, d)); !f.IsZero() {
			t.Fatalf("outage %d fired inside the wait", i)
		}
		at = at.Add(lossWait - 2*time.Second)
		w.step(at, lossIn(true, false, d)) // the ingest returns
		at = at.Add(2 * time.Second)
	}
	// And the same hour of mixed outages never fired in total.
	if f, _ := lossRun(&w, at, lossWait-4*time.Second, lossIn(false, false, d)); !f.IsZero() {
		t.Fatal("outages that each recovered added up")
	}
}

// GUARD 3a: A PUBLISHER RECONNECT. The staging log shows "rtmp publisher
// disconnected" followed by an ingest process restart; the hub is silent for
// the respawn (the supervisor's backoff reaches 30s) and the destinations'
// FFmpegs ride it out. A reconnect inside the wait stops nothing.
func TestAutoStopIgnoresAPublisherReconnect(t *testing.T) {
	var w lossWatch
	d := lossDelivering(1)
	w.step(lossT0, lossIn(true, false, d))
	// 45s of silence: the respawn backoff and the encoder's retry. Destinations
	// look stalled meanwhile.
	stalled := d
	stalled.Process = &supervisor.Status{State: supervisor.StateRunning, UptimeSec: 40, Stalled: true}
	if f, _ := lossRun(&w, lossT0.Add(2*time.Second), 45*time.Second, lossIn(false, false, stalled)); !f.IsZero() {
		t.Fatal("a reconnect inside the wait fired")
	}
	w.step(lossT0.Add(48*time.Second), lossIn(true, false, d))
	if f, _ := lossRun(&w, lossT0.Add(50*time.Second), 5*time.Minute, lossIn(true, false, d)); !f.IsZero() {
		t.Fatal("fired with the ingest back")
	}
}

// GUARD 3b: A FAILOVER HANDOVER IS NOT A LOSS. The primary is gone for hours
// and the backup is carrying the show; the selector's output keeps moving.
// Mutation: ignore backupLive. Observed to fail with "fired while the backup
// was carrying".
func TestAutoStopIgnoresAFailoverToTheBackup(t *testing.T) {
	var w lossWatch
	d := lossDelivering(1)
	w.step(lossT0, lossIn(true, false, d))
	if f, _ := lossRun(&w, lossT0.Add(2*time.Second), 4*time.Hour, lossIn(false, true, d)); !f.IsZero() {
		t.Fatal("fired while the backup was carrying")
	}
	// The backup then dies too: now both are gone, and the wait runs from there.
	at := lossT0.Add(4*time.Hour + 2*time.Second)
	f, _ := lossRun(&w, at, 3*time.Minute, lossIn(false, false, d))
	if f.IsZero() || f.Sub(at) < lossWait {
		t.Fatalf("both ingests lost: fired at +%v, want at least %v after the backup died", f.Sub(at), lossWait)
	}
}

// GUARD 3c: A SOURCE SWITCH (the retireFeed path) tears the selector's feed
// down and starts another. The destinations read through it and stall for the
// seam, but neither ingest hub is touched, so the sweep's inputs do not move
// and nothing is lost. The same switch to a slate or playlist with no encoder
// IS a loss -- they are not a stream -- and fires.
func TestAutoStopIgnoresASourceSwitchButNotASlate(t *testing.T) {
	var w lossWatch
	d := lossDelivering(1)
	w.step(lossT0, lossIn(true, false, d))
	seam := d
	seam.Process = &supervisor.Status{State: supervisor.StateReconnecting, UptimeSec: 0}
	seam.Enabled = true
	// Primary still arriving while the destination restarts around the seam, for
	// longer than the wait: nothing about it is a loss.
	if f, _ := lossRun(&w, lossT0.Add(2*time.Second), 3*lossWait, lossIn(true, false, seam)); !f.IsZero() {
		t.Fatal("a source switch fired")
	}
	// Encoder gone, slate on air: the selector's output is moving, which is not
	// an input here. It fires after the wait.
	at := lossT0.Add(time.Minute)
	w.step(at, lossIn(true, false, d))
	f, _ := lossRun(&w, at.Add(2*time.Second), 3*time.Minute, lossIn(false, false, d))
	if f.IsZero() {
		t.Fatal("a slate on air with no encoder was treated as a stream")
	}
}

// The stop reaches this programme's enabled, on-air destinations and nothing
// else: a disabled one and one with no process are not "live".
func TestAutoStopListsOnlyLiveDestinations(t *testing.T) {
	var w lossWatch
	off := lossDelivering(3)
	off.Enabled = false
	failed := DestStatus{ID: 4, Enabled: true, Process: &supervisor.Status{State: supervisor.StateFailed}}
	w.step(lossT0, lossIn(true, false, lossDelivering(1), off, failed))
	_, ids := lossRun(&w, lossT0.Add(2*time.Second), 3*time.Minute, lossIn(false, false, lossDelivering(1), off, failed))
	if !reflect.DeepEqual(ids, []int64{1}) {
		t.Errorf("listed %v, want [1]", ids)
	}
}

// A destination holding a broadcast delay buffer is enabled with no process
// yet, and is part of what the stop reaches.
func TestAutoStopIncludesADestinationFillingItsDelay(t *testing.T) {
	var w lossWatch
	buf := DestStatus{ID: 5, Enabled: true, Delay: &DelayStatus{Phase: DelayBuffering}}
	w.step(lossT0, lossIn(true, false, lossDelivering(1), buf))
	_, ids := lossRun(&w, lossT0.Add(2*time.Second), 3*time.Minute, lossIn(false, false, lossDelivering(1), buf))
	if !reflect.DeepEqual(ids, []int64{1, 5}) {
		t.Errorf("listed %v, want [1 5]", ids)
	}
}

// A programme with no ingest that could be lost resets the watch: a source the
// operator disabled while armed is not an outage.
func TestAutoStopResetsWhenTheIngestIsNoLongerConfigured(t *testing.T) {
	var w lossWatch
	d := lossDelivering(1)
	w.step(lossT0, lossIn(true, false, d))
	off := lossIn(false, false, d)
	off.configured = false
	if f, _ := lossRun(&w, lossT0.Add(2*time.Second), time.Hour, off); !f.IsZero() {
		t.Fatal("fired for a programme with no ingest")
	}
	if f, _ := lossRun(&w, lossT0.Add(time.Hour), time.Hour, lossIn(false, false, d)); !f.IsZero() {
		t.Fatal("it stayed armed across a disabled spell")
	}
}

// ---------------------------------------------------------------------------
// The engine wiring: settings -> handler.

type lossRecorder struct {
	mu   sync.Mutex
	hits []IngestLoss
}

func (r *lossRecorder) handle(l IngestLoss) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hits = append(r.hits, l)
}

func (r *lossRecorder) n() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.hits) }

func armedEngine(t *testing.T, as db.AutoStopSettings) (*Engine, *lossRecorder) {
	t.Helper()
	e, _ := loggedEngine(t)
	rec := &lossRecorder{}
	e.SetIngestLossHandler(rec.handle)
	e.mu.Lock()
	e.settings.Ingest.Mode = db.IngestSRT
	e.settings.Ingest.AutoStop = as
	e.sourceDisabled = false
	e.mu.Unlock()
	e.lossDests = func() []DestStatus { return []DestStatus{lossDelivering(7)} }
	return e, rec
}

func lossSweep(e *Engine, from time.Time, d time.Duration, primary bool) {
	for at := from; at.Sub(from) <= d; at = at.Add(2 * time.Second) {
		e.sweepIngestLoss(at, primary)
	}
}

// The default settings never fire, and neither does an unacknowledged row
// written by hand: both halves arm it.
//
// Mutation: drop `&& a.Acknowledged` from Armed. Observed to fail with "an
// unacknowledged setting stopped a destination".
func TestAnEngineWithAutoStopOffOrUnacknowledgedStopsNothing(t *testing.T) {
	for name, as := range map[string]db.AutoStopSettings{
		"default":        {},
		"unacknowledged": {Enabled: true, WaitSeconds: 60},
	} {
		e, rec := armedEngine(t, as)
		e.sweepIngestLoss(lossT0, true)
		lossSweep(e, lossT0.Add(2*time.Second), 3*time.Hour, false)
		if rec.n() != 0 {
			t.Errorf("%s: an unacknowledged setting stopped a destination", name)
		}
	}
}

// The wiring: an armed engine, seen delivering, then silent, calls the handler
// once, after its configured wait, naming its own programme and destinations.
func TestAnArmedEngineCallsTheHandlerAfterTheWait(t *testing.T) {
	e, rec := armedEngine(t, db.AutoStopSettings{Enabled: true, Acknowledged: true, WaitSeconds: 90})
	e.sweepIngestLoss(lossT0, true)
	lossSweep(e, lossT0.Add(2*time.Second), 85*time.Second, false)
	if rec.n() != 0 {
		t.Fatal("fired before the wait")
	}
	lossSweep(e, lossT0.Add(88*time.Second), 20*time.Second, false)
	if rec.n() != 1 {
		t.Fatalf("handler called %d times, want 1", rec.n())
	}
	got := rec.hits[0]
	if got.SourceID != e.sourceID || !reflect.DeepEqual(got.Destinations, []int64{7}) || got.Wait != 90*time.Second {
		t.Errorf("handed %+v", got)
	}
}

// A programme with failover on never acts inside the selector's grace: the
// wait is lifted to the grace plus the clearance.
//
// Mutation: pass 0 as the failover grace to EffectiveWait. Observed to fail
// with "fired at 90s with a 200s failover grace".
func TestTheWaitClearsTheConfiguredFailoverGrace(t *testing.T) {
	e, rec := armedEngine(t, db.AutoStopSettings{Enabled: true, Acknowledged: true, WaitSeconds: 90})
	e.mu.Lock()
	e.settings.Failover.Enabled = true
	e.settings.Failover.GraceSeconds = 200
	e.mu.Unlock()
	e.sweepIngestLoss(lossT0, true)
	lossSweep(e, lossT0.Add(2*time.Second), 200*time.Second, false)
	if rec.n() != 0 {
		t.Fatal("fired at 90s with a 200s failover grace")
	}
	lossSweep(e, lossT0.Add(204*time.Second), 60*time.Second, false)
	if rec.n() != 1 {
		t.Fatalf("handler called %d times after grace+margin, want 1", rec.n())
	}
}

// FINDING 4: DESTINATIONS IN BACKOFF DO NOT DISARM THE WATCH. A dead ingest
// starves the platform connections, every destination goes into backoff, and a
// sweep with nothing carrying must not forget the loss: the stop still comes at
// the wait, and reaches those destinations.
//
// Mutation: reset the watch when nothing is on air (the old `len(onAir)==0`).
// Observed to fail with "the fallback never fired with every destination in
// backoff".
func TestAutoStopStillFiresWhenEveryDestinationIsInBackoff(t *testing.T) {
	var w lossWatch
	d := lossDelivering(1)
	w.step(lossT0, lossIn(true, false, d, lossDelivering(2)))
	backoff := func(id int64) DestStatus {
		return DestStatus{ID: id, Enabled: true, Process: &supervisor.Status{State: supervisor.StateStarting}}
	}
	at, ids := lossRun(&w, lossT0.Add(2*time.Second), 3*time.Minute, lossIn(false, false, backoff(1), backoff(2)))
	if at.IsZero() {
		t.Fatal("the fallback never fired with every destination in backoff")
	}
	if want := lossT0.Add(2*time.Second + lossWait); !at.Equal(want) {
		t.Errorf("fired at +%v, want +%v", at.Sub(lossT0), want.Sub(lossT0))
	}
	if !reflect.DeepEqual(ids, []int64{1, 2}) {
		t.Errorf("stopped %v, want [1 2]", ids)
	}
	// ...and a programme with nothing enabled at all still disarms, and an
	// armed-then-never-delivering one never fires at boot (guards 1 above).
	w = lossWatch{}
	w.step(lossT0, lossIn(true, false, d))
	off := d
	off.Enabled = false
	w.step(lossT0.Add(2*time.Second), lossIn(false, false, off))
	if f, _ := lossRun(&w, lossT0.Add(4*time.Second), time.Hour, lossIn(false, false, d)); !f.IsZero() {
		t.Fatal("fired after every destination had been switched off")
	}
}

// FINDING 8: A CHANGE OF THE WAIT RESTARTS THE CLOCK. Lowering the wait during
// an outage must not fire on the next sweep because of time counted against the
// old wait.
//
// Mutation: drop the `w.wait != in.wait` restart in step. Observed to fail with
// "fired on the sweep after the wait was lowered".
func TestAutoStopLoweringTheWaitMidOutageRestartsTheClock(t *testing.T) {
	var w lossWatch
	d := lossDelivering(1)
	long := lossIn(true, false, d)
	long.wait = 10 * time.Minute
	w.step(lossT0, long)
	long.primaryLive = false
	for at := lossT0.Add(2 * time.Second); at.Before(lossT0.Add(5 * time.Minute)); at = at.Add(2 * time.Second) {
		w.step(at, long)
	}
	short := lossIn(false, false, d) // 2 minutes, already exceeded by the outage so far
	at := lossT0.Add(5 * time.Minute)
	if _, _, fire := w.step(at, short); fire {
		t.Fatal("fired on the sweep after the wait was lowered")
	}
	f, _ := lossRun(&w, at.Add(2*time.Second), 3*time.Minute, short)
	if f.IsZero() || f.Sub(at) < lossWait {
		t.Fatalf("fired at +%v after the change, want a fresh %v", f.Sub(at), lossWait)
	}
}

// FINDING 6: SWITCHING THE FEATURE OFF RESETS THE CLOCK. Lost for wait-10s,
// toggled off, toggled on: the old count is gone, and the watch is not armed
// again until the ingest has arrived with something delivering.
//
// Mutation: remove `e.lossW.reset()` from the not-armed branch of
// sweepIngestLossWith. Observed to fail with "fired from a clock that was
// switched off".
func TestTogglingAutoStopOffResetsTheLossClock(t *testing.T) {
	armed := db.AutoStopSettings{Enabled: true, Acknowledged: true, WaitSeconds: 90}
	e, rec := armedEngine(t, armed)
	e.sweepIngestLoss(lossT0, true)
	lossSweep(e, lossT0.Add(2*time.Second), 80*time.Second, false) // wait - 10s
	if rec.n() != 0 {
		t.Fatal("fired early")
	}
	set := func(as db.AutoStopSettings) {
		e.mu.Lock()
		e.settings.Ingest.AutoStop = as
		e.mu.Unlock()
	}
	at := lossT0.Add(84 * time.Second)
	set(db.AutoStopSettings{})
	e.sweepIngestLoss(at, false) // off
	set(armed)
	// On again, ingest still gone: a fresh wait is needed, and with nothing seen
	// arriving since it is never armed, so it does not fire at all.
	lossSweep(e, at.Add(2*time.Second), 10*time.Minute, false)
	if rec.n() != 0 {
		t.Fatal("fired from a clock that was switched off")
	}
	// Once the ingest has arrived again, a loss is judged from scratch.
	back := at.Add(11 * time.Minute)
	e.sweepIngestLoss(back, true)
	lossSweep(e, back.Add(2*time.Second), 80*time.Second, false)
	if rec.n() != 0 {
		t.Fatal("fired before a fresh wait")
	}
	lossSweep(e, back.Add(82*time.Second), 20*time.Second, false)
	if rec.n() != 1 {
		t.Fatalf("handler called %d times after a fresh wait, want 1", rec.n())
	}
}

// FINDING 8, PULL SOURCES: the 60s floor reasons about a listener's respawn
// backoff; a pull source may wait up to its reconnect delay between attempts,
// and an outage inside that is the source retrying.
//
// Mutation: pass 0 for the pull reconnect in sweepIngestLossWith. Observed to
// fail with "a pull source fired inside its reconnect delay".
func TestAutoStopWaitsOutAPullSourcesReconnectDelay(t *testing.T) {
	e, rec := armedEngine(t, db.AutoStopSettings{Enabled: true, Acknowledged: true, WaitSeconds: 90})
	e.mu.Lock()
	e.settings.Ingest.Mode = db.IngestPull
	e.settings.Ingest.Pull.ReconnectDelayMaxSeconds = 600
	e.ingest = &supervisor.Process{} // a pull source has a child; the sweep only asks whether there is one
	e.mu.Unlock()
	e.sweepIngestLoss(lossT0, true)
	lossSweep(e, lossT0.Add(2*time.Second), 620*time.Second, false)
	if rec.n() != 0 {
		t.Fatal("a pull source fired inside its reconnect delay")
	}
	lossSweep(e, lossT0.Add(622*time.Second), 40*time.Second, false)
	if rec.n() != 1 {
		t.Fatalf("handler called %d times after reconnect delay + margin, want 1", rec.n())
	}
}

// The run-time liveness the host re-checks is the engine's own last sweep.
//
// Mutation: drop the lossLive.Store in sweepIngestLossWith. Observed to fail
// with "IngestArriving did not follow the ingest".
func TestIngestArrivingFollowsTheLastSweep(t *testing.T) {
	e, _ := armedEngine(t, db.AutoStopSettings{Enabled: true, Acknowledged: true})
	e.sweepIngestLoss(lossT0, true)
	if !e.IngestArriving() {
		t.Fatal("IngestArriving did not follow the ingest")
	}
	e.sweepIngestLoss(lossT0.Add(2*time.Second), false)
	if e.IngestArriving() {
		t.Fatal("IngestArriving stayed true after the ingest stopped")
	}
}

// FINDING 7: THE WIRING. observeLoop's per-tick body is observeTick(now, rx):
// a hub whose byte counter moves and then stops drives the fallback through the
// tick, once, with ONE status snapshot per sweep.
//
// Mutations: delete the sweepIngestLossWith call in observeTick (observed to
// fail with "the handler was never called from the observe tick"); read
// e.Status() again in alertSnapshot instead of using the sweep's (observed to
// fail with "status snapshots per sweep").
func TestTheObserveTickDrivesTheFallbackWithOneStatusPerSweep(t *testing.T) {
	e, rec := armedEngine(t, db.AutoStopSettings{Enabled: true, Acknowledged: true, WaitSeconds: 60})
	e.lossDests = nil
	var statuses int
	e.sweepStatus = func() Status {
		statuses++
		return Status{Destinations: []DestStatus{lossDelivering(7)}}
	}
	realBefore := e.statusCalls.Load()
	st := observeState{firstRx: true}
	at := lossT0
	rx := uint64(1000)
	tick := func() { e.observeTick(at, rx, &st); at = at.Add(2 * time.Second) }
	tick() // first sweep: nothing to compare to
	for i := 0; i < 5; i++ {
		rx += 500
		tick() // arriving
	}
	for i := 0; i < 40; i++ { // 80s of silence, past the 60s floor
		tick()
	}
	if rec.n() != 1 {
		t.Fatalf("the handler was never called from the observe tick (called %d times)", rec.n())
	}
	if rec.hits[0].Destinations[0] != 7 {
		t.Errorf("handed %+v", rec.hits[0])
	}
	if sweeps := 1 + 5 + 40; statuses != sweeps {
		t.Errorf("%d status snapshots per sweep over %d sweeps, want exactly 1 each", statuses, sweeps)
	}
	if n := e.statusCalls.Load() - realBefore; n != 0 {
		t.Errorf("%d extra status snapshots taken outside the sweep's one", n)
	}
}
