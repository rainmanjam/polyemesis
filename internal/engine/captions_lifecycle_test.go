package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// captionEngine is an engine that can really start a live captioner. The audio
// tap is this test binary, re-executed in the mode tap names (see
// engineFakeChildCaptionTap); whisper is never run, because the tap never
// writes the audio a window would need. The model is the live default for this
// machine, as an empty file, so the operator config leaves Model blank and the
// reconcile has to pick it, the way an install that never chose one does.
//
// The returned channel receives one value each time the captioner asks for a
// reconcile on its own (the exit hook), in place of a real Reconcile, which a
// partly built engine cannot run.
func captionEngine(t *testing.T, tap string) (*Engine, <-chan struct{}) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary to re-execute as a stand-in audio tap: %v", err)
	}
	t.Setenv(engineFakeChildCaptionTap, tap)

	e := failoverEngine(t)
	e.tools.FFmpeg = self
	e.alloc = relay.NewPortAllocator(freeUDPPort(t), 8)
	e.cfg.DataDir = t.TempDir()

	models := t.TempDir()
	name := transcribe.LiveDefaultModel(transcribe.HintFromTools(e.tools)).Name
	if err := os.WriteFile(filepath.Join(models, "ggml-"+name+".bin"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	e.whisper = &transcribe.Tools{Binary: filepath.Join(models, "no-whisper-is-ever-run")}
	e.whisperDir = models

	dest, err := e.store.CreateDestination(&db.Destination{
		Name: "cc", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
		URL: "rtmp://127.0.0.1:1/rtmp", StreamKey: "key", Enabled: true,
		AudioBitrate: 128, Profile: routing.DefaultProfile(), CaptionMode: "sei",
	})
	if err != nil {
		t.Fatalf("create captioned destination: %v", err)
	}
	e.sourceID = *dest.SourceID

	cfg := transcribe.DefaultLiveConfig()
	cfg.VTTPath = filepath.Join(t.TempDir(), "captions.vtt")
	if err := e.setCaptionConfig(cfg); err != nil {
		t.Fatalf("setCaptionConfig: %v", err)
	}

	woken := make(chan struct{}, 64)
	e.captWake = func() {
		select {
		case woken <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() {
		e.mu.Lock()
		c, port, hub, vtt := e.capt, e.captPort, e.captHub, e.captVTT
		e.capt, e.captPort, e.captHub, e.captVTT, e.captSig = nil, 0, nil, nil, ""
		e.stopped = true
		e.mu.Unlock()
		e.teardownCaptions(c, port, hub, vtt)
	})
	return e, woken
}

// currentCaptioner is the installed captioner, read under the lock.
func currentCaptioner(e *Engine) *transcribe.LiveCaptioner {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.capt
}

// waitStopped waits for a captioner's loop to end on its own.
func waitStopped(t *testing.T, c *transcribe.LiveCaptioner) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for c.Running() {
		if time.Now().After(deadline) {
			t.Fatal("the stand-in tap exited, but the captioner still reports running")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestACaptionerWhoseTapExitedIsRebuiltOnTheNextReconcile is the reconcile-level
// half of #4. The tap ends on its own, which leaves the captioner pointer set
// and its signature matching. The next reconcile must build a new captioner
// rather than return early on the matching signature.
//
// MUTATION, internal/engine/engine.go: drop the cur.Running() check in
// captionerIsCurrent. Observed: FAIL -- the reconcile kept the dead captioner.
func TestACaptionerWhoseTapExitedIsRebuiltOnTheNextReconcile(t *testing.T) {
	e, _ := captionEngine(t, "exit")
	e.reconcileCaptions()
	first := currentCaptioner(e)
	if first == nil {
		t.Fatalf("no captioner started; warning %q", e.LiveCaptions().Warning)
	}
	waitStopped(t, first)

	e.reconcileCaptions()
	if next := currentCaptioner(e); next == nil || next == first {
		t.Fatalf("captioner after the reconcile = %p, want a new one in place of the dead %p", next, first)
	}
}

// TestATapThatKeepsDyingRightAfterStartIsSwitchedOffAfterTheRestartBudget is
// #4b. Once a dead captioner counts as not current, a tap that dies at once (a
// relay that is gone, a whisper that crashes) would be rebuilt on every
// reconcile, each time with a new FFmpeg child. After captionRestartLimit quick
// self-exits in a row the feature is switched off with a reason, as a captioner
// that cannot start is. Each exit also publishes a status, so the dashboard stops
// showing a captioner that is not running, and asks for a reconcile, so the
// rebuild does not wait for an unrelated save.
//
// MUTATION, internal/engine/engine.go in reconcileCaptions: never give up (treat
// every self-exit as within budget). Observed: FAIL -- started 5 captioners, want
// 3. And dropping the publishCaptionStatus in onCaptionerExited: FAIL -- no
// status reported a stopped captioner while captions were still enabled.
func TestATapThatKeepsDyingRightAfterStartIsSwitchedOffAfterTheRestartBudget(t *testing.T) {
	e, woken := captionEngine(t, "exit")
	sub := e.bus.Subscribe(events.TypeCaption)
	defer sub.Close()

	starts := 0
	for i := 0; i < captionRestartLimit+2; i++ {
		e.reconcileCaptions()
		c := currentCaptioner(e)
		if c == nil {
			break
		}
		starts++
		waitStopped(t, c)
		select {
		case <-woken:
		case <-time.After(5 * time.Second):
			t.Fatalf("start %d: the captioner exited without asking for a reconcile", starts)
		}
	}
	if starts != captionRestartLimit {
		t.Fatalf("started %d captioners for a tap that dies at once, want %d", starts, captionRestartLimit)
	}
	st := e.LiveCaptions()
	if st.Enabled || st.Running {
		t.Fatalf("after the budget: enabled %t, running %t; want captions switched off", st.Enabled, st.Running)
	}
	if st.Warning == "" {
		t.Fatalf("captions were switched off with no warning for the operator")
	}

	// A status with Running false went out for an exit, not only for the give-up.
	sawStopped := false
	for {
		select {
		case ev := <-sub.C:
			if ce, ok := ev.Data.(CaptionEvent); ok && ce.Status != nil && !ce.Status.Running && ce.Status.Enabled {
				sawStopped = true
			}
			continue
		default:
		}
		break
	}
	if !sawStopped {
		t.Fatalf("no caption status reported a stopped captioner while captions were still enabled")
	}
}

// TestTheRestartBudgetCountsOnlyQuickExitsInARowAndAFreshSwitchOnResetsIt pins
// the counting rule without real time. A run that lasted resets the count,
// because a captioner that worked for an hour and then lost its relay is worth
// restarting. The operator switching captions on again also resets it.
//
// MUTATION, internal/engine/engine.go in captionRestartBudget.exited: drop the
// reset for a long run. Observed: FAIL -- gave up on quick exit 1 after the
// long run.
func TestTheRestartBudgetCountsOnlyQuickExitsInARowAndAFreshSwitchOnResetsIt(t *testing.T) {
	var b captionRestartBudget
	quick, long := time.Second, captionQuickExit+time.Second
	for i := 1; i < captionRestartLimit; i++ {
		if b.exited(quick) {
			t.Fatalf("gave up after %d quick exits, want %d", i, captionRestartLimit)
		}
	}
	if b.exited(long) {
		t.Fatalf("gave up on a run that lasted %s", long)
	}
	for i := 1; i < captionRestartLimit; i++ {
		if b.exited(quick) {
			t.Fatalf("gave up on quick exit %d after a long run reset the count", i)
		}
	}
	if !b.exited(quick) {
		t.Fatalf("did not give up after %d quick exits in a row", captionRestartLimit)
	}

	e := failoverEngine(t)
	e.captBudget = b
	cfg := transcribe.DefaultLiveConfig()
	cfg.Enabled = true
	if err := e.setCaptionConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if e.captBudget.quick != 0 {
		t.Fatalf("switching captions on kept a count of %d quick exits", e.captBudget.quick)
	}
}

// TestAStartedCaptionerIsNotRestartedByTheNextReconcileButAThreadsChangeIs is #18.
// The signature was hashed before the model was defaulted, and the stored config
// then gained the model, so the reconcile right after a start always saw a
// different signature and restarted the captioner. It also left out Threads,
// Health, Denoise and Speaker, so a change to one of them was never applied.
//
// Failing first, before the fix: the unchanged second reconcile restarted the
// captioner.
//
// MUTATION, internal/engine/engine.go in captionSig: zero Threads before hashing.
// Observed: FAIL -- a Threads change did not restart the captioner.
func TestAStartedCaptionerIsNotRestartedByTheNextReconcileButAThreadsChangeIs(t *testing.T) {
	e, _ := captionEngine(t, "hold")
	e.reconcileCaptions()
	first := currentCaptioner(e)
	if first == nil || !first.Running() {
		t.Fatalf("no running captioner; warning %q", e.LiveCaptions().Warning)
	}
	e.reconcileCaptions()
	if again := currentCaptioner(e); again != first {
		t.Fatalf("an unchanged reconcile restarted the captioner (%p -> %p)", first, again)
	}

	// A thread count different from the resolved one, and inside the clamp.
	threads := 1
	if first.Config().Threads == 1 {
		threads = 2
	}
	e.mu.Lock()
	e.captCfg.Threads = threads
	e.mu.Unlock()
	e.reconcileCaptions()
	next := currentCaptioner(e)
	if next == first || next == nil {
		t.Fatalf("a Threads change did not restart the captioner")
	}
	if got, want := next.Config().Threads, threads; got != want {
		t.Fatalf("restarted captioner runs %d threads, want %d", got, want)
	}
}

// TestStartingACaptionerKeepsTheOperatorsAutomaticTrackAndBlankModel is #19.
// After a start the stored config was replaced by the captioner's resolved one,
// so "automatic" became a fixed track and a blank model became the default's
// name; a later source change then kept the old track. The operator config is
// kept as it was, and the status reports what is running from the captioner.
//
// MUTATION, internal/engine/engine.go: put back e.captCfg = capt.Config() after
// the start. Observed: FAIL -- stored config after start: track 0, model "base".
func TestStartingACaptionerKeepsTheOperatorsAutomaticTrackAndBlankModel(t *testing.T) {
	e, _ := captionEngine(t, "hold")
	e.reconcileCaptions()
	c := currentCaptioner(e)
	if c == nil {
		t.Fatalf("no captioner started; warning %q", e.LiveCaptions().Warning)
	}
	e.mu.RLock()
	stored := e.captCfg
	e.mu.RUnlock()
	if stored.Track != transcribe.LiveTrackAuto || stored.Model != "" {
		t.Fatalf("stored config after start: track %d, model %q; want the operator's automatic track and blank model",
			stored.Track, stored.Model)
	}
	st := e.LiveCaptions()
	if st.Track != c.Config().Track || st.Model != c.Config().Model || st.Model == "" {
		t.Fatalf("status reports track %d model %q, want the running %d %q", st.Track, st.Model, c.Config().Track, c.Config().Model)
	}
	if st.Config.Track != transcribe.LiveTrackAuto {
		t.Fatalf("status config track = %d, want the operator's automatic choice", st.Config.Track)
	}
}

// TestSwitchingCaptionsOffPublishesAStatusThatSaysTheyStopped is #20. The
// teardown path returned without a status event, so a dashboard kept showing a
// running captioner until something else published.
//
// MUTATION, internal/engine/engine.go in reconcileCaptions: drop the
// publishCaptionStatus on the want == "" return. Observed: FAIL -- no status.
func TestSwitchingCaptionsOffPublishesAStatusThatSaysTheyStopped(t *testing.T) {
	e, _ := captionEngine(t, "hold")
	e.reconcileCaptions()
	if currentCaptioner(e) == nil {
		t.Fatalf("no captioner started; warning %q", e.LiveCaptions().Warning)
	}
	sub := e.bus.Subscribe(events.TypeCaption)
	defer sub.Close()

	e.mu.Lock()
	e.captOn, e.captCfg.Enabled = false, false
	e.mu.Unlock()
	e.reconcileCaptions()
	if currentCaptioner(e) != nil {
		t.Fatalf("captions switched off but the captioner is still installed")
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-sub.C:
			if ce, ok := ev.Data.(CaptionEvent); ok && ce.Status != nil && !ce.Status.Running {
				return
			}
		case <-deadline:
			t.Fatalf("switching captions off published no status saying they stopped")
		}
	}
}
