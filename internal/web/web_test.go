package web

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// builtDist is the two-file stand-in the quarantine register named as the fix.
//
// The row `web-spa-fallback-needs-a-built-ui` said, in the field for what it
// would take to un-silence these tests: "either a UI build step before the Go
// suite, or a test-only fs.FS seam so the fallback can be driven against a
// two-file stand-in. The second is cheap and is what #167 is about." This is
// that stand-in, and HandlerFor is that seam. The register row is deleted and
// the ceiling came down with it.
//
// It carries an assets/ directory as well as index.html, because a filesystem
// with no directory in it cannot demonstrate anything about the directory case
// below.
func builtDist() fs.FS {
	return fstest.MapFS{
		"index.html":              {Data: []byte("<!doctype html><title>polyemesis</title>")},
		"assets/index-abc123.js":  {Data: []byte("export default 1;\n")},
		"assets/index-abc123.css": {Data: []byte(":root{}\n")},
	}
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// The SPA fallback used to answer every unmatched path with index.html,
// including the ones under /api. A caller that asked for JSON got 200 and a
// page of HTML, so `res.ok` was true, `JSON.parse` failed on '<', and a
// mistyped or removed endpoint was indistinguishable from a working one.
//
// Verified against a live install before the fix: /api/v1/no-such-endpoint,
// /api/v1/sourcez and /api/v2/sources all returned 200 text/html.
//
// IT USED TO QUARANTINE ITSELF when Built() was false -- which is every CI job
// in this repository and every clean checkout, because the embed.FS holds only
// .gitkeep until `npm run build` has run. A guard for a live-verified bug that
// declines to run wherever it is actually run is #161's shape and #167's cause.
// It now runs over BOTH filesystems, unconditionally: the /api branch answers
// before the fallback is reached, so it is the one branch whose behaviour does
// not depend on whether a UI was compiled, and asserting that in both columns is
// stronger than what the quarantined version claimed in one.
func TestUnmatchedAPIPathIsJSON404NotTheSPA(t *testing.T) {
	bare, err := FS()
	if err != nil {
		t.Fatalf("embedded FS: %v", err)
	}
	for _, col := range []struct {
		name string
		h    http.Handler
	}{
		{"bare", HandlerFor(bare)},
		{"built", HandlerFor(builtDist())},
	} {
		for _, path := range []string{
			"/api",
			"/api/",
			"/api/v1/no-such-endpoint",
			"/api/v1/sourcez",
			"/api/v2/sources",
			"/api/v1/deeply/nested/missing",
		} {
			t.Run(col.name+path, func(t *testing.T) {
				rec := get(t, col.h, path)

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
}

// The fix must not touch the reason the fallback exists: a client-side deep
// link has to survive a reload.
//
// Un-quarantined the same way, but it can only be driven against a populated
// filesystem and that is stated rather than hidden: with no index.html there is
// nothing to fall back TO, and the honest answer for that configuration is the
// "UI not built" 404 -- which is a different assertion, made in
// internal/api's #167 branch table.
func TestUnknownUIPathStillFallsBackToTheSPA(t *testing.T) {
	h := HandlerFor(builtDist())

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
			rec := get(t, h, path)

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
				t.Errorf("Content-Type = %q, want HTML", ct)
			}
		})
	}
}

// TestADirectoryUnderTheAssetRootIsNotServedAsAListing is the bug #156's review
// named and nothing had ever driven.
//
// `sub.Open("assets")` SUCCEEDS -- opening a directory is not an error -- so the
// handler handed it to http.FileServer, which answered 200 with an index of the
// directory. Measured before the fix, against exactly this filesystem:
//
//	GET /assets/ -> 200 text/html
//	<pre><a href="index-abc123.css">index-abc123.css</a>
//	     <a href="index-abc123.js">index-abc123.js</a></pre>
//
// The whole bundle inventory, to an anonymous caller, from a handler mounted as
// the mux's terminal NotFound. Not a credential, and not nothing: it publishes
// build layout and file names to anyone who asks.
//
// It could not have been written before HandlerFor existed. Against the empty
// filesystem this repository and CI check out, the old code and the new code are
// indistinguishable -- every path takes the "UI not built" branch. That is #167
// in one sentence.
func TestADirectoryUnderTheAssetRootIsNotServedAsAListing(t *testing.T) {
	h := HandlerFor(builtDist())

	// THE POSITIVE CONTROL, first. Every assertion below is "the listing is
	// absent", and a filesystem with no assets in it would satisfy them all
	// having proved nothing. The real bundle must be served, by name, from the
	// asset branch.
	if w := get(t, h, "/assets/index-abc123.js"); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "export default 1;") {
		t.Fatalf("the fingerprinted bundle is not served: status %d, body %q. Every check "+
			"below asserts the ABSENCE of a listing of these files; if they are not "+
			"there to be listed, none of it means anything.", w.Code, w.Body.String())
	}
	if cc := get(t, h, "/assets/index-abc123.js").Header().Get("Cache-Control"); cc !=
		"public, max-age=31536000, immutable" {
		t.Errorf("the asset branch's Cache-Control is %q; the fix must not have moved "+
			"fingerprinted bundles off the immutable path", cc)
	}

	for _, path := range []string{"/assets/", "/assets"} {
		w := get(t, h, path)
		body := w.Body.String()
		for _, name := range []string{"index-abc123.js", "index-abc123.css"} {
			if strings.Contains(body, name) {
				t.Errorf("GET %s answered %d with a response naming %q. Opening a directory "+
					"under the asset root succeeds, and handing the open directory to "+
					"http.FileServer publishes its whole inventory to an anonymous "+
					"caller.\nbody: %s", path, w.Code, name, body)
			}
		}
	}

	// AND IT MUST BEHAVE LIKE ANY OTHER UNKNOWN PATH, rather than 404ing: the
	// SPA router owns paths this handler cannot serve, and a deep link has to
	// survive a reload.
	if w := get(t, h, "/assets/"); w.Code != http.StatusOK ||
		!strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(w.Body.String(), "<title>polyemesis</title>") {
		t.Errorf("GET /assets/ answered %d with Content-Type %q; a directory should fall "+
			"through to the SPA fallback like every other unknown path.\nbody: %s",
			w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
}
