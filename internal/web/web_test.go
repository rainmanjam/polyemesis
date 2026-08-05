package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The SPA fallback used to answer every unmatched path with index.html,
// including the ones under /api. A caller that asked for JSON got 200 and a
// page of HTML, so `res.ok` was true, `JSON.parse` failed on '<', and a
// mistyped or removed endpoint was indistinguishable from a working one.
//
// Verified against a live install before the fix: /api/v1/no-such-endpoint,
// /api/v1/sourcez and /api/v2/sources all returned 200 text/html.
func TestUnmatchedAPIPathIsJSON404NotTheSPA(t *testing.T) {
	if !Built() {
		t.Skip("UI not built; nothing to fall back to")
	}
	h, err := Handler()
	if err != nil {
		t.Fatalf("Handler(): %v", err)
	}

	for _, path := range []string{
		"/api",
		"/api/",
		"/api/v1/no-such-endpoint",
		"/api/v1/sourcez",
		"/api/v2/sources",
		"/api/v1/deeply/nested/missing",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
			}
			if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "text/html") {
				t.Errorf("Content-Type = %q, must not be HTML", ct)
			}
			// Parsed rather than string-matched: the contract is that a client
			// can decode the body as JSON, which is the thing that was broken.
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not JSON (%v): %q", err, rec.Body.String())
			}
			if body["error"] == "" || body["error"] == nil {
				t.Errorf("body carries no error message: %q", rec.Body.String())
			}
		})
	}
}

// The fix must not touch the reason the fallback exists: a client-side deep
// link has to survive a reload.
func TestUnknownUIPathStillFallsBackToTheSPA(t *testing.T) {
	if !Built() {
		t.Skip("UI not built; nothing to fall back to")
	}
	h, err := Handler()
	if err != nil {
		t.Fatalf("Handler(): %v", err)
	}

	for _, path := range []string{
		"/routing/3",
		"/settings",
		"/this-route-does-not-exist",
		// Not under /api despite the substring -- the guard matches a path
		// segment, not a prefix of an arbitrary word.
		"/apiary",
		"/api-docs",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
				t.Errorf("Content-Type = %q, want HTML", ct)
			}
		})
	}
}
