package api

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
)

// ISSUE #168, HALF TWO: THE HEADER INSPECTORS.
//
// The issue says "API response headers are inspected by no test at all". That
// half was ALREADY STALE when this file was written, and saying so precisely
// matters more than closing it twice: ten test files in this package read a
// response header today, covering eight of the sixteen names the derivation
// finds -- Vary, Cache-Control, Content-Type, Access-Control-Allow-Origin,
// WWW-Authenticate, Set-Cookie, Retry-After and Content-Disposition. #168's own
// round-3 comment already recorded the narrower version of this ("rawResponse
// now renders every header, so the invariance sweep does read headers -- but
// only for the routes it sweeps").
//
// What was true is the version of the claim this file answers: the LEDGER
// inspected one header. `response-header/Set-Cookie` had an inspector and the
// other three rows did not, and the eight names no shape row had ever mentioned
// were inspected by the ledger zero times whatever else in the package looked at
// them. A test asserting a header somewhere in the package is not a shape this
// ledger can account for; that distinction is the whole of coverageShape.
// Jurisdiction, and it is why three of these rows could have been discharged
// with a jurisdiction record naming an existing test and are not: an inspector
// the preflight CALLS is the strong discharge, and every one of these turned out
// to be reachable for a request or two.
//
// THE COST, because a preflight nobody can afford to run is a preflight
// somebody deletes. Eight of the fourteen rows below share ONE request on the
// shared rig -- the security middleware writes five of this API's headers on
// every response, and writeJSON plus principalVaryingResponse write three more
// -- so the whole family costs four extra requests on the rig, two throwaway
// playout origins, one self-signed provider and one middleware call with no
// server at all. Measured in the PR body.
//
// WHAT AN INSPECTOR HERE DOES NOT CLAIM. It witnesses ONE emission site of a
// header the derivation finds at up to eight. `response-header/Content-Type` is
// written at eight sites and inspected at one; the row says so. The derivation
// is what makes that statable -- before it, nobody knew the denominator.

// witnessHeader is the shared body of every inspector below.
//
// It requires the header to be PRESENT AND TO CONTAIN THE STRING THAT MAKES IT
// THAT HEADER, on the rule the inspector section of route_ledger_test.go opens
// with: a sample that is merely non-empty would let any header stand in for any
// other, which is the substitution that let a 50-byte error page stand in for a
// manifest. `Cache-Control: public, max-age=10` is a real header and it is not
// the private, no-store one the credential-bearing row is about.
func witnessHeader(t *testing.T, shape, name, want string, h http.Header) shapeObservation {
	t.Helper()
	got := strings.Join(h.Values(name), ", ")
	switch {
	case strings.TrimSpace(got) == "":
		t.Errorf("the %s inspector drove a response that this package's source says emits "+
			"%s and the header is absent. `Inspected` means something read the real emitted "+
			"output; an absent header is the same claim the empty `By` string used to make.\n"+
			"all headers on that response: %v", shape, name, sortedHeaderNames(h))
	case !strings.Contains(got, want):
		t.Errorf("the %s inspector read %s: %q, which does not contain %q.\n"+
			"The row claims this shape is inspected, so what has to be witnessed is the "+
			"header this row is ABOUT rather than any header of that name. A header whose "+
			"value changed out from under the row is a shape the ledger is still counting "+
			"as covered.", shape, name, got, want)
	}
	return shapeObservation{Shape: shape, Sample: name + ": " + got}
}

func sortedHeaderNames(h http.Header) []string {
	set := map[string]bool{}
	for k := range h {
		set[k] = true
	}
	return sortedSet(set)
}

// ---------------------------------------------------------------- the drives

// rigJSONResponse is the ONE request eight header rows share.
//
// GET /api/v1/settings with a read bearer passes through securityHeaders (five
// headers), principalVaryingResponse (Vary and Cache-Control) and writeJSON
// (Content-Type and X-Content-Type-Options). Eight of this API's sixteen
// headers on one round trip, which is why this family is affordable in the
// preflight at all.
//
// The read bearer rather than the admin signer: the response this row is about
// is the redacted one, and Cache-Control: private, no-store exists precisely
// because that body depends on who asked.
func rigJSONResponse(t *testing.T, rig shapeRig) http.Header {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/settings", nil)
	bearer(rig.read)(r)
	w := do(t, rig.h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("the shared header drive got status %d from GET /api/v1/settings with a "+
			"read bearer, so the eight header rows joined to it are reading an error "+
			"response rather than the one they describe.\n%s",
			w.Code, truncateForFailure(w.Body.String()))
	}
	return w.Header()
}

// rigRedirectResponse drives the ONE in-package Location emission.
//
// AND THAT SENTENCE IS THIS ROUND'S CORRECTION. The Location row carried a
// jurisdiction record pointing at cmd/polyemesis, on the reading that the
// header is emitted by the HTTPS redirect wrapper in package main. The
// derivation says otherwise: `http.Redirect` is called twice in this package,
// both in oauth_handlers.go, and the OAuth flow's Location is emitted by a
// handler on this router. The package-main redirect is a second surface, not
// the only one, and this ledger's row was excusing itself out of a header its
// own routes write. No test in this package read a Location header before this
// one.
//
// The error branch of the callback rather than the success one: it needs no
// stored platform credentials and no provider round trip, and it reaches
// oauthDone, which is the function both Location sites funnel through.
func rigRedirectResponse(t *testing.T, rig shapeRig) http.Header {
	t.Helper()
	r := jsonRequest(t, http.MethodGet,
		"/api/v1/oauth/twitch/callback?error=access_denied", nil)
	rig.sign(r)
	w := do(t, rig.h, r)
	if w.Code != http.StatusFound {
		t.Fatalf("the Location drive got status %d from the OAuth callback, want 302. A "+
			"response that did not redirect has no Location to inspect.\n%s",
			w.Code, truncateForFailure(w.Body.String()))
	}
	return w.Header()
}

// rigPosterResponse is the only drive here that plants state rather than
// asking for it, and the reason is that the poster is the one emission site in
// this API that no test in this package has ever reached.
//
// playout.go writes Content-Length at exactly one site: the poster handler,
// after posterJPEG has produced bytes. posterJPEG needs a .ts segment on disk
// and a real FFmpeg to decode a frame out of it, so every fixture in this
// package gets a 404 there -- TestPlayoutPosterVerdict asserts that 404 for
// every principal, and sec_playout_vary_test.go records "with no segment on
// disk the authorised branch reaches posterJPEG" as the reason its columns do
// not diverge. The derivation is what turned that from a fixture quirk into a
// named blind spot: a header this API emits that nothing in the package had
// ever seen emitted.
//
// So the CACHE is primed rather than the pipeline run. posterJPEG returns
// st.poster whenever st.posterAt is inside posterMaxAge, so a planted cache
// entry drives the real handler, through the real mux, down the real 200
// branch, and the bytes on the wire are the handler's. That is the same
// white-box seam inspectSlogOutput uses to point s.log at a buffer, and it buys
// the same thing: an emission site that would otherwise cost an FFmpeg
// stand-in and a spawn wait becomes one request.
//
// SHARED-RIG ETIQUETTE, per the rule stated over inspectOutboundHookBody:
// nothing else in the registry reads the poster or the playout store's cache,
// so this mutation cannot give the inspectors an order.
func rigPosterResponse(t *testing.T, rig shapeRig) http.Header {
	t.Helper()
	st := rig.s.playoutStore()
	st.mu.Lock()
	st.poster = []byte("\xff\xd8\xff\xe0planted poster bytes")
	st.posterErr = nil
	st.posterAt = time.Now()
	st.mu.Unlock()

	r := jsonRequest(t, http.MethodGet, "/api/v1/playout/poster.jpg", nil)
	rig.sign(r)
	w := do(t, rig.h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("the Content-Length drive got status %d from the poster route with a "+
			"primed cache, want 200. The 404 branch sets no Content-Length, so this "+
			"inspector would be reading a header the handler never wrote.", w.Code)
	}
	return w.Header()
}

// rigThrottledLoginResponse spends the login budget for one address so the
// throttle answers 429 with Retry-After.
//
// A RemoteAddr this drive owns, for the reason the throttle exists: the budget
// is per address, so burning one cannot lock out anything else on the shared
// rig. Seven attempts because the allowance is six; the first six pay for a
// bcrypt comparison each, which is the whole of this drive's cost.
func rigThrottledLoginResponse(t *testing.T, rig shapeRig) http.Header {
	t.Helper()
	const addr = "198.51.100.168:44168"
	for i := 0; i < 8; i++ {
		r := jsonRequest(t, http.MethodPost, "/api/v1/auth/login",
			map[string]string{"username": "admin", "password": "not-the-password"})
		r.RemoteAddr = addr
		w := do(t, rig.h, r)
		if w.Code == http.StatusTooManyRequests {
			return w.Header()
		}
	}
	t.Fatalf("the Retry-After drive made 8 failed login attempts from %s and never got a "+
		"429, so the throttle branch that writes this header was never entered. If the "+
		"allowance has grown, this loop is what has to grow with it.", addr)
	return nil
}

// caDownloadResponse drives handleDownloadCA over a real self-signed provider.
//
// The handler rather than the router, which is a weaker drive than every other
// one here and is stated rather than hidden: the planted fixture runs with TLS
// off, so its /api/v1/tls/ca answers 404 and there is no CA to serve. What this
// costs instead is one self-signed provider over a temp dir. The header this
// row is about is written by the HANDLER (certs.go:125) and not by any
// middleware, so the chain the drive skips is not the chain that emits it --
// but a middleware that started rewriting Content-Disposition would be
// invisible here, and that is the honest width of this witness.
func caDownloadResponse(t *testing.T) http.Header {
	t.Helper()
	s := selfSignedServer(t, config.Config{
		TLS: config.TLS{Mode: config.ModeSelfSigned, Hostname: "box.local"}})
	w := httptest.NewRecorder()
	s.handleDownloadCA(w, httptest.NewRequest(http.MethodGet, "/api/v1/tls/ca", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("the Content-Disposition drive got status %d from the CA download, want "+
			"200. Every mode without a local CA answers 404 and writes no attachment "+
			"header at all.", w.Code)
	}
	return w.Header()
}

// playoutChallengeResponse is the Basic challenge on a token-protected origin.
//
// The same fixture inspectStreamingManifest and inspectPlayoutCookie build,
// driven WITHOUT the token: the gate answers 401 and names Basic, because that
// is what turns a bare /playout/ URL pasted into an address bar into a password
// prompt rather than a dead end.
func playoutChallengeResponse(t *testing.T) http.Header {
	t.Helper()
	_, h, _ := playoutOriginServer(t, enabledPlayout(true),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
	r := httptest.NewRequest(http.MethodGet, PlayoutPrefix+"master.m3u8", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	w := do(t, h, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("the WWW-Authenticate drive got status %d from a protected stream with no "+
			"token, want 401. Only the challenge branch writes this header.", w.Code)
	}
	return w.Header()
}

// playoutCORSResponse needs a fixture with cross-origin embedding turned on,
// which no other inspector wants: the three Access-Control-Allow-Origin sites
// in this package are all guarded by AllowCrossOrigin, and it is off by default
// and off on the planted rig.
func playoutCORSResponse(t *testing.T) http.Header {
	t.Helper()
	set := enabledPlayout(true)
	set.AllowCrossOrigin = true
	_, h, sign := playoutOriginServer(t, set,
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
	r := jsonRequest(t, http.MethodGet, "/api/v1/playout/public", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("the Access-Control-Allow-Origin drive got status %d from "+
			"/api/v1/playout/public, want 200.\n%s",
			w.Code, truncateForFailure(w.Body.String()))
	}
	return w.Header()
}

// hstsResponse drives securityHeaders directly, and it is the only drive here
// with no server behind it at all.
//
// Strict-Transport-Security is written under two conditions this package's test
// fixtures never both satisfy: the resolved TLS mode has to be one a browser
// will validate (acme or manual), and r.TLS has to be non-nil, because a
// forwarded header is deliberately not consulted. securityHeaders is a
// constructor taking exactly those two inputs, so the drive is the constructor
// plus a request that really carries a TLS state -- which is the same fixture
// its own doc comment says it was built to allow ("can be tested without
// building a server").
func hstsResponse(t *testing.T) http.Header {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	r.TLS = &tls.ConnectionState{}
	securityHeaders(config.ModeACME, true)(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	).ServeHTTP(w, r)
	return w.Header()
}

// ------------------------------------------------------------ the inspectors
//
// One named function per row, rather than one factory closed over a header
// name. inspectorName reads the runtime name of the func value into the
// artifact's `inspectedBy` column, and a factory would write
// `api.headerInspector.func1` into fourteen rows -- a derived name that is
// technically honest and tells a reader of the ledger nothing about which
// function ran. #176's whole point is that the column names something real.
//
// EVERY ONE OF THESE WAS MUTATION TESTED against the production line it names,
// and each mutation was observed to fail THE SUBTEST OF THAT ROW rather than
// the parent -- which is the property a shared drive puts at risk, since eight
// of them read one response and a mutation that took the whole response down
// would fail all eight and prove none of them individually. The mutations, each
// a one-line edit reverted immediately after:
//
//	Content-Type               api.go writeJSON -> "text/plain; charset=utf-8"
//	X-Content-Type-Options     api.go writeJSON -> "sniff-please"
//	Cache-Control              redact.go        -> "private, max-age=60"
//	Vary                       redact.go        -> Add("Vary","Accept")
//	Content-Security-Policy    security.go      -> "frame-ancestors 'none'"
//	X-Frame-Options            security.go      -> "SAMEORIGIN"
//	Referrer-Policy            security.go      -> "origin-when-cross-origin"
//	Permissions-Policy         security.go      -> "gyroscope=()"
//	Strict-Transport-Security  security.go      -> "preload" (no max-age)
//	Location                   oauth_handlers.go oauthDone target -> "/dashboard"
//	Content-Disposition        certs.go         -> "inline; filename=..."
//	WWW-Authenticate           playout.go       -> `Bearer realm=...`
//	Access-Control-Allow-Origin playout.go      -> "https://example.test"
//	Retry-After                handlers.go      -> Itoa(int(Ceil(0*wait.Seconds())))
//	Content-Length             playout.go       -> the Set line DELETED
//
// All fifteen were observed to fail by name, e.g.
// "--- FAIL: TestEveryInspectedShapeWitnessesItself/response-header/Retry-After
// ... the Retry-After inspector read \"Retry-After: 0\", which is not a positive
// number of seconds". The Content-Length one is the only mutation that DELETES
// rather than rewrites, deliberately: it is what exercises witnessHeader's
// absent branch, which every other mutation leaves untouched.
//
// One mutation was rejected and is recorded because the rejection is the
// interesting part. `Retry-After -> "0"` as a bare literal does not compile --
// it orphans the math and strconv imports -- so it fails the build rather than
// the test, and a build failure is not a mutation result. Multiplying the wait
// by zero keeps both imports live and produces the same emitted byte.

func inspectContentTypeHeader(t *testing.T, rig shapeRig) shapeObservation {
	t.Helper()
	return witnessHeader(t, "response-header/Content-Type", "Content-Type",
		"application/json", rigJSONResponse(t, rig))
}

func inspectNosniffHeader(t *testing.T, rig shapeRig) shapeObservation {
	t.Helper()
	return witnessHeader(t, "response-header/X-Content-Type-Options",
		"X-Content-Type-Options", "nosniff", rigJSONResponse(t, rig))
}

func inspectCacheControlHeader(t *testing.T, rig shapeRig) shapeObservation {
	t.Helper()
	return witnessHeader(t, "response-header/Cache-Control", "Cache-Control",
		"no-store", rigJSONResponse(t, rig))
}

func inspectVaryHeader(t *testing.T, rig shapeRig) shapeObservation {
	t.Helper()
	return witnessHeader(t, "response-header/Vary", "Vary",
		"Authorization", rigJSONResponse(t, rig))
}

func inspectCSPHeader(t *testing.T, rig shapeRig) shapeObservation {
	t.Helper()
	return witnessHeader(t, "response-header/Content-Security-Policy",
		"Content-Security-Policy", "default-src 'self'", rigJSONResponse(t, rig))
}

func inspectFrameOptionsHeader(t *testing.T, rig shapeRig) shapeObservation {
	t.Helper()
	return witnessHeader(t, "response-header/X-Frame-Options", "X-Frame-Options",
		"DENY", rigJSONResponse(t, rig))
}

func inspectReferrerPolicyHeader(t *testing.T, rig shapeRig) shapeObservation {
	t.Helper()
	return witnessHeader(t, "response-header/Referrer-Policy", "Referrer-Policy",
		"no-referrer", rigJSONResponse(t, rig))
}

func inspectPermissionsPolicyHeader(t *testing.T, rig shapeRig) shapeObservation {
	t.Helper()
	return witnessHeader(t, "response-header/Permissions-Policy", "Permissions-Policy",
		"camera=()", rigJSONResponse(t, rig))
}

func inspectHSTSHeader(t *testing.T, _ shapeRig) shapeObservation {
	t.Helper()
	return witnessHeader(t, "response-header/Strict-Transport-Security",
		"Strict-Transport-Security", "max-age=", hstsResponse(t))
}

func inspectLocationHeader(t *testing.T, rig shapeRig) shapeObservation {
	t.Helper()
	return witnessHeader(t, "response-header/Location", "Location",
		"/settings?tab=platforms", rigRedirectResponse(t, rig))
}

func inspectContentDispositionHeader(t *testing.T, _ shapeRig) shapeObservation {
	t.Helper()
	return witnessHeader(t, "response-header/Content-Disposition", "Content-Disposition",
		"attachment;", caDownloadResponse(t))
}

func inspectWWWAuthenticateHeader(t *testing.T, _ shapeRig) shapeObservation {
	t.Helper()
	return witnessHeader(t, "response-header/WWW-Authenticate", "WWW-Authenticate",
		"Basic ", playoutChallengeResponse(t))
}

func inspectCORSHeader(t *testing.T, _ shapeRig) shapeObservation {
	t.Helper()
	return witnessHeader(t, "response-header/Access-Control-Allow-Origin",
		"Access-Control-Allow-Origin", "*", playoutCORSResponse(t))
}

func inspectRetryAfterHeader(t *testing.T, rig shapeRig) shapeObservation {
	t.Helper()
	obs := witnessHeader(t, "response-header/Retry-After", "Retry-After",
		"", rigThrottledLoginResponse(t, rig))
	// The discriminating property, and the one witnessHeader's substring rule
	// cannot express: a Retry-After of "0" is present, is a valid header, and
	// tells the client to retry immediately -- which is the throttle not
	// throttling. login_throttle_test.go rejects it for the same reason.
	if secs, err := strconv.Atoi(strings.TrimPrefix(obs.Sample, "Retry-After: ")); err != nil ||
		secs <= 0 {
		t.Errorf("the Retry-After inspector read %q, which is not a positive number of "+
			"seconds (%v). A zero or unparseable delay is a throttle that asks the caller "+
			"to come straight back.", obs.Sample, err)
	}
	return obs
}

func inspectContentLengthHeader(t *testing.T, rig shapeRig) shapeObservation {
	t.Helper()
	obs := witnessHeader(t, "response-header/Content-Length", "Content-Length",
		"", rigPosterResponse(t, rig))
	if n, err := strconv.Atoi(strings.TrimPrefix(obs.Sample, "Content-Length: ")); err != nil ||
		n <= 0 {
		t.Errorf("the Content-Length inspector read %q, which is not a positive byte count "+
			"(%v). This site sets the length of a rendered poster; a zero there is a "+
			"response whose body the handler believes is empty.", obs.Sample, err)
	}
	return obs
}
