package engine

import (
	"testing"
	"time"
)

// THE RELATIONSHIP THE CEILING'S CORRECTNESS RESTS ON.
//
// reconcileOutputs rejected a timeout because one "fires just as readily while a
// probe is merely slow". holdCeiling answers that by sitting above the
// consecutive-failure path's own worst case, so on a stream that is actually
// being probed the counter always reaches five first and the ceiling never
// fires. Written as an assertion rather than a comment because the argument is
// arithmetic between four constants in two files, and any one of them moving
// silently turns the ceiling into the timeout that was rejected.
func TestTheHoldCeilingCannotPreemptTheCounter(t *testing.T) {
	worst := time.Duration(probeGiveUp) * (probeAttemptTimeout + probeFastCadence)
	if holdCeiling <= worst {
		t.Fatalf("holdCeiling=%s does not exceed the counter's worst case %s "+
			"(%d * (%s + %s)); it would fire while a probe was merely slow, which "+
			"is the failure reconcileOutputs rejected a timeout for",
			holdCeiling, worst, probeGiveUp, probeAttemptTimeout, probeFastCadence)
	}
	// And below the acceptance suite's 90s baseline wait, or the exit is not
	// observable by the thing that reports it missing (#473).
	if holdCeiling >= 90*time.Second {
		t.Errorf("holdCeiling=%s is not below the acceptance suite's 90s ceiling; "+
			"the hold would still outlive the wait that measures it", holdCeiling)
	}
}

func TestAnEngineHoldingNothingNeverExpires(t *testing.T) {
	e := &Engine{}
	// The dangerous direction: expiring with no hold recorded declares the
	// layout unmeasurable and starts every destination on a guess, which is the
	// outage the hold exists to prevent.
	if e.holdExpired(time.Now()) {
		t.Fatal("a fresh engine reports an expired hold; destinations would start " +
			"provisionally before anything had ever been probed")
	}
	if e.holdExpired(time.Now().Add(1000 * time.Hour)) {
		t.Error("an engine holding nothing expired merely because time passed")
	}
}

func TestTheHoldExpiresOnlyOnceTheCeilingIsReached(t *testing.T) {
	e := &Engine{}
	start := time.Now()
	e.holdBegan(start)

	if e.holdExpired(start.Add(holdCeiling - time.Second)) {
		t.Errorf("gave up one second short of holdCeiling=%s; a probe that was "+
			"about to land would be discarded", holdCeiling)
	}
	if !e.holdExpired(start.Add(holdCeiling)) {
		t.Errorf("still holding at holdCeiling=%s; this is #473, a hold with no "+
			"bound on the clock anyone measures", holdCeiling)
	}
}

// The #469 shape, in the new field: a clock restamped on every pass is a clock
// that never runs out. reconcileOutputs calls holdBegan on EVERY held reconcile.
func TestAContinuingHoldDoesNotRestartItsClock(t *testing.T) {
	e := &Engine{}
	start := time.Now()
	e.holdBegan(start)
	for i := 1; i <= 20; i++ {
		e.holdBegan(start.Add(time.Duration(i) * time.Second))
	}
	if !e.holdExpired(start.Add(holdCeiling)) {
		t.Fatal("the ceiling moved with the reconcile that observed it, so it can " +
			"never be reached -- the same defect as the give-up counter reset in #469")
	}
}

// And it must be releasable, or the next hold inherits an already-expired clock
// and starts destinations provisionally the instant one begins.
func TestReleasingTheHoldResetsItsClock(t *testing.T) {
	e := &Engine{}
	first := time.Now()
	e.holdBegan(first)
	e.holdSince.Store(0) // what reconcileOutputs does on any pass that does not hold

	later := first.Add(10 * holdCeiling)
	if e.holdExpired(later) {
		t.Fatal("a released hold still reports expired")
	}
	e.holdBegan(later)
	if e.holdExpired(later) {
		t.Error("a hold that began now is already expired; it inherited the old clock")
	}
}
