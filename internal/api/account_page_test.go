package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// A Facebook account says whether it is a Page, so the console need not know
// that Pages are the refs spelled "page:" (oauth's fbRefPage). The destination
// dialog hides the privacy control for a Page broadcast, which is always public.
func TestTheAccountsListSaysWhichFacebookAccountsArePages(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)
	seed := func(platform db.Platform, name, ref string) {
		t.Helper()
		if _, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
			Platform: platform, AccountName: name, AccountRef: ref,
			AccessToken: "t", ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	seed(db.PlatformFacebook, "My Page", "page:123")
	seed(db.PlatformFacebook, "Me", "user:456")
	seed(db.PlatformFacebook, "Legacy", "789")
	seed(db.PlatformTwitch, "ada", "page:not-facebook")

	var got []struct {
		AccountName string `json:"accountName"`
		IsPage      bool   `json:"isPage"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/platforms/accounts", nil, http.StatusOK), &got)
	want := map[string]bool{"My Page": true, "Me": false, "Legacy": false, "ada": false}
	if len(got) != len(want) {
		t.Fatalf("got %d accounts, want %d", len(got), len(want))
	}
	for _, a := range got {
		if a.IsPage != want[a.AccountName] {
			t.Errorf("%s: isPage = %v, want %v", a.AccountName, a.IsPage, want[a.AccountName])
		}
	}
}
