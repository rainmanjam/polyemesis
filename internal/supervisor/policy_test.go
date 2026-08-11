package supervisor

// The live restart policy.
//
// These three values are the only part of a Spec that never reaches the child's
// argv -- they govern what the SUPERVISOR does after it exits -- so they are the
// only part that can honestly change without replacing the process. Everything
// here is about proving that a change lands on a process that is already
// running, and that it lands without granting anything a fresh set of lives.

import (
	"context"
	"testing"
	"time"
)

// A destination crawling on a 30s ceiling must come back sooner when the
// operator lowers the ceiling, not after the wait it was already in.
func TestSetPolicyShortensAnInFlightBackoff(t *testing.T) {
	p := testProcess(t, fakeExit(1), Spec{
		AutoRestart: true,
		MinBackoff:  30 * time.Second,
		MaxBackoff:  30 * time.Second,
	})
	p.Start()
	waitFor(t, "the first backoff to begin", func() bool {
		return p.Status().State == StateReconnecting
	})

	start := time.Now()
	p.SetPolicy(Policy{MinBackoff: 10 * time.Millisecond, MaxBackoff: 10 * time.Millisecond})

	waitFor(t, "a respawn under the new ceiling", func() bool { return p.Status().Restarts >= 2 })
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("respawn took %s; the retune did not shorten the wait already in flight", took)
	}
}

// The opposite direction. Raising the ceiling is a statement about FUTURE
// waits: an operator who raises it does not expect the destination they are
// currently staring at to go quiet for longer than it already promised.
func TestSetPolicyNeverLengthensAnInFlightBackoff(t *testing.T) {
	p := testProcess(t, fakeExit(1), Spec{
		AutoRestart: true,
		MinBackoff:  50 * time.Millisecond,
		MaxBackoff:  50 * time.Millisecond,
	})
	p.Start()
	waitFor(t, "the first backoff to begin", func() bool {
		return p.Status().State == StateReconnecting
	})

	start := time.Now()
	p.SetPolicy(Policy{MinBackoff: 30 * time.Second, MaxBackoff: 30 * time.Second})

	waitFor(t, "the in-flight wait to finish on its original deadline", func() bool {
		return p.Status().Restarts >= 2
	})
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("the in-flight wait was extended to %s by a policy change", took)
	}
}

// A settings save touches every destination. If SetPolicy reset the counters,
// saving a log level would quietly grant every destination that had been
// failing all night a fresh set of lives, and one that should have given up
// would retry for ever -- which is the exact condition GiveUpAfter exists to
// end.
func TestSetPolicyDoesNotResetTheRestartCounters(t *testing.T) {
	p := testProcess(t, fakeExit(1), Spec{
		AutoRestart: true,
		MinBackoff:  time.Millisecond,
		MaxBackoff:  time.Millisecond,
	})
	p.Start()
	waitFor(t, "three restarts", func() bool { return p.Status().Restarts >= 3 })

	before := p.Status().Restarts
	p.SetPolicy(Policy{MinBackoff: time.Millisecond, MaxBackoff: time.Millisecond, MaxRestarts: 500})
	if got := p.Status().Restarts; got < before {
		t.Fatalf("restarts went backwards: %d -> %d; a policy change must not forgive history", before, got)
	}
}

// Lowering the limit below what a process has already spent must not execute it
// where it stands. It is judged on its next exit, under the new rule.
func TestLoweringMaxRestartsAppliesFromTheNextExitRatherThanRetroactively(t *testing.T) {
	p := testProcess(t, fakeSleep(30*time.Second), Spec{
		AutoRestart: true,
		MinBackoff:  time.Millisecond,
		MaxBackoff:  time.Millisecond,
	})
	p.Start()
	waitFor(t, "the child to be running", func() bool { return p.Status().State == StateRunning })

	p.SetPolicy(Policy{MinBackoff: time.Millisecond, MaxBackoff: time.Millisecond, MaxRestarts: 1})

	if got := p.Status().State; got != StateRunning {
		t.Fatalf("state = %s, want running: a running child must not be failed by a policy change", got)
	}
}

// A reconciling teardown of a reconnecting destination has no child to signal,
// so it must not pay the 8s grace or the 12s stop budget. Eight of them in one
// reconcile is the case that makes this matter.
func TestStoppingAReconnectingProcessReturnsPromptly(t *testing.T) {
	p := testProcess(t, fakeExit(1), Spec{
		AutoRestart: true,
		MinBackoff:  30 * time.Second,
		MaxBackoff:  30 * time.Second,
	})
	p.Start()
	waitFor(t, "the backoff to begin", func() bool { return p.Status().State == StateReconnecting })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_ = p.Stop(ctx)

	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Stop took %s on a process with no child; teardown of a reconnecting "+
			"destination must not wait out its backoff", took)
	}
	if got := p.Status().State; got != StateStopped {
		t.Fatalf("state = %s, want stopped", got)
	}
}

// Reviving a process that gave up.
//
// supervise returns down the give-up path WITHOUT clearing p.running, so
// Start() on a failed process is a silent no-op -- it takes the `if p.running`
// early return. Only Restart() revives it, and Stop() inside Restart returns
// immediately because done is already closed. The engine's "you raised the
// give-up limit, so this destination comes back" path is built on this, so it
// is pinned here rather than left to be rediscovered.
func TestAFailedProcessIsRevivedByRestartAndNotByStart(t *testing.T) {
	p := testProcess(t, fakeExit(1), Spec{
		AutoRestart: true,
		MinBackoff:  time.Millisecond,
		MaxBackoff:  time.Millisecond,
		MaxRestarts: 1,
	})
	p.Start()
	waitFor(t, "the process to give up", func() bool { return p.Status().State == StateFailed })

	p.Start()
	if got := p.Status().State; got != StateFailed {
		t.Fatalf("state = %s after Start on a failed process, want it unchanged at failed", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.SetPolicy(Policy{MinBackoff: time.Millisecond, MaxBackoff: time.Millisecond})
	p.Restart(ctx)

	waitFor(t, "the revived process to run again", func() bool { return p.Status().Restarts >= 2 })
}
