package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// The accounts listing carries what the "reconnect to enable" banner needs: the
// version an account was granted under (scopeVer), the one this build asks for
// (currentScopeVersion), and the i18n keys for what the bumps in between unlock.
func TestTheAccountsListCarriesScopeVersionsAndUnlocks(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)
	seed := func(platform db.Platform, name string, ver int) {
		t.Helper()
		if _, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
			Platform: platform, AccountName: name, AccountRef: name,
			AccessToken: "t", ExpiresAt: time.Now().Add(time.Hour), ScopeVer: ver,
		}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	seed(db.PlatformTwitch, "old-twitch", 5)
	seed(db.PlatformTwitch, "new-twitch", 6)
	seed(db.PlatformX, "old-x", 1)

	var got []struct {
		AccountName         string   `json:"accountName"`
		ScopeVer            int      `json:"scopeVer"`
		CurrentScopeVersion int      `json:"currentScopeVersion"`
		ScopeUnlocks        []string `json:"scopeUnlocks"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/platforms/accounts", nil, http.StatusOK), &got)
	type want struct {
		granted, current int
		unlocks          []string
	}
	wants := map[string]want{
		"old-twitch": {5, 6, []string{"reconnect.unlock.twitch6"}},
		"new-twitch": {6, 6, nil},
		"old-x":      {1, 2, []string{"reconnect.unlock.x2"}},
	}
	if len(got) != len(wants) {
		t.Fatalf("got %d accounts, want %d", len(got), len(wants))
	}
	for _, a := range got {
		w := wants[a.AccountName]
		if a.ScopeVer != w.granted || a.CurrentScopeVersion != w.current || len(a.ScopeUnlocks) != len(w.unlocks) {
			t.Errorf("%s: %+v, want %+v", a.AccountName, a, w)
			continue
		}
		for i := range w.unlocks {
			if a.ScopeUnlocks[i] != w.unlocks[i] {
				t.Errorf("%s: unlocks %v, want %v", a.AccountName, a.ScopeUnlocks, w.unlocks)
			}
		}
	}
}
