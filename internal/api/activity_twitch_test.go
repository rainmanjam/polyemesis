package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// fakeEventSub is the smallest Twitch: a WebSocket that welcomes and then
// delivers what the test sends, and a Helix that accepts every subscription.
type fakeEventSub struct {
	srv   *httptest.Server
	mu    sync.Mutex
	conn  *websocket.Conn
	ready chan struct{}
	once  sync.Once
	posts []string
}

func newFakeEventSub(t *testing.T) *fakeEventSub {
	t.Helper()
	f := &fakeEventSub{ready: make(chan struct{})}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conn = c
		f.mu.Unlock()
		stamp := time.Now().UTC().Format(time.RFC3339Nano)
		f.write(fmt.Sprintf(`{"metadata":{"message_id":"w1","message_type":"session_welcome","message_timestamp":%q},"payload":{"session":{"id":"S1","status":"connected","keepalive_timeout_seconds":30,"reconnect_url":null,"connected_at":%q}}}`, stamp, stamp))
		f.once.Do(func() { close(f.ready) })
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	})
	mux.HandleFunc("/helix/users", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":[{"id":%q,"broadcaster_type":"affiliate"}]}`, r.URL.Query().Get("id"))
	})
	mux.HandleFunc("/helix/eventsub/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-1337" || r.Header.Get("Client-Id") != "cid" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.posts = append(f.posts, r.URL.Path)
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"data":[{"id":"x","status":"enabled","cost":0}],"total":1,"total_cost":0,"max_total_cost":10}`)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeEventSub) write(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conn != nil {
		_ = f.conn.WriteMessage(websocket.TextMessage, []byte(s))
	}
}

func TestTheTwitchActivitySourceStartsWithTheAccountAndCarriesItsEvents(t *testing.T) {
	_, s, _, _, _ := activityServer(t)
	fake := newFakeEventSub(t)
	s.twitchEventSub = twitchEventSubSeam{
		WSURL:     "ws" + strings.TrimPrefix(fake.srv.URL, "http") + "/ws",
		HelixBase: fake.srv.URL + "/helix",
	}
	if err := s.store.PutPlatformCreds(s.box, db.PlatformTwitch, "cid", "secret"); err != nil {
		t.Fatal(err)
	}
	acct, err := s.store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformTwitch, AccountName: "streamer", AccountRef: "1337",
		AccessToken: "tok-1337", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour),
		Scopes: "chat:read bits:read moderator:read:followers", ScopeVer: 6,
	})
	if err != nil {
		t.Fatal(err)
	}

	s.attachActivitySource(context.Background(), *acct)
	t.Cleanup(func() { s.detachActivitySource(db.PlatformTwitch, "1337") })

	select {
	case <-fake.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("the source never connected")
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	fake.write(fmt.Sprintf(`{"metadata":{"message_id":"n1","message_type":"notification","message_timestamp":%q,"subscription_type":"channel.follow","subscription_version":"2"},`+
		`"payload":{"subscription":{"id":"s","type":"channel.follow","version":"2","status":"enabled","cost":0},`+
		`"event":{"user_id":"1234","user_login":"cool_user","user_name":"Cool_User","broadcaster_user_id":"1337","followed_at":"2020-07-15T18:16:11.17106713Z"}}}`, stamp))

	deadline := time.Now().Add(5 * time.Second)
	for len(s.activity.Recent(1, "twitch", activity.KindFollow)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the follow never reached the hub")
		}
		time.Sleep(5 * time.Millisecond)
	}
	got := s.activity.Recent(1, "twitch", activity.KindFollow)[0]
	if got.Account != "1337" || got.Actor.Name != "Cool_User" || got.Source.ID != "n1" {
		t.Errorf("follow = %+v", got)
	}

	// The report lists the kinds, with the scopes the ACCOUNT holds deciding
	// which want a reconnect (this token predates the rest).
	var st []activity.KindStatus
	for _, ss := range s.activity.Statuses() {
		if ss.Platform == "twitch" {
			st = ss.Kinds
		}
	}
	state := map[activity.Kind]activity.KindState{}
	for _, k := range st {
		state[k.Kind] = k.State
	}
	if state[activity.KindFollow] != activity.KindOn || state[activity.KindTip] != activity.KindOn || state[activity.KindSubscribe] != activity.KindReconnect {
		t.Errorf("kinds = %v", state)
	}

	// Detaching stops the source.
	s.detachActivitySource(db.PlatformTwitch, "1337")
	if len(s.activity.Statuses()) != 0 {
		t.Errorf("statuses after detach = %+v", s.activity.Statuses())
	}
}

func TestTheTwitchActivityBuilderRefusesAnAccountItCannotConnect(t *testing.T) {
	_, s, _, _, _ := activityServer(t)
	a := db.PlatformAccount{ID: 1, Platform: db.PlatformTwitch, AccountName: "x", AccountRef: "1337"}
	if _, err := buildTwitchActivity(s, a); err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Errorf("without credentials: %v", err)
	}
	if err := s.store.PutPlatformCreds(s.box, db.PlatformTwitch, "cid", "secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := buildTwitchActivity(s, db.PlatformAccount{ID: 1, Platform: db.PlatformTwitch}); err == nil {
		t.Error("an account with no user id was accepted")
	}
	src, err := buildTwitchActivity(s, a)
	if err != nil || src.Platform() != "twitch" || src.Account() != "1337" {
		t.Errorf("src=%v err=%v", src, err)
	}
}
