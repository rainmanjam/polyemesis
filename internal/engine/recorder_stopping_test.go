//go:build !windows

package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// wedgedRecorder starts a stand-in recorder that ignores SIGTERM, so its Stop
// takes the whole stopTimeout: the window in which a real recorder is still
// finalising the segment it was writing.
func wedgedRecorder(t *testing.T) *supervisor.Process {
	t.Helper()
	dir := t.TempDir()
	bin, armed := filepath.Join(dir, "ffmpeg"), filepath.Join(dir, "armed")
	// SIG_IGN survives exec, so the sleep inherits the ignored TERM.
	script := "#!/bin/sh\ntrap '' TERM; : > '" + armed + "'\nexec sleep 60\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	proc := supervisor.New(slog.New(slog.NewTextHandler(io.Discard, nil)),
		supervisor.Spec{Name: "recorder", Kind: "recorder", Bin: bin})
	proc.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = proc.Stop(ctx)
	})
	waitUntil(t, func() bool { _, err := os.Stat(armed); return err == nil },
		"the stand-in recorder to be running with SIGTERM ignored")
	return proc
}

func shortStopTimeout(t *testing.T) {
	prev := stopTimeout
	stopTimeout = 1500 * time.Millisecond
	t.Cleanup(func() { stopTimeout = prev })
}

// assertRunningUntilStopped asserts RecorderRunning holds true while stop is
// still running and turns false once it has returned.
func assertRunningUntilStopped(t *testing.T, running func() bool, slotEmpty func() bool, stop func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); stop() }()
	waitUntil(t, slotEmpty, "the recorder to leave its slot")
	select {
	case <-done:
		t.Fatal("the stop returned before the wedged recorder could be observed stopping")
	default:
	}
	if !running() {
		t.Fatal("RecorderRunning() = false while the recorder child is still alive and " +
			"writing its segment: Delete's guard would unlink a file being written")
	}
	<-done
	if running() {
		t.Error("RecorderRunning() = true after the recorder's Stop returned")
	}
}

// THE SLOT EMPTIES BEFORE THE CHILD EXITS. Turning recording off (or the
// free-space floor) takes the recorder out of its slot and only then stops it,
// and ffmpeg goes on writing until it has finalised the file. RecorderRunning
// is what Delete's guard asks, so it has to count that child until Stop
// returns.
//
// Mutation: drop the recorderStopping term in RecorderRunning. Observed to
// fail both engine tests with "false while the recorder child is still alive".
func TestRecorderRunningCoversARecorderStillStoppingAfterLeavingItsSlot(t *testing.T) {
	shortStopTimeout(t)
	e := lifeEngine(t)
	e.recorder = wedgedRecorder(t)
	slotEmpty := func() bool { e.mu.RLock(); defer e.mu.RUnlock(); return e.recorder == nil }
	assertRunningUntilStopped(t, e.RecorderRunning, slotEmpty, func() { e.stopAux(auxRecorder) })
}

func TestRecorderRunningCoversARecorderStillStoppingDuringEngineStop(t *testing.T) {
	shortStopTimeout(t)
	e, _ := storeEngine(t)
	e.recorder = wedgedRecorder(t)
	slotEmpty := func() bool { e.mu.RLock(); defer e.mu.RUnlock(); return e.recorder == nil }
	assertRunningUntilStopped(t, e.RecorderRunning, slotEmpty, e.Stop)
}

// A DELETED SOURCE'S ENGINE LEAVES THE MAP BEFORE IT STOPS. Sync removes a
// retiring engine from m.engines and only then calls Stop, so a probe that
// read only the map answered "no recorder" while that programme's recorder
// was still finalising a tagged rec-s<id>- segment. And Engine.Stop does not
// wait for a recorder stop a concurrent reconcile began, so the entry has to
// outlive Stop until the engine itself reports no recorder.
//
// Mutation: drop the stopping loop in Manager.RecorderRunning. Observed to
// fail with "false while an engine that is still stopping has a recorder".
// Mutation: forget the entry in doneStopping. Observed to fail with "false
// while a recorder stop begun by a reconcile is still running".
func TestManagerRecorderRunningCountsEnginesStillStopping(t *testing.T) {
	e := lifeEngine(t)
	e.recorder = loudTestProc()
	m := &Manager{engines: map[int64]*Engine{}}
	if m.RecorderRunning() {
		t.Fatal("RecorderRunning() = true with no engines at all")
	}
	m.mu.Lock()
	m.markStopping(e)
	m.mu.Unlock()
	if !m.RecorderRunning() {
		t.Fatal("RecorderRunning() = false while an engine that is still stopping has a recorder")
	}

	// Its Stop returned, but a recorder stop begun elsewhere is still running.
	e.mu.Lock()
	e.recorder, e.recorderStopping = nil, 1
	e.mu.Unlock()
	m.doneStopping(e)
	if !m.RecorderRunning() {
		t.Fatal("RecorderRunning() = false while a recorder stop begun by a reconcile is still running")
	}

	e.recorderStopped()
	if m.RecorderRunning() {
		t.Error("RecorderRunning() = true after the last recorder stop returned")
	}
	m.mu.RLock()
	left := len(m.stopping)
	m.mu.RUnlock()
	if left != 0 {
		t.Errorf("%d stopped engines still tracked after they reported no recorder", left)
	}
}

// stopEngine keeps an entry whose recorder a concurrent stop still holds,
// marked done so RecorderRunning or Sync's prune can drop it once that stop
// returns.
func TestStopEngineKeepsAnEngineWhoseRecorderIsStillStopping(t *testing.T) {
	e, _ := storeEngine(t)
	m := &Manager{engines: map[int64]*Engine{}}
	m.mu.Lock()
	m.markStopping(e)
	m.mu.Unlock()
	e.mu.Lock()
	e.recorderStopping = 1
	e.mu.Unlock()

	m.stopEngine(e)
	m.mu.RLock()
	done, tracked := m.stopping[e]
	m.mu.RUnlock()
	if !tracked || !done {
		t.Fatalf("after stopEngine with a recorder stop still running: tracked=%v done=%v, "+
			"want tracked and done", tracked, done)
	}
	if !m.RecorderRunning() {
		t.Fatal("RecorderRunning() = false while a recorder stop is still running")
	}
	e.recorderStopped()
	if m.RecorderRunning() || stoppingLen(m) != 0 {
		t.Error("the entry outlived the last recorder stop")
	}
}

func stoppingLen(m *Manager) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.stopping)
}

// A SOURCE THAT KEEPS FAILING TO START must not pin one engine per reconcile.
// Start can launch the recorder before it fails, and the engine is in no map
// yet, so it is tracked from before Start: a delete in that window sees the
// recorder. Once stopped with no recorder left, the entry goes at once rather
// than waiting for the delete guard to be asked.
//
// Mutation: mark the engine stopping only after start returns. Observed to
// fail with "false while a failing start has launched a recorder".
// Mutation: drop the forget in finishStopping. Observed to fail with "the
// stopping set grew".
func TestARepeatedlyFailingStartIsTrackedAndDoesNotGrowTheStoppingSet(t *testing.T) {
	m := &Manager{log: testLogger(), engines: map[int64]*Engine{}}
	for i := 0; i < 3; i++ {
		e, _ := storeEngine(t)
		ok := m.startTracked(context.Background(), e, func(context.Context) error {
			e.mu.Lock()
			e.recorder = loudTestProc()
			e.mu.Unlock()
			if !m.RecorderRunning() {
				t.Error("RecorderRunning() = false while a failing start has launched a recorder")
			}
			return errors.New("reconcile failed")
		})
		if ok {
			t.Fatal("startTracked reported success for a start that failed")
		}
		if n := stoppingLen(m); n != 0 {
			t.Fatalf("after %d failed starts the stopping set grew to %d", i+1, n)
		}
	}
	if m.RecorderRunning() {
		t.Error("RecorderRunning() = true after every failed start was stopped")
	}
}

// A successful start leaves the entry for the caller to drop once it has
// published the engine, so there is no instant at which it is in neither.
func TestASuccessfulStartStaysTrackedUntilPublished(t *testing.T) {
	m := &Manager{log: testLogger(), engines: map[int64]*Engine{}}
	e := lifeEngine(t)
	if !m.startTracked(context.Background(), e, func(context.Context) error { return nil }) {
		t.Fatal("startTracked reported failure for a start that succeeded")
	}
	m.mu.RLock()
	done, tracked := m.stopping[e]
	m.mu.RUnlock()
	if !tracked || done {
		t.Fatalf("after a successful start: tracked=%v done=%v, want tracked and not done", tracked, done)
	}
}

// Sync prunes too, so an entry a concurrent stop kept alive is dropped even
// on an install where nobody ever asks the delete guard.
//
// Mutation: drop the pruneStopping call. Observed to fail with "kept".
func TestPruneStoppingDropsOnlyStoppedEnginesWithNoRecorder(t *testing.T) {
	e := lifeEngine(t)
	m := &Manager{engines: map[int64]*Engine{}}
	m.mu.Lock()
	m.markStopping(e)
	m.mu.Unlock()
	e.mu.Lock()
	e.recorderStopping = 1
	e.mu.Unlock()

	m.pruneStopping()
	if stoppingLen(m) != 1 {
		t.Fatal("pruneStopping dropped an engine whose Stop has not returned")
	}
	m.doneStopping(e)
	m.pruneStopping()
	if stoppingLen(m) != 1 {
		t.Fatal("pruneStopping dropped an engine whose recorder is still stopping")
	}
	e.recorderStopped()
	m.pruneStopping()
	if stoppingLen(m) != 0 {
		t.Error("a stopped engine with no recorder was kept")
	}
}
