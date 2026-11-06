package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
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
// MUTATION: compare backups by pointer alone (the first fix). Observed: FAIL,
// a backup started or stopped alone read as "restarted".
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
		if applied != AppliedLive || reason != nothingToApply {
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
		if applied != AppliedRestart || reason == "" {
			t.Fatalf("applied = %q reason = %q, want restart with a reason", applied, reason)
		}
	})

	// The redundant feed moving is not the destination restarting: the output
	// the audience watches kept running, and the reason says what the backup did.
	backupCase := func(t *testing.T, hadBackup, hasBackup bool) (applied, reason string) {
		t.Helper()
		e, store := storeEngine(t)
		row := liveRow(t, store, false)
		plan := planFor(t, e, row)
		proc := replyChild(t, "Command reply for stream 0: ret:0 res:")
		t.Cleanup(func() { _ = proc.Stop(t.Context()) })
		d := liveRunningDest(e, row, plan, proc)
		var oldBackup *supervisor.Process
		if hadBackup {
			oldBackup = replyChild(t, "Command reply for stream 0: ret:0 res:")
			t.Cleanup(func() { _ = oldBackup.Stop(t.Context()) })
			e.mu.Lock()
			d.backup = oldBackup
			e.mu.Unlock()
		}
		before := e.DestinationRunOf(row.ID)

		// What reconcileBackup does: the primary entry is republished, same
		// primary process, with the backup it now has.
		next := *d
		next.backup = nil
		if hasBackup {
			fresh := replyChild(t, "Command reply for stream 0: ret:0 res:")
			t.Cleanup(func() { _ = fresh.Stop(t.Context()) })
			next.backup = fresh
		}
		e.mu.Lock()
		e.dests[row.ID] = &next
		e.mu.Unlock()
		return e.AppliedSince(row.ID, before)
	}
	for _, c := range []struct {
		name                 string
		hadBackup, hasBackup bool
		want                 string
	}{
		{"a backup replaced alone", true, true, backupRestarted},
		{"a backup started alone", false, true, backupStarted},
		{"a backup stopped alone", true, false, backupStopped},
	} {
		t.Run(c.name+" is live, and says what the backup did", func(t *testing.T) {
			applied, reason := backupCase(t, c.hadBackup, c.hasBackup)
			if applied != AppliedLive || reason != c.want {
				t.Fatalf("applied = %q reason = %q, want live, %q", applied, reason, c.want)
			}
		})
	}

	// A delayed destination's process exists while its buffer fills and is
	// started only when the first delayed packet is due. Until then nothing
	// is running, and a save neither kept nor restarted anything.
	// MUTATION: DestinationRunOf counts an unstarted process. Observed: FAIL,
	// applied = "live" for a destination whose FFmpeg never started.
	t.Run("a process not yet started is not running", func(t *testing.T) {
		e, store := storeEngine(t)
		row := liveRow(t, store, false)
		plan := planFor(t, e, row)
		idle := supervisor.New(testLogger(), supervisor.Spec{Name: "dest-delayed", Kind: "destination", Bin: "/nonexistent"})
		liveRunningDest(e, row, plan, idle)
		before := e.DestinationRunOf(row.ID)
		if before.Running() {
			t.Fatal("a process that was never started reads as running")
		}
		if applied, reason := e.AppliedSince(row.ID, before); applied != "" {
			t.Fatalf("applied = %q (%s) for a destination whose process never started", applied, reason)
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

		e.setLiveNote(row.ID, liveNote{applied: AppliedLive})
		fresh := replyChild(t, "Command reply for stream 0: ret:0 res:")
		t.Cleanup(func() { _ = fresh.Stop(t.Context()) })
		liveRunningDest(e, row, plan, fresh)

		if applied, reason := e.AppliedSince(row.ID, before); applied != AppliedRestart {
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
