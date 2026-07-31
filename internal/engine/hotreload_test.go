package engine

// Hot reload: the settings that reach a running process without replacing it.
//
// The dividing line is mechanical -- does the value end up in an FFmpeg argv?
// Everything that does keeps the signature-diff-and-respawn path. Everything
// that does not must be pushed into the running process, because the
// alternative is an operator being told a change applied while the process goes
// on running the old one.

import (
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// testLogger discards output. The engine's constructors all want a *slog.Logger
// and none of these tests assert on log lines.
func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// The reconnect policy governs what the supervisor does AFTER ffmpeg exits. It
// never reaches the command line, so editing it must not tear down a live
// output. Until this landed, the only way to tell polyemesis "be more patient
// with this platform" was to drop the connection to it.
func TestChangingOnlyTheResiliencePolicyDoesNotChangeTheDestinationSpec(t *testing.T) {
	compiled := routing.Result{FilterComplex: "[0:a:0]anull[out]", OutLabel: "[out]"}
	base := destSpec(testDestination(7, nil), compiled, "")

	tests := []struct {
		name   string
		mutate func(*db.Destination)
	}{
		{"raising the minimum backoff", func(d *db.Destination) { d.Resilience.MinBackoffSeconds = 5 }},
		{"raising the maximum backoff", func(d *db.Destination) { d.Resilience.MaxBackoffSeconds = 120 }},
		{"raising the give-up threshold", func(d *db.Destination) { d.Resilience.GiveUpAfter = 40 }},
		{"setting all three at once", func(d *db.Destination) {
			d.Resilience = db.DestResilience{MinBackoffSeconds: 2, MaxBackoffSeconds: 90, GiveUpAfter: 12}
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := testDestination(7, nil)
			tc.mutate(row)
			if got := destSpec(row, compiled, ""); got != base {
				t.Errorf("spec changed; the destination would be torn down to apply a " +
					"value that never reaches its command line")
			}
		})
	}
}

// The other half. Leaving destSpec must not mean the setting stopped working:
// it has to arrive somewhere, and destPolicy is where.
func TestResilienceStillReachesTheSupervisorPolicy(t *testing.T) {
	row := testDestination(7, nil)
	row.Resilience = db.DestResilience{MinBackoffSeconds: 3, MaxBackoffSeconds: 90, GiveUpAfter: 12}

	got := destPolicy(row)
	want := supervisor.Policy{
		MinBackoff:  3 * time.Second,
		MaxBackoff:  90 * time.Second,
		MaxRestarts: 12,
	}
	if got != want {
		t.Fatalf("destPolicy = %+v, want %+v", got, want)
	}
}

// The zero value must stay exactly what every destination ran on before the
// policy was configurable: the supervisor's own defaults, which secondsOr
// spells as a zero Duration.
func TestAnUnsetResiliencePolicyLeavesTheSupervisorDefaultsInPlace(t *testing.T) {
	if got := destPolicy(testDestination(7, nil)); got != (supervisor.Policy{}) {
		t.Fatalf("destPolicy = %+v, want the zero Policy so supervisor.New's defaults apply", got)
	}
}

// The invariant that makes the whole live-apply set safe: a live-applied value
// cannot be rejected by FFmpeg because it is never shown to FFmpeg. If a
// resilience field is ever added to the argv builder, this fails and the
// classification in reload.go becomes a lie.
func TestNoResilienceFieldExistsOnTheDestinationArgvSpec(t *testing.T) {
	forbidden := []string{"backoff", "giveup", "restart", "resilience"}
	rt := reflect.TypeOf(ffmpeg.DestSpec{})
	for i := range rt.NumField() {
		name := strings.ToLower(rt.Field(i).Name)
		for _, bad := range forbidden {
			if strings.Contains(name, bad) {
				t.Errorf("ffmpeg.DestSpec.%s looks like a reconnect-policy field. "+
					"The policy is applied live precisely because it never reaches an "+
					"argv; putting it in one makes that claim false and makes editing "+
					"it a silent no-op on every running destination.", rt.Field(i).Name)
			}
		}
	}
}

// The survivor branch of startDestinations is the only place a running
// destination is touched without being replaced, so it is the only place a
// retune can land.
func TestRefreshingARunningDestinationRetunesItsSupervisorPolicy(t *testing.T) {
	proc := supervisor.New(testLogger(), supervisor.Spec{Name: "dest:7"})
	running := &destination{
		row:     &db.Destination{ID: 7, Name: "twitch"},
		proc:    proc,
		port:    9001,
		subName: "dest:7",
		spec:    "unchanged",
	}
	// The logger is not optional here even though nothing asserts on it:
	// applyDestPolicy reports what it retuned, and an Engine built by New
	// always has one. A bare literal without it panics on the first retune,
	// which is a property of the test rather than of the code under it.
	e := &Engine{log: testLogger(), dests: map[int64]*destination{7: running}}

	updated := &db.Destination{ID: 7, Name: "twitch"}
	updated.Resilience = db.DestResilience{MinBackoffSeconds: 4, MaxBackoffSeconds: 60, GiveUpAfter: 9}
	e.startDestinations(map[int64]destPlan{7: {row: updated, spec: "unchanged"}})

	want := supervisor.Policy{MinBackoff: 4 * time.Second, MaxBackoff: 60 * time.Second, MaxRestarts: 9}
	if got := proc.Policy(); got != want {
		t.Fatalf("policy = %+v, want %+v: the edit was saved and never reached the "+
			"supervisor that governs the reconnect", got, want)
	}
}

// moreForgiving decides whether a destination that gave up should be revived. 0
// is unlimited, so it is the MOST forgiving value rather than the least -- read
// as a plain number it sorts the wrong way round.
func TestMoreForgivingTreatsZeroAsUnlimited(t *testing.T) {
	tests := []struct {
		name   string
		before supervisor.Policy
		want   supervisor.Policy
		revive bool
	}{
		{"raising a finite limit", supervisor.Policy{MaxRestarts: 5}, supervisor.Policy{MaxRestarts: 20}, true},
		{"lowering a finite limit", supervisor.Policy{MaxRestarts: 20}, supervisor.Policy{MaxRestarts: 5}, false},
		{"finite to unlimited", supervisor.Policy{MaxRestarts: 5}, supervisor.Policy{MaxRestarts: 0}, true},
		{"unlimited to finite", supervisor.Policy{MaxRestarts: 0}, supervisor.Policy{MaxRestarts: 5}, false},
		{"unchanged", supervisor.Policy{MaxRestarts: 5}, supervisor.Policy{MaxRestarts: 5}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := moreForgiving(tc.before, tc.want); got != tc.revive {
				t.Errorf("moreForgiving = %v, want %v", got, tc.revive)
			}
		})
	}
}

// meters.intervalMs was captured by value into the StdoutHandler closure when
// the metering sidecar spawned, and it is not in metersSig -- so changing it
// stored cleanly, returned 200, and did absolutely nothing until some unrelated
// change happened to restart the meters. It is a throttle in a Go parser and
// never reaches an argv, so it must apply to the running process.
func TestTheMeterThrottleReadsTheCurrentIntervalRatherThanTheOneItSpawnedWith(t *testing.T) {
	e := &Engine{}
	e.applyMeterInterval(db.MeterSettings{Enabled: true, IntervalMS: 1000})
	th := &meterThrottle{e: e}

	base := time.Now()
	if !th.allow(base) {
		t.Fatal("the first frame must always be published")
	}
	if th.allow(base.Add(50 * time.Millisecond)) {
		t.Fatal("a frame 50ms into a 1000ms window was published; the throttle is not throttling")
	}

	e.applyMeterInterval(db.MeterSettings{Enabled: true, IntervalMS: 10})

	if !th.allow(base.Add(50 * time.Millisecond)) {
		t.Fatal("the same frame was still suppressed after the interval was lowered to 10ms; " +
			"the throttle is reading a value captured when the sidecar spawned")
	}
}

// The interval has to be stored before every early return in reconcileMeters,
// or lowering it on a box whose ingest has not probed yet would be lost -- and
// the operator would have to change it twice.
func TestTheMeterIntervalIsAppliedEvenWhenTheMetersCannotRun(t *testing.T) {
	e := &Engine{log: testLogger(), source: routing.DefaultSource()}
	e.reconcileMeters(db.Settings{Meters: db.MeterSettings{Enabled: false, IntervalMS: 250}})

	if got := e.meterInterval.Load(); got != int64(250*time.Millisecond) {
		t.Fatalf("meterInterval = %d, want %d: the interval must be stored before the "+
			"early returns, or a change made while the meters are down is lost",
			got, int64(250*time.Millisecond))
	}
}
