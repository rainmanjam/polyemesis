//go:build !windows

package engine

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// A failover away from a DEAD primary used to take the grace period plus eight
// seconds, and the eight seconds then reappeared as a forward jump in the
// published timeline at the next switch. One mechanism, two symptoms.
//
// The outgoing feed is a copy hop reading the primary's relay. When the primary
// dies that relay goes quiet, and an FFmpeg blocked in a read of a quiet UDP
// input does not answer SIGTERM: it sits there until the supervisor's grace
// period runs out and it is killed. ensureFeed waited for all of that before it
// started the replacement, so:
//
//   - the switch landed ~8 s after the decision (measured: 11.5 s against a 3 s
//     graceSeconds, `feed seam teardownMs≈8003`), and
//   - the incoming feed's -output_ts_offset, stamped at the decision, was ~8 s
//     behind wall clock by the time it published, so its whole timeline lagged
//     by the teardown and the NEXT switch repaid it as an 8 s forward jump.
//
// This test stands the wedge up for real: the fake FFmpeg below ignores SIGTERM
// whenever it is started as a copy hop, exactly as the real one does on a quiet
// input, and answers it at once when it is the slate.

// wedgedFeedBinary writes a stand-in for FFmpeg that ignores SIGTERM when its
// argv is a relay copy hop (`-c copy`) and exits on it otherwise. It returns
// the binary and a marker file the copy hop creates once its trap is in place:
// a TERM that lands before the trap would kill it, and the test would measure
// a switch away from a healthy feed instead of a wedged one.
func wedgedFeedBinary(t *testing.T) (bin, armed string) {
	t.Helper()
	dir := t.TempDir()
	bin, armed = filepath.Join(dir, "ffmpeg"), filepath.Join(dir, "armed")
	// SIG_IGN survives exec, so the sleep inherits the ignored TERM.
	script := "#!/bin/sh\ncase \" $* \" in *\" copy \"*) trap '' TERM; : > '" + armed + "';; esac\nexec sleep 60\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, armed
}

func TestASwitchAwayFromAWedgedFeedDoesNotWaitOutItsGracePeriod(t *testing.T) {
	e := failoverEngine(t)
	bin, armed := wedgedFeedBinary(t)
	e.tools.FFmpeg = bin
	var buf syncBuffer
	e.log = slog.New(slog.NewTextHandler(&buf, nil))

	s := failoverOnSettings()
	setSettings(e, s)

	t0 := time.Now()
	e.reconcileSelector(s, wantSelector(s), "")
	hub := e.selectorHub()
	if hub == nil {
		t.Fatal("the selector tier did not start")
	}
	t.Cleanup(func() {
		// The cleanup's own stop must not wait on the wedge either.
		prev := stopTimeout
		stopTimeout = 100 * time.Millisecond
		defer func() { stopTimeout = prev }()
		e.selMu.Lock()
		defer e.selMu.Unlock()
		_ = e.teardownFeed(e.sel.feed)
		_ = hub.Close()
	})

	e.deliver(sourcePrimary, t0)
	e.step(s, t0)
	e.mu.RLock()
	primary := e.sel.feed
	e.mu.RUnlock()
	if primary == nil || primary.kind != sourcePrimary || primary.in == nil {
		t.Fatalf("the primary's copy hop did not go on air (feed %+v)", primary)
	}
	waitUntil(t, func() bool { _, err := os.Stat(armed); return err == nil },
		"the primary's copy hop to be running with SIGTERM ignored")
	in := primary.in

	// The primary goes quiet past the grace period. Its copy hop is now the
	// wedged reader the field saw.
	buf.Reset()
	began := time.Now()
	e.step(s, t0.Add(20*time.Second))
	took := time.Since(began)

	e.mu.RLock()
	active, inFeed := e.sel.active, e.sel.feed
	e.mu.RUnlock()
	if active != sourceSlate || inFeed == nil {
		t.Fatalf("active = %q, feed %v; the switch under test did not happen", active, inFeed != nil)
	}

	// Row 17: the switch is the decision plus a short, bounded wait -- not the
	// decision plus the outgoing child's whole grace period.
	if took > 3*time.Second {
		t.Errorf("the switch away from a wedged feed took %s; it waited out the outgoing "+
			"child's grace period instead of starting the replacement", took.Round(time.Millisecond))
	}

	// Row 18: teardownMs is the lag between the incoming feed's offset being
	// stamped and it starting to publish, which is the jump the next switch
	// repays. It must be bounded by the seam wait, not by the grace period.
	lines := seamLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("one switch wrote %d seam lines:\n%s", len(lines), buf.String())
	}
	if ms := seamFloat(t, lines[0], "teardownMs"); ms > float64(seamStopWait.Milliseconds())+500 {
		t.Errorf("teardownMs = %.0f: the incoming timeline starts that far behind wall clock, "+
			"and the next switch jumps forward by it", ms)
	}
	if lines[0]["outDetached"] != "true" {
		t.Errorf("outDetached = %q; a seam that started the replacement while the old child "+
			"was still exiting must say so in the ledger", lines[0]["outDetached"])
	}

	// The safety half. The replacement started while the old child was still
	// alive, which is only sound because that child had already been cut off
	// from its input: it has nothing left to publish into the selector.
	if slices.Contains(in.Subscribers(), selectorSubName) {
		t.Error("the outgoing copy hop is still subscribed to its input; it can keep " +
			"publishing into the selector beside its replacement")
	}

	// And the old child is still collected, and its port given back, once it
	// is killed -- in the background, not on the switch's time.
	deadline := time.Now().Add(15 * time.Second)
	for feedRunning(primary) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if feedRunning(primary) {
		t.Fatal("the wedged outgoing feed was never stopped")
	}
	waitUntil(t, func() bool {
		e.heldMu.Lock()
		defer e.heldMu.Unlock()
		_, held := e.heldPorts[primary.port]
		return !held
	}, "the outgoing feed's port to be released after it was killed")
}

// healthyFeedBinary writes a stand-in for FFmpeg whose copy hop DOES answer
// SIGTERM, the way a real one does while its input is still delivering: it
// leaves a marker the moment the signal lands, takes a beat to "flush", and
// exits. The slate exits on TERM at once.
//
// The marker is what lets the test ask the ORDER question. manager.go measured
// that an FFmpeg given SIGTERM on an input that has already gone silent is still
// alive fifteen seconds later, while one signalled with packets arriving exits in
// 0.105 s -- so cutting a healthy copy hop's input before signalling it is what
// turns a clean switch into a wedged one. At the instant the signal lands, the
// copy hop must still be subscribed.
func healthyFeedBinary(t *testing.T) (bin, armed, termed string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "ffmpeg")
	armed, termed = filepath.Join(dir, "armed"), filepath.Join(dir, "termed")
	// Not exec: the trap has to stay with this shell, so the sleep runs in the
	// background and the shell waits on it, which a TERM interrupts.
	script := "#!/bin/sh\n" +
		"case \" $* \" in\n" +
		"*\" copy \"*) trap ': > \"" + termed + "\"; sleep 0.2; kill $! 2>/dev/null; exit 0' TERM; : > '" + armed + "';;\n" +
		"*) trap 'kill $! 2>/dev/null; exit 0' TERM;;\n" +
		"esac\n" +
		"sleep 60 &\nwait\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, armed, termed
}

// A switch away from a copy hop that is still healthy -- recovery, a pin, a
// respawn, or the moment a quiet source crosses its grace while its hop is still
// draining -- must signal it while its input is still delivering, and must not
// come out of it detached. Cutting the input first is what wedges it.
func TestASwitchAwayFromAHealthyFeedSignalsItBeforeCuttingItsInput(t *testing.T) {
	e := failoverEngine(t)
	bin, armed, termed := healthyFeedBinary(t)
	e.tools.FFmpeg = bin
	var buf syncBuffer
	e.log = slog.New(slog.NewTextHandler(&buf, nil))

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
	primary := e.sel.feed
	e.mu.RUnlock()
	if primary == nil || primary.kind != sourcePrimary || primary.in == nil {
		t.Fatalf("the primary's copy hop did not go on air (feed %+v)", primary)
	}
	waitUntil(t, func() bool { _, err := os.Stat(armed); return err == nil },
		"the primary's copy hop to be running with its TERM trap in place")
	in := primary.in

	// Watch for the signal landing, and look at the subscription at that moment.
	subscribedAtTerm := make(chan bool, 1)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := os.Stat(termed); err == nil {
				subscribedAtTerm <- slices.Contains(in.Subscribers(), selectorSubName)
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	defer close(stop)

	buf.Reset()
	e.step(s, t0.Add(20*time.Second))

	select {
	case sub := <-subscribedAtTerm:
		if !sub {
			t.Error("the outgoing copy hop was cut off from its input before it was sent " +
				"SIGTERM; an FFmpeg signalled on a silent input does not exit, so every switch " +
				"away from a healthy feed becomes a wedged one")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the outgoing copy hop was never signalled")
	}

	lines := seamLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("one switch wrote %d seam lines:\n%s", len(lines), buf.String())
	}
	if lines[0]["outDetached"] != "false" {
		t.Errorf("outDetached = %q for a feed that exits on SIGTERM; the ledger reports a "+
			"wedge that did not happen", lines[0]["outDetached"])
	}
	if ms := seamFloat(t, lines[0], "teardownMs"); ms >= float64(seamStopWait.Milliseconds()) {
		t.Errorf("teardownMs = %.0f for a feed that exits 0.2 s after SIGTERM; the switch "+
			"paid the whole seam wait", ms)
	}
	// And after the stop, the subscription is gone -- signalling first must not
	// mean never cutting.
	if slices.Contains(in.Subscribers(), selectorSubName) {
		t.Error("the outgoing copy hop is still subscribed after the switch")
	}
}

// A flap inside the old child's kill window. The first switch leaves the wedged
// primary hop dying in the background and hands its done channel to the slate
// as prev. Switching straight back must cost the SLATE's stop -- which is
// immediate -- and not the predecessor's remaining seconds: the predecessor is
// carried forward to the next feed, not waited on at the seam.
func TestAQuickSwitchBackDoesNotWaitForTheFeedBeforeLast(t *testing.T) {
	e := failoverEngine(t)
	bin, armed := wedgedFeedBinary(t)
	e.tools.FFmpeg = bin
	var buf syncBuffer
	e.log = slog.New(slog.NewTextHandler(&buf, nil))

	s := failoverOnSettings()
	setSettings(e, s)

	t0 := time.Now()
	e.reconcileSelector(s, wantSelector(s), "")
	hub := e.selectorHub()
	if hub == nil {
		t.Fatal("the selector tier did not start")
	}
	t.Cleanup(func() {
		prev := stopTimeout
		stopTimeout = 100 * time.Millisecond
		defer func() { stopTimeout = prev }()
		e.selMu.Lock()
		defer e.selMu.Unlock()
		_ = e.teardownFeed(e.sel.feed)
		_ = hub.Close()
	})

	e.deliver(sourcePrimary, t0)
	e.step(s, t0)
	e.mu.RLock()
	first := e.sel.feed
	e.mu.RUnlock()
	if first == nil || first.kind != sourcePrimary {
		t.Fatalf("the primary did not go on air (feed %+v)", first)
	}
	waitUntil(t, func() bool { _, err := os.Stat(armed); return err == nil },
		"the primary's copy hop to be running with SIGTERM ignored")

	// Primary goes quiet: away to the slate, leaving the wedged hop behind.
	e.step(s, t0.Add(20*time.Second))
	e.mu.RLock()
	slate := e.sel.feed
	e.mu.RUnlock()
	if slate == nil || slate.kind != sourceSlate || slate.prev == nil {
		t.Fatalf("the first switch did not detach onto the slate (feed %+v)", slate)
	}

	// The primary is back at once, while the first hop is still dying.
	if !feedRunning(first) {
		t.Fatal("the first hop was already gone, so this would measure nothing")
	}
	buf.Reset()
	back := t0.Add(21 * time.Second)
	e.deliver(sourcePrimary, back)
	e.step(s, back)
	e.mu.RLock()
	active, cur := e.sel.active, e.sel.feed
	e.mu.RUnlock()
	if active != sourcePrimary || cur == nil {
		t.Fatalf("active = %q; the switch back under test did not happen", active)
	}
	lines := seamLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("one switch wrote %d seam lines:\n%s", len(lines), buf.String())
	}
	if ms := seamFloat(t, lines[0], "teardownMs"); ms >= float64(seamStopWait.Milliseconds()) {
		t.Errorf("teardownMs = %.0f: stopping a slate that exits at once waited on the "+
			"feed before it, still dying from the previous switch", ms)
	}
	if lines[0]["outDetached"] != "false" {
		t.Errorf("outDetached = %q for a slate that exited on SIGTERM", lines[0]["outDetached"])
	}
	// The predecessor is not dropped: the new feed carries it, so the next
	// teardown still collects its process and port.
	if cur.prev == nil {
		t.Error("the feed before last is no longer tracked by anything; shutdown would " +
			"not wait for it and would report its port as leaked")
	}
}
