//go:build !windows

package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// startQuietPrimary brings the selector up with a real primary copy hop whose
// input has received a stream longer than the probe window and then gone quiet.
func startQuietPrimary(t *testing.T) (e *Engine, primary *sourceFeed, advance func()) {
	t.Helper()
	ffmpegBin, _ := offsetBenchTools(t)
	fixture := quietStopFixture(t, ffmpegBin)
	e = failoverEngine(t)
	e.tools.FFmpeg = ffmpegBin
	s := failoverOnSettings()
	setSettings(e, s)
	t0 := time.Now()
	e.reconcileSelector(s, wantSelector(s), "")
	hub := e.selectorHub()
	if hub == nil {
		t.Fatal("the selector tier did not start")
	}
	t.Cleanup(func() {
		e.selMu.Lock()
		defer e.selMu.Unlock()
		_ = e.teardownFeed(e.sel.feed)
		_ = hub.Close()
	})
	e.deliver(sourcePrimary, t0)
	e.step(s, t0)
	e.mu.RLock()
	primary = e.sel.feed
	e.mu.RUnlock()
	if primary == nil || primary.kind != sourcePrimary {
		t.Fatalf("the primary's copy hop did not go on air (feed %+v)", primary)
	}
	publishAndLeave(t, ffmpegBin, fixture, e.hub.Port())
	return e, primary, func() { e.step(s, t0.Add(20*time.Second)) }
}

// A switch away from a copy hop whose input has gone quiet must take the hop
// down cleanly INSIDE the seam, through the production path: real FFmpeg, the
// engine's own startFeed (relayConsumerSpec, its WakeOnStop) and retireFeed.
//
// THE DEFECT (#126, the teardown half). retireFeed waited seamStopWait (500ms)
// and then cut the hop's relay subscription, but the supervisor's wake for a
// stopping child on a quiet feed fires at supervisor.WakeAfter (750ms), and
// relay.Hub.Wake sends to a subscriber that no longer exists. So the quiet hop
// was never woken: it was detached at 501ms, SIGKILLed at 8.011s with no final
// progress block, and a teardown right after the switch (a settings reconcile,
// shutdown) waited 7.5s on it. Measured with this test before the change.
func TestASwitchAwayFromAQuietRealFeedExitsCleanlyInsideTheSeam(t *testing.T) {
	e, primary, advance := startQuietPrimary(t)

	began := time.Now()
	advance() // the real decision path: primary quiet past grace -> slate
	took := time.Since(began)

	e.mu.RLock()
	cur := e.sel.feed
	e.mu.RUnlock()
	if cur == nil || cur.kind != sourceSlate {
		t.Fatalf("the switch under test did not happen (feed %+v)", cur)
	}
	// Bound: the wake lands at WakeAfter and FFmpeg is reaped within a beat.
	if bound := seamStopWait + 500*time.Millisecond; took > bound {
		t.Errorf("the switch took %s (bound %s)", took, bound)
	}
	// The hop exited INSIDE the seam: not detached, not still running.
	if feedRunning(primary) {
		t.Fatalf("the outgoing hop was still running when the switch returned (took %s); "+
			"it was cut off before the wake could reach it", took)
	}
	// And FFmpeg flushed: the final progress block the seam ledger reads.
	if !primary.proc.Status().Progress.Done {
		t.Error("the outgoing hop left no final progress block; it was killed, not stopped")
	}

	// Nothing is left behind for the next teardown to wait on.
	b2 := time.Now()
	e.selMu.Lock()
	err := e.teardownFeed(cur)
	e.selMu.Unlock()
	if d := time.Since(b2); d > 2*time.Second || err != nil {
		t.Errorf("tearing the replacement down right after the switch took %s (err %v); "+
			"it waited on the previous hop", d, err)
	}
}

// The fix adds NO input read timeout, and this is why that matters: a consumer
// reads its relay through quiet patches on purpose, and the engine decides a
// source is lost from the hub's byte counters against graceSeconds -- not from a
// child's read erroring. A udp timeout= (or -rw_timeout) shorter than the
// longest tolerated grace would make a merely slow or bursty feed's hop exit and
// be respawned with a fresh timestamp offset, a false "input lost".
func TestARelayConsumerInputCarriesNoReadTimeout(t *testing.T) {
	joined := strings.Join(ffmpeg.RelayConsumerInput("udp://127.0.0.1:5000", ffmpeg.RelayQueue), " ")
	for _, bad := range []string{"timeout=", "rw_timeout"} {
		if strings.Contains(joined, bad) {
			t.Errorf("RelayConsumerInput carries %q: %s", bad, joined)
		}
	}
}

// A bursty feed: the publisher leaves and the hop must ride the quiet patch out
// -- still running seconds later, not exited on a read error.
func TestARealFeedRidesThroughAQuietPatchWithoutExiting(t *testing.T) {
	_, primary, _ := startQuietPrimary(t)
	time.Sleep(4 * time.Second)
	if !feedRunning(primary) {
		t.Fatal("the copy hop exited during a quiet patch shorter than the failover grace")
	}
}
