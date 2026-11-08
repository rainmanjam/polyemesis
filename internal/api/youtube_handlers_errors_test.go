package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// The pickers' and the auto-start endpoint's refusals, each asserted by status and message, and each response checked
// for the planted stream key.

type yhStub struct {
	mu     sync.Mutex
	mode   string // "", "error" (500 naming the key), "quota", "liveEvent"
	tokens int
}

func (y *yhStub) set(m string) { y.mu.Lock(); y.mode = m; y.mu.Unlock() }

func yhFixture(t *testing.T) (*Server, http.Handler, *db.DB, func(*http.Request), int64, *yhStub) {
	t.Helper()
	st := &yhStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		mode := st.mode
		st.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			st.mu.Lock()
			st.tokens++
			st.mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		switch mode {
		case "error":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"backend exploded on `+ytpKeyA+`"}}`)
			return
		case "quota":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":403,"errors":[{"reason":"quotaExceeded"}]}}`)
			return
		}
		switch {
		case r.URL.Path == "/liveStreams":
			_, _ = io.WriteString(w, ypeStreams)
		case r.URL.Path == "/channels":
			_, _ = io.WriteString(w, `{"items":[{"id":"UCchan","snippet":{"title":"Chan"}}]}`)
		case r.URL.Path == "/liveBroadcasts" && r.URL.Query().Get("id") != "":
			status := "ready"
			if mode == "liveEvent" {
				status = "live"
			}
			_, _ = io.WriteString(w, `{"items":[`+ypeEvent(r.URL.Query().Get("id"), status, "stream-A", false)+`]}`)
		default:
			_, _ = io.WriteString(w, `{"items":[]}`)
		}
	}))
	t.Cleanup(srv.Close)
	s, h, store := testServerWith(t, Options{Providers: oauth.NewSet(oauth.WithBaseURL(srv.URL))})
	sign := login(t, h)
	return s, h, store, sign, connectAccount(t, store, s.box, db.PlatformYouTube, "chan"), st
}

func yhDo(t *testing.T, h http.Handler, sign func(*http.Request), method, path string) (int, string) {
	t.Helper()
	r := jsonRequest(t, method, path, nil)
	sign(r)
	w := do(t, h, r)
	return w.Code, w.Body.String()
}

func TestThePickerRoutesRefuseBadRequestsAndUnknownAccounts(t *testing.T) {
	s, h, store, sign, acct, _ := yhFixture(t)
	_ = s
	base := "/api/v1/platforms/accounts/"
	for _, kind := range []string{"streams", "events"} {
		for name, tc := range map[string]struct {
			path string
			want int
		}{
			"non-numeric account id":  {base + "abc/youtube/" + kind, http.StatusBadRequest},
			"non-numeric destination": {base + itoa(acct) + "/youtube/" + kind + "?destinationId=abc", http.StatusBadRequest},
			"negative destination":    {base + itoa(acct) + "/youtube/" + kind + "?destinationId=-4", http.StatusBadRequest},
			"unknown account":         {base + "987654/youtube/" + kind, http.StatusNotFound},
		} {
			code, body := yhDo(t, h, sign, http.MethodGet, tc.path)
			if code != tc.want {
				t.Errorf("%s %s: status %d (%s), want %d", kind, name, code, body, tc.want)
			}
			if strings.Contains(body, ytpKeyA) {
				t.Errorf("%s %s: the refusal carries a key: %s", kind, name, body)
			}
		}
	}
	// A destination id the caller does not own is simply not matched: the list still answers.
	ytpDest(t, store, acct, "main", ytpKeyA, "")
	for _, kind := range []string{"streams", "events"} {
		if code, body := yhDo(t, h, sign, http.MethodGet, base+itoa(acct)+"/youtube/"+kind+"?destinationId=99999"); code != http.StatusOK {
			t.Errorf("%s with an unknown destinationId: %d %s", kind, code, body)
		}
	}
}

func TestThePickerRoutesSayNotSupportedForANonYouTubeAccount(t *testing.T) {
	s, h, store, sign, _, _ := yhFixture(t)
	twitch := connectAccount(t, store, s.box, db.PlatformTwitch, "streamer")
	for _, kind := range []string{"streams", "events"} {
		code, body := yhDo(t, h, sign, http.MethodGet, "/api/v1/platforms/accounts/"+itoa(twitch)+"/youtube/"+kind)
		if code != http.StatusOK || !strings.Contains(body, `"supported":false`) || !strings.Contains(body, "only for YouTube") {
			t.Errorf("%s: %d %s, want 200 with supported:false and the reason", kind, code, body)
		}
	}
}

// A YouTube error becomes 502 with its message, and the planted key in the platform's own error text never reaches
// the response.
func TestAYouTubeErrorOnThePickersIsA502WithTheKeyScrubbed(t *testing.T) {
	_, h, store, sign, acct, st := yhFixture(t)
	ytpDest(t, store, acct, "main", ytpKeyA, "")
	st.set("error")
	for _, kind := range []string{"streams", "events"} {
		code, body := yhDo(t, h, sign, http.MethodGet, "/api/v1/platforms/accounts/"+itoa(acct)+"/youtube/"+kind)
		if code != http.StatusBadGateway || !strings.Contains(body, "backend exploded") {
			t.Errorf("%s: %d %s, want 502 with the platform's message", kind, code, body)
		}
		if strings.Contains(body, ytpKeyA) {
			t.Errorf("%s: the stream key reached the response: %s", kind, body)
		}
	}
}

func TestASpentQuotaOnThePickersIsA502NamingIt(t *testing.T) {
	_, h, _, sign, acct, st := yhFixture(t)
	st.set("quota")
	for _, kind := range []string{"streams", "events"} {
		code, body := yhDo(t, h, sign, http.MethodGet, "/api/v1/platforms/accounts/"+itoa(acct)+"/youtube/"+kind)
		if code != http.StatusBadGateway || !strings.Contains(body, "quotaExceeded") {
			t.Errorf("%s: %d %s, want 502 naming quotaExceeded", kind, code, body)
		}
	}
}

// A token that cannot be refreshed stops the request before any YouTube call.
func TestAFailedTokenRefreshStopsThePickersAndAutoStart(t *testing.T) {
	s, h, store, sign, _, st := yhFixture(t)
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid", "secret"); err != nil {
		t.Fatal(err)
	}
	expired, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{Platform: db.PlatformYouTube, AccountName: "old",
		AccountRef: "old-ref", AccessToken: "at", RefreshToken: "dead", ExpiresAt: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/platforms/accounts/" + itoa(expired.ID) + "/youtube/"
	for name, tc := range map[string]struct{ method, path string }{
		"streams":    {http.MethodGet, base + "streams"},
		"events":     {http.MethodGet, base + "events"},
		"auto-start": {http.MethodPost, base + "events/ev-1/auto-start"},
	} {
		code, body := yhDo(t, h, sign, tc.method, tc.path)
		if code < 400 || code == http.StatusNotFound {
			t.Errorf("%s: %d %s, want a refusal that is not a 404 (the account exists)", name, code, body)
		}
	}
	if st.tokens == 0 {
		t.Error("the refresh was never attempted")
	}
}

func TestTheAutoStartEndpointsRefusals(t *testing.T) {
	s, h, store, sign, acct, st := yhFixture(t)
	post := func(path string) (int, string) { return yhDo(t, h, sign, http.MethodPost, path) }
	base := "/api/v1/platforms/accounts/"

	if code, _ := post(base + "abc/youtube/events/ev-1/auto-start"); code != http.StatusBadRequest {
		t.Errorf("bad account id: %d, want 400", code)
	}
	long := strings.Repeat("e", 129)
	if code, body := post(base + itoa(acct) + "/youtube/events/" + long + "/auto-start"); code != http.StatusBadRequest || !strings.Contains(body, "event id") {
		t.Errorf("a 129-character event id: %d %s, want 400 asking for an event id", code, body)
	}
	if code, _ := post(base + "987654/youtube/events/ev-1/auto-start"); code != http.StatusNotFound {
		t.Errorf("unknown account: %d, want 404", code)
	}
	twitch := connectAccount(t, store, s.box, db.PlatformTwitch, "streamer")
	if code, body := post(base + itoa(twitch) + "/youtube/events/ev-1/auto-start"); code != http.StatusBadRequest || !strings.Contains(body, "YouTube setting") {
		t.Errorf("a Twitch account: %d %s, want 400 saying auto-start is a YouTube setting", code, body)
	}
	// An event YouTube will not change any more (it is live) is YouTube's refusal in polyemesis's words.
	st.set("liveEvent")
	if code, body := post(base + itoa(acct) + "/youtube/events/ev-1/auto-start"); code != http.StatusBadGateway || !strings.Contains(body, "accepts no change") {
		t.Errorf("a live event: %d %s, want 502 explaining YouTube freezes the toggle", code, body)
	}
	st.set("quota")
	if code, body := post(base + itoa(acct) + "/youtube/events/ev-1/auto-start"); code != http.StatusBadGateway || !strings.Contains(body, "quotaExceeded") {
		t.Errorf("a spent quota: %d %s, want 502 naming it", code, body)
	}
	// Read tokens are refused: it is a write.
	read := createScopedToken(t, h, sign, "yh-read", db.ScopeRead)
	r := jsonRequest(t, http.MethodPost, base+itoa(acct)+"/youtube/events/ev-1/auto-start", nil)
	bearer(read)(r)
	if w := do(t, h, r); w.Code != http.StatusForbidden {
		t.Errorf("a read token: %d, want 403", w.Code)
	}
}

// The coordinator's liveChatId reaches the running YouTube adapter: a send goes to that chat with no broadcast list,
// and "" releases it so the next send discovers again. Every miss is quiet.
func TestTheLiveChatIdReachesTheRunningYouTubeAdapter(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	var chatIDs []string
	ys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "liveChat/messages"):
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			chatIDs = append(chatIDs, string(b))
			mu.Unlock()
			_, _ = io.WriteString(w, `{"id":"m1","snippet":{"type":"textMessageEvent","displayMessage":"hello","publishedAt":"2030-01-01T00:00:00Z"}}`)
		default:
			_, _ = io.WriteString(w, `{"items":[],"pollingIntervalMillis":60000}`)
		}
	}))
	t.Cleanup(ys.Close)

	s, _, store := testServer(t, config.Config{})
	s.setYouTubeLiveChat(1, "chat-9") // no hub yet: quiet
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	s.setYouTubeLiveChat(424242, "chat-9") // unknown account: quiet
	twitch, _ := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{Platform: db.PlatformTwitch, AccountName: "t", AccountRef: "t-ref", AccessToken: "at"})
	s.setYouTubeLiveChat(twitch.ID, "chat-9") // not a YouTube account: quiet
	acct, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{Platform: db.PlatformYouTube, AccountName: "chan", AccountRef: "UCchan", AccessToken: "at"})
	if err != nil {
		t.Fatal(err)
	}
	s.setYouTubeLiveChat(acct.ID, "chat-9") // no adapter attached: quiet

	adapter, err := chat.NewYouTube(chat.YouTubeConfig{AccountRef: "UCchan", Channel: "chan",
		Token: func(context.Context) (string, error) { return "at", nil }, APIBase: ys.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.chat.Attach(context.Background(), adapter); err != nil {
		t.Fatal(err)
	}

	s.setYouTubeLiveChat(acct.ID, "chat-9")
	if _, err := adapter.Send(context.Background(), "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	mu.Lock()
	sent := append([]string(nil), chatIDs...)
	mu.Unlock()
	if len(sent) != 1 || !strings.Contains(sent[0], `"liveChatId":"chat-9"`) {
		t.Errorf("the send went to %v, want the chat the coordinator named", sent)
	}
	s.setYouTubeLiveChat(acct.ID, "") // released: nothing is sent to the finished broadcast's chat
	if _, err := adapter.Send(context.Background(), "again"); err == nil || !strings.Contains(err.Error(), "no live chat") {
		t.Errorf("a send after the release = %v, want the no-live-chat refusal", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(chatIDs) != 1 {
		t.Errorf("sends reached YouTube %d times, want only the first", len(chatIDs))
	}
}
