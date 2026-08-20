package supervisor

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A STOP THAT FINISHED ON THE DEADLINE IS A STOP THAT FINISHED.
//
// stop() waits on two channels: `done`, closed when the run loop has reaped the
// child and unwound, and ctx.Done(). Go chooses UNIFORMLY AT RANDOM among ready
// cases, so when both are ready the outer select is a coin flip -- and losing it
// reported ErrStopDeadline for a stop that had already completed, and issued a
// SIGKILL at a pid that had been reaped, which is a signal to whoever holds that
// number next.
//
// Both become ready together whenever this goroutine is descheduled across the
// boundary, which is exactly what a loaded CI runner does. It presented as a
// one-off flake in TestTheGraceEscalatorStopsWaitingOnceTheChildIsReaped: Stop
// returned the deadline error at 10.01s on a child that honours SIGTERM and dies
// in about nine milliseconds. It never reproduced by repetition -- reproducing
// it needs the deschedule to land inside one select -- so it is provoked here
// instead.
//
// THE ITERATIONS ARE THE TEST. One pass proves nothing against a coin: the
// unfixed code returns the right answer half the time. 64 rounds put the chance
// of the bug hiding at 2^-64, and cost about half a second -- the rounds spawn
// real children rather than a mocked channel, deliberately, because the thing
// under test is the interaction between the run loop's unwind and the deadline
// and a mock would assert the shape I already believe.
//
// Which round a reverted fix fails on is NOT a property of anything: at p=0.5
// the expected failure is round 2 and any early round is unremarkable. The
// guarantee is the aggregate, not the anecdote.
func TestAStopThatCompletesOnTheDeadlineIsNotReportedAsATimeout(t *testing.T) {
	const rounds = 64

	for i := 0; i < rounds; i++ {
		p := testProcess(t, fakeExit(0), Spec{})
		p.Start()

		// WAIT FOR `done` ITSELF, not for a state that merely suggests it.
		//
		// The first version of this test waited for StateStopped and assumed the
		// run loop had unwound. It had not: the state moves before supervise
		// returns, so `done` was still open, the deadline arm was taken for the
		// ordinary reason, and the test failed against the fixed code. The
		// precondition of the race is that `done` is ALREADY CLOSED when Stop is
		// called, so that is what has to be waited for.
		p.runMu.Lock()
		done := p.done
		p.runMu.Unlock()
		waitFor(t, "the run loop to unwind and close done", func() bool {
			select {
			case <-done:
				return true
			default:
				return false
			}
		})

		// An ALREADY-EXPIRED context, so ctx.Done() is ready too. Now both arms
		// of the select can proceed and the choice between them is random.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if err := p.Stop(ctx); err != nil {
			if errors.Is(err, ErrStopDeadline) {
				t.Fatalf("round %d: Stop reported a deadline on a child that had ALREADY "+
					"been reaped. `done` and ctx.Done() were both ready and the select "+
					"chose the deadline arm; completion has to win that tie, or a clean "+
					"stop is reported as a failure and a reaped pid is sent SIGKILL", i)
			}
			t.Fatalf("round %d: Stop returned %v, want nil", i, err)
		}
	}
}

// The deadline arm still has to work when the stop genuinely did not finish,
// which is the property the fix could plausibly have broken: a non-blocking
// re-check that somehow swallowed a real timeout would make Stop silently
// unbounded, which is worse than the flake it fixes.
func TestARealTimeoutStillReportsTheDeadline(t *testing.T) {
	p := testProcess(t, fakeDeaf(30*time.Second), Spec{})
	p.grace = time.Hour // keep the escalator out of it; this is stop()'s own arm
	p.Start()
	waitForDeaf(t, p)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := p.Stop(ctx)
	if !errors.Is(err, ErrStopDeadline) {
		t.Fatalf("Stop on a child that ignores SIGTERM returned %v, want ErrStopDeadline: "+
			"the tie-break added for the boundary case must not swallow a real timeout", err)
	}
}
