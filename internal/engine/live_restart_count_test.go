package engine

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// A RESTART THE LIVE-GAIN PATH CAUSES IS A RESTART, AND IT IS COUNTED.
//
// The destination card's restart count, polyemesis_destination_restarts_total,
// the destination.restarted event and the Stream Deck's restart-storm key all
// read one thing: supervisor.Status.Restarts and its storm-window companion
// RecentRestarts. Both used to move in exactly one place -- the supervise loop's
// retry after a child exited on its own -- so the two restarts the live-gain
// path performs were invisible to every one of them:
//
//   - A FORMAT EVENT (a failover to a source at another sample rate, or the
//     ingest's next session) respawns a destination whose live level the
//     reinit would revert. That goes through Process.Restart, whose stop
//     cancels the loop before it reaches the counter.
//   - A LEVEL CHANGE THE RUNNING CHILD REFUSES -- every level change on
//     Windows, where there is no command channel -- tears the destination
//     down and the next start builds a brand-new Process, whose count begins
//     at zero again.
//
// So an output cycling on every failover read "0 restarts" and could never
// trip the storm signal. These tests pin that both paths now count, once each,
// and that a level the running child takes in place does not count at all.

// waitLevelsCurrent waits until proc has been respawned from its holder's
// graph and is running again.
func waitLevelsCurrent(t *testing.T, proc *supervisor.Process, holder *graphHolder, why string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for levelsStale(proc, holder) || proc.Status().State != supervisor.StateRunning {
		if time.Now().After(deadline) {
			t.Fatalf("%s: the process was not respawned from its holder; argv is still\n%s",
				why, strings.Join(proc.Args(), " "))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// destStatusOf is the destination's entry in the status the dashboard reads.
func destStatusOf(t *testing.T, e *Engine, id int64) DestStatus {
	t.Helper()
	for _, ds := range e.Status().Destinations {
		if ds.ID == id {
			return ds
		}
	}
	t.Fatalf("destination %d is missing from the status", id)
	return DestStatus{}
}

// MUTATION: in respawnStaleLevels, call p.Restart instead of p.RestartCounted.
// Observed: FAIL, "restarts = 0 after a format-event respawn, want 1".
func TestAFormatEventRespawnForALiveLevelCountsAsOneRestart(t *testing.T) {
	e, store := storeEngine(t)
	proc, holder := stagedLiveMute(t, e, store)
	if got := proc.Status().Restarts; got != 0 {
		t.Fatalf("setup: restarts = %d before anything restarted", got)
	}

	e.respawnStaleLevels("test: the source feed changed")
	waitLevelsCurrent(t, proc, holder, "a format event")

	// EXACTLY ONCE. The stop inside the restart cancels the supervise loop, and
	// a loop that counted its own cancelled exit as well would read 2 here a
	// moment later, so the read waits past the point where it would land.
	time.Sleep(300 * time.Millisecond)
	st := proc.Status()
	if st.Restarts != 1 {
		t.Errorf("restarts = %d after a format-event respawn, want 1", st.Restarts)
	}
	if st.RecentRestarts != 1 {
		t.Errorf("recent restarts = %d after a format-event respawn, want 1; the storm window never saw it", st.RecentRestarts)
	}
}

// THE STORM SIGNAL SEES THEM. A destination cycled on every failover is the
// restart storm the Stream Deck key (DestStatus.RestartStorm) and the
// PolyemesisDestinationRestartStorm alert exist to surface; before this, a
// destination respawned for its live level a hundred times read 0.
//
// MUTATION: in RestartCounted, drop noteRestartLocked. Observed: FAIL,
// "restartStorm = 0 after 6 respawns".
func TestRepeatedLiveLevelRespawnsTripTheRestartStormSignal(t *testing.T) {
	e, store := storeEngine(t)
	proc, holder := stagedLiveMute(t, e, store)

	id := proc2id(t, e, proc)
	e.mu.RLock()
	row := e.dests[id].row
	e.mu.RUnlock()
	unmuted := planFor(t, e, row).compiled
	mutedRow := *row
	mutedRow.Profile = mixProfile(0)
	mutedRow.Profile.Tracks[0].Muted = true
	muted := planFor(t, e, &mutedRow).compiled

	const respawns = supervisor.RestartStormAbove + 1
	for i := range respawns {
		// Each pass leaves the holder disagreeing with the running argv, the
		// state a live mute or unmute leaves behind, and then raises the event.
		if i%2 == 1 {
			holder.set(unmuted)
		} else if i > 0 {
			holder.set(muted)
		}
		if !levelsStale(proc, holder) {
			t.Fatalf("pass %d: setup did not leave the level stale", i)
		}
		e.respawnStaleLevels("test: failover")
		waitLevelsCurrent(t, proc, holder, "a repeated format event")
		// respawnStaleLevels skips a process it is still cycling, and the
		// goroutine clears that mark only after Restart returns.
		deadline := time.Now().Add(5 * time.Second)
		for {
			e.levelRespawnMu.Lock()
			busy := e.levelRespawning[proc]
			e.levelRespawnMu.Unlock()
			if !busy {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the respawn goroutine never finished")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	ds := destStatusOf(t, e, proc2id(t, e, proc))
	if ds.Process == nil || ds.Process.Restarts != respawns {
		t.Fatalf("status restarts = %+v, want %d", ds.Process, respawns)
	}
	if ds.RestartStorm != respawns {
		t.Errorf("restartStorm = %d after %d respawns, want %d", ds.RestartStorm, respawns, respawns)
	}
}

// proc2id finds the destination id running proc.
func proc2id(t *testing.T, e *Engine, proc *supervisor.Process) int64 {
	t.Helper()
	e.mu.RLock()
	defer e.mu.RUnlock()
	for id, d := range e.dests {
		if d.proc == proc {
			return id
		}
	}
	t.Fatal("no destination runs this process")
	return 0
}

// standInFFmpeg makes the engine's own destination spawns a stand-in that stays
// up: the test binary, kept alive by TestMain's astats branch, which is keyed on
// the environment because the engine builds the whole argv. A stand-in that
// exited would add its own crash-respawn to the count and blur the one the test
// is about.
func standInFFmpeg(t *testing.T, e *Engine) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary to re-execute as a child: %v", err)
	}
	t.Setenv(engineFakeChildAstats, "1")
	e.tools.FFmpeg = self
}

// The refused-level fallback replaces the Process outright. The replacement
// starts from the count its predecessor had, plus the restart that replaced it.
//
// MUTATION: delete the InheritRestarts call in startDest. Observed: FAIL,
// "restarts = 0 after a refused level change restarted it, want 1".
func TestARefusedLevelChangeCountsTheRestartItFallsBackTo(t *testing.T) {
	e, store := storeEngine(t)
	row := liveRow(t, store, false)
	before := planFor(t, e, row)

	// The refusing child is spawned BEFORE the stand-in environment is set:
	// TestMain reads that variable first, and a child spawned under it would
	// answer nothing, so the fallback would come from a timeout, not a refusal.
	proc := replyChild(t, "Command reply for stream 0: ret:-78 res:")
	t.Cleanup(func() { _ = proc.Stop(t.Context()) })
	standInFFmpeg(t, e)
	liveRunningDest(e, row, before, proc)

	muted := *row
	muted.Profile = mixProfile(0)
	muted.Profile.Tracks[0].Muted = true
	after := planFor(t, e, &muted)
	e.stopDestinations(map[int64]destPlan{row.ID: after})
	if applied, _, _ := e.DestinationLive(row.ID); applied != liveRestart {
		t.Fatalf("setup: applied = %q, want the restart fallback", applied)
	}
	e.startDestinations(map[int64]destPlan{row.ID: after})

	e.mu.RLock()
	cur := e.dests[row.ID]
	e.mu.RUnlock()
	if cur == nil || cur.proc == nil || cur.proc == proc {
		t.Fatalf("setup: the fallback did not start a replacement process (%+v)", cur)
	}
	waitRunning(t, cur.proc)
	st := cur.proc.Status()
	if st.Restarts != 1 {
		t.Errorf("restarts = %d after a refused level change restarted it, want 1", st.Restarts)
	}
	if st.RecentRestarts != 1 {
		t.Errorf("recent restarts = %d after the fallback restart, want 1", st.RecentRestarts)
	}
	if ds := destStatusOf(t, e, row.ID); ds.Process == nil || ds.Process.Restarts != 1 {
		t.Errorf("the dashboard's status reads restarts %+v, want 1", ds.Process)
	}
}

// What is carried is only for the start that follows the fallback. A
// destination disabled in between and started fresh later is a new run, and it
// starts at zero like any other first start -- not with a count left behind by
// a teardown the operator then superseded.
//
// MUTATION: drop the clear of unconsumed carries in startDestinations.
// Observed: FAIL, "a fresh start inherited a superseded fallback".
func TestAFallbackCountIsNotInheritedByALaterFreshStart(t *testing.T) {
	e, store := storeEngine(t)
	row := liveRow(t, store, false)
	before := planFor(t, e, row)

	// The refusing child is spawned BEFORE the stand-in environment is set:
	// TestMain reads that variable first, and a child spawned under it would
	// answer nothing, so the fallback would come from a timeout, not a refusal.
	proc := replyChild(t, "Command reply for stream 0: ret:-78 res:")
	t.Cleanup(func() { _ = proc.Stop(t.Context()) })
	standInFFmpeg(t, e)
	liveRunningDest(e, row, before, proc)

	muted := *row
	muted.Profile = mixProfile(0)
	muted.Profile.Tracks[0].Muted = true
	after := planFor(t, e, &muted)
	e.stopDestinations(map[int64]destPlan{row.ID: after})
	// Disabled before the start that would have consumed it.
	e.startDestinations(map[int64]destPlan{})

	e.startDestinations(map[int64]destPlan{row.ID: after})
	e.mu.RLock()
	cur := e.dests[row.ID]
	e.mu.RUnlock()
	if cur == nil || cur.proc == nil {
		t.Fatal("setup: the destination did not start")
	}
	waitRunning(t, cur.proc)
	if got := cur.proc.Status().Restarts; got != 0 {
		t.Errorf("restarts = %d: a fresh start inherited a superseded fallback", got)
	}
}

// The redundant output follows the same rule as its primary. A refused level
// change on the backup replaces its process, and the replacement continues the
// count the card shows under the backup feed.
//
// MUTATION: delete the InheritRestarts call in reconcileBackup. Observed: FAIL,
// "backup restarts = 0 after a refused level change restarted it, want 1".
func TestARefusedLevelChangeOnTheBackupCountsTheRestartItFallsBackTo(t *testing.T) {
	e, store := storeEngine(t)
	levels := liveRow(t, store, false)
	before := planFor(t, e, levels)
	mutedRow := *levels
	mutedRow.Profile = mixProfile(0)
	mutedRow.Profile.Tracks[0].Muted = true
	after := planFor(t, e, &mutedRow)

	row := backupRow()
	row.ID = levels.ID
	backup := replyChild(t, "Command reply for stream 0: ret:-78 res:")
	t.Cleanup(func() { _ = backup.Stop(t.Context()) })
	standInFFmpeg(t, e) // after the refusing child; see the primary's test
	prev := &destination{
		row: row, hub: e.hub, backup: backup, backupGraph: newGraphHolder(before.compiled),
		backupSpec: backupSpecOf(row, after.compiled, "up"),
	}
	e.mu.Lock()
	e.dests[row.ID] = prev
	e.mu.Unlock()

	e.reconcileBackup(row.ID, prev, after.compiled, "up")

	e.mu.RLock()
	cur := e.dests[row.ID]
	e.mu.RUnlock()
	if cur == nil || cur.backup == nil || cur.backup == backup {
		t.Fatalf("setup: the refusal did not replace the backup (%+v)", cur)
	}
	t.Cleanup(func() { _ = cur.backup.Stop(t.Context()) })
	waitRunning(t, cur.backup)
	st := cur.backup.Status()
	if st.Restarts != 1 || st.RecentRestarts != 1 {
		t.Errorf("backup restarts = %d (recent %d) after a refused level change restarted it, want 1",
			st.Restarts, st.RecentRestarts)
	}
}
