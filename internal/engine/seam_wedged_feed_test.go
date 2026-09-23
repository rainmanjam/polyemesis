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
