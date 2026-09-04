package supervisor

import (
	"context"
	"testing"
	"time"
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
func liveByPID(pid int) (Child, bool) {
	for _, c := range Live() {
		if c.PID == pid {
			return c, true
		}
	}
	return Child{}, false
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
	// positive: with a child up, Live() is non-empty and LiveCount() agrees
	// with it.
	p := testProcess(t, fakeSleep(30*time.Second), Spec{Name: "ingest", Kind: "ingest"})
	p.Start()
	waitFor(t, "a non-empty census", func() bool { return LiveCount() > 0 })
	if n, l := LiveCount(), len(Live()); n != l {
		t.Fatalf("LiveCount()=%d but len(Live())=%d; the cheap path and the reporting "+
			"path disagree, so one of them is lying to somebody", n, l)
	}
}
