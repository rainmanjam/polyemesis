package supervisor

import (
	"context"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/procname"
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

// One wake can be lost -- a dropped loopback datagram -- so it repeats until the
// child goes or the grace does. The grace stays the backstop.
func TestTheWakeRepeatsUntilTheGraceAndTheGraceStillKills(t *testing.T) {
	skipWithoutSIGTERM(t)
	const grace = 1600 * time.Millisecond // wakes at 0.75s and 1.25s

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

// The first wake is the only one with a real packet to release, so it must not
// arrive before FFmpeg has registered the SIGTERM. On Linux that happens at the
// next -stats_period tick, up to ffmpegStatsPeriod after the signal (see
// wakeAfter). A wakeAfter at or under that period passes on macOS, where the
// signal is noticed at once, and fails about half the time on Linux: the
// recorder is SIGKILLed with its file unfinalised.
func TestTheFirstWakeWaitsOutFFmpegsStatsPeriod(t *testing.T) {
	if wakeAfter < ffmpegStatsPeriod+200*time.Millisecond {
		t.Errorf("wakeAfter is %v; FFmpeg on Linux can take its whole %v stats period to act "+
			"on SIGTERM, and a wake before then is spent on a child that has not yet stopped",
			wakeAfter, ffmpegStatsPeriod)
	}
	// The recorder's grace -- the case this exists for -- still has room for
	// the first wake and a repeat.
	if g := graceFor("recorder"); wakeAfter+wakeEvery >= g {
		t.Errorf("wakeAfter %v + wakeEvery %v leaves no repeat inside the %v recorder grace",
			wakeAfter, wakeEvery, g)
	}
}

// WakeAfter and WakeEvery are the exported views of the schedule the supervisor
// runs; the engine derives its seam cap from them, so they must not drift from
// the values the wake loop uses.
func TestExportedWakeScheduleMatchesTheSupervisors(t *testing.T) {
	if WakeAfter != wakeAfter || WakeEvery != wakeEvery {
		t.Fatalf("exported schedule (%v, %v) differs from the supervisor's (%v, %v)",
			WakeAfter, WakeEvery, wakeAfter, wakeEvery)
	}
	if WakeAfter <= 0 || WakeEvery <= 0 {
		t.Fatalf("wake schedule must be positive, got %v, %v", WakeAfter, WakeEvery)
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// A loudness monitor stopped on a quiet feed is woken at wakeAfter and needs a
// moment to act on the packet. Its grace used to be fastGrace (1s), which left
// 250ms after the first wake and SIGKILLed it, with a WARN, on a healthy stop.
// The child here answers 400ms after the wake: slower than that window, well
// inside any sane one.
func TestAWokenOutputlessChildIsNotKilledOnAHealthyQuietStop(t *testing.T) {
	skipWithoutSIGTERM(t)
	var logs syncBuf
	var pid atomic.Int64
	var woken atomic.Bool
	spec := Spec{
		Name: "loudness:4", Kind: procname.KindLoudness,
		WakeOnStop: func() {
			if woken.Swap(true) {
				return
			}
			go func() {
				time.Sleep(400 * time.Millisecond)
				if proc, err := os.FindProcess(int(pid.Load())); err == nil {
					_ = proc.Kill()
				}
			}()
		},
	}
	f := fakeDeaf(60 * time.Second)
	spec.Bin, spec.Args = f.bin, f.args
	p := New(slog.New(slog.NewTextHandler(&logs, nil)), spec)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = p.Stop(ctx)
	})
	p.Start()
	waitFor(t, "child to start", func() bool { return p.Status().State == StateRunning })
	waitForDeaf(t, p)
	pid.Store(int64(p.Status().PID))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop returned %v", err)
	}
	p.escalators.Wait()
	if strings.Contains(logs.String(), graceEscalationLogLine) {
		t.Errorf("a healthy stop was SIGKILLed after the grace (%v) cut across the wake schedule:\n%s",
			p.grace, logs.String())
	}
}

// Whatever kind a child is, a grace shorter than the wake schedule plus a
// post-wake window would kill it before it could act on its wake.
func TestEveryGraceLeavesRoomForTheWakeWhenThereIsOne(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, kind := range []procname.Kind{procname.KindMeters, procname.KindLoudness, procname.KindSilence} {
		p := New(quiet, Spec{Name: string(kind), Kind: kind, Bin: "/bin/true", WakeOnStop: func() {}})
		if min := wakeAfter + wakeEvery + postWakeGrace; p.grace < min {
			t.Errorf("%s: grace %v leaves less than %v after the repeat wake", kind, p.grace, postWakeGrace)
		}
		bare := New(quiet, Spec{Name: string(kind), Kind: kind, Bin: "/bin/true"})
		if bare.grace != fastGrace {
			t.Errorf("%s without a wake: grace %v, want %v", kind, bare.grace, fastGrace)
		}
	}
}
