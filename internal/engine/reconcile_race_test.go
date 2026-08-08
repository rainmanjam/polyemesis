package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// fakeFFprobe is a tiny executable rather than a mock: probeOnce deliberately
// owns the process boundary, and these regressions need to hold a probe in
// flight on the far side of that boundary.
func fakeFFprobe(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatalf("write fake ffprobe: %v", err)
	}
	return path
}

const stereoProbeJSON = `{"streams":[{"codec_name":"h264","codec_type":"video","width":1280,"height":720,"avg_frame_rate":"30/1"},{"codec_name":"aac","codec_type":"audio","channels":2,"channel_layout":"stereo","sample_rate":"48000"}]}`

// This stages the precise two-pass disagreement that made an output disappear:
// an older Reconcile has already decided that an unmeasured source needs an
// empty destination plan, while a probe has just measured it and wants to
// start every destination. The only safe order is for probeLoop to queue
// behind Reconcile's end-to-end lock, letting the old pass stop first and the
// probe pass start last.
//
// Mutant exercised: in probeLoop, delete the e.reconcileMu.Lock()/Unlock()
// around reconcileMeters, reconcileRecorder, and reconcileOutputs. With that
// edit the probe pass starts the destination while this test holds the lock;
// the staged old pass then applies its empty plan and leaves no destination.
func TestProbeLoopQueuesItsOutputReconcileBehindAnOlderEmptyPlan(t *testing.T) {
	e, store := storeEngine(t)
	s := db.DefaultSettings()
	setSettings(e, s)

	dst, err := store.CreateDestination(&db.Destination{
		Name:      "stress destination",
		Kind:      db.DestRTMP,
		URL:       "rtmp://example.invalid/live",
		StreamKey: "key",
		Enabled:   true,
		SourceID:  &e.sourceID,
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}

	e.tools.FFprobe = fakeFFprobe(t, "printf '%s\\n' '"+stereoProbeJSON+"'\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.probeLoop(ctx)

	// probeLoop snapshots RxBytes before its first timer. Keep data flowing so
	// its first tick is unambiguously a probing tick regardless of scheduling.
	stopFlow := make(chan struct{})
	defer close(stopFlow)
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			e.hub.Deliver([]byte{1})
			select {
			case <-stopFlow:
				return
			case <-ticker.C:
			}
		}
	}()

	// This is the older Reconcile after it has snapshotted measured=false and
	// compiled its deliberately empty plan. Holding its real serialization
	// boundary makes the ordering observable without timing a goroutine race.
	e.reconcileMu.Lock()
	locked := true
	defer func() {
		if locked {
			e.reconcileMu.Unlock()
		}
	}()

	waitUntil(t, func() bool {
		e.mu.RLock()
		defer e.mu.RUnlock()
		return e.measured
	}, "the probe to measure the source")

	// On the mutant, probeLoop has no lock and its output reconcile reaches
	// startDestinations here. Wait only for that wrong state; on fixed code it
	// cannot happen while reconcileMu is held, and the bounded wait is simply
	// the proof of that ordering.
	startedWhileOlderPassHeld := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.RLock()
		startedWhileOlderPassHeld = e.dests[dst.ID] != nil
		e.mu.RUnlock()
		if startedWhileOlderPassHeld {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The old pass must still execute its stop phase. With the fixed ordering
	// this is a no-op and the queued probe pass starts the destination after we
	// release reconcileMu. With the mutant it tears down the just-started one.
	e.stopDestinations(map[int64]destPlan{})
	e.reconcileMu.Unlock()
	locked = false

	waitUntil(t, func() bool {
		e.mu.RLock()
		defer e.mu.RUnlock()
		return e.dests[dst.ID] != nil
	}, "the destination to be running after the two reconciles")

	if startedWhileOlderPassHeld {
		t.Fatal("probeLoop started a destination while an older Reconcile held reconcileMu; " +
			"the older empty plan can tear it down permanently")
	}
}

// A successful ffprobe is allowed to take ten seconds. If the ingest changes
// during that time, the result belongs to the old transport and must be
// discarded rather than certifying the new one as measured.
//
// Mutant exercised: in probeOnce, remove the sourceGen capture/check (or,
// specifically, delete `if e.sourceGen != gen { ... return false }`). The
// released old probe then commits its stereo RTMP layout as if it measured SRT.
func TestProbeOnceDiscardsAResultFromBeforeAnIngestModeChange(t *testing.T) {
	e, _ := storeEngine(t)
	entered := filepath.Join(t.TempDir(), "probe-entered")
	release := filepath.Join(t.TempDir(), "probe-release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	e.tools.FFprobe = fakeFFprobe(t, "touch \"$PROBE_ENTERED\"\n"+
		"while [ ! -f \"$PROBE_RELEASE\" ]; do sleep 0.01; done\n"+
		"printf '%s\\n' '"+stereoProbeJSON+"'\n")
	t.Setenv("PROBE_ENTERED", entered)
	t.Setenv("PROBE_RELEASE", release)

	old := db.DefaultSettings()
	old.Ingest.Mode = db.IngestRTMP
	old.Ingest.RTMP.App = "live"
	setSettings(e, old)
	e.mu.Lock()
	e.source = routing.Source{Tracks: []routing.Track{{Index: 0, Channels: 2, Codec: "aac"}}}
	e.measured = true
	e.measuredMode = db.IngestRTMP
	e.mu.Unlock()

	done := make(chan bool, 1)
	go func() { done <- e.probeOnce(context.Background()) }()
	waitUntil(t, func() bool {
		_, err := os.Stat(entered)
		return err == nil
	}, "ffprobe to begin reading the old ingest")

	// This is the production invalidation path: a mode change resets the
	// placeholder and advances sourceGen before SRT takes its early return.
	next := old
	next.Ingest.Mode = db.IngestSRT
	setSettings(e, next)
	e.reconcileIngest(next, old)
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatalf("release fake ffprobe: %v", err)
	}

	select {
	case changed := <-done:
		if changed {
			t.Fatal("a probe from before the mode change reported a layout change")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("probeOnce did not return after ffprobe was released")
	}

	e.mu.RLock()
	measured, mode := e.measured, e.measuredMode
	src := e.source
	e.mu.RUnlock()
	if measured || mode != db.IngestUnset || !sameSource(src, routing.DefaultSource()) {
		t.Fatalf("stale probe certified the new ingest: measured=%v mode=%q source=%+v; "+
			"want the unmeasured placeholder after the mode change", measured, mode, src)
	}
}
