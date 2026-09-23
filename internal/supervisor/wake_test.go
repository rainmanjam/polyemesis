package supervisor

import (
	"context"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// Spec.WakeOnStop is what lets a relay consumer stopped on a quiet feed finish
// cleanly instead of being SIGKILLed at the end of the grace. The supervisor's
// half is timing: call it after a child has had a fair chance to answer SIGTERM
// alone, keep calling it until the child goes, and never call it for a child
// that went on its own. The relay's half is relay.Hub.Wake; the two meet FFmpeg
// in engine.TestARecorderStoppedOnAQuietFeedFinalisesItsFileInsteadOfBeingKilled.

func skipWithoutSIGTERM(t *testing.T) {
	t.Helper()
	// Process.Signal cannot deliver SIGTERM on Windows, so every stop there is
	// the escalation and the wake window never opens.
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM is not deliverable on Windows")
	}
}

// A child that does not answer SIGTERM until it is woken exits at the wake, not
// at the end of the grace.
func TestAStoppingChildThatNeedsWakingIsWokenInsteadOfWaitingOutTheGrace(t *testing.T) {
	skipWithoutSIGTERM(t)
	const grace = 5 * time.Second

	var calls atomic.Int32
	var pid atomic.Int64
	p := testProcess(t, fakeDeaf(60*time.Second), Spec{
		// Stands in for a quiet relay being woken: the child's only way out is
		// what the wake does to it.
		WakeOnStop: func() {
			if calls.Add(1) == 1 {
				if proc, err := os.FindProcess(int(pid.Load())); err == nil {
					_ = proc.Kill()
				}
			}
		},
	})
	p.grace = grace
	p.Start()
	waitFor(t, "child to start", func() bool { return p.Status().State == StateRunning })
	waitForDeaf(t, p)
	pid.Store(int64(p.Status().PID))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	err := p.Stop(ctx)
	took := time.Since(started)

	if err != nil {
		t.Fatalf("Stop returned %v", err)
	}
	if calls.Load() == 0 {
		t.Fatalf("WakeOnStop was never called; the stop took %v", took)
	}
	if took < wakeAfter {
		t.Errorf("the child went after %v, before the %v it is owed to answer SIGTERM alone", took, wakeAfter)
	}
	// Two seconds is far above wakeAfter plus a reap, and far below the grace,
	// so only the wake can have ended it inside the bound.
	if took > 2*time.Second {
		t.Errorf("stop took %v: the child was not woken, it waited for the %v grace", took, grace)
	}
}

// One wake can be lost -- a dropped loopback datagram, or a wake that lands
// before the child registered the signal -- so it repeats until the child goes
// or the grace does. The grace stays the backstop.
func TestTheWakeRepeatsUntilTheGraceAndTheGraceStillKills(t *testing.T) {
	skipWithoutSIGTERM(t)
	const grace = 1400 * time.Millisecond // wakes at 0.3s, 0.8s and 1.3s

	var calls atomic.Int32
	p := testProcess(t, fakeDeaf(60*time.Second), Spec{WakeOnStop: func() { calls.Add(1) }})
	p.grace = grace
	p.Start()
	waitFor(t, "child to start", func() bool { return p.Status().State == StateRunning })
	waitForDeaf(t, p)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop returned %v: a child the wake cannot reach must still be killed at the grace", err)
	}
	if n := calls.Load(); n < 2 {
		t.Errorf("WakeOnStop was called %d times inside a %v grace, want it repeated", n, grace)
	}
}

// The ordinary stop -- input still flowing, FFmpeg out in a tenth of a second --
// never reaches the wake.
func TestAChildThatAnswersSIGTERMIsNeverWoken(t *testing.T) {
	skipWithoutSIGTERM(t)
	var calls atomic.Int32
	p := testProcess(t, fakeSleep(30*time.Second), Spec{WakeOnStop: func() { calls.Add(1) }})
	p.Start()
	waitFor(t, "child to start", func() bool { return p.Status().State == StateRunning })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	p.escalators.Wait()
	if n := calls.Load(); n != 0 {
		t.Errorf("WakeOnStop was called %d times for a child that exited on SIGTERM", n)
	}
}
