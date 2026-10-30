package relay

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a hand-wound clock for the arrival tracking, so a test can
// leave a gap longer than receivingGap without sleeping through it.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func hubWithClock(t *testing.T) (*Hub, *fakeClock) {
	t.Helper()
	h := newTestHub(t)
	c := &fakeClock{t: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	h.now = c.now
	return h, c
}

// A push ingest on the shared SRT listener has no ingest process, so the
// dashboard's uptime had nothing to time and read "—" for the whole broadcast.
// The hub is the one place every byte of that ingest passes through, so it
// reports how long the current stream has been arriving: from the first
// datagram after a silence, not from when the hub was built.
func TestTheHubReportsHowLongTheCurrentStreamHasBeenArriving(t *testing.T) {
	h, clock := hubWithClock(t)

	// Built and idle for a minute: an idle hub is not receiving, and the
	// minute it sat waiting is not uptime.
	clock.advance(time.Minute)
	if got := h.Stats().ReceivingSec; got != 0 {
		t.Fatalf("idle hub ReceivingSec = %v, want 0", got)
	}

	h.Deliver(tsPacketOn(256, 0))
	for i := 1; i <= 90; i++ {
		clock.advance(time.Second)
		h.Deliver(tsPacketOn(256, uint8(i)))
	}
	if got := h.Stats().ReceivingSec; got != 90 {
		t.Fatalf("after 90s of steady delivery ReceivingSec = %v, want 90", got)
	}
}

// The encoder went away: the stream is no longer arriving, so the hub must
// stop claiming an uptime rather than keep counting from the last datagram.
// And when it comes back, the clock restarts -- a reconnect is a new stream,
// exactly as a restarted ingest process's uptime starts again from zero.
func TestASilenceLongerThanTheGapEndsTheStreamAndAReconnectRestartsTheClock(t *testing.T) {
	h, clock := hubWithClock(t)

	h.Deliver(tsPacketOn(256, 0))
	for i := 1; i <= 30; i++ {
		clock.advance(time.Second)
		h.Deliver(tsPacketOn(256, uint8(i)))
	}

	clock.advance(receivingGap + time.Second)
	if got := h.Stats().ReceivingSec; got != 0 {
		t.Fatalf("after a %v silence ReceivingSec = %v, want 0", receivingGap+time.Second, got)
	}

	h.Deliver(tsPacketOn(256, 2))
	for i := 3; i <= 7; i++ {
		clock.advance(time.Second)
		h.Deliver(tsPacketOn(256, uint8(i)))
	}
	if got := h.Stats().ReceivingSec; got != 5 {
		t.Fatalf("after reconnecting for 5s ReceivingSec = %v, want 5 (not 35+)", got)
	}
}

// A hiccup shorter than the gap is the same stream: SRT reconnects inside its
// own latency window and a network blip of a second must not reset an uptime
// an operator is watching mid-broadcast.
func TestAGapShorterThanTheThresholdKeepsTheSameStream(t *testing.T) {
	h, clock := hubWithClock(t)

	h.Deliver(tsPacketOn(256, 0))
	for i := 1; i <= 10; i++ {
		clock.advance(time.Second)
		h.Deliver(tsPacketOn(256, uint8(i)))
	}
	clock.advance(receivingGap - time.Second)
	h.Deliver(tsPacketOn(256, 11))

	want := (10*time.Second + receivingGap - time.Second).Seconds()
	if got := h.Stats().ReceivingSec; got != want {
		t.Fatalf("ReceivingSec = %v, want %v", got, want)
	}
}

// The wire shape: absent while nothing arrives, so an idle or disabled
// programme's status frame is byte-for-byte what it was, and present under the
// name the UI reads (RelayStats.receivingSec).
func TestReceivingSecIsOmittedWhileIdleAndNamedForTheUIWhileArriving(t *testing.T) {
	h, clock := hubWithClock(t)

	idle, err := json.Marshal(h.Stats())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(idle), "receivingSec") {
		t.Fatalf("idle stats carry receivingSec: %s", idle)
	}

	h.Deliver(tsPacketOn(256, 0))
	clock.advance(2 * time.Second)
	h.Deliver(tsPacketOn(256, 1))
	live, err := json.Marshal(h.Stats())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(live), `"receivingSec":2`) {
		t.Fatalf("live stats do not carry receivingSec 2: %s", live)
	}
}
