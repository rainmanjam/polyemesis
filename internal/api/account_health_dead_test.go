package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// A refresh the platform refused for good must stop the 10-minute RefreshLoop
// retrying, until a reconnect stores a new expiry.
func TestRefreshLoopSkipsAccountsWhoseGrantIsGoneUntilReconnected(t *testing.T) {
	var b accountHealthBook
	exp := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	if b.isDead(1, exp) {
		t.Fatal("an account with no failures is not dead")
	}
	b.fail(1, errors.New("transient"), time.Now())
	b.markDead(1, exp) // not called for transient failures in tokenFor; guard anyway
	if !b.isDead(1, exp) {
		t.Fatal("a refused grant at this expiry is dead")
	}
	if b.isDead(1, exp.Add(24*time.Hour)) {
		t.Fatal("a reconnect stores a new expiry and must lift the skip")
	}
	b.ok(1)
	if b.isDead(1, exp) {
		t.Fatal("a successful refresh clears it")
	}
	b.markDead(2, exp) // no recorded failure: must not invent one
	if b.isDead(2, exp) {
		t.Fatal("markDead without a failure must be a no-op")
	}
}

// The book alone proves nothing about the wiring: tokenFor must mark a refused
// grant dead, and the loop must skip it until a reconnect changes ExpiresAt.
// Mutation: delete the isDead skip in refreshDue and the second tick refreshes
// again; delete the markDead call in tokenFor and the same happens.
func TestRefreshDueSkipsARefusedGrantUntilReconnected(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Bad Request","status":400,"message":"Invalid refresh token"}`))
	}))
	t.Cleanup(srv.Close)

	s, _, store, _ := engineServer(t, defaultTools(), Options{Providers: oauth.NewSet(oauth.WithBaseURL(srv.URL))})
	acct, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformTwitch, AccountName: "ada", AccountRef: "ada-ref",
		AccessToken: "old-access", RefreshToken: "old-refresh",
		ExpiresAt: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if err := store.PutPlatformCreds(s.box, db.PlatformTwitch, "cid", "secret"); err != nil {
		t.Fatalf("creds: %v", err)
	}

	s.refreshDue(context.Background())
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("first tick made %d refresh calls, want 1", n)
	}
	s.refreshDue(context.Background())
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("a refused grant was retried on the next tick (%d calls)", n)
	}

	// A reconnect stores a new (still due) expiry; the account is tried again.
	acct.ExpiresAt = time.Now().Add(-30 * time.Minute)
	if _, err := store.UpsertPlatformAccount(s.box, acct); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	s.refreshDue(context.Background())
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("after a reconnect the refresh was not tried again (%d calls, want 2)", n)
	}
}
