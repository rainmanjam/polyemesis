//go:build windows

package engine

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// ON WINDOWS A LEVEL-ONLY CHANGE RESTARTS, and says why. The live path is
// unverified there (liveGainsSupported), so the change the Unix build sends on
// the command channel (TestALevelOnlyChangeGoesToTheRunningProcess) must instead
// tear the destination down for the next start to build with the new level, and
// the operator is told it was a restart and that the platform is the reason.
//
// MUTATION: make liveGainsSupported return true. Observed (by reading, on a
// Windows runner): the apply reaches a process with no pipe, fails as
// unavailable rather than unverified, and the reason no longer names the
// platform.
func TestOnWindowsALevelOnlyChangeRestartsAndSaysWhy(t *testing.T) {
	e, store := storeEngine(t)
	row := liveRow(t, store, false)
	before := planFor(t, e, row)

	proc := replyChild(t, "Command reply for stream 0: ret:0 res:")
	t.Cleanup(func() { _ = proc.Stop(t.Context()) })
	liveRunningDest(e, row, before, proc)

	muted := *row
	muted.Profile = mixProfile(0)
	muted.Profile.Tracks[0].Muted = true
	after := planFor(t, e, &muted)
	if after.spec != before.spec {
		t.Fatal("a mute moved the restart signature; this would restart for another reason")
	}
	e.stopDestinations(map[int64]destPlan{row.ID: after})

	applied, reason, _ := e.DestinationLive(row.ID)
	if applied != liveRestart || !strings.Contains(reason, "not verified on this platform") {
		t.Fatalf("applied = %q reason = %q, want a restart naming the platform", applied, reason)
	}
	e.mu.RLock()
	_, still := e.dests[row.ID]
	e.mu.RUnlock()
	if still {
		t.Error("a level change on Windows left the destination in place; it must restart")
	}
	if proc.Status().State == supervisor.StateRunning {
		t.Error("a level change on Windows left the process running")
	}
}
