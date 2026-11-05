//go:build !windows

// Unix only for the level-only case, which needs the command channel; see
// live_reconcile_unix_test.go.

package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// AppliedSince answers a saved change by what the reconcile did to the process,
// never by what the change looked like. The three cases the routing page's
// toast reports, end to end through stopDestinations:
//
//   - E3 (2026-11-04 demo): music kept out of a mix the music track had already
//     left. The profile moved, the command line did not, and the process was
//     kept -- the console said it was restarting.
//   - A level alone: sent live on the command channel, process kept.
//   - A limiter: a new stage in the graph, so the process is replaced.
//
// MUTATION: drop the `after == before` arm, so a kept process with no level
// note reads as a restart. Observed: FAIL, the music-rights case answered
// "restart".
// MUTATION: drop the `after.Running()` arm. Observed: FAIL, the limiter case
// answered "" for a destination running on a new process.
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

	t.Run("a level alone is sent live and keeps the process", func(t *testing.T) {
		e, store := storeEngine(t)
		row := liveRow(t, store, false)
		plan := planFor(t, e, row)
		proc := replyChild(t, "Command reply for stream 0: ret:0 res:")
		t.Cleanup(func() { _ = proc.Stop(t.Context()) })
		liveRunningDest(e, row, plan, proc)
		before := e.DestinationRunOf(row.ID)

		quieter := *row
		quieter.Profile = mixProfile(0)
		quieter.Profile.Tracks[0].Gain = 0.25
		e.ForgetDestinationLive(row.ID)
		e.stopDestinations(map[int64]destPlan{row.ID: planFor(t, e, &quieter)})

		applied, reason := e.AppliedSince(row.ID, before)
		if applied != liveApplied || reason != "" {
			t.Fatalf("applied = %q reason = %q, want live with no reason", applied, reason)
		}
		if e.DestinationRunOf(row.ID) != before {
			t.Fatal("a live level change replaced the process")
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

	t.Run("nothing running before is neither live nor a restart", func(t *testing.T) {
		e, store := storeEngine(t)
		row := liveRow(t, store, false)
		if applied, reason := e.AppliedSince(row.ID, e.DestinationRunOf(row.ID)); applied != "" {
			t.Fatalf("applied = %q (%s) for a destination that was not running", applied, reason)
		}
	})
}
