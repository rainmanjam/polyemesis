package engine

import (
	"slices"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// stoppedEngine is an engine that has already shut down, with a one-port
// allocator.
//
// The span of one is the assertion: a port that was not released cannot be
// handed out again, so "did the stopped path give the port back" is answerable
// without reaching inside the allocator.
// A real store rather than an &Engine{...} literal, because the point of the
// mutation is that the guarded process STARTS -- and a started process reports
// its state, which reaches Status, which reads the database. A literal engine
// panics there, and a panic is a much weaker signal than an assertion naming
// what leaked.
func stoppedEngine(t *testing.T) (*Engine, *relay.Hub) {
	t.Helper()
	e, _ := storeEngine(t)
	e.alloc = relay.NewPortAllocator(freeUDPPort(t), 1)
	e.mu.Lock()
	e.stopped = true
	e.mu.Unlock()
	return e, e.hub
}

// A8. startDest was the one start path in the file that did not check
// e.stopped before publishing and starting -- startRendition, startFeed,
// reconcileBackupIngest, reconcilePlaylist, reconcileLoudness, startLoudness,
// reconcileClips, reconcileCaptions, startPreviewLocked, reconcileSelector and
// reconcileRecorder all do. A reconcile that raced shutdown left an FFmpeg
// publishing to a platform with nothing holding a reference to it.
//
// Mutation: in startDest, delete the `if e.stopped { ... return nil }` block
// immediately above the `e.dests[row.ID] = &destination{...}` publication.
// Observed to fail on all three assertions.
func TestStartingADestinationAfterShutdownLeavesNothingBehind(t *testing.T) {
	e, hub := stoppedEngine(t)
	row := &db.Destination{ID: 1, Name: "twitch", Kind: db.DestRTMP,
		URL: "rtmp://live.example/app", StreamKey: "key"}

	if err := e.startDest(destPlan{row: row, spec: "spec"}, hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}

	if d := e.dests[1]; d != nil {
		t.Error("a destination started after shutdown was published into e.dests, " +
			"where nothing will ever look for it again: Stop has already copied the map")
	}
	if subs := hub.Subscribers(); len(subs) != 0 {
		t.Errorf("the hub still forwards to %v after shutdown; the port goes back to "+
			"the allocator and the stale entry blasts datagrams at whoever gets it next", subs)
	}
	if _, err := e.alloc.Allocate(); err != nil {
		t.Errorf("the relay port was not released: %v. There are 500 across every "+
			"source engine, and each shutdown race burns one for the life of the daemon", err)
	}
}

// The same guard on the redundant output, which is the worse of the two: it is
// built with AutoRestart, so an orphan does not exit -- it reconnects to the
// platform's backup ingest for ever.
//
// Mutation: in publishDest, delete `e.stopped ||` from the condition guarding
// the swap. Observed to fail on all three assertions.
func TestStartingABackupAfterShutdownLeavesNothingBehind(t *testing.T) {
	e, hub := stoppedEngine(t)
	d := &destination{row: backupRow(), hub: hub}
	e.dests[d.row.ID] = d

	e.reconcileBackup(d.row.ID, d, routing.Result{}, "up")

	if got := e.dests[d.row.ID]; got.backup != nil {
		t.Error("a backup spawned after shutdown was published onto the destination; " +
			"it restarts for ever and no path can reach it to stop it")
	}
	if subs := hub.Subscribers(); slices.Contains(subs, destSubName(d.row.ID, destRoleBackup)) {
		t.Errorf("the hub still forwards to the backup's subscription: %v", subs)
	}
	if _, err := e.alloc.Allocate(); err != nil {
		t.Errorf("the backup's relay port was not released: %v", err)
	}
}

// A8 again, and the reason the two guards above were not enough. They begin
// from an ALREADY-stopped engine, so they exercise the e.stopped guard and
// never the window it cannot close:
//
//  1. a reconcile publishes into e.dests while e.stopped is false, and unlocks;
//  2. Stop runs -- sets stopped, copies the map, tears this entry down. Stop on
//     a supervisor.Process that has not been started was a no-op, so nothing was
//     stopped, but the hub subscription went and the relay port went back to the
//     allocator to be reissued;
//  3. the reconcile resumes and calls Start.
//
// The child is then live, publishing to the platform, after shutdown, on a port
// somebody else now owns, and in no map -- so nothing can ever find it to stop
// it. Reachable rather than theoretical: Manager.Sync stops an engine whose
// source was deleted while a concurrent Manager.Reconcile still holds that
// engine pointer.
//
// Deterministic, with no sleep and no timing: e.afterPublish is a seam that sits
// exactly in the window, and the whole of Stop runs inside it. What closes the
// window is supervisor.Process retiring on Stop, so the teardown in step 2
// latches the process and the Start in step 3 does nothing.
//
// Mutation: in supervisor.Start, delete `p.retired ||` from
// `if p.retired || p.running`. Observed to fail -- the process left "stopped".
func TestAReconcileThatPublishesIntoAShutdownStartsNothing(t *testing.T) {
	e, _ := storeEngine(t)
	e.alloc = relay.NewPortAllocator(freeUDPPort(t), 1)
	hub := e.hub
	row := &db.Destination{ID: 1, Name: "twitch", Kind: db.DestRTMP,
		URL: "rtmp://live.example/app", StreamKey: "key"}

	var proc *supervisor.Process
	e.afterPublish = func() {
		e.mu.Lock()
		d := e.dests[row.ID]
		e.mu.Unlock()
		if d == nil {
			t.Error("the destination was not published; this test is no longer in the window")
			return
		}
		proc = d.proc
		e.Stop()
	}

	if err := e.startDest(destPlan{row: row, spec: "spec"}, hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}

	if proc == nil {
		t.Fatal("the seam never ran, so the window was never exercised")
	}
	// That the port came back is what proves the shutdown really did run its
	// full teardown inside the window: without it this would be asserting
	// nothing started because nothing had been stopped.
	if _, err := e.alloc.Allocate(); err != nil {
		t.Errorf("the relay port was not released by the shutdown: %v", err)
	}
	assertNeverRuns(t, proc, "the destination")
}

// The same window on the redundant output, which is the worse of the two: the
// backup is built with AutoRestart, so an escaped child does not exit -- it
// reconnects to the platform's backup ingest for ever.
//
// Mutation: in supervisor.Start, delete `p.retired ||` from
// `if p.retired || p.running`. Observed to fail -- the backup left "stopped".
func TestAReconciledBackupPublishedIntoAShutdownStartsNothing(t *testing.T) {
	e, _ := storeEngine(t)
	base, held := testenv.FreeUDPWindow(t, 2)
	// Released together, immediately before the allocator is built: the window
	// has to be free for Allocate to hand it out, and holding it until this line
	// is what stopped anything else from taking it.
	for _, r := range held {
		r.Release()
	}
	e.alloc = relay.NewPortAllocator(base, 2)
	d := &destination{row: backupRow(), hub: e.hub}
	e.mu.Lock()
	e.dests[d.row.ID] = d
	e.mu.Unlock()

	var backup *supervisor.Process
	e.afterPublish = func() {
		e.mu.Lock()
		got := e.dests[d.row.ID]
		e.mu.Unlock()
		if got == nil || got.backup == nil {
			t.Error("no backup was published; this test is no longer in the window")
			return
		}
		backup = got.backup
		e.Stop()
	}

	e.reconcileBackup(d.row.ID, d, routing.Result{}, "up")

	if backup == nil {
		t.Fatal("the seam never ran, so the window was never exercised")
	}
	if subs := e.hub.Subscribers(); slices.Contains(subs, destSubName(d.row.ID, destRoleBackup)) {
		t.Errorf("the shutdown left the backup subscribed: %v", subs)
	}
	assertNeverRuns(t, backup, "the backup")
}

// assertNeverRuns fails the moment a process leaves StateStopped, and otherwise
// keeps watching for a fixed window.
//
// Absence is the assertion, so it has to be given a real chance rather than
// checked once: a process that Start did begin supervising announces StateStarting
// from the goroutine Start launched. Polling rather than sleeping the whole
// window means a broken guard fails in single-digit milliseconds; only a passing
// run pays the wait.
func assertNeverRuns(t *testing.T, p *supervisor.Process, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st := p.Status().State; st != supervisor.StateStopped {
			t.Fatalf("%s reached %q after the shutdown had already released its "+
				"relay port and its hub subscription: it is publishing to the "+
				"platform from a process in no map, on a port the allocator has "+
				"handed to somebody else, and nothing can reach it to stop it",
				what, st)
		}
		time.Sleep(time.Millisecond)
	}
}
