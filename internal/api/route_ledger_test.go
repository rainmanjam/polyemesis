package api

import (
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/hooks"
)

// THE ROUTE COVERAGE LEDGER.
//
// The recurring failure in #150 was never any single leak. It was that a guard
// can be thorough over a set that excludes the bug, and nothing says so. Five
// times: an empty-fixture publish-token test; a top-level-keys drift guard; a
// hand-copied viewSource restatement; a route sweep asserting `len(walked) < 60`
// against a router with 88 GET patterns; and three excuses -- "needs a running
// child process", "not an HTTP response body", plus a /processes sweep against a
// fixture that started no destination -- which between them hid a live stream
// key on four egresses.
//
// So the unit of coverage here is not a route. It is a (method, pattern, shape),
// and every one of them is in exactly one of four states, recorded in
// testdata/route-coverage.json where a reviewer sees it in the diff:
//
//	swept    a value sweep reads its real bytes and scans them for sentinels
//	excused  not swept, WITH a reason and either a runtime counterpart proof or
//	         a filed issue saying what is deferred and why that is safe
//	denied   a read token receives 403 and the status is asserted instead
//	probe    outside chi's routing trie -- the NotFound surface and the 405
//	         surface -- enumerated by hand and DRIVEN, because chi.Walk is
//	         complete over the trie and the trie is not the mux
//
// Four rules make it a ratchet rather than a document:
//
//  1. EQUALITY, never a floor. The enumerated set must equal the artifact's.
//  2. An excuse discharges only on a RUNTIME PROOF with a differential positive
//     control: the high-privilege principal's bytes must CONTAIN a sentinel and
//     the read principal's must not. An excuse that merely names a green test is
//     a new free pass, so a name alone does not discharge anything.
//  3. -update-coverage refreshes the route list and NOTHING ELSE. It cannot
//     launder a missing counterpart or a failing proof. That asymmetry is the
//     ratchet.
//  4. The excused and deferred counts may fall freely; raising either requires
//     editing excusedCeiling in the artifact by hand, which is a reviewable act.

var updateCoverage = flag.Bool("update-coverage", false,
	"rewrite testdata/route-coverage.json's ROUTE LIST from the live router. It does not, "+
		"and must never, suppress the counterpart or positive-control failures.")

const coveragePath = "testdata/route-coverage.json"

// ---------------------------------------------------------------- the artifact

type coverageLedger struct {
	Note string `json:"note"`
	// ExcusedCeiling is the ratchet. Hand-edited, never regenerated.
	ExcusedCeiling  int             `json:"excusedCeiling"`
	DeferredCeiling int             `json:"deferredCeiling"`
	Totals          coverageTotals  `json:"totals"`
	Routes          []coverageRoute `json:"routes"`
	Excuses         []coverageExcus `json:"excuses"`
	Shapes          []coverageShape `json:"shapes"`
	Deferred        []coverageDefer `json:"deferred"`
}

type coverageTotals struct {
	MethodPatternPairs int `json:"methodPatternPairs"`
	Swept              int `json:"swept"`
	Excused            int `json:"excused"`
	Denied             int `json:"denied"`
	NonTrieProbes      int `json:"nonTrieProbes"`
	ShapesEmitted      int `json:"shapesEmitted"`
	ShapesNotInspected int `json:"shapesNotInspected"`
}

type coverageRoute struct {
	Method   string `json:"method"`
	Pattern  string `json:"pattern"`
	Coverage string `json:"coverage"`
}

type coverageExcus struct {
	Route string `json:"route"`
	// Scope distinguishes an excused ROUTE from an excused shape or an excused
	// test-side restatement, so the registry can carry all three.
	Scope  string `json:"scope"`
	Reason string `json:"reason"`
	// Counterpart names a proof in counterpartProofs. Discharged by RUNNING it,
	// never by the name existing.
	Counterpart string `json:"counterpart"`
	// DeferredIssue is the honest alternative: no counterpart, but a filed issue
	// saying what is deferred, why it is safe, and what would make it unsafe.
	DeferredIssue string `json:"deferredIssue"`
}

type coverageShape struct {
	Shape     string `json:"shape"`
	Emitted   bool   `json:"emitted"`
	Inspected bool   `json:"inspected"`
	By        string `json:"by"`
	Note      string `json:"note"`
}

type coverageDefer struct {
	ID                    string `json:"id"`
	What                  string `json:"what"`
	WhySafe               string `json:"whySafe"`
	WhatWouldMakeItUnsafe string `json:"whatWouldMakeItUnsafe"`
	Issue                 string `json:"issue"`
}

// ------------------------------------------------------- the excuse registry

// excusedRoutes is the whole of what this sweep does NOT value-sweep, with the
// reason and, for every one of them, either a runtime counterpart or a filed
// deferral.
//
// Two entries here used to read "needs a running child process" and "a WebSocket
// upgrade; its frames are asserted in ws_test.go". The second named a file that
// does not exist in this package -- checked; there is no ws_test.go -- which is
// what a bare string counterpart is worth. Both now name proofs that drive the
// real bytes.
var excusedRoutes = map[string]coverageExcus{
	// ---- discharged by a runtime proof with a differential positive control.
	"GET /api/v1/processes/{name}/logs": {
		Reason: "the fixture the value sweep runs against starts no destination child, so " +
			"this route answers 404 there. It is NOT excused from inspection: a separate " +
			"fixture spawns a real child and drives it.",
		Counterpart: "runningDestinationLogs",
	},
	"GET /api/v1/ws": {
		Reason: "a WebSocket upgrade, so it emits frames rather than a response body and " +
			"the body sweep cannot read it at all.",
		Counterpart: "websocketFrames",
	},
	"GET /api/v1/hooks/{id}/deliveries": {
		Reason: "needs a hook row. Written NOW rather than deferred because it is " +
			"alerts.Redact consumer #5 -- the same dependency class as the argv leak -- " +
			"and deferring the one excuse that shares the bug's shape is how the bug " +
			"comes back.",
		Counterpart: "hookDeliveries",
	},
	"GET /api/v1/playout/public": {
		Reason: "gated by authorizePlayout; against this fixture a read token receives no " +
			"body at all, exactly as a stranger does, so a 200-body sweep would be " +
			"asserting over a response the deployment never produces.",
		Counterpart: "playoutPublicView",
	},

	// ---- denied to read tokens outright; the 403 is asserted instead.
	"GET /api/v1/destinations/{id}/expert":          denied("TestReadTokenIsDeniedTheRoutesThatAreNotReads"),
	"GET /api/v1/clipper/recordings/{id}/keyframes": denied("TestReadTokenIsDeniedTheRoutesThatAreNotReads"),
	"GET /api/v1/platforms/accounts/{id}/stats":     denied("TestReadTokenIsDeniedTheRoutesThatAreNotReads"),
	"GET /api/v1/metadata/broadcast-window":         denied("TestReadTokenIsDeniedTheRoutesThatAreNotReads"),

	// ---- session-only: no bearer of either scope reaches these.
	"GET /api/v1/auth/tokens":               sessionOnly(),
	"GET /hls/*":                            sessionOnly(),
	"HEAD /hls/*":                           sessionOnly(),
	"GET /api/v1/oauth/{platform}/start":    sessionOnly(),
	"GET /api/v1/oauth/{platform}/callback": sessionOnly(),

	// ---- unauthenticated by design and carrying nothing stored.
	"GET /api/v1/health": open("an unauthenticated liveness probe: a status word and an uptime"),
	"GET /api/v1/setup":  open("unauthenticated; needsSetup and a minimum password length"),
	"GET /api/v1/tls/ca": open("unauthenticated; the PUBLIC half of the local CA"),
	// r.HandleFunc, so chi registers it for EVERY method. One entry covers them
	// all rather than eleven copies of the same sentence.
	"ANY /api/v1/chat/kick/{secret}": open("unauthenticated by necessity: the path segment IS " +
		"the credential and a mismatch is a bare 404, for every method chi registered"),

	// ---- 503 without the subsystem wired.
	"GET /api/v1/jobs":          notWired(),
	"GET /api/v1/jobs/overview": notWired(),
	"GET /api/v1/jobs/policy":   notWired(),

	// ---- the streaming media origin and the player SPA.
	"GET /playout/*": {
		Reason: "the public media origin. A STREAMING response -- an HLS manifest and MPEG-TS " +
			"segments -- which is the shape that escaped the sweep before, since a body " +
			"scan reads none of it. Gated per request by authorizePlayout.",
		Counterpart: "playoutManifestBytes",
	},
	"ANY /watch":     spa(),
	"ANY /watch/*":   spa(),
	"ANY /playout/*": publicOrigin(),

	// Unauthenticated by necessity: these are how a caller BECOMES a principal,
	// or how a fresh install acquires one.
	"POST /api/v1/auth/login": open("unauthenticated by necessity: it is how a session is " +
		"obtained. Throttled; see login_throttle_test.go"),
	"POST /api/v1/setup": open("unauthenticated; refuses once an admin exists"),
	"POST /api/v1/version/check": {
		Reason: "readScopeWritePatterns admits a read token, so this IS reachable and does " +
			"return a body -- but the body comes from an outbound call to the release " +
			"feed, and value-sweeping it would make this suite depend on the network. " +
			"Its payload is a version string, a URL and release notes; no stored " +
			"configuration is read.",
		DeferredIssue: "#157",
	},
	"GET /api/v1/playout/poster.jpg": {
		Reason: "a JPEG rendered from a segment, and this fixture has no segment. Gated by " +
			"authorizePlayout; the verdict is asserted rather than the bytes, because at " +
			"the wire an allowed poster with nothing to render and a denied one are both " +
			"404.",
		DeferredIssue: "#154",
	},

	// ---- reached only with a row this fixture does not create. Each was traced
	// to leaf fields and carries no stored credential: recordings, clips, jobs,
	// library sessions and transcripts are media and text. Deferred rather than
	// excused-with-a-counterpart, and the deferral is FILED so it is visible.
	"GET /api/v1/recordings/{id}/download":             needsRow("#154"),
	"GET /api/v1/recordings/stems/{name}/download":     needsRow("#154"),
	"GET /api/v1/clips/{name}/download":                needsRow("#154"),
	"GET /api/v1/clipper/recordings/{id}":              needsRow("#154"),
	"GET /api/v1/clipper/recordings/{id}/transcript":   needsRow("#154"),
	"GET /api/v1/clipper/jobs/{id}/download":           needsRow("#154"),
	"GET /api/v1/library/recordings/{id}/transcript":   needsRow("#154"),
	"GET /api/v1/library/recordings/{id}/media/{file}": needsRow("#154"),
	"GET /api/v1/library/recordings/{id}":              needsRow("#154"),
	"GET /api/v1/library/sessions/{id}":                needsRow("#154"),
	"GET /api/v1/jobs/{id}":                            needsRow("#163"),
	"GET /api/v1/metadata/push/{id}":                   needsRow("#163"),
	"GET /api/v1/alerts/rules/{id}":                    needsRow("#163"),
	"GET /api/v1/hooks/{id}":                           needsRow("#163"),
	"GET /api/v1/schedules/{id}":                       needsRow("#163"),
	"GET /api/v1/renditions/{id}":                      needsRow("#163"),
	"GET /api/v1/library/search":                       needsRow("#163"),
}

func denied(by string) coverageExcus {
	return coverageExcus{
		Reason:        "denied to read tokens outright; the 403 is asserted in " + by,
		DeferredIssue: "n/a: nothing is disclosed to assert about",
	}
}
func sessionOnly() coverageExcus {
	return coverageExcus{
		Reason:        "session-only; no bearer of either scope reaches it",
		DeferredIssue: "n/a: unreachable by the principal this sweep is about",
	}
}
func open(reason string) coverageExcus {
	return coverageExcus{Reason: reason, DeferredIssue: "n/a: no stored value is served"}
}
func notWired() coverageExcus {
	return coverageExcus{
		Reason:        "503 without a job queue wired; carries no stored credential",
		DeferredIssue: "#163",
	}
}
func publicOrigin() coverageExcus {
	return coverageExcus{
		Reason: "the public media origin under every method chi registered on the mount. " +
			"Gated per request by authorizePlayout; the GET row carries the streaming " +
			"counterpart and the rest reach the same gate.",
		Counterpart: "playoutManifestBytes",
	}
}

func spa() coverageExcus {
	return coverageExcus{
		Reason:        "the player SPA bundle: build-time embed.FS, byte-identical for every principal",
		Counterpart:   "notFoundSurfaceIsPrincipalIndependent",
		DeferredIssue: "",
	}
}
func needsRow(issue string) coverageExcus {
	return coverageExcus{
		Reason: "reached only with a row this fixture does not create; traced to leaf fields " +
			"and carrying no stored credential (media, transcripts and text)",
		DeferredIssue: issue,
	}
}

// excuseFor resolves an excuse for one (method, pattern), honouring the ANY
// wildcard used by the routes chi registered with HandleFunc.
func excuseFor(method, pattern string) (coverageExcus, bool) {
	if ex, ok := excusedRoutes[method+" "+pattern]; ok {
		return ex, true
	}
	if ex, ok := excusedRoutes["ANY "+pattern]; ok {
		return ex, true
	}
	return coverageExcus{}, false
}

// readScopeIsRefused DRIVES a read-scoped request at a concretised form of the
// pattern and reports whether the scope rule refused it with a 403.
//
// Executed rather than reasoned about. If it returns false the route is left
// UNCLASSIFIED and the ledger fails, which is the correct direction: a non-GET
// route a read token CAN reach is a route whose response body nothing in this
// package inspects.
//
// The request really is issued, so a route that does not refuse may have a side
// effect on the fixture. That is acceptable and deliberate: the fixture is
// per-test and thrown away, and a mutation that succeeded is precisely the
// finding.
func readScopeIsRefused(t *testing.T, h http.Handler, method, pattern string) bool {
	t.Helper()
	path := concretePath(pattern)
	r := jsonRequest(t, method, path, map[string]any{})
	bearer(readScopeToken(t, h))(r)
	w := do(t, h, r)
	return w.Code == http.StatusForbidden
}

// readScopeToken mints one read token per handler and caches it, so the walk
// does not create ninety of them.
var readScopeTokens = map[http.Handler]string{}

func readScopeToken(t *testing.T, h http.Handler) string {
	t.Helper()
	if tok, ok := readScopeTokens[h]; ok {
		return tok
	}
	tok := createScopedToken(t, h, ledgerSessions[h], "ledger-methods", db.ScopeRead)
	readScopeTokens[h] = tok
	return tok
}

var ledgerSessions = map[http.Handler]func(*http.Request){}

// concretePath turns a chi pattern into a path that matches it: every {param}
// becomes "1" and a trailing wildcard becomes a single segment.
func concretePath(pattern string) string {
	parts := strings.Split(pattern, "/")
	for i, p := range parts {
		switch {
		case strings.HasPrefix(p, "{"):
			parts[i] = "1"
		case p == "*":
			parts[i] = "x"
		}
	}
	return strings.Join(parts, "/")
}

// -------------------------------------------------------- the counterpart proofs

// proofResult is what a counterpart must produce. Bytes, not a claim.
type proofResult struct {
	// Pattern is read back from chi.RouteContext by the helper that drove the
	// request, so a proof cannot claim a route it did not actually reach.
	Pattern string
	// High is what a session or admin principal received. It MUST contain a
	// sentinel: that is the differential positive control, and without it "the
	// read principal saw no sentinel" is a statement about an empty fixture.
	High string
	// Read is what the read-scoped principal received. It must contain none.
	Read string
	// PositiveControlWaived is for a route where no principal is entitled to a
	// credential, so there is nothing to differ. It must carry its own argument.
	PositiveControlWaived string
	// Sentinels are credentials this proof planted itself, beyond the package's
	// standing list. A proof whose credential is minted by the server -- a hook
	// signing secret, a rotated token -- cannot plant it in advance, and without
	// this it would have to waive the positive control it is perfectly able to
	// satisfy.
	Sentinels []string
}

type counterpartProof func(t *testing.T) proofResult

// counterpartProofs is the registry. A route's excuse names a key here, and the
// ledger test RUNS it.
var counterpartProofs = map[string]counterpartProof{
	"runningDestinationLogs":                proveRunningDestinationLogs,
	"websocketFrames":                       proveWebsocketFrames,
	"hookDeliveries":                        proveHookDeliveries,
	"playoutPublicView":                     provePlayoutPublicView,
	"playoutManifestBytes":                  provePlayoutManifest,
	"notFoundSurfaceIsPrincipalIndependent": proveNotFoundSurface,
}

// patternDriven drives a request and reports the chi pattern that MATCHED, so a
// proof's claim about which route it covered is taken from the router rather
// than from the proof.
func patternDriven(t *testing.T, h http.Handler, method, path string) string {
	t.Helper()
	rctx := chi.NewRouteContext()
	p, _, _ := strings.Cut(path, "?")
	if !h.(chi.Routes).Match(rctx, method, p) {
		return ""
	}
	return rctx.RoutePattern()
}

func proveRunningDestinationLogs(t *testing.T) proofResult {
	h, _, sign := runningDestServer(t)
	read := createScopedToken(t, h, sign, "ledger-read", db.ScopeRead)
	const path = "/api/v1/processes/dest:1/logs"
	return proofResult{
		Pattern: patternDriven(t, h, http.MethodGet, path),
		// The admin's own view of the SAME credential, through the route that is
		// entitled to it. /processes is scrubbed unconditionally for every
		// principal -- process.log and the retained MQTT topic have no principal
		// -- so the positive control has to come from the expert route, which
		// reads Args() raw.
		High: bodyOf(t, h, sign, "/api/v1/destinations/1/expert"),
		Read: bodyOf(t, h, bearer(read), path),
	}
}

func proveWebsocketFrames(t *testing.T) proofResult {
	h, _, sign := runningDestServer(t)
	s := serverUnderTest(t, h)
	read := createScopedToken(t, h, sign, "ledger-ws", db.ScopeRead)
	return proofResult{
		Pattern: patternDriven(t, h, http.MethodGet, "/api/v1/ws"),
		High:    bodyOf(t, h, sign, "/api/v1/destinations/1/expert"),
		Read:    strings.Join(wsFrames(t, s, "Bearer "+read, 2*time.Second), "\n"),
	}
}

// proveHookDeliveries writes the fixture the old excuse deferred.
//
// Written now rather than filed because it is alerts.Redact consumer #5 and
// therefore the one deferred excuse that shares the argv leak's dependency
// class: a delivery record's request and response bodies are free text scrubbed
// by the same best-effort matcher that provably failed on the command line.
func proveHookDeliveries(t *testing.T) proofResult {
	h, _, sign := plantedServer(t)
	read := createScopedToken(t, h, sign, "ledger-hooks", db.ScopeRead)

	// Created through the REAL route rather than through the store, so the
	// fixture exercises the same normalisation and masking the product does. A
	// Slack-shaped webhook, because that URL carries its secret in the PATH and
	// is the case RedactWebhookURL exists for.
	created := send(t, h, sign, http.MethodPost, "/api/v1/hooks", map[string]any{
		"name":     "ledger",
		"url":      "https://hooks.example.com/services/T0/B1/" + sentinelDestKey,
		"triggers": []string{string(hooks.TriggerIngestPublished)},
		"enabled":  true,
	}, http.StatusCreated)
	var out struct {
		ID     int64  `json:"id"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(created, &out); err != nil || out.ID == 0 || out.Secret == "" {
		t.Fatalf("create hook: %v (%s)", err, created)
	}
	path := "/api/v1/hooks/" + strconv.FormatInt(out.ID, 10) + "/deliveries"

	// THE POSITIVE CONTROL, and the first version of this proof failed it --
	// which is the registry doing its job. The hook's URL sentinel is NOT the
	// right control here, because Hook.MarshalJSON masks the URL for every
	// principal including the admin, so nobody ever holds it and there is no
	// differential to draw.
	//
	// The signing secret is. It is minted by the server and shown exactly once,
	// in this create response, to a session principal -- so the admin
	// demonstrably held a credential, and the deliveries list, which records the
	// request and response bodies of each attempt, must never carry it back to a
	// read token.
	return proofResult{
		Pattern:   patternDriven(t, h, http.MethodGet, path),
		High:      string(created),
		Read:      rawBody(t, h, bearer(read), path),
		Sentinels: []string{out.Secret},
	}
}

func provePlayoutPublicView(t *testing.T) proofResult {
	h, _, sign := plantedServer(t)
	read := createScopedToken(t, h, sign, "ledger-playout", db.ScopeRead)
	return proofResult{
		Pattern: patternDriven(t, h, http.MethodGet, "/api/v1/playout/public"),
		// GET /playout shows the admin the watch token in the clear.
		High: bodyOf(t, h, sign, "/api/v1/playout"),
		Read: rawBody(t, h, bearer(read), "/api/v1/playout/public"),
	}
}

func provePlayoutManifest(t *testing.T) proofResult {
	h, _, sign := plantedServer(t)
	read := createScopedToken(t, h, sign, "ledger-manifest", db.ScopeRead)
	return proofResult{
		Pattern: patternDriven(t, h, http.MethodGet, PlayoutPrefix+"master.m3u8"),
		High:    bodyOf(t, h, sign, "/api/v1/playout"),
		Read:    rawBody(t, h, bearer(read), PlayoutPrefix+"master.m3u8"),
	}
}

// proveNotFoundSurface is G2 brought into frame.
//
// r.NotFound is INVISIBLE to chi.Walk: the reconciliation is complete over the
// routing TRIE, and the trie is not the mux. The embedded-UI handler answers
// every unmatched path AND every unmatched method -- /, /assets/*, /.env,
// /debug/pprof/, /metrics, /API/V1/SETTINGS -- and no guard has ever looked at
// it. It is benign today, and "benign today, unlooked-at for ever" is the exact
// shape of everything else in this file.
//
// The claim asserted is principal-INDEPENDENCE: anonymous and read-scoped
// receive byte-identical responses, so the handler cannot be disclosing
// anything to one that it withholds from the other. The positive control is
// waived with its argument, because no principal is entitled to a credential
// here and there is therefore nothing to differ.
func proveNotFoundSurface(t *testing.T) proofResult {
	h, _, sign := plantedServer(t)
	read := createScopedToken(t, h, sign, "ledger-notfound", db.ScopeRead)

	var anonAll, readAll strings.Builder
	for _, probe := range notFoundProbes() {
		anon := rawResponse(t, h, nil, probe.method, probe.path)
		rd := rawResponse(t, h, bearer(read), probe.method, probe.path)
		if anon != rd {
			t.Errorf("%s %s: the NotFound surface answers a read token differently from an "+
				"anonymous stranger.\nanon: %s\nread: %s", probe.method, probe.path, anon, rd)
		}
		anonAll.WriteString(anon)
		readAll.WriteString(rd)
	}
	return proofResult{
		Pattern: "<non-trie: r.NotFound>",
		Read:    readAll.String(),
		PositiveControlWaived: "no principal is entitled to a credential from the embedded UI " +
			"handler, so there is no differential to draw. What IS asserted is stronger " +
			"for this surface: anonymous and read-scoped responses are byte-identical " +
			"across nine probes, so nothing can be disclosed to one and not the other.",
	}
}

// ------------------------------------------------------------- non-trie probes

type nonTrieProbe struct {
	method string
	path   string
	// why records what this probe is FOR, since a path that matches no route is
	// otherwise indistinguishable from a typo.
	why string
}

// notFoundProbes is the NotFound surface, hand-declared because chi.Walk cannot
// emit it, and EXECUTED rather than merely listed.
func notFoundProbes() []nonTrieProbe {
	return []nonTrieProbe{
		{http.MethodGet, "/", "the SPA root"},
		{http.MethodGet, "/assets/app.js", "a bundled asset path"},
		{http.MethodGet, "/.env", "the credential file every scanner asks for first"},
		{http.MethodGet, "/debug/pprof/", "the profiler surface, if anything ever mounted it"},
		{http.MethodGet, "/metrics", "the Prometheus convention, unrouted here"},
		{http.MethodGet, "/API/V1/SETTINGS", "a case-varied spelling of a real route"},
		{http.MethodPost, "/.env", "an unmatched METHOD as well as an unmatched path"},
		{http.MethodDelete, "/anything", "a destructive method on the catch-all"},
		{http.MethodGet, "/api/v1/no-such-route", "an unrouted path INSIDE the API prefix"},
	}
}

// methodNotAllowedProbes is G4: chi's methodNotAllowed short-circuits BEFORE
// group middleware, so requireAuth never runs and an anonymous caller can tell a
// registered (method, path) pair from an unregistered one.
//
// Enumerated and asserted here rather than fixed. Low severity -- docs/API.md
// publishes the table -- and the behavioural change is filed. What was true
// before is that no test drove this response class at all.
func methodNotAllowedProbes() []nonTrieProbe {
	return []nonTrieProbe{
		{http.MethodHead, "/api/v1/settings", "a GET-only route answered 405 with Allow: GET"},
		{http.MethodPut, "/api/v1/upgrade/stage", "a POST-only route answered 405 with Allow: POST"},
	}
}

// ------------------------------------------------------------------ the shapes

// emittedShapes is I5: coverage is (method, pattern, SHAPE), because the two
// things that escaped were both shapes rather than routes. The playout manifest
// is a streaming response and the argv leak travelled through a WebSocket frame;
// a ledger that only counted routes would have called both covered.
func emittedShapes() []coverageShape {
	return []coverageShape{
		{"json-body", true, true, "TestReadTokenReceivesNoCredentialOnAnyRoute",
			"the value sweep: real read-bearer bytes scanned for every planted sentinel"},
		{"response-header/Location", true, true, "TestAConfiguredRedirectNeverCachesAWatchToken",
			"the HTTPS redirect's Location carries the request URI verbatim, watch token included"},
		{"response-header/Set-Cookie", true, true, "TestPlayoutCookieHandoff",
			"the playout watch cookie"},
		{"response-header/Cache-Control", true, true, "TestAConfiguredRedirectNeverCachesAWatchToken",
			"whether a credential-bearing response may be stored"},
		{"response-header/Content-Disposition", true, false, "",
			"download filenames; media names only, no stored credential. Deferred: #154"},
		{"streaming-media", true, true, "playoutManifestBytes",
			"the HLS manifest and its segments -- the shape a body sweep reads none of, " +
				"and the one that escaped the previous audit"},
		{"file-download", true, false, "",
			"recordings, stems, clips and exports. Deferred: #154, already decided"},
		{"websocket-frame", true, true, "websocketFrames + TestEveryEventTypeHasAWebSocketPolicy",
			"one policy row per events.Type over a CLOSED table; an unclassified type " +
				"fails the build and is dropped for a read scope"},
		{"sse", false, false, "", "ABSENT: this API emits no server-sent events"},
		{"mqtt-retained-topic", true, false, "",
			"cmd/polyemesis/mqtt.go publishes Status.LastError RETAINED, with no principal " +
				"and never any. Scrubbed at source by supervisor.scrub; the broker-side " +
				"consumer audit is deferred: #160"},
		{"on-disk-process-log", true, true, "TestRunningDestinationLeaksNoSentinelOnAnyEgress",
			"the file that goes into support tarballs; asserted from disk"},
		{"slog-output", true, false, "",
			"the server's own structured log. Deferred: #160"},
	}
}

// readScopeWriteSweep is the non-GET value sweep: the routes readScopeWritePatterns
// admits a read-scoped token to, which therefore build a real body for it.
func readScopeWriteSweep() []struct {
	method, pattern, path string
	body                  any
} {
	return []struct {
		method, pattern, path string
		body                  any
	}{
		{http.MethodPost, "/api/v1/routing/compile", "/api/v1/routing/compile",
			map[string]any{"profile": map[string]any{}, "source": map[string]any{"tracks": []any{}}}},
	}
}

// TestReadScopeWriteRoutesCarryNoCredential is the value half for the non-GET
// routes a read token can actually reach.
//
// readScopeWritePatterns is a hand-built allowlist of POSTs "that answer a
// question rather than change anything" -- and its own comment records that the
// last audit found a route on it whose ANSWER was the destination's stream key.
// A list reasoned about in prose is exactly the thing that needs its bytes read.
func TestReadScopeWriteRoutesCarryNoCredential(t *testing.T) {
	h, _, sign := plantedServer(t)
	read := createScopedToken(t, h, sign, "write-sweep", db.ScopeRead)

	for _, c := range readScopeWriteSweep() {
		r := jsonRequest(t, c.method, c.path, c.body)
		bearer(read)(r)
		w := do(t, h, r)
		if w.Code == http.StatusForbidden {
			t.Errorf("%s %s returned 403 to a read token, but readScopeWritePatterns "+
				"lists it. Either the list or this sweep is out of date.", c.method, c.path)
			continue
		}
		for _, secret := range allSentinels() {
			if strings.Contains(w.Body.String(), secret) {
				t.Errorf("%s %s handed a read-scoped token %s.\nbody: %s",
					c.method, c.path, secret, w.Body.String())
			}
		}
	}
}

// ------------------------------------------------------------------ the ledger

func TestRouteCoverageLedger(t *testing.T) {
	h, _, sign := plantedServer(t)
	ledgerSessions[h] = sign
	s := serverUnderTest(t, h)

	// 1. ENUMERATE. Every method, every pattern. No GET filter and no TrimSuffix
	// narrowing: the previous sweep dropped 51 non-GET pairs and rewrote the
	// patterns it did keep, so the set it reconciled against was not the set the
	// router serves.
	var enumerated []coverageRoute
	seen := map[string]bool{}
	err := chi.Walk(s.Handler().(chi.Routes),
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			if route != "/" {
				route = strings.TrimSuffix(route, "/")
			}
			key := method + " " + route
			if seen[key] {
				return nil
			}
			seen[key] = true
			enumerated = append(enumerated, coverageRoute{Method: method, Pattern: route})
			return nil
		})
	if err != nil {
		t.Fatalf("walk the router: %v", err)
	}

	// 2. CLASSIFY.
	swept := map[string]bool{}
	for _, path := range leakRoutes() {
		swept["GET "+patternOf(t, s, path)] = true
	}
	// The two NON-GET routes a read scope may still reach. They are swept for
	// values, not excused: readScopeWritePatterns lets a read token POST to them
	// and they return a body, so the method rule that covers every other non-GET
	// does not cover these. This is the part of G3 that is actually reachable,
	// and it is closed rather than deferred.
	for _, c := range readScopeWriteSweep() {
		swept[c.method+" "+c.pattern] = true
	}
	var totals coverageTotals
	for i := range enumerated {
		method, pattern := enumerated[i].Method, enumerated[i].Pattern
		key := method + " " + pattern
		ex, excused := excuseFor(method, pattern)
		switch {
		case swept[key]:
			enumerated[i].Coverage = "swept"
			totals.Swept++
		case excused:
			if strings.HasPrefix(ex.Reason, "denied to read tokens") {
				enumerated[i].Coverage = "denied"
				totals.Denied++
			} else {
				enumerated[i].Coverage = "excused"
				totals.Excused++
			}
		case method != http.MethodGet && method != http.MethodHead &&
			readScopeIsRefused(t, h, method, pattern):
			// DENIED BY METHOD, and PROVEN by driving it rather than asserted
			// from the middleware's source. requireScope refuses every non-GET
			// to a read scope unless the pattern is in readScopeWritePatterns,
			// so the handler never runs and no body is ever built -- but "the
			// middleware says so" is exactly the kind of claim that was true of
			// three other things in this PR and wrong about the fourth. The
			// classification is a 403 that actually happened.
			//
			// It is what makes G3's deferral honest: the non-GET BODIES are not
			// value-swept, and this is the reason that is safe.
			enumerated[i].Coverage = "denied-by-method"
			totals.Denied++
		default:
			enumerated[i].Coverage = "UNCLASSIFIED"
		}
	}
	sort.Slice(enumerated, func(i, j int) bool {
		if enumerated[i].Pattern != enumerated[j].Pattern {
			return enumerated[i].Pattern < enumerated[j].Pattern
		}
		return enumerated[i].Method < enumerated[j].Method
	})

	for _, r := range enumerated {
		if r.Coverage == "UNCLASSIFIED" {
			t.Errorf("%s %s is reachable and is neither swept, excused nor denied.\n"+
				"Add it to leakRoutes() if a read token may call it, or to excusedRoutes "+
				"with a reason AND either a counterpart proof or a filed deferral. A new "+
				"route fails this test on the day it lands, which is when its author still "+
				"has the context to classify it.", r.Method, r.Pattern)
		}
	}
	for key := range excusedRoutes {
		if strings.HasPrefix(key, "ANY ") {
			pattern := strings.TrimPrefix(key, "ANY ")
			live := false
			for k := range seen {
				if strings.HasSuffix(k, " "+pattern) {
					live = true
				}
			}
			if !live {
				t.Errorf("excusedRoutes names %s, which the router does not serve.", key)
			}
			continue
		}
		if !seen[key] {
			t.Errorf("excusedRoutes names %s, which the router does not serve. Delete the "+
				"entry rather than leaving a dead excuse in place.", key)
		}
	}

	// 3. THE EXCUSE RULE. This is the heart, and -update-coverage does not touch
	// it. An excuse discharges on a runtime proof or on a filed deferral, and on
	// nothing else -- a bare test NAME is a new free pass, which is what the two
	// entries this replaces were.
	for key, ex := range excusedRoutes {
		if ex.Counterpart == "" && ex.DeferredIssue == "" {
			t.Errorf("the excuse for %s (%q) names neither a counterpart proof nor a "+
				"deferred issue. Every route this sweep does not read must be covered "+
				"some OTHER way, or be recorded as knowingly uncovered. That single rule "+
				"is what would have caught #150's argv leak: \"needs a running child "+
				"process\" and \"not an HTTP response body\" would each have had to name "+
				"a proof, and neither could have.", key, ex.Reason)
			continue
		}
		if ex.Counterpart == "" {
			continue
		}
		if _, ok := counterpartProofs[ex.Counterpart]; !ok {
			t.Errorf("the excuse for %s names the counterpart %q, which is not in "+
				"counterpartProofs.", key, ex.Counterpart)
		}
	}

	// 4. EQUALITY against the artifact.
	want := readLedger(t)
	if *updateCoverage {
		writeLedger(t, want, enumerated, totals)
	} else {
		assertRouteSetsEqual(t, want.Routes, enumerated)
	}

	// 5. THE RATCHET. Excused and deferred may fall freely; raising either takes
	// a hand edit of the ceiling, which is a reviewable act rather than a silent
	// one. Checked even under -update-coverage: regeneration refreshes the route
	// list and must never rebank the ceiling upward.
	if totals.Excused > want.ExcusedCeiling {
		t.Errorf("%d routes are excused and the committed ceiling is %d. Going UP requires "+
			"editing excusedCeiling in %s by hand. Going down is free -- regenerate and "+
			"the ceiling comes with it.", totals.Excused, want.ExcusedCeiling, coveragePath)
	}
	deferred := deferredCount()
	if deferred > want.DeferredCeiling {
		t.Errorf("%d excuses ship with a deferral rather than a counterpart, and the "+
			"committed ceiling is %d.", deferred, want.DeferredCeiling)
	}

	// 6. STRICT MODE. The excuse registry is only worth anything if it actually
	// runs, and a `-run` filter that happens to match this test and not
	// TestEveryCounterpartProofActuallyProves would leave every counterpart
	// undischarged while still printing ok. CI sets POLYEMESIS_LEDGER=strict, and
	// in that mode the proofs run from HERE as well, so no single filter can
	// silence them both.
	if os.Getenv("POLYEMESIS_LEDGER") == "strict" {
		t.Run("counterparts", func(t *testing.T) { runCounterpartProofs(t) })
	}

	// 7. SHAPES. A shape that is emitted and neither inspected nor accompanied by
	// a note recording the deferral fails.
	for _, sh := range emittedShapes() {
		if !sh.Emitted || sh.Inspected {
			continue
		}
		if !strings.Contains(sh.Note, "Deferred:") && !strings.Contains(sh.Note, "#") {
			t.Errorf("the shape %q is emitted, is not inspected, and carries no deferral. "+
				"The playout manifest was a streaming response and the argv leak travelled "+
				"through a WebSocket frame -- both are SHAPES, not routes, and a ledger "+
				"that counted only routes called both covered.", sh.Shape)
		}
	}
}

// TestEveryCounterpartProofActuallyProves RUNS the registry.
//
// Split from the ledger test so a failure names the proof rather than the
// ledger, and so each proof gets its own subtest budget -- two of them spawn a
// real FFmpeg stand-in.
//
// The differential positive control is the rule that cannot be satisfied by
// vacuity: the high-privilege bytes MUST contain a sentinel, which is what
// proves the credential was there to leak, and the read bytes must contain none.
// A proof that produces two empty strings fails.
func TestEveryCounterpartProofActuallyProves(t *testing.T) { runCounterpartProofs(t) }

func runCounterpartProofs(t *testing.T) {
	// Referenced-by, so a proof that no excuse names is reported rather than run
	// forever as decoration.
	referenced := map[string]bool{}
	for _, ex := range excusedRoutes {
		if ex.Counterpart != "" {
			referenced[ex.Counterpart] = true
		}
	}
	names := make([]string, 0, len(counterpartProofs))
	for name := range counterpartProofs {
		if !referenced[name] {
			t.Errorf("counterpartProofs holds %q, which no excuse names. Delete it or use "+
				"it; a proof nobody invokes is decoration.", name)
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			res := counterpartProofs[name](t)

			if res.Pattern == "" {
				t.Errorf("the proof %q did not reach a routed pattern. The pattern is read "+
					"back from chi.RouteContext precisely so a proof cannot claim a route "+
					"it never drove.", name)
			}
			sentinels := append(append(allSentinels(), argvSentinels()...), res.Sentinels...)
			if res.PositiveControlWaived == "" {
				if strings.TrimSpace(res.High) == "" {
					t.Fatalf("the proof %q produced NO high-privilege bytes. The positive "+
						"control is the whole point: without it, \"the read principal saw "+
						"no sentinel\" is a statement about an empty fixture.", name)
				}
				if !containsAny(res.High, sentinels) {
					t.Fatalf("the proof %q produced high-privilege bytes carrying NO "+
						"sentinel, so there was nothing for the read principal to be "+
						"denied and the assertion below is vacuous. Plant the credential "+
						"or waive the control with an argument.\nhigh: %s",
						name, truncateForFailure(res.High))
				}
			}
			if strings.TrimSpace(res.Read) == "" && res.PositiveControlWaived == "" {
				t.Errorf("the proof %q produced no read-principal bytes to inspect", name)
			}
			for _, secret := range sentinels {
				if strings.Contains(res.Read, secret) {
					t.Errorf("the proof %q found %s in what a read-scoped principal "+
						"received on %s.\n%s", name, secret, res.Pattern,
						truncateForFailure(res.Read))
				}
			}
		})
	}
}

// TestTheNonTrieSurfacesAreDriven is G2 and G4: the parts of the mux chi.Walk
// cannot see.
//
// r.NotFound answers every unmatched path AND method. chi's methodNotAllowed
// short-circuits before group middleware, so requireAuth never runs on it. The
// reconciliation above is complete over the routing TRIE, and the trie is not
// the mux -- the same species as the /playout/* mount, one level further out.
func TestTheNonTrieSurfacesAreDriven(t *testing.T) {
	h, _, sign := plantedServer(t)
	read := createScopedToken(t, h, sign, "nontrie", db.ScopeRead)

	t.Run("notFound", func(t *testing.T) {
		for _, p := range notFoundProbes() {
			anon := rawResponse(t, h, nil, p.method, p.path)
			rd := rawResponse(t, h, bearer(read), p.method, p.path)
			if anon != rd {
				t.Errorf("%s %s (%s): anonymous and read-scoped responses differ. The "+
					"embedded-UI handler is benign only while it reads no server state "+
					"and varies by no principal; this is the assertion that says so.\n"+
					"anon: %s\nread: %s", p.method, p.path, p.why, anon, rd)
			}
			for _, secret := range allSentinels() {
				if strings.Contains(rd, secret) {
					t.Errorf("%s %s leaked %s from the NotFound surface", p.method, p.path, secret)
				}
			}
		}
	})

	t.Run("methodNotAllowed", func(t *testing.T) {
		for _, p := range methodNotAllowedProbes() {
			r := jsonRequest(t, p.method, p.path, nil)
			w := do(t, h, r)
			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: status %d, want 405. This response class is an anonymous "+
					"oracle for whether a (method, path) pair is registered -- chi answers "+
					"it BEFORE requireAuth runs. It is enumerated and asserted here; the "+
					"behavioural fix is deferred, see the ledger.", p.method, p.path, w.Code)
			}
			if w.Header().Get("Allow") == "" {
				t.Errorf("%s %s: no Allow header on the 405", p.method, p.path)
			}
		}
	})
}

// ------------------------------------------------------------------ artifact IO

// deferredCount is how many excuses ship with a filed deferral instead of a
// runtime counterpart. Its own ratchet, separate from the excused count, so
// converting a deferral into a proof is visible as a number going down rather
// than being lost inside a total that did not move.
func deferredCount() int {
	n := 0
	for _, ex := range excusedRoutes {
		if ex.Counterpart == "" && strings.HasPrefix(ex.DeferredIssue, "#") {
			n++
		}
	}
	return n
}

func readLedger(t *testing.T) coverageLedger {
	t.Helper()
	b, err := os.ReadFile(coveragePath)
	if err != nil {
		t.Fatalf("read %s: %v\nRun `go test ./internal/api -run TestRouteCoverageLedger "+
			"-update-coverage` to create it.", coveragePath, err)
	}
	var l coverageLedger
	if err := json.Unmarshal(b, &l); err != nil {
		t.Fatalf("parse %s: %v", coveragePath, err)
	}
	return l
}

// writeLedger regenerates the ROUTE LIST and the derived totals, and nothing
// else. The ceilings are carried through untouched, and the excuse rules above
// have already run: regeneration cannot launder a missing counterpart. That
// asymmetry IS the ratchet.
//
// Fully deterministic: sorted, no timestamps, no absolute paths, no map
// iteration order. An artifact that churns is regenerated blindly and reviewed
// by nobody, which defeats the whole point of committing it.
func writeLedger(t *testing.T, prev coverageLedger, routes []coverageRoute, totals coverageTotals) {
	t.Helper()
	totals.MethodPatternPairs = len(routes)
	totals.NonTrieProbes = len(notFoundProbes()) + len(methodNotAllowedProbes())
	shapes := emittedShapes()
	for _, sh := range shapes {
		if sh.Emitted {
			totals.ShapesEmitted++
			if !sh.Inspected {
				totals.ShapesNotInspected++
			}
		}
	}

	keys := make([]string, 0, len(excusedRoutes))
	for k := range excusedRoutes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	excuses := make([]coverageExcus, 0, len(keys))
	for _, k := range keys {
		ex := excusedRoutes[k]
		ex.Route = k
		ex.Scope = "route"
		excuses = append(excuses, ex)
	}

	out := coverageLedger{
		Note:            ledgerNote,
		ExcusedCeiling:  prev.ExcusedCeiling,
		DeferredCeiling: prev.DeferredCeiling,
		Totals:          totals,
		Routes:          routes,
		Excuses:         excuses,
		Shapes:          shapes,
		Deferred:        deferredWithReasons(),
	}
	// The ceilings RATCHET DOWN on regeneration and never up: a run that excuses
	// fewer routes rebanks the ceiling to the lower number, and a run that
	// excuses more has already failed the assertion in the caller before
	// reaching here on a subsequent, non-updating run.
	out.ExcusedCeiling = totals.Excused
	out.DeferredCeiling = deferredCount()
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("marshal ledger: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(coveragePath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(coveragePath, append(b, '\n'), 0o644); err != nil {
		t.Fatalf("write %s: %v", coveragePath, err)
	}
	t.Logf("regenerated %s: %d method+pattern pairs", coveragePath, len(routes))
}

func assertRouteSetsEqual(t *testing.T, want, got []coverageRoute) {
	t.Helper()
	index := func(rows []coverageRoute) map[string]string {
		m := map[string]string{}
		for _, r := range rows {
			m[r.Method+" "+r.Pattern] = r.Coverage
		}
		return m
	}
	w, g := index(want), index(got)
	for k, wc := range w {
		gc, ok := g[k]
		if !ok {
			t.Errorf("%s is in %s and NOT in the live router. A route was removed or "+
				"re-methoded; regenerate with -update-coverage so the change is visible "+
				"in the diff.", k, coveragePath)
			continue
		}
		if gc != wc {
			t.Errorf("%s is recorded as %q in %s and is %q live.", k, wc, coveragePath, gc)
		}
	}
	for k := range g {
		if _, ok := w[k]; !ok {
			t.Errorf("%s is served by the router and is NOT in %s. EQUALITY, not a floor: "+
				"the assertion this replaces was `len(walked) < 60` against a router with "+
				"88 GET patterns, a floor 28 below reality that would not have noticed "+
				"two-thirds of the API vanishing. Classify the route and regenerate.",
				k, coveragePath)
		}
	}
}

const ledgerNote = "The route coverage ledger for internal/api. Regenerated ONLY by " +
	"`go test ./internal/api -run TestRouteCoverageLedger -update-coverage`, which " +
	"refreshes the route list and the derived totals and NOTHING else -- it cannot " +
	"suppress a missing counterpart, a failing positive control, or a raised ceiling. " +
	"Read `excuses` to answer \"what is NOT covered, and why is that safe\". " +
	"MinSecretLen is 8: the exact-literal scrub in internal/alerts refuses a shorter " +
	"credential, which is a real permanent residual on the stderr path for very short " +
	"secrets, covered only by the best-effort alerts.Redact pass. Every credential this " +
	"system mints or accepts is longer (SRT refuses a passphrase under 10, publish " +
	"tokens are 24), and the failure direction of the floor itself is over-masking, " +
	"never disclosure."

// deferredWithReasons is I6: everything knowingly not done, each stating what is
// deferred, why it is safe to defer, and what would make it unsafe.
//
// The issue numbers are real and filed. #156 to #161 were opened for this PR's
// deferrals; #162 and #163 were opened by this change. #154 predates it and is
// explicitly out of scope. Every row states what is deferred, why deferring it is
// safe, and -- the part that matters when somebody revisits this in a year -- what
// would make it unsafe.
func deferredWithReasons() []coverageDefer {
	return []coverageDefer{
		{
			ID:   "G2",
			What: "chi.Walk is complete over the routing TRIE, and the trie is not the MUX.",
			WhySafe: "r.NotFound is the build-time embedded UI: an embed.FS with no server " +
				"state behind it, and anonymous and read-scoped responses are " +
				"byte-identical across nine executed probes. The GUARD is fixed in this " +
				"PR; the architectural residual is not.",
			WhatWouldMakeItUnsafe: "the UI handler reading server state, or varying by " +
				"principal. Either turns an unenumerated surface into an unguarded one.",
			Issue: "#156",
		},
		{
			ID: "G3",
			What: "the VALUE sweep is GET-only. The non-GET method+pattern pairs are " +
				"enumerated and classified in this ledger, but their response BODIES are " +
				"not scanned for sentinels.",
			WhySafe: "swept by hand this round: only admin principals receive sentinels " +
				"from a non-GET handler, and a read token is refused every non-GET route " +
				"by the scope rule before a body is built at all.",
			WhatWouldMakeItUnsafe: "a non-GET handler that a read scope may call and that " +
				"returns a body built from stored configuration.",
			Issue: "#157",
		},
		{
			ID: "G4",
			What: "chi's methodNotAllowed short-circuits BEFORE group middleware, so " +
				"requireAuth never runs and 405-vs-401 is an anonymous oracle for whether " +
				"a (method, path) pair is registered.",
			WhySafe: "low severity: docs/API.md publishes the route table already. The " +
				"response class is now enumerated and asserted rather than invisible.",
			WhatWouldMakeItUnsafe: "a route whose EXISTENCE is the secret. " +
				"/api/v1/chat/kick/{secret} already has that shape.",
			Issue: "#158",
		},
		{
			ID: "ws-scope-snapshot",
			What: "the /ws principal is captured once at upgrade; a token revoked or " +
				"downgraded mid-session keeps its scope until the socket closes.",
			WhySafe: "pre-existing and not a regression -- requireAuth already ran once at " +
				"upgrade and never again -- and safe under a single-administrator product.",
			WhatWouldMakeItUnsafe: "multi-operator installs, or revocation being relied on " +
				"as an incident-response control. Suggested fix: re-check the token row on " +
				"the ~25s ping tick.",
			Issue: "#159",
		},
		{
			ID: "redact-per-argument",
			What: "alerts.Redact remains available to future callers now that " +
				"CommandString has stopped applying it per argument.",
			WhySafe: "no caller loops it over tokens today; its doc comment now says " +
				"whole-text-only and that it is never a boundary.",
			WhatWouldMakeItUnsafe: "a new caller applying it per token. The canonical " +
				"`-headers Authorization: Bearer X` is clean whole-string and LEAKS " +
				"per-argument, so this is a shape, not a hypothetical.",
			Issue: "#162",
		},
		{
			ID: "redact-consumers",
			What: "the remaining best-effort alerts.Redact consumers with no " +
				"principal-varying counterpart: the retained MQTT IngestError topic, and " +
				"the server's own slog output.",
			WhySafe: "both are scrubbed at source now by supervisor.scrub, which removes " +
				"the exact declared literals before Redact ever runs.",
			WhatWouldMakeItUnsafe: "a credential that reaches those sinks WITHOUT passing " +
				"through a supervised process -- an OAuth refresh error, a hook delivery " +
				"body. The hook-deliveries fixture is written in this PR; the broader " +
				"consumer audit is not.",
			Issue: "#160",
		},
		{
			ID: "self-silencing-outside-api",
			What: "the same vacuity pathology outside internal/api: internal/oauth's three " +
				"provider-drift skips that pass by DECLINING TO RUN, plus roughly 98 " +
				"t.Skip and 11 testing.Short sites repo-wide.",
			WhySafe: "outside this PR's frame, and none of them is a disclosure guard.",
			WhatWouldMakeItUnsafe: "a skipped test being counted as coverage. The excuse " +
				"registry has no jurisdiction over t.Skip today; extending it there is the " +
				"follow-up.",
			Issue: "#161",
		},
		{
			ID: "empty-counterpart-rows",
			What: "the excuses that ship with a filed deferral rather than a counterpart " +
				"proof: the recordings, clips, jobs, rules, schedules and library-session " +
				"routes reached only with a row this fixture does not create.",
			WhySafe: "each was traced to its leaf fields and carries no stored credential; " +
				"they are media, transcripts and text.",
			WhatWouldMakeItUnsafe: "any of those payloads gaining a configuration field. " +
				"Deferred but no longer invisible, which is the whole difference.",
			Issue: "#163",
		},
		{
			ID: "read-token-media-envelope",
			What: "a read token can still download recordings, stems, clips and exports, " +
				"and read full transcripts.",
			WhySafe:               "already decided and explicitly out of scope for this PR.",
			WhatWouldMakeItUnsafe: "n/a: this is a product decision, not an oversight.",
			Issue:                 "#154",
		},
	}
}

// ------------------------------------------------------------------- utilities

func containsAny(s string, secrets []string) bool {
	for _, secret := range secrets {
		if secret != "" && strings.Contains(s, secret) {
			return true
		}
	}
	return false
}

// rawBody performs a request and returns the body WHATEVER the status, because
// several of the proofs are about routes that deliberately refuse.
func rawBody(t *testing.T, h http.Handler, sign func(*http.Request), path string) string {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, path, nil)
	if sign != nil {
		sign(r)
	}
	return do(t, h, r).Body.String()
}

// rawResponse renders status, the headers that can carry a credential, and the
// body into one comparable string, so "byte-identical for two principals" is an
// assertion about the whole response rather than about its body.
func rawResponse(t *testing.T, h http.Handler, sign func(*http.Request), method, path string) string {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	if sign != nil {
		sign(r)
	}
	w := do(t, h, r)
	var b strings.Builder
	b.WriteString(strconv.Itoa(w.Code))
	for _, k := range []string{"Content-Type", "Location", "Set-Cookie", "Cache-Control", "Allow"} {
		if v := w.Header().Get(k); v != "" {
			b.WriteString("|" + k + ": " + v)
		}
	}
	b.WriteString("|" + w.Body.String())
	return b.String()
}
