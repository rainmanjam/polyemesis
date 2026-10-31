package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/clips"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
)

// app-council A14: the clip buffer's switch survives a restart.
//
// It used to be held only in e.clipOn, so every restart -- an upgrade, a
// reboot, a crash the supervisor recovered from -- came back with the buffer
// off, and the Clips page carried a note apologising for it. The operator found
// out by pressing Capture after the moment had already happened, which is the
// one time the feature exists for.
//
// A fresh engine over the SAME database and the SAME programme is the restart.
//
// Mutation: drop the SetSourceClipBuffer call from SetClipBuffer (or the load
// in New). Observed to fail with "came back off".
func TestTheClipBufferSwitchAndWindowSurviveARestart(t *testing.T) {
	one, store := storeEngine(t)
	if err := one.SetClipBuffer(true, 45); err != nil {
		t.Fatalf("SetClipBuffer: %v", err)
	}
	one.Stop()

	two, err := New(testLogger(), one.cfg, store, one.tools, events.NewBroker(), one.sourceID, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(two.Stop)

	if !two.ClipBuffer().Enabled {
		t.Fatal("the clip buffer came back off after a restart; the operator's switch was lost")
	}
	two.mu.RLock()
	window := two.clipCfg.WindowSeconds
	two.mu.RUnlock()
	if window != 45 {
		t.Errorf("window after restart = %ds, want the 45s the operator applied", window)
	}
}

// Per programme, never install-wide: SetClipBuffer takes a programme, so the
// stored switch does too. Studio B's operator turning capture on must not start
// Main's buffer on the next boot -- that is memory Main never asked to spend.
func TestARestartRestoresEachProgrammesClipBufferSeparately(t *testing.T) {
	mainEng, store := storeEngine(t)

	src := &db.Source{Name: "Studio B", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := store.CreateSource(src); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	studioB, err := New(testLogger(), mainEng.cfg, store, mainEng.tools, events.NewBroker(), src.ID, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := studioB.SetClipBuffer(true, 0); err != nil {
		t.Fatalf("SetClipBuffer: %v", err)
	}
	studioB.Stop()
	mainEng.Stop()

	mainAgain, err := New(testLogger(), mainEng.cfg, store, mainEng.tools, events.NewBroker(), mainEng.sourceID, nil, nil)
	if err != nil {
		t.Fatalf("New Main: %v", err)
	}
	t.Cleanup(mainAgain.Stop)
	bAgain, err := New(testLogger(), mainEng.cfg, store, mainEng.tools, events.NewBroker(), src.ID, nil, nil)
	if err != nil {
		t.Fatalf("New Studio B: %v", err)
	}
	t.Cleanup(bAgain.Stop)

	if mainAgain.ClipBuffer().Enabled {
		t.Error("Main's buffer came back on; Studio B's switch was stored install-wide")
	}
	if !bAgain.ClipBuffer().Enabled {
		t.Error("Studio B's buffer came back off after a restart")
	}
	// Enabled with 0 seconds keeps the default window, and the default is what
	// comes back -- not 0, which the capturer would read as no history at all.
	bAgain.mu.RLock()
	window := bAgain.clipCfg.WindowSeconds
	bAgain.mu.RUnlock()
	if window != clips.DefaultWindowSeconds {
		t.Errorf("Studio B window after restart = %ds, want the default %ds", window, clips.DefaultWindowSeconds)
	}
}

// An install that never touched the switch -- every one upgraded to this
// build -- boots with it off. On by default would start holding a window of
// somebody's 4K feed in memory because a build added a table.
func TestAProgrammeThatNeverSetTheClipBufferBootsWithItOff(t *testing.T) {
	e, _ := storeEngine(t)
	if e.ClipBuffer().Enabled {
		t.Error("an untouched programme booted with the clip buffer on")
	}
}
