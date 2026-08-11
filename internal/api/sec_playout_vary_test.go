package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The recurrence guard for #155: the playout origin serves segments and the
// poster with `Cache-Control: public` from behind a credential gate, and told
// no cache that the answer depends on the credential.
//
// WHY THE COOKIE AND NOT `?t=`. playoutTokenMatches reads four channels and one
// of them is a query parameter. A test that authorised itself with `?t=` would
// compare two requests with DIFFERENT URLS, so it would pass over a completely
// absent Vary -- the URL alone would already be a correct cache key. The cookie
// is also the channel production actually uses: setPlayoutTokenCookie mints it
// on the first authorised request, and every segment after that carries it.
//
// WHY NOT AN API PRINCIPAL. /playout/* is mounted OUTSIDE the authenticated
// group (api.go:860) and the poster and player page are registered before it
// (api.go:378-379). A bearer token proves nothing here: it is not what this
// gate reads, and a test that signed one would be exercising a different door.

const playoutWatchToken = "SENTINEL-playout-vary-watch-6b21"

// varyGatedRoutes are the three things authorizePlayout gates, with the one
// property that differs between them recorded rather than assumed.
//
// The media prefix is addressed with a SEGMENT name rather than a manifest
// because the segment is the response that carries `public, max-age=N`; the
// manifest is `no-store` and was never the risk. Nothing has to be on disk for
// either: the header is written by the gate, above the file handler, which is
// also the only place it can be written for the 401 half.
//
// diverges says whether the refusal and the authorised answer are DISTINGUISHABLE
// in this fixture. Two of the three are: they answer 401 without the cookie.
// The poster is not, and that is measured, not assumed -- with no segment on
// disk its authorised branch runs out of media and answers the same 404 the
// refusal does, so a differential control on it would compare 404 with 404 and
// certify nothing. It is asserted for the header alone, and the mutation below
// is what keeps that assertion honest.
var varyGatedRoutes = []struct {
	path      string
	diverges  bool
	whyNoDiff string
}{
	{path: PlayoutPrefix + "segment000.ts", diverges: true},
	{path: "/api/v1/playout/public", diverges: true},
	{path: "/api/v1/playout/poster.jpg", diverges: false,
		whyNoDiff: "with no segment on disk the authorised branch reaches posterJPEG, " +
			"finds nothing, and answers the same 404 the refusal does"},
}

func TestTheGatedPlayoutRoutesTellCachesTheyVaryOnTheCredential(t *testing.T) {
	h := tokenProtectedPlayoutServer(t)

	for _, route := range varyGatedRoutes {
		t.Run(route.path, func(t *testing.T) {
			without := playoutGet(t, h, route.path, false)
			with := playoutGet(t, h, route.path, true)

			// POSITIVE CONTROL, where one is available: the gate must actually
			// be live on this route and the COOKIE must actually be what gets
			// through it. Without this a route that had quietly stopped being
			// gated would pass the header assertion below and prove nothing.
			if route.diverges {
				if without.Code == with.Code {
					t.Fatalf("GET %s answered %d both with and without the playout cookie. "+
						"Either the gate is not on this route or the fixture never "+
						"protected the stream; either way the header assertion would "+
						"prove nothing.\nwithout: %s",
						route.path, without.Code, strings.TrimSpace(without.Body.String()))
				}
				if without.Code != http.StatusUnauthorized {
					t.Fatalf("GET %s without the cookie answered %d, want 401. The fixture "+
						"is a token-protected published stream, and a different refusal "+
						"means the gate took a branch this test was not written for",
						route.path, without.Code)
				}
			} else if without.Code != with.Code {
				t.Fatalf("GET %s now DIVERGES on the cookie (%d vs %d), but this row says "+
					"it cannot: %q. Move it to diverges:true and let it carry the "+
					"differential control the others do",
					route.path, without.Code, with.Code, route.whyNoDiff)
			}

			// The defect. BOTH answers must carry it: a Vary on the authorised
			// response alone still lets a shared cache store the 401 under the
			// bare URL and hand it back to the viewer who does hold the token.
			for _, w := range []struct {
				what string
				vals []string
			}{
				{"the unauthenticated answer", without.Header().Values("Vary")},
				{"the cookie-bearing answer", with.Header().Values("Vary")},
			} {
				joined := strings.ToLower(strings.Join(w.vals, ", "))
				for _, name := range []string{"cookie", "authorization",
					strings.ToLower(playoutTokenHeader)} {
					if !strings.Contains(joined, name) {
						t.Errorf("GET %s: %s is cacheable under the URL alone -- its Vary "+
							"does not name %q, which authorizePlayout reads to decide "+
							"this very response.\nVary: %q",
							route.path, w.what, name, strings.Join(w.vals, ", "))
					}
				}
			}
		})
	}
}

// TestPlayoutDoesNotClaimToVaryOnOrigin is the other half, and it is here
// because the obvious over-correction is to add every header somebody might
// think of.
//
// MEASURED: every CORS site reachable from this gate emits the CONSTANT
// `Access-Control-Allow-Origin: *`, never a reflected request origin. So the
// response does not depend on Origin, naming it would fragment a shared cache
// once per origin for nothing, and a Vary that names a header the response does
// not depend on is a false statement of the same kind as a comment that
// describes code the code does not run.
func TestPlayoutDoesNotClaimToVaryOnOrigin(t *testing.T) {
	h := tokenProtectedPlayoutServer(t)

	for _, route := range varyGatedRoutes {
		w := playoutGet(t, h, route.path, true)
		vary := strings.ToLower(strings.Join(w.Header().Values("Vary"), ", "))
		if strings.Contains(vary, "origin") {
			t.Errorf("GET %s names Origin in Vary, but every CORS header this gate "+
				"writes is the constant `*`. If a reflected origin has been "+
				"introduced, this test is the one to update -- and the CORS site "+
				"is the thing to check.\nVary: %q", route.path, vary)
		}
	}
}

// tokenProtectedPlayoutServer publishes playout as PUBLIC and TOKEN-PROTECTED,
// which is the one configuration where the gate's answer depends on a
// credential a cache could be handed.
//
// Open playout is the same bytes for everybody and admin-only playout is 404 to
// everybody; neither is the shape #155 is about.
func tokenProtectedPlayoutServer(t *testing.T) http.Handler {
	t.Helper()
	h, store, _ := sourceServer(t)
	s := serverUnderTest(t, h)

	if _, err := s.playoutStore().save(playoutPublish{
		Protection: PlayoutProtectToken,
		Token:      playoutWatchToken,
		Title:      "gated",
	}); err != nil {
		t.Fatalf("publish a token-protected playout: %v", err)
	}
	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.Playout.Enabled = true
	st.Playout.Public = true
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := s.mgr.Reconcile(); err != nil {
		t.Fatalf("reconcile so the engine sees the published playout: %v", err)
	}
	if set := s.playoutSettings(); !set.Enabled || !set.Public {
		t.Fatalf("fixture: playout reads enabled=%v public=%v, so the gate would "+
			"refuse everyone and the comparison below would be vacuous",
			set.Enabled, set.Public)
	}
	return h
}

// playoutGet issues one request, optionally carrying the playout token in the
// cookie the product itself mints.
func playoutGet(t *testing.T, h http.Handler, path string, authorised bool) *httptest.ResponseRecorder {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, path, nil)
	if authorised {
		r.AddCookie(&http.Cookie{Name: playoutTokenCookie, Value: playoutWatchToken})
	}
	// Deliberately no Authorization header and no session: see the file
	// comment. A principal this gate does not read would only obscure which
	// channel let the request through.
	return do(t, h, r)
}
