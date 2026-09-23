package api

import (
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/auth"
	"github.com/rainmanjam/polyemesis/internal/config"
)

// THE CSRF TOKEN IS BOUND TO THE SESSION, not merely equal to a cookie.
//
// A plain double-submit check asks one question -- does the header equal the
// polyemesis_csrf cookie -- and anyone who can WRITE a cookie for this host
// (a sibling subdomain, a plaintext hop on the same name, a cookie-tossing
// bug anywhere under the parent domain) can answer it: plant
// `polyemesis_csrf=attacker` ahead of the real one, send `attacker` in the
// header, and Go's r.Cookie returns the first match. Reproduced in the
// exploratory run (row 34): that request created a source, 201.
//
// Binding the expected value to the session cookie -- which is HttpOnly and
// cannot be read, only replayed by the browser -- removes the question the
// attacker could answer. The CSRF cookie is now only how the SPA learns the
// value; what it holds is never trusted.
//
// Mutation: make Manager.CheckCSRF compare the header to r.Cookie(CSRFCookie)
// again. Observed to fail on both planted-cookie cases with 201.

// signedCookies logs in and returns the raw cookies and the CSRF value.
func signedCookies(t *testing.T, h http.Handler) (session *http.Cookie, csrf string) {
	t.Helper()
	w := do(t, h, jsonRequest(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "admin", "password": testPassword}))
	if w.Code != http.StatusOK {
		t.Fatalf("login: status %d, body %s", w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		switch c.Name {
		case auth.SessionCookie:
			session = c
		case auth.CSRFCookie:
			csrf = c.Value
		}
	}
	if session == nil || csrf == "" {
		t.Fatal("login did not set both cookies")
	}
	return session, csrf
}

func TestAPlantedCSRFCookieDoesNotAuthoriseAWrite(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	session, legit := signedCookies(t, h)
	const planted = "attacker-chosen-csrf-value"

	for _, tc := range []struct {
		name    string
		cookies []*http.Cookie
	}{
		{"planted ahead of the real one", []*http.Cookie{
			session,
			{Name: auth.CSRFCookie, Value: planted},
			{Name: auth.CSRFCookie, Value: legit},
		}},
		{"planted in place of the real one", []*http.Cookie{
			session,
			{Name: auth.CSRFCookie, Value: planted},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := jsonRequest(t, http.MethodPost, "/api/v1/auth/tokens",
				map[string]string{"name": "forged"})
			for _, c := range tc.cookies {
				r.AddCookie(c)
			}
			r.Header.Set(auth.CSRFHeader, planted)
			if w := do(t, h, r); w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403: a CSRF value the attacker chose and "+
					"planted as a cookie authorised a write (body %s)", w.Code, w.Body.String())
			}
		})
	}
}

// One session's token is not another's. Without the binding any value the
// server ever issued would do for any session that carries it as a cookie.
func TestACSRFTokenFromAnotherSessionIsRefused(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	_, otherCSRF := signedCookies(t, h)
	session, _ := signedCookies(t, h)

	r := jsonRequest(t, http.MethodPost, "/api/v1/auth/tokens", map[string]string{"name": "x"})
	r.AddCookie(session)
	r.AddCookie(&http.Cookie{Name: auth.CSRFCookie, Value: otherCSRF})
	r.Header.Set(auth.CSRFHeader, otherCSRF)
	if w := do(t, h, r); w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403: another session's CSRF token was accepted", w.Code)
	}
}

// The legitimate path still works, and a session that holds a CSRF cookie
// minted before the binding existed -- every browser signed in across the
// upgrade -- is handed the bound value on its next authenticated request
// instead of being locked out of every write until the session expires.
func TestTheBoundTokenWorksAndAStaleCookieIsReplaced(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	session, legit := signedCookies(t, h)

	r := jsonRequest(t, http.MethodPost, "/api/v1/auth/tokens", map[string]string{"name": "ok"})
	r.AddCookie(session)
	r.AddCookie(&http.Cookie{Name: auth.CSRFCookie, Value: legit})
	r.Header.Set(auth.CSRFHeader, legit)
	if w := do(t, h, r); w.Code >= 300 {
		t.Fatalf("status = %d with the bound token, want success (body %s)", w.Code, w.Body.String())
	}

	g := jsonRequest(t, http.MethodGet, "/api/v1/auth/me", nil)
	g.AddCookie(session)
	g.AddCookie(&http.Cookie{Name: auth.CSRFCookie, Value: "minted-before-the-upgrade"})
	w := do(t, h, g)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /auth/me = %d", w.Code)
	}
	var reissued string
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CSRFCookie {
			reissued = c.Value
		}
	}
	if reissued != legit {
		t.Errorf("a stale CSRF cookie was answered with %q, want the session's bound "+
			"value %q: a browser signed in across the upgrade could not write again", reissued, legit)
	}
}
