//go:build !windows

// The live path itself, which needs the command channel. Unix only by design:
// on Windows a destination has no stdin pipe and every level change restarts
// (liveGainsSupported); live_reconcile_windows_test.go proves that restart.

package engine

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// MUTATION: in stopDestinations, skip the liveTry append (treat every kept
// level change as nothing to do). Observed: FAIL, "no outcome recorded".
func TestALevelOnlyChangeGoesToTheRunningProcess(t *testing.T) {
	e, store := storeEngine(t)
	row := liveRow(t, store, false)
	before := planFor(t, e, row)

	proc := replyChild(t, "Command reply for stream 0: ret:0 res:")
	t.Cleanup(func() { _ = proc.Stop(t.Context()) })
	d := liveRunningDest(e, row, before, proc)

	muted := *row
	muted.Profile = mixProfile(0)
	muted.Profile.Tracks[0].Muted = true
	after := planFor(t, e, &muted)
	if after.spec != before.spec {
		t.Fatal("a mute moved the restart signature; it can never be live")
	}

	e.stopDestinations(map[int64]destPlan{row.ID: after})

	applied, reason, ok := e.DestinationLive(row.ID)
	if !ok {
		t.Fatal("no outcome recorded for a level change")
	}
	if applied != liveApplied {
		t.Fatalf("applied = %q (%s), want live", applied, reason)
	}
	e.mu.RLock()
	cur := e.dests[row.ID]
	e.mu.RUnlock()
	if cur == nil || cur.proc != proc {
		t.Fatal("the destination was replaced; a live change must keep the same process")
	}
	if proc.Status().State != supervisor.StateRunning {
		t.Fatalf("the process is %s after a live change", proc.Status().State)
	}
	if !strings.Contains(d.graph.get().FilterComplex, "volume@trk0=volume=0") {
		t.Errorf("the respawn graph does not carry the mute: %s", d.graph.get().FilterComplex)
	}
	if !strings.Contains(cur.compiled.FilterComplex, "volume@trk0=volume=0") {
		t.Errorf("the published graph does not carry the mute: %s", cur.compiled.FilterComplex)
	}
}
