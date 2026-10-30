package obsbridge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/eventstream/streamtest"
	"github.com/rainmanjam/polyemesis/internal/obsbridge"
	"github.com/rainmanjam/polyemesis/internal/obsbridge/obstest"
)

// NOTHING IN THIS FILE TALKS TO OBS. Every connection below is to
// obstest.Server, a mock written from obs-websocket's protocol document and
// its Crypto.cpp. It proves the bridge speaks the protocol as documented; it
// cannot prove OBS behaves as documented.

// fakeActions records what the bridge asked for. start blocks, when set, until
// the context ends -- a paced start still walking its list.
type fakeActions struct {
	mu     sync.Mutex
	starts int
	stops  int
	scenes []string
	// blockStart makes Start wait for ctx; startCtxErr records why it ended.
	blockStart  bool
	startEnded  chan error
	startCalled chan struct{}
	stopCalled  chan struct{}
	sceneCalled chan string
}

func newFakeActions() *fakeActions {
	return &fakeActions{
		startEnded: make(chan error, 4), startCalled: make(chan struct{}, 8),
		stopCalled: make(chan struct{}, 8), sceneCalled: make(chan string, 8),
	}
}

func (f *fakeActions) Start(ctx context.Context, cfg obsbridge.Config) []obsbridge.Outcome {
	f.mu.Lock()
	f.starts++
	block := f.blockStart
	f.mu.Unlock()
	f.startCalled <- struct{}{}
	if block {
		<-ctx.Done()
		f.startEnded <- ctx.Err()
		return []obsbridge.Outcome{{ID: cfg.Destinations[0], Outcome: "skipped"}}
	}
	return []obsbridge.Outcome{{ID: cfg.Destinations[0], Outcome: "started"}}
}

func (f *fakeActions) Stop(_ context.Context, cfg obsbridge.Config) []obsbridge.Outcome {
	f.mu.Lock()
	f.stops++
	f.mu.Unlock()
	f.stopCalled <- struct{}{}
	return []obsbridge.Outcome{{ID: cfg.Destinations[0], Outcome: "stopped"}}
}

func (f *fakeActions) ApplyScene(_ context.Context, _ obsbridge.Config, rule obsbridge.SceneRule) []obsbridge.Outcome {
	f.mu.Lock()
	f.scenes = append(f.scenes, rule.Scene)
	f.mu.Unlock()
	f.sceneCalled <- rule.Scene
	return []obsbridge.Outcome{{ID: 1, Outcome: "applied"}}
}

func (f *fakeActions) counts() (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.stops, len(f.scenes)
}

// store is a mutable stored configuration.
type store struct {
	mu  sync.Mutex
	cfg obsbridge.Config
	pw  string
}

func (s *store) load() (obsbridge.Config, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg, s.pw, nil
}

func (s *store) set(cfg obsbridge.Config, pw string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg, s.pw = cfg, pw
}

type rig struct {
	b    *obsbridge.Bridge
	hub  *eventstream.Hub
	sub  *eventstream.Subscription
	acts *fakeActions
	st   *store
	logs *bytes.Buffer
	lmu  *sync.Mutex
}

// logText reads the log under the writer's lock.
func (r *rig) logText() string {
	r.lmu.Lock()
	defer r.lmu.Unlock()
	return r.logs.String()
}

// newRig builds a bridge over cfg and runs it. Its log is a private buffer:
// a leak assertion over a buffer another component also writes to proves
// nothing about this one.
func newRig(t *testing.T, cfg obsbridge.Config, pw string) *rig {
	t.Helper()
	logs := &bytes.Buffer{}
	logMu := &sync.Mutex{}
	log := slog.New(slog.NewTextHandler(lockedWriter{logMu, logs}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	hub := eventstream.NewHub()
	st := &store{cfg: cfg, pw: pw}
	b := obsbridge.New(obsbridge.Options{Log: log, Pub: hub, Load: st.load})
	obsbridge.ShortenForTest(b, 20*time.Millisecond)
	acts := newFakeActions()
	b.SetActions(acts)
	sub := streamtest.Subscribe(t, hub)
	// Loaded before Run starts, so a test that calls Handle at once is acting
	// on its configuration and not on the zero value Run has yet to replace.
	if err := b.Reload(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; b.Close() })
	return &rig{b: b, hub: hub, sub: sub, acts: acts, st: st, logs: logs, lmu: logMu}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func baseConfig(url string) obsbridge.Config {
	return obsbridge.Config{
		Enabled: true, Connect: url != "", URL: url,
		Destinations:  []int64{7},
		StartOnStream: true, StopOnStream: true, StopAcknowledged: true,
		Scenes: []obsbridge.SceneRule{{Scene: "Just Chatting", Preset: "no-music"}},
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func expectCall(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s was never called", what)
	}
}

func expectNoCall(t *testing.T, ch <-chan struct{}, what string, d time.Duration) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("%s was called and must not have been", what)
	case <-time.After(d):
	}
}

// The three event types, each emitted by the real producer: the client reading
// a real (mock) obs-websocket. These are the emission tests the catalogue
// guard (TestEveryCataloguedTypeHasAnEmissionTest) requires.
func TestTheClientEmitsConnectionStreamAndSceneEventsFromObsWebsocket(t *testing.T) {
	obs := obstest.New(t, "mock-obs-pass-1")
	r := newRig(t, baseConfig(obs.URL()), "mock-obs-pass-1")

	obs.WaitConnected(5 * time.Second)
	for {
		env := streamtest.Expect(t, r.sub, eventstream.TypeOBSConnection)
		if env.Data.(map[string]any)["state"] == string(obsbridge.ConnConnected) {
			break
		}
	}

	obs.StreamState(obsbridge.OutputStarted)
	env := streamtest.Expect(t, r.sub, eventstream.TypeOBSStream)
	if d := env.Data.(map[string]any); d["state"] != "started" || d["via"] != "websocket" {
		t.Fatalf("obs.stream data = %v", d)
	}

	obs.Scene("Just Chatting")
	for {
		env = streamtest.Expect(t, r.sub, eventstream.TypeOBSScene)
		if d := env.Data.(map[string]any); d["scene"] == "Just Chatting" {
			if d["preset"] != "no-music" {
				t.Fatalf("a scene with a rule names its preset; got %v", d)
			}
			break
		}
	}
}

func TestTheClientIdentifiesWithScenesAndOutputsOnlyAndReadsTheCurrentState(t *testing.T) {
	obs := obstest.New(t, "mock-obs-pass-2")
	obs.SetState(true, "Gameplay")
	r := newRig(t, baseConfig(obs.URL()), "mock-obs-pass-2")
	obs.WaitConnected(5 * time.Second)

	if got, want := obs.Subscriptions(), (1<<2)|(1<<6); got != want {
		t.Fatalf("eventSubscriptions = %d; want Scenes|Outputs = %d", got, want)
	}
	waitFor(t, "the sync reads to land", func() bool {
		st := r.b.Status()
		return st.Streaming != nil && *st.Streaming && st.Scene == "Gameplay"
	})
	if st := r.b.Status(); st.Connection.State != obsbridge.ConnConnected || st.Connection.OBSWebSocketVersion != "5.5.2" {
		t.Fatalf("connection = %+v", st.Connection)
	}
	// A reconnect into a stream that is already live is NOT OBS starting: the
	// sync read acts on nothing.
	expectNoCall(t, r.acts.startCalled, "Start, on a sync read", 200*time.Millisecond)
}

func TestAWrongPasswordIsRefusedAndNeitherTheStateNorTheLogsCarryThePassword(t *testing.T) {
	obs := obstest.New(t, "the-right-one")
	// Built at run time, so no literal in this file looks like a credential.
	secret := "WRONG-" + strings.Repeat("s3cr3t", 3)
	r := newRig(t, baseConfig(obs.URL()), secret)

	waitFor(t, "the refusal", func() bool { return r.b.Status().Connection.State == obsbridge.ConnRefused })
	if _, refusals := obs.Counts(); refusals == 0 {
		t.Fatal("the mock never refused anything; the state above came from somewhere else")
	}
	st, _ := json.Marshal(r.b.Status())
	if bytes.Contains(st, []byte(secret)) {
		t.Fatalf("the status carries the password: %s", st)
	}
	if !strings.Contains(r.b.Status().Connection.Detail, "refused the password") {
		t.Fatalf("detail = %q", r.b.Status().Connection.Detail)
	}
	// Every obs.connection event published so far.
	timeout := time.After(200 * time.Millisecond)
	for done := false; !done; {
		select {
		case env := <-r.sub.C:
			raw, _ := json.Marshal(env)
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatalf("an event carries the password: %s", raw)
			}
		case <-timeout:
			done = true
		}
	}
	if strings.Contains(r.logText(), secret) {
		t.Fatalf("the log carries the password:\n%s", r.logText())
	}
	if !strings.Contains(r.logText(), "obs-websocket session ended") {
		t.Fatal("positive control: the bridge logged nothing at all, so the absence above proves nothing")
	}
}

func TestAPasswordAskedForAndNotSetSaysSoRatherThanUnreachable(t *testing.T) {
	obs := obstest.New(t, "needs-one")
	r := newRig(t, baseConfig(obs.URL()), "")
	waitFor(t, "the refusal", func() bool { return r.b.Status().Connection.State == obsbridge.ConnRefused })
	if d := r.b.Status().Connection.Detail; !strings.Contains(d, "none is set") {
		t.Fatalf("detail = %q", d)
	}
}

func TestStreamStartStartsTheDestinationsOnceEvenWhenBothWaysInReportIt(t *testing.T) {
	obs := obstest.New(t, "")
	r := newRig(t, baseConfig(obs.URL()), "")
	obs.WaitConnected(5 * time.Second)
	waitFor(t, "the sync read", func() bool { return r.b.Status().Streaming != nil })

	obs.StreamState("OBS_WEBSOCKET_OUTPUT_STARTING")
	obs.StreamState(obsbridge.OutputStarted)
	expectCall(t, r.acts.startCalled, "Start")
	// The OBS script reports the same start a moment later.
	if err := r.b.Handle(obsbridge.Event{Kind: obsbridge.KindStreamStarted, Via: obsbridge.ViaScript}); err != nil {
		t.Fatal(err)
	}
	expectNoCall(t, r.acts.startCalled, "Start, a second time", 200*time.Millisecond)
	waitFor(t, "the action record", func() bool { return len(r.b.Status().LastActions) == 1 })
	if a := r.b.Status().LastActions[0]; a.Action != "start" || a.Via != obsbridge.ViaWebSocket || a.Results[0].Outcome != "started" {
		t.Fatalf("record = %+v", a)
	}
}

func TestReconnectingIsNotStoppingAndArmsNoStop(t *testing.T) {
	obs := obstest.New(t, "")
	r := newRig(t, baseConfig(obs.URL()), "")
	obs.WaitConnected(5 * time.Second)
	obs.StreamState(obsbridge.OutputStarted)
	expectCall(t, r.acts.startCalled, "Start")

	obs.StreamState("OBS_WEBSOCKET_OUTPUT_RECONNECTING")
	obs.StreamState("OBS_WEBSOCKET_OUTPUT_RECONNECTED")
	expectNoCall(t, r.acts.stopCalled, "Stop, on a reconnect", 300*time.Millisecond)
	if st := r.b.Status(); st.StopPendingUntil != nil || st.Streaming == nil || !*st.Streaming {
		t.Fatalf("a reconnecting output still streams with nothing pending; status = %+v", st)
	}
}

func TestStopWaitsOutTheGraceAndAStartInsideItCancelsTheStop(t *testing.T) {
	cfg := baseConfig("")
	cfg.StopGraceSeconds = 10 // x 20ms in this rig: 200ms
	r := newRig(t, cfg, "")
	h := func(k obsbridge.Kind) {
		t.Helper()
		if err := r.b.Handle(obsbridge.Event{Kind: k, Via: obsbridge.ViaScript}); err != nil {
			t.Fatal(err)
		}
	}
	h(obsbridge.KindStreamStarted)
	expectCall(t, r.acts.startCalled, "Start")

	h(obsbridge.KindStreamStopped)
	if r.b.Status().StopPendingUntil == nil {
		t.Fatal("a stop inside its grace period reports when it will happen")
	}
	h(obsbridge.KindStreamStarted)
	expectNoCall(t, r.acts.stopCalled, "Stop, after OBS came back inside the grace", 400*time.Millisecond)
	expectCall(t, r.acts.startCalled, "Start, for the restart")

	h(obsbridge.KindStreamStopped)
	expectNoCall(t, r.acts.stopCalled, "Stop, before the grace ran out", 100*time.Millisecond)
	expectCall(t, r.acts.stopCalled, "Stop, once the grace ran out")
	if r.b.Status().StopPendingUntil != nil {
		t.Fatal("the pending stop is cleared once it has happened")
	}
}

func TestStopIsNeverArmedWithoutTheAcknowledgement(t *testing.T) {
	cfg := baseConfig("")
	cfg.StopAcknowledged = false // as a hand-edited row could hold
	r := newRig(t, cfg, "")
	if err := r.b.Handle(obsbridge.Event{Kind: obsbridge.KindStreamStarted, Via: obsbridge.ViaScript}); err != nil {
		t.Fatal(err)
	}
	// Waited for: a stop arriving before the queued start runs cancels it,
	// which is its own test below and not this one.
	expectCall(t, r.acts.startCalled, "Start")
	if err := r.b.Handle(obsbridge.Event{Kind: obsbridge.KindStreamStopped, Via: obsbridge.ViaScript}); err != nil {
		t.Fatal(err)
	}
	expectNoCall(t, r.acts.stopCalled, "Stop, unacknowledged", 200*time.Millisecond)
}

func TestADroppedConnectionNeverStopsAnythingEvenWhenOBSStoppedMeanwhile(t *testing.T) {
	obs := obstest.New(t, "")
	r := newRig(t, baseConfig(obs.URL()), "")
	obs.WaitConnected(5 * time.Second)
	obs.StreamState(obsbridge.OutputStarted)
	expectCall(t, r.acts.startCalled, "Start")

	// OBS crashes; when it comes back it is not streaming.
	obs.SetState(false, "Starting Soon")
	obs.DropAll()
	obs.WaitConnected(5 * time.Second)
	waitFor(t, "the reconnect's sync read", func() bool {
		st := r.b.Status()
		return st.Streaming != nil && !*st.Streaming
	})
	expectNoCall(t, r.acts.stopCalled, "Stop, on a reconnect that found OBS stopped", 300*time.Millisecond)
	if ids, _ := obs.Counts(); ids < 2 {
		t.Fatalf("identifies = %d; the client did not reconnect", ids)
	}
}

func TestAStopCancelsAPacedStartThatIsStillWalkingItsList(t *testing.T) {
	r := newRig(t, baseConfig(""), "")
	r.acts.blockStart = true
	if err := r.b.Handle(obsbridge.Event{Kind: obsbridge.KindStreamStarted, Via: obsbridge.ViaScript}); err != nil {
		t.Fatal(err)
	}
	expectCall(t, r.acts.startCalled, "Start")
	if err := r.b.Handle(obsbridge.Event{Kind: obsbridge.KindStreamStopped, Via: obsbridge.ViaScript}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-r.acts.startEnded:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the start ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OBS stopping did not cancel the paced start")
	}
	expectCall(t, r.acts.stopCalled, "Stop, after the cancelled start")
}

func TestASceneWithARuleAppliesItAndASceneWithoutOneDoesNothing(t *testing.T) {
	r := newRig(t, baseConfig(""), "")
	for _, scene := range []string{"Gameplay", "Just Chatting", "Just Chatting"} {
		if err := r.b.Handle(obsbridge.Event{Kind: obsbridge.KindSceneChanged, Scene: scene, Via: obsbridge.ViaScript}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case got := <-r.acts.sceneCalled:
		if got != "Just Chatting" {
			t.Fatalf("applied %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the rule was not applied")
	}
	select {
	case got := <-r.acts.sceneCalled:
		t.Fatalf("the same scene reported twice applied twice (%q)", got)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestADisabledBridgeRefusesTheScriptAndActsOnNothing(t *testing.T) {
	cfg := baseConfig("")
	cfg.Enabled = false
	r := newRig(t, cfg, "")
	err := r.b.Handle(obsbridge.Event{Kind: obsbridge.KindStreamStarted, Via: obsbridge.ViaScript})
	if !errors.Is(err, obsbridge.ErrDisabled) {
		t.Fatalf("err = %v", err)
	}
	expectNoCall(t, r.acts.startCalled, "Start, while disabled", 100*time.Millisecond)

	// Switched on: the next report acts.
	cfg.Enabled = true
	r.st.set(cfg, "")
	if err := r.b.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := r.b.Handle(obsbridge.Event{Kind: obsbridge.KindStreamStarted, Via: obsbridge.ViaScript}); err != nil {
		t.Fatal(err)
	}
	expectCall(t, r.acts.startCalled, "Start")
}

func TestHandleRefusesWhatIsNotOneOfTheThreeFacts(t *testing.T) {
	r := newRig(t, baseConfig(""), "")
	for _, ev := range []obsbridge.Event{
		{Kind: "stream.paused", Via: obsbridge.ViaScript},
		{Kind: obsbridge.KindSceneChanged, Scene: "", Via: obsbridge.ViaScript},
		{Kind: obsbridge.KindSceneChanged, Scene: "line\nbreak", Via: obsbridge.ViaScript},
		{Kind: obsbridge.KindSceneChanged, Scene: strings.Repeat("x", obsbridge.MaxSceneNameLength+1), Via: obsbridge.ViaScript},
		{Kind: obsbridge.KindStreamStarted, Via: "carrier-pigeon"},
	} {
		if err := r.b.Handle(ev); !errors.Is(err, obsbridge.ErrBadEvent) {
			t.Errorf("%+v: err = %v; want ErrBadEvent", ev, err)
		}
	}
}

func TestAnUnrelatedConfigurationSaveDoesNotCycleAHealthyLink(t *testing.T) {
	obs := obstest.New(t, "pw-cycle")
	cfg := baseConfig(obs.URL())
	r := newRig(t, cfg, "pw-cycle")
	obs.WaitConnected(5 * time.Second)

	cfg.Scenes = append(cfg.Scenes, obsbridge.SceneRule{Scene: "BRB", Preset: "mic-only"})
	r.st.set(cfg, "pw-cycle")
	if err := r.b.Reload(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if ids, _ := obs.Counts(); ids != 1 {
		t.Fatalf("identifies = %d; a scene-rule save reconnected", ids)
	}

	// A password rotation does cycle it -- same length, different value.
	r.st.set(cfg, "pw-CYCLE")
	if err := r.b.Reload(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the refusal of the rotated password", func() bool {
		_, refusals := obs.Counts()
		return refusals > 0
	})
}
