package chat

import (
	"context"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/eventstream/streamtest"
)

// streamedHub is a chat hub publishing onto a real bus, bridged into a real
// events hub exactly as main wires it.
func streamedHub(t *testing.T) (*Hub, *eventstream.Subscription) {
	t.Helper()
	bus := events.NewBroker()
	stream := eventstream.NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sub := streamtest.Subscribe(t, stream)
	go eventstream.BridgeBus(ctx, bus, stream)
	// The bridge subscribes on its own goroutine; nothing published before it
	// has would reach it.
	waitFor(t, "the bridge to subscribe", func() bool { n, _ := bus.Stats(); return n == 1 })
	return testHub(t, WithPublisher(bus)), sub
}

// TestChatMessagesRetractionsAndModerationReachTheEventsStream drives the chat
// hub the way a platform and a moderator do: a message arrives, the operator
// deletes it, then bans and unbans its author. Each reaches the events stream
// through the bus the hub already publishes to, in order, as the type a
// consumer switches on.
func TestChatMessagesRetractionsAndModerationReachTheEventsStream(t *testing.T) {
	h, sub := streamedHub(t)
	a := &moderatingAdapter{banningAdapter: &banningAdapter{fakeAdapter: &fakeAdapter{
		platform: db.PlatformTwitch, account: "acct",
		messages: []Message{{ID: "m1", Author: Author{ID: "u1", Name: "viewer"}, Text: "hello"}},
	}}}
	if err := h.Attach(context.Background(), a); err != nil {
		t.Fatal(err)
	}

	msg := streamtest.Expect(t, sub, eventstream.TypeChatMessage)
	if m, ok := msg.Data.(map[string]any); !ok || m["text"] != "hello" {
		t.Fatalf("chat.message data = %#v", msg.Data)
	}

	if err := h.Delete(context.Background(), db.PlatformTwitch, "acct", "m1"); err != nil {
		t.Fatal(err)
	}
	streamtest.Expect(t, sub, eventstream.TypeChatRetract)
	mod := streamtest.Expect(t, sub, eventstream.TypeChatModeration)
	if m := mod.Data.(map[string]any); m["action"] != ModDelete || m["by"] != ActorOperator || m["messageId"] != "m1" {
		t.Errorf("delete moderation = %v", m)
	}

	if err := h.Ban(context.Background(), db.PlatformTwitch, "acct", "u1", 10*time.Minute, "spam"); err != nil {
		t.Fatal(err)
	}
	mod = streamtest.Expect(t, sub, eventstream.TypeChatModeration)
	if m := mod.Data.(map[string]any); m["action"] != ModTimeout || m["seconds"] != float64(600) || m["userId"] != "u1" {
		t.Errorf("timeout moderation = %v", m)
	}

	if err := h.Unban(context.Background(), db.PlatformTwitch, "acct", "u1"); err != nil {
		t.Fatal(err)
	}
	mod = streamtest.Expect(t, sub, eventstream.TypeChatModeration)
	if m := mod.Data.(map[string]any); m["action"] != ModUnban {
		t.Errorf("unban moderation = %v", m)
	}
}

// TestAutomodsActionsAreAnnouncedAsAutomods: the same Ban path, told who is
// taking it. An audit trail that recorded automod's bans as the operator's
// would blame the person for the machine.
func TestAutomodsActionsAreAnnouncedAsAutomods(t *testing.T) {
	h, sub := streamedHub(t)
	inner := &fakeAdapter{
		platform: db.PlatformTwitch, account: "acct",
		messages: []Message{{ID: "m1", Author: Author{ID: "u1"}, Text: "hmm"}},
	}
	h.SetModerator(&stubModerator{verdict: automodFinding(automod.ActionBan, 0)})
	if err := h.Attach(context.Background(), &banningAdapter{fakeAdapter: inner}); err != nil {
		t.Fatal(err)
	}
	mod := streamtest.Expect(t, sub, eventstream.TypeChatModeration)
	m := mod.Data.(map[string]any)
	if m["by"] != ActorAutomod || m["action"] != ModBan {
		t.Errorf("automod's ban was announced as %v", m)
	}
}

// TestAFailedModerationActionIsNotAnnounced: an event saying a ban happened
// when the platform said no is the one lie this must not tell.
func TestAFailedModerationActionIsNotAnnounced(t *testing.T) {
	h, sub := streamedHub(t)
	if err := h.Ban(context.Background(), db.PlatformTwitch, "nobody", "u1", 0, ""); err == nil {
		t.Fatal("a ban on a platform that is not connected succeeded")
	}
	streamtest.None(t, sub, eventstream.TypeChatModeration, 200*time.Millisecond)
}

// moderatingAdapter adds Deleter to banningAdapter.
type moderatingAdapter struct{ *banningAdapter }

func (moderatingAdapter) Delete(context.Context, string) error { return nil }

var _ Deleter = moderatingAdapter{}
