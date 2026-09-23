package supervisor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// gatedSink holds the FIRST stderr line containing match until release is
// called, and passes every other line straight through. Held inside
// appendLog, it keeps runOnce's drain -- and so the supervise goroutine and
// its `done` -- alive after the child itself has been reaped, which is the
// shape of a SIGKILLed child the kernel has not yet let go of: a stop that
// hit its deadline has returned, and the goroutine it was stopping has not.
type gatedSink struct {
	match   string
	once    sync.Once
	held    chan struct{}
	gate    chan struct{}
	release func()
}

func newGatedSink(match string) *gatedSink {
	g := &gatedSink{match: match, held: make(chan struct{}), gate: make(chan struct{})}
	var once sync.Once
	g.release = func() { once.Do(func() { close(g.gate) }) }
	return g
}

func (g *gatedSink) WriteLog(l LogLine) {
	if !strings.Contains(l.Text, g.match) {
		return
	}
	first := false
	g.once.Do(func() { first = true })
	if !first {
		return
	}
	close(g.held)
	<-g.gate
}

// A RESTART WHOSE STOP HIT ITS DEADLINE MUST NOT START A SECOND SUPERVISOR
// ALONGSIDE THE FIRST.
//
// stop() returns ErrStopDeadline without waiting for `done`: it cannot, the
// deadline is spent. Restart then called Start, which saw running == false and
// launched a new supervise loop while the old one was still inside runOnce.
// Two things followed:
//
//   - two children at once, so two pushers on one destination key; and
//   - the old loop, when it finally unwound, found its ctx cancelled and wrote
//     StateStopped over the new loop's StateRunning. The process read Stopped
//     while its child was live and publishing.
//
// The start now waits for the old loop's `done`.
func TestRestartAfterAStopDeadlineDoesNotOverlapTheOldSupervisor(t *testing.T) {
	sink := newGatedSink(deafReadyLine)
	rec := newRecorder()
	p := testProcess(t, fakeDeaf(30*time.Second), Spec{OnState: rec.onState, LogSink: sink})
	// A short drain so the old runOnce reaches its unbounded `<-drained` soon
	// after the SIGKILL, rather than two seconds later.
	p.drain = 20 * time.Millisecond
	// Registered after testProcess, so it runs first: the gate must open before
	// the cleanup Stop waits on the goroutine it is holding.
	t.Cleanup(sink.release)

	p.Start()
	<-sink.held // the deaf child's handlers are installed, and the drain is now held
	if rec.distinctPIDs() != 1 {
		t.Fatalf("want exactly one child before the restart, saw %d", rec.distinctPIDs())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := p.stop(ctx, false)
	if !errors.Is(err, ErrStopDeadline) {
		t.Fatalf("stop = %v, want ErrStopDeadline: the deaf child should have outlasted the deadline", err)
	}
	p.Start() // the second half of Restart

	// The old supervisor is still held. Nothing new may have been spawned.
	time.Sleep(300 * time.Millisecond)
	if n := rec.distinctPIDs(); n != 1 {
		t.Errorf("a second child was spawned while the first supervisor was still unwinding "+
			"(%d distinct pids): two pushers on one destination", n)
	}

	sink.release()
	waitFor(t, "the restarted child to come up", func() bool {
		return rec.distinctPIDs() == 2 && p.Status().State == StateRunning
	})
	// The old loop's last word has had time to land if it was going to. The
	// process must still read Running: its child is alive.
	time.Sleep(300 * time.Millisecond)
	if st := p.Status().State; st != StateRunning {
		t.Errorf("state after the old supervisor unwound = %q, want %q: a stale loop "+
			"overwrote the live one", st, StateRunning)
	}
}

// A Start left waiting on the old loop is a promise, not a commitment: a Stop
// that lands before the old loop ends must still retire the process, and a
// second Start in the meantime must not queue a second promise.
func TestAPendingStartHonoursAStopThatLandsFirst(t *testing.T) {
	sink := newGatedSink(deafReadyLine)
	rec := newRecorder()
	p := testProcess(t, fakeDeaf(30*time.Second), Spec{OnState: rec.onState, LogSink: sink})
	p.drain = 20 * time.Millisecond
	t.Cleanup(sink.release)

	p.Start()
	<-sink.held
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	p.Restart(ctx) // the stop hits its deadline; the start is left pending
	p.Start()      // idempotent while pending

	p.runMu.Lock()
	pending := p.startPending
	p.runMu.Unlock()
	if !pending {
		t.Fatal("the Start after a deadline stop was not left pending: this test's precondition is gone")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stopCancel()
	_ = p.Stop(stopCtx) // retires it while the start is still pending
	sink.release()

	waitFor(t, "the pending start to resolve", func() bool {
		p.runMu.Lock()
		defer p.runMu.Unlock()
		return !p.startPending
	})
	time.Sleep(200 * time.Millisecond)
	if n := rec.distinctPIDs(); n != 1 {
		t.Errorf("a retired process spawned again (%d distinct pids)", n)
	}
	if st := p.Status().State; st != StateStopped {
		t.Errorf("state = %q, want %q", st, StateStopped)
	}
}

// stop() finishes by writing StateStopped for the generation it ended. A Start
// that has begun a newer one since owns the state, and the late write must be
// dropped rather than land on top of it.
func TestAStopForAnOlderGenerationDoesNotOverwriteTheState(t *testing.T) {
	p := New(discardLog(), Spec{Name: "gen"})
	p.mu.Lock()
	old := p.gen
	p.gen++ // what Start does when it launches the next loop
	p.state = StateRunning
	p.mu.Unlock()

	p.setStateFor(old, StateStopped, "")
	if st := p.Status().State; st != StateRunning {
		t.Errorf("a stop for generation %d overwrote generation %d's state: got %q", old, old+1, st)
	}
	p.setStateFor(old+1, StateStopped, "")
	if st := p.Status().State; st != StateStopped {
		t.Errorf("a stop for the current generation was dropped: got %q", st)
	}
}

// THE SAME GUARD, DRIVEN THROUGH stop() ITSELF.
//
// The test above proves setStateFor drops a stale write; this one proves stop()
// hands it the generation it ended rather than whatever is current when it
// finally writes. The interleaving: a Stop is waiting on `done` with time to
// spare, a Start arrives meanwhile and is left pending on that same `done`, and
// when the loop ends both wake. If the pending Start's new loop reaches Running
// before stop() writes, that write must be dropped. stopWaited holds stop() in
// exactly that gap, so the order is forced rather than hoped for.
//
// Mutation: replace stop()'s `p.setStateFor(gen, StateStopped, "")` with
// `_ = gen; p.setState(StateStopped, "")` (gen must stay used, or the mutant
// does not build and proves nothing). This test fails with the process reading
// Stopped over a live child; the three tests above all still pass.
func TestAStopThatFinishesAfterAPendingStartFiredLeavesTheNewLoopRunning(t *testing.T) {
	sink := newGatedSink(deafReadyLine)
	rec := newRecorder()
	p := testProcess(t, fakeDeaf(30*time.Second), Spec{OnState: rec.onState, LogSink: sink})
	p.drain = 20 * time.Millisecond
	// The deaf child ignores SIGTERM, so the escalator's SIGKILL is what ends
	// it. Short, so the stop below is waiting on the held drain, not the grace.
	p.grace = 50 * time.Millisecond

	waited := make(chan struct{})
	proceed := make(chan struct{})
	var releaseStop sync.Once
	letStopFinish := func() { releaseStop.Do(func() { close(proceed) }) }
	var holdOnce sync.Once
	p.stopWaited = func() {
		// Only the stop under test is held; the cleanup Stop passes through.
		holdOnce.Do(func() {
			close(waited)
			<-proceed
		})
	}
	// Registered after testProcess, so these run before its cleanup Stop.
	t.Cleanup(letStopFinish)
	t.Cleanup(sink.release)

	p.Start()
	<-sink.held

	// Long enough that this stop ends on `done`, never on its deadline: the
	// point is the clean-completion path, which is the one that races a
	// pending Start.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopCancel()
	stopErr := make(chan error, 1)
	go func() { stopErr <- p.stop(stopCtx, false) }()

	waitFor(t, "the stop to let go of the loop", func() bool {
		p.runMu.Lock()
		defer p.runMu.Unlock()
		return !p.running
	})
	p.Start() // the old loop is still held, so this is left pending on its `done`
	p.runMu.Lock()
	pending := p.startPending
	p.runMu.Unlock()
	if !pending {
		t.Fatal("the Start during the stop was not left pending: this test's precondition is gone")
	}

	sink.release() // the old loop ends; stop() and the pending Start both wake
	select {
	case <-waited:
	case <-time.After(waitTimeout):
		t.Fatal("stop() never finished waiting on the loop it ended")
	}
	waitFor(t, "the pending start's child to come up while stop() is held", func() bool {
		return rec.distinctPIDs() == 2 && p.Status().State == StateRunning
	})

	letStopFinish()
	select {
	case err := <-stopErr:
		if err != nil {
			t.Fatalf("stop = %v, want nil: it ended on `done`, not its deadline", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("stop() did not return once released")
	}
	if st := p.Status().State; st != StateRunning {
		t.Errorf("state after the older stop finished = %q, want %q: it wrote Stopped over "+
			"the generation the pending Start began", st, StateRunning)
	}
}
