package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// twitchToolsPlanted is a login only the stub Helix banned list reports, so its
// appearance in a body is the planted value and nothing else.
const twitchToolsPlanted = "planted_banned_viewer_4093"

// toolsAdapter is a real Twitch adapter whose Helix base is a stub, with Run
// replaced by a wait so the Hub can supervise it without an IRC server.
type toolsAdapter struct{ *chat.TwitchAdapter }

func (a toolsAdapter) Run(ctx context.Context, _ chat.Sink) error { <-ctx.Done(); return nil }

type helixCall struct {
	Method, Path string
	Query        url.Values
	Body         map[string]any
}

type helixStub struct {
	mu    sync.Mutex
	calls []helixCall
}

func (s *helixStub) last() helixCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return helixCall{}
	}
	return s.calls[len(s.calls)-1]
}

func (s *helixStub) all() []helixCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]helixCall(nil), s.calls...)
}

// twitchToolsFixture attaches a Twitch adapter whose Helix calls hit a stub.
// fail maps "METHOD /path" to a status to answer instead of success.
func twitchToolsFixture(t *testing.T, fail map[string]int) (*Server, http.Handler, func(*http.Request), *helixStub) {
	t.Helper()
	s, h, _ := testServer(t, config.Config{})
	stub := &helixStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		stub.mu.Lock()
		stub.calls = append(stub.calls, helixCall{r.Method, r.URL.Path, r.URL.Query(), body})
		stub.mu.Unlock()
		if st, ok := fail[r.Method+" "+r.URL.Path]; ok {
			w.WriteHeader(st)
			io.WriteString(w, `{"error":"x","message":"twitch said no"}`)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /moderation/banned":
			io.WriteString(w, `{"data":[{"user_id":"1","user_login":"`+twitchToolsPlanted+`","user_name":"B"}],"pagination":{}}`)
		case "GET /chat/chatters":
			io.WriteString(w, `{"data":[],"total":57,"pagination":{}}`)
		case "GET /moderation/blocked_terms":
			io.WriteString(w, `{"data":[],"pagination":{}}`)
		case "GET /users":
			io.WriteString(w, `{"data":[{"id":"900"}]}`)
		case "GET /moderation/shield_mode", "PUT /moderation/shield_mode":
			io.WriteString(w, `{"data":[{"is_active":true,"moderator_login":"mod"}]}`)
		case "GET /chat/pins":
			io.WriteString(w, `{"data":[]}`)
		case "GET /moderation/automod/settings":
			io.WriteString(w, `{"data":[{"overall_level":2}]}`)
		case "GET /moderation/unban_requests":
			io.WriteString(w, `{"data":[{"id":"u1","user_login":"x","text":"sorry","status":"pending"}],"pagination":{}}`)
		case "GET /channels/vips":
			io.WriteString(w, `{"data":[{"user_id":"4","user_login":"v","user_name":"V"}]}`)
		case "GET /moderation/moderators":
			io.WriteString(w, `{"data":[{"user_id":"3","user_login":"m","user_name":"M"}]}`)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)

	ta, err := chat.NewTwitch(chat.TwitchConfig{
		Nick: "streamer", Channel: "streamer", AccountRef: "acct", Token: "tok",
		HelixToken: chat.StaticToken("helix"), ClientID: "cid",
		BroadcasterID: "42", ModeratorID: "42", APIBase: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	hub := chat.New()
	t.Cleanup(hub.Close)
	s.chat = hub
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := hub.Attach(ctx, toolsAdapter{ta}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return s, h, login(t, h), stub
}

// proveTwitchTools is GET /chat/twitch/{resource}'s counterpart: the admin reads
// the planted banned viewer, the read token is refused it.
func proveTwitchTools(t *testing.T) proofResult {
	_, h, sign, _ := twitchToolsFixture(t, nil)
	read := createScopedToken(t, h, sign, "ledger-twitch-tools", db.ScopeRead)

	const path = "/api/v1/chat/twitch/banned?account=acct"
	code, high := rawBodyOf(t, h, sign, path)
	if code != http.StatusOK || !strings.Contains(high, twitchToolsPlanted) {
		t.Fatalf("the admin's banned list answered %d without the planted login: %s", code, truncateForFailure(high))
	}
	code, low := rawBodyOf(t, h, bearer(read), path)
	if code != http.StatusForbidden {
		t.Errorf("the read token's banned list answered %d, want 403", code)
	}
	return proofResult{
		Pattern:   patternDriven(t, h, http.MethodGet, "/api/v1/chat/twitch/banned"),
		High:      high,
		Read:      low,
		Sentinels: []string{twitchToolsPlanted},
	}
}

func TestReadsOfTheTwitchToolsReachTheAdapter(t *testing.T) {
	_, h, sign, _ := twitchToolsFixture(t, nil)
	for path, want := range map[string]string{
		"banned":             twitchToolsPlanted,
		"chatters":           `"total":57`,
		"blocked-terms":      `"terms":[]`,
		"shield":             `"active":true`,
		"pin":                `"pin":null`,
		"roles?role=vip":     `"holders"`,
		"shoutout-cooldowns": `"targets"`,
		"automod-settings":   `"overallLevel":2`,
		"unban-requests":     `"requests"`,
	} {
		code, body := rawBodyOf(t, h, sign, "/api/v1/chat/twitch/"+joinAccount(path))
		if code != http.StatusOK || !strings.Contains(body, want) {
			t.Errorf("GET %s: %d %s, want it to contain %s", path, code, truncateForFailure(body), want)
		}
	}
	// Unknown resources are a 404, not a silent success.
	if code, _ := rawBodyOf(t, h, sign, "/api/v1/chat/twitch/nope?account=acct"); code != http.StatusNotFound {
		t.Errorf("unknown resource answered %d", code)
	}
	// A GET whose adapter call fails is a 502 carrying Twitch's words.
	_, h2, sign2, _ := twitchToolsFixture(t, map[string]int{"GET /moderation/banned": 401})
	code, body := rawBodyOf(t, h2, sign2, "/api/v1/chat/twitch/banned?account=acct")
	if code != http.StatusBadGateway || !strings.Contains(body, "reconnect") {
		t.Errorf("refused read answered %d %s", code, truncateForFailure(body))
	}
}

func joinAccount(p string) string {
	if strings.Contains(p, "?") {
		return p + "&account=acct"
	}
	return p + "?account=acct"
}

func TestWritesOfTheTwitchToolsReachTheAdapter(t *testing.T) {
	_, h, sign, stub := twitchToolsFixture(t, nil)
	do := func(method, path string, body any, want string) {
		t.Helper()
		out := send(t, h, sign, method, "/api/v1/chat/twitch/"+path, body, http.StatusOK)
		if want != "" && !strings.Contains(string(out), want) {
			t.Errorf("%s %s: %s, want %s", method, path, out, want)
		}
	}
	const a = "?account=acct"
	do(http.MethodPost, "announcements"+a, map[string]any{"message": "hi", "color": "blue"}, "done")
	if c := stub.last(); c.Path != "/chat/announcements" || c.Body["color"] != "blue" {
		t.Errorf("announcement = %+v", c)
	}
	do(http.MethodPost, "shoutouts"+a, map[string]any{"login": "friend"}, "nextAnyAt")
	do(http.MethodPost, "warnings"+a, map[string]any{"userId": "7", "reason": "stop"}, "")
	do(http.MethodPost, "blocked-terms"+a, map[string]any{"text": "badword"}, "")
	do(http.MethodPost, "held-messages"+a, map[string]any{"messageId": "h1", "allow": false}, "")
	if c := stub.last(); c.Body["action"] != "DENY" {
		t.Errorf("held = %+v", c)
	}
	do(http.MethodPost, "roles"+a, map[string]any{"role": "vip", "userId": "8"}, "")
	do(http.MethodPost, "suspicious"+a, map[string]any{"userId": "8", "status": "RESTRICTED"}, "")
	do(http.MethodPut, "pin"+a, map[string]any{"messageId": "m", "durationSeconds": 60}, "")
	do(http.MethodPut, "shield"+a, map[string]any{"active": true}, `"active":true`)
	do(http.MethodPut, "automod-settings"+a, map[string]any{"settings": map[string]any{"overallLevel": 2}}, "")
	if c := stub.last(); len(c.Body) != 1 || c.Body["overall_level"] != float64(2) {
		t.Errorf("automod body = %+v", c.Body)
	}
	do(http.MethodPatch, "pin"+a, map[string]any{"messageId": "m", "durationSeconds": 90}, "")
	do(http.MethodPatch, "unban-requests"+a, map[string]any{"id": "u1", "status": "approved"}, "")

	// Query-parameter verbs.
	do(http.MethodDelete, "pin?messageId=m&account=acct", nil, "")
	do(http.MethodDelete, "blocked-terms?id=i1&account=acct", nil, "")
	if c := stub.last(); c.Query.Get("id") != "i1" {
		t.Errorf("delete term = %+v", c)
	}
	do(http.MethodDelete, "roles?role=moderator&login=pal&account=acct", nil, "")
	do(http.MethodDelete, "suspicious?userId=8&account=acct", nil, "")
}

func TestTwitchToolWritesRefuseBadInputAndReportTwitchsWords(t *testing.T) {
	_, h, sign, _ := twitchToolsFixture(t, map[string]int{"POST /chat/announcements": 429})
	post := func(res string, body any, want string) {
		t.Helper()
		out := send(t, h, sign, http.MethodPost, "/api/v1/chat/twitch/"+res+"?account=acct", body, http.StatusBadRequest)
		if !strings.Contains(string(out), want) {
			t.Errorf("POST %s: %s, want %q", res, out, want)
		}
	}
	post("announcements", map[string]any{"message": "x"}, "limits how many announcements")
	post("held-messages", map[string]any{"messageId": "h"}, "allow")
	post("warnings", map[string]any{"userId": "1"}, "reason")
	send(t, h, sign, http.MethodPut, "/api/v1/chat/twitch/shield?account=acct", map[string]any{}, http.StatusBadRequest)
	send(t, h, sign, http.MethodPut, "/api/v1/chat/twitch/automod-settings?account=acct", map[string]any{}, http.StatusBadRequest)
	send(t, h, sign, http.MethodPost, "/api/v1/chat/twitch/nope?account=acct", map[string]any{}, http.StatusNotFound)
	// Not connected.
	send(t, h, sign, http.MethodPost, "/api/v1/chat/twitch/announcements?account=other", map[string]any{"message": "x"}, http.StatusBadRequest)
}

func TestTwitchToolsAre503WithoutAChatHub(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	send(t, h, sign, http.MethodGet, "/api/v1/chat/twitch/banned", nil, http.StatusServiceUnavailable)
}

func TestBlockedTermSyncPushesOnlyPlainAutomodRules(t *testing.T) {
	s, h, sign, stub := twitchToolsFixture(t, nil)
	if _, err := s.store.UpdateSettings(func(st *db.Settings) error {
		st.Automod.Rules = []db.AutomodRule{
			{ID: 1, Name: "plain", Enabled: true, Pattern: "scam link", Action: "delete"},
			{ID: 2, Name: "regex", Enabled: true, Pattern: `free\s+robux`, Action: "delete"},
			{ID: 3, Name: "off", Enabled: false, Pattern: "disabled", Action: "delete"},
			{ID: 4, Name: "star", Enabled: true, Pattern: "wild*", Action: "delete"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var res struct {
		Added   []string                `json:"added"`
		Skipped []struct{ Term string } `json:"skipped"`
	}
	out := send(t, h, sign, http.MethodPost, "/api/v1/chat/twitch/blocked-terms-sync?account=acct", map[string]any{}, http.StatusOK)
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 1 || res.Added[0] != "scam link" {
		t.Fatalf("added = %v (body %s)", res.Added, out)
	}
	for _, c := range stub.all() {
		if c.Method == "POST" && c.Path == "/moderation/blocked_terms" && c.Body["text"] != "scam link" {
			t.Errorf("a non-plain rule reached Twitch: %+v", c)
		}
	}
}

func TestPlainAutomodTerms(t *testing.T) {
	got := plainAutomodTerms([]db.AutomodRule{
		{Enabled: true, Pattern: " hello world "}, {Enabled: true, Pattern: "a.b"},
		{Enabled: true, Pattern: ""}, {Enabled: false, Pattern: "off"}, {Enabled: true, Pattern: "x+"},
	})
	if len(got) != 1 || got[0] != "hello world" {
		t.Fatalf("got %v", got)
	}
}

func TestErrNeedNamesTheField(t *testing.T) {
	if got := errNeed("allow").Error(); got != "allow is required" {
		t.Fatal(got)
	}
	if v := nonNil[int](nil); v == nil {
		t.Fatal("nonNil returned nil")
	}
}

func TestTwitchToolsRefuseAnAdapterWithoutThem(t *testing.T) {
	s, h, _ := testServer(t, config.Config{})
	hub := chat.New()
	t.Cleanup(hub.Close)
	s.chat = hub
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := hub.Attach(ctx, &moderatableAdapter{platform: db.PlatformTwitch, account: "bare"}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	sign := login(t, h)
	body := send(t, h, sign, http.MethodGet, "/api/v1/chat/twitch/banned?account=bare", nil, http.StatusBadGateway)
	if !strings.Contains(string(body), "no channel tools") {
		t.Fatalf("body = %s", body)
	}
}
