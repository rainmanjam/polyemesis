package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/auth"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

const testPassword = "correct horse battery"

// testServer returns a server whose store already holds the admin account,
// plus its handler. Engine and event broker stay nil: no route exercised here
// touches them.
//
// Its provider set is the zero oauth.Set, which resolves to the platforms' real
// hosts. That is correct and deliberately inconvenient: a test that reaches a
// platform without saying so fails by trying to reach the internet rather than
// by quietly passing. A test that means to make a platform call uses
// testServerWith and hands in a stubbed set.
func testServer(t *testing.T, cfg config.Config) (*Server, http.Handler, *db.DB) {
	t.Helper()
	return testServerWith(t, Options{Config: cfg})
}

// testServerWith is testServer with the caller's own Options folded in. Only
// the fields a test cannot supply for itself -- the store, the secret box, the
// discarded logger -- are filled here; everything else is left as passed.
func testServerWith(t *testing.T, o Options) (*Server, http.Handler, *db.DB) {
	t.Helper()

	store, err := db.Open(filepath.Join(t.TempDir(), "polyemesis.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.CreateUser("admin", testPassword); err != nil {
		t.Fatalf("create admin: %v", err)
	}

	box, err := secrets.New(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	o.DB = store
	o.Secrets = box
	o.Version = "test"
	s := New(o)
	return s, s.Handler(), store
}

// stubbedServer is testServer plus a stub standing in for every platform API,
// and it is what replaced `s.pushMetadataFn = func(...)`. The returned stub is
// where a test reads what actually left the process.
func stubbedServer(t *testing.T, cfg config.Config) (*Server, http.Handler, *db.DB, *platformStub) {
	t.Helper()
	stub := newPlatformStub(t)
	s, h, store := testServerWith(t, Options{Config: cfg, Providers: stub.set()})
	return s, h, store, stub
}

func do(t *testing.T, h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func jsonRequest(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "203.0.113.5:44444"
	return r
}

// login signs in and returns a function that stamps the resulting session and
// CSRF credentials onto a request, the way the SPA does.
func login(t *testing.T, h http.Handler) func(*http.Request) {
	t.Helper()
	w := do(t, h, jsonRequest(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "admin", "password": testPassword}))
	if w.Code != http.StatusOK {
		t.Fatalf("login: status %d, body %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	var csrf string
	for _, c := range cookies {
		if c.Name == auth.CSRFCookie {
			csrf = c.Value
		}
	}
	if csrf == "" {
		t.Fatal("login did not set a CSRF cookie")
	}
	return func(r *http.Request) {
		for _, c := range cookies {
			r.AddCookie(c)
		}
		r.Header.Set(auth.CSRFHeader, csrf)
	}
}

func createToken(t *testing.T, h http.Handler, sign func(*http.Request), name string) string {
	t.Helper()
	r := jsonRequest(t, http.MethodPost, "/api/v1/auth/tokens", map[string]string{"name": name})
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create token: status %d, body %s", w.Code, w.Body.String())
	}
	var resp struct {
		Token     db.APIToken `json:"token"`
		Plaintext string      `json:"plaintext"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if resp.Plaintext == "" {
		t.Fatal("create response carried no plaintext")
	}
	return resp.Plaintext
}

func TestPlaintextTokenIsReturnedOnceAndNeverAgain(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	plaintext := createToken(t, h, sign, "ci runner")

	r := jsonRequest(t, http.MethodGet, "/api/v1/auth/tokens", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list: status %d, body %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), plaintext) {
		t.Error("the token list echoed the plaintext back")
	}
}

func TestBearerTokenAuthenticatesWithoutACookieOrCSRFHeader(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	plaintext := createToken(t, h, sign, "ci runner")

	// A state-changing request with neither cookie nor CSRF header: exactly
	// what a curl one-liner sends, and exactly what a cross-site page cannot.
	r := jsonRequest(t, http.MethodPost, "/api/v1/auth/logout", nil)
	r.Header.Set("Authorization", "Bearer "+plaintext)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("bearer request rejected: status %d, body %s", w.Code, w.Body.String())
	}
}

func TestBearerAuthIsRejectedFor(t *testing.T) {
	_, h, store := testServer(t, config.Config{})
	sign := login(t, h)
	plaintext := createToken(t, h, sign, "ci runner")

	revoked, revokedPlaintext, err := store.CreateAPIToken("old laptop")
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if err := store.DeleteAPIToken(revoked.ID); err != nil {
		t.Fatalf("DeleteAPIToken: %v", err)
	}

	tests := []struct {
		name   string
		header string
	}{
		{name: "an unknown token", header: "Bearer " + db.TokenPrefix + strings.Repeat("A", 43)},
		{name: "a revoked token", header: "Bearer " + revokedPlaintext},
		{name: "a token truncated to its display prefix", header: "Bearer " + plaintext[:12]},
		{name: "a credential offered under the wrong scheme", header: "Basic " + plaintext},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := jsonRequest(t, http.MethodGet, "/api/v1/auth/me", nil)
			r.Header.Set("Authorization", tc.header)
			if w := do(t, h, r); w.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestABearerTokenCannotMintOrRevokeTokens(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	plaintext := createToken(t, h, sign, "ci runner")

	tests := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{name: "create", method: http.MethodPost, path: "/api/v1/auth/tokens", body: map[string]string{"name": "escalated"}},
		{name: "revoke", method: http.MethodDelete, path: "/api/v1/auth/tokens/1"},
		{name: "list", method: http.MethodGet, path: "/api/v1/auth/tokens"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := jsonRequest(t, tc.method, tc.path, tc.body)
			r.Header.Set("Authorization", "Bearer "+plaintext)
			if w := do(t, h, r); w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
			}
		})
	}
}

func TestRevokedTokenStopsWorkingImmediately(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	plaintext := createToken(t, h, sign, "ci runner")

	probe := func() int {
		r := jsonRequest(t, http.MethodGet, "/api/v1/auth/me", nil)
		r.Header.Set("Authorization", "Bearer "+plaintext)
		return do(t, h, r).Code
	}
	if code := probe(); code != http.StatusOK {
		t.Fatalf("fresh token status = %d, want 200", code)
	}

	list := jsonRequest(t, http.MethodGet, "/api/v1/auth/tokens", nil)
	sign(list)
	var tokens []db.APIToken
	if err := json.Unmarshal(do(t, h, list).Body.Bytes(), &tokens); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	del := jsonRequest(t, http.MethodDelete, "/api/v1/auth/tokens/"+strconv.FormatInt(tokens[0].ID, 10), nil)
	sign(del)
	if w := do(t, h, del); w.Code != http.StatusOK {
		t.Fatalf("revoke: status %d, body %s", w.Code, w.Body.String())
	}

	if code := probe(); code != http.StatusUnauthorized {
		t.Errorf("revoked token status = %d, want 401", code)
	}
}

func TestCookieAuthStillRequiresTheCSRFHeader(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	r := jsonRequest(t, http.MethodPost, "/api/v1/auth/tokens", map[string]string{"name": "forged"})
	sign(r)
	r.Header.Del(auth.CSRFHeader)
	if w := do(t, h, r); w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}
