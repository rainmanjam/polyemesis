package engine

import (
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
// was still finalising a tagged rec-s<id>- segment.
//
// Mutation: drop the m.stopping loop in Manager.RecorderRunning. Observed to
// fail with "false while an engine that is still stopping has a recorder".
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
	m.doneStopping(e)
	if m.RecorderRunning() {
		t.Error("RecorderRunning() = true after the stopping engine was forgotten")
	}
}
