package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
)

// A READ TOKEN IS METADATA, NOT CONTENT, AND CHAT IS CONTENT. The REST half of
// this is in TestReadTokenIsDeniedTheRoutesThatAreNotReads; this is the other
// door. /ws admits a read token on purpose -- watching a stream go out is what
// read tokens are for -- and every chat message was fanned out to it with only
// a best-effort text scrub: what each viewer wrote, and who they are.
//
// The admin socket is the positive control: without the message reaching it,
// the read socket's silence would prove nothing.
func TestAReadSocketIsSentNoChat(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	s := serverUnderTest(t, h)
	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)
	admin := createScopedToken(t, h, sign, "deploy", db.ScopeAdmin)

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws"
	open := func(tok string) *websocket.Conn {
		c, _, err := websocket.DefaultDialer.Dial(url, http.Header{"Authorization": {"Bearer " + tok}})
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		return c
	}
	readConn, adminConn := open(read), open(admin)
	defer readConn.Close()
	defer adminConn.Close()

	const said = "a viewer wrote this in chat"
	time.Sleep(100 * time.Millisecond)
	s.bus.Publish(events.TypeChat, map[string]any{"author": "someone", "text": said})
	// Published after the chat message, so the read socket having received it
	// proves the chat frame, had it been sent, would already have arrived.
	s.bus.Publish(events.TypeChatState, map[string]any{"platform": "twitch", "state": "connected"})

	if got := waitForFrame(t, adminConn, events.TypeChat); !strings.Contains(got, said) {
		t.Fatalf("the ADMIN socket did not receive the chat message (frame %q); the positive control failed", got)
	}
	// Every frame up to the chat_state marker, which the broker delivers after
	// the chat message: a chat frame sent to this socket would be among them.
	_ = readConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, msg, err := readConn.ReadMessage()
		if err != nil {
			t.Fatalf("the read socket never received the chat_state marker: %v", err)
		}
		frame := string(msg)
		if strings.Contains(frame, `"type":"`+string(events.TypeChat)+`"`) {
			t.Fatalf("the READ socket was sent a chat message: %s", frame)
		}
		if strings.Contains(frame, `"type":"`+string(events.TypeChatState)+`"`) {
			break
		}
	}
}
