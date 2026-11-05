package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// #4 of the 2026-11-04 lingering-state audit. Removing an app's credentials
// never touched the accounts connected through it, which keep working until
// their tokens expire (for ever, for a token with no expiry). The SPA believed
// the server refused while accounts used the app; nothing did. Now an
// UNCONFIRMED removal is refused, naming them, and {"confirm": true} proceeds.
func TestRemovingCredentialsInUseIsRefusedUntilConfirmed(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)
	seedCreds(t, s, store, db.PlatformTwitch)
	acct := connectAccount(t, store, s.box, db.PlatformTwitch, "streamer")

	var refusal struct {
		Error    string           `json:"error"`
		Code     string           `json:"code"`
		Accounts []credsInUseItem `json:"accounts"`
	}
	decodeInto(t, send(t, h, sign, http.MethodDelete, "/api/v1/platforms/credentials/twitch", nil,
		http.StatusConflict), &refusal)
	if refusal.Code != codeCredsInUse || len(refusal.Accounts) != 1 || refusal.Accounts[0].ID != acct {
		t.Fatalf("refusal %+v, want creds_in_use naming account %d", refusal, acct)
	}
	if !strings.Contains(refusal.Error, "streamer") || !strings.Contains(refusal.Error, `{"confirm": true}`) {
		t.Errorf("refusal %q does not name the account and the way through", refusal.Error)
	}
	if _, err := store.GetPlatformCreds(s.box, db.PlatformTwitch); err != nil {
		t.Fatalf("a refused removal deleted the credentials: %v", err)
	}

	send(t, h, sign, http.MethodDelete, "/api/v1/platforms/credentials/twitch",
		map[string]any{"confirm": true}, http.StatusOK)
	if _, err := store.GetPlatformCreds(s.box, db.PlatformTwitch); err == nil {
		t.Fatal("a confirmed removal kept the credentials")
	}
	// Removing the app is not a disconnect: the account is the disconnect
	// route's, with its own destination guard.
	if accts, err := store.ListPlatformAccounts(); err != nil || len(accts) != 1 {
		t.Fatalf("accounts after a confirmed removal: %v %v, want the account untouched", accts, err)
	}
}

// With nothing connected the bare DELETE answers as it always has, and an
// account on another platform does not count.
func TestRemovingUnusedCredentialsNeedsNoConfirmation(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)
	seedCreds(t, s, store, db.PlatformTwitch)
	connectAccount(t, store, s.box, db.PlatformYouTube, "a channel")
	send(t, h, sign, http.MethodDelete, "/api/v1/platforms/credentials/twitch", nil, http.StatusOK)
	if _, err := store.GetPlatformCreds(s.box, db.PlatformTwitch); err == nil {
		t.Fatal("an unused app's credentials survived a bare DELETE")
	}
}

// #8: every disconnect says the platform still lists the grant, not only the
// ones that had destinations attached.
func TestADisconnectSaysThePlatformStillListsTheGrant(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "a channel")
	var resp struct {
		Status   string   `json:"status"`
		Warnings []string `json:"warnings"`
	}
	decodeInto(t, send(t, h, sign, http.MethodDelete, "/api/v1/platforms/accounts/"+itoa(acct), nil,
		http.StatusOK), &resp)
	if resp.Status != "disconnected" {
		t.Fatalf("status %q", resp.Status)
	}
	joined := strings.Join(resp.Warnings, "\n")
	if !strings.Contains(joined, "YouTube still lists the connection") {
		t.Fatalf("warnings %q do not say YouTube still lists the connection", resp.Warnings)
	}
}
