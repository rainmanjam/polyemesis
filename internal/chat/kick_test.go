package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func kickAdapter(t *testing.T, opts func(*KickConfig)) *KickAdapter {
	t.Helper()
	cfg := KickConfig{
		AccountRef:        "99",
		BroadcasterUserID: 99,
		Channel:           "mychannel",
		Token:             StaticToken("kick-token"),
		APIBase:           "http://example.invalid",
		PublicURL:         "https://stream.example.com",
		Now:               fixedClock(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)),
		// Reachable by default so a test that is not about reachability does
		// not have to say so.
		Probe: func(_ context.Context, endpoint string) (string, error) {
			_, nonce, _ := strings.Cut(endpoint, "?probe=")
			return nonce, nil
		},
	}
	if opts != nil {
		opts(&cfg)
	}
	a, err := NewKick(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

const kickFullEvent = `{
  "message_id": "msg-1",
  "created_at": "2026-03-01T12:00:00Z",
  "broadcaster": {"user_id": 99, "username": "mychannel", "channel_slug": "mychannel"},
  "sender": {"user_id": 7, "username": "Viewer",
             "identity": {"username_color": "#FF0000",
                          "badges": [{"type": "moderator", "text": "Moderator"}]}},
  "content": "hello kick",
  "emotes": [{"emote_id": "e1", "name": "Kappa", "positions": [{"s": 0, "e": 4}]}],
  "replies_to": {"message_id": "prev-1", "content": "earlier", "sender": {"user_id": 8, "username": "Alice"}}
}`

func TestKickWebhookNormalisation(t *testing.T) {
	a := kickAdapter(t, nil)

	tests := []struct {
		name      string
		eventType string
		body      string
		wantOK    bool
		check     func(t *testing.T, m Message)
	}{
		{
			name:      "a full chat event maps onto the unified model",
			eventType: "chat.message.sent",
			body:      kickFullEvent,
			wantOK:    true,
			check: func(t *testing.T, m Message) {
				if m.ID != "msg-1" || m.Text != "hello kick" || m.Platform != db.PlatformKick {
					t.Fatalf("message = %+v", m)
				}
				if m.Author.Name != "Viewer" || m.Author.ID != "7" || m.Author.Color != "#FF0000" {
					t.Fatalf("author = %+v", m.Author)
				}
				if m.ReplyToID != "prev-1" || m.ReplyTo != "Alice" {
					t.Fatalf("reply = %q/%q", m.ReplyToID, m.ReplyTo)
				}
				if !m.At.Equal(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)) {
					t.Fatalf("timestamp = %v", m.At)
				}
				n := m.Normalise(nil)
				if !n.Author.Moderator {
					t.Fatal("the moderator badge did not set the flag")
				}
				if len(n.Emotes) != 1 || n.Emotes[0].End != 5 {
					t.Fatalf("emotes = %+v, want an exclusive end of 5", n.Emotes)
				}
			},
		},
		{
			name:      "the sender being the broadcaster is recognised by id",
			eventType: "chat.message.sent",
			body: `{"message_id":"m","content":"hi","broadcaster":{"user_id":99},
			        "sender":{"user_id":99,"username":"mychannel"}}`,
			wantOK: true,
			check: func(t *testing.T, m Message) {
				n := m.Normalise(nil)
				if !n.Author.Broadcaster {
					t.Fatal("the host was not marked as the broadcaster")
				}
			},
		},
		{
			name:      "an alternate spelling of the text field is accepted",
			eventType: "chat.message.sent",
			body:      `{"id":"m2","message":"alternate shape","sender":{"user_id":7,"username":"V"}}`,
			wantOK:    true,
			check: func(t *testing.T, m Message) {
				if m.ID != "m2" || m.Text != "alternate shape" {
					t.Fatalf("message = %+v", m)
				}
			},
		},
		{
			name:      "a livestream event is not a chat message and is not an error",
			eventType: "livestream.status.updated",
			body:      `{"is_live":true}`,
			wantOK:    false,
		},
		{
			name:      "an empty message is not delivered",
			eventType: "chat.message.sent",
			body:      `{"message_id":"m","content":"   ","sender":{"user_id":7}}`,
			wantOK:    false,
		},
		{
			name:      "a body that is not JSON is refused rather than crashing",
			eventType: "chat.message.sent",
			body:      `not json at all`,
			wantOK:    false,
		},
		{
			name:      "a delivery with no event-type header is still parsed",
			eventType: "",
			body:      kickFullEvent,
			wantOK:    true,
			check: func(t *testing.T, m Message) {
				if m.Text != "hello kick" {
					t.Fatalf("message = %+v", m)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := a.messageFrom(tc.eventType, []byte(tc.body))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok {
				tc.check(t, m)
			}
		})
	}
}

func TestKickHandlerDeliversAndAlwaysAcknowledges(t *testing.T) {
	a := kickAdapter(t, nil)
	got := make(chan Message, 4)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Run(ctx, SinkFunc(func(m Message) { got <- m })) }()
	waitFor(t, "the adapter to register its sink", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.sink != nil
	})

	srv := httptest.NewServer(a.Handler())
	defer srv.Close()

	post := func(t *testing.T, eventType, body string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
		req.Header.Set("Kick-Event-Type", eventType)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := post(t, "chat.message.sent", kickFullEvent); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	select {
	case m := <-got:
		if m.Text != "hello kick" {
			t.Fatalf("delivered %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the webhook delivery never reached the sink")
	}

	// An unreadable body must still be acknowledged: a 500 earns a retry, and
	// a retry storm of chat helps nobody.
	if code := post(t, "chat.message.sent", "{{{"); code != http.StatusOK {
		t.Fatalf("an unparseable body returned %d; Kick would retry it forever", code)
	}
	waitFor(t, "the unparseable delivery to be counted", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.unparseable == 1
	})
}

func TestKickHandlerAnswersTheReachabilityProbe(t *testing.T) {
	a := kickAdapter(t, nil)
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "?probe=abc123")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 32)
	n, _ := resp.Body.Read(buf)
	if got := strings.TrimSpace(string(buf[:n])); got != "abc123" {
		t.Fatalf("probe answered %q, want the nonce echoed", got)
	}

	req, _ := http.NewRequest(http.MethodPut, srv.URL, nil)
	other, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Body.Close()
	if other.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("PUT returned %d, want 405", other.StatusCode)
	}
}

func TestKickHandlerRejectsADeliveryThatFailsVerification(t *testing.T) {
	a := kickAdapter(t, func(c *KickConfig) {
		c.Verify = func(*http.Request, []byte) error { return fmt.Errorf("bad signature") }
	})
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL, "application/json", strings.NewReader(kickFullEvent))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestKickUnreachableCallbackIsExplainedNotHidden(t *testing.T) {
	tests := []struct {
		name    string
		cfg     func(*KickConfig)
		wantSub []string
	}{
		{
			name:    "no public URL at all",
			cfg:     func(c *KickConfig) { c.PublicURL = "" },
			wantSub: []string{"publicly reachable", "Settings", "sending to Kick works"},
		},
		{
			name:    "a loopback address the internet cannot reach",
			cfg:     func(c *KickConfig) { c.PublicURL = "http://127.0.0.1:8080" },
			wantSub: []string{"private or loopback", "HTTPS"},
		},
		{
			name: "the probe never comes back",
			cfg: func(c *KickConfig) {
				c.Probe = func(context.Context, string) (string, error) {
					return "", fmt.Errorf("dial tcp: i/o timeout")
				}
			},
			wantSub: []string{"could not reach its own callback URL", "listening anyway"},
		},
		{
			name: "something other than polyemesis answers the URL",
			cfg: func(c *KickConfig) {
				c.Probe = func(context.Context, string) (string, error) { return "hello from nginx", nil }
			},
			wantSub: []string{"something other than polyemesis"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := kickAdapter(t, tc.cfg)
			ok, detail := a.checkReachable(context.Background())
			if ok {
				t.Fatal("an unreachable callback was reported as fine")
			}
			for _, want := range tc.wantSub {
				if !strings.Contains(detail, want) {
					t.Fatalf("detail %q does not mention %q", detail, want)
				}
			}
		})
	}
}

// An unreachable callback must never stop the adapter: a tunnel or a NAT rule
// can make a URL work that looks impossible from in here.
func TestKickKeepsListeningEvenWhenItLooksUnreachable(t *testing.T) {
	a := kickAdapter(t, func(c *KickConfig) { c.PublicURL = "" })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan Message, 1)
	go func() { _ = a.Run(ctx, SinkFunc(func(m Message) { got <- m })) }()

	waitFor(t, "the degraded state to be reported", func() bool {
		return a.Health().State == StateDegraded
	})

	a.ingest("chat.message.sent", []byte(kickFullEvent))
	select {
	case m := <-got:
		if m.Text != "hello kick" {
			t.Fatalf("delivered %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a delivery was dropped because the reachability check was pessimistic")
	}
}

func TestKickSilenceIsReportedWithTheURLToConfigure(t *testing.T) {
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	a := kickAdapter(t, func(c *KickConfig) { c.Now = func() time.Time { return clock } })

	a.mu.Lock()
	a.reachable = true
	a.startedAt = clock
	a.mu.Unlock()

	clock = clock.Add(kickSilenceAfter + time.Minute)
	a.refreshHealth()

	h := a.Health()
	if h.State != StateDegraded {
		t.Fatalf("state = %q, want degraded after a long silence", h.State)
	}
	if !strings.Contains(h.Detail, a.CallbackURL()) {
		t.Fatalf("detail %q does not include the URL to paste into Kick", h.Detail)
	}
	if !strings.Contains(h.Detail, "Sending still works") {
		t.Fatalf("detail %q does not say what still works", h.Detail)
	}
}

func TestKickCallbackPathCarriesAnUnguessableSecret(t *testing.T) {
	a := kickAdapter(t, nil)
	b := kickAdapter(t, nil)

	if a.CallbackPath() == b.CallbackPath() {
		t.Fatal("two adapters generated the same callback path")
	}
	if !strings.HasPrefix(a.CallbackPath(), "/api/v1/chat/kick/") {
		t.Fatalf("path = %q", a.CallbackPath())
	}
	if a.CallbackURL() != "https://stream.example.com"+a.CallbackPath() {
		t.Fatalf("url = %q", a.CallbackURL())
	}

	// A configured secret is used verbatim, so the URL survives a restart.
	c := kickAdapter(t, func(cfg *KickConfig) { cfg.CallbackSecret = "fixed-secret" })
	if !strings.HasSuffix(c.CallbackPath(), "/fixed-secret") {
		t.Fatalf("path = %q, want the configured secret", c.CallbackPath())
	}
}

func TestKickSend(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/public/v1/chat" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer kick-token" {
			t.Errorf("authorization = %q", got)
		}
		json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"data":{"is_sent":true,"message_id":"sent-5"}}`)
	}))
	defer srv.Close()

	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL })
	m, err := a.Send(context.Background(), "hello kick")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "sent-5" {
		t.Fatalf("echo id = %q, want Kick's own id so the webhook copy deduplicates", m.ID)
	}
	if body["content"] != "hello kick" || body["type"] != "user" {
		t.Fatalf("request body = %+v", body)
	}
	if fmt.Sprint(body["broadcaster_user_id"]) != "99" {
		t.Fatalf("broadcaster id = %v", body["broadcaster_user_id"])
	}
}

func TestKickSendRefusalsAreActionable(t *testing.T) {
	tests := []struct {
		name    string
		cfg     func(*KickConfig)
		text    string
		wantSub string
	}{
		{
			name:    "an overlong message names the limit",
			text:    strings.Repeat("a", KickMaxMessage+1),
			wantSub: "500",
		},
		{
			name:    "a missing broadcaster id says to reconnect",
			cfg:     func(c *KickConfig) { c.BroadcasterUserID = 0; c.AccountRef = "x" },
			text:    "hello",
			wantSub: "reconnect",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := kickAdapter(t, tc.cfg)
			_, err := a.Send(context.Background(), tc.text)
			if err == nil {
				t.Fatal("the send was accepted")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err, tc.wantSub)
			}
		})
	}
}

func TestKickDeleteTurnsAScopeRefusalIntoAnInstruction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"insufficient scope"}`)
	}))
	defer srv.Close()

	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL })
	err := a.Delete(context.Background(), "msg-1")
	if err == nil {
		t.Fatal("a refused deletion reported success")
	}
	if !strings.Contains(err.Error(), "moderation:chat_message:manage") {
		t.Fatalf("error %q does not name the missing scope", err)
	}
	if strings.Contains(err.Error(), "kick-token") {
		t.Fatal("the access token leaked into the error")
	}
}

func TestNewKickWithoutATokenIsAConfigurationState(t *testing.T) {
	_, err := NewKick(KickConfig{})
	if err == nil {
		t.Fatal("a missing token was accepted")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("error %q does not read as a configuration state", err)
	}
}
