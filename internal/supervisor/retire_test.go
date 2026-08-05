package supervisor

import (
	"context"
	"testing"
	"time"
)

// A8, the half the first remediation left open.
//
// Stop on a process that had not been started was a silent no-op. Every caller
// that builds a process, publishes it where a shutdown can find it, and only
// THEN calls Start therefore had a window: the shutdown took the published
// entry, called Stop on a process that was not running, released the relay port
// and the hub subscription it had been given -- and the original caller then
// started a child on both. internal/engine's destinations and their backup
// ingest, its renditions, and internal/playout's variants all have that shape,
// which is why the latch is here and not re-derived at each of them.
//
// Mutation: in Start, delete `p.retired ||` from `if p.retired || p.running`.
// Observed to fail on the first assertion.
func TestStopBeforeStartRetiresTheProcessForEver(t *testing.T) {
	rec := newRecorder()
	// AutoRestart, because that is what makes the orphan permanent rather than
	// merely wrong: a destination's backup is built this way, so a child that
	// escapes here reconnects to the platform for ever.
	p := testProcess(t, fakeSleep(time.Minute), Spec{AutoRestart: true, OnState: rec.onState})

	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	p.Stop(ctx)

	p.Start()

	// Decided by the time Start returns, with no waiting and no timing: Start
	// sets p.running under runMu BEFORE it launches the supervisor goroutine, so
	// a Start that did anything at all is already visible here.
	p.runMu.Lock()
	running := p.running
	p.runMu.Unlock()
	if running {
		t.Fatal("Start after Stop began supervising again: the caller that was " +
			"about to Start has just brought a child up behind a shutdown that " +
			"already released its port and its subscription, and nothing holds a " +
			"reference to it")
	}

	// Corroboration rather than the guard. Absence can only be checked by giving
	// a spawn a real chance, and a spawn the mutation permits announces itself in
	// single-digit milliseconds.
	time.Sleep(250 * time.Millisecond)
	if n := rec.distinctPIDs(); n != 0 {
		t.Errorf("%d child process(es) were spawned by a retired process", n)
	}
	if rec.saw(StateStarting) || rec.saw(StateRunning) {
		t.Error("a retired process announced itself as starting or running")
	}
	if got := p.Status().State; got != StateStopped {
		t.Errorf("state = %s, want %s", got, StateStopped)
	}
}

// The other half of the same rule, and the one a terminal latch could easily
// have broken: Restart is a cycle, not a retirement.
//
// TestAFailedProcessIsRevivedByRestartAndNotByStart in policy_test.go already
// depends on this, and the engine's retunePolicy and its two Restart callers in
// engine.go are built on it. Stated here as well because it is the invariant
// that decides whether Stop's latch may be set by Restart's internal stop, and
// a reader of Stop needs it in front of them.
//
// Mutation: in Restart, replace `p.stop(ctx, false)` with `p.stop(ctx, true)`.
// Observed to fail -- the restarted process never came back.
func TestRestartDoesNotRetireTheProcess(t *testing.T) {
	p := testProcess(t, fakeExit(0), Spec{
		AutoRestart: true,
		MinBackoff:  time.Millisecond,
		MaxBackoff:  time.Millisecond,
	})
	p.Start()
	waitFor(t, "the first run", func() bool { return p.Status().Restarts >= 1 })

	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	p.Restart(ctx)

	waitFor(t, "the cycled process to run again", func() bool {
		return p.Status().Restarts >= 1
	})
	p.runMu.Lock()
	running := p.running
	p.runMu.Unlock()
	if !running {
		t.Fatal("Restart left the process retired; a routing-profile change would " +
			"stop the destination it was meant to cycle and never bring it back")
	}
}

// A Stop that lands between Restart's stop and Restart's start must win: the
// caller of Stop asked for the process to be gone, and Restart must not undo
// that. Sequenced through the same seam the latch itself provides -- the stop
// is issued while Restart is blocked, which is what the engine's shutdown does
// to a reconcile that is retuning a policy.
//
// Mutation: in Restart, replace `p.stop(ctx, false); p.Start()` with
// `p.stop(ctx, false); p.runMu.Lock(); p.retired = false; p.runMu.Unlock();
// p.Start()`. Observed to fail.
func TestAStopDuringARestartWins(t *testing.T) {
	p := testProcess(t, fakeSleep(time.Minute), Spec{AutoRestart: true})
	p.Start()
	waitFor(t, "the child to be running", func() bool { return p.Status().State == StateRunning })

	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	p.Stop(ctx)
	p.Restart(ctx)

	p.runMu.Lock()
	running := p.running
	p.runMu.Unlock()
	if running {
		t.Fatal("Restart revived a process that had been Stopped; shutdown is not " +
			"final if any concurrent path can undo it")
	}
}
