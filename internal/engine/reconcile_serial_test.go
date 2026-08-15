package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// storeEngine builds a real Engine over a temporary database, without starting
// it. Reconcile and Status both read the store, so neither can be exercised
// against the &Engine{...} literals the rest of this package builds.
//
// The FFmpeg path cannot exec, so a child spawned here logs a failed spawn
// rather than binding anything -- what these tests are about is the
// bookkeeping around the spawn, not the spawn.
func storeEngine(t *testing.T) (*Engine, *db.DB) {
	t.Helper()
	dir := t.TempDir()
	store := dbtest.OpenAt(t, filepath.Join(dir, "polyemesis.db"))

	cfg := config.Config{DataDir: dir}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	tools := &ffmpeg.Tools{
		FFmpeg:  filepath.Join(dir, "no-such-ffmpeg"),
		FFprobe: filepath.Join(dir, "no-such-ffprobe"),
	}
	id, err := store.DefaultSourceID()
	if err != nil {
		t.Fatalf("DefaultSourceID: %v", err)
	}
	e, err := New(testLogger(), cfg, store, tools, events.NewBroker(), id, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(e.Stop)
	return e, store
}

// A3. Reconcile had no serialization boundary at all: e.mu is dropped and
// retaken a dozen times inside one pass, so two callers -- and there are seven
// concurrent ones, five HTTP handlers, the scheduler's actuator and
// observeLoop -- could both see a destination as missing and both start it.
// The hub's subscriber map is a bare assignment on a deterministic name, so the
// second start silently replaces the first, and the first FFmpeg goes on
// running against a port nothing sends to, in no map, holding a relay port
// nothing will release.
//
// Proven by holding the lock the reconcile must take. Mutation: delete
// `e.reconcileMu.Lock()` from Reconcile (engine.go, first line of the body).
// Observed to fail -- Reconcile ran to completion while this test held the
// lock.
func TestReconcileIsSerializedForTheWholeOfIt(t *testing.T) {
	e, _ := storeEngine(t)

	e.reconcileMu.Lock()
	done := make(chan error, 1)
	go func() { done <- e.Reconcile() }()

	select {
	case <-done:
		e.reconcileMu.Unlock()
		t.Fatal("Reconcile ran to completion while the reconcile lock was held: " +
			"there is no serialization boundary, so two reconciles can both start " +
			"the same destination and orphan the first FFmpeg")
	case <-time.After(250 * time.Millisecond):
	}
	e.reconcileMu.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Reconcile never returned after the lock was released; the boundary " +
			"is a deadlock rather than a queue")
	}
}

// The boundary is only safe because nothing that holds another engine lock
// calls Reconcile. This pins the one callback that does call it -- the caption
// health guard -- to doing so from a goroutine of its own, which is what keeps
// a reconcile that is tearing the captioner down from waiting on itself.
//
// Mutation: in onCaptionsDegraded, replace the `go func() { ... }()` wrapper
// with a direct `e.Reconcile()`. Observed to fail -- the second Reconcile
// blocked for the whole timeout.
func TestTheCaptionHealthGuardReconcilesOffTheCallersGoroutine(t *testing.T) {
	e, _ := storeEngine(t)

	e.reconcileMu.Lock()

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		e.onCaptionsDegraded("the machine could not keep up")
	}()

	var blocked bool
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		blocked = true
	}
	e.reconcileMu.Unlock()
	<-returned
	// Queues behind whatever the guard handed off, so the handed-off reconcile
	// has finished before this test does.
	_ = e.Reconcile()

	if blocked {
		t.Fatal("onCaptionsDegraded blocked on a reconcile that cannot start until " +
			"the one already in flight finishes -- and the one in flight is the " +
			"teardown that waits for this goroutine")
	}
}
