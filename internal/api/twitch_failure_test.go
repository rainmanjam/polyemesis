package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/analytics"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// twitchTokenEndpoint refuses the refresh token "rt-bad" the way Twitch does
// (400 "Invalid refresh token", no invalid_grant) and refreshes any other.
func twitchTokenEndpoint(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path != "/oauth2/token" {
			http.NotFound(w, r)
			return
		}
		if r.PostForm.Get("refresh_token") == "rt-bad" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"Bad Request","status":400,"message":"Invalid refresh token"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-new-" + r.PostForm.Get("refresh_token"), "refresh_token": "rt-next",
			"expires_in": 14400, "scope": []string{"chat:read"}, "token_type": "bearer",
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// One account's dead grant must reach chat as ErrReconnectRequired (so its
// runner stops) without touching another account's refresh.
func TestChatTokenMapsARevokedGrantAndIsolatesAccounts(t *testing.T) {
	srv := twitchTokenEndpoint(t)
	s, _, store := testServerWith(t, Options{Providers: oauth.NewSet(oauth.WithBaseURL(srv.URL))})
	if err := store.PutPlatformCreds(s.box, db.PlatformTwitch, "cid", "csecret"); err != nil {
		t.Fatal(err)
	}
	mk := func(name, ref, refresh string) *db.PlatformAccount {
		a, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
			Platform: db.PlatformTwitch, AccountName: name, AccountRef: ref,
			AccessToken: "at-old-" + name, RefreshToken: refresh,
			ExpiresAt: time.Now().Add(-time.Hour), Scopes: "chat:read",
		})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	bad, good := mk("baddie", "1", "rt-bad"), mk("gooddie", "2", "rt-good")

	if _, err := s.chatToken(bad.ID)(context.Background()); !errors.Is(err, chat.ErrReconnectRequired) {
		t.Fatalf("revoked grant error = %v, want chat.ErrReconnectRequired", err)
	}
	tok, err := s.chatToken(good.ID)(context.Background())
	if err != nil || tok != "at-new-rt-good" {
		t.Fatalf("healthy account: token %q err %v; another account's failure stalled it", tok, err)
	}
	// A transient failure is NOT a reconnect.
	if errors.Is(chatTokenError(errors.New("connection reset")), chat.ErrReconnectRequired) {
		t.Fatal("a transient error was classed as reconnect-required")
	}
}

// The reactive refresh used for a Helix 401 refreshes even an unexpired token.
func TestChatTokenRefreshForcesARefresh(t *testing.T) {
	srv := twitchTokenEndpoint(t)
	s, _, store := testServerWith(t, Options{Providers: oauth.NewSet(oauth.WithBaseURL(srv.URL))})
	if err := store.PutPlatformCreds(s.box, db.PlatformTwitch, "cid", "csecret"); err != nil {
		t.Fatal(err)
	}
	a, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformTwitch, AccountName: "dallas", AccountRef: "4242",
		AccessToken: "at-revoked-early", RefreshToken: "rt-good", ExpiresAt: time.Now().Add(2 * time.Hour),
		Scopes: "chat:read",
	})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := s.chatTokenRefresh(a.ID)(context.Background())
	if err != nil || !strings.HasPrefix(tok, "at-new-") {
		t.Fatalf("forced refresh gave %q, %v", tok, err)
	}
	b, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformTwitch, AccountName: "other", AccountRef: "7",
		AccessToken: "at-x", RefreshToken: "rt-bad", ExpiresAt: time.Now().Add(2 * time.Hour), Scopes: "chat:read",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.chatTokenRefresh(b.ID)(context.Background()); !errors.Is(err, chat.ErrReconnectRequired) {
		t.Fatalf("revoked grant on forced refresh = %v, want chat.ErrReconnectRequired", err)
	}
}

// Twitch's Ratelimit-Reset (parsed into RetryAfter) is honoured as given, not
// rounded up to a minute.
func TestSamplerHonoursTwitchRateLimitReset(t *testing.T) {
	var a analyticsSource
	err := a.platformError(analytics.Target{Platform: string(db.PlatformTwitch)},
		&oauth.StatusError{Status: http.StatusTooManyRequests, RetryAfter: 7 * time.Second})
	var ra *analytics.RetryAfterError
	if !errors.As(err, &ra) || ra.After < 7*time.Second || ra.After > 9*time.Second {
		t.Fatalf("err = %v, want a RetryAfterError of about 7s", err)
	}
	err = a.platformError(analytics.Target{Platform: string(db.PlatformTwitch)},
		&oauth.StatusError{Status: http.StatusTooManyRequests})
	if !errors.As(err, &ra) || ra.After < time.Minute {
		t.Fatalf("no header: err = %v, want the one-minute floor", err)
	}
}
