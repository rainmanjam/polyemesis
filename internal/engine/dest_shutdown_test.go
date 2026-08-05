package engine

import (
	"slices"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// stoppedEngine is an engine that has already shut down, with a one-port
// allocator.
//
// The span of one is the assertion: a port that was not released cannot be
// handed out again, so "did the stopped path give the port back" is answerable
// without reaching inside the allocator.
func stoppedEngine(t *testing.T) (*Engine, *relay.Hub) {
	t.Helper()
	hub, err := relay.New(testLogger(), 0)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	t.Cleanup(func() { hub.Close() })
	dir := t.TempDir()
	e := &Engine{
		log:   testLogger(),
		tools: &ffmpeg.Tools{FFmpeg: dir + "/no-such-ffmpeg"},
		alloc: relay.NewPortAllocator(freeUDPPort(t), 1),
		hub:   hub,
		dests: map[int64]*destination{},
	}
	e.stopped = true
	return e, hub
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

	if err := e.startDest(row, routing.Result{}, "spec", hub, 0); err != nil {
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
// Mutation: in startBackup, delete the `if e.stopped { ... return }` block
// above the `d.backup = proc` assignment. Observed to fail on all three
// assertions.
func TestStartingABackupAfterShutdownLeavesNothingBehind(t *testing.T) {
	e, hub := stoppedEngine(t)
	d := &destination{row: backupRow(), hub: hub}

	e.startBackup(d, routing.Result{}, "spec")

	if d.backup != nil {
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
