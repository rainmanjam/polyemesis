package engine

import (
	"slices"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// A1. Stop cleared e.dests and then stopped only d.proc. stopBackup was
// reachable from exactly two places, teardownDest and reconcileBackup, and
// neither was on the shutdown path -- so shutdown, and deleting a SOURCE, left
// the redundant FFmpeg publishing for ever and its relay port burned.
//
// Mutation: in Stop, replace the `e.teardownDest(dest)` goroutine loop with the
// `stop(d.proc)` it replaced. Observed to fail on the backup's subscription and
// on both ports.
func TestStopTakesTheBackupDownWithTheDestination(t *testing.T) {
	e, _ := storeEngine(t)
	// Span of two, so "was the port given back" is answerable by asking for it
	// again rather than by reaching inside the allocator.
	base, held := testenv.FreeUDPWindow(t, 2)
	// Released together, immediately before the allocator is built: the window
	// has to be free for Allocate to hand it out, and holding it until this line
	// is what stopped anything else from taking it.
	for _, r := range held {
		r.Release()
	}
	e.alloc = relay.NewPortAllocator(base, 2)

	row := backupRow()
	primaryPort, err := e.alloc.Allocate()
	if err != nil {
		t.Fatalf("allocate primary: %v", err)
	}
	backupPort, err := e.alloc.Allocate()
	if err != nil {
		t.Fatalf("allocate backup: %v", err)
	}
	primarySub, backupSub := destSubName(row.ID, ""), destSubName(row.ID, destRoleBackup)
	e.hub.Subscribe(primarySub, primaryPort)
	e.hub.Subscribe(backupSub, backupPort)
	e.dests[row.ID] = &destination{
		row: row, hub: e.hub, spec: "spec",
		port: primaryPort, subName: primarySub,
		backupPort: backupPort, backupSub: backupSub,
		backup: supervisor.New(testLogger(), supervisor.Spec{Name: backupSub}),
	}

	e.Stop()

	if subs := e.hub.Subscribers(); slices.Contains(subs, backupSub) {
		t.Errorf("the backup is still subscribed after Stop: %v. Its FFmpeg has "+
			"AutoRestart, so it does not exit -- it keeps publishing to the platform "+
			"from a process nothing holds a reference to", subs)
	}
	if subs := e.hub.Subscribers(); slices.Contains(subs, primarySub) {
		t.Errorf("the primary is still subscribed after Stop: %v", subs)
	}
	for i := range 2 {
		if _, err := e.alloc.Allocate(); err != nil {
			t.Errorf("port %d of 2 was not released by Stop: %v. The allocator is "+
				"shared across every source engine, so each deleted source with a "+
				"backup permanently burns one of 500", i+1, err)
		}
	}
}
