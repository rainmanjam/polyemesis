package engine

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"sync/atomic"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// The manager remembers a handful of install-wide settings and has to hand every
// one of them to every engine -- the engines running when the setting was saved,
// and the engines built afterwards when an operator adds a source. Those used to
// be two lists: a SetX method with its own loop, and a hand-written block in
// Sync's creation path. Nothing but prose kept them in step, and the failure a
// divergence produces is the nastiest shape there is: the setting works, right up
// until the operator adds a source, and then it works on every programme except
// the new one, with no error anywhere.
//
// These tests drive the Sync half, which is the half that had no coverage at all
// -- every clause in that block could be replaced with a discard and the engine,
// api and cmd suites stayed green.

// stubLifecycle is a LifecycleObserver that does nothing, which is all these
// tests need: what is under test is whether the observer ARRIVED, not what it
// does with an edge. Wanted answers false so that wiring one here cannot make an
// engine start sampling status on its 2s tick.
type stubLifecycle struct{}

func (*stubLifecycle) Observe(hooks.Event) {}
func (*stubLifecycle) Wanted() bool        { return false }

// managerSettingsProbe is the set of distinguishable values pushed at a Manager,
// kept so the assertions can check identity rather than merely non-nil. A test
// that only asserted "not nil" would pass against an engine that built its own
// dispatcher, which is exactly the wrong answer.
type managerSettingsProbe struct {
	tw        *transcribe.Tools
	modelsDir string
	niceMark  string
	attempts  int
	hooks     *hooks.Dispatcher
	lifecycle LifecycleObserver
}

func pushEveryManagerSetting(t *testing.T, m *Manager) managerSettingsProbe {
	t.Helper()
	p := managerSettingsProbe{
		tw:        &transcribe.Tools{Binary: "/opt/whisper/probe"},
		modelsDir: t.TempDir(),
		niceMark:  "probe-nice",
		attempts:  7,
		hooks: hooks.NewDispatcher(slog.New(slog.NewTextHandler(io.Discard, nil)),
			hooks.SourceFunc(func() ([]hooks.Hook, error) { return nil, nil })),
		lifecycle: &stubLifecycle{},
	}
	m.SetTranscriber(p.tw, p.modelsDir, func(name string, args []string) (string, []string) {
		return p.niceMark, args
	})
	m.SetAlertRetry(p.attempts)
	m.SetHooks(p.hooks)
	m.SetLifecycle(p.lifecycle)
	return p
}

// checkEngineHasEverySetting reads the engine's copies under its own lock, the
// way the engine's own readers do, and reports every one that did not arrive
// rather than stopping at the first. Which ONE is missing is the whole diagnosis
// when this fails.
func checkEngineHasEverySetting(t *testing.T, eng *Engine, p managerSettingsProbe, what string) {
	t.Helper()
	if eng == nil {
		t.Fatalf("%s: no engine to check", what)
	}
	eng.mu.RLock()
	whisper, dir, nice := eng.whisper, eng.whisperDir, eng.whisperNice
	attempts, hookd, lifecycle := eng.alertAttempts, eng.hooks, eng.lifecycle
	eng.mu.RUnlock()

	if whisper != p.tw {
		t.Errorf("%s: transcriber = %v, want the one the manager was given: "+
			"this programme's recordings would never transcribe", what, whisper)
	}
	if dir != p.modelsDir {
		t.Errorf("%s: models directory = %q, want %q", what, dir, p.modelsDir)
	}
	if nice == nil {
		t.Errorf("%s: no nice wrapper, so speech recognition on this programme "+
			"would compete with the encoders at equal priority", what)
	} else if got, _ := nice("whisper", nil); got != p.niceMark {
		t.Errorf("%s: nice wrapper is not the one the manager was given (%q)", what, got)
	}
	if attempts != p.attempts {
		t.Errorf("%s: alert retry budget = %d, want %d: this programme's alerts "+
			"would chase a dead endpoint for a different number of tries than "+
			"every other programme's", what, attempts, p.attempts)
	}
	if hookd != p.hooks {
		t.Errorf("%s: hook dispatcher = %v, want the shared one: this programme's "+
			"lifecycle hooks would never fire", what, hookd)
	}
	if lifecycle != p.lifecycle {
		t.Errorf("%s: lifecycle observer = %v, want the coordinator: this "+
			"programme's YouTube destinations would stream perfectly while their "+
			"broadcasts sat in \"testing\" for the whole show", what, lifecycle)
	}
}

// The Sync half. This is the one that had no coverage and the one an operator
// meets as "the setting works until I add a source".
func TestASourceAddedAfterWiringGetsEveryManagerLevelSetting(t *testing.T) {
	m, store := managerFixture(t)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	p := pushEveryManagerSetting(t, m)

	vert := addSource(t, store, "Vertical")
	if err := m.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	eng := m.Engine(vert.ID)
	if eng == nil {
		t.Fatal("Sync started no engine for the source that was just added")
	}
	checkEngineHasEverySetting(t, eng, p, "engine created by Sync after the settings were pushed")
}

// The SetX half, which is what the Sync half has to stay in step with. Both are
// asserted in one file on purpose: they are the two lists that used to disagree,
// and a change that fixes one and breaks the other should fail here.
func TestASettingSavedLaterReachesTheEnginesAlreadyRunning(t *testing.T) {
	m, store := managerFixture(t)
	vert := addSource(t, store, "Vertical")
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	eng := m.Engine(vert.ID)
	if eng == nil {
		t.Fatal("Start brought up no engine for the source")
	}

	p := pushEveryManagerSetting(t, m)
	checkEngineHasEverySetting(t, eng, p, "engine that was already running when the settings were pushed")
}

// POSITIVE CONTROL. Every assertion above compares against a value the test
// itself created, so an engine that manufactured its own dispatcher would fail
// them -- but an Engine constructor that copied settings off some other engine,
// or a checkEngineHasEverySetting that quietly compared nothing, would not. This
// pins the other end: a manager nobody configured hands a new engine NOTHING, so
// the values the tests above observe can only have come from the pushes.
func TestAnEngineGetsNoSettingsFromAManagerThatWasNeverConfigured(t *testing.T) {
	m, store := managerFixture(t)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	vert := addSource(t, store, "Vertical")
	if err := m.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	eng := m.Engine(vert.ID)
	if eng == nil {
		t.Fatal("Sync started no engine for the source that was just added")
	}

	eng.mu.RLock()
	whisper, attempts, hookd, lifecycle := eng.whisper, eng.alertAttempts, eng.hooks, eng.lifecycle
	eng.mu.RUnlock()

	if whisper != nil {
		t.Errorf("transcriber = %v on an install where none was detected", whisper)
	}
	if attempts != 0 {
		t.Errorf("alert retry budget = %d, want 0: an operator who never chose one "+
			"must be left on the alerts package default, not clamped to a number "+
			"no other engine is using", attempts)
	}
	if hookd != nil {
		t.Errorf("hook dispatcher = %v on an install where none was wired", hookd)
	}
	if lifecycle != nil {
		t.Errorf("lifecycle observer = %v on an install where none was wired", lifecycle)
	}
}

// Engine.alertAttempts is a REMEMBERED copy, and the value that actually
// governs a delivery lives in alerts.Notifier. Asserting only on the copy would
// leave the one step nobody can see -- the forward into the notifier -- covered
// by nothing, and an engine that remembered 1 while its notifier still used the
// package default of 4 would satisfy every other test in this file.
//
// So this one asserts the budget where it bites: a rule whose endpoint always
// answers 500 is retried until the budget runs out, and a budget of one means
// the endpoint is hit exactly once. Counting the requests is the only view of
// that number available from outside internal/alerts.
//
// AllowPrivateTarget is set because the endpoint is httptest's loopback
// listener and the notifier's dial-time SSRF guard refuses private targets by
// default -- see alerts.Rule. Nothing about the retry budget is loopback
// specific; this is how the test reaches a server it controls.
func TestTheAlertBudgetReachesTheNotifierAndNotJustTheEngine(t *testing.T) {
	var hits atomic.Int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(endpoint.Close)

	m, store := managerFixture(t)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// One attempt and no retries, which is what makes the assertion a count of
	// one rather than a stopwatch: the default budget of four would hit the
	// endpoint four times, spread over the backoff curve.
	m.SetAlertRetry(1)

	vert := addSource(t, store, "Vertical")
	if err := m.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	eng := m.Engine(vert.ID)
	if eng == nil {
		t.Fatal("Sync started no engine for the source that was just added")
	}

	rule := alerts.Rule{
		Name: "probe", Enabled: true, URL: endpoint.URL,
		Format: alerts.FormatJSON, AllowPrivateTarget: true,
	}
	// The endpoint refuses every request on purpose, so an error here is the
	// expected outcome and only the request count is under test.
	_ = eng.Alerts().Test(context.Background(), rule)

	if got := hits.Load(); got != 1 {
		t.Errorf("the endpoint was hit %d times, want 1: the engine created by Sync "+
			"is delivering on a retry budget of %d, not the 1 the operator saved. "+
			"Its alerts chase a dead endpoint for a different length of time than "+
			"every other programme's.", got, alerts.DefaultAttempts)
	}
}

// The device this file exists to protect is that engineSettings is ONE list. A
// fifth setting is added by adding a field here and a line to
// applyEngineSettings -- and if the author adds the field and forgets the line,
// the two tests above will not notice, because they only ever assert about the
// settings they know to push.
//
// So this pins the list itself. It is a warning rather than a control -- a
// control would need the compiler to reject an engineSettings field that
// applyEngineSettings does not read, which Go cannot express and which would
// otherwise cost a code generator and a build step to fake. What it buys is
// that the field lands with a failing test naming exactly what else to do,
// rather than silently.
func TestEveryEngineSettingIsCoveredByTheSyncTests(t *testing.T) {
	covered := []string{"alertAttempts", "hooks", "lifecycle", "modelsDir", "nice", "tw"}

	typ := reflect.TypeOf(engineSettings{})
	got := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		got = append(got, typ.Field(i).Name)
	}
	sort.Strings(got)

	if !reflect.DeepEqual(got, covered) {
		t.Fatalf("engineSettings fields = %v, this test knows about %v.\n\n"+
			"A setting was added to or removed from the one list. Three things have "+
			"to move together or an operator gets the failure this whole file is "+
			"about -- the setting working on every programme except the one they "+
			"just added:\n"+
			"  1. Manager.applyEngineSettings must push the new field into the engine.\n"+
			"  2. pushEveryManagerSetting and checkEngineHasEverySetting in this file "+
			"must exercise and assert it.\n"+
			"  3. This list must be updated to match.",
			got, covered)
	}
}
