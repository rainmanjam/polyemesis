package api

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
)

// /ws and /events carry the same chat events and used to classify them in two
// tables that disagreed: a chat retraction (message ids, the platform account,
// and the author a ban swept) reached a read-scoped /ws socket and was withheld
// from a read-scoped /events stream. Read scope does not get chat content or
// moderation of named viewers, so /ws now follows the catalogue.
//
// Mutation: restore events.TypeChatRetract to wsPassthrough, or stop eventView
// consulting eventstream.BusReadAccess.
func TestReadScopedSocketGetsNoChatRetraction(t *testing.T) {
	ev := events.Event{Type: events.TypeChatRetract, Data: chat.Retraction{Platform: "twitch", AuthorID: "viewer-1"}}
	if _, send := eventView(ev, true); send {
		t.Fatal("a read-scoped /ws socket was sent a chat retraction, which /events withholds from the same token")
	}
	if _, send := eventView(ev, false); !send {
		t.Fatal("an admin socket must still receive it")
	}
}

// Every bus type the stream bridges is classified by the catalogue, and /ws
// agrees with it: a Withheld row is never sent to a read-scoped socket.
func TestWebSocketAgreesWithTheCatalogueOnBridgedTypes(t *testing.T) {
	bridged := 0
	for _, typ := range events.AllTypes() {
		acc, ok := eventstream.BusReadAccess(typ)
		if !ok {
			continue
		}
		bridged++
		_, send := eventView(events.Event{Type: typ}, true)
		if acc == eventstream.Withheld && send {
			t.Errorf("%s is Withheld from read tokens on /events but sent on /ws", typ)
		}
	}
	if bridged < 3 {
		t.Fatalf("only %d bridged types found; the walk went blind", bridged)
	}
}

// A bridged type the catalogue Grants but wsEventPolicy does not list is
// withheld from a read-scoped socket: no rule is not a licence to send.
func TestGrantedButUnlistedBridgedTypeIsWithheld(t *testing.T) {
	const stub events.Type = "stub-granted-bridged"
	prev := busReadAccess
	busReadAccess = func(typ events.Type) (eventstream.Access, bool) {
		if typ == stub {
			return eventstream.Granted, true
		}
		return prev(typ)
	}
	t.Cleanup(func() { busReadAccess = prev })
	if _, send := eventView(events.Event{Type: stub}, true); send {
		t.Fatal("an unlisted type was sent to a read token because the catalogue Granted it")
	}
}
