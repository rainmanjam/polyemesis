package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// Twitch requires the app to validate each user token at startup and hourly,
// and to end the sessions that use a token Twitch says is no longer valid.
// These run the real pass against a fake id.twitch.tv.

// fakeTwitchID answers /oauth2/validate and /oauth2/token.
type fakeTwitchID struct {
	URL string

	mu sync.Mutex
	// valid is the set of access tokens /validate accepts.
	valid map[string]bool
	// validateStatus, when non-zero, answers every /validate with it (a 5xx).
	validateStatus int
	// refreshTo is the access token a refresh returns; empty means the refresh
	// is refused with invalid_grant (the user disconnected the integration).
	refreshTo string
	validates []string
	refreshes int
}

func newFakeTwitchID(t *testing.T, valid ...string) *fakeTwitchID {
	t.Helper()
	f := &fakeTwitchID{valid: map[string]bool{}}
	for _, v := range valid {
		f.valid[v] = true
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

func (f *fakeTwitchID) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/oauth2/validate":
		tok := strings.TrimPrefix(strings.TrimPrefix(r.Header.Get("Authorization"), "OAuth "), "Bearer ")
		f.validates = append(f.validates, tok)
		if f.validateStatus != 0 {
			w.WriteHeader(f.validateStatus)
			return
		}
		if !f.valid[tok] {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status":401,"message":"invalid access token"}`))
			return
		}
		_, _ = w.Write([]byte(`{"client_id":"cid","login":"dallas","user_id":"1","scopes":[],"expires_in":9000}`))
	case "/oauth2/token":
		f.refreshes++
		if f.refreshTo == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":400,"message":"Invalid refresh token"}`))
			return
		}
		f.valid[f.refreshTo] = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": f.refreshTo, "refresh_token": "rt-2", "expires_in": 14400,
			"scope": []string{"chat:read"}, "token_type": "bearer",
		})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeTwitchID) validateCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.validates)
}

// idleAdapter is a chat adapter that just waits, so the Hub holds it attached.
type idleAdapter struct {
	platform db.Platform
	ref      string
}

func (a idleAdapter) Platform() db.Platform { return a.platform }
func (a idleAdapter) Account() string       { return a.ref }
func (a idleAdapter) Run(ctx context.Context, _ chat.Sink) error {
	<-ctx.Done()
	return nil
}

// twitchValidationFixture: a server whose Twitch provider talks to the fake, a
// stored credentials row, one connected Twitch account, and a chat adapter for
// it attached to the Hub.
func twitchValidationFixture(t *testing.T, f *fakeTwitchID, access string) (*Server, *db.PlatformAccount) {
	t.Helper()
	hub := chat.New()
	s, _, store := testServerWith(t, Options{Chat: hub, Providers: oauth.NewSet(oauth.WithBaseURL(f.URL))})
	if err := store.PutPlatformCreds(s.box, db.PlatformTwitch, "cid", "csecret"); err != nil {
		t.Fatal(err)
	}
	acct, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformTwitch, AccountName: "dallas", AccountRef: "4242",
		AccessToken: access, RefreshToken: "rt-1", ExpiresAt: time.Now().Add(2 * time.Hour),
		Scopes: "chat:read chat:edit",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.chatLife = ctx
	if err := hub.Attach(ctx, idleAdapter{db.PlatformTwitch, "4242"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hub.DetachWithin(db.PlatformTwitch, "4242", time.Second) })
	return s, acct
}

func twitchAttached(s *Server) bool {
	_, ok := s.chat.Adapter(db.PlatformTwitch, "4242")
	return ok
}

func TestTwitchValidationLeavesAValidAccountAlone(t *testing.T) {
	f := newFakeTwitchID(t, "at-good")
	s, acct := twitchValidationFixture(t, f, "at-good")

	s.validateTwitchTokens(context.Background())

	if f.validateCount() != 1 {
		t.Fatalf("validate calls = %d, want 1", f.validateCount())
	}
	if !twitchAttached(s) {
		t.Fatal("a valid token's chat was stopped")
	}
	if r := s.acctHealth.revokedReason(acct.ID); r != "" {
		t.Fatalf("a valid account was marked: %q", r)
	}
}

func TestTwitchValidationRevokedTokenStopsChatAndNeedsReconnect(t *testing.T) {
	f := newFakeTwitchID(t) // no token is valid; the refresh is refused too
	s, acct := twitchValidationFixture(t, f, "at-revoked")

	s.validateTwitchTokens(context.Background())

	if twitchAttached(s) {
		t.Fatal("the IRC session for a revoked token is still attached")
	}
	reason := s.acctHealth.revokedReason(acct.ID)
	if reason == "" {
		t.Fatal("the account was not marked")
	}

	// What the console reads: reconnect needed, and a health state it already renders.
	accts, err := s.store.ListPlatformAccounts()
	if err != nil {
		t.Fatal(err)
	}
	h := s.accountHealthNow(accts[0], time.Now())
	if h.State != AccountHealthRefreshFailing || !strings.Contains(h.LastError, "Reconnect") {
		t.Fatalf("health = %+v, want refreshFailing naming the reconnect", h)
	}
	if v := s.accountViewFor(accts[0], time.Now()); !v.Reconnect.Needed || v.Reconnect.Reason == "" {
		t.Fatalf("reconnect = %+v, want Needed with a reason", v.Reconnect)
	}
}

// A 401 on a token whose refresh works means the token was merely stale: one
// refresh, a second validation, and no reconnect prompt.
func TestTwitchValidation401ThenSuccessfulRefreshIsNotARevocation(t *testing.T) {
	f := newFakeTwitchID(t)
	f.refreshTo = "at-fresh"
	s, acct := twitchValidationFixture(t, f, "at-stale")

	s.validateTwitchTokens(context.Background())

	if f.refreshes != 1 {
		t.Fatalf("refreshes = %d, want exactly one retry", f.refreshes)
	}
	if !twitchAttached(s) {
		t.Fatal("chat was stopped for a token that refreshed fine")
	}
	if r := s.acctHealth.revokedReason(acct.ID); r != "" {
		t.Fatalf("marked despite a good refresh: %q", r)
	}
	got, err := s.store.GetPlatformAccount(s.box, acct.ID)
	if err != nil || got.AccessToken != "at-fresh" {
		t.Fatalf("stored token = %v, %v; want the refreshed one", got, err)
	}
}

// Twitch being down is not the user disconnecting.
func TestTwitchValidationServerErrorIsNotARevocation(t *testing.T) {
	f := newFakeTwitchID(t)
	f.validateStatus = http.StatusBadGateway
	s, acct := twitchValidationFixture(t, f, "at-1")

	s.validateTwitchTokens(context.Background())

	if !twitchAttached(s) || s.acctHealth.revokedReason(acct.ID) != "" {
		t.Fatal("a 502 from Twitch ended a chat session")
	}
	if f.refreshes != 0 {
		t.Fatalf("a 502 triggered %d refreshes", f.refreshes)
	}
}

// Reconnecting clears the mark; the next valid pass keeps it clear.
func TestTwitchValidationMarkClearsOnReconnectAndOnAValidPass(t *testing.T) {
	f := newFakeTwitchID(t)
	s, acct := twitchValidationFixture(t, f, "at-revoked")
	s.validateTwitchTokens(context.Background())
	if s.acctHealth.revokedReason(acct.ID) == "" {
		t.Fatal("setup: not marked")
	}

	// The operator reconnects: new token, same row. attachAccountChat is what
	// the OAuth callback calls.
	f.mu.Lock()
	f.valid["at-new"] = true
	f.mu.Unlock()
	if _, err := s.store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformTwitch, AccountName: "dallas", AccountRef: "4242",
		AccessToken: "at-new", RefreshToken: "rt-1", ExpiresAt: time.Now().Add(2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	s.attachAccountChat(db.PlatformTwitch, "4242")
	if r := s.acctHealth.revokedReason(acct.ID); r != "" {
		t.Fatalf("reconnect left the mark: %q", r)
	}
}

// A mark set earlier is cleared by a pass that finds the token valid.
func TestTwitchValidationValidPassClearsAStaleMark(t *testing.T) {
	f := newFakeTwitchID(t, "at-good")
	s, acct := twitchValidationFixture(t, f, "at-good")
	s.acctHealth.markRevoked(acct.ID, "earlier")
	s.validateTwitchTokens(context.Background())
	if r := s.acctHealth.revokedReason(acct.ID); r != "" {
		t.Fatalf("still marked after a valid pass: %q", r)
	}
}

// The loop validates at once and then on the interval; it ends with its context.
func TestTwitchValidationLoopRunsAtStartAndHourly(t *testing.T) {
	f := newFakeTwitchID(t, "at-good")
	s, _ := twitchValidationFixture(t, f, "at-good")

	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.twitchValidationLoop(ctx, ticks) }()

	waitUntil(t, "the startup validation", func() bool { return f.validateCount() == 1 })
	ticks <- time.Now()
	waitUntil(t, "the hourly validation", func() bool { return f.validateCount() == 2 })
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the loop did not stop with its context")
	}
}

func TestTwitchValidationIntervalIsHourly(t *testing.T) {
	if twitchValidateEvery != time.Hour {
		t.Fatalf("interval = %s; Twitch requires hourly", twitchValidateEvery)
	}
}
