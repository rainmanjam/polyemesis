package api

import (
	"context"
	"encoding/json"
	"errors"
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

// fakeKick answers id.kick.com and api.kick.com on one server, with the shapes
// recorded live: a bad bearer token is a bare 401 {"data":{},"message":
// "Unauthorized"}, and a dead refresh token is a 401 with error=invalid_grant.
type fakeKick struct {
	URL string

	mu sync.Mutex
	// valid is the access tokens the API accepts.
	valid map[string]bool
	// refreshTo is what a refresh returns; empty refuses it with invalid_grant.
	refreshTo string
	refreshes int
	apiCalls  []string
}

func newFakeKick(t *testing.T, valid ...string) *fakeKick {
	t.Helper()
	f := &fakeKick{valid: map[string]bool{}}
	for _, v := range valid {
		f.valid[v] = true
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

func (f *fakeKick) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/oauth/token" {
		f.refreshes++
		if f.refreshTo == "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"The refresh token is invalid."}`))
			return
		}
		f.valid[f.refreshTo] = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": f.refreshTo, "refresh_token": "rt-2", "expires_in": 7200, "token_type": "Bearer",
		})
		return
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.apiCalls = append(f.apiCalls, tok)
	if !f.valid[tok] {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"data":{},"message":"Unauthorized"}`))
		return
	}
	switch r.URL.Path {
	case "/public/v1/users/livestreams":
		_, _ = w.Write([]byte(`{"data":[{"slug":"nightowl","stream_title":"late","viewer_count":9}]}`))
	case "/public/v1/channels":
		if r.Method == http.MethodPatch {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"slug":"nightowl","stream":{"is_live":true,"viewer_count":9}}]}`))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeKick) refreshCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refreshes
}

func kickFixture(t *testing.T, f *fakeKick, access string, expires time.Time) (*Server, *db.PlatformAccount) {
	t.Helper()
	s, _, store := testServerWith(t, Options{Providers: oauth.NewSet(oauth.WithBaseURL(f.URL))})
	if err := store.PutPlatformCreds(s.box, db.PlatformKick, "cid", "csecret"); err != nil {
		t.Fatal(err)
	}
	acct, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformKick, AccountName: "nightowl", AccountRef: "99",
		AccessToken: access, RefreshToken: "rt-1", ExpiresAt: expires,
		Scopes: "channel:read chat:write",
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, acct
}

func TestKickA401RefreshesOnceAndRetries(t *testing.T) {
	f := newFakeKick(t) // "stale" is not accepted
	f.refreshTo = "fresh"
	s, acct := kickFixture(t, f, "stale", time.Now().Add(time.Hour)) // not expired locally

	st, err := analyticsSource{s}.readStats(context.Background(), acct)
	if err != nil {
		t.Fatalf("readStats: %v; a revoked access token with a good refresh token must recover", err)
	}
	if !st.Live || st.Viewers == nil || *st.Viewers != 9 {
		t.Fatalf("stats = %+v", st)
	}
	if n := f.refreshCount(); n != 1 {
		t.Fatalf("%d refreshes, want 1", n)
	}
	cur, err := s.store.GetPlatformAccount(s.box, acct.ID)
	if err != nil || cur.AccessToken != "fresh" || cur.RefreshToken != "rt-2" {
		t.Fatalf("stored account = %+v, %v; the new tokens must be saved", cur, err)
	}
	if s.kickRevoked(cur) {
		t.Fatal("a recovered account was marked revoked")
	}
}

func TestKickA401WithADeadRefreshGrantMarksTheAccountRevoked(t *testing.T) {
	f := newFakeKick(t) // refreshTo empty: invalid_grant
	s, acct := kickFixture(t, f, "stale", time.Now().Add(time.Hour))

	_, err := analyticsSource{s}.readStats(context.Background(), acct)
	if err == nil {
		t.Fatal("a dead grant reported success")
	}
	if !strings.Contains(err.Error(), "reconnect") {
		t.Fatalf("err = %q, want a reconnect instruction", err)
	}
	if !s.kickRevoked(acct) {
		t.Fatal("the account was not marked revoked")
	}
	h := s.accountHealthNow(*acct, time.Now())
	if h.State != AccountHealthRefreshFailing || !strings.Contains(h.LastError, "Reconnect") {
		t.Fatalf("account health = %+v, want the reconnect state", h)
	}
	if v := s.accountViewFor(*acct, time.Now()); !v.Reconnect.Needed {
		t.Fatalf("account view = %+v, want Reconnect.Needed", v.Reconnect)
	}

	// Terminal: further calls neither refresh nor hit the API with a dead token.
	before := f.refreshCount()
	if _, err := (analyticsSource{s}).readStats(context.Background(), acct); err == nil {
		t.Fatal("still reported success")
	}
	if got := f.refreshCount(); got != before {
		t.Fatalf("refresh grant retried after being found dead (%d -> %d calls)", before, got)
	}
}

func TestKickRevokedGrantIsNotRetriedByTokenForOrTheRefreshLoop(t *testing.T) {
	f := newFakeKick(t)
	s, acct := kickFixture(t, f, "stale", time.Now().Add(-time.Minute)) // expired: tokenFor will refresh

	_, err := s.tokenFor(context.Background(), acct.ID)
	if !errors.Is(err, oauth.ErrReconnectRequired) {
		t.Fatalf("tokenFor = %v, want ErrReconnectRequired", err)
	}
	if !s.kickRevoked(acct) {
		t.Fatal("a refused refresh grant did not mark the account revoked")
	}
	if n := f.refreshCount(); n != 1 {
		t.Fatalf("%d refreshes, want 1", n)
	}

	// Every later caller and every RefreshLoop tick is turned away locally.
	for i := 0; i < 3; i++ {
		if _, err := s.tokenFor(context.Background(), acct.ID); !errors.Is(err, oauth.ErrReconnectRequired) {
			t.Fatalf("tokenFor #%d = %v", i, err)
		}
		s.refreshDueAccounts(context.Background())
	}
	if n := f.refreshCount(); n != 1 {
		t.Fatalf("a revoked grant was sent to id.kick.com %d times, want 1", n)
	}
}

func TestKickReconnectClearsTheRevokedMarkAndRefreshWorksAgain(t *testing.T) {
	f := newFakeKick(t)
	s, acct := kickFixture(t, f, "stale", time.Now().Add(-time.Minute))
	_, _ = s.tokenFor(context.Background(), acct.ID)
	if !s.kickRevoked(acct) {
		t.Fatal("setup: not revoked")
	}
	// What a reconnect does: store a new grant, then clear the mark.
	if _, err := s.store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformKick, AccountName: "nightowl", AccountRef: "99",
		AccessToken: "newgrant", RefreshToken: "rt-new", ExpiresAt: time.Now().Add(time.Hour), Scopes: "channel:read",
	}); err != nil {
		t.Fatal(err)
	}
	s.attachAccountChat(db.PlatformKick, "99")
	got, err := s.tokenFor(context.Background(), acct.ID)
	if err != nil || got.AccessToken != "newgrant" {
		t.Fatalf("tokenFor after reconnect = %+v, %v", got, err)
	}
}

func TestKickAFailedRefreshThatLostToAReconnectDoesNotMarkTheNewGrantRevoked(t *testing.T) {
	f := newFakeKick(t)
	s, acct := kickFixture(t, f, "stale", time.Now().Add(-time.Minute))
	seen := acct.Revision()
	// The reconnect lands while the failing refresh was in flight.
	if _, err := s.store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformKick, AccountName: "nightowl", AccountRef: "99",
		AccessToken: "newgrant", RefreshToken: "rt-new", ExpiresAt: time.Now().Add(time.Hour), Scopes: "channel:read",
	}); err != nil {
		t.Fatal(err)
	}
	s.markKickRevoked(acct, seen)
	if s.kickRevoked(acct) {
		t.Fatal("a stale refresh failure marked a freshly reconnected account revoked")
	}
}

func TestKickChatTokenSourcesAreFatalOnceTheGrantIsGone(t *testing.T) {
	f := newFakeKick(t)
	s, acct := kickFixture(t, f, "stale", time.Now().Add(-time.Minute))

	if _, err := s.chatToken(acct.ID)(context.Background()); !chat.IsFatal(err) {
		t.Fatalf("chatToken = %v, want fatal so chat stops retrying", err)
	}
	if _, err := s.kickChatReauth(acct.ID)(context.Background()); !chat.IsFatal(err) {
		t.Fatalf("kickChatReauth = %v, want fatal", err)
	}
}

func TestKickChatReauthReturnsTheFreshToken(t *testing.T) {
	f := newFakeKick(t)
	f.refreshTo = "fresh"
	s, acct := kickFixture(t, f, "stale", time.Now().Add(time.Hour))
	tok, err := s.kickChatReauth(acct.ID)(context.Background())
	if err != nil || tok != "fresh" {
		t.Fatalf("kickChatReauth = %q, %v", tok, err)
	}
	// Within the cooldown a concurrent caller is handed the token the first one
	// stored, not a refusal and not a second refresh: two sends that both got a
	// 401 must both succeed on the retry.
	tok, err = s.kickChatReauth(acct.ID)(context.Background())
	if err != nil || tok != "fresh" {
		t.Fatalf("second kickChatReauth inside the cooldown = %q, %v; want the stored fresh token", tok, err)
	}
	if n := f.refreshCount(); n != 1 {
		t.Fatalf("%d refreshes, want 1", n)
	}
}

func TestKickRetryLeavesOtherPlatformsAndOtherErrorsAlone(t *testing.T) {
	f := newFakeKick(t)
	s, acct := kickFixture(t, f, "stale", time.Now().Add(time.Hour))

	calls := 0
	boom := errors.New("boom")
	if err := s.kickRetry(context.Background(), acct, func(string) error { calls++; return boom }); err != boom || calls != 1 {
		t.Fatalf("a non-401 was retried or altered: err=%v calls=%d", err, calls)
	}
	other := *acct
	other.Platform = db.PlatformTwitch
	calls = 0
	err := s.kickRetry(context.Background(), &other, func(string) error {
		calls++
		return &oauth.StatusError{Status: http.StatusUnauthorized}
	})
	if err == nil || calls != 1 || f.refreshCount() != 0 {
		t.Fatalf("a Twitch 401 was retried by the Kick path: calls=%d refreshes=%d", calls, f.refreshCount())
	}
}
