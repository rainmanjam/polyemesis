package activityhub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/twitch/eventsub"
)

// The fake Twitch: one httptest server that speaks the EventSub WebSocket
// protocol on /ws and /ws2 (the reconnect target) and the two Helix calls the
// source makes. Payloads are the shapes in Twitch's reference, in
// twitch_payloads_test.go.

const fakeBroadcaster = "1337"

var allScopes = []string{
	"moderator:read:followers", "channel:read:subscriptions", "bits:read",
	"channel:read:redemptions", "channel:read:hype_train", "channel:read:predictions",
	"channel:manage:polls", "moderator:manage:shoutouts",
}

type fakeConn struct {
	c       *websocket.Conn
	wmu     sync.Mutex
	inbound atomic.Int32 // text frames the client sent: must stay 0
	closed  chan struct{}
	path    string
}

func (c *fakeConn) send(s string) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.c.WriteMessage(websocket.TextMessage, []byte(s))
}

func (c *fakeConn) closeWith(code int) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, ""), time.Now().Add(time.Second))
}

type fakeTwitch struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	conns     []*fakeConn
	posts     []string         // subscription types created, in order
	postBody  []map[string]any // decoded request bodies
	paths     []string         // every Helix path hit
	tier      string
	status    map[string]int    // type -> forced answer status
	reset     map[string]string // type -> Ratelimit-Reset header
	userCode  int               // forced status for GET /users, 0 = ok
	sessions  int
	keepalive int
	hits      map[string]int
}

func newFakeTwitch(t *testing.T) *fakeTwitch {
	t.Helper()
	f := &fakeTwitch{t: t, tier: "affiliate", status: map[string]int{}, reset: map[string]string{}, keepalive: 30, hits: map[string]int{}}
	mux := http.NewServeMux()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	ws := func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		fc := &fakeConn{c: c, closed: make(chan struct{}), path: r.URL.Path}
		f.mu.Lock()
		f.conns = append(f.conns, fc)
		f.sessions++
		n := f.sessions
		ka := f.keepalive
		f.mu.Unlock()
		fc.send(welcome(fmt.Sprintf("sess-%d", n), ka))
		go func() {
			defer close(fc.closed)
			for {
				mt, _, err := c.ReadMessage()
				if err != nil {
					return
				}
				if mt == websocket.TextMessage {
					fc.inbound.Add(1)
				}
			}
		}()
	}
	mux.HandleFunc("/ws", ws)
	mux.HandleFunc("/ws2", ws)
	mux.HandleFunc("/helix/users", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		code, tier := f.userCode, f.tier
		f.mu.Unlock()
		if code != 0 {
			w.WriteHeader(code)
			return
		}
		fmt.Fprintf(w, `{"data":[{"id":%q,"login":"cooler_user","broadcaster_type":%q}]}`, r.URL.Query().Get("id"), tier)
	})
	mux.HandleFunc("/helix/eventsub/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		typ, _ := body["type"].(string)
		f.mu.Lock()
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		f.hits[typ]++
		code := f.status[typ]
		rst := f.reset[typ]
		// A forced status that carries a reset is answered once, then clears.
		if rst != "" {
			delete(f.status, typ)
			delete(f.reset, typ)
		}
		if code == 0 || code/100 == 2 {
			f.posts = append(f.posts, typ)
			f.postBody = append(f.postBody, body)
		}
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("Client-Id") != "cid" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if code != 0 && code/100 != 2 {
			if rst != "" {
				w.Header().Set("Ratelimit-Reset", rst)
			}
			w.WriteHeader(code)
			fmt.Fprint(w, `{"error":"x","status":`+strconv.Itoa(code)+`,"message":"VIEWER-TEXT-SHOULD-NEVER-BE-LOGGED"}`)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"data":[{"id":"sub-%s","status":"enabled","type":%q,"version":"1","cost":0}],"total":1,"total_cost":0,"max_total_cost":10}`, typ, typ)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTwitch) wsURL() string  { return "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/ws" }
func (f *fakeTwitch) ws2URL() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/ws2" }
func (f *fakeTwitch) helix() string  { return f.srv.URL + "/helix" }

func (f *fakeTwitch) conn(i int) *fakeConn {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		if len(f.conns) > i {
			c := f.conns[i]
			f.mu.Unlock()
			return c
		}
		f.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	f.t.Fatalf("connection %d never arrived", i)
	return nil
}

func (f *fakeTwitch) posted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.posts...)
}

func (f *fakeTwitch) hitCount(typ string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[typ]
}

func (f *fakeTwitch) postedType(typ string) bool {
	for _, p := range f.posted() {
		if p == typ {
			return true
		}
	}
	return false
}

// --- message builders -------------------------------------------------------

func welcome(id string, keepalive int) string {
	return fmt.Sprintf(`{"metadata":{"message_id":"w-%s","message_type":"session_welcome","message_timestamp":%q},`+
		`"payload":{"session":{"id":%q,"status":"connected","keepalive_timeout_seconds":%d,"reconnect_url":null,"connected_at":%q}}}`,
		id, nowStamp(), id, keepalive, nowStamp())
}

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func note(msgID, typ, version, event string) string {
	return noteAt(msgID, typ, version, event, nowStamp())
}

func noteAt(msgID, typ, version, event, stamp string) string {
	return fmt.Sprintf(`{"metadata":{"message_id":%q,"message_type":"notification","message_timestamp":%q,"subscription_type":%q,"subscription_version":%q},`+
		`"payload":{"subscription":{"id":"f1c2a387-161a-49f9-a165-0f21d7a4e1c4","type":%q,"version":%q,"status":"enabled","cost":0,"condition":{"broadcaster_user_id":%q},"transport":{"method":"websocket","session_id":"AQoQ"},"created_at":"2019-11-16T10:11:12.634234626Z"},"event":%s}}`,
		msgID, stamp, typ, version, typ, version, fakeBroadcaster, event)
}

func keepaliveMsg(id string) string {
	return fmt.Sprintf(`{"metadata":{"message_id":%q,"message_type":"session_keepalive","message_timestamp":%q},"payload":{}}`, id, nowStamp())
}

func reconnectMsg(id, url string) string {
	return fmt.Sprintf(`{"metadata":{"message_id":%q,"message_type":"session_reconnect","message_timestamp":%q},`+
		`"payload":{"session":{"id":"AQoQexAWVYKSTIu4ec_2VAxyuhAB","status":"reconnecting","keepalive_timeout_seconds":null,"reconnect_url":%q,"connected_at":%q}}}`,
		id, nowStamp(), url, nowStamp())
}

func revocationMsg(id, typ, status string) string {
	return fmt.Sprintf(`{"metadata":{"message_id":%q,"message_type":"revocation","message_timestamp":%q,"subscription_type":%q,"subscription_version":"1"},`+
		`"payload":{"subscription":{"id":"s1","status":%q,"type":%q,"version":"1","cost":1,"condition":{"broadcaster_user_id":%q},"transport":{"method":"websocket","session_id":"AQoQ"},"created_at":"2019-11-16T10:11:12.634234626Z"}}}`,
		id, nowStamp(), typ, status, typ, fakeBroadcaster)
}

// --- harness ----------------------------------------------------------------

type twRig struct {
	t      *testing.T
	f      *fakeTwitch
	hub    *Hub
	src    *Twitch
	logs   *syncBuf
	cancel context.CancelFunc
}

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

var _ io.Writer = (*syncBuf)(nil)

func twConfig(f *fakeTwitch, logs io.Writer) TwitchConfig {
	return TwitchConfig{
		AccountRef:         fakeBroadcaster,
		ClientID:           "cid",
		Token:              func(context.Context) (string, error) { return "tok", nil },
		Scopes:             func() []string { return allScopes },
		Mirrors:            &MirrorLedger{},
		Log:                slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		WSURL:              f.wsURL(),
		HelixBase:          f.helix(),
		AllowReconnectHost: func(string) error { return nil },
		KeepaliveGrace:     200 * time.Millisecond,
		PollHold:           400 * time.Millisecond,
	}
}

// startTwitch attaches a Twitch source built from cfg to a REAL hub, so the hub's
// own dedupe and validation are exercised.
func startTwitch(t *testing.T, f *fakeTwitch, mod func(*TwitchConfig)) *twRig {
	t.Helper()
	logs := &syncBuf{}
	cfg := twConfig(f, logs)
	if mod != nil {
		mod(&cfg)
	}
	hub := New(
		WithLogger(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))),
		WithBackoff(func(int) time.Duration { return 20 * time.Millisecond }),
	)
	t.Cleanup(hub.Close)
	src := NewTwitch(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := hub.Attach(ctx, src); err != nil {
		t.Fatal(err)
	}
	return &twRig{t: t, f: f, hub: hub, src: src, logs: logs, cancel: cancel}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	// A safety net on an event wait: cond is polled and the wait ends the
	// moment it holds, so only a hang costs the full 30s.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ready waits until the source has finished subscribing.
func (r *twRig) ready() *fakeConn {
	r.t.Helper()
	c := r.f.conn(0)
	waitFor(r.t, "the source to go live", func() bool { return r.src.Health().State == "live" })
	return c
}

func (r *twRig) events(kind activity.Kind) []activity.Event {
	return r.hub.Recent(500, "twitch", kind)
}

func (r *twRig) kind(k activity.Kind) activity.KindStatus {
	for _, s := range r.src.Kinds() {
		if s.Kind == k {
			return s
		}
	}
	r.t.Fatalf("no status for %s", k)
	return activity.KindStatus{}
}

var _ = eventsub.DefaultWSURL
