package supervisor

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// cmdForKill's WHOLE JOB IS TO REFUSE A SUCCESSOR, EVEN WHEN `done` WAS OPEN A
// MOMENT AGO.
//
// The first version of the nil-cmd fallback re-read p.cmd unconditionally
// once it found `done` not yet closed -- but that check and the read were two
// separate moments, and the caller's outer select only proves `done` was open
// AT THAT SELECT, not at the read a preKill pause (or plain descheduling) can
// follow it with. In the gap, this generation can finish, `done` can close,
// and a pending Start can publish a successor's cmd; an unconditional re-read
// then adopts it.
//
// This drives cmdForKill directly, synthesising exactly that interleaving --
// `done` already closed, p.cmd already holding a "successor" -- without the
// timing choreography a full Stop()/Start() race would need, because the
// method's whole contract is expressible as a pure function of its inputs.
func TestCmdForKillRefusesASuccessorPublishedAfterDoneClosed(t *testing.T) {
	p := New(discardLog(), Spec{Name: "cmdForKill"})

	successor := &exec.Cmd{}
	successorExited := make(chan struct{})
	p.cmdMu.Lock()
	p.cmd, p.exited = successor, successorExited
	p.cmdMu.Unlock()

	done := make(chan struct{})
	close(done) // this generation finished; a pending Start published the successor

	gotCmd, gotExited := p.cmdForKill(done, nil, nil)
	if gotCmd != nil || gotExited != nil {
		t.Fatalf("cmdForKill(done closed, nil, nil) = (%v, %v), want (nil, nil): it adopted "+
			"p.cmd although `done` had already closed, which is exactly the successor this "+
			"method exists to refuse", gotCmd, gotExited)
	}
}

// And the ordinary case must still work: `done` open and p.cmd already
// published is this generation's own, and the fallback must return it.
func TestCmdForKillAdoptsALateSameGenerationPublish(t *testing.T) {
	p := New(discardLog(), Spec{Name: "cmdForKill"})

	own := &exec.Cmd{}
	ownExited := make(chan struct{})
	p.cmdMu.Lock()
	p.cmd, p.exited = own, ownExited
	p.cmdMu.Unlock()

	done := make(chan struct{}) // not closed: this generation is still running

	gotCmd, gotExited := p.cmdForKill(done, nil, nil)
	if gotCmd != own || gotExited != (chan struct{})(ownExited) {
		t.Fatalf("cmdForKill(done open, nil, nil) = (%v, %v), want the published (%v, %v): "+
			"a spawn that finished publishing between stop()'s original nil snapshot and this "+
			"call must still be killed", gotCmd, gotExited, own, ownExited)
	}
}

// A non-nil snapshot is already this generation's own and must pass straight
// through, untouched by whatever p.cmd holds now.
func TestCmdForKillPassesThroughANonNilSnapshot(t *testing.T) {
	p := New(discardLog(), Spec{Name: "cmdForKill"})

	pinned := &exec.Cmd{}
	pinnedExited := make(chan struct{})

	// p.cmd deliberately set to something ELSE, to prove it is never consulted.
	p.cmdMu.Lock()
	p.cmd, p.exited = &exec.Cmd{}, make(chan struct{})
	p.cmdMu.Unlock()

	done := make(chan struct{})
	close(done)

	gotCmd, gotExited := p.cmdForKill(done, pinned, pinnedExited)
	if gotCmd != pinned || gotExited != (chan struct{})(pinnedExited) {
		t.Fatalf("cmdForKill with a non-nil snapshot = (%v, %v), want the snapshot unchanged "+
			"(%v, %v): a pinned cmd must never be swapped out, regardless of `done` or p.cmd",
			gotCmd, gotExited, pinned, pinnedExited)
	}
}

// A KILL ISSUED AFTER THE DEADLINE MUST NOT REACH A CHILD THAT WAS NEVER THE
// ONE BEING STOPPED.
//
// stop()'s deadline arm used to call p.kill(), which re-reads p.cmd and
// p.exited fresh at call time. Between ctx.Done() firing and that call
// actually running -- a window a descheduled goroutine can hold open for a
// while on a loaded machine -- this generation's child can already have been
// reaped by terminate()'s own escalator, which races the same deadline and is
// itself generation-safe (it closes over the cmd it signalled). A pending
// Start queued behind that reap -- Restart's own second half, or any other
// caller that calls Start on a Process it does not know is mid-Stop -- can
// then bring up a whole new generation and publish its cmd before the
// deadline arm's kill gets around to running. A fresh p.cmd read at that point
// names the SUCCESSOR, and the SIGKILL meant for a child that ignored SIGTERM
// lands on one that was never asked to leave.
//
// This test forces that interleaving with the preKill seam: the deadline
// arm's kill is paused, under test control, at exactly the point it is about
// to run -- after it has already decided the loop has not finished and before
// it touches anything. Inside the pause, generation one is reaped by its own
// terminate() escalator (a legitimate, independently generation-safe kill), a
// pending Start fires behind it, and generation two comes up and publishes
// its own cmd. Only then is the pause released.
//
// THE ASSERTION IS THAT GENERATION TWO'S CHILD SURVIVES. It has done nothing
// wrong and nobody has asked to stop it; a kill correctly scoped to the
// generation this Stop actually targeted must find nothing left to do.
//
// Mutation check: in stop(), the deadline arm's `p.killCmd(cmd, exited)` was
// temporarily reverted to `p.kill()` (the old fresh-read call, dropping the
// pinned snapshot). Observed to fail exactly as predicted: generation two's
// child was dead when this test checked it, because the reverted kill()
// re-read p.cmd/p.exited after the respawn and killed the live successor
// instead of finding the (already reaped) generation it meant to act on.
// Restored the fix and reran: passes.
func TestStopsDeadlineKillDoesNotHitTheNextGeneration(t *testing.T) {
	rec := newRecorder()
	p := testProcess(t, fakeDeaf(30*time.Second), Spec{OnState: rec.onState})
	// Short, so the escalator reaps generation one well inside the pause below,
	// entirely on its own.
	p.grace = 80 * time.Millisecond

	gate := make(chan struct{})
	proceed := make(chan struct{})
	var once sync.Once
	p.preKill = func() {
		fired := false
		once.Do(func() { fired = true })
		if !fired {
			// The cleanup Stop at the end of this test may also reach the
			// deadline arm; it must pass straight through rather than block
			// forever on a gate only the test body is going to open.
			return
		}
		close(gate)
		<-proceed
	}

	p.Start()
	waitFor(t, "the first child to start", func() bool { return p.Status().State == StateRunning })
	waitForDeaf(t, p)

	// Shorter than p.grace, so stop()'s own deadline fires first, while the
	// escalator has not yet reaped the child: the inner tie-break select takes
	// `default`, and the deadline arm is the one about to kill.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	stopErr := make(chan error, 1)
	go func() { stopErr <- p.stop(ctx, false) }()

	select {
	case <-gate:
	case <-time.After(waitTimeout):
		t.Fatal("stop() never reached its deadline kill")
	}

	// p.running was already cleared by stop(), so this Start queues behind the
	// still-unwound first generation rather than launching a second loop
	// directly -- see Start()'s startPending gate.
	p.Start()
	p.runMu.Lock()
	pending := p.startPending
	p.runMu.Unlock()
	if !pending {
		t.Fatal("the Start during the held kill was not left pending: this test's precondition is gone")
	}

	// Let the escalator reap generation one and the pending Start bring up
	// generation two, entirely while the deadline kill is still paused.
	waitFor(t, "a second child to start", func() bool {
		return rec.distinctPIDs() == 2 && p.Status().State == StateRunning
	})
	secondPID := p.Status().PID

	close(proceed) // release the paused kill

	select {
	case err := <-stopErr:
		if !errors.Is(err, ErrStopDeadline) {
			t.Fatalf("stop = %v, want ErrStopDeadline: the deaf child should have outlasted the "+
				"20ms deadline", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("stop() never returned after its kill was released")
	}

	// GIVE A WRONGLY-SCOPED KILL TIME TO LAND. SIGKILL/TerminateProcess is
	// fast; this margin is generous against the kill itself, not against
	// anything the fix is slow at.
	time.Sleep(300 * time.Millisecond)
	if !alive(secondPID) {
		t.Fatalf("the second generation's child (pid %d) is dead: the deadline kill left over "+
			"from stopping the FIRST generation reached the second one instead", secondPID)
	}
}
