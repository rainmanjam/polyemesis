//go:build !windows

package engine

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/relay"
)

// wakeProbe stands in for the relay's Hub.Wake, as the feed's wake reaches it.
// It calls the real Wake, so FFmpeg is really woken, and records what a wake
// found: whether the hop was still subscribed (the property the fix is about --
// a wake that finds no subscriber reaches nothing) and whether the relay
// reported it delivered. lose drops the first N wakes on the floor while
// reporting them delivered, which is the lost-datagram case the supervisor's
// repeat exists for.
type wakeProbe struct {
	mu         sync.Mutex
	lose       int
	attempts   int
	delivered  int
	subscribed []bool
	firstAt    time.Time
}

func (p *wakeProbe) wake(hub *relay.Hub, name string) bool {
	p.mu.Lock()
	p.attempts++
	if p.firstAt.IsZero() {
		p.firstAt = time.Now()
	}
	lost := p.attempts <= p.lose
	p.mu.Unlock()
	if lost {
		return true
	}
	sub := slices.Contains(hub.Subscribers(), name)
	ok := hub.Wake(name)
	p.mu.Lock()
	defer p.mu.Unlock()
	if ok {
		p.delivered++
		p.subscribed = append(p.subscribed, sub)
	}
	return ok
}

// installWakeProbe routes every feed's wake through a probe for the test. It
// must run before the feed is started: startFeed reads hubWake once.
func installWakeProbe(t *testing.T, lose int) *wakeProbe {
	t.Helper()
	p := &wakeProbe{lose: lose}
	prev := hubWake
	hubWake = p.wake
	t.Cleanup(func() { hubWake = prev })
	return p
}

// startQuietPrimary brings the selector up with a real primary copy hop whose
// input has received a stream longer than the probe window and then gone quiet.
func startQuietPrimary(t *testing.T, probe *wakeProbe) (e *Engine, primary *sourceFeed, advance func()) {
	t.Helper()
	_ = probe // installed by the caller before this starts the feed
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
// THE DEFECT (#126, the teardown half). retireFeed waited a fixed 500ms and
// then cut the hop's relay subscription, but the supervisor's wake for a
// stopping child on a quiet feed fires at supervisor.WakeAfter (750ms), and
// relay.Hub.Wake sends to a subscriber that no longer exists. So the quiet hop
// was never woken: it was detached at 501ms, SIGKILLed at 8.011s with no final
// progress block, and a teardown right after the switch (a settings reconcile,
// shutdown) waited 7.5s on it. Measured with this test before the change.
//
// THE PROPERTY, NOT THE MARGIN: a wake reached the hop while it was still
// subscribed, the hop flushed its final progress block, and the switch stayed
// inside a loose bound. The wait is driven by the wake being delivered, so no
// constant has to out-run scheduling for this to hold under load.
func TestASwitchAwayFromAQuietRealFeedExitsCleanlyInsideTheSeam(t *testing.T) {
	probe := installWakeProbe(t, 0)
	e, primary, advance := startQuietPrimary(t, probe)

	began := time.Now()
	advance() // the real decision path: primary quiet past grace -> slate
	took := time.Since(began)
	t.Logf("quiet switch took %s", took.Round(time.Millisecond))

	e.mu.RLock()
	cur := e.sel.feed
	e.mu.RUnlock()
	if cur == nil || cur.kind != sourceSlate {
		t.Fatalf("the switch under test did not happen (feed %+v)", cur)
	}
	// Loose on purpose: the property below is the assertion, this only catches
	// a switch that waits out something much longer than the wake schedule.
	if bound := 2500 * time.Millisecond; took > bound {
		t.Errorf("the switch took %s (loose bound %s)", took, bound)
	}
	probe.mu.Lock()
	delivered, subscribed := probe.delivered, slices.Clone(probe.subscribed)
	probe.mu.Unlock()
	if delivered == 0 {
		t.Fatal("no wake reached the outgoing hop before the switch returned; it was cut off " +
			"from its input before the supervisor woke it")
	}
	if slices.Contains(subscribed, false) {
		t.Errorf("a wake was delivered to a hop that was no longer subscribed: %v", subscribed)
	}
	// The hop exited INSIDE the seam: not detached, not still running.
	if feedRunning(primary) {
		t.Fatalf("the outgoing hop was still running when the switch returned (took %s)", took)
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

// The supervisor repeats the wake (every WakeEvery after the first) because a
// loopback datagram can be dropped, and only a wake that reaches a real packet
// frees the hop. So the switch must not cut the input after the FIRST wake:
// here the first wake is lost on the way, the repeat frees the hop, and the
// switch returns before the hard cap with the hop stopped, not killed. A switch
// that cut the input a grace after the first wake would detach the hop and cost
// it the 8s SIGKILL.
func TestASwitchRecoversAQuietRealFeedWhoseFirstWakeWasLost(t *testing.T) {
	probe := installWakeProbe(t, 1)
	e, primary, advance := startQuietPrimary(t, probe)

	began := time.Now()
	advance()
	took := time.Since(began)
	t.Logf("quiet switch with the first wake lost took %s", took.Round(time.Millisecond))

	e.mu.RLock()
	cur := e.sel.feed
	e.mu.RUnlock()
	if cur == nil || cur.kind != sourceSlate {
		t.Fatalf("the switch under test did not happen (feed %+v)", cur)
	}
	probe.mu.Lock()
	attempts, delivered := probe.attempts, probe.delivered
	probe.mu.Unlock()
	if attempts < 2 {
		t.Fatalf("only %d wake(s) were sent; the repeat is what this test is about", attempts)
	}
	if delivered == 0 {
		t.Fatal("the repeat wake never reached the hop")
	}
	if took >= seamStopCap {
		t.Errorf("the switch took %s, not before the hard cap %s; the repeat did not free the hop",
			took, seamStopCap)
	}
	if feedRunning(primary) {
		t.Fatalf("the outgoing hop was cut off before the repeat wake could free it (took %s)", took)
	}
	if !primary.proc.Status().Progress.Done {
		t.Error("the hop left no final progress block; it was killed, not stopped")
	}
	e.selMu.Lock()
	_ = e.teardownFeed(cur)
	e.selMu.Unlock()
}

// The fix adds NO input read timeout, and this is why that matters: a consumer
// reads its relay through quiet patches on purpose, and the engine decides a
// source is lost from the hub's byte counters against graceSeconds -- not from a
// child's read erroring. A udp timeout= (or -rw_timeout) shorter than the
// longest tolerated grace would make a merely slow or bursty feed's hop exit and
// be respawned with a fresh timestamp offset, a false "input lost".
func TestARelayConsumerInputCarriesNoReadTimeout(t *testing.T) {
	// Any argument naming a timeout, in any spelling (timeout=, -rw_timeout,
	// -stimeout, -timeout): case-insensitive and anywhere in the input args.
	for _, a := range ffmpeg.RelayConsumerInput("udp://127.0.0.1:5000", ffmpeg.RelayQueue) {
		if strings.Contains(strings.ToLower(a), "timeout") {
			t.Errorf("RelayConsumerInput carries a timeout argument %q", a)
		}
	}
}

// A bursty feed: the publisher leaves and the hop must ride the quiet patch out
// -- still running seconds later, not exited on a read error.
func TestARealFeedRidesThroughAQuietPatchWithoutExiting(t *testing.T) {
	_, primary, _ := startQuietPrimary(t, nil)
	time.Sleep(4 * time.Second)
	if !feedRunning(primary) {
		t.Fatal("the copy hop exited during a quiet patch shorter than the failover grace")
	}
}
