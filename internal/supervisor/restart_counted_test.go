package supervisor

import (
	"testing"
	"time"
)

// RestartCounted counts a cycle of a SUPERVISED child, once. A process that was
// never started, or has been retired by Stop, has nothing to restart, and
// counting the call would put a restart on a card whose output never ran.
//
// MUTATION: drop the wasRunning guard in RestartCounted. Observed: FAIL,
// "a never-started process counted a restart".
func TestRestartCountedCountsOnlyASupervisedChildAndOnlyOnce(t *testing.T) {
	idle := testProcess(t, fakeSleep(time.Minute), Spec{AutoRestart: true})
	// The Start inside it brings the never-started process up, and that first
	// spawn is a start, not a restart.
	idle.RestartCounted(t.Context())
	if got := idle.Status().Restarts; got != 0 {
		t.Fatalf("restarts = %d: a never-started process counted a restart", got)
	}
	_ = idle.Stop(t.Context())
	idle.RestartCounted(t.Context())
	if got := idle.Status().Restarts; got != 0 {
		t.Fatalf("restarts = %d: a retired process counted a restart", got)
	}

	p := testProcess(t, fakeSleep(time.Minute), Spec{AutoRestart: true})
	p.Start()
	waitFor(t, "the child to run", func() bool { return p.Status().State == StateRunning })
	p.RestartCounted(t.Context())
	waitFor(t, "the child to run again", func() bool { return p.Status().State == StateRunning })
	time.Sleep(200 * time.Millisecond)
	if st := p.Status(); st.Restarts != 1 || st.RecentRestarts != 1 {
		t.Fatalf("restarts = %d (recent %d) after one counted restart, want 1", st.Restarts, st.RecentRestarts)
	}
}

// InheritRestarts continues a predecessor's count and storm window, plus the
// one restart that replaced it; a nil or self predecessor changes nothing.
func TestInheritRestartsContinuesThePredecessorsCountPlusOne(t *testing.T) {
	prev := testProcess(t, fakeSleep(time.Minute), Spec{})
	prev.mu.Lock()
	prev.restarts = 3
	now := time.Now()
	prev.restartAt = []time.Time{now.Add(-time.Hour), now.Add(-time.Minute), now.Add(-time.Second)}
	prev.mu.Unlock()

	p := testProcess(t, fakeSleep(time.Minute), Spec{})
	p.InheritRestarts(nil)
	p.InheritRestarts(p)
	if got := p.Status().Restarts; got != 0 {
		t.Fatalf("restarts = %d after a nil and a self inherit, want 0", got)
	}
	p.InheritRestarts(prev)
	st := p.Status()
	if st.Restarts != 4 {
		t.Errorf("restarts = %d, want the predecessor's 3 plus the replacement", st.Restarts)
	}
	// The hour-old respawn is outside the window and is pruned; the other two
	// and the replacement are inside it.
	if st.RecentRestarts != 3 {
		t.Errorf("recent restarts = %d, want 3", st.RecentRestarts)
	}
}

// THE CAUSE OF THE LAST COUNTED RESTART TRAVELS WITH THE COUNT.
//
// A live-level restart and a crash respawn both move Status.Restarts, and the
// destination.restarted event read the same sentence for both: "the process
// exited and was started again", which is false for a restart the engine made
// to apply a level. The status now says which kind the latest restart was.
//
// Mutation: drop the RestartCauseLevel assignment in RestartCounted. Observed:
// FAIL, the cause reads as an exit.
func TestRestartCountedRecordsThatTheRestartWasALiveLevelChange(t *testing.T) {
	p := testProcess(t, fakeSleep(time.Minute), Spec{AutoRestart: true})
	p.Start()
	waitFor(t, "the child to run", func() bool { return p.Status().State == StateRunning })
	p.RestartCounted(t.Context())
	waitFor(t, "the child to run again", func() bool { return p.Status().State == StateRunning })
	if got := p.Status().RestartCause; got != RestartCauseLevel {
		t.Fatalf("restart cause = %q after a live-level restart, want %q", got, RestartCauseLevel)
	}
}

// A REFUSED LIVE LEVEL CHANGE IS A LEVEL RESTART TOO.
//
// The engine tears a destination down and starts its replacement with the
// level when the running child cannot take a change in place. That hand-off goes
// through InheritRestarts, not RestartCounted, so it needs its own cause: the
// replacement did not exit for it, and the event for it must not say it did.
//
// Mutation: set RestartCauseExit in InheritRestarts. Observed: FAIL, the cause
// reads as an exit.
func TestAnInheritedRestartIsALiveLevelChangeNotAnExit(t *testing.T) {
	prev := testProcess(t, fakeSleep(time.Minute), Spec{})
	p := testProcess(t, fakeSleep(time.Minute), Spec{})
	p.InheritRestarts(prev)
	if got := p.Status().RestartCause; got != RestartCauseLevel {
		t.Fatalf("restart cause = %q after an inherited live-level restart, want %q", got, RestartCauseLevel)
	}
}

// A CRASH RESPAWN IS AN EXIT, NOT A LEVEL CHANGE.
//
// The counterpart of the test above: the supervise loop's own respawn after a
// non-zero exit must keep saying so, or the event would blame a level change for
// every crash.
//
// Mutation: set RestartCauseLevel in the supervise loop's respawn. Observed: FAIL.
func TestACrashRespawnIsReasonedAsAnExitNotALevelChange(t *testing.T) {
	p := testProcess(t, fakeExit(1), Spec{
		AutoRestart: true,
		MinBackoff:  10 * time.Millisecond,
		MaxBackoff:  10 * time.Millisecond,
	})
	p.Start()
	waitFor(t, "one crash respawn", func() bool { return p.Status().Restarts >= 1 })
	if got := p.Status().RestartCause; got != RestartCauseExit {
		t.Fatalf("restart cause = %q after a crash respawn, want %q", got, RestartCauseExit)
	}
}
