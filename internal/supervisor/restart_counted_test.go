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
