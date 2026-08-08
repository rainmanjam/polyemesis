package engine

import (
	"strings"
	"testing"
)

// The hold must have an exit when a probe can NEVER succeed.
//
// wantSilence itself requires `measured`, so a missing or broken ffprobe left
// silenceSig empty and measured false forever -- every destination down
// permanently, with the one tier that could have lifted the hold structurally
// unable to. It was the second time this guard produced a permanent outage.
func TestTheHoldLiftsOnceProbingHasDefinitivelyFailed(t *testing.T) {
	e := &Engine{}

	if e.probeUnmeasurable() {
		t.Fatal("a fresh engine already considers its layout unmeasurable; every " +
			"destination would start on a guess before anything was tried")
	}

	// A transient failure must ride through: the hold is correct while a probe
	// might still land.
	for i := 1; i < probeGiveUp; i++ {
		e.probeFails.Store(int64(i))
		if e.probeUnmeasurable() {
			t.Fatalf("gave up after %d failure(s) of %d; a relay port not yet bound "+
				"or a stream whose first packets are not yet decodable would start "+
				"every destination on a guess it did not need", i, probeGiveUp)
		}
	}

	e.probeFails.Store(probeGiveUp)
	if !e.probeUnmeasurable() {
		t.Errorf("still holding after %d consecutive failures; a broken ffprobe takes "+
			"every destination off air for the length of the event", probeGiveUp)
	}

	// And it reverts the instant a probe succeeds. This is the property a
	// timeout could not have: a timeout fires just as readily while a probe is
	// merely slow, which reintroduces the original bug on a schedule.
	e.probeFails.Store(0)
	if e.probeUnmeasurable() {
		t.Error("a recovered probe did not restore the hold")
	}
}

// A probe that RUNS and identifies nothing must count toward giving up.
//
// This is the "unidentifiable stream" case the exit was written for, and the
// counter reset used to sit above the zero-stream early return -- so this branch
// cleared it on every attempt and it could never reach probeGiveUp. The one
// shape the exit most needed to cover was the one it could not reach. Both
// reviewers found it independently.
func TestAProbeThatIdentifiesNothingCountsTowardGivingUp(t *testing.T) {
	src := readEngineFile(t, "engine.go")
	body := funcBody(t, src, "func (e *Engine) probeOnce(ctx context.Context) bool {")

	nothing := strings.Index(body, "if res.Video == nil && len(res.Audio) == 0 {")
	reset := strings.Index(body, "e.probeFails.Store(0)")
	if nothing < 0 {
		t.Fatal("cannot find the identified-nothing branch")
	}
	if reset >= 0 && reset < nothing {
		t.Error("the failure counter is reset BEFORE the identified-nothing return, " +
			"so a stream ffprobe runs against but cannot identify clears the count " +
			"on every attempt and never reaches probeGiveUp -- held forever, which " +
			"is the exact outage the exit exists to end")
	}
	branch := body[nothing:]
	if end := strings.Index(branch, "\n\t}"); end > 0 {
		branch = branch[:end]
	}
	if !strings.Contains(branch, "e.probeFails.Add(1)") {
		t.Error("an identified-nothing probe does not count as a failure to measure")
	}
}

// The failure history belongs to a transport. Switching ingest mode must clear
// it, or five failed RTMP probes start destinations provisionally on the very
// next reconcile after a switch to SRT -- before one SRT probe has been tried.
// The SRT path returns early without replacing the ingest child, so the reset
// at ingest start never runs.
func TestAModeChangeClearsTheFailureHistory(t *testing.T) {
	src := readEngineFile(t, "engine.go")
	body := funcBody(t, src, "func (e *Engine) reconcileIngest(s, prev db.Settings) {")
	at := strings.Index(body, "if e.measuredMode != s.Ingest.Mode {")
	if at < 0 {
		t.Fatal("cannot find the ingest-mode change branch")
	}
	rest := body[at:]
	if end := strings.Index(rest, "\n\te.mu.Unlock()"); end > 0 {
		rest = rest[:end]
	}
	if !strings.Contains(rest, "e.probeFails.Store(0)") {
		t.Error("an ingest-mode change leaves the previous transport's probe failures " +
			"on the counter, so the new transport can be declared unmeasurable " +
			"before it has been probed once")
	}
}

// The exit has to be WIRED IN. The test above would pass with reconcileOutputs
// never consulting it.
func TestTheHoldExitIsWiredIntoReconcileOutputs(t *testing.T) {
	src := readEngineFile(t, "engine.go")

	if !strings.Contains(src, "unmeasurable := !measured && e.probeUnmeasurable()") {
		t.Error("reconcileOutputs no longer asks whether the layout is unmeasurable; " +
			"a probe that can never succeed holds every destination down forever")
	}
	if !strings.Contains(src, "holdDests := !measured && silenceSig == \"\" && !unmeasurable") {
		t.Error("the hold no longer has its exit; see probeUnmeasurable")
	}
	// And the plan must be built provisionally when it is taken, or the guessed
	// matrices come straight back and with them the silent dialogue loss.
	if !strings.Contains(src, "e.planDestinations(destRows, wantRends, src, srcSig, unmeasurable)") {
		t.Error("planDestinations is no longer told the layout is a guess, so it " +
			"compiles the placeholder's channel counts into a real pan matrix -- " +
			"which publishes front L/R and discards centre without erroring")
	}
	// The counter must reset on a new ingest, or the next stream starts
	// provisionally before anything has been tried.
	if !strings.Contains(src, "e.probeFails.Store(0)") {
		t.Error("the consecutive-failure count is never reset")
	}

	// THE LOAD-BEARING ASSUMPTION, pinned because everything above rests on it.
	//
	// probeOnce runs only when bytes are on the relay, so a failure means "there
	// IS a stream and we cannot identify it" -- never "no encoder has connected".
	// If probing were ever attempted on an idle relay, this change would start
	// every destination fifteen seconds after boot against a stream that does
	// not exist, which is the opposite of what the hold is for.
	// THE EXIT MUST FIRE BY ITSELF.
	//
	// This is the defect both reviewers led with, and it made the whole fix
	// inert: probeLoop reconciled only when probeOnce reported a LAYOUT CHANGE,
	// and probeOnce returns false on every failure -- so the fifth failure, the
	// one that declares the layout unmeasurable, looked exactly like the four
	// before it. The log line went out and nothing re-planned. Destinations
	// stayed held until some unrelated HTTP request happened to call Reconcile,
	// which on an unattended box is never.
	if !strings.Contains(src, "wasUnmeasurable := e.probeUnmeasurable()") ||
		!strings.Contains(src, "changed || e.probeUnmeasurable() != wasUnmeasurable") {
		t.Error("probeLoop reconciles only on a layout change, so the hold's exit is " +
			"never acted on: the state flips, the log says destinations are starting, " +
			"and nothing plans them")
	}

	loop := funcBody(t, src, "func (e *Engine) probeLoop(ctx context.Context) {")
	gate := strings.Index(loop, "if flowing {")
	call := strings.Index(loop, "e.probeOnce(ctx)")
	if gate < 0 || call < 0 || call < gate {
		t.Error("probeOnce is no longer gated on the relay carrying data. A probe " +
			"attempted on an idle relay fails for want of a stream, and after " +
			"probeGiveUp such failures every destination would start provisionally " +
			"against an ingest nobody has connected")
	}
}
