package overlay

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/eventstream/streamtest"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newGate(t *testing.T) (*Gate, *eventstream.Hub, *eventstream.Subscription, *clock) {
	t.Helper()
	hub := eventstream.NewHub()
	sub, _, _ := hub.Subscribe(eventstream.Filter{Overlay: &eventstream.OverlayView{}}, "")
	t.Cleanup(sub.Close)
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	return New(hub, WithClock(c.now)), hub, sub, c
}

func m(id, text string) chat.Message {
	return chat.Message{
		ID: id, Platform: db.PlatformTwitch, Account: "acct", Channel: "chan",
		Author: chat.Author{ID: "author-1234", Name: "Viewer", Color: "#ff0000"},
		Text:   text, ReplyToID: "parent-1",
	}
}

func next(t *testing.T, sub *eventstream.Subscription) (eventstream.Envelope, bool) {
	t.Helper()
	select {
	case env := <-sub.C:
		return env, true
	case <-time.After(50 * time.Millisecond):
		return eventstream.Envelope{}, false
	}
}

func TestAClearMessageIsShownAtOnceWithoutIdentity(t *testing.T) {
	g, _, sub, _ := newGate(t)
	g.Offer(m("m1", "hello"), chat.DisplayClear)
	env, ok := next(t, sub)
	if !ok || env.Type != eventstream.TypeChatDisplay {
		t.Fatalf("got %+v, want a chat.display", env)
	}
	line := env.Data.(eventstream.DisplayLine)
	if line.Text != "hello" || line.Name != "Viewer" || line.Platform != "twitch" || line.Color != "#ff0000" {
		t.Errorf("line = %+v", line)
	}
	if line.ID == "" || line.ID == "m1" {
		t.Errorf("display id %q must be opaque, not the platform's message id", line.ID)
	}
}

func TestABlockedMessageIsNeverShown(t *testing.T) {
	g, _, sub, c := newGate(t)
	g.Offer(m("m1", "slur"), chat.DisplayBlocked)
	c.add(time.Hour)
	g.Sweep()
	if env, ok := next(t, sub); ok {
		t.Fatalf("a blocked message produced %+v", env)
	}
}

// THE RETRACTION TRAP. With the model checker on, a message is held until it is
// judged; a Blocked verdict means it never reaches the wire at all, so there is
// nothing to retract and no frame in which it was visible.
func TestAPendingMessageIsHeldUntilJudged(t *testing.T) {
	g, _, sub, _ := newGate(t)
	g.Offer(m("m1", "ok"), chat.DisplayPending)
	g.Offer(m("m2", "bad"), chat.DisplayPending)
	if env, ok := next(t, sub); ok {
		t.Fatalf("a pending message was published before its verdict: %+v", env)
	}
	g.Judge(db.PlatformTwitch, "acct", "m2", chat.DisplayBlocked)
	g.Judge(db.PlatformTwitch, "acct", "m1", chat.DisplayClear)
	env, ok := next(t, sub)
	if !ok || env.Data.(eventstream.DisplayLine).Text != "ok" {
		t.Fatalf("got %+v, want only the cleared line", env)
	}
	if env, ok := next(t, sub); ok {
		t.Fatalf("the blocked line was published: %+v", env)
	}
}

// The bound. A verdict that never comes (queue full, model hung, moderator
// replaced) must not hold a message for ever, and must not release it either:
// past MaxHold it is DROPPED. Fail closed -- a missing line on an overlay is
// better than a line automod would have removed.
func TestAMessageNeverJudgedIsDroppedAtTheDeadline(t *testing.T) {
	g, _, sub, c := newGate(t)
	g.Offer(m("m1", "slow"), chat.DisplayPending)
	c.add(MaxHold - time.Millisecond)
	g.Sweep()
	g.Judge(db.PlatformTwitch, "acct", "m1", chat.DisplayClear)
	if _, ok := next(t, sub); !ok {
		t.Fatal("a verdict just inside the deadline did not release the line")
	}

	g.Offer(m("m2", "slower"), chat.DisplayPending)
	c.add(MaxHold + time.Millisecond)
	g.Sweep()
	g.Judge(db.PlatformTwitch, "acct", "m2", chat.DisplayClear)
	if env, ok := next(t, sub); ok {
		t.Fatalf("a line judged after the deadline was published: %+v", env)
	}
	if s := g.Stats(); s.Expired != 1 {
		t.Errorf("expired = %d, want 1", s.Expired)
	}

	// And a verdict that beats the sweep but not the deadline is refused by
	// Judge itself: the sweep only bounds memory.
	g.Offer(m("m3", "late"), chat.DisplayPending)
	c.add(MaxHold + time.Millisecond)
	g.Judge(db.PlatformTwitch, "acct", "m3", chat.DisplayClear)
	if env, ok := next(t, sub); ok {
		t.Fatalf("a verdict past the deadline released %+v before any sweep", env)
	}
}

func TestARetractedPendingMessageIsNeverShown(t *testing.T) {
	g, _, sub, _ := newGate(t)
	g.Offer(m("m1", "x"), chat.DisplayPending)
	g.Retract(db.PlatformTwitch, "acct", []string{"m1"})
	g.Judge(db.PlatformTwitch, "acct", "m1", chat.DisplayClear)
	if env, ok := next(t, sub); ok {
		t.Fatalf("published %+v", env)
	}
}

// After display, an operator's hide, delete or ban still takes it off screen.
func TestARetractionAfterDisplayRemovesTheLine(t *testing.T) {
	g, _, sub, _ := newGate(t)
	g.Offer(m("m1", "x"), chat.DisplayClear)
	shown, _ := next(t, sub)
	g.Retract(db.PlatformTwitch, "acct", []string{"m1", "never-shown"})
	env, ok := next(t, sub)
	if !ok || env.Type != eventstream.TypeChatUndisplay {
		t.Fatalf("got %+v, want chat.undisplay", env)
	}
	rm := env.Data.(eventstream.DisplayRemoval)
	if len(rm.IDs) != 1 || rm.IDs[0] != shown.Data.(eventstream.DisplayLine).ID || rm.Platform != "twitch" {
		t.Errorf("removal %+v does not name exactly the shown line", rm)
	}
	// Nothing for ids that were never on screen.
	g.Retract(db.PlatformTwitch, "acct", []string{"nope"})
	if env, ok := next(t, sub); ok {
		t.Fatalf("published %+v for an id never shown", env)
	}
}

// The ids are opaque but stable for one message, and different across
// accounts that reuse a platform id.
func TestDisplayIDsAreOpaqueAndDistinct(t *testing.T) {
	g, _, _, _ := newGate(t)
	a := g.displayID(db.PlatformTwitch, "acct", "1")
	b := g.displayID(db.PlatformTwitch, "other", "1")
	if a == b || a != g.displayID(db.PlatformTwitch, "acct", "1") {
		t.Errorf("ids %q %q", a, b)
	}
}

// ----------------------------------------------------------- real producers

// scriptAdapter delivers its messages then waits.
type scriptAdapter struct {
	platform db.Platform
	msgs     []chat.Message
}

func (a *scriptAdapter) Platform() db.Platform { return a.platform }
func (a *scriptAdapter) Account() string       { return "acct-" + string(a.platform) }
func (a *scriptAdapter) Run(ctx context.Context, sink chat.Sink) error {
	for _, m := range a.msgs {
		sink.Deliver(m)
	}
	<-ctx.Done()
	return nil
}

// TestTheChatHubDrivesTheOverlay is the emission test for both overlay types:
// the REAL chat hub, a real adapter delivering, a real local hide -- not
// envelopes published by hand.
func TestTheChatHubDrivesTheOverlay(t *testing.T) {
	stream := eventstream.NewHub()
	sub := streamtest.Subscribe(t, stream)
	g := New(stream)
	ch := chat.New(chat.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(ch.Close)
	ch.SetDisplay(g)
	if err := ch.Attach(context.Background(), &scriptAdapter{
		platform: db.PlatformTwitch,
		msgs:     []chat.Message{{ID: "m1", Author: chat.Author{ID: "u1", Name: "Viewer"}, Text: "hi overlay"}},
	}); err != nil {
		t.Fatal(err)
	}
	env := streamtest.Expect(t, sub, eventstream.TypeChatDisplay)
	line := env.Data.(eventstream.DisplayLine)
	if line.Text != "hi overlay" {
		t.Fatalf("line %+v", line)
	}
	if err := ch.HideLocally(db.PlatformTwitch, "acct-twitch", "m1"); err != nil {
		t.Fatal(err)
	}
	rm := streamtest.Expect(t, sub, eventstream.TypeChatUndisplay).Data.(eventstream.DisplayRemoval)
	if len(rm.IDs) != 1 || rm.IDs[0] != line.ID {
		t.Errorf("removal %+v, want [%s]", rm, line.ID)
	}
}

type nopPub struct{}

func (nopPub) Publish(eventstream.Envelope) {}
