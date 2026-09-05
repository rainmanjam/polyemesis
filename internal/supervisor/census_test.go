package supervisor

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/childcensus"
)

/* The census exists because #631 could not be answered from inside the program.
 *
 * An ffmpeg was found on the production host with its ppid pointing at the LIVE
 * polyemesis server, and no SIGKILL escalation anywhere in the log -- because
 * no escalation happened. Nothing held the handle to escalate with. The issue
 * records the consequence plainly: "nothing in polyemesis did it, and outside
 * systemd nothing would have."
 *
 * These tests pin the two properties that make the census worth trusting: it
 * counts what the OPERATING SYSTEM counts, and it counts REAPED rather than
 * SIGNALLED. A census that cleared its entry when a signal was sent would agree
 * with the log and disagree with `ps`, which is the exact disagreement that
 * hid this bug for three weeks.
 */

// liveByPID finds this test's own child rather than asserting on the global
// count. The census is package-level by design, and a sibling test's cleanup
// goroutine may still be reaping when this one starts.
func liveByPID(pid int) (childcensus.Child, bool) {
	for _, c := range childcensus.Live() {
		if c.PID == pid {
			return c, true
		}
	}
	return childcensus.Child{}, false
}

func TestASpawnedChildIsCountedAndAReapedOneIsNot(t *testing.T) {
	p := testProcess(t, fakeSleep(30*time.Second), Spec{Name: "meters", Kind: "meters"})
	p.Start()

	var pid int
	waitFor(t, "the child to be enrolled", func() bool {
		p.cmdMu.Lock()
		defer p.cmdMu.Unlock()
		if p.cmd == nil || p.cmd.Process == nil {
			return false
		}
		pid = p.cmd.Process.Pid
		_, ok := liveByPID(pid)
		return ok
	})

	got, _ := liveByPID(pid)
	if got.Name != "meters" || got.Kind != "meters" {
		t.Fatalf("census entry is %+v; a report that cannot name the child sends an "+
			"operator to `ps`, which is where this started", got)
	}
	if got.Since.IsZero() {
		t.Fatal("no spawn time recorded, so a report cannot say how long the child has " +
			"outlived whatever should have reaped it")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop on a child that honours SIGTERM: %v", err)
	}
	if c, ok := liveByPID(pid); ok {
		t.Fatalf("the child was reaped and the census still lists it: %s", c)
	}
}

func TestTheCensusCountsReapedRatherThanSignalled(t *testing.T) {
	// THE PROPERTY #631 NEEDED. A deaf child is one that does not act on
	// SIGTERM -- which is what an FFmpeg blocked in a timeout-less read on a
	// source that has gone quiet is, and the reconcile paths stop the feed
	// BEFORE they signal the child. Stop gives up on its deadline, issues
	// SIGKILL and returns WITHOUT waiting, because the deadline is already
	// spent.
	//
	// So there is a window where the log says the stop is done and the process
	// is still there. A census that cleared on the signal would agree with the
	// log and disagree with the host, which is precisely the disagreement that
	// let 53 escalations accumulate over three weeks without anything noticing.
	p := testProcess(t, fakeDeaf(30*time.Second), Spec{Name: "dest:studio-a", Kind: "destination"})
	p.Start()
	// Before signalling anything. StateRunning is set the instant cmd.Start()
	// returns, which is before the child has run its first instruction -- so a
	// stop issued on the strength of that lands on a child whose handler is not
	// installed yet, and the "deaf" fake dies obediently. That is exactly what
	// the first version of this test did, and it failed reporting a clean stop
	// of a child that is supposed to ignore SIGTERM.
	waitForDeaf(t, p)

	var pid int
	waitFor(t, "the deaf child to be enrolled", func() bool {
		p.cmdMu.Lock()
		defer p.cmdMu.Unlock()
		if p.cmd == nil || p.cmd.Process == nil {
			return false
		}
		pid = p.cmd.Process.Pid
		_, ok := liveByPID(pid)
		return ok
	})

	// A deadline short enough that the deaf child cannot have died of its own
	// accord, so what is asserted below is the escalation path and not a race
	// with a cooperative exit.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err := p.Stop(ctx)
	if err == nil {
		t.Fatal("Stop reported a clean stop of a child that ignores SIGTERM; this test " +
			"can say nothing about the census if the escalation never happened")
	}

	// And then it does go, because SIGKILL was issued -- the census clears
	// itself off the reap, with nobody having to tell it. If this never
	// happened the entry would be a permanent false positive, which is its own
	// way of being useless.
	waitFor(t, "the killed child to be discharged", func() bool {
		_, ok := liveByPID(pid)
		return !ok
	})
}

func TestTheCensusIsNotVacuous(t *testing.T) {
	// A census that never enrolled anything would pass both tests above by
	// reporting nothing at every point they look. This one asserts the
	// positive: with a child up, childcensus.Live() is non-empty and childcensus.LiveCount() agrees
	// with it.
	p := testProcess(t, fakeSleep(30*time.Second), Spec{Name: "ingest", Kind: "ingest"})
	p.Start()
	waitFor(t, "a non-empty census", func() bool { return childcensus.LiveCount() > 0 })
	if n, l := childcensus.LiveCount(), len(childcensus.Live()); n != l {
		t.Fatalf("childcensus.LiveCount()=%d but len(childcensus.Live())=%d; the cheap path and the reporting "+
			"path disagree, so one of them is lying to somebody", n, l)
	}
}

func TestACensusEntryReadsAsSomethingAnOperatorCanActotOn(t *testing.T) {
	// The String is what lands in a report, and a report that omits the pid
	// tells an operator there is a problem without telling them how to find it
	// -- which is where #631 started, with somebody reading `ps` output by eye.
	c := childcensus.Child{PID: 5216, Name: "meters", Kind: "meters", Since: time.Now().Add(-90 * time.Second)}
	got := c.String()
	for _, want := range []string{"5216", "meters"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q does not mention %q", got, want)
		}
	}
	if !strings.Contains(got, "up 1m30") {
		t.Errorf("%q does not say how long the child has been running; "+
			"\"still there\" and \"still there since before the last deploy\" are "+
			"different findings", got)
	}
}

func TestASpawnThatFailedEnrolsNothing(t *testing.T) {
	// The one way this census could report a child that never existed, and the
	// reason enrol sits inside `if startErr == nil` rather than after it.
	//
	// It matters more than a stray map entry: cmd/polyemesis prints the census
	// at the end of every shutdown, so a phantom here is a warning line naming
	// a pid that was never a process -- on the very report whose job is to be
	// believed the one time it fires.
	before := childcensus.LiveCount()
	p := testProcess(t, fake{bin: filepath.Join(t.TempDir(), "no-such-binary")},
		Spec{Name: "ghost", Kind: "ghost"})
	p.Start()

	// Asserted as "never rises" rather than "is right once", because the failure
	// this guards against is a transient entry that a single later look would
	// miss -- and the shutdown report reads the census at one arbitrary moment.
	// AutoRestart is off in this Spec, so there is no second attempt to race.
	deadline := time.Now().Add(750 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := childcensus.LiveCount(); got > before {
			for _, c := range childcensus.Live() {
				t.Logf("  census entry: %s", c)
			}
			t.Fatalf("a spawn that never happened put the census at %d, was %d", got, before)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// kill() must not signal a reaped pid. #720.
//
// killGroup issues a raw syscall.Kill(-pid, SIGKILL), which names a process
// GROUP by number and bypasses Go's ErrProcessDone -- so on a reaped pid it can
// signal a group this supervisor never started. Its two sibling signal sites
// each carry a guard for this; kill() rested on an ordering argument written as
// a comment across three functions.
//
// TESTED AGAINST THE GUARD DIRECTLY rather than through the supervisor, because
// the supervisor clears p.exited during teardown -- so waiting for a real reap
// races the very field the guard reads, and the test would be measuring the
// teardown rather than the guard. The escalation path on a LIVE child is
// covered by the stop/kill tests next door; what is missing there, and pinned
// here, is the reaped one.
func TestKillIsARefusalOnAReapedChild(t *testing.T) {
	p := New(slog.New(slog.NewTextHandler(io.Discard, nil)), Spec{Name: "guard", Kind: "test"})

	// A real child, run to completion and reaped, so its pid is a number the
	// operating system may well have handed to somebody else by now.
	f := fakeExit(0)
	cmd := exec.Command(f.bin, f.args...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = cmd.Wait()

	exited := make(chan struct{})
	close(exited) // what runOnce does the instant cmd.Wait() returns

	p.cmdMu.Lock()
	p.cmd, p.exited = cmd, exited
	p.cmdMu.Unlock()

	// The guard's job: return without reaching killGroup. There is no assertion
	// available on "no signal was sent" -- the syscall either happened or it did
	// not -- so what is pinned is that a reaped child with a live cmd handle
	// takes the early return rather than the signal.
	done := make(chan struct{})
	go func() { p.kill(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("kill() blocked on a reaped child")
	}

	// The other early return: no cmd at all.
	p.cmdMu.Lock()
	p.cmd = nil
	p.cmdMu.Unlock()
	p.kill()
}
