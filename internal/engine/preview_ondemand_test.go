package engine

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func previewSettings(idle int) db.Settings {
	s := db.DefaultSettings()
	s.Preview.IdleTimeoutSeconds = idle
	return s
}

func TestPreviewIdleWindow(t *testing.T) {
	tests := []struct {
		name string
		idle int
		want time.Duration
	}{
		{"unset idle timeout falls back to the built-in default", 0, previewIdleDefault},
		{"negative idle timeout falls back to the built-in default", -5, previewIdleDefault},
		{"configured idle timeout is honoured", 90, 90 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := previewIdleWindow(previewSettings(tt.idle)); got != tt.want {
				t.Errorf("previewIdleWindow() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPreviewIdle(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		idle  int
		since time.Duration
		want  bool
	}{
		{"a page reload does not idle the encoder out", 30, 3 * time.Second, false},
		{"polling within the window keeps the encoder alive", 30, 29 * time.Second, false},
		{"the window boundary stops the encoder", 30, 30 * time.Second, true},
		{"a closed dashboard stops the encoder", 30, 5 * time.Minute, true},
		{"a longer configured window defers the stop", 300, 2 * time.Minute, false},
		{"never having been requested counts as idle", 30, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A zero seen time is what a never-requested preview carries, and
			// must read as idle rather than as "requested at the epoch".
			seen := time.Time{}
			if tt.since != 0 {
				seen = now.Add(-tt.since)
			}
			if got := previewIdle(previewSettings(tt.idle), seen, now); got != tt.want {
				t.Errorf("previewIdle(seen=-%v) = %v, want %v", tt.since, got, tt.want)
			}
		})
	}
}

func TestPreviewSigIgnoresIdleTimeout(t *testing.T) {
	base := previewSettings(30)
	longer := previewSettings(600)

	if previewSig(base) != previewSig(longer) {
		t.Error("changing the idle timeout changed the restart signature; it would cycle a live preview")
	}

	tests := []struct {
		name  string
		apply func(*db.Settings)
	}{
		{"segment length change restarts the encoder", func(s *db.Settings) { s.Preview.SegmentSeconds = 4 }},
		{"height change restarts the encoder", func(s *db.Settings) { s.Preview.VideoHeight = 720 }},
		{"bitrate change restarts the encoder", func(s *db.Settings) { s.Preview.VideoKbps = 1500 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := previewSettings(30)
			tt.apply(&changed)
			if previewSig(base) == previewSig(changed) {
				t.Error("signature unchanged; the encoder would keep stale arguments")
			}
		})
	}
}

// A NEW HUB IS NOT DELIVERY.
//
// previewFlowing decides whether anything is on air by watching a byte counter
// advance. A counter is only comparable with ITSELF: when the selector comes up
// or goes down the hub is replaced, and the replacement starts at zero. Compared
// against the old hub's total that reads as "the number changed" -- the exact
// opposite of the truth -- so the gate would report a live output and start an
// encoder against silence for the whole grace period.
//
// Found in review rather than by the suite, which is why it is pinned.
func TestASwappedHubDoesNotCountAsFlow(t *testing.T) {
	e := lifeEngine(t)
	// FATAL, NOT SKIP. Without a hub this test asserts nothing, and a test that
	// declines to run still prints ok -- which is the free pass the skip census
	// exists to refuse. The fixture does provide one; if that ever stops being
	// true the fixture is broken and should say so.
	if e.downstreamHub() == nil {
		t.Fatal("fixture: lifeEngine has no downstream hub, so there is nothing to " +
			"sample and the hub-identity property cannot be exercised")
	}

	// A baseline remembered from a DIFFERENT hub, carrying a large total, and
	// stamped as though it had just advanced. That is the state a selector swap
	// leaves behind.
	e.mu.Lock()
	e.previewRxHub = nil
	e.previewRxBytes = 1 << 20
	e.previewRxAt = time.Now()
	e.mu.Unlock()

	if e.previewFlowing(time.Now()) {
		t.Error("a hub whose counter has not been SEEN to advance read as flowing. " +
			"Its zero differs from the remembered total, and treating a difference " +
			"as delivery starts an encoder against silence after every selector swap")
	}

	// And it adopts, so the next real advance is measured against the right
	// baseline rather than being missed.
	e.mu.RLock()
	adopted := e.previewRxHub == e.downstreamHub()
	e.mu.RUnlock()
	if !adopted {
		t.Error("the new hub was not adopted as the baseline, so every later sample " +
			"compares against a hub that is gone")
	}
}

// ------------------------------------------------------ what is actually on air

// OutputLive waits to SEE the hub advance, and then follows it down again.
//
// The gate is a byte DELTA rather than a total, and the difference is the whole
// behaviour: relay.Hub.RxBytes() only ever grows, so a `> 0` test answers yes for
// the rest of the process's life after the first byte. A tile drawn from that
// lights up once and never clears, which is exactly the dead picture an operator
// was left staring at when a stream ended.
func TestOutputLiveWaitsToSeeBytesArriveOnTheHubTheDestinationsRead(t *testing.T) {
	e := lifeEngine(t)
	// FATAL, NOT SKIP. Without a hub there is nothing to sample and this test
	// asserts nothing at all, which is the free pass a skip would buy.
	h := e.downstreamHub()
	if h == nil {
		t.Fatal("fixture: lifeEngine has no downstream hub, so there is no counter to watch")
	}

	// The first sample only adopts a baseline: nothing has been seen to advance,
	// and on an install where nothing has ever published that is the truth.
	if e.OutputLive() {
		t.Fatal("output reported on air before the hub had carried a single byte; the " +
			"grid would draw a player against a stream that does not exist")
	}

	deliverTS(t, h, 4)

	if !e.OutputLive() {
		t.Error("bytes arrived on the hub the destinations read and output still reports " +
			"dead, so a programme that IS being broadcast gets no picture in the grid")
	}
}

// And it clears again when the stream ends.
//
// The test above only shows the gate coming UP. Going back down is the half an
// operator complained about: a tile that lit once and then held a dead picture.
//
// Driven at chosen instants rather than by sleeping: the grace is measured in
// seconds and a test that waited them out would be ten seconds slower for no
// extra assurance.
func TestOutputStopsBeingLiveOnceTheStreamEnds(t *testing.T) {
	e := lifeEngine(t)
	h := e.downstreamHub()
	if h == nil {
		t.Fatal("fixture: lifeEngine has no downstream hub, so there is no counter to watch")
	}

	t0 := time.Now()
	e.previewFlowing(t0) // adopts the baseline, as the first sample always does
	deliverTS(t, h, 4)
	if !e.previewFlowing(t0) {
		t.Fatal("the hub advanced and the gate did not notice, so the rest of this test " +
			"would be measuring the decay of something that never came up")
	}

	// An ordinary sampling gap is not a dead stream. The grace is deliberately
	// wider than the sweep interval so a quiet moment between two datagrams
	// cannot take a live picture off the grid.
	if !e.previewFlowing(t0.Add(previewFlowGrace - time.Second)) {
		t.Error("a gap shorter than the grace read as a dead stream; the tile would flap " +
			"on an ordinary stream rather than following it")
	}

	// Nothing further arrives. Only time passes.
	if e.previewFlowing(t0.Add(previewFlowGrace)) {
		t.Error("output still reports on air a full grace period after the last byte. A " +
			"counter that only ever grows answers yes for ever, which is why this is a " +
			"delta -- and why the tile used to keep a dead picture up")
	}
}

// An engine whose relay never came up has nothing to report on.
//
// It is a real state: relay.New can fail to bind, and the engine survives it.
// Answering "on air" there would invent a fact about a pipeline that does not
// exist, and reading the counter anyway would take the process down.
func TestWithNoRelayAtAllNothingIsOnAir(t *testing.T) {
	e := lifeEngine(t)
	e.mu.Lock()
	e.hub = nil
	e.mu.Unlock()
	if e.downstreamHub() != nil {
		t.Fatal("fixture: the engine still has a downstream hub, so the no-relay case is " +
			"not what is being exercised")
	}

	if e.OutputLive() {
		t.Error("an engine with no relay reported output on air")
	}
}

// ------------------------------------------------------ the encoder's own gate

// Nothing on air, nothing to preview.
//
// With the relay quiet the preview's ffmpeg does not fail and does not exit --
// it BLOCKS in avformat_open_input on a UDP socket that has no EOF. The
// supervisor has no stall watchdog, so it sits "running" for ever, writes no
// playlist, and every poll 404s while a libx264 process holds a port. The
// assertion is therefore about what is HELD afterwards: a one-port allocator
// makes a leaked port the next Allocate() failing.
func TestThePreviewRefusesToStartAgainstAQuietRelay(t *testing.T) {
	e := lifeEngine(t)
	e.alloc = oneSlotAllocator(t)
	setSettings(e, previewOnSettings())
	// Deliberately NO markPreviewFlowing: nothing has ever published here, which
	// is the condition under test.

	e.startPreviewLocked(e.Settings())

	if e.preview != nil {
		t.Error("an encoder was spawned to transcode silence; it blocks for ever on a " +
			"socket that never delivers and 404s every playlist poll behind it")
	}
	if e.previewPort != 0 {
		t.Errorf("previewPort = %d after a refused start", e.previewPort)
	}
	if hasSubscriber(e.hub, "preview") {
		t.Error("a refused start left a subscription on the hub")
	}
	mustAllocate(t, e.alloc, "after a preview start that was refused")
}

// A stream that ends stops the encoder without waiting out the idle window.
//
// Idle means "nobody is watching"; dry means "there is nothing to watch". The
// viewer below is watching RIGHT NOW, so the idle window alone would keep the
// encoder alive for the whole thirty seconds on a stream that has already
// finished -- which is most of what made the pipeline panel flap.
func TestASweepStopsThePreviewWhenTheStreamEndsEvenThoughSomebodyIsWatching(t *testing.T) {
	e := lifeEngine(t)
	e.alloc = oneSlotAllocator(t)
	setSettings(e, previewOnSettings())
	markPreviewFlowing(e)

	e.startPreviewLocked(e.Settings())
	if e.preview == nil {
		t.Fatal("the preview did not start, so there is no encoder for the sweep to stop")
	}

	now := time.Now()
	e.mu.Lock()
	// Watched a moment ago: previewIdle says keep it.
	e.previewSeen = now
	// And the last byte was longer ago than the grace: the stream has ended.
	e.previewRxAt = now.Add(-2 * previewFlowGrace)
	e.mu.Unlock()

	e.sweepPreview(now)

	if e.preview != nil {
		t.Error("the encoder is still running on a stream that ended, because the sweep " +
			"only asked whether anybody was watching. It will hold a port and a libx264 " +
			"process for the rest of the idle window")
	}
	if hasSubscriber(e.hub, "preview") {
		t.Error("the stopped preview is still subscribed to the hub")
	}
	mustAllocate(t, e.alloc, "after the sweep stopped a preview whose stream had ended")
}

// The preview reads the tier that is ON AIR, and gives back the hub it joined.
//
// Every other consumer -- meters, clips, captions, destinations -- reads
// downstreamHub(). The preview read the raw primary ingest, so during a failover
// the destinations rode the slate and the OPERATOR'S PREVIEW SHOWED NOTHING,
// which is the moment a preview is worth most. The release matters for the same
// reason in reverse: unsubscribing from e.hub instead of from the hub actually
// joined leaves a live subscription on a selector hub that is about to close.
func TestThePreviewJoinsAndThenLeavesTheHubThatIsOnAir(t *testing.T) {
	e := lifeEngine(t)
	e.alloc = oneSlotAllocator(t)
	setSettings(e, previewOnSettings())

	// The failover tier is up, so the hub carrying the broadcast is the
	// selector's and not the primary ingest's.
	onAir := lifeHub(t)
	e.mu.Lock()
	e.sel = &selector{hub: onAir}
	e.mu.Unlock()
	markPreviewFlowing(e)

	e.startPreviewLocked(e.Settings())
	if e.preview == nil {
		t.Fatal("the preview did not start, so there is no subscription to assert about")
	}
	if !hasSubscriber(onAir, "preview") {
		t.Error("the preview did not join the tier that is on air, so it encodes whatever " +
			"the primary ingest is doing -- nothing, during a failover")
	}
	if hasSubscriber(e.hub, "preview") {
		t.Error("the preview joined the raw primary ingest while the selector was on air")
	}

	// The tier goes away underneath it, which is what happens when failover is
	// switched off or the selector is rebuilt. The hub to release is still the
	// one that was JOINED.
	e.mu.Lock()
	e.sel = nil
	e.mu.Unlock()

	e.previewMu.Lock()
	e.stopPreviewLocked()
	e.previewMu.Unlock()

	if hasSubscriber(onAir, "preview") {
		t.Error("the preview was released from some other hub and is still subscribed to " +
			"the one it joined; that hub is about to close under a live subscription")
	}
	mustAllocate(t, e.alloc, "after the preview was stopped")
}
