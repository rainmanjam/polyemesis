package api

// The test-connection check, asserted through the production router.
//
// The check exists so a Stream Deck inspector (or any script) can prove a
// pasted token works and say what it may do before the operator relies on it.
// Three things must hold: an anonymous caller is refused, a caller learns only
// about the token it presented, and an admin token is marked so a client can
// warn that a read-only plugin was given more than it needs.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func TestConnectionCheckAnswersOnlyForThePresentedToken(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	read := createScopedToken(t, h, sign, "streamdeck", db.ScopeRead)
	admin := createScopedToken(t, h, sign, "deploy", db.ScopeAdmin)

	get := func(token string) *httptest.ResponseRecorder {
		r := jsonRequest(t, http.MethodGet, "/api/v1/auth/connection", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		return do(t, h, r)
	}

	if w := get(""); w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: status = %d, want 401", w.Code)
	}

	w := get(read)
	if w.Code != http.StatusOK {
		t.Fatalf("read token: status = %d, want 200, body %s", w.Code, w.Body.String())
	}
	var rd connectionCheckBody
	if err := json.Unmarshal(w.Body.Bytes(), &rd); err != nil {
		t.Fatalf("decode read: %v", err)
	}
	if rd.Name != "streamdeck" || rd.Scope != db.ScopeRead || rd.AdminScope {
		t.Errorf("read token answer = %+v, want name streamdeck, scope read, adminScope false", rd)
	}

	w = get(admin)
	if w.Code != http.StatusOK {
		t.Fatalf("admin token: status = %d, want 200, body %s", w.Code, w.Body.String())
	}
	var ad connectionCheckBody
	if err := json.Unmarshal(w.Body.Bytes(), &ad); err != nil {
		t.Fatalf("decode admin: %v", err)
	}
	if ad.Name != "deploy" || ad.Scope != db.ScopeAdmin || !ad.AdminScope {
		t.Errorf("admin token answer = %+v, want name deploy, scope admin, adminScope true", ad)
	}
	if ad.Name == rd.Name {
		t.Errorf("the two calls reported the same token: %q", ad.Name)
	}
}
