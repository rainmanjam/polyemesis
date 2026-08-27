package engine

import (
	"context"
	"os"
	"strings"
	"testing"
)

// SETTING THE SWITCH TO THE POSITION IT ALREADY CLAIMS MUST STILL RECONCILE.
//
// #612. `loudOff` is the zero value, so the analyser tier reads as wanted from
// the moment an engine is built -- but analysers are started only by
// reconcileLoudness, and on a fresh install the pass that runs it happens while
// destinations are still held waiting for the ingest probe. It correctly finds
// nothing to measure. Nothing reconciles the tier once they start, and
// SetLoudnessMonitor(true) returned early because the flag already said on.
//
// The install then reported `enabled: true` with no analyser ever having run.
// GET /loudness said so, the Meters page drew the switch on, and nothing
// measured. Only an off-then-on toggle recovered it, because the off transition
// was the only one that reached a reconcile.
//
// Asserted as "the call reconciles", not "an analyser appears": this fixture
// has no live ingest, so no destination is eligible and no analyser can start.
// What is being pinned is that the work is ATTEMPTED rather than skipped --
// which is the whole of the defect.
//
// Mutation: restore `if e.loudOff == !enabled { return nil }`. Observed to fail
// with "setting the monitor to the value it already held did not reconcile".
func TestSettingTheLoudnessMonitorToItsCurrentValueStillReconciles(t *testing.T) {
	m, store := managerFixture(t)
	addSource(t, store, "studio")
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	eng := m.Default()
	if eng == nil {
		t.Fatal("no engine in the fixture")
	}

	// The tier is wanted by default, so this is exactly the no-op the old code
	// skipped -- and the call an operator makes when the switch already shows
	// on and nothing is measuring.
	before := eng.reconcileCount()
	if err := eng.SetLoudnessMonitor(true); err != nil {
		t.Fatalf("SetLoudnessMonitor(true): %v", err)
	}
	if got := eng.reconcileCount(); got == before {
		t.Errorf("setting the monitor to the value it already held did not reconcile "+
			"(count stayed %d). That is #612: the switch reports enabled while no "+
			"analyser has ever run, and only an off-then-on toggle recovers it.", got)
	}

	// THE CONTROL: a real transition must reconcile too, which the old code did
	// do. A fix that broke this would trade one silent failure for another.
	before = eng.reconcileCount()
	if err := eng.SetLoudnessMonitor(false); err != nil {
		t.Fatalf("SetLoudnessMonitor(false): %v", err)
	}
	if got := eng.reconcileCount(); got == before {
		t.Error("turning the monitor off did not reconcile")
	}
}

// THE PROBE PATH RUNS THE SAME TAIL AS Reconcile.
//
// #612's root cause. Destinations are HELD on a fresh install until the ingest
// probe lands, and the probe-landed path at engine.go ran three of Reconcile's
// steps -- meters, recorder, outputs -- and stopped. reconcileOutputs is what
// starts the destinations; reconcileLoudness is the only thing that starts
// analysers; and Reconcile is event-driven with no ticker, so nothing ran the
// rest. The analyser tier was reconciled exactly once, at the only moment it
// was guaranteed to have nothing to do.
//
// Asserted as SOURCE SHAPE rather than behaviour, deliberately. Reproducing it
// needs a real probe against a real ingest, which this package's fixtures do
// not have -- it took a container, a synthetic stream and four rounds of
// instrumentation to see it live. What can be pinned cheaply is the property
// that actually broke: the probe path calls the same consumer steps Reconcile
// does. A subset copied by hand drifts the moment the sequence grows, which is
// precisely what happened here.
//
// Mutation: delete the reconcileLoudness call from the probe path. Observed to
// fail naming it.
func TestTheProbePathReconcilesEverythingReconcileDoes(t *testing.T) {
	src, err := os.ReadFile("engine.go")
	if err != nil {
		t.Fatalf("read engine.go: %v", err)
	}
	body := string(src)

	// The probe path is the only other place that takes reconcileMu itself.
	const marker = "_ = e.reconcileOutputs()"
	at := strings.Index(body, marker)
	if at < 0 {
		t.Fatal("cannot find the probe path's reconcileOutputs call; this guard is " +
			"comparing nothing")
	}
	// Everything between that call and the unlock that closes the section.
	rest := body[at:]
	end := strings.Index(rest, "e.reconcileMu.Unlock()")
	if end < 0 {
		t.Fatal("the probe path's reconcileMu section has no Unlock; shape changed")
	}
	section := rest[:end]

	// The consumers Reconcile runs after the outputs, and why each is here:
	// every one reads a hub reconcileOutputs may have just replaced.
	for _, step := range []string{
		"e.reconcilePreview(",
		"e.reconcileClips()",
		"e.reconcileCaptions()",
		"e.reconcileLoudness(",
	} {
		if !strings.Contains(section, step) {
			t.Errorf("the probe path starts destinations and then does not call %s. "+
				"That is #612: on a fresh install destinations are held until the probe "+
				"lands, this path starts them, and nothing runs Reconcile again -- so "+
				"whatever this skips is never reconciled at all.", step)
		}
	}
}
