package supervisor

import (
	"context"
	"path/filepath"
	"strings"
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

func TestACensusEntryReadsAsSomethingAnOperatorCanActotOn(t *testing.T) {
	// The String is what lands in a report, and a report that omits the pid
	// tells an operator there is a problem without telling them how to find it
	// -- which is where #631 started, with somebody reading `ps` output by eye.
	c := Child{PID: 5216, Name: "meters", Kind: "meters", Since: time.Now().Add(-90 * time.Second)}
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

func TestTheCensusRefusesAPIDThatIsNotOne(t *testing.T) {
	// cmd.Process.Pid is only meaningful after a successful Start. A zero here
	// would be a permanent entry for a child that never existed, and a census
	// with a phantom in it is one nobody trusts the rest of.
	before := LiveCount()
	enrol(0, "ghost", "ghost")
	enrol(-1, "ghost", "ghost")
	if LiveCount() != before {
		t.Fatalf("a non-pid was enrolled: count went %d -> %d", before, LiveCount())
	}
	discharge(0)
	discharge(-1)
	if LiveCount() != before {
		t.Fatalf("discharging a non-pid disturbed the census: %d -> %d", before, LiveCount())
	}
}

func TestTheOldestSurvivorIsReportedFirst(t *testing.T) {
	// A report leads with the child that has been wrong for longest, because
	// that is the one whose cause is furthest back and least likely to be the
	// thing the operator is currently looking at.
	before := len(Live())
	now := time.Now()
	census.mu.Lock()
	if census.live == nil {
		census.live = map[int]Child{}
	}
	census.live[900001] = Child{PID: 900001, Name: "newer", Since: now}
	census.live[900002] = Child{PID: 900002, Name: "older", Since: now.Add(-time.Hour)}
	census.mu.Unlock()
	t.Cleanup(func() { discharge(900001); discharge(900002) })

	got := Live()
	if len(got) != before+2 {
		t.Fatalf("expected %d entries, got %d", before+2, len(got))
	}
	var names []string
	for _, c := range got {
		if c.PID == 900001 || c.PID == 900002 {
			names = append(names, c.Name)
		}
	}
	if len(names) != 2 || names[0] != "older" {
		t.Fatalf("Live() ordered the survivors %v; oldest must come first", names)
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
	before := LiveCount()
	p := testProcess(t, fake{bin: filepath.Join(t.TempDir(), "no-such-binary")},
		Spec{Name: "ghost", Kind: "ghost"})
	p.Start()

	// Asserted as "never rises" rather than "is right once", because the failure
	// this guards against is a transient entry that a single later look would
	// miss -- and the shutdown report reads the census at one arbitrary moment.
	// AutoRestart is off in this Spec, so there is no second attempt to race.
	deadline := time.Now().Add(750 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := LiveCount(); got > before {
			for _, c := range Live() {
				t.Logf("  census entry: %s", c)
			}
			t.Fatalf("a spawn that never happened put the census at %d, was %d", got, before)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
