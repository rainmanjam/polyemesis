package api

import (
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"
)

// TestWrongKickSecretIsIndistinguishableFromAnUnroutedPath is the #158 fix,
// asserted against the real router.
//
// /api/v1/chat/kick/{secret} is the one route in this build whose EXISTENCE is
// meant to be private: it is mounted outside every authentication group because
// Kick posts to it unauthenticated, and the unguessable path segment is the
// whole credential. So the reply to a wrong secret must be the reply to a path
// that is not mounted -- otherwise an anonymous prober learns that the route is
// there, which is the fact the design is keeping.
//
// It was not. http.NotFound writes Go's own `404 page not found` as text/plain
// with no Cache-Control; the router's own miss is `{"error":"no such
// endpoint"}` as JSON with Cache-Control: no-store. The handler's comment
// claimed an attacker "learns nothing from a wrong secret that they would not
// learn from no such route", and that was false on every one of those four
// axes.
//
// FOUR AXES, compared explicitly rather than by eyeballing the body: a
// difference in any one of them is the same disclosure, and a test that only
// compared bodies would have gone green on a fix that left the Content-Type
// different.
func TestWrongKickSecretIsIndistinguishableFromAnUnroutedPath(t *testing.T) {
	h, _, _ := sourceServer(t)

	// Both requests are ANONYMOUS. That is the threat model: the route is
	// outside every authenticated group, so the prober has no credential and
	// needs none.
	methods := []string{http.MethodPost, http.MethodGet, http.MethodHead}
	for _, m := range methods {
		t.Run(m, func(t *testing.T) {
			wrong := do(t, h, jsonRequest(t, m, "/api/v1/chat/kick/notthesecretatall0000", nil))
			absent := do(t, h, jsonRequest(t, m, "/api/v1/nosuchroute", nil))

			if wrong.Code != absent.Code {
				t.Errorf("status: wrong-secret = %d, unrouted = %d", wrong.Code, absent.Code)
			}
			if wrong.Code != http.StatusNotFound {
				t.Errorf("wrong-secret status = %d, want 404; a 401 or a 403 would announce "+
					"the route just as loudly", wrong.Code)
			}
			for _, hdr := range []string{"Content-Type", "Cache-Control", "Allow", "WWW-Authenticate"} {
				if got, want := wrong.Header().Get(hdr), absent.Header().Get(hdr); got != want {
					t.Errorf("%s: wrong-secret = %q, unrouted = %q. A header that differs is "+
						"the same disclosure as a body that differs.", hdr, got, want)
				}
			}
			if got, want := wrong.Body.String(), absent.Body.String(); got != want {
				t.Errorf("body: wrong-secret = %q, unrouted = %q", got, want)
			}
		})
	}
}

// TestTheMethodOracleStillBehavesAsDocumented pins #158's ACCEPTED RISK.
//
// chi decides 405 before any group middleware runs, so requireAuth never sees a
// wrong-method request and 405-vs-404 tells an anonymous caller whether a
// (method, path) pair is registered. That is CLOSED AS WORKING AS INTENDED, and
// this test exists so the decision is executable rather than a paragraph in a
// closed issue:
//
//  1. Everything the oracle discloses about /api/v1 is published in
//     docs/API.md's route table, which also documents the HEAD-405 behaviour by
//     name. An oracle that reproduces a published document is not a leak.
//  2. The premise that /api/v1/chat/kick/{secret} "already has that shape" is
//     FALSE on execution, and this test measures it: the route is registered
//     with r.HandleFunc, which registers every method chi knows, so chi never
//     emits 405 for it and the oracle does not reach the one route whose
//     existence is arguably sensitive. That is asserted below.
//  3. A fix cannot live in a handler. It needs a router-level MethodNotAllowed
//     handler that inspects credentials -- a THIRD authentication site in a
//     package whose TestOnlyTwoFunctionsAuthenticate exists to keep it at two.
//  4. The debuggability cost is permanent and asymmetric: every wrong verb
//     becomes "wrong URL", and the Allow header goes with it.
//
// If this test starts failing because 405 became 404, that is a deliberate
// reversal of a decision, not a bug fix. Re-read the four reasons first.
func TestTheMethodOracleStillBehavesAsDocumented(t *testing.T) {
	h, _, _ := sourceServer(t)

	// The oracle, anonymous, exactly as measured.
	//
	// `allow` is a SET, and the comparison below is order-insensitive, because
	// chi's Allow header is not ordered. chi builds methodsAllowed by ranging
	// over a map (tree.go) and calls Header().Add once PER METHOD (mux.go), so a
	// path registered for more than one verb emits several Allow headers in Go
	// map order -- which is randomised per process. Header().Get returns only
	// the first of those, so asserting on it made /api/v1/settings a coin flip
	// between "GET" and "PUT": measured at 4 failures in 40 fresh processes
	// before this was changed. What the test means to pin is WHICH METHODS the
	// oracle discloses, and that is a set.
	oracle := []struct {
		method, path string
		allow        []string
	}{
		{http.MethodHead, "/api/v1/settings", []string{"GET", "PUT"}},
		{http.MethodPut, "/api/v1/upgrade/stage", []string{"POST"}},
	}
	for _, c := range oracle {
		w := do(t, h, jsonRequest(t, c.method, c.path, nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405. This is the ACCEPTED behaviour, not an "+
				"oversight; see the comment on this test before changing it.",
				c.method, c.path, w.Code)
		}
		got := allowSet(w.Header().Values("Allow"))
		want := append([]string(nil), c.allow...)
		sort.Strings(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s %s Allow = %v, want %v (as a set). The oracle's disclosure is "+
				"the set of registered methods; if this changed, a route gained or lost "+
				"a verb.", c.method, c.path, got, want)
		}
	}

	// And an unrouted path is still a 404, which is the other half of the
	// oracle being an oracle at all.
	if w := do(t, h, jsonRequest(t, http.MethodHead, "/api/v1/nosuchroute", nil)); w.Code != http.StatusNotFound {
		t.Errorf("HEAD /api/v1/nosuchroute = %d, want 404", w.Code)
	}

	// REASON 2, measured. The kick route is registered with r.HandleFunc, so
	// chi knows every method for it and NEVER answers 405 -- which is why the
	// "this route already has that shape" premise for fixing the oracle does
	// not hold. Every one of these is a 404 with the unrouted body, not a 405.
	for _, m := range []string{
		http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead, http.MethodGet,
	} {
		w := do(t, h, jsonRequest(t, m, "/api/v1/chat/kick/notthesecretatall0000", nil))
		if w.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s /api/v1/chat/kick/{secret} = 405 with Allow=%v. The route is no "+
				"longer registered for every method, so the 405 oracle now DOES reach the "+
				"one route whose existence is the secret -- which invalidates reason 2 for "+
				"accepting the oracle. Re-open #158.", m, allowSet(w.Header().Values("Allow")))
		}
	}
}

// allowSet normalises an Allow header into a sorted set of methods.
//
// RFC 9110 permits both spellings and chi has used one of them: several Allow
// headers, one method each. A comma-joined single header is equally valid, so
// both are flattened here rather than pinning the shape chi happens to emit --
// what these tests assert is which METHODS are disclosed, never how the bytes
// were split.
func allowSet(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		for _, m := range strings.Split(v, ",") {
			m = strings.TrimSpace(m)
			if m == "" || seen[m] {
				continue
			}
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}
