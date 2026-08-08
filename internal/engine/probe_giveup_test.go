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
}
