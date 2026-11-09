//go:build !windows

package engine

import (
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/relay"
)

// These tests drive retireFeed's wake-driven wait and startFeed's WakeOnStop
// wrapper without FFmpeg: the hop is a shell stand-in and the relay's Hub.Wake is
// replaced through the hubWake seam, so a wake can be delivered, lost, or made to
// release the hop, on demand. The real-FFmpeg versions of the same property are
// in seam_quiet_feed_integration_test.go; they skip where FFmpeg is absent, and
// these do not.

// releasableFeedBinary writes a stand-in FFmpeg whose copy hop ignores SIGTERM
// and keeps running until the returned release file exists, as a hop reading a
// quiet input does until something wakes it. The slate exits on SIGTERM. armed
// appears once the hop's trap is in place.
func releasableFeedBinary(t *testing.T) (bin, armed, release string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "ffmpeg")
	armed, release = filepath.Join(dir, "armed"), filepath.Join(dir, "release")
	script := "#!/bin/sh\n" +
		"case \" $* \" in\n" +
		"*\" copy \"*) trap '' TERM; : > '" + armed + "'; " +
		"while [ ! -e '" + release + "' ]; do sleep 0.02; done; exit 0;;\n" +
		"*) trap 'exit 0' TERM; while :; do sleep 0.02; done;;\n" +
		"esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, armed, release
}

// quietHop is a running primary copy hop whose input the test controls.
type quietHop struct {
	e       *Engine
	primary *sourceFeed
	in      *relay.Hub
	release string
	// retire runs the real switch away from the hop (primary quiet past its
	// grace, so the slate takes over) and returns how long it took.
	retire func() time.Duration
}

// startQuietHop brings the selector up with the stand-in as the primary's hop.
// wake replaces Hub.Wake; it is installed before the feed starts, because
// startFeed reads hubWake once. The hop is released and everything stopped when
// the test ends.
func startQuietHop(t *testing.T, wake func(*relay.Hub, string) bool) *quietHop {
	t.Helper()
	prevWake := hubWake
	hubWake = wake
	t.Cleanup(func() { hubWake = prevWake })

	bin, armed, release := releasableFeedBinary(t)
	e := failoverEngine(t)
	e.tools.FFmpeg = bin
	s := failoverOnSettings()
	setSettings(e, s)

	t0 := time.Now()
	e.reconcileSelector(s, wantSelector(s), "")
	hub := e.selectorHub()
	if hub == nil {
		t.Fatal("the selector tier did not start")
	}
	h := &quietHop{e: e, release: release}
	t.Cleanup(func() {
		// Let a hop that is still held exit, then stop whatever is on air. The
		// release file is the only thing a stand-in hop waits for.
		_ = os.WriteFile(release, nil, 0o644)
		e.selMu.Lock()
		defer e.selMu.Unlock()
		_ = e.teardownFeed(e.sel.feed)
		if h.primary != nil {
			waitUntil(t, func() bool { return !feedRunning(h.primary) }, "the outgoing hop to exit")
		}
		_ = hub.Close()
	})

	e.deliver(sourcePrimary, t0)
	e.step(s, t0)
	e.mu.RLock()
	h.primary = e.sel.feed
	e.mu.RUnlock()
	if h.primary == nil || h.primary.kind != sourcePrimary || h.primary.in == nil {
		t.Fatalf("the primary's copy hop did not go on air (feed %+v)", h.primary)
	}
	h.in = h.primary.in
	waitUntil(t, func() bool { _, err := os.Stat(armed); return err == nil },
		"the copy hop to be running with SIGTERM ignored")
	h.retire = func() time.Duration {
		began := time.Now()
		e.step(s, t0.Add(20*time.Second))
		took := time.Since(began)
		e.mu.RLock()
		cur := e.sel.feed
		e.mu.RUnlock()
		if cur == nil || cur.kind != sourceSlate {
			t.Fatalf("the switch under test did not happen (feed %+v)", cur)
		}
		return took
	}
	return h
}

// The wrapper startFeed puts around WakeOnStop exists only for a feed with a
// relay subscription. The slate has none: no woken channel, and the spec's wake
// is whatever relayConsumerSpec gave it.
func TestStartFeedReportsWakesOnlyForAFeedWithARelaySubscription(t *testing.T) {
	h := startQuietHop(t, func(*relay.Hub, string) bool { return true })
	if h.primary.woken == nil {
		t.Fatal("a copy hop with a relay subscription has no woken channel; retireFeed could never see a wake")
	}
	h.retire()
	h.e.mu.RLock()
	slate := h.e.sel.feed
	h.e.mu.RUnlock()
	if slate.in != nil || slate.woken != nil {
		t.Errorf("the slate has no relay subscription but got a woken channel (in=%v woken=%v)",
			slate.in != nil, slate.woken != nil)
	}
}

// A wake the relay reports delivered is what retireFeed waits on: the first
// releases the hop's held packet, the repeat covers a dropped first, and only
// then does the post-wake grace run and the input get cut. A hop that never
// answers is detached, unsubscribed, and still collected afterwards.
func TestRetireFeedWaitsForTheFirstWakeAndItsRepeatThenCutsTheInput(t *testing.T) {
	var calls atomic.Int64
	var subscribedAtWake atomic.Bool
	subscribedAtWake.Store(true)
	h := startQuietHop(t, func(hub *relay.Hub, name string) bool {
		calls.Add(1)
		if !slices.Contains(hub.Subscribers(), name) {
			subscribedAtWake.Store(false)
		}
		return true
	})

	took := h.retire()
	t.Logf("wake-driven wait took %s over %d wakes", took.Round(time.Millisecond), calls.Load())

	if n := calls.Load(); n < seamWakesBeforeCut {
		t.Errorf("%d wakes reached the hop before its input was cut, want at least %d (the first and its repeat)",
			n, seamWakesBeforeCut)
	}
	if !subscribedAtWake.Load() {
		t.Error("a wake found the hop already unsubscribed; Hub.Wake reaches nothing then")
	}
	// Loose on both sides: not before the first wake could have been sent, and
	// well inside the cap plus a loaded runner's slack.
	if took < 500*time.Millisecond {
		t.Errorf("the wait ended after %s, before a wake could have landed", took)
	}
	if took > seamStopCap+time.Second {
		t.Errorf("the wait took %s, past the hard cap %s", took, seamStopCap)
	}
	if slices.Contains(h.in.Subscribers(), selectorSubName) {
		t.Error("the wedged hop is still subscribed after the wait; it can publish beside its replacement")
	}
	if !feedRunning(h.primary) {
		t.Error("the hop exited, so this did not exercise the detach path")
	}
}

// A wake that Hub.Wake reports as not delivered (nothing subscribed, the feed
// not quiet) is not counted: retireFeed has nothing to wait on and runs to the
// hard cap.
func TestRetireFeedRunsToTheHardCapWhenNoWakeIsDelivered(t *testing.T) {
	var calls atomic.Int64
	h := startQuietHop(t, func(*relay.Hub, string) bool {
		calls.Add(1)
		return false
	})

	took := h.retire()
	t.Logf("undelivered-wake wait took %s over %d wake attempts", took.Round(time.Millisecond), calls.Load())

	if calls.Load() == 0 {
		t.Error("the supervisor never attempted a wake")
	}
	if took < seamStopCap-50*time.Millisecond {
		t.Errorf("the wait ended after %s with no wake delivered, before the hard cap %s", took, seamStopCap)
	}
	if took > seamStopCap+time.Second {
		t.Errorf("the wait took %s, well past the hard cap %s", took, seamStopCap)
	}
	if len(h.primary.woken) != 0 {
		t.Error("an undelivered wake was reported on the woken channel")
	}
	if slices.Contains(h.in.Subscribers(), selectorSubName) {
		t.Error("the hop is still subscribed after the cap")
	}
}

// The wake releases the hop: it exits while the wait is open, so the switch
// returns on the hop's exit, not on the grace or the cap, and nothing is
// detached.
func TestRetireFeedReturnsWhenTheWakeReleasesTheHop(t *testing.T) {
	var release string
	h := startQuietHop(t, func(*relay.Hub, string) bool {
		_ = os.WriteFile(release, nil, 0o644)
		return true
	})
	release = h.release // read by the wake only after retire starts the supervisor's timers

	took := h.retire()
	t.Logf("wake-released hop: switch took %s", took.Round(time.Millisecond))

	if took >= seamStopCap {
		t.Errorf("the switch took %s; the hop exited on the wake but the wait ran to the cap %s", took, seamStopCap)
	}
	if feedRunning(h.primary) {
		t.Error("the hop is still running after a switch that was released by the wake")
	}
	if slices.Contains(h.in.Subscribers(), selectorSubName) {
		t.Error("the hop's subscription was not given back after it exited")
	}
}

// A later wake pushes the post-wake grace out rather than leaving it to fire on
// the first schedule. The test hands retireFeed the wake signals directly (the
// channel the wrapper writes to) so the spacing is its own, not the
// supervisor's.
func TestRetireFeedExtendsThePostWakeGraceOnALaterWake(t *testing.T) {
	// Wakes from the supervisor are suppressed: only the test's signals count.
	h := startQuietHop(t, func(*relay.Hub, string) bool { return false })

	done := make(chan time.Duration, 1)
	began := time.Now()
	go func() { done <- h.retire() }()

	h.primary.woken <- struct{}{} // the first wake
	h.primary.woken <- struct{}{} // its repeat: the grace starts now
	time.Sleep(100 * time.Millisecond)
	sentAt := time.Since(began)
	h.primary.woken <- struct{}{} // a later wake: the grace restarts from here

	took := <-done
	t.Logf("grace extended by a wake sent at %s: switch took %s",
		sentAt.Round(time.Millisecond), took.Round(time.Millisecond))

	// Only assert the extension when the later wake was sent comfortably inside
	// the first grace window; on a stalled runner it may have missed it, and then
	// the grace legitimately ran out on the first schedule.
	if sentAt < seamPostWakeGrace-100*time.Millisecond && took < sentAt+seamPostWakeGrace-30*time.Millisecond {
		t.Errorf("the switch took %s, but a wake at %s should have held the cut until %s",
			took, sentAt, sentAt+seamPostWakeGrace)
	}
	if took >= seamStopCap {
		t.Errorf("the switch took %s; the grace ended it, not the cap %s", took, seamStopCap)
	}
	if slices.Contains(h.in.Subscribers(), selectorSubName) {
		t.Error("the hop is still subscribed after the grace")
	}
}
