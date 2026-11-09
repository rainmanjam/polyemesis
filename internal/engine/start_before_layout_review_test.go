package engine

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/relay"
)

// srtStoreEngine is a store-backed engine whose source is SRT, so Reconcile
// owns no ingest child and the only thing that can reset the layout state is
// the mode comparison under test.
func srtStoreEngine(t *testing.T) (*Engine, *db.DB, *startCounter) {
	t.Helper()
	e, store := storeEngine(t)
	standInFFmpeg(t, e)
	e.alloc = relay.NewPortAllocator(freeUDPPort(t), 8)
	counter := &startCounter{}
	e.log = slog.New(counter)
	src, err := store.GetSource(e.sourceID)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	src.Ingest.Mode = db.IngestSRT
	if err := store.UpdateSource(src); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	if _, err := store.CreateDestination(&db.Destination{
		Name: "YouTube", Kind: db.DestRTMP, URL: "rtmp://example.invalid/live",
		StreamKey: "key", Enabled: true, SourceID: &e.sourceID,
	}); err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	t.Cleanup(func() {
		e.reconcileMu.Lock()
		defer e.reconcileMu.Unlock()
		e.stopDestinations(map[int64]destPlan{})
	})
	return e, store, counter
}

func runningDests(e *Engine) int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	n := 0
	for _, d := range e.dests {
		if d != nil && d.proc != nil {
			n++
		}
	}
	return n
}

func reconcileOrFail(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

// A SECOND START PRESS AFTER THE GIVE-UP MUST NOT TEAR DOWN THE PROVISIONAL
// DESTINATION, AND MUST NOT DISCARD A PROBE IN FLIGHT.
//
// reconcileIngest compared measuredMode with the new mode, and measuredMode is
// IngestUnset until a layout is measured, so every Reconcile before the first
// probe landed reset probeFails and bumped sourceGen. The give-up was erased,
// the destination the first press let out was re-held with an empty plan, and
// an in-flight probe failed its generation check.
//
// Through the real Reconcile, not reconcileOutputs: the reset lives above it.
//
// Mutation: engine.go reconcileIngest, `modeChanged := prev.Ingest.Mode !=
// s.Ingest.Mode || (...)` -> `modeChanged := e.measuredMode != s.Ingest.Mode`.
func TestASecondStartPressAfterTheGiveUpKeepsTheProvisionalDestination(t *testing.T) {
	e, _, counter := srtStoreEngine(t)
	reconcileOrFail(t, e) // boot: nothing measured, nothing seen, held

	e.probeFails.Store(probeGiveUp)
	reconcileOrFail(t, e) // the first press: provisional start
	if n := runningDests(e); n != 1 {
		t.Fatalf("setup: %d destinations running after the give-up, want 1", n)
	}

	e.mu.RLock()
	gen := e.sourceGen
	e.mu.RUnlock()

	reconcileOrFail(t, e) // the second press
	reconcileOrFail(t, e)

	if n := runningDests(e); n != 1 {
		t.Errorf("%d destinations running after a second start press, want 1: "+
			"the provisional destination was torn down", n)
	}
	if n := counter.count("YouTube"); n != 1 {
		t.Errorf("YouTube started %d times, want 1", n)
	}
	if got := e.probeFails.Load(); got != probeGiveUp {
		t.Errorf("probeFails = %d after a reconcile that changed no mode, want %d", got, probeGiveUp)
	}
	e.mu.RLock()
	after := e.sourceGen
	e.mu.RUnlock()
	if after != gen {
		t.Errorf("sourceGen moved %d -> %d on a reconcile that changed no mode; "+
			"that discards the probe in flight", gen, after)
	}
}

// AN EXPIRED LATCH DOES NOT SURVIVE A MODE SWITCH. The hold's clock and the
// stream-seen flag belong to the old transport; with no layout measured the
// switch used to clear neither, so destinations kept running provisionally
// before a single probe of the new transport.
//
// Mutation: delete the streamSeen/holdSince clears in reconcileIngest's
// modeChanged block.
func TestAModeSwitchClearsAnExpiredHoldLatch(t *testing.T) {
	e, store, _ := srtStoreEngine(t)
	src, err := store.GetSource(e.sourceID)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	src.Ingest.Mode = db.IngestUnset
	if err := store.UpdateSource(src); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	reconcileOrFail(t, e)
	e.streamSeen.Store(true)
	e.holdSince.Store(time.Now().Add(-2 * holdCeiling).UnixNano())
	reconcileOrFail(t, e)
	if n := runningDests(e); n != 1 {
		t.Fatalf("setup: %d destinations running on an expired latch, want 1", n)
	}

	src.Ingest.Mode = db.IngestSRT
	if err := store.UpdateSource(src); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	reconcileOrFail(t, e)

	if e.streamSeen.Load() || e.holdSince.Load() != 0 {
		t.Errorf("streamSeen=%v holdSince=%d after a mode switch, want both cleared",
			e.streamSeen.Load(), e.holdSince.Load())
	}
	if n := runningDests(e); n != 0 {
		t.Errorf("%d destinations still running provisionally on the new transport, want 0 (held)", n)
	}
}

// probeLoopEngine is srtStoreEngine with the probe loop running.
func probeLoopEngine(t *testing.T) *Engine {
	t.Helper()
	e, _, _ := srtStoreEngine(t)
	reconcileOrFail(t, e)
	if n := runningDests(e); n != 0 {
		t.Fatalf("setup: %d destinations running before any stream, want 0 (held)", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.probeLoop(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return e
}

// feedUntil delivers into a hub every 100ms until stop is closed.
func feedUntil(t *testing.T, h *relay.Hub, stop <-chan struct{}) {
	t.Helper()
	go func() {
		var cc uint8
		tk := time.NewTicker(100 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				h.Deliver(tsDatagram(&cc))
			}
		}
	}()
}

// ANY FEED REACHING THE SELECTOR COUNTS AS A STREAM: the backup, or the slate
// behind the selector. Going live on one of them before the primary connects
// used to hold destinations forever, because only the primary's hub set
// streamSeen; before the ceiling they went out after 75s.
//
// The loop is the real probeLoop, the clock is backdated the way
// TestAStartBeforeTheLayoutIsKnown does it, and no Reconcile is called by the
// test after the loop starts: destinations must go out at the ceiling on the
// loop's own initiative, with the primary never having connected.
//
// Mutation: feedWatch.advanced watches only w.e.hub.
func TestABackupOrSlateOnlyStreamStillReleasesTheHoldAtTheCeiling(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, e *Engine) *relay.Hub
	}{
		{"backup", func(t *testing.T, e *Engine) *relay.Hub {
			h, err := relay.New(slog.New(&startCounter{}), freeUDPPort(t))
			if err != nil {
				t.Fatalf("relay.New: %v", err)
			}
			e.mu.Lock()
			e.backup = &backupIngest{hub: h}
			e.mu.Unlock()
			return h
		}},
		{"slate", func(t *testing.T, e *Engine) *relay.Hub {
			h, err := relay.New(slog.New(&startCounter{}), freeUDPPort(t))
			if err != nil {
				t.Fatalf("relay.New: %v", err)
			}
			e.mu.Lock()
			e.sel = &selector{hub: h, active: sourceSlate}
			e.mu.Unlock()
			return h
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := probeLoopEngine(t)
			h := tc.setup(t, e)
			t.Cleanup(func() { _ = h.Close() })
			stop := make(chan struct{})
			t.Cleanup(func() { close(stop) })
			feedUntil(t, h, stop)

			waitFor := func(what string, cond func() bool) {
				t.Helper()
				deadline := time.Now().Add(15 * time.Second)
				for !cond() {
					if time.Now().After(deadline) {
						t.Fatalf("timed out waiting for %s", what)
					}
					time.Sleep(50 * time.Millisecond)
				}
			}
			waitFor("the loop to see the feed", func() bool { return e.streamSeen.Load() })
			waitFor("the hold's clock to start", func() bool { return e.holdSince.Load() != 0 })
			if e.hub.RxBytes() != 0 {
				t.Fatal("the primary carried bytes; this case is about it never connecting")
			}
			if n := runningDests(e); n != 0 {
				t.Fatalf("%d destinations out before the ceiling", n)
			}
			e.holdSince.Store(time.Now().Add(-2 * holdCeiling).UnixNano())
			waitFor("destinations to go out at the ceiling", func() bool { return runningDests(e) == 1 })
		})
	}
}

// THE CEILING IS DRIVEN BY A TIMER, NOT BY WHATEVER RECONCILE ARRIVES. A stream
// that flowed once and went quiet never produced a `changed` or a give-up flip,
// so nothing re-planned when the 75s ran out (#473). Here the primary carries
// one burst and then nothing; the test backdates the clock and calls no
// Reconcile, and the loop must release the hold by itself.
//
// Mutation: delete the `if expiryFlipped { ... }` call in probeLoop.
func TestTheHoldCeilingReleasesDestinationsOnAQuietStreamWithoutAReconcile(t *testing.T) {
	e := probeLoopEngine(t)
	// A burst EVERY round until the loop reacts: a single burst after a fixed
	// sleep becomes the loop's baseline if it lands before the loop took its
	// own, and then nothing ever moves.
	var cc uint8
	deadline := time.Now().Add(15 * time.Second)
	for e.holdSince.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the clock never started after the stream was seen")
		}
		for range 20 {
			e.hub.Deliver(tsDatagram(&cc))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := runningDests(e); n != 0 {
		t.Fatalf("%d destinations out before the ceiling", n)
	}
	e.holdSince.Store(time.Now().Add(-2 * holdCeiling).UnixNano())
	deadline = time.Now().Add(15 * time.Second)
	for runningDests(e) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the ceiling expired on a quiet stream and nothing re-planned")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A STREAM-SEEN FLAG FROM A TICK THAT BEGAN ON THE OLD INGEST IS REFUSED.
// Mutation: drop the `e.sourceGen != gen` check in noteStreamSeen.
func TestAStaleTickDoesNotSetStreamSeenAfterTheIngestChanged(t *testing.T) {
	e, _, _ := srtStoreEngine(t)
	e.mu.RLock()
	gen := e.sourceGen
	e.mu.RUnlock()

	e.mu.Lock()
	e.sourceGen++ // reconcileIngest cleared the flag and moved the generation
	e.mu.Unlock()
	e.noteStreamSeen(gen, feedIngest)
	if e.streamSeen.Load() || e.holdSince.Load() != 0 {
		t.Error("a tick from the old ingest re-set the flag after the ingest changed")
	}

	e.mu.RLock()
	gen = e.sourceGen
	e.mu.RUnlock()
	e.noteStreamSeen(gen, feedIngest)
	if !e.streamSeen.Load() {
		t.Error("a current tick did not set the flag")
	}
}

// A TICK THAT STRADDLES A MODE CHANGE MUST NOT COUNT THE OLD INGEST'S BYTES.
// feedWatch compared against the previous tick's baseline, so RTMP bytes
// delivered just before a switch to SRT read as movement under the new
// generation, and the 75s clock started with no SRT stream. The first tick of a
// new generation now only takes a baseline.
//
// The three calls are probeLoop's own, in its order: read the generation, ask
// the watch, note the result.
//
// Mutation: delete the `if gen != w.gen { clear(w.last); w.gen = gen }` block
// in feedWatch.advanced.
func TestATickStraddlingAModeChangeOnlyTakesABaseline(t *testing.T) {
	e, _, _ := srtStoreEngine(t)
	reconcileOrFail(t, e) // a destination is held, so a clock could start
	feeds := newFeedWatch(e)

	var cc uint8
	for range 20 {
		e.hub.Deliver(tsDatagram(&cc)) // the OLD ingest's last bytes
	}
	e.mu.Lock()
	e.sourceGen++ // the mode changed: reconcileIngest cleared the flag
	e.mu.Unlock()
	e.streamSeen.Store(false)
	e.holdSince.Store(0)

	tick := func() {
		e.mu.RLock()
		gen := e.sourceGen
		e.mu.RUnlock()
		e.noteStreamSeen(gen, feeds.advanced(gen))
	}
	tick()
	if e.streamSeen.Load() || e.holdSince.Load() != 0 {
		t.Fatal("the old ingest's bytes started the new generation's clock")
	}
	for range 20 {
		e.hub.Deliver(tsDatagram(&cc)) // the new ingest
	}
	tick()
	if !e.streamSeen.Load() || e.holdSince.Load() == 0 {
		t.Error("bytes from the new ingest, after the baseline, were not counted")
	}
}

// THE SLATE COUNTS ONLY UNTIL AN ENCODER CONNECTS, AND THE WAIT IS MEASURED
// FROM THE ENCODER. The layout probe reads the primary hub, so it cannot
// measure the slate; a slate-only go-live is released by the ceiling alone and
// the slate must start the clock (TestABackupOrSlateOnly...). But a slate that
// plays from boot started it before any encoder was there, and the 75s ran out
// first. A real ingest now restarts a clock the slate began.
//
// Mutation: delete the `if e.slateSeen.Swap(false) && ... { e.holdSince.Store(0) }`
// block in noteStreamSeen.
func TestASlateFromBootDoesNotStartTheWaitBeforeTheEncoderConnects(t *testing.T) {
	e := probeLoopEngine(t)
	slate, err := relay.New(slog.New(&startCounter{}), freeUDPPort(t))
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	t.Cleanup(func() { _ = slate.Close() })
	e.mu.Lock()
	e.sel = &selector{hub: slate, active: sourceSlate}
	e.mu.Unlock()
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	feedUntil(t, slate, stop)

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitFor("the slate to start the clock", func() bool { return e.holdSince.Load() != 0 })
	atSlate := e.holdSince.Load()
	time.Sleep(400 * time.Millisecond) // the encoder connects later

	var cc uint8
	waitFor("the encoder's arrival to restart the clock", func() bool {
		for range 20 {
			e.hub.Deliver(tsDatagram(&cc))
		}
		return e.holdSince.Load() > atSlate+int64(300*time.Millisecond)
	})
	if n := runningDests(e); n != 0 {
		t.Errorf("%d destinations out; the wait had not run out", n)
	}
}

// NO HOLD, NO CLOCK. With no destination waiting, a stream seen must not start
// the hold's clock (and reconcileAfterHoldExpiry must not then warn that
// destinations are being started provisionally when none was held).
//
// Mutation: drop the `hold != nil` condition in noteStreamSeen.
func TestAStreamSeenWithNothingHeldDoesNotStartTheClock(t *testing.T) {
	e, _, _ := srtStoreEngine(t) // no Reconcile: nothing is held
	e.mu.RLock()
	gen := e.sourceGen
	e.mu.RUnlock()
	e.noteStreamSeen(gen, feedIngest)
	if !e.streamSeen.Load() {
		t.Error("the stream was not noted")
	}
	if e.holdSince.Load() != 0 {
		t.Error("the clock started with no destination held")
	}
}
