package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// AppliedSince answers a saved change by what the reconcile did to the process,
// never by what the change looked like. End to end through stopDestinations:
//
//   - E3 (2026-11-04 demo): music kept out of a mix the music track had already
//     left. The profile moved, the command line did not, and the process was
//     kept -- the console said it was restarting.
//   - A limiter: a new stage in the graph, so the process is replaced.
//   - A level alone, sent live on the command channel: applied_since_unix_test.go,
//     since it needs the channel; nothing here does, so this runs on Windows.
//
// MUTATION: answer "restart" whenever no level note was recorded (drop the
// `default` arm's live). Observed: FAIL, the music-rights case answered
// "restart".
// MUTATION: drop the primary-replaced arm. Observed: FAIL, the limiter case
// answered "live" for a destination running on a new process.
// MUTATION: drop the backup-replaced arm. Observed: FAIL, a replaced backup
// read as "nothing ... changed".
func TestAppliedSinceReadsTheProcessNotTheChange(t *testing.T) {
	t.Run("a change that compiles to the same command line keeps the process", func(t *testing.T) {
		e, store := storeEngine(t)
		row := liveRow(t, store, false)
		plan := planFor(t, e, row)
		proc := replyChild(t, "Command reply for stream 0: ret:0 res:")
		t.Cleanup(func() { _ = proc.Stop(t.Context()) })
		liveRunningDest(e, row, plan, proc)
		before := e.DestinationRunOf(row.ID)

		guarded := *row
		guarded.Profile = mixProfile(0)
		guarded.Profile.ExcludeRoles = []routing.TrackRole{routing.RoleMusic}
		after := planFor(t, e, &guarded)
		if after.spec != plan.spec {
			t.Fatal("keeping music out of a mix with no music track moved the restart signature")
		}
		e.ForgetDestinationLive(row.ID)
		e.stopDestinations(map[int64]destPlan{row.ID: after})

		applied, reason := e.AppliedSince(row.ID, before)
		if applied != liveApplied || reason != nothingToApply {
			t.Fatalf("applied = %q reason = %q, want live, %q", applied, reason, nothingToApply)
		}
		if e.DestinationRunOf(row.ID) != before {
			t.Fatal("the process was replaced by a change that reaches nothing it runs from")
		}
	})

	t.Run("a structural change replaces the process and says restart", func(t *testing.T) {
		e, store := storeEngine(t)
		row := liveRow(t, store, false)
		plan := planFor(t, e, row)
		proc := replyChild(t, "Command reply for stream 0: ret:0 res:")
		t.Cleanup(func() { _ = proc.Stop(t.Context()) })
		liveRunningDest(e, row, plan, proc)
		before := e.DestinationRunOf(row.ID)

		limited := *row
		limited.Profile = mixProfile(0)
		limited.Profile.Normalize = routing.NormLimiter
		next := planFor(t, e, &limited)
		if next.spec == plan.spec {
			t.Fatal("a limiter did not move the restart signature")
		}
		e.ForgetDestinationLive(row.ID)
		e.stopDestinations(map[int64]destPlan{row.ID: next})
		if e.DestinationRunOf(row.ID).Running() {
			t.Fatal("the stop phase kept a destination whose command line changed")
		}
		// Between the phases nothing runs: a stop is not a restart.
		if applied, reason := e.AppliedSince(row.ID, before); applied != "" {
			t.Fatalf("with nothing running yet: applied = %q (%s), want none", applied, reason)
		}
		// The start phase, standing in for startDestinations: a new process for
		// the same row.
		fresh := replyChild(t, "Command reply for stream 0: ret:0 res:")
		t.Cleanup(func() { _ = fresh.Stop(t.Context()) })
		liveRunningDest(e, &limited, next, fresh)

		applied, reason := e.AppliedSince(row.ID, before)
		if applied != liveRestart || reason == "" {
			t.Fatalf("applied = %q reason = %q, want restart with a reason", applied, reason)
		}
	})

	t.Run("a backup replaced alone is a restart that says the primary kept running", func(t *testing.T) {
		e, store := storeEngine(t)
		row := liveRow(t, store, false)
		plan := planFor(t, e, row)
		proc := replyChild(t, "Command reply for stream 0: ret:0 res:")
		t.Cleanup(func() { _ = proc.Stop(t.Context()) })
		oldBackup := replyChild(t, "Command reply for stream 0: ret:0 res:")
		t.Cleanup(func() { _ = oldBackup.Stop(t.Context()) })
		d := liveRunningDest(e, row, plan, proc)
		e.mu.Lock()
		d.backup = oldBackup
		e.mu.Unlock()
		before := e.DestinationRunOf(row.ID)

		// What reconcileBackup does for a backup whose spec moved: the primary
		// entry is republished with a new backup process beside the same one.
		newBackup := replyChild(t, "Command reply for stream 0: ret:0 res:")
		t.Cleanup(func() { _ = newBackup.Stop(t.Context()) })
		e.mu.Lock()
		next := *d
		next.backup = newBackup
		e.dests[row.ID] = &next
		e.mu.Unlock()

		applied, reason := e.AppliedSince(row.ID, before)
		if applied != liveRestart || reason != backupOnlyRestart {
			t.Fatalf("applied = %q reason = %q, want restart, %q", applied, reason, backupOnlyRestart)
		}
	})

	// A note says what happened to a LEVEL; it must not speak for a process
	// that was replaced after it was written.
	// MUTATION: read the level note before the processes. Observed: FAIL,
	// applied = "live" for a destination now on a new process.
	t.Run("a live level note does not hide a replaced process", func(t *testing.T) {
		e, store := storeEngine(t)
		row := liveRow(t, store, false)
		plan := planFor(t, e, row)
		proc := replyChild(t, "Command reply for stream 0: ret:0 res:")
		t.Cleanup(func() { _ = proc.Stop(t.Context()) })
		liveRunningDest(e, row, plan, proc)
		before := e.DestinationRunOf(row.ID)

		e.setLiveNote(row.ID, liveNote{applied: liveApplied})
		fresh := replyChild(t, "Command reply for stream 0: ret:0 res:")
		t.Cleanup(func() { _ = fresh.Stop(t.Context()) })
		liveRunningDest(e, row, plan, fresh)

		if applied, reason := e.AppliedSince(row.ID, before); applied != liveRestart {
			t.Fatalf("applied = %q (%s), want restart: the process was replaced", applied, reason)
		}
	})

	t.Run("nothing running before is neither live nor a restart", func(t *testing.T) {
		e, store := storeEngine(t)
		row := liveRow(t, store, false)
		if applied, reason := e.AppliedSince(row.ID, e.DestinationRunOf(row.ID)); applied != "" {
			t.Fatalf("applied = %q (%s) for a destination that was not running", applied, reason)
		}
	})
}
