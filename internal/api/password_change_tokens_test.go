package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// changePasswordBody is what POST /api/v1/auth/password answers with. Decoded
// through a struct rather than a map so the test states the contract the UI
// reads rather than whatever keys happen to be present.
type changePasswordBody struct {
	Status           string         `json:"status"`
	APITokensRevoked int            `json:"apiTokensRevoked"`
	APITokens        []db.APIToken  `json:"apiTokens"`
	APITokensNote    string         `json:"apiTokensNote"`
	APITokensError   string         `json:"apiTokensError"`
	Raw              map[string]any `json:"-"`
}

func changePassword(t *testing.T, h http.Handler, sign func(*http.Request), body map[string]any) changePasswordBody {
	t.Helper()
	r := jsonRequest(t, http.MethodPost, "/api/v1/auth/password", body)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("change password: status %d, body %s", w.Code, w.Body.String())
	}
	var out changePasswordBody
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	return out
}

// bearerReaches reports whether an API token still authenticates.
func bearerReaches(t *testing.T, h http.Handler, plaintext string) int {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/auth/me", nil)
	r.Header.Set("Authorization", "Bearer "+plaintext)
	return do(t, h, r).Code
}

// A password change is what an operator does when they believe a credential has
// leaked. It bumps users.token_epoch, which refuses every session issued before
// that moment -- and it does nothing at all to API tokens, which are matched by
// hash and carry no epoch.
//
// That is a defensible default (destroying the token in somebody's CI runner
// during a routine rotation is an outage nobody asked for) and an indefensible
// silence: the operator was told "password changed" at the exact moment they
// were deciding whether their install was still compromised.
func TestChangingThePasswordSaysWhichAPITokensSurviveIt(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	ci := createToken(t, h, sign, "ci runner")
	monitoring := createToken(t, h, sign, "monitoring")

	got := changePassword(t, h, sign, map[string]any{
		"current": testPassword, "new": "a whole new password",
	})

	if got.APITokensRevoked != 0 {
		t.Errorf("apiTokensRevoked = %d, want 0 when the caller did not ask", got.APITokensRevoked)
	}
	if len(got.APITokens) != 2 {
		t.Fatalf("apiTokens has %d entries, want the 2 that survived -- a client "+
			"cannot warn about credentials it was not handed", len(got.APITokens))
	}
	names := got.APITokens[0].Name + " " + got.APITokens[1].Name
	for _, want := range []string{"ci runner", "monitoring"} {
		if !strings.Contains(names, want) {
			t.Errorf("surviving tokens %q do not name %q", names, want)
		}
	}
	if !strings.Contains(got.APITokensNote, "2 API tokens") || !strings.Contains(got.APITokensNote, "NOT revoked") {
		t.Errorf("apiTokensNote = %q, want it to say plainly that 2 tokens were NOT revoked", got.APITokensNote)
	}

	// The claim is true, which is the half a message can get wrong: both
	// tokens really do still reach the API.
	if code := bearerReaches(t, h, ci); code != http.StatusOK {
		t.Errorf("ci runner token status = %d, want 200 -- the response said it survived", code)
	}
	if code := bearerReaches(t, h, monitoring); code != http.StatusOK {
		t.Errorf("monitoring token status = %d, want 200 -- the response said it survived", code)
	}
}

// The other half: an operator who says this IS an incident gets the complete
// gesture in one request, rather than having to remember a second screen.
func TestChangingThePasswordCanRevokeEveryAPIToken(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	ci := createToken(t, h, sign, "ci runner")
	monitoring := createToken(t, h, sign, "monitoring")

	got := changePassword(t, h, sign, map[string]any{
		"current": testPassword, "new": "a whole new password", "revokeApiTokens": true,
	})

	if got.APITokensRevoked != 2 {
		t.Errorf("apiTokensRevoked = %d, want 2", got.APITokensRevoked)
	}
	if len(got.APITokens) != 0 {
		t.Errorf("apiTokens = %+v, want nothing left", got.APITokens)
	}
	if got.APITokensNote != "" {
		t.Errorf("apiTokensNote = %q, want silence when nothing survived", got.APITokensNote)
	}
	for name, plaintext := range map[string]string{"ci runner": ci, "monitoring": monitoring} {
		if code := bearerReaches(t, h, plaintext); code != http.StatusUnauthorized {
			t.Errorf("%s token status = %d, want 401 after an opted-in revoke", name, code)
		}
	}
}

// DeleteAllAPITokens is a state, not a row: an install with no tokens is
// already in the state the caller asked for, so zero is a success.
func TestRevokingEveryAPITokenOnAnInstallWithNoneIsNotAnError(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	got := changePassword(t, h, sign, map[string]any{
		"current": testPassword, "new": "a whole new password", "revokeApiTokens": true,
	})
	if got.APITokensRevoked != 0 || got.APITokensError != "" {
		t.Errorf("revoked = %d, error = %q; want 0 and no error", got.APITokensRevoked, got.APITokensError)
	}
}
