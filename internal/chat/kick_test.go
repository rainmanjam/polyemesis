package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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
		CallbackPath:      func(secret string) string { return "/api/v1/chat/kick/" + secret },
		Now:               fixedClock(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)),
		// Reachable by default so a test that is not about reachability does
		// not have to say so.
		Probe: func(_ context.Context, endpoint string) (string, error) {
			_, nonce, _ := strings.Cut(endpoint, "?probe=")
			return nonce, nil
		},
		// The real verifier against the package test key, not a permissive
		// stub. Every handler test therefore exercises signature checking on
		// the way in, and a test that posts an unsigned body gets the 401 a
		// stranger would — which is the behaviour worth defending.
		Verify: func(r *http.Request, body []byte) error {
			return VerifyKickSignature(&testKickKey(t).PublicKey, r, body)
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
		signKick(t, testKickKey(t), req, []byte(body))
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

func TestKickHandlerRefusesEveryDeliveryWhenNoVerifierIsConfigured(t *testing.T) {
	// The regression guard for the finding this whole change exists to fix.
	//
	// A nil Verify used to mean "skip the check", so a construction site that
	// forgot to set it produced an endpoint anybody could post chat to. It now
	// means "refuse", which turns the same omission into an obvious outage
	// instead of a silent hole. This test fails if that inverts back.
	a := kickAdapter(t, func(c *KickConfig) { c.Verify = nil })
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL, "application/json", strings.NewReader(kickFullEvent))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: an unconfigured verifier must refuse "+
			"the delivery, not accept it unverified", resp.StatusCode)
	}
}

func TestKickHandlerRejectsAnUnsignedDelivery(t *testing.T) {
	// What a stranger who found the callback URL would actually send.
	a := kickAdapter(t, nil) // the default fixture verifies for real
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL, "application/json", strings.NewReader(kickFullEvent))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unsigned POST returned %d, want 401", resp.StatusCode)
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

func TestKickSilenceIsReportedAndPointsAtTheURL(t *testing.T) {
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
	// The URL carries the path secret and health text is broadcast to every
	// chat pane, so the sentence points at where the URL is shown instead.
	if strings.Contains(h.Detail, a.CallbackPath()) || strings.Contains(h.Detail, "/chat/kick/") {
		t.Fatalf("detail %q carries the webhook secret path", h.Detail)
	}
	if !strings.Contains(h.Detail, "Settings") {
		t.Fatalf("detail %q does not say where the URL to paste into Kick is shown", h.Detail)
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

// KICK COUNTS IN MINUTES. YouTube and Twitch count in seconds.
//
// This is the single most dangerous detail in the moderation work: a unified API
// taking a bare "600" would mean ten minutes on two platforms and SEVEN DAYS
// here, and nothing anywhere would report an error. The interface takes a
// time.Duration so the unit only exists at the point the request is built, and
// this test is what holds that point honest.
func TestKickBanConvertsSecondsToMinutes(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want any // the "duration" field, or nil when it must be absent
	}{
		{"ten minutes is 10, not 600", 10 * time.Minute, float64(10)},
		{"an hour is 60", time.Hour, float64(60)},
		// Rounded UP. Truncating to zero would mean PERMANENT, which is not a
		// rounding error anybody recovers from.
		{"thirty seconds rounds up to one minute, never zero", 30 * time.Second, float64(1)},
		{"one second rounds up to one minute", time.Second, float64(1)},
		{"zero is permanent, so no duration is sent at all", 0, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewDecoder(r.Body).Decode(&body)
				fmt.Fprint(w, `{"data":{}}`)
			}))
			defer srv.Close()

			a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL })
			if err := a.Ban(context.Background(), "77", tc.d, ""); err != nil {
				t.Fatal(err)
			}
			got, present := body["duration"]
			if tc.want == nil {
				if present {
					t.Fatalf("duration = %v was sent for a permanent ban; Kick reads a duration as a TIMEOUT", got)
				}
				return
			}
			if !present {
				t.Fatalf("no duration sent for %s; Kick would read that as a permanent ban", tc.d)
			}
			if got != tc.want {
				t.Fatalf("duration = %v minutes for %s, want %v", got, tc.d, tc.want)
			}
		})
	}
}

// Kick's documented ceiling is 10080 minutes. Past it the API rejects the
// request, so silently converting to a permanent ban would be the worst possible
// interpretation of "timeout for a very long time".
func TestKickBanRefusesBeyondSevenDaysRatherThanBanningForever(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL })
	err := a.Ban(context.Background(), "77", 8*24*time.Hour, "")
	if err == nil {
		t.Fatal("a timeout longer than Kick's maximum was accepted")
	}
	if called {
		t.Fatal("the request was sent anyway")
	}
	if !strings.Contains(err.Error(), "permanent") {
		t.Fatalf("error = %q, want it to name the alternative the operator actually has", err)
	}
}

// Kick's ids are integers in JSON. A quoted id fails as a validation error that
// names neither field, so it is caught here where the message can be useful.
func TestKickBanRejectsANonNumericUserID(t *testing.T) {
	a := kickAdapter(t, nil)
	if err := a.Ban(context.Background(), "someone", time.Minute, ""); err == nil {
		t.Fatal("a non-numeric user id was accepted")
	}
}

func TestNewKickWithoutACallbackPathBuilderIsRefused(t *testing.T) {
	_, err := NewKick(KickConfig{Token: StaticToken("t")})
	if err == nil || !strings.Contains(err.Error(), "callback path") {
		t.Fatalf("a missing callback path builder was accepted: %v", err)
	}
}

// Kick counts 500 grapheme clusters AND caps the content at 2048 UTF-8 bytes
// (docs.kick.com/apis/chat). Counting runes gets both wrong on emoji.
func TestKickSendLengthIsGraphemesAndBytes(t *testing.T) {
	const (
		family = "\U0001F468‍\U0001F469‍\U0001F467‍\U0001F466" // 1 grapheme, 7 runes, 25 bytes
		flag   = "\U0001F1FA\U0001F1F8"                        // 1 grapheme, 2 runes, 8 bytes
		eAcute = "é"                                          // 1 grapheme, 2 runes, 3 bytes
	)
	tests := []struct {
		name    string
		text    string
		wantErr string // empty = accepted
	}{
		{"500 ascii is the limit", strings.Repeat("a", 500), ""},
		{"501 ascii is refused on characters", strings.Repeat("a", 501), "500 characters"},
		{"combining marks count once, so 500 clusters of 2 runes pass", strings.Repeat(eAcute, 500), ""},
		{"501 combining clusters are refused on characters", strings.Repeat(eAcute, 501), "500 characters"},
		{"500 flags (4000 bytes) are under 500 clusters but over the byte cap", strings.Repeat(flag, 500), "2048 bytes"},
		{"250 flags are 2000 bytes and pass", strings.Repeat(flag, 250), ""},
		{"257 flags are 2056 bytes and are refused", strings.Repeat(flag, 257), "2048 bytes"},
		{"82 family emoji are 2050 bytes and are refused", strings.Repeat(family, 82), "2048 bytes"},
		{"81 family emoji are 2025 bytes and pass", strings.Repeat(family, 81), ""},
		{"500 four-byte emoji are 2000 bytes and pass", strings.Repeat("\U0001F600", 500), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sent := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sent = true
				fmt.Fprint(w, `{"data":{"is_sent":true,"message_id":"m"}}`)
			}))
			defer srv.Close()
			a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL })
			_, err := a.Send(context.Background(), tc.text)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("an over-limit message was accepted")
			}
			if sent {
				t.Error("an over-limit message reached Kick")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

// ------------------------------------------------------------ subscription

// fakeKickEvents is Kick's /public/v1/events/subscriptions: it remembers what
// was created, can be told to forget it (Kick auto-disables subscriptions after
// a day of failed deliveries, and disabling webhooks drops every one), and
// records every request so a test can say what did and did not happen.
type fakeKickEvents struct {
	mu       sync.Mutex
	subs     []map[string]any
	requests []string
	posts    []map[string]any
	actPosts []map[string]any
	// postStatus/postBody override the POST answer; 0 means 200 and success.
	postStatus int
	postBody   string
	getStatus  int
	auth       []string
	listEmpty  bool
	// reject4xx: any POST naming one of these events is a 400 in Kick's words.
	// refuse: a 200 whose item for the event carries this error (Kick's way of
	// saying "not for this account").
	reject4xx map[string]string
	refuse    map[string]string
}

// forget drops our subscription but leaves an unrelated one in the listing:
// the listing is non-empty and lacks ours, which is positive evidence it is gone.
func (f *fakeKickEvents) forget() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs = []map[string]any{{"id": "other", "event": "channel.followed", "version": 1, "method": "webhook", "broadcaster_user_id": 99}}
}

// empty makes the listing come back empty whatever exists (Kick issue #419: GET
// can omit existing chat.message.sent subscriptions).
func (f *fakeKickEvents) empty() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listEmpty = true
}

func (f *fakeKickEvents) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeKickEvents) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if r.URL.Path != "/public/v1/events/subscriptions" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if f.getStatus != 0 {
			w.WriteHeader(f.getStatus)
			fmt.Fprint(w, `{"message":"nope"}`)
			return
		}
		if r.URL.Query().Get("broadcaster_user_id") != "99" {
			http.Error(w, "wrong broadcaster", http.StatusBadRequest)
			return
		}
		data := f.subs
		if f.listEmpty {
			data = nil
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data, "message": "OK"})
	case http.MethodPost:
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		// Chat and activity subscriptions are separate requests. Activity ones
		// are logged as POST-ACT so the chat tests below keep counting what they
		// always counted: how often CHAT was subscribed.
		if evs, _ := body["events"].([]any); !postsChat(evs) {
			f.requests[len(f.requests)-1] = "POST-ACT " + r.URL.Path
			f.actPosts = append(f.actPosts, body)
		} else {
			f.posts = append(f.posts, body)
		}
		for _, e := range body["events"].([]any) {
			if msg, bad := f.reject4xx[e.(map[string]any)["name"].(string)]; bad {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `{"message":%q}`, msg)
				return
			}
		}
		if f.postStatus != 0 {
			w.WriteHeader(f.postStatus)
			fmt.Fprint(w, f.postBody)
			return
		}
		if f.postBody != "" {
			fmt.Fprint(w, f.postBody)
			return
		}
		evs, _ := body["events"].([]any)
		var data []map[string]any
		for i, e := range evs {
			em := e.(map[string]any)
			if msg, no := f.refuse[em["name"].(string)]; no {
				data = append(data, map[string]any{"name": em["name"], "version": em["version"], "error": msg})
				continue
			}
			f.subs = append(f.subs, map[string]any{
				"id": fmt.Sprintf("sub-%d-%d", len(f.posts), i), "event": em["name"], "version": em["version"],
				"method": "webhook", "broadcaster_user_id": 99, "app_id": "app",
			})
			data = append(data, map[string]any{"name": em["name"], "version": em["version"], "subscription_id": "x", "error": ""})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data, "message": "OK"})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func postsChat(evs []any) bool {
	for _, e := range evs {
		if m, ok := e.(map[string]any); ok && m["name"] == "chat.message.sent" {
			return true
		}
	}
	return false
}

// runKick starts the adapter's Run and returns a stop that waits for it.
func runKick(t *testing.T, a *KickAdapter) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = a.Run(ctx, SinkFunc(func(Message) {})) }()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("Run did not return after its context ended")
		}
	}
}

func TestKickRunCreatesTheChatSubscriptionWhenMissing(t *testing.T) {
	f := &fakeKickEvents{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL; c.Tick = 5 * time.Millisecond })
	stop := runKick(t, a)
	defer stop()

	waitFor(t, "the subscription POST", func() bool { return f.count("POST ") == 1 })
	f.mu.Lock()
	post := f.posts[0]
	auth := f.auth[0]
	f.mu.Unlock()
	if auth != "Bearer kick-token" {
		t.Errorf("authorization = %q", auth)
	}
	if post["method"] != "webhook" {
		t.Errorf("method = %v, want webhook", post["method"])
	}
	evs, _ := post["events"].([]any)
	if len(evs) != 1 {
		t.Fatalf("events = %v, want exactly chat.message.sent", post["events"])
	}
	ev := evs[0].(map[string]any)
	if ev["name"] != "chat.message.sent" || fmt.Sprint(ev["version"]) != "1" {
		t.Errorf("event = %v", ev)
	}
	if fmt.Sprint(post["broadcaster_user_id"]) != "99" {
		t.Errorf("broadcaster_user_id = %v", post["broadcaster_user_id"])
	}
	// GET came first: creation is conditional on absence.
	f.mu.Lock()
	first := f.requests[0]
	f.mu.Unlock()
	if first != "GET /public/v1/events/subscriptions" {
		t.Errorf("first request = %q, want the GET", first)
	}
	waitFor(t, "a live state", func() bool { return a.Health().State == StateLive })
}

func TestKickRunDoesNotDuplicateAnExistingSubscription(t *testing.T) {
	f := &fakeKickEvents{subs: []map[string]any{
		{"id": "s1", "event": "chat.message.sent", "version": 1, "method": "webhook", "broadcaster_user_id": 99},
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL; c.Tick = 5 * time.Millisecond })
	stop := runKick(t, a)
	waitFor(t, "the subscription lookup", func() bool { return f.count("GET ") >= 1 })
	waitFor(t, "live", func() bool { return a.Health().State == StateLive })
	stop()
	if n := f.count("POST "); n != 0 {
		t.Fatalf("%d POSTs against an existing subscription; creation is not idempotent", n)
	}
}

// Another event type (or an older chat version) is not a chat subscription.
func TestKickRunIgnoresSubscriptionsThatAreNotChatV1(t *testing.T) {
	f := &fakeKickEvents{subs: []map[string]any{
		{"id": "s1", "event": "channel.followed", "version": 1, "method": "webhook", "broadcaster_user_id": 99},
		{"id": "s2", "event": "chat.message.sent", "version": 2, "method": "webhook", "broadcaster_user_id": 99},
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL; c.Tick = 5 * time.Millisecond })
	stop := runKick(t, a)
	defer stop()
	waitFor(t, "the POST", func() bool { return f.count("POST ") == 1 })
}

func TestKickRunSurfacesAWebhookNotEnabledFailure(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"403 from Kick":                  {http.StatusForbidden, `{"message":"webhooks are not enabled for this app"}`},
		"400 shows Kick's text":          {http.StatusBadRequest, `{"message":"events: version is invalid"}`},
		"200 with a per-event error":     {0, `{"data":[{"name":"chat.message.sent","version":1,"error":"webhook not enabled"}],"message":"OK"}`},
		"500 is reported, not swallowed": {http.StatusInternalServerError, `{"message":"boom"}`},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeKickEvents{postStatus: tc.status, postBody: tc.body}
			srv := httptest.NewServer(f)
			defer srv.Close()
			a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL; c.Tick = 5 * time.Millisecond })
			stop := runKick(t, a)
			defer stop()
			waitFor(t, "a degraded state naming the subscription", func() bool {
				h := a.Health()
				return h.State == StateDegraded && strings.Contains(h.Detail, "subscri")
			})
			h := a.Health()
			switch tc.status {
			case http.StatusForbidden, 0:
				if !strings.Contains(h.Detail, "Enable webhooks") {
					t.Errorf("detail %q does not say how to enable webhooks", h.Detail)
				}
			case http.StatusBadRequest:
				// A 400 is Kick objecting to the REQUEST, not a missing
				// webhook: show Kick's words, and do not claim webhooks are off.
				if !strings.Contains(h.Detail, "events: version is invalid") {
					t.Errorf("detail %q does not show Kick's error text", h.Detail)
				}
				if strings.Contains(h.Detail, "webhook not enabled") {
					t.Errorf("detail %q blames webhooks for a 400", h.Detail)
				}
			}
			if strings.Contains(h.Detail, "/api/v1/chat/kick/") {
				t.Errorf("detail %q carries the webhook secret path", h.Detail)
			}
		})
	}
}

// recorded is what the adapter has written down about its last check. A test
// that advances a fake clock must wait for THIS, not for the request to reach
// the fake server: the request arrives before ensureSubscription stamps
// subCheckedAt, and advancing in that gap makes the stamp already "due".
func recorded(a *KickAdapter) (checked bool, at time.Time, problem string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.subChecked, a.subCheckedAt, a.subProblem
}

func TestKickRunRecreatesAVanishedSubscription(t *testing.T) {
	f := &fakeKickEvents{}
	srv := httptest.NewServer(f)
	defer srv.Close()

	var mu sync.Mutex
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	advance := func(d time.Duration) { mu.Lock(); clock = clock.Add(d); mu.Unlock() }

	a := kickAdapter(t, func(c *KickConfig) {
		c.APIBase = srv.URL
		c.Tick = 5 * time.Millisecond
		c.Now = now
	})
	stop := runKick(t, a)
	defer stop()
	waitFor(t, "the first POST", func() bool { return f.count("POST ") == 1 })
	start := now()
	waitFor(t, "the adapter to record the check", func() bool {
		ok, at, problem := recorded(a)
		return ok && problem == "" && at.Equal(start)
	})

	// Inside the recheck window nothing more happens, however many ticks pass.
	f.forget()
	advance(10 * time.Minute)
	time.Sleep(60 * time.Millisecond)
	if n := f.count("POST "); n != 1 {
		t.Fatalf("%d POSTs before the recheck interval elapsed", n)
	}

	// Past it the vanished subscription is found missing and created again.
	advance(25 * time.Minute)
	waitFor(t, "the re-creation", func() bool { return f.count("POST ") == 2 })
}

// A failed check is retried far sooner than the 30-minute cadence, so a
// network blip at start does not leave chat unsubscribed for half an hour.
func TestKickRunRetriesAFailedCheckSoon(t *testing.T) {
	f := &fakeKickEvents{getStatus: http.StatusInternalServerError}
	srv := httptest.NewServer(f)
	defer srv.Close()
	var mu sync.Mutex
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	a := kickAdapter(t, func(c *KickConfig) {
		c.APIBase = srv.URL
		c.Tick = 5 * time.Millisecond
		c.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	})
	stop := runKick(t, a)
	defer stop()
	waitFor(t, "the first lookup", func() bool { return f.count("GET ") == 1 })
	waitFor(t, "the adapter to record the failed check", func() bool {
		ok, _, problem := recorded(a)
		return ok && problem != ""
	})
	f.mu.Lock()
	f.getStatus = 0
	f.mu.Unlock()
	mu.Lock()
	clock = clock.Add(3 * time.Minute)
	mu.Unlock()
	waitFor(t, "the retry to create it", func() bool { return f.count("POST ") == 1 })
}

// Subscriptions persist per app and broadcaster, so deleting on every restart
// would drop chat events delivered in between. Nothing is deleted on shutdown.
func TestKickRunNeverDeletesTheSubscriptionOnShutdown(t *testing.T) {
	f := &fakeKickEvents{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL; c.Tick = 5 * time.Millisecond })
	stop := runKick(t, a)
	waitFor(t, "the POST", func() bool { return f.count("POST ") == 1 })
	stop()
	if n := f.count("DELETE "); n != 0 {
		t.Fatalf("%d DELETE requests on shutdown", n)
	}
}

// ---- review fixes: duplicates, secret in health text

// Kick issue #419 (KickEngineering/KickDevDocs): GET can omit an existing
// chat.message.sent subscription. An empty listing alone must not make a 30
// minute POST loop, so once polyemesis has created the subscription it trusts
// it through an empty listing until a long backoff.
func TestKickRunDoesNotRePostOnAnEmptyListingAfterCreating(t *testing.T) {
	f := &fakeKickEvents{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	var mu sync.Mutex
	clock := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	advance := func(d time.Duration) { mu.Lock(); clock = clock.Add(d); mu.Unlock() }
	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL; c.Tick = 5 * time.Millisecond; c.Now = now })
	stop := runKick(t, a)
	defer stop()
	waitFor(t, "the first POST", func() bool { return f.count("POST ") == 1 })
	start := now()
	waitFor(t, "recorded", func() bool { ok, at, p := recorded(a); return ok && p == "" && at.Equal(start) })

	f.empty()
	gets := f.count("GET ")
	advance(kickSubRecheck + time.Minute)
	waitFor(t, "the recheck", func() bool { return f.count("GET ") > gets })
	waitFor(t, "the recheck recorded", func() bool { _, at, _ := recorded(a); return at.Equal(now()) })
	if n := f.count("POST "); n != 1 {
		t.Fatalf("%d POSTs after an empty listing; Kick's GET can omit a live subscription", n)
	}
	if _, _, p := recorded(a); p != "" {
		t.Fatalf("an empty listing was recorded as a subscription problem: %q", p)
	}

	// After the long backoff it is created again (it may really be gone).
	gets = f.count("GET ")
	advance(kickSubEmptyBackoff + time.Minute)
	waitFor(t, "the re-creation after the backoff", func() bool { return f.count("POST ") == 2 })
}

// Never created by us and the listing is empty: this is the first run, so create.
// If Kick's GET omitted a subscription that exists, Kick's refusal as a
// duplicate is success, not a degraded card.
func TestKickRunTreatsADuplicateAnswerAsSuccess(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"400 already exists":  {http.StatusBadRequest, `{"message":"subscription already exists"}`},
		"409 conflict":        {http.StatusConflict, `{"message":"duplicate subscription"}`},
		"200 per-item exists": {0, `{"data":[{"name":"chat.message.sent","version":1,"error":"Subscription already exists"}],"message":"OK"}`},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeKickEvents{postStatus: tc.status, postBody: tc.body}
			srv := httptest.NewServer(f)
			defer srv.Close()
			a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL; c.Tick = 5 * time.Millisecond })
			stop := runKick(t, a)
			defer stop()
			waitFor(t, "the POST", func() bool { return f.count("POST ") == 1 })
			waitFor(t, "recorded", func() bool { ok, _, _ := recorded(a); return ok })
			if _, _, p := recorded(a); p != "" {
				t.Fatalf("a duplicate answer was reported as a problem: %q", p)
			}
			waitFor(t, "live", func() bool { return a.Health().State == StateLive })
		})
	}
}

// A subscription that belongs to a different broadcaster does not count as ours.
func TestKickRunOnlyCountsASubscriptionForItsOwnBroadcaster(t *testing.T) {
	f := &fakeKickEvents{subs: []map[string]any{
		{"id": "s1", "event": "chat.message.sent", "version": 1, "method": "webhook", "broadcaster_user_id": 12345},
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL; c.Tick = 5 * time.Millisecond })
	stop := runKick(t, a)
	defer stop()
	waitFor(t, "the POST for our own channel", func() bool { return f.count("POST ") == 1 })
}

// *url.Error prints the whole URL, and the callback URL's path is a secret.
// The reachability warning ends up in the health text the hub publishes.
func TestKickReachabilityWarningNeverCarriesTheCallbackSecret(t *testing.T) {
	// Built at run time: a 32-character literal under the name "secret" is what
	// the repository secret scan flags, and this is a made-up value.
	secret := strings.Repeat("c0ffee", 5)
	a := kickAdapter(t, func(c *KickConfig) {
		c.CallbackSecret = secret
		c.Probe = func(ctx context.Context, endpoint string) (string, error) {
			return "", &url.Error{Op: "Get", URL: endpoint, Err: errors.New("connection refused")}
		}
	})
	_, detail := a.checkReachable(context.Background())
	if strings.Contains(detail, secret) || strings.Contains(detail, "/chat/kick/") {
		t.Fatalf("the reachability warning carries the webhook secret: %q", detail)
	}
	if !strings.Contains(detail, "connection refused") {
		t.Fatalf("the warning lost the cause: %q", detail)
	}
}

// A hostname that resolves to this machine (Ubuntu's 127.0.1.1) makes the self-probe fail without leaving the box,
// which says nothing about whether Kick can reach the URL.
func TestKickSelfProbeThatDialedLoopbackIsInconclusiveNotDegraded(t *testing.T) {
	dial := func(ip net.IP) error {
		return &url.Error{Op: "Get", URL: "https://host.example/x", Err: &net.OpError{
			Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: ip, Port: 443}, Err: errors.New("connect: connection refused")}}
	}
	for name, err := range map[string]error{
		"127.0.1.1": dial(net.ParseIP("127.0.1.1")),
		"::1":       dial(net.ParseIP("::1")),
		"ipaddr":    &net.OpError{Op: "dial", Addr: &net.IPAddr{IP: net.ParseIP("127.0.0.1")}, Err: errors.New("refused")},
	} {
		t.Run(name, func(t *testing.T) {
			a := kickAdapter(t, func(c *KickConfig) {
				c.Probe = func(context.Context, string) (string, error) { return "", err }
			})
			if ok, detail := a.checkReachable(context.Background()); !ok || detail != "" {
				t.Fatalf("a loopback self-probe read as degraded: ok=%v detail=%q", ok, detail)
			}
		})
	}
}

func TestKickSelfProbeThatDialedAPublicAddressStillWarns(t *testing.T) {
	for name, err := range map[string]error{
		"public ip": &net.OpError{Op: "dial", Addr: &net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 443}, Err: errors.New("connection refused")},
		"read op":   &net.OpError{Op: "read", Addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1")}, Err: errors.New("reset")},
		"no addr":   &net.OpError{Op: "dial", Err: errors.New("no route")},
	} {
		t.Run(name, func(t *testing.T) {
			a := kickAdapter(t, func(c *KickConfig) {
				c.Probe = func(context.Context, string) (string, error) { return "", err }
			})
			ok, detail := a.checkReachable(context.Background())
			if ok || !strings.Contains(detail, "could not reach its own callback URL") {
				t.Fatalf("a real unreachable probe was swallowed: ok=%v detail=%q", ok, detail)
			}
		})
	}
}
