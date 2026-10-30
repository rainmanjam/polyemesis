package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// ACCOUNT HEALTH (A5). The destination dialog shows a connected account as a
// chip; it can only say "failing to refresh" if the server says so.

func TestAccountHealthRanksAFailingRefreshAboveExpiry(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	withRefresh := db.PlatformAccount{RefreshToken: "rt", ExpiresAt: now.Add(-time.Hour)}
	noRefresh := func(exp time.Time) db.PlatformAccount { return db.PlatformAccount{ExpiresAt: exp} }

	cases := []struct {
		name    string
		acct    db.PlatformAccount
		outcome refreshOutcome
		failing bool
		want    string
	}{
		{"an expired token with a refresh token is routine", withRefresh, refreshOutcome{}, false, AccountHealthOK},
		{"a failed refresh names itself", withRefresh, refreshOutcome{failures: 2, lastError: "invalid_grant", at: now}, true, AccountHealthRefreshFailing},
		{"no refresh token and past expiry", noRefresh(now.Add(-time.Minute)), refreshOutcome{}, false, AccountHealthExpired},
		{"no refresh token and under a day left", noRefresh(now.Add(3 * time.Hour)), refreshOutcome{}, false, AccountHealthExpiring},
		{"no refresh token and weeks left", noRefresh(now.Add(30 * 24 * time.Hour)), refreshOutcome{}, false, AccountHealthOK},
		{"no expiry recorded", noRefresh(time.Time{}), refreshOutcome{}, false, AccountHealthOK},
	}
	for _, c := range cases {
		got := accountHealthFor(c.acct, c.acct.RefreshToken != "", c.outcome, c.failing, now)
		if got.State != c.want {
			t.Errorf("%s: state = %q, want %q", c.name, got.State, c.want)
		}
		if c.want == AccountHealthRefreshFailing && (got.Failures != 2 || got.LastError != "invalid_grant" || got.FailedAt == nil) {
			t.Errorf("%s: the failure detail did not travel: %+v", c.name, got)
		}
	}
}

func TestAccountHealthBookClearsOnASuccessfulRefresh(t *testing.T) {
	var b accountHealthBook // zero value, as on a Server
	if _, has := b.get(1); has {
		t.Fatal("an empty book reports an outcome")
	}
	b.fail(1, errors.New("first"), time.Now())
	b.fail(1, errors.New("second"), time.Now())
	if o, _ := b.get(1); o.failures != 2 || o.lastError != "second" {
		t.Fatalf("outcome = %+v, want two failures ending in the latest error", o)
	}
	b.ok(1)
	if _, has := b.get(1); has {
		t.Fatal("a successful refresh did not clear the run of failures")
	}
}

// End to end: a refresh the platform refuses shows on GET /platforms/accounts
// as refreshFailing with the platform's words, and a later success clears it.
func TestAFailedRefreshShowsOnTheAccountsList(t *testing.T) {
	refuse := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if refuse {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_grant","error_description":"refresh token revoked"}`)
			return
		}
		fmt.Fprint(w, `{"access_token":"fresh","refresh_token":"fresh-rt","expires_in":3600}`)
	}))
	t.Cleanup(srv.Close)

	s, h, store := testServerWith(t, Options{Providers: oauth.NewSet(oauth.WithBaseURL(srv.URL))})
	sign := login(t, h)
	acct, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformTwitch, AccountName: "ada", AccountRef: "ada-ref",
		AccessToken: "old", RefreshToken: "old-rt", ExpiresAt: time.Now().Add(-time.Hour),
		Scopes: "channel:manage:broadcast", ScopeVer: 8,
	})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if err := store.PutPlatformCreds(s.box, db.PlatformTwitch, "cid", "secret"); err != nil {
		t.Fatalf("creds: %v", err)
	}

	health := func() AccountHealth {
		var got []struct {
			Health AccountHealth `json:"health"`
		}
		decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/platforms/accounts", nil, http.StatusOK), &got)
		if len(got) != 1 {
			t.Fatalf("got %d accounts, want 1", len(got))
		}
		return got[0].Health
	}

	if _, err := s.tokenFor(t.Context(), acct.ID); err == nil {
		t.Fatal("the refusing platform's refresh succeeded")
	}
	if got := health(); got.State != AccountHealthRefreshFailing || got.Failures != 1 || got.LastError == "" {
		t.Fatalf("after a refused refresh, health = %+v, want refreshFailing with the reason", got)
	}

	refuse = false
	if _, err := s.tokenFor(t.Context(), acct.ID); err != nil {
		t.Fatalf("tokenFor after the platform recovered: %v", err)
	}
	if got := health(); got.State != AccountHealthOK {
		t.Fatalf("after a successful refresh, health = %+v, want ok", got)
	}
}
