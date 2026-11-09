package activityhub

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/twitch/eventsub"
)

// The drain window is how long the OLD socket is still read after a reconnect
// swap. These tests pin the pieces of that: its default, what happens to frames
// already buffered when the window expires, and the ways the old socket ends.

func drainRig(t *testing.T) (*Twitch, *[]activity.Event) {
	t.Helper()
	src := NewTwitch(twConfig(newFakeTwitch(t), io.Discard))
	var got []activity.Event
	return src, &got
}

func frameOf(t *testing.T, raw string) eventsub.Frame {
	t.Helper()
	m, err := eventsub.ParseMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return eventsub.Frame{Msg: m}
}

func TestTheDrainWindowDefaultsToOneSecondWhenUnset(t *testing.T) {
	for name, cfg := range map[string]time.Duration{"unset": 0, "negative": -time.Second} {
		src := NewTwitch(TwitchConfig{AccountRef: "1", DrainWindow: cfg})
		if got := src.drainWindow(); got != time.Second {
			t.Errorf("%s: drain window = %s, want the 1s default", name, got)
		}
	}
	if got := NewTwitch(TwitchConfig{AccountRef: "1", DrainWindow: 7 * time.Second}).drainWindow(); got != 7*time.Second {
		t.Errorf("a configured window = %s, want 7s", got)
	}
}

// The window expiring must not lose what was already read off the old socket:
// Twitch does not repeat those messages on the new one.
func TestFramesBufferedOnTheOldSocketAreHandedOverWhenTheWindowExpires(t *testing.T) {
	src, got := drainRig(t)
	emit := func(e activity.Event) { *got = append(*got, e) }
	c := make(chan eventsub.Frame, 4)
	c <- frameOf(t, note("d-1", "channel.cheer", "1", evCheer))
	c <- frameOf(t, note("d-2", "channel.cheer", "1", evCheer))
	src.flushDrained(context.Background(), c, emit)
	if len(*got) != 2 {
		t.Fatalf("%d events handed over, want both buffered frames", len(*got))
	}
	if len(c) != 0 {
		t.Errorf("%d frames left in the old socket's buffer", len(c))
	}
}

// Expiry flushes what is BUFFERED; it must not sit waiting for more.
func TestTheExpiryHandoverDoesNotWaitForFramesThatHaveNotArrived(t *testing.T) {
	src, _ := drainRig(t)
	c := make(chan eventsub.Frame) // open, empty, never written
	done := make(chan struct{})
	go func() { src.flushDrained(context.Background(), c, func(activity.Event) {}); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the handover blocked on an empty, open channel")
	}
}

func TestTheExpiryHandoverStopsAtAnErrorFrameAndAtAClosedSocket(t *testing.T) {
	src, got := drainRig(t)
	emit := func(e activity.Event) { *got = append(*got, e) }

	c := make(chan eventsub.Frame, 4)
	c <- frameOf(t, note("e-1", "channel.cheer", "1", evCheer))
	c <- eventsub.Frame{Err: errors.New("socket reset")}
	c <- frameOf(t, note("e-2", "channel.cheer", "1", evCheer))
	src.flushDrained(context.Background(), c, emit)
	if len(*got) != 1 || len(c) != 1 {
		t.Errorf("handed over %d, %d left; an error frame ends the old socket, so only what came before it counts", len(*got), len(c))
	}

	*got = nil
	cl := make(chan eventsub.Frame, 2)
	cl <- frameOf(t, note("c-1", "channel.cheer", "1", evCheer))
	close(cl)
	src.flushDrained(context.Background(), cl, emit)
	if len(*got) != 1 {
		t.Errorf("a closed socket's buffered frame: %d events, want 1", len(*got))
	}
}

func TestDrainedReportsWhetherTheOldSocketIsStillWorthReading(t *testing.T) {
	src, got := drainRig(t)
	emit := func(e activity.Event) { *got = append(*got, e) }
	ctx := context.Background()

	if src.drained(ctx, eventsub.Frame{}, false, emit) {
		t.Error("a closed channel must end the drain")
	}
	if src.drained(ctx, eventsub.Frame{Err: errors.New("boom")}, true, emit) {
		t.Error("an error frame must end the drain")
	}
	before := src.Stats().Unparseable
	if !src.drained(ctx, eventsub.Frame{Bad: true}, true, emit) {
		t.Error("an unparseable frame must not end the drain")
	}
	if src.Stats().Unparseable != before+1 {
		t.Error("an unparseable frame was not counted")
	}
	if !src.drained(ctx, frameOf(t, keepaliveMsg("k")), true, emit) || len(*got) != 0 {
		t.Errorf("a keepalive keeps the drain going and emits nothing: %+v", *got)
	}
	if !src.drained(ctx, frameOf(t, note("n-1", "channel.cheer", "1", evCheer)), true, emit) || len(*got) != 1 {
		t.Errorf("a notification is emitted and the drain continues: %+v", *got)
	}
	rev := frameOf(t, revocationMsg("rv", "channel.cheer", "authorization_revoked"))
	if !src.drained(ctx, rev, true, emit) {
		t.Error("a revocation keeps the drain going")
	}
	for _, k := range src.Kinds() {
		if k.Kind == activity.KindTip && k.State == activity.KindOn {
			t.Errorf("the revocation read off the old socket was not applied: %+v", k)
		}
	}
}

// End to end: when the window expires the old socket is really closed, and a
// window longer than the test keeps it open.
func TestTheOldSocketIsClosedWhenTheDrainWindowExpires(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, func(c *TwitchConfig) { c.DrainWindow = 50 * time.Millisecond })
	old := r.ready()
	old.send(reconnectMsg("rc", f.ws2URL()))
	waitFor(t, "the swap", func() bool { return r.src.Stats().Reconnects == 1 })
	select {
	case <-old.closed:
	case <-time.After(30 * time.Second):
		t.Fatal("the old socket was still open long after its drain window")
	}
}
