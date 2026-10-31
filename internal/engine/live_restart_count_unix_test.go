//go:build !windows

package engine

import (
	"testing"
	"time"
)

// A LEVEL THE RUNNING CHILD TAKES IN PLACE IS NOT A RESTART. Counting it would
// make every fader move on the mixer read as an output dropping, and six of them
// in a quarter of an hour would light the restart-storm key on a destination
// that never lost a frame. Unix only, like live_reconcile_unix_test.go: the
// in-place path needs the command channel Windows does not have.
//
// MUTATION: count a restart on the liveTry success branch of stopDestinations.
// Observed: FAIL, "restarts = 1 after an in-place level change, want 0".
func TestALevelChangeAppliedInPlaceIsNotCountedAsARestart(t *testing.T) {
	e, store := storeEngine(t)
	row := liveRow(t, store, false)
	before := planFor(t, e, row)

	proc := replyChild(t, "Command reply for stream 0: ret:0 res:")
	t.Cleanup(func() { _ = proc.Stop(t.Context()) })
	liveRunningDest(e, row, before, proc)
	pid := proc.Status().PID

	muted := *row
	muted.Profile = mixProfile(0)
	muted.Profile.Tracks[0].Muted = true
	e.stopDestinations(map[int64]destPlan{row.ID: planFor(t, e, &muted)})
	e.startDestinations(map[int64]destPlan{row.ID: planFor(t, e, &muted)})
	if applied, reason, _ := e.DestinationLive(row.ID); applied != liveApplied {
		t.Fatalf("setup: applied = %q (%s), want live", applied, reason)
	}

	time.Sleep(300 * time.Millisecond)
	e.mu.RLock()
	cur := e.dests[row.ID]
	e.mu.RUnlock()
	if cur == nil || cur.proc != proc {
		t.Fatal("setup: the destination was replaced; an in-place change keeps its process")
	}
	st := proc.Status()
	if st.PID != pid {
		t.Errorf("pid %d -> %d: the child was respawned by an in-place change", pid, st.PID)
	}
	if st.Restarts != 0 {
		t.Errorf("restarts = %d after an in-place level change, want 0", st.Restarts)
	}
	if st.RecentRestarts != 0 {
		t.Errorf("recent restarts = %d after an in-place level change, want 0", st.RecentRestarts)
	}
	if ds := destStatusOf(t, e, row.ID); ds.RestartStorm != 0 {
		t.Errorf("restartStorm = %d after an in-place level change, want 0", ds.RestartStorm)
	}
}
