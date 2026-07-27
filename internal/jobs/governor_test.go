package jobs

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------- test rig

// fakeSuspender stands in for a processor that can genuinely stop mid-run.
type fakeSuspender struct {
	mu        sync.Mutex
	suspended bool
	suspends  int
	resumes   int
	err       error
}

func (f *fakeSuspender) Suspend() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.suspends++
	f.suspended = true
	return nil
}

func (f *fakeSuspender) Resume() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumes++
	f.suspended = false
	return nil
}

func (f *fakeSuspender) is() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.suspended
}

// governorPolicy is a policy with every gate off but the one a test switches
// on, so a failure names one cause rather than several.
func governorPolicy() Policy {
	return Policy{
		Enabled:        true,
		YieldToStream:  false,
		Default:        KindPolicy{Mode: ModeDeferred},
		Kinds:          map[Kind]KindPolicy{},
		CPU:            CPUPolicy{},
		GPU:            GPUPolicy{AvoidWhenStreaming: true},
		Power:          PowerPolicy{},
		DeferFor:       30 * time.Second,
		ManualDeferFor: 30 * time.Minute,
		IngestLinger:   30 * time.Second,
	}
}

// testGovernor wires a governor to a real queue over the memory store, so the
// deferral assertions run against the same Defer path production uses.
func testGovernor(t *testing.T, p Policy, s Sensors) (*Governor, *Queue, *memStore, *fakeClock) {
	t.Helper()
	clk := newClock()
	st := newMemStore(clk.Now)
	q := New(quietLog(), st, WithClock(clk.Now), WithProgressInterval(0))
	g := NewGovernor(quietLog(), q,
		WithPolicy(p), WithSensors(s), WithGovernorClock(clk.Now),
		WithNiceTools(NiceTools{}))
	return g, q, st, clk
}

func stateOf(t *testing.T, st *memStore, id int64) State {
	t.Helper()
	return getJob(t, st, id).State
}

// ----------------------------------------------------------------- windows

func TestWindowContainsHandlesWrapAroundMidnightAndWeekdays(t *testing.T) {
	// 2026-03-01 is a Sunday, 2026-03-02 a Monday, 2026-03-07 a Saturday.
	at := func(day, hour, min int) time.Time {
		return time.Date(2026, 3, day, hour, min, 0, 0, time.UTC)
	}
	overnight := Window{StartMinutes: 2 * 60, EndMinutes: 6 * 60}
	wrapped := Window{StartMinutes: 22 * 60, EndMinutes: 6 * 60}
	satNight := Window{StartMinutes: 22 * 60, EndMinutes: 6 * 60, Days: []time.Weekday{time.Saturday}}
	allDay := Window{StartMinutes: 0, EndMinutes: MinutesPerDay}
	zoned := Window{TZ: "America/Denver", StartMinutes: 2 * 60, EndMinutes: 6 * 60}

	tests := []struct {
		name string
		w    Window
		t    time.Time
		want bool
	}{
		{"inside a plain window", overnight, at(1, 3, 0), true},
		{"on the opening edge is inside", overnight, at(1, 2, 0), true},
		{"on the closing edge is outside", overnight, at(1, 6, 0), false},
		{"before a plain window", overnight, at(1, 1, 59), false},
		{"after a plain window", overnight, at(1, 6, 1), false},

		{"late evening is inside a wrapped window", wrapped, at(1, 23, 0), true},
		{"early morning is inside a wrapped window", wrapped, at(1, 5, 59), true},
		{"the afternoon gap is outside a wrapped window", wrapped, at(1, 14, 0), false},

		{"saturday night opens the window", satNight, at(7, 23, 0), true},
		{"sunday morning is still saturday's window", satNight, at(8, 3, 0), true},
		{"sunday night does not open it", satNight, at(8, 23, 0), false},
		{"saturday morning is outside, the window has not opened yet", satNight, at(7, 3, 0), false},

		{"a full day window is always open", allDay, at(2, 13, 37), true},

		// 03:00 in Denver is 10:00 UTC in March; a window that read the clock
		// in the server's zone would get this exactly backwards.
		{"a zoned window is read in its own zone", zoned, at(2, 10, 0), true},
		{"a zoned window is not read in utc", zoned, at(2, 3, 0), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.w.Contains(tc.t); got != tc.want {
				t.Errorf("Contains(%s) = %v, want %v", tc.t.Format(time.RFC3339), got, tc.want)
			}
		})
	}
}

func TestWindowValidateRejectsWhatCannotBeEvaluated(t *testing.T) {
	tests := []struct {
		name    string
		w       Window
		wantErr bool
	}{
		{name: "a plain window", w: Window{StartMinutes: 120, EndMinutes: 360}},
		{name: "midnight to midnight", w: Window{StartMinutes: 0, EndMinutes: MinutesPerDay}},
		{name: "a wrapped window", w: Window{StartMinutes: 1320, EndMinutes: 360}},
		{name: "a negative start", w: Window{StartMinutes: -1, EndMinutes: 360}, wantErr: true},
		{name: "a start past midnight", w: Window{StartMinutes: MinutesPerDay, EndMinutes: 360}, wantErr: true},
		{name: "an end past midnight", w: Window{StartMinutes: 60, EndMinutes: MinutesPerDay + 1}, wantErr: true},
		{name: "a zero-length window", w: Window{StartMinutes: 120, EndMinutes: 120}, wantErr: true},
		{name: "an unknown weekday", w: Window{StartMinutes: 1, EndMinutes: 2, Days: []time.Weekday{9}}, wantErr: true},
		{name: "an unknown zone", w: Window{TZ: "Mars/Olympus", StartMinutes: 1, EndMinutes: 2}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.w.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestWindowWithAnUnloadableZoneFailsOpen(t *testing.T) {
	// The restrictive answer here would be a black hole: work that never runs
	// because a zone string was mistyped, with nothing to show for it.
	w := Window{TZ: "Nowhere/AtAll", StartMinutes: 2 * 60, EndMinutes: 3 * 60}
	if !w.Contains(time.Date(2026, 3, 1, 18, 0, 0, 0, time.UTC)) {
		t.Error("Contains() = false for an unloadable zone, want true so the gate fails open")
	}
}

func TestInAnyWindowWithNoWindowsMeansAlways(t *testing.T) {
	if !InAnyWindow(nil, time.Date(2026, 3, 1, 13, 0, 0, 0, time.UTC)) {
		t.Error("InAnyWindow(nil) = false, want true: an unfinished schedule must not be a black hole")
	}
}

// -------------------------------------------------------------- the matrix

func TestDecideAppliesEachGateToTheModesItGoverns(t *testing.T) {
	now := time.Date(2026, 3, 1, 13, 0, 0, 0, time.UTC)
	night := []Window{{StartMinutes: 2 * 60, EndMinutes: 6 * 60}}

	yield := governorPolicy()
	yield.YieldToStream = true

	tests := []struct {
		name         string
		kp           KindPolicy
		policy       Policy
		gates        Gates
		wantStart    bool
		wantContinue bool
		wantReason   string
	}{
		{
			name: "an idle machine allows deferred work",
			kp:   KindPolicy{Mode: ModeDeferred}, policy: yield, gates: Gates{},
			wantStart: true, wantContinue: true, wantReason: ReasonAllowed,
		},
		{
			name: "a live ingest holds deferred work back and stops what is running",
			kp:   KindPolicy{Mode: ModeDeferred}, policy: yield, gates: Gates{IngestLive: true},
			wantStart: false, wantContinue: false, wantReason: ReasonIngestLive,
		},
		{
			name: "a realtime kind is not held back by the ingest gate",
			kp:   KindPolicy{Mode: ModeRealtime}, policy: yield, gates: Gates{IngestLive: true},
			wantStart: true, wantContinue: true, wantReason: ReasonRealtime,
		},
		{
			name: "a kind that opts out of the ingest gate keeps running",
			kp:   KindPolicy{Mode: ModeDeferred, IgnoreIngest: true}, policy: yield, gates: Gates{IngestLive: true},
			wantStart: true, wantContinue: true, wantReason: ReasonAllowed,
		},
		{
			name: "yielding switched off leaves a live ingest ungated",
			kp:   KindPolicy{Mode: ModeDeferred}, policy: governorPolicy(), gates: Gates{IngestLive: true},
			wantStart: true, wantContinue: true, wantReason: ReasonAllowed,
		},
		{
			name: "manual work waits for a human but is not stopped mid-run",
			kp:   KindPolicy{Mode: ModeManual}, policy: yield, gates: Gates{},
			wantStart: false, wantContinue: true, wantReason: ReasonManualOnly,
		},
		{
			name: "scheduled work outside its window starts nothing and finishes what it has",
			kp:   KindPolicy{Mode: ModeScheduled, Windows: night}, policy: yield, gates: Gates{},
			wantStart: false, wantContinue: true, wantReason: ReasonOutsideWindow,
		},
		{
			name: "the instantaneous cpu ceiling stops starts but lets running work finish",
			kp:   KindPolicy{Mode: ModeDeferred}, policy: yield, gates: Gates{CPUOverCeiling: true},
			wantStart: false, wantContinue: true, wantReason: ReasonCPUBusy,
		},
		{
			name: "a sustained cpu ceiling also stops running work",
			kp:   KindPolicy{Mode: ModeDeferred}, policy: yield,
			gates:     Gates{CPUOverCeiling: true, CPUSustained: true},
			wantStart: false, wantContinue: false, wantReason: ReasonCPUBusy,
		},
		{
			name: "a busy gpu only gates work that would use it",
			kp:   KindPolicy{Mode: ModeDeferred, UsesGPU: true}, policy: yield, gates: Gates{GPUBusy: true},
			wantStart: false, wantContinue: false, wantReason: ReasonGPUBusy,
		},
		{
			name: "a busy gpu does not gate cpu-only work",
			kp:   KindPolicy{Mode: ModeDeferred}, policy: yield, gates: Gates{GPUBusy: true},
			wantStart: true, wantContinue: true, wantReason: ReasonAllowed,
		},
		{
			name: "battery holds new work back and lets running work finish",
			kp:   KindPolicy{Mode: ModeDeferred}, policy: yield, gates: Gates{OnBattery: true},
			wantStart: false, wantContinue: true, wantReason: ReasonOnBattery,
		},
		{
			name: "the thermal stop reaches realtime work too",
			kp:   KindPolicy{Mode: ModeRealtime}, policy: yield, gates: Gates{TooHot: true},
			wantStart: false, wantContinue: false, wantReason: ReasonTooHot,
		},
		{
			name: "the thermal stop outranks every other explanation",
			kp:   KindPolicy{Mode: ModeDeferred}, policy: yield,
			gates:     Gates{TooHot: true, IngestLive: true, CPUOverCeiling: true},
			wantStart: false, wantContinue: false, wantReason: ReasonTooHot,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := decide("k", tc.kp, tc.policy.Normalized(), tc.gates, now)
			if got.Start != tc.wantStart || got.Continue != tc.wantContinue || got.Reason != tc.wantReason {
				t.Errorf("decide() = {start:%v continue:%v reason:%q}, want {start:%v continue:%v reason:%q}",
					got.Start, got.Continue, got.Reason, tc.wantStart, tc.wantContinue, tc.wantReason)
			}
		})
	}
}

// --------------------------------------------------------- ingest gating

func TestGovernorDefersDeferredWorkWhileTheIngestIsLiveButNotRealtimeWork(t *testing.T) {
	live := true
	p := governorPolicy()
	p.YieldToStream = true
	p.Kinds["captions"] = KindPolicy{Mode: ModeRealtime}
	p.Kinds["transcribe"] = KindPolicy{Mode: ModeDeferred}

	g, _, st, _ := testGovernor(t, p, Sensors{IngestLive: func() bool { return live }})
	heavy := st.seed(Job{Kind: "transcribe", State: StateQueued, MaxAttempts: 3})
	rt := st.seed(Job{Kind: "captions", State: StateQueued, MaxAttempts: 3})

	g.Tick(g.now())

	if got := stateOf(t, st, heavy.ID); got != StateDeferred {
		t.Errorf("deferred-mode job state = %q, want %q while the ingest is live", got, StateDeferred)
	}
	if got := stateOf(t, st, rt.ID); got != StateQueued {
		t.Errorf("realtime job state = %q, want %q: the ingest gate must not touch realtime work", got, StateQueued)
	}
}

func TestGovernorHoldsTheIngestGateForTheLingerThenReleasesIt(t *testing.T) {
	live := true
	p := governorPolicy()
	p.YieldToStream = true

	g, _, st, clk := testGovernor(t, p, Sensors{IngestLive: func() bool { return live }})
	j := st.seed(Job{Kind: "transcribe", State: StateQueued, MaxAttempts: 3})

	if snap := g.Tick(clk.Now()); !snap.Gates.IngestLive {
		t.Fatal("gates.IngestLive = false while the ingest is delivering")
	}
	if got := stateOf(t, st, j.ID); got != StateDeferred {
		t.Fatalf("state = %q, want %q", got, StateDeferred)
	}

	// The stream stops. The gate must NOT open immediately: an encoder that
	// dropped is usually about to reconnect.
	live = false
	clk.Advance(10 * time.Second)
	if snap := g.Tick(clk.Now()); !snap.Gates.IngestLive {
		t.Error("gates.IngestLive = false 10s after the stream stopped, want true for the linger")
	}

	clk.Advance(25 * time.Second)
	snap := g.Tick(clk.Now())
	if snap.Gates.IngestLive {
		t.Error("gates.IngestLive = true 35s after the stream stopped, want false once the linger passed")
	}
	if len(snap.Deferred) != 0 {
		t.Errorf("Deferred = %v, want nothing deferred once the stream is gone", snap.Deferred)
	}
}

// -------------------------------------------------------------- suspension

func TestGovernorSuspendsAKindThatCanStopAndOnlyReportsTheOnesThatCannot(t *testing.T) {
	live := true
	p := governorPolicy()
	p.YieldToStream = true

	g, _, st, clk := testGovernor(t, p, Sensors{IngestLive: func() bool { return live }})
	sus := &fakeSuspender{}
	if err := g.RegisterSuspender("transcribe", sus); err != nil {
		t.Fatalf("RegisterSuspender: %v", err)
	}
	st.seed(Job{Kind: "transcribe", State: StateRunning, MaxAttempts: 3})
	st.seed(Job{Kind: "proxy", State: StateRunning, MaxAttempts: 3})

	snap := g.Tick(clk.Now())
	if !sus.is() {
		t.Error("a suspendable kind was not suspended while the ingest is live")
	}
	if !reflect.DeepEqual(snap.Suspended, []Kind{"transcribe"}) {
		t.Errorf("Suspended = %v, want [transcribe]", snap.Suspended)
	}
	// The honest half: a kind that cannot stop is reported as finishing rather
	// than described as paused.
	if !reflect.DeepEqual(snap.Yielding, []Kind{"proxy"}) {
		t.Errorf("Yielding = %v, want [proxy]", snap.Yielding)
	}
	if got := g.SuspensionFor("proxy"); got != SuspendFinish {
		t.Errorf("SuspensionFor(proxy) = %q, want %q", got, SuspendFinish)
	}
	if got := g.SuspensionFor("transcribe"); got != SuspendStop {
		t.Errorf("SuspensionFor(transcribe) = %q, want %q", got, SuspendStop)
	}

	live = false
	clk.Advance(time.Minute)
	g.Tick(clk.Now())
	if sus.is() {
		t.Error("a suspended kind was not resumed once the stream stopped")
	}
}

func TestGovernorRegisterSuspenderRefusesADuplicateKind(t *testing.T) {
	g, _, _, _ := testGovernor(t, governorPolicy(), Sensors{})
	if err := g.RegisterSuspender("transcribe", &fakeSuspender{}); err != nil {
		t.Fatalf("first RegisterSuspender: %v", err)
	}
	if err := g.RegisterSuspender("transcribe", &fakeSuspender{}); err == nil {
		t.Error("second RegisterSuspender for the same kind = nil, want an error")
	}
	if err := g.RegisterSuspender("", &fakeSuspender{}); err == nil {
		t.Error("RegisterSuspender with no kind = nil, want an error")
	}
	if err := g.RegisterSuspender("k", nil); err == nil {
		t.Error("RegisterSuspender with no suspender = nil, want an error")
	}
}

func TestGovernorRetriesASuspendThatFailedRatherThanBelievingIt(t *testing.T) {
	p := governorPolicy()
	p.YieldToStream = true
	g, _, st, clk := testGovernor(t, p, Sensors{IngestLive: func() bool { return true }})
	sus := &fakeSuspender{err: errMemNotFound}
	if err := g.RegisterSuspender("transcribe", sus); err != nil {
		t.Fatalf("RegisterSuspender: %v", err)
	}
	st.seed(Job{Kind: "transcribe", State: StateRunning, MaxAttempts: 3})

	if snap := g.Tick(clk.Now()); len(snap.Suspended) != 0 {
		t.Fatalf("Suspended = %v after a failed Suspend, want nothing claimed", snap.Suspended)
	}
	sus.mu.Lock()
	sus.err = nil
	sus.mu.Unlock()
	clk.Advance(5 * time.Second)
	if snap := g.Tick(clk.Now()); len(snap.Suspended) != 1 {
		t.Errorf("Suspended = %v on the next tick, want the suspend to be retried", snap.Suspended)
	}
}

// --------------------------------------------------------------- cpu gate

func TestGovernorCPUCeilingStopsStartsAtOnceAndRunningWorkOnlyWhenSustained(t *testing.T) {
	cpu := 95.0
	p := governorPolicy()
	p.CPU = CPUPolicy{CeilingPercent: 80, ResumePercent: 50, Sustained: 30 * time.Second, Settle: 20 * time.Second}

	g, _, _, clk := testGovernor(t, p, Sensors{CPUPercent: func() float64 { return cpu }})

	snap := g.Tick(clk.Now())
	if !snap.Gates.CPUOverCeiling || snap.Gates.CPUSustained {
		t.Fatalf("gates = %+v, want over the ceiling but not yet sustained", snap.Gates)
	}

	clk.Advance(30 * time.Second)
	if snap := g.Tick(clk.Now()); !snap.Gates.CPUSustained {
		t.Error("gates.CPUSustained = false after 30s over the ceiling, want true")
	}

	// A dip into the dead band is not calm: it must not release running work.
	cpu = 60
	clk.Advance(time.Minute)
	if snap := g.Tick(clk.Now()); !snap.Gates.CPUSustained {
		t.Error("gates.CPUSustained = false in the dead band, want the hysteresis to hold")
	}

	cpu = 20
	g.Tick(clk.Now())
	if snap := g.Tick(clk.Now()); !snap.Gates.CPUSustained {
		t.Error("gates.CPUSustained = false the instant the load dropped, want it held until it settles")
	}
	clk.Advance(20 * time.Second)
	snap = g.Tick(clk.Now())
	if snap.Gates.CPUSustained || snap.Gates.CPUOverCeiling {
		t.Errorf("gates = %+v, want the cpu gate released once the machine settled", snap.Gates)
	}
}

func TestGovernorCPUGateFailsOpenWhenTheReadingIsUnavailable(t *testing.T) {
	tests := []struct {
		name   string
		sensor func() float64
	}{
		{name: "no sensor at all"},
		{name: "an unavailable reading", sensor: func() float64 { return -1 }},
		{name: "a nonsense reading", sensor: func() float64 { return math.NaN() }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := governorPolicy()
			p.CPU = CPUPolicy{CeilingPercent: 80, ResumePercent: 50}
			g, _, st, clk := testGovernor(t, p, Sensors{CPUPercent: tc.sensor})
			j := st.seed(Job{Kind: "transcribe", State: StateQueued, MaxAttempts: 3})

			snap := g.Tick(clk.Now())
			if snap.Gates.CPUOverCeiling || snap.Gates.CPUSustained {
				t.Errorf("gates = %+v, want the gate open when the machine cannot be read", snap.Gates)
			}
			if got := stateOf(t, st, j.ID); got != StateQueued {
				t.Errorf("state = %q, want %q: an unreadable cpu must not hold work back", got, StateQueued)
			}
		})
	}
}

// --------------------------------------------------------------- gpu gate

func TestGovernorGPUGateComesFromTheSensorOrTheManualSwitch(t *testing.T) {
	p := governorPolicy()
	p.Kinds["render"] = KindPolicy{Mode: ModeDeferred, UsesGPU: true}
	p.Kinds["transcribe"] = KindPolicy{Mode: ModeDeferred}

	g, _, st, clk := testGovernor(t, p, Sensors{})
	gpu := st.seed(Job{Kind: "render", State: StateQueued, MaxAttempts: 3})
	cpu := st.seed(Job{Kind: "transcribe", State: StateQueued, MaxAttempts: 3})

	if snap := g.Tick(clk.Now()); snap.Gates.GPUBusy {
		t.Fatal("gates.GPUBusy = true with nothing reporting it")
	}

	// The manual switch, which is the honest answer on a platform that cannot
	// be asked whether the encoder is using the GPU.
	g.SetGPUBusy(true)
	clk.Advance(time.Minute)
	snap := g.Tick(clk.Now())
	if !snap.Gates.GPUBusy {
		t.Fatal("gates.GPUBusy = false after SetGPUBusy(true)")
	}
	if got := stateOf(t, st, gpu.ID); got != StateDeferred {
		t.Errorf("gpu job state = %q, want %q", got, StateDeferred)
	}
	if got := stateOf(t, st, cpu.ID); got != StateQueued {
		t.Errorf("cpu-only job state = %q, want %q: it was never going to touch the gpu", got, StateQueued)
	}
}

// ------------------------------------------------------------ deferral care

func TestGovernorNeverBringsARetryBackoffForward(t *testing.T) {
	p := governorPolicy()
	p.YieldToStream = true
	g, _, st, clk := testGovernor(t, p, Sensors{IngestLive: func() bool { return true }})

	backoff := clk.Now().Add(10 * time.Minute)
	j := st.seed(Job{Kind: "transcribe", State: StateQueued, AvailableAt: backoff, MaxAttempts: 3})

	g.Tick(clk.Now())

	got := getJob(t, st, j.ID)
	if !got.AvailableAt.Equal(backoff) {
		t.Errorf("AvailableAt = %s, want the retry backoff %s left alone",
			got.AvailableAt.Format(time.RFC3339), backoff.Format(time.RFC3339))
	}
}

func TestGovernorRedefersOnlyWhenTheParkedInstantIsAboutToLapse(t *testing.T) {
	p := governorPolicy()
	p.YieldToStream = true
	g, _, st, clk := testGovernor(t, p, Sensors{IngestLive: func() bool { return true }})
	j := st.seed(Job{Kind: "transcribe", State: StateQueued, MaxAttempts: 3})

	first := g.Tick(clk.Now())
	if len(first.Deferred) != 1 {
		t.Fatalf("Deferred = %v on the first tick, want the job pushed back", first.Deferred)
	}
	if again := g.Tick(clk.Now()); len(again.Deferred) != 0 {
		t.Errorf("Deferred = %v on an immediate second tick, want no repeat write", again.Deferred)
	}

	clk.Advance(20 * time.Second)
	if lapsing := g.Tick(clk.Now()); len(lapsing.Deferred) != 1 {
		t.Errorf("Deferred = %v once the deferral is about to lapse, want it renewed", lapsing.Deferred)
	}
	if got := stateOf(t, st, j.ID); got != StateDeferred {
		t.Errorf("state = %q throughout, want %q", got, StateDeferred)
	}
}

func TestGovernorParksManualWorkUntilAHumanReleasesIt(t *testing.T) {
	p := governorPolicy()
	p.YieldToStream = true
	p.Kinds["clip"] = KindPolicy{Mode: ModeManual}
	live := false

	g, _, st, clk := testGovernor(t, p, Sensors{IngestLive: func() bool { return live }})
	j := st.seed(Job{Kind: "clip", State: StateQueued, MaxAttempts: 3})

	g.Tick(clk.Now())
	got := getJob(t, st, j.ID)
	if got.State != StateDeferred {
		t.Fatalf("state = %q, want %q for a manual kind nobody released", got.State, StateDeferred)
	}
	if want := clk.Now().Add(p.ManualDeferFor); !got.AvailableAt.Equal(want) {
		t.Errorf("AvailableAt = %s, want the long manual parking %s",
			got.AvailableAt.Format(time.RFC3339), want.Format(time.RFC3339))
	}

	g.Release(j.ID)
	clk.Advance(time.Hour)
	g.Tick(clk.Now())
	if got := stateOf(t, st, j.ID); got != StateDeferred {
		t.Errorf("state = %q after Release, want it left alone at %q so the queue can claim it",
			got, StateDeferred)
	}

	// A release is not a licence to compete with the broadcast.
	live = true
	clk.Advance(time.Minute)
	g.Tick(clk.Now())
	after := getJob(t, st, j.ID)
	if !after.AvailableAt.Equal(clk.Now().Add(p.DeferFor)) {
		t.Errorf("AvailableAt = %s, want a released job still held back by the live ingest",
			after.AvailableAt.Format(time.RFC3339))
	}
}

func TestGovernorForgetsReleasesForJobsThatHaveFinished(t *testing.T) {
	g, _, st, clk := testGovernor(t, governorPolicy(), Sensors{})
	j := st.seed(Job{Kind: "clip", State: StateQueued, MaxAttempts: 3})
	g.Release(j.ID)
	g.Tick(clk.Now())
	if !g.Released(j.ID) {
		t.Fatal("Released() = false for an active job that was released")
	}
	if err := st.FinishJob(j.ID, StateDone, "", clk.Now()); err != nil {
		t.Fatalf("FinishJob: %v", err)
	}
	g.Tick(clk.Now())
	if g.Released(j.ID) {
		t.Error("Released() = true for a finished job, want the flag forgotten")
	}
}

// ------------------------------------------------------------ global pause

func TestGovernorPausesTheQueueOnlyForTheMachineSafetyStop(t *testing.T) {
	power := PowerState{Known: true, Percent: 100, TempC: 45}
	p := governorPolicy()
	p.YieldToStream = true
	p.Power = PowerPolicy{ThermalCeilingC: 90}

	g, q, _, clk := testGovernor(t, p, Sensors{
		IngestLive: func() bool { return true },
		Power:      func() PowerState { return power },
	})

	g.Tick(clk.Now())
	if q.Paused() {
		t.Error("the queue is paused merely because an ingest is live; that is what deferral is for")
	}

	power.TempC = 95
	clk.Advance(time.Minute)
	if snap := g.Tick(clk.Now()); !snap.Gates.TooHot || !q.Paused() {
		t.Errorf("gates.TooHot = %v, Paused = %v; want the safety stop to pause everything",
			snap.Gates.TooHot, q.Paused())
	}

	power.TempC = 45
	clk.Advance(time.Minute)
	g.Tick(clk.Now())
	if q.Paused() {
		t.Error("the queue is still paused after the machine cooled down")
	}
}

func TestGovernorNeverUndoesAPauseAnOperatorApplied(t *testing.T) {
	g, q, _, clk := testGovernor(t, governorPolicy(), Sensors{})
	q.Pause()
	g.Tick(clk.Now())
	if !q.Paused() {
		t.Error("the governor resumed a queue it never paused; an operator's pause must survive it")
	}
}

func TestGovernorDisabledReleasesEverythingItWasHolding(t *testing.T) {
	power := PowerState{Known: true, Percent: 100, TempC: 99}
	p := governorPolicy()
	p.Power = PowerPolicy{ThermalCeilingC: 90}

	g, q, st, clk := testGovernor(t, p, Sensors{Power: func() PowerState { return power }})
	sus := &fakeSuspender{}
	if err := g.RegisterSuspender("transcribe", sus); err != nil {
		t.Fatalf("RegisterSuspender: %v", err)
	}
	st.seed(Job{Kind: "transcribe", State: StateRunning, MaxAttempts: 3})

	g.Tick(clk.Now())
	if !q.Paused() || !sus.is() {
		t.Fatalf("setup: Paused = %v, suspended = %v; want both held", q.Paused(), sus.is())
	}

	p.Enabled = false
	g.SetPolicy(p)
	clk.Advance(time.Minute)
	snap := g.Tick(clk.Now())
	if snap.Enabled {
		t.Error("Snapshot.Enabled = true for a disabled governor")
	}
	if q.Paused() {
		t.Error("a disabled governor left the queue paused")
	}
	if sus.is() {
		t.Error("a disabled governor left work suspended")
	}
}

// blindController is a queue whose listing always fails.
type blindController struct {
	*Queue
	err error
}

func (b blindController) List(Filter) ([]Job, error) { return nil, b.err }

func TestGovernorSurvivesAStoreThatCannotListWithoutStoppingTheQueue(t *testing.T) {
	// A listing that failed says nothing about whether the machine is busy, so
	// the restrictive answer — pause everything — would be the wrong one.
	clk := newClock()
	st := newMemStore(clk.Now)
	q := New(quietLog(), st, WithClock(clk.Now))
	g := NewGovernor(quietLog(), blindController{Queue: q, err: errMemNotFound},
		WithPolicy(governorPolicy()), WithGovernorClock(clk.Now), WithNiceTools(NiceTools{}))

	snap := g.Tick(clk.Now())
	if q.Paused() {
		t.Error("the queue is paused because a listing failed")
	}
	if !snap.Enabled {
		t.Error("Snapshot.Enabled = false; a failed listing is not a disabled governor")
	}
}

// ----------------------------------------------------------------- policy

func TestPolicyNormalizedFillsDefaultsAndKeepsTheHysteresis(t *testing.T) {
	tests := []struct {
		name  string
		in    Policy
		check func(*testing.T, Policy)
	}{
		{
			name: "an unknown mode becomes the default",
			in:   Policy{Default: KindPolicy{Mode: "whenever"}},
			check: func(t *testing.T, p Policy) {
				if p.Default.Mode != DefaultMode {
					t.Errorf("Default.Mode = %q, want %q", p.Default.Mode, DefaultMode)
				}
			},
		},
		{
			name: "a resume level at or above the ceiling is pulled below it",
			in:   Policy{CPU: CPUPolicy{CeilingPercent: 80, ResumePercent: 80}},
			check: func(t *testing.T, p Policy) {
				if p.CPU.ResumePercent >= p.CPU.CeilingPercent {
					t.Errorf("ResumePercent = %v, want strictly below the ceiling %v so the gate cannot chatter",
						p.CPU.ResumePercent, p.CPU.CeilingPercent)
				}
			},
		},
		{
			name: "a nice level past the kernel's ceiling is clamped",
			in:   Policy{NiceLevel: 99},
			check: func(t *testing.T, p Policy) {
				if p.NiceLevel != MaxNiceLevel {
					t.Errorf("NiceLevel = %d, want %d", p.NiceLevel, MaxNiceLevel)
				}
			},
		},
		{
			name: "zero durations become the defaults",
			in:   Policy{},
			check: func(t *testing.T, p Policy) {
				if p.DeferFor != DefaultDeferFor || p.ManualDeferFor != DefaultManualDeferFor {
					t.Errorf("DeferFor = %v, ManualDeferFor = %v; want the defaults", p.DeferFor, p.ManualDeferFor)
				}
			},
		},
		{
			name: "a per-kind mode is normalized too",
			in:   Policy{Kinds: map[Kind]KindPolicy{"k": {}}},
			check: func(t *testing.T, p Policy) {
				if p.Kinds["k"].Mode != DefaultMode {
					t.Errorf("Kinds[k].Mode = %q, want %q", p.Kinds["k"].Mode, DefaultMode)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { tc.check(t, tc.in.Normalized()) })
	}
}

func TestPolicyValidateRejectsWhatCannotBeEvaluated(t *testing.T) {
	tests := []struct {
		name    string
		p       Policy
		wantErr bool
	}{
		{name: "the defaults", p: DefaultPolicy()},
		{name: "an unknown default mode", p: Policy{Default: KindPolicy{Mode: "soon"}}, wantErr: true},
		{
			name:    "an unknown per-kind mode",
			p:       Policy{Default: KindPolicy{Mode: ModeDeferred}, Kinds: map[Kind]KindPolicy{"k": {Mode: "soon"}}},
			wantErr: true,
		},
		{
			name: "a window that never opens",
			p: Policy{Default: KindPolicy{Mode: ModeDeferred}, Kinds: map[Kind]KindPolicy{
				"k": {Mode: ModeScheduled, Windows: []Window{{StartMinutes: 60, EndMinutes: 60}}},
			}},
			wantErr: true,
		},
		{name: "a cpu ceiling over 100", p: Policy{Default: KindPolicy{Mode: ModeDeferred}, CPU: CPUPolicy{CeilingPercent: 101}}, wantErr: true},
		{name: "a nice level past the kernel ceiling", p: Policy{Default: KindPolicy{Mode: ModeDeferred}, NiceLevel: 20}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.p.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestPolicyForFallsBackToTheDefault(t *testing.T) {
	p := DefaultPolicy()
	p.Kinds["captions"] = KindPolicy{Mode: ModeRealtime}
	if got := p.For("captions").Mode; got != ModeRealtime {
		t.Errorf("For(captions) = %q, want %q", got, ModeRealtime)
	}
	if got := p.For("something-nobody-configured").Mode; got != ModeDeferred {
		t.Errorf("For(unknown) = %q, want %q", got, ModeDeferred)
	}
}

// ---------------------------------------------------------------- niceness

func TestNiceToolsWrapUsesWhateverIsInstalledAndNothingElse(t *testing.T) {
	both := NiceTools{Nice: "/usr/bin/nice", IONice: "/usr/bin/ionice"}
	tests := []struct {
		name     string
		tools    NiceTools
		level    int
		idleIO   bool
		wantName string
		wantArgs []string
	}{
		{
			name: "both tools wrap the command", tools: both, level: 10, idleIO: true,
			wantName: "/usr/bin/ionice",
			wantArgs: []string{"-c", "3", "/usr/bin/nice", "-n", "10", "ffmpeg", "-i", "in.mkv"},
		},
		{
			name: "nice alone drops cpu priority only", tools: NiceTools{Nice: "/usr/bin/nice"}, level: 10, idleIO: true,
			wantName: "/usr/bin/nice", wantArgs: []string{"-n", "10", "ffmpeg", "-i", "in.mkv"},
		},
		{
			name: "idle io switched off leaves ionice out", tools: both, level: 10, idleIO: false,
			wantName: "/usr/bin/nice", wantArgs: []string{"-n", "10", "ffmpeg", "-i", "in.mkv"},
		},
		{
			name: "level zero means no renice at all", tools: both, level: 0, idleIO: false,
			wantName: "ffmpeg", wantArgs: []string{"-i", "in.mkv"},
		},
		{
			name: "a level past the kernel ceiling is clamped", tools: NiceTools{Nice: "/usr/bin/nice"}, level: 99,
			wantName: "/usr/bin/nice", wantArgs: []string{"-n", "19", "ffmpeg", "-i", "in.mkv"},
		},
		{
			// Missing tools must never be the reason a job does not run.
			name: "neither tool leaves the command untouched", tools: NiceTools{}, level: 10, idleIO: true,
			wantName: "ffmpeg", wantArgs: []string{"-i", "in.mkv"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, args := tc.tools.Wrap(tc.level, tc.idleIO, "ffmpeg", []string{"-i", "in.mkv"})
			if name != tc.wantName || !reflect.DeepEqual(args, tc.wantArgs) {
				t.Errorf("Wrap() = %q %v, want %q %v", name, args, tc.wantName, tc.wantArgs)
			}
		})
	}
}

func TestGovernorNiceCommandAppliesThePolicyLevel(t *testing.T) {
	p := governorPolicy()
	p.NiceLevel = 12
	p.IdleIO = false
	clk := newClock()
	st := newMemStore(clk.Now)
	q := New(quietLog(), st, WithClock(clk.Now))
	g := NewGovernor(quietLog(), q, WithPolicy(p), WithGovernorClock(clk.Now),
		WithNiceTools(NiceTools{Nice: "/usr/bin/nice"}))

	name, args := g.NiceCommand("ffmpeg", "-i", "in.mkv")
	if name != "/usr/bin/nice" || !reflect.DeepEqual(args, []string{"-n", "12", "ffmpeg", "-i", "in.mkv"}) {
		t.Errorf("NiceCommand() = %q %v, want the child niced to 12", name, args)
	}
}

func TestDetectNiceToolsNeverFails(t *testing.T) {
	// Whatever this machine has, detection must return rather than error: an
	// absent nice(1) is a missing optimisation, not a broken install.
	tools := DetectNiceTools()
	if tools.Nice == "" && tools.Available() {
		t.Error("Available() = true with nothing detected")
	}
}

// ------------------------------------------------------------------- power

func TestReadPowerStateReadsSysfsAndStaysSilentWhenThereIsNothingToRead(t *testing.T) {
	write := func(t *testing.T, path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	tests := []struct {
		name  string
		build func(t *testing.T, root string)
		want  PowerState
	}{
		{
			name:  "a platform with no sysfs reports nothing",
			build: func(*testing.T, string) {},
			want:  PowerState{Known: false, Percent: -1, TempC: -1},
		},
		{
			name: "a discharging laptop",
			build: func(t *testing.T, root string) {
				write(t, filepath.Join(root, "power_supply/BAT0/type"), "Battery\n")
				write(t, filepath.Join(root, "power_supply/BAT0/status"), "Discharging\n")
				write(t, filepath.Join(root, "power_supply/BAT0/capacity"), "37\n")
			},
			want: PowerState{Known: true, OnBattery: true, Percent: 37, TempC: -1},
		},
		{
			name: "a full battery on mains is not on battery",
			build: func(t *testing.T, root string) {
				write(t, filepath.Join(root, "power_supply/BAT0/type"), "Battery\n")
				write(t, filepath.Join(root, "power_supply/BAT0/status"), "Not charging\n")
				write(t, filepath.Join(root, "power_supply/BAT0/capacity"), "100\n")
			},
			want: PowerState{Known: true, OnBattery: false, Percent: 100, TempC: -1},
		},
		{
			name: "live mains overrides a stale battery status",
			build: func(t *testing.T, root string) {
				write(t, filepath.Join(root, "power_supply/BAT0/type"), "Battery\n")
				write(t, filepath.Join(root, "power_supply/BAT0/status"), "Discharging\n")
				write(t, filepath.Join(root, "power_supply/BAT0/capacity"), "80\n")
				write(t, filepath.Join(root, "power_supply/ZZ_AC/type"), "Mains\n")
				write(t, filepath.Join(root, "power_supply/ZZ_AC/online"), "1\n")
			},
			want: PowerState{Known: true, OnBattery: false, Percent: 80, TempC: -1},
		},
		{
			name: "the hottest believable zone wins",
			build: func(t *testing.T, root string) {
				write(t, filepath.Join(root, "thermal/thermal_zone0/temp"), "48000\n")
				write(t, filepath.Join(root, "thermal/thermal_zone1/temp"), "71500\n")
				// A sensor reporting 900°C is broken, not an emergency.
				write(t, filepath.Join(root, "thermal/thermal_zone2/temp"), "900000\n")
			},
			want: PowerState{Known: true, Percent: -1, TempC: 71.5},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.build(t, root)
			got := readPowerState(powerRoots{
				supply:  filepath.Join(root, "power_supply"),
				thermal: filepath.Join(root, "thermal"),
			})
			if got != tc.want {
				t.Errorf("readPowerState() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestGovernorPowerGatesOnlyFireOnAReadingWeHave(t *testing.T) {
	tests := []struct {
		name          string
		power         PowerState
		wantOnBattery bool
		wantTooHot    bool
	}{
		{name: "nothing known gates nothing", power: UnknownPower()},
		{
			name:          "a low battery holds work back",
			power:         PowerState{Known: true, OnBattery: true, Percent: 20, TempC: -1},
			wantOnBattery: true,
		},
		{
			name:  "a healthy battery does not",
			power: PowerState{Known: true, OnBattery: true, Percent: 90, TempC: -1},
		},
		{
			name:  "an unknown charge level does not",
			power: PowerState{Known: true, OnBattery: true, Percent: -1, TempC: -1},
		},
		{
			name:       "a hot machine stops everything",
			power:      PowerState{Known: true, Percent: -1, TempC: 95},
			wantTooHot: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := governorPolicy()
			p.Power = PowerPolicy{BatteryFloorPercent: 40, ThermalCeilingC: 90}
			g, _, _, clk := testGovernor(t, p, Sensors{Power: func() PowerState { return tc.power }})
			snap := g.Tick(clk.Now())
			if snap.Gates.OnBattery != tc.wantOnBattery || snap.Gates.TooHot != tc.wantTooHot {
				t.Errorf("gates = {onBattery:%v tooHot:%v}, want {onBattery:%v tooHot:%v}",
					snap.Gates.OnBattery, snap.Gates.TooHot, tc.wantOnBattery, tc.wantTooHot)
			}
		})
	}
}

// ------------------------------------------------------------------ wiring

func TestGovernorScheduledWorkWaitsForItsWindowAndGoesWhenItOpens(t *testing.T) {
	p := governorPolicy()
	p.Kinds["transcribe"] = KindPolicy{
		Mode:    ModeScheduled,
		Windows: []Window{{StartMinutes: 2 * 60, EndMinutes: 6 * 60}},
	}
	g, _, st, clk := testGovernor(t, p, Sensors{})
	j := st.seed(Job{Kind: "transcribe", State: StateQueued, MaxAttempts: 3})

	// The clock starts at 12:00 UTC, well outside the window.
	g.Tick(clk.Now())
	if got := stateOf(t, st, j.ID); got != StateDeferred {
		t.Fatalf("state = %q at midday, want %q", got, StateDeferred)
	}

	// 14 hours on is 02:00, inside it.
	clk.Advance(14 * time.Hour)
	snap := g.Tick(clk.Now())
	if len(snap.Deferred) != 0 {
		t.Errorf("Deferred = %v inside the window, want the job left alone", snap.Deferred)
	}
	for _, v := range snap.Verdicts {
		if v.Kind == "transcribe" && !v.Start {
			t.Errorf("verdict inside the window = %+v, want Start true", v)
		}
	}
}

func TestGovernorOnChangeFiresOnlyWhenTheDecisionChanges(t *testing.T) {
	live := true
	p := governorPolicy()
	p.YieldToStream = true

	clk := newClock()
	st := newMemStore(clk.Now)
	q := New(quietLog(), st, WithClock(clk.Now))
	var mu sync.Mutex
	var fired int
	g := NewGovernor(quietLog(), q,
		WithPolicy(p), WithSensors(Sensors{IngestLive: func() bool { return live }}),
		WithGovernorClock(clk.Now), WithNiceTools(NiceTools{}),
		WithGovernorOnChange(func(Snapshot) {
			mu.Lock()
			fired++
			mu.Unlock()
		}))
	st.seed(Job{Kind: "transcribe", State: StateQueued, MaxAttempts: 3})

	g.Tick(clk.Now())
	g.Tick(clk.Now())
	mu.Lock()
	afterSame := fired
	mu.Unlock()
	if afterSame != 1 {
		t.Errorf("onChange fired %d times for one unchanging decision, want 1", afterSame)
	}

	live = false
	clk.Advance(time.Minute)
	g.Tick(clk.Now())
	mu.Lock()
	afterChange := fired
	mu.Unlock()
	if afterChange != 2 {
		t.Errorf("onChange fired %d times after the ingest stopped, want 2", afterChange)
	}
}

func TestGovernorRunLeavesTheQueueAsItFoundItWhenTheContextEnds(t *testing.T) {
	power := PowerState{Known: true, Percent: -1, TempC: 99}
	p := governorPolicy()
	p.Power = PowerPolicy{ThermalCeilingC: 90}

	clk := newClock()
	st := newMemStore(clk.Now)
	q := New(quietLog(), st, WithClock(clk.Now))
	g := NewGovernor(quietLog(), q, WithPolicy(p),
		WithSensors(Sensors{Power: func() PowerState { return power }}),
		WithGovernorClock(clk.Now), WithGovernorTick(time.Millisecond),
		WithNiceTools(NiceTools{}))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		g.Run(ctx)
		close(done)
	}()
	waitUntil(t, "the governor to pause an overheating machine", q.Paused)
	cancel()
	<-done

	if q.Paused() {
		t.Error("Run() exited with the queue still paused; the whole tier would stay down")
	}
}

func TestGovernorLastReportsTheMostRecentDecision(t *testing.T) {
	g, _, _, clk := testGovernor(t, governorPolicy(), Sensors{})
	g.Tick(clk.Now())
	if got := g.Last().At; !got.Equal(clk.Now()) {
		t.Errorf("Last().At = %s, want %s", got, clk.Now())
	}
}
