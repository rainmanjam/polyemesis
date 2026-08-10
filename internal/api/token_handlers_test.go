package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/auth"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
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

	store := dbtest.OpenCheap(t)
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

// createToken mints an ADMIN-scoped token, because that is what every test
// using it was written about: before #104 a token had no scope and could do
// everything, so "a token" in these tests means the full-power one.
//
// The API's own default is the opposite -- a create request that omits the
// scope mints a read-only token -- which is the point of the feature and is
// asserted directly in TestOmittedScopeMintsAReadToken rather than left to be
// inferred from a helper.
func createToken(t *testing.T, h http.Handler, sign func(*http.Request), name string) string {
	t.Helper()
	return createScopedToken(t, h, sign, name, db.ScopeAdmin)
}

func createScopedToken(t *testing.T, h http.Handler, sign func(*http.Request), name, scope string) string {
	t.Helper()
	r := jsonRequest(t, http.MethodPost, "/api/v1/auth/tokens",
		map[string]string{"name": name, "scope": scope})
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

	revoked, revokedPlaintext, err := store.CreateAPIToken("old laptop", db.ScopeAdmin)
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

// The account password is not something a machine credential gets to touch.
//
// handleChangePassword demands the current password, so this was never
// exploitable with a token alone; the route joined the session-only group
// anyway, because after #140 "no code enforces it but the handler happens to
// ask for something else" is not a security property this package states out
// loud. The 403 comes from the router, before the handler reads a body.
func TestABearerTokenCannotChangeThePassword(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	plaintext := createToken(t, h, sign, "ci runner")

	r := jsonRequest(t, http.MethodPost, "/api/v1/auth/password",
		map[string]string{"current": testPassword, "new": "a whole new password"})
	r.Header.Set("Authorization", "Bearer "+plaintext)
	if w := do(t, h, r); w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body %s", w.Code, http.StatusForbidden, w.Body.String())
	}

	// And the password genuinely did not change: the old one still signs in.
	// Asserting the status alone would pass even if the handler had run.
	login(t, h)
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

// #104: a token used to be able to do everything the operator could, which is
// a poor fit for the thing people actually mint one for -- a monitoring script
// that reads /status every ten seconds and should not be able to delete a
// destination if the box it runs on is compromised.
//
// Every case here goes through the production router, so what is being asserted
// is the middleware chain rather than a helper nobody calls.
func TestReadScopedTokenReachesReadsAndNothingElse(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)

	tests := []struct {
		name     string
		method   string
		path     string
		body     any
		wantRead int
	}{
		// The reads a monitoring token exists for. Not /status: this fixture
		// has no engine, so that route answers 500 for every caller and would
		// be asserting the fixture rather than the scope.
		{name: "GET platform presets", method: http.MethodGet, path: "/api/v1/platforms/presets", wantRead: http.StatusOK},
		{name: "GET me", method: http.MethodGet, path: "/api/v1/auth/me", wantRead: http.StatusOK},
		// Mutations, refused by method with no route list consulted.
		{name: "PUT settings", method: http.MethodPut, path: "/api/v1/settings",
			body: map[string]any{}, wantRead: http.StatusForbidden},
		{name: "POST destinations", method: http.MethodPost, path: "/api/v1/destinations",
			body: map[string]any{"name": "x"}, wantRead: http.StatusForbidden},
		{name: "DELETE destination", method: http.MethodDelete, path: "/api/v1/destinations/1",
			wantRead: http.StatusForbidden},
		// A POST that is not on the write-nothing allowlist. It spawns FFmpeg
		// with a caller-supplied argument list, which is exactly why it is not.
		{name: "POST expert dry-run", method: http.MethodPost,
			path: "/api/v1/destinations/1/expert/dry-run", body: map[string]any{},
			wantRead: http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := jsonRequest(t, tc.method, tc.path, tc.body)
			r.Header.Set("Authorization", "Bearer "+read)
			if w := do(t, h, r); w.Code != tc.wantRead {
				t.Errorf("read token: status = %d, want %d, body %s",
					w.Code, tc.wantRead, w.Body.String())
			}
		})
	}
}

// The other direction, which is what stops the scope check from being a
// blanket refusal that happens to satisfy the test above: an admin token keeps
// the behaviour every token had before #104.
//
// POST /auth/logout is the mutation used here because the scope check is the
// only gate in front of it -- no engine, no store row, nothing else that could
// answer for a reason of its own. Asserting on a route that needs a running
// pipeline would be asserting about the fixture.
func TestAdminScopedTokenStillMutates(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)
	admin := createScopedToken(t, h, sign, "deploy", db.ScopeAdmin)

	probe := func(token string) *httptest.ResponseRecorder {
		r := jsonRequest(t, http.MethodPost, "/api/v1/auth/logout", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		return do(t, h, r)
	}
	if w := probe(admin); w.Code != http.StatusOK {
		t.Errorf("admin token: status = %d, want 200, body %s", w.Code, w.Body.String())
	}
	if w := probe(read); w.Code != http.StatusForbidden {
		t.Errorf("read token: status = %d, want 403, body %s", w.Code, w.Body.String())
	}
}

// The allowlist is the one place the method rule is relaxed, so it gets its own
// test: these POSTs answer a question and write nothing, and a read token would
// be needlessly crippled without them.
func TestReadScopedTokenReachesTheWriteNothingPosts(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)

	// /version/check is the one allowlisted route reachable on a server with no
	// engine and no destinations, so it is the one that can be asserted end to
	// end here. It reaches GitHub or fails quietly; either way the scope layer
	// must not be what stops it.
	r := jsonRequest(t, http.MethodPost, "/api/v1/version/check", nil)
	r.Header.Set("Authorization", "Bearer "+read)
	w := do(t, h, r)
	if w.Code == http.StatusForbidden {
		t.Fatalf("allowlisted POST refused for a read token: %s", w.Body.String())
	}
}

// The default is the feature. A client that has never heard of scopes -- an
// older UI build, a curl line from a blog post -- must get the weaker
// credential, or the release protects only the people who already knew.
func TestOmittedScopeMintsAReadToken(t *testing.T) {
	_, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	r := jsonRequest(t, http.MethodPost, "/api/v1/auth/tokens", map[string]string{"name": "legacy client"})
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", w.Code, w.Body.String())
	}

	tokens, err := store.ListAPITokens()
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("got %d tokens, want 1", len(tokens))
	}
	if tokens[0].Scope != db.ScopeRead {
		t.Errorf("scope = %q, want %q", tokens[0].Scope, db.ScopeRead)
	}
}

func TestUnknownScopeIsRefused(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)

	r := jsonRequest(t, http.MethodPost, "/api/v1/auth/tokens",
		map[string]string{"name": "typo", "scope": "readonly"})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d, body %s", w.Code, http.StatusBadRequest, w.Body.String())
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
