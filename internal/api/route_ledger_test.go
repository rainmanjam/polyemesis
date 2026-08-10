package api

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
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
	ExcusedCeiling int `json:"excusedCeiling"`
	// CounterpartlessExcusedCeiling replaces the old deferredCeiling. The old
	// one counted excuses that named a filed ISSUE instead of a proof, which
	// stopped meaning anything the moment an issue number stopped being a
	// discharge. This counts excuses that discharge on a driven Want alone --
	// real, honest, and still weaker than a counterpart that reads bytes.
	CounterpartlessExcusedCeiling int `json:"counterpartlessExcusedCeiling"`
	// DifferentialFloor may only RISE. It is the exact mirror of every ceiling
	// in this file: min() for a thing that must not grow, max() for a thing
	// that must not shrink. Blank a planted credential and a differential route
	// becomes invariant, the count falls below this floor, and the suite fails
	// by name. That is #165's decisive property.
	DifferentialFloor int `json:"differentialFloor"`
	// SentinelWitnessFloor is differentialFloor at the granularity that actually
	// matters, and it is the answer to "what makes the per-path evidence
	// load-bearing rather than decorative". It counts (path, sentinel) pairs --
	// every planted credential witnessed in a high-privilege body, per route --
	// so a route that carried four and starts carrying three FAILS, even though
	// it is still a differential and the route-level floor never moves.
	// max()-clamped on regeneration, exactly like DifferentialFloor, which is
	// what stops `-update-coverage` from shrinking the committed evidence to fit
	// a regression.
	SentinelWitnessFloor  int             `json:"sentinelWitnessFloor"`
	UnstableCeiling       int             `json:"unstableCeiling"`
	InertCeiling          int             `json:"inertCeiling"`
	VarianceExemptCeiling int             `json:"varianceExemptCeiling"`
	Totals                coverageTotals  `json:"totals"`
	Partition             partitionTotals `json:"partition"`
	Routes                []coverageRoute `json:"routes"`
	SweepVerdicts         []sweepVerdict  `json:"sweepVerdicts"`
	Excuses               []coverageExcus `json:"excuses"`
	Shapes                []coverageShape `json:"shapes"`
	Deferred              []coverageDefer `json:"deferred"`
	// CitedIssues is every issue number any excuse or deferral names. It is
	// HAND-MAINTAINED in the direction that adds, and regenerable only in the
	// direction that removes: writeLedger intersects the live citations with the
	// committed ones, so `-update-coverage` can delete a citation that no longer
	// exists and can never introduce one.
	//
	// That asymmetry is the fix for a real hole. The list used to be regenerated
	// wholesale, so a fabricated citation -- #99999, naming an issue nobody ever
	// filed -- failed once, and then `-update-coverage` wrote it into the
	// committed list and the next run passed. Same shape as the ceiling
	// laundering this file already fixed once: the write happened before the
	// check. Now the write cannot bless a citation, and the check runs first
	// anyway.
	//
	// It remains a FORM check: it catches a typo and it makes adding a citation
	// a reviewable artifact edit. It cannot tell you the issue exists or says
	// what the excuse claims. The liveness half -- "does a reachable commit
	// announce closing it" -- is TestNoLedgerCitationNamesAnIssueACommitClosed,
	// and it runs git.
	CitedIssues []string `json:"citedIssues"`
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

// routeWant is a DRIVEN premise, and it is the whole of #164 and #166.
//
// THE CLOSED VOCABULARY, and the single point at which this file can be
// re-hollowed. Every field below is something the harness OBSERVES by issuing a
// request. There is deliberately no premiseAlways(true), no Skip, no
// "unverifiable" constructor and no escape hatch of any kind: the moment one
// exists, every excuse in the registry can be discharged by naming it, and the
// registry is a document again. If a future route genuinely cannot be driven,
// the answer is a counterpart proof that reads its bytes some other way -- not a
// new member of this vocabulary. Adding one is the re-hollowing, and this
// comment is here so that whoever adds it has to delete this paragraph first.
type routeWant struct {
	// As is the principal the status is claimed for: "read" or "anon".
	As string `json:"as"`
	// Status is the HTTP status that principal must receive. Zero means no
	// status claim, and is legal ONLY alongside AnonMatchesRead, so that a Want
	// always asserts at least one thing that can fail.
	Status int `json:"status"`
	// AnonMatchesRead requires the anonymous and read-scoped responses to be
	// byte-identical -- status, credential-bearing headers and body. It is the
	// predicate that makes a 2xx body legal under an excuse: a body a stranger
	// receives verbatim cannot be disclosing anything to a read scope.
	AnonMatchesRead bool `json:"anonMatchesRead,omitempty"`
}

type coverageExcus struct {
	Route string `json:"route"`
	// Scope distinguishes an excused ROUTE from an excused shape or an excused
	// test-side restatement, so the registry can carry all three.
	Scope string `json:"scope"`
	// Why is PROSE, and NOTHING READS IT. That is the point of #164: the old
	// Reason field sat next to a DeferredIssue string that nothing verified, and
	// between them they discharged fifteen excuses on writing. Deleting every
	// Why in this file changes no verdict -- there is a test that says so.
	Why string `json:"why"`
	// Want is the driven premise. An excuse is legal iff it carries one of
	// these or a Counterpart; there is no field left that holds prose alone.
	Want *routeWant `json:"want,omitempty"`
	// PerMethod overrides Want for ONE method of an "ANY" entry, which stands
	// for every method chi registered on a mount.
	//
	// It exists because driving all ten methods of each ANY entry, rather than
	// collapsing them to GET, found one pair whose real status is not the
	// entry's. An override is NOT a waiver: it is a routeWant, of the same
	// closed vocabulary, driven the same way, and it fails the day that pair
	// changes. What it cannot do is excuse a pair from being driven -- there is
	// no value of this map that turns a request off.
	PerMethod map[string]*routeWant `json:"perMethod,omitempty"`
	// Counterpart names a proof in counterpartProofs. Discharged by RUNNING it,
	// never by the name existing.
	Counterpart string `json:"counterpart"`
	// Issue is a citation for a READER, never a discharge. It is checked two
	// ways -- form, against the committed citedIssues list, and liveness,
	// against a git scan for a reachable commit announcing it closed -- and it
	// discharges nothing either way. The previous design let this string be the
	// entire discharge for fifteen excuses, four of which cited #154, which
	// commit ae8df24 announces closing.
	Issue string `json:"issue,omitempty"`
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
		Why: "the fixture the value sweep runs against starts no destination child, so " +
			"this route answers 404 there. It is NOT excused from inspection: a separate " +
			"fixture spawns a real child and drives it.",
		Want:        &routeWant{As: "read", Status: http.StatusNotFound},
		Counterpart: "runningDestinationLogs",
	},
	"GET /api/v1/ws": {
		Why: "a WebSocket upgrade, so it emits frames rather than a response body and " +
			"the body sweep cannot read it at all. A plain GET without the upgrade " +
			"headers is a 400, which is what the Want below observes.",
		Want:        &routeWant{As: "read", Status: http.StatusBadRequest},
		Counterpart: "websocketFrames",
	},
	"GET /api/v1/playout/public": {
		Why: "gated by authorizePlayout; a read token receives exactly what a stranger " +
			"does. The old text said \"no body at all\", which is false when driven -- " +
			"there is a fifty-byte error body -- so the claim that survives is the one " +
			"that matters and can fail: byte-identical to anonymous.",
		Want: &routeWant{As: "read", Status: http.StatusUnauthorized, AnonMatchesRead: true},
		// The counterpart is what makes this more than a 401: it drives the
		// admin's /playout, which shows the watch token in the clear, and
		// asserts the read principal's bytes carry none of it.
		Counterpart: "playoutPublicView",
	},

	// ---- denied to read tokens outright; the 403 is DRIVEN, not asserted from
	// the middleware's source.
	"GET /api/v1/destinations/{id}/expert":          denied(),
	"GET /api/v1/clipper/recordings/{id}/keyframes": denied(),
	"GET /api/v1/platforms/accounts/{id}/stats":     denied(),
	"GET /api/v1/metadata/broadcast-window":         denied(),

	// ---- session-only: no bearer of either scope reaches these. 403, driven.
	"GET /api/v1/auth/tokens":               sessionOnly(),
	"GET /hls/*":                            sessionOnly(),
	"HEAD /hls/*":                           sessionOnly(),
	"GET /api/v1/oauth/{platform}/start":    sessionOnly(),
	"GET /api/v1/oauth/{platform}/callback": sessionOnly(),

	// ---- unauthenticated by design. GET /health and GET /setup used to live
	// here on the strength of "carries nothing stored"; both answer a read token
	// 200 with a body, so under the new rule they are SWEPT, in leakRoutes().
	"GET /api/v1/tls/ca": {
		Why: "unauthenticated; the PUBLIC half of the local CA. This fixture runs TLS " +
			"off, so there is no CA to serve and the route answers 404 to everyone.",
		Want: &routeWant{As: "read", Status: http.StatusNotFound, AnonMatchesRead: true},
	},
	// r.HandleFunc, so chi registers it for EVERY method. One entry covers them
	// all rather than eleven copies of the same sentence.
	"ANY /api/v1/chat/kick/{secret}": {
		Why: "unauthenticated by necessity: the path segment IS the credential and a " +
			"mismatch is a bare 404, for every method chi registered",
		Want: &routeWant{As: "read", Status: http.StatusNotFound, AnonMatchesRead: true},
	},

	// ---- 503 without the subsystem wired. THREE of the four entries that used
	// to sit here stated a premise that is false when driven: /jobs/overview
	// answers 200 with 2759 bytes and /jobs/policy answers 200 with 2375, to a
	// read token, on this exact fixture. Both are now SWEPT. /jobs and
	// /jobs/{id} really are 503, and now say so in a form that fails if they
	// stop being.
	"GET /api/v1/jobs": notWired(),
	// Re-declared. It used to carry needsRow("#163") -- "reached only with a row
	// this fixture does not create" -- which is the wrong reason: the row is
	// irrelevant, the queue is not wired, and the route never reaches its
	// handler at all.
	"GET /api/v1/jobs/{id}": notWired(),

	// ---- the streaming media origin and the player SPA.
	"GET /playout/*": {
		Why: "the public media origin. A STREAMING response -- an HLS manifest and MPEG-TS " +
			"segments -- which is the shape that escaped the sweep before, since a body " +
			"scan reads none of it. Gated per request by authorizePlayout.",
		Want:        &routeWant{As: "read", Status: http.StatusUnauthorized, AnonMatchesRead: true},
		Counterpart: "playoutManifestBytes",
	},
	"ANY /watch":     spa(),
	"ANY /watch/*":   spa(),
	"ANY /playout/*": publicOrigin(),

	// Unauthenticated by necessity: these are how a caller BECOMES a principal,
	// or how a fresh install acquires one. Driven with an empty body, so the
	// observable premise is the 400 both a stranger and a read token receive --
	// which is the assertion that fails the day either one starts being
	// answered differently.
	"POST /api/v1/auth/login": {
		Why: "unauthenticated by necessity: it is how a session is obtained. Throttled; " +
			"see login_throttle_test.go",
		Want: &routeWant{As: "read", Status: http.StatusBadRequest, AnonMatchesRead: true},
	},
	"POST /api/v1/setup": {
		Why:  "unauthenticated; refuses once an admin exists",
		Want: &routeWant{As: "read", Status: http.StatusBadRequest, AnonMatchesRead: true},
	},
	"GET /api/v1/playout/poster.jpg": {
		Why: "a JPEG rendered from a segment, and this fixture has no segment. Gated by " +
			"authorizePlayout; at the wire an allowed poster with nothing to render and " +
			"a denied one are both 404, which is why the identity with anonymous is the " +
			"claim rather than the bytes.",
		Want: &routeWant{As: "read", Status: http.StatusNotFound, AnonMatchesRead: true},
	},

	// ---- reached only with a row this fixture does not create. The prose used
	// to be the whole discharge. Now each one DRIVES the 404 it claims: if any
	// of these ever starts answering a read token with a body, the drive fails
	// naming the route and the observed status, and the fix is leakRoutes().
	"GET /api/v1/recordings/{id}/download":             denied(),
	"GET /api/v1/recordings/stems/{name}/download":     denied(),
	"GET /api/v1/clips/{name}/download":                denied(),
	"GET /api/v1/clipper/recordings/{id}":              needsRow(),
	"GET /api/v1/clipper/recordings/{id}/transcript":   denied(),
	"GET /api/v1/clipper/jobs/{id}/download":           denied(),
	"GET /api/v1/library/recordings/{id}/transcript":   denied(),
	"GET /api/v1/library/recordings/{id}/media/{file}": denied(),
	"GET /api/v1/library/recordings/{id}":              needsRow(),
	"GET /api/v1/library/sessions/{id}":                needsRow(),
	"GET /api/v1/metadata/push/{id}": {
		Why: "reached only with a metadata push row this fixture does not create; the " +
			"404 body is the handler's own \"no such metadata push\", not the router's",
		Want: &routeWant{As: "read", Status: http.StatusNotFound},
	},
	"GET /api/v1/alerts/rules/{id}": needsRow(),
	"GET /api/v1/hooks/{id}":        needsRow(),
	"GET /api/v1/schedules/{id}":    needsRow(),
	"GET /api/v1/renditions/{id}":   needsRow(),
	"GET /api/v1/library/search":    denied(),
}

// The constructors below no longer take a test NAME or an ISSUE STRING. Both
// used to be the argument, and both were the free pass: the name was never
// resolved and the issue number was never checked. What they take now is
// nothing, because the discharge is the driven Want each one carries.

func denied() coverageExcus {
	return coverageExcus{
		Why: "denied to a read token outright by requireScope; there is no body to " +
			"assert about because the handler never runs",
		Want: &routeWant{As: "read", Status: http.StatusForbidden},
	}
}
func sessionOnly() coverageExcus {
	return coverageExcus{
		Why:  "session-only; no bearer of either scope reaches it",
		Want: &routeWant{As: "read", Status: http.StatusForbidden},
	}
}
func notWired() coverageExcus {
	return coverageExcus{
		Why:  "503 without a job queue wired; the handler never reaches storage",
		Want: &routeWant{As: "read", Status: http.StatusServiceUnavailable},
	}
}
func publicOrigin() coverageExcus {
	return coverageExcus{
		Why: "the public media origin under every method chi registered on the mount. " +
			"Gated per request by authorizePlayout; the GET row carries the streaming " +
			"counterpart and the rest reach the same gate.",
		Want:        &routeWant{As: "read", Status: http.StatusUnauthorized, AnonMatchesRead: true},
		Counterpart: "playoutManifestBytes",
		// FOUND BY DRIVING ALL TEN METHODS, which is the whole of the change
		// that expanded ANY entries into pairs. Nine of them reach
		// authorizePlayout and answer 401. OPTIONS does not: it is answered 204
		// with no body ABOVE the gate, so the sentence "the rest reach the same
		// gate" was false for one pair in ten and no request had ever been
		// issued that could say so.
		//
		// Recorded as what it is rather than argued away. It is the subject of
		// #170, which is out of scope this round and is being fixed on another
		// branch; the honest thing here is a Want that states the status this
		// pair really returns, driven exactly like the other nine. When #170's
		// fix lands, this override FAILS with the new status and whoever rebases
		// has to look at it -- which is the behaviour a correct-but-stale
		// premise should have.
		PerMethod: map[string]*routeWant{
			http.MethodOptions: {As: "read", Status: http.StatusNoContent, AnonMatchesRead: true},
		},
	}
}

func spa() coverageExcus {
	return coverageExcus{
		Why: "the player SPA bundle: a build-time embed.FS, byte-identical for every " +
			"principal. No STATUS is claimed, deliberately: it is 404 with the UI " +
			"unbuilt and 200 with it built, and pinning either would make this " +
			"assertion a statement about the build rather than about disclosure. The " +
			"identity with an anonymous stranger is the claim, and it holds either way.",
		Want:        &routeWant{As: "read", AnonMatchesRead: true},
		Counterpart: "notFoundSurfaceIsPrincipalIndependent",
	}
}
func needsRow() coverageExcus {
	return coverageExcus{
		Why: "reached only with a row this fixture does not create, so it answers 404. " +
			"Traced to leaf fields and carrying no stored credential (media, " +
			"transcripts and text). The FIXTURE is what is deferred, not the guard: " +
			"the 404 is driven, and a body appearing here fails.",
		Want:  &routeWant{As: "read", Status: http.StatusNotFound},
		Issue: "#163",
	}
}

// sweptCounterparts are proofs that run for a route that is ALSO value-swept.
//
// GET /hooks/{id}/deliveries is the case. It used to be excused with a
// counterpart; driven, it answers a read token 200 with "[]", so under the new
// rule it is swept and its excuse is gone. The proof is not: it plants a real
// hook through the real route and uses the server-minted signing secret as the
// positive control, which the empty-list sweep cannot do. Registered here so
// runCounterpartProofs still counts it as referenced -- a proof no excuse names
// is decoration, and this is the honest way to say "named by a sweep instead".
var sweptCounterparts = map[string]string{
	"GET /api/v1/hooks/{id}/deliveries": "hookDeliveries",
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

// ------------------------------------------------------- driving the excuses

// excuseObservation is what actually happened when an excuse's premise was
// driven, as opposed to what its prose said would happen.
type excuseObservation struct {
	Key        string
	Method     string
	ReadStatus int
	ReadBody   string
	AnonBody   string
	Identical  bool
}

// driveExcuse issues the excuse's claim as a real request, for ONE method.
//
// It used to collapse "ANY" to GET on the grounds that "the per-pair rules below
// cover the rest of the methods", and the code did no such thing:
// classifyRoutes' `case excused:` short-circuits before the readScopeIsRefused
// branch, so the other methods were never driven anywhere. Four ANY entries
// cover 40 method+pattern pairs -- /api/v1/chat/kick/{secret}, /watch, /watch/*
// and /playout/*, ten methods each, 70% of everything this registry excuses --
// and exactly four requests were issued. Thirty-six pairs discharged on an
// observation of a DIFFERENT pair, which is "the counterpart names a green test"
// in a new costume.
//
// The caller now expands each ANY key into every method the router actually
// registers for that pattern and calls this once per pair.
func driveExcuse(t *testing.T, h http.Handler, read, key, method string) excuseObservation {
	t.Helper()
	_, pattern, _ := strings.Cut(key, " ")
	path := concretePath(pattern)
	rd := rawResponse(t, h, bearer(read), method, path)
	anon := rawResponse(t, h, nil, method, path)
	r := jsonRequest(t, method, path, nil)
	bearer(read)(r)
	w := do(t, h, r)
	return excuseObservation{
		Key: key, Method: method,
		ReadStatus: w.Code, ReadBody: w.Body.String(),
		AnonBody: anon, Identical: rd == anon,
	}
}

// assertExcusesDischargeByRunning is #164, #166 and half of #163 in one loop.
//
// Every mechanical failure it can produce, and each of them is a thing that
// silently passed before:
//
//	(a) an excuse carrying neither a driven Want nor a Counterpart. Prose-only
//	    entries are not merely rejected, they are UNREPRESENTABLE: no field
//	    holds them any more.
//	(b) an observed status that is not the declared one -- fail naming the
//	    pattern, the want, the got and the byte count.
//	(c) a read principal receiving 2xx WITH A BODY under any excuse. The excuse
//	    is void; the route belongs in leakRoutes(). The only survivable form is
//	    a body an anonymous stranger receives byte-identically, and that is
//	    itself driven rather than claimed.
//	(d) a Counterpart naming a proof that is not in the registry. (Kept from the
//	    previous design, which was right about this.)
//	(e) an ANY entry whose OTHER methods were never driven. Every method+pattern
//	    pair an excuse covers is now driven separately; see driveExcuse.
func assertExcusesDischargeByRunning(t *testing.T, h http.Handler, s *Server, read string) {
	t.Helper()
	_, served := enumerateRoutes(t, s)
	// The methods the router actually registers for each pattern, so an ANY
	// entry is driven over the real set rather than over a guess.
	methodsOf := map[string][]string{}
	for key := range served {
		method, pattern, _ := strings.Cut(key, " ")
		methodsOf[pattern] = append(methodsOf[pattern], method)
	}
	for p := range methodsOf {
		sort.Strings(methodsOf[p])
	}

	keys := make([]string, 0, len(excusedRoutes))
	for k := range excusedRoutes {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		ex := excusedRoutes[key]
		if ex.Want == nil && ex.Counterpart == "" {
			t.Errorf("the excuse for %s discharges on NOTHING. Every route this sweep does "+
				"not read must carry a driven Want or a counterpart proof that runs. A "+
				"prose reason and an issue number are not discharges -- that single rule "+
				"is #164, and fifteen excuses were living on it.", key)
			continue
		}
		if ex.Counterpart != "" {
			if _, ok := counterpartProofs[ex.Counterpart]; !ok {
				t.Errorf("the excuse for %s names the counterpart %q, which is not in "+
					"counterpartProofs.", key, ex.Counterpart)
			}
		}
		if ex.Want == nil {
			continue
		}

		// THE PAIRS THIS EXCUSE ACTUALLY COVERS. An "ANY" key is one entry
		// standing for every method chi registered on the mount, and each of
		// them is a separate claim about a separate response.
		method, pattern, _ := strings.Cut(key, " ")
		methods := []string{method}
		if method == "ANY" {
			// A method with its OWN explicit entry is driven under that key with
			// that Want. GET /playout/* is the case: it carries the streaming
			// counterpart, and ANY /playout/* covers the other nine.
			methods = slices.DeleteFunc(slices.Clone(methodsOf[pattern]), func(m string) bool {
				_, own := excusedRoutes[m+" "+pattern]
				return own
			})
		}
		if len(methods) == 0 {
			t.Errorf("the excuse for %s drives NOTHING: the router serves no method for that "+
				"pattern that this entry covers. An excuse whose loop body never executes is "+
				"the vacuity this file exists to catch, one level up.", key)
			continue
		}
		for m := range ex.PerMethod {
			if !slices.Contains(methods, m) {
				t.Errorf("the excuse for %s carries a per-method Want for %s, which is not "+
					"among the methods the router serves for that pattern (%v). A dead "+
					"override is an assertion that cannot fail.", key, m, methods)
			}
		}

		for _, m := range methods {
			pair := m + " " + pattern
			want := ex.Want
			if pm, ok := ex.PerMethod[m]; ok && pm != nil {
				want = pm
			}
			if want.Status == 0 && !want.AnonMatchesRead {
				t.Errorf("the excuse for %s carries a Want that claims nothing: no status and "+
					"no identity predicate. A Want must assert at least one thing that can "+
					"fail, or it is the prose it replaced with a struct around it.", pair)
				continue
			}
			obs := driveExcuse(t, h, read, key, m)
			if want.Status != 0 && obs.ReadStatus != want.Status {
				t.Errorf("EXCUSE PREMISE FALSE. %s: the excuse claims a %s principal receives "+
					"%d; DRIVEN as %s %s it returns %d with %d bytes.\nbody: %s\n"+
					"Either the premise was never true or the route changed. If the route now "+
					"answers with a body, delete the excuse and add the path to leakRoutes(); "+
					"otherwise correct the Want to the status the router actually imposes. If "+
					"only this METHOD differs, give the entry a perMethod Want for it -- one "+
					"that states the status this pair really returns, which is still a driven "+
					"claim and is not a waiver.",
					pair, want.As, want.Status, obs.Method, concretePath(pattern),
					obs.ReadStatus, len(obs.ReadBody), truncateForFailure(obs.ReadBody))
			}
			if want.AnonMatchesRead && !obs.Identical {
				t.Errorf("EXCUSE PREMISE FALSE. %s: the excuse claims an anonymous stranger and "+
					"a read-scoped token receive byte-identical responses, and they do not. "+
					"That predicate is the ONLY thing standing between this route and the "+
					"value sweep, so it is not decorative.\nread: %s\nanon: %s",
					pair, truncateForFailure(obs.ReadBody), truncateForFailure(obs.AnonBody))
			}
			// (c) THE RULE THAT CANNOT BE EXCUSED AWAY.
			if obs.ReadStatus/100 == 2 && strings.TrimSpace(obs.ReadBody) != "" && !want.AnonMatchesRead {
				t.Errorf("EXCUSED ROUTE ANSWERS A READ TOKEN. %s returned %d with %d bytes to a "+
					"read-scoped principal, and its excuse carries no anon-identity predicate. "+
					"A route that answers the very principal this sweep is about, with a body, "+
					"is not excusable on any grounds: add %q to leakRoutes() so its bytes are "+
					"scanned.\nbody: %s",
					pair, obs.ReadStatus, len(obs.ReadBody), concretePath(pattern),
					truncateForFailure(obs.ReadBody))
			}
			// A 2xx body that IS byte-identical to a stranger's still gets scanned.
			// Byte-identity says nothing is disclosed to one principal and not
			// another; it says nothing at all about a credential disclosed to
			// everyone. That residue is #168's, and this is the cheap half of it.
			if obs.ReadStatus/100 == 2 {
				for _, secret := range allSentinels() {
					if strings.Contains(obs.ReadBody, secret) {
						t.Errorf("%s handed a read-scoped token %s in an EXCUSED response.",
							pair, secret)
					}
				}
			}
		}
	}
}

// TestDeletingEveryProseReasonChangesNoVerdict is #164's acceptance criterion,
// executed rather than asserted.
//
// It blanks every Why in the registry, re-runs the whole classification, and
// requires the verdicts to be identical. If any assertion in this file ever
// starts reading the prose again -- the previous design keyed "denied" off
// strings.HasPrefix(ex.Reason, "denied to read tokens"), which is exactly that
// mistake -- this fails.
func TestDeletingEveryProseReasonChangesNoVerdict(t *testing.T) {
	h, _, sign := plantedServer(t)
	s := serverUnderTest(t, h)
	ledgerSessions[h] = sign

	before, _ := classifyRoutes(t, h, s)
	saved := map[string]coverageExcus{}
	for k, ex := range excusedRoutes {
		saved[k] = ex
		ex.Why = ""
		excusedRoutes[k] = ex
	}
	t.Cleanup(func() {
		for k, ex := range saved {
			excusedRoutes[k] = ex
		}
	})
	after, _ := classifyRoutes(t, h, s)

	if len(before) != len(after) {
		t.Fatalf("blanking the prose changed the ROUTE COUNT: %d then %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("blanking every prose reason changed a verdict: %s %s was %q and is "+
				"now %q. Some assertion in this file is reading the prose, which is the "+
				"whole of #164.", before[i].Method, before[i].Pattern,
				before[i].Coverage, after[i].Coverage)
		}
	}
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
			"download filenames; media names only, no stored credential. RE-POINTED from " +
				"#154, which commit ae8df24 announces closing: what remains is the general " +
				"fact that response HEADERS are not inspected by any sweep. Deferred: #168"},
		{"streaming-media", true, true, "playoutManifestBytes",
			"the HLS manifest and its segments -- the shape a body sweep reads none of, " +
				"and the one that escaped the previous audit"},
		{"file-download", true, false, "",
			"recordings, stems, clips and exports. #154 decided this and is CLOSED by " +
				"ae8df24: every download route now answers a read token 403, which the " +
				"excuse registry drives. What is still uninspected is the shape itself " +
				"for the principals entitled to it. Deferred: #168"},
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
		// THE BODY WAS WRONG, and the sweep did not notice. It carried a
		// "source" key that handleCompileRouting's request struct does not
		// have, decodeJSON is strict, and the request 400ed before the handler
		// ever ran -- so the ledger recorded this pair as swept while nothing
		// had ever reached the code that builds the response. The status is now
		// REQUIRED to be 2xx, which is what makes the body's correctness an
		// assertion rather than a hope.
		{http.MethodPost, "/api/v1/routing/compile", "/api/v1/routing/compile",
			map[string]any{"profile": map[string]any{
				"mode":   "simple",
				"tracks": []any{map[string]any{"track": 0, "enabled": true, "gain": 1}},
			}}},
		// POST /version/check used to be excused with DeferredIssue "#157" on
		// the grounds that value-sweeping it would put the network in the test
		// suite. Driven, it answers a read token 200 with a body -- so under
		// the discharge rule it is not excusable, and the network objection is
		// answered by the stub seam the version tests already use rather than
		// by an issue number. TestReadScopeWriteRoutesCarryNoCredential
		// installs stubReleaseFeed before driving it; nothing here ever reaches
		// api.github.com.
		{http.MethodPost, "/api/v1/version/check", "/api/v1/version/check", map[string]any{}},
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
	// The release feed, pointed at a local stub. POST /version/check is on this
	// sweep now, and the ONE thing that must never happen is this suite
	// reaching api.github.com: a sweep whose result depends on the network is a
	// sweep that goes green on a timeout.
	stubReleaseFeed(t, releaseJSON("v0.0.1", "https://example.test/none"))

	for _, c := range readScopeWriteSweep() {
		r := jsonRequest(t, c.method, c.path, c.body)
		bearer(read)(r)
		w := do(t, h, r)
		if w.Code == http.StatusForbidden {
			t.Errorf("%s %s returned 403 to a read token, but readScopeWritePatterns "+
				"lists it. Either the list or this sweep is out of date.", c.method, c.path)
			continue
		}
		// A 2xx IS REQUIRED. The compile route sat on this list for a whole
		// round returning 400 to a malformed body, and a sweep that shrugs at
		// the status is scanning an error message for credentials rather than
		// the response the handler builds.
		if w.Code/100 != 2 {
			t.Errorf("%s %s returned %d with %d bytes, so this sweep never reached the "+
				"handler and has been scanning an error message. Fix the request body "+
				"in readScopeWriteSweep.\nbody: %s",
				c.method, c.path, w.Code, w.Body.Len(), truncateForFailure(w.Body.String()))
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

// enumerateRoutes walks the router and returns every (method, pattern) it
// serves, plus the same set as a lookup.
//
// No GET filter and no TrimSuffix narrowing: the previous sweep dropped 51
// non-GET pairs and rewrote the patterns it did keep, so the set it reconciled
// against was not the set the router serves. Extracted from classifyRoutes so
// that the excuse drive can reach the same set -- an "ANY" excuse covers ten
// method+pattern pairs and has to DRIVE ten, not one.
func enumerateRoutes(t *testing.T, s *Server) ([]coverageRoute, map[string]bool) {
	t.Helper()
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
	return enumerated, seen
}

// classifyRoutes enumerates the router and computes one verdict per
// (method, pattern). It reads no prose: every branch below is either a set
// membership or a request that was actually issued.
func classifyRoutes(t *testing.T, h http.Handler, s *Server) ([]coverageRoute, coverageTotals) {
	t.Helper()

	// 1. ENUMERATE. Every method, every pattern.
	enumerated, seen := enumerateRoutes(t, s)

	// 2. CLASSIFY.
	swept := map[string]bool{}
	for _, path := range leakRoutes() {
		swept["GET "+patternOf(t, s, path)] = true
	}
	// The NON-GET routes a read scope may still reach. They are swept for
	// values, not excused: readScopeWritePatterns lets a read token POST to them
	// and they return a body, so the method rule that covers every other non-GET
	// does not cover these.
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
			// DENIED is now a DRIVEN property -- the excuse's Want says 403 and
			// the drive above observed one -- rather than a prefix match on the
			// prose. The previous line here was
			// strings.HasPrefix(ex.Reason, "denied to read tokens"), which is
			// the exact shape #164 is about: a verdict computed from a string
			// somebody typed.
			if ex.Want != nil && ex.Want.Status == http.StatusForbidden {
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
				"with a DRIVEN Want or a counterpart proof. A new route fails this test on "+
				"the day it lands, which is when its author still has the context to "+
				"classify it.", r.Method, r.Pattern)
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
	return enumerated, totals
}

// TestLedgerPreflight is the whole ledger, and TestMain runs it BEFORE whatever
// the caller asked for -- see main_test.go. That is #161's jurisdiction problem
// solved for this package: `go test ./internal/api -run TestSomethingElse` no
// longer leaves every one of these obligations unchecked.
//
// Every failure message below names the route, the observed status, the byte
// count and the exact edit that fixes it. A preflight with bad messages is a
// preflight that gets deleted, and then all five issues return at once.
func TestLedgerPreflight(t *testing.T) {
	// ALREADY RUN, IN THIS PROCESS. TestMain forces this test through a first
	// m.Run with the caller's -run, -skip and -count set aside; the second pass
	// restores them and would otherwise re-drive 55 paths x 3 principals x 3
	// samples for a verdict already computed. Measured at ~14s of a ~42s
	// package, which is the entire reason the unfiltered CI invocation used to
	// cost +9.8% rather than +2%.
	//
	// This is NOT a skip and is deliberately not one: a skip is a test that did
	// not run, and this one ran, in this process, minutes ago, with every
	// assertion below live. If TestMain is deleted the flag stays false and the
	// full body runs here, so the failure direction is duplicated work rather
	// than a hole.
	if ledgerPreflightDone {
		t.Logf("the route coverage preflight already ran in this process, in TestMain's " +
			"first pass, with -run/-skip/-count forced aside. Recomputing it here cannot " +
			"reach a different verdict; see main_test.go.")
		return
	}

	// The liveness marker. `make preflight-guard` asserts that this line still
	// appears under `-run XXXNoSuchTest`, under `-skip TestLedgerPreflight` and
	// under `-count=0`, so the preflight's own existence is proven by RUNNING
	// something that fails if it stopped -- which is the rule this whole PR is
	// about, applied to the guard itself.
	fmt.Println(ledgerPreflightMarker)

	h, _, sign := plantedServer(t)
	ledgerSessions[h] = sign
	s := serverUnderTest(t, h)
	read := createScopedToken(t, h, sign, "preflight", db.ScopeRead)

	// 1. THE DISCHARGE RULE. #164, #166, and the excuse half of #163. Driven for
	// EVERY method+pattern pair the excuse covers, not one pair per entry.
	assertExcusesDischargeByRunning(t, h, s, read)

	// 2. ENUMERATE AND CLASSIFY.
	enumerated, totals := classifyRoutes(t, h, s)
	assertSweptCounterpartsNameSweptRoutes(t, enumerated)

	// 3. THE PARTITION. #165: every swept path gets a computed verdict, and
	// "swept" stops being one word covering two different claims.
	verdicts := sweepCensus(t, h, sign)
	part := countPartition(verdicts)
	assertEverySentinelIsWitnessed(t, h, sign)

	// 4. EQUALITY against the artifact.
	want := readLedger(t)

	// CITATIONS, FORM HALF -- and it runs HERE, before anything is written.
	// It used to run at the very end, after writeLedger, which made it the
	// ceiling-laundering bug in a second costume: a fabricated citation failed
	// once ("#99999 is not in citedIssues"), and `-update-coverage` regenerated
	// the list around it and turned the run green. The order alone is not the
	// whole fix -- writeLedger now intersects rather than regenerates the list,
	// so a citation can only be REMOVED by a regeneration, never blessed by one
	// -- but a check that runs after the write is worth nothing on the very run
	// that matters.
	assertCitationsAreWellFormed(t, want)

	if *updateCoverage {
		writeLedger(t, want, enumerated, totals, verdicts, part)
	} else {
		assertRouteSetsEqual(t, want.Routes, enumerated)
		assertSweepVerdictsEqual(t, want.SweepVerdicts, verdicts)
		assertPartitionTotalsEqual(t, want.Partition, part)
		assertCoverageTotalsEqual(t, want.Totals, fillDerivedTotals(totals, enumerated))
		// The note is the other scalar block, and the one a reviewer reads first.
		// A committed note saying "Everything in this ledger is fully covered. No
		// route is excused." passed a full strict run before this line existed --
		// prose asserting the exact opposite of the 57 excused pairs recorded
		// three keys below it.
		if want.Note != ledgerNote {
			t.Errorf("the note in %s is not the one this build writes. It is the first "+
				"thing a reader trusts and nothing was comparing it.\ncommitted: %q\nlive: %q",
				coveragePath, want.Note, ledgerNote)
		}
		assertProseSectionsEcho(t, want)
	}

	// 5. THE RATCHETS. Ceilings clamp DOWN with min() and the floor clamps UP
	// with max(); neither is ever an assignment. The ratchet bug this PR's
	// predecessor shipped was exactly an assignment where a clamp belonged.
	if totals.Excused > want.ExcusedCeiling {
		t.Errorf("%d routes are excused and the committed ceiling is %d. Going UP requires "+
			"editing excusedCeiling in %s by hand. Going down is free -- regenerate and "+
			"the ceiling comes with it.", totals.Excused, want.ExcusedCeiling, coveragePath)
	}
	if n := counterpartlessExcused(); n > want.CounterpartlessExcusedCeiling {
		t.Errorf("%d excuses discharge on a driven Want alone, with no counterpart proof "+
			"reading any bytes, and the committed ceiling is %d. A Want is a real "+
			"assertion and a weaker one than a proof; raising this is a hand edit.",
			n, want.CounterpartlessExcusedCeiling)
	}
	if part.Differential < want.DifferentialFloor {
		t.Errorf("THE POSITIVE CONTROL FELL. %d swept paths carry a planted credential in "+
			"the high-privilege body, and the committed floor is %d. This is #165's "+
			"decisive assertion: blank a fixture credential and the route it covered "+
			"stops being a differential, so the sweep over it becomes a statement about "+
			"an empty body -- which is exactly what "+
			"TestReadScopedTokenCannotReadAPublishToken was. A floor going DOWN is never "+
			"free; it is a hand edit of differentialFloor in %s.",
			part.Differential, want.DifferentialFloor, coveragePath)
	}
	// THE PER-(ROUTE, SENTINEL) FLOOR, and it is the one number in this file
	// that a regeneration cannot walk backwards.
	//
	// differentialFloor counts ROUTES that carry at least one planted credential
	// in the high-privilege body, and that granularity was exploitable: making
	// handleListDestinations stop emitting extraInputArgs/extraOutputArgs to
	// every principal removed sentinelExpertArgs from GET /api/v1/destinations,
	// left three other sentinels on the route, kept the class at "differential",
	// kept the floor at 8, and passed the entire package. The committed verdict
	// still claimed the argv redaction on that route was under test. It was not.
	//
	// assertSweepVerdictsEqual now compares the per-path sentinel LIST, which
	// fails that mutation by name -- but equality alone is launderable: run
	// -update-coverage and the committed list shrinks to match. This floor is
	// what makes the evidence load-bearing rather than decorative, because
	// writeLedger clamps it with max() exactly like differentialFloor. Lowering
	// it is a hand edit of sentinelWitnessFloor, and the sentence somebody has
	// to write on purpose is "this route stopped being checked for this
	// credential".
	if n := countSentinelWitnesses(verdicts); n < want.SentinelWitnessFloor {
		t.Errorf("A PER-ROUTE CREDENTIAL WITNESS DISAPPEARED. %d (path, sentinel) pairs are "+
			"witnessed in a high-privilege body across the sweep, and the committed floor "+
			"is %d. The route still being a differential is NOT enough: a route that "+
			"carries four planted credentials and starts carrying three has stopped "+
			"proving anything about the fourth, while its class, its floor and its "+
			"absence assertion all stay green. The message above from "+
			"assertSweepVerdictsEqual names which path lost which sentinel.\n"+
			"per-path witnesses: %v",
			n, want.SentinelWitnessFloor, sentinelWitnessesByPath(verdicts))
	}
	if part.Unstable > want.UnstableCeiling {
		t.Errorf("%d swept paths return different bytes to the same principal on two "+
			"consecutive samples, and the committed ceiling is %d.",
			part.Unstable, want.UnstableCeiling)
	}
	// INERT is a ceiling for the same reason unstable is. An inert row is a
	// sweep reading "[]" or "{}": it costs three requests and asserts absence
	// over nothing. Eleven of the 43 invariants are in that state today. The
	// number was computed and committed and nothing compared it, so a route
	// whose list quietly emptied moved into the inert count in silence -- which
	// is a sweep going vacuous by exactly the mechanism this file is named for.
	if part.Inert > want.InertCeiling {
		t.Errorf("%d swept paths return a body that is entirely \"[]\" or \"{}\", so the "+
			"sweep over them scans nothing, and the committed ceiling is %d. A route that "+
			"has just become inert is a route whose fixture stopped producing rows: find "+
			"it in the sweepVerdicts diff (inert: true) and either give it a row or raise "+
			"inertCeiling in %s by hand.", part.Inert, want.InertCeiling, coveragePath)
	}
	if len(nonCredentialVariance) > want.VarianceExemptCeiling {
		t.Errorf("%d (pattern, pointer) pairs are exempted from the variance rule and the "+
			"committed ceiling is %d.", len(nonCredentialVariance), want.VarianceExemptCeiling)
	}

	// 6. STRICT MODE. CI sets POLYEMESIS_LEDGER=strict and the counterpart
	// proofs run from HERE as well, so no single -run filter silences both.
	// They are NOT in the preflight by default: two of them spawn a real FFmpeg
	// stand-in and cost ~15s, and a preflight nobody can afford to run is a
	// preflight somebody deletes. That residual has its own row in
	// deferredWithReasons rather than being implicit.
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

	// 8. CITATIONS ran at step 4, before the write. The liveness half is the git
	// scan in TestNoLedgerCitationNamesAnIssueACommitClosed.
}

const ledgerPreflightMarker = "polyemesis: route-coverage preflight ran"

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
	for _, name := range sweptCounterparts {
		referenced[name] = true
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

// counterpartlessExcused is how many excuses discharge on a driven Want alone,
// with no counterpart proof reading any bytes.
//
// It replaces deferredCount, which counted excuses naming a filed ISSUE. That
// number stopped meaning anything the moment an issue number stopped being a
// discharge -- it was measuring how many entries had a string in a particular
// field. This measures the real remaining weakness: a status code is a genuine
// assertion and a much weaker one than bytes read and scanned.
func counterpartlessExcused() int {
	n := 0
	for _, ex := range excusedRoutes {
		if ex.Counterpart == "" {
			n++
		}
	}
	return n
}

// citedIssues is every issue number the registry and the deferral list name,
// sorted and deduplicated.
func citedIssues() []string {
	set := map[string]bool{}
	for _, ex := range excusedRoutes {
		if ex.Issue != "" {
			set[ex.Issue] = true
		}
	}
	for _, d := range deferredWithReasons() {
		if d.Issue != "" {
			set[d.Issue] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var issueRef = regexp.MustCompile(`^#[0-9]{1,5}$`)

// assertCitationsAreWellFormed is the FORM half of #164, and it is labelled as
// such because it verifies nothing about the issue.
//
// It catches a typo and it makes ADDING a citation a visible artifact diff. It
// cannot tell you the issue exists, is open, or says what the excuse claims it
// says. The liveness half is the git scan below, and even that is incomplete by
// construction -- see its comment.
func assertCitationsAreWellFormed(t *testing.T, want coverageLedger) {
	t.Helper()
	committed := map[string]bool{}
	for _, id := range want.CitedIssues {
		committed[id] = true
	}
	for _, id := range citedIssues() {
		if !issueRef.MatchString(id) {
			t.Errorf("the ledger cites %q, which is not an issue reference. This is a FORM "+
				"check: it catches a typo and nothing else.", id)
		}
		if !committed[id] {
			t.Errorf("the ledger cites %s, which is not in citedIssues in %s. ADD IT TO THE "+
				"ARTIFACT BY HAND. -update-coverage will not do it for you and deliberately "+
				"cannot: it intersects the live citations with the committed ones, so a "+
				"regeneration may only drop a citation, never introduce one.\n"+
				"That asymmetry exists because the previous design regenerated this list "+
				"wholesale, which made it launderable -- a citation naming an issue nobody "+
				"ever filed failed once, and then the very command this message used to "+
				"recommend wrote it into the committed evidence and turned the run green. "+
				"A citation discharges nothing either way; what it must not be is "+
				"self-certifying.", id, coveragePath)
		}
	}
}

// TestNoLedgerCitationNamesAnIssueACommitClosed is the LIVENESS half of #164.
//
// The registry's fifteen prose discharges were bad in two different ways. The
// first was that nothing checked whether the deferral had been done. The second
// was subtler and is what this catches: four excuses cited #154, and commit
// ae8df24 -- reachable from every branch -- says "Closes #154" in its body.
// #171 had discharged eight of those routes into denied() and the excuses still
// pointed at the closed issue, so the ledger was citing, as its justification, a
// decision that had already been carried out.
//
// INCOMPLETE BY CONSTRUCTION, and stated here rather than in a PR description
// nobody reads later: this catches an issue closed by a commit that ANNOUNCES
// it, which is the mechanism that produced the #154 bug. It misses an issue
// closed silently in the web UI. The form check above catches only typos. There
// is no offline oracle for "is this issue open", and putting `gh` in the test
// suite would put the network in it -- which is the objection that kept
// POST /version/check excused for a whole round.
func TestNoLedgerCitationNamesAnIssueACommitClosed(t *testing.T) {
	if _, err := os.Stat(filepath.Join("..", "..", ".git")); err != nil {
		// A source tarball has no history to scan. This is the one place in the
		// ledger that cannot assert, and it says so out loud on every run
		// rather than passing quietly.
		t.Logf("NO GIT HISTORY at ../../.git: the issue-liveness scan cannot run here, so "+
			"the %d citations in the ledger are unchecked in this build. This is a real "+
			"hole and it is printed rather than skipped.", len(citedIssues()))
		return
	}
	out, err := exec.Command("git", "-C", filepath.Join("..", ".."),
		"log", "--format=%H%x1f%s%x1f%b%x1e").Output()
	if err != nil {
		t.Fatalf("scan the history for closing announcements: %v", err)
	}
	closing := regexp.MustCompile(`(?i)\b(close[sd]?|fix(e[sd])?|resolve[sd]?)\s*:?\s*(#[0-9]+)`)
	closedBy := map[string]string{}
	for _, commit := range strings.Split(string(out), "\x1e") {
		parts := strings.SplitN(commit, "\x1f", 3)
		if len(parts) < 3 {
			continue
		}
		sha := strings.TrimSpace(parts[0])
		for _, m := range closing.FindAllStringSubmatch(parts[1]+"\n"+parts[2], -1) {
			if _, seen := closedBy[m[3]]; !seen {
				closedBy[m[3]] = sha
			}
		}
	}
	for _, id := range citedIssues() {
		if sha, ok := closedBy[id]; ok {
			t.Errorf("the ledger cites %s as a live deferral, and commit %s announces "+
				"closing it. A citation is not a discharge and never was -- but citing a "+
				"CLOSED issue means the ledger is offering, as its reason, work that has "+
				"already been done. Re-point the citation or delete it.", id, sha[:12])
		}
	}
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
func writeLedger(t *testing.T, prev coverageLedger, routes []coverageRoute,
	totals coverageTotals, verdicts []sweepVerdict, part partitionTotals) {
	t.Helper()
	totals = fillDerivedTotals(totals, routes)
	shapes := emittedShapes()

	out := coverageLedger{
		Note:          ledgerNote,
		Totals:        totals,
		Partition:     part,
		Routes:        routes,
		SweepVerdicts: verdicts,
		Excuses:       liveExcuses(),
		Shapes:        shapes,
		Deferred:      deferredWithReasons(),
		// INTERSECTION, never a regeneration. A citation may be dropped by
		// regenerating -- the excuse that named it is gone, and carrying a dead
		// number forward helps nobody -- and may only be ADDED by editing this
		// list in the artifact by hand. See the field's comment: regenerating it
		// wholesale is how a fabricated #99999 got laundered into the committed
		// evidence by the very command the failure message told you to run.
		CitedIssues: intersectCitations(citedIssues(), prev.CitedIssues),
	}
	// The ceilings RATCHET DOWN on regeneration and never up. This CLAMPS rather
	// than assigns, and the difference is the whole guarantee.
	//
	// Assigning looked equivalent because the caller asserts the ceiling too --
	// but it writes the file BEFORE that assertion runs, so `-update-coverage`
	// banked the raised number and the next plain run passed against it. The
	// laundering needed no bad intent: fail, do what the message says and
	// regenerate, re-run, green. The evidence was a two-character diff inside
	// 1539 lines of JSON, which is exactly the silent raise the ratchet exists
	// to prevent. A ratchet that does not ratchet is worse than none, because it
	// is cited as though it did.
	//
	// Raising a ceiling is still possible and still allowed -- by editing this
	// file by hand, which is a reviewable act. That is the point.
	out.ExcusedCeiling = min(totals.Excused, prev.ExcusedCeiling)
	out.CounterpartlessExcusedCeiling = min(counterpartlessExcused(), prev.CounterpartlessExcusedCeiling)
	out.UnstableCeiling = min(part.Unstable, prev.UnstableCeiling)
	out.InertCeiling = min(part.Inert, prev.InertCeiling)
	out.VarianceExemptCeiling = min(len(nonCredentialVariance), prev.VarianceExemptCeiling)
	// THE MIRROR. Everything above may only fall; the differential floor may
	// only RISE. max() where the others use min(), for the same reason and in
	// the same direction of safety: the number of routes over which the sweep
	// makes a real differential claim is the one quantity in this file that
	// must never quietly go down. Lowering it is a hand edit, and a hand edit
	// that says "this fixture stopped planting a credential" is a sentence
	// somebody has to write on purpose.
	out.DifferentialFloor = max(part.Differential, prev.DifferentialFloor)
	// The same mirror, one granularity finer. This is the clamp that makes the
	// per-path sentinel lists evidence: without it, a route quietly losing one
	// of its four witnessed credentials is a `-update-coverage` away from being
	// committed as the new truth.
	out.SentinelWitnessFloor = max(countSentinelWitnesses(verdicts), prev.SentinelWitnessFloor)
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

// assertSweepVerdictsEqual is the partition's half of the equality rule, and it
// compares the WHOLE verdict: class, inertness, the witnessed sentinel list and
// the differing JSON pointers.
//
// It used to compare Class and nothing else, which is the vacuity pathology for
// the seventh time, in the file written to stop it. Sentinels, Pointers and
// Inert were computed, committed to the artifact, and never read back -- so the
// artifact's per-path evidence was decoration, and two separate falsifications
// left the suite green:
//
//   - BY HAND. Inventing a sentinel list for /api/v1/settings, fake pointers for
//     /api/v1/destinations, flipping inert on two rows and setting
//     inertSubsetOfInvariant to 999 all passed, because nothing compared any of
//     it.
//   - BY A PLAUSIBLE CODE CHANGE. Making handleListDestinations stop emitting
//     extraInputArgs/extraOutputArgs to every principal -- "the list view does
//     not need expert mode" -- removed sentinelExpertArgs from
//     GET /api/v1/destinations. Three other sentinels remained, so the class
//     stayed "differential" and differentialFloor stayed 8, and the whole
//     internal/api suite passed while the committed artifact went on claiming
//     the argv redaction on that route was under test.
//
// The rule this file runs on is that evidence which is measured, committed and
// never compared is decoration. Every field the artifact carries is compared
// here, and each mismatch names the path and the exact thing that moved.
func assertSweepVerdictsEqual(t *testing.T, want, got []sweepVerdict) {
	t.Helper()
	index := func(rows []sweepVerdict) map[string]sweepVerdict {
		m := map[string]sweepVerdict{}
		for _, v := range rows {
			m[v.Path] = v
		}
		return m
	}
	w, g := index(want), index(got)
	for path, wv := range w {
		gv, ok := g[path]
		if !ok {
			t.Errorf("%s has a committed sweep verdict in %s and is no longer swept. "+
				"Regenerate so the removal is visible in the diff.", path, coveragePath)
			continue
		}
		if gv.Class != wv.Class {
			t.Errorf("SWEEP VERDICT CHANGED. %s is recorded as %q in %s and computes as %q "+
				"live.\ncommitted sentinels: %v\nobserved sentinels: %v\nobserved "+
				"differing pointers: %v\nIf a differential became an invariant, a planted "+
				"credential stopped reaching the high-privilege body and the sweep over "+
				"this route is now asserting absence over a body that never carried "+
				"anything. That is #165 exactly.",
				path, wv.Class, coveragePath, gv.Class, wv.Sentinels, gv.Sentinels, gv.Pointers)
			// The finer comparisons below would restate the same event three
			// times for a route that changed class; the class message already
			// prints both lists.
			continue
		}
		if lost, gained := stringSetDiff(wv.Sentinels, gv.Sentinels); len(lost) > 0 || len(gained) > 0 {
			t.Errorf("THE WITNESSED CREDENTIALS ON A SWEPT ROUTE CHANGED. %s is still %q, so "+
				"neither the class nor differentialFloor notices, but the planted "+
				"credentials the high-privilege body actually carries have moved.\n"+
				"  no longer witnessed: %v\n  newly witnessed:     %v\n"+
				"  committed: %v\n  observed:  %v\n"+
				"A sentinel that disappears here is a route that has stopped proving "+
				"anything about that credential while its absence assertion stays green -- "+
				"a handler that stops emitting a field to EVERY principal looks exactly "+
				"like this and passes everything else in the package. If the change is "+
				"intended, regenerate %s; sentinelWitnessFloor is clamped upward and will "+
				"still require the hand edit that says so out loud.",
				path, gv.Class, lost, gained, wv.Sentinels, gv.Sentinels, coveragePath)
		}
		if lost, gained := stringSetDiff(wv.Pointers, gv.Pointers); len(lost) > 0 || len(gained) > 0 {
			t.Errorf("THE READ-VS-HIGH DIFFERENCE ON A SWEPT ROUTE MOVED. %s is still %q and "+
				"the set of JSON pointers that differ between the read principal and the "+
				"high-privilege one has changed.\n"+
				"  no longer differing: %v\n  newly differing:     %v\n"+
				"  committed: %v\n  observed:  %v\n"+
				"A pointer that stops differing is a field that stopped being redacted for "+
				"one principal or stopped being served to the other, and the artifact still "+
				"claims it as the evidence for this route. Regenerate if it is intended.",
				path, gv.Class, lost, gained, wv.Pointers, gv.Pointers)
		}
		if wv.Inert != gv.Inert {
			t.Errorf("%s is committed with inert=%v and computes as inert=%v. An inert row "+
				"is a sweep reading \"[]\" or \"{}\" -- it asserts absence over nothing -- "+
				"so a route becoming inert is a fixture that stopped producing rows, and a "+
				"route ceasing to be inert is bytes nobody has looked at before. Neither is "+
				"a thing to notice by accident.", path, wv.Inert, gv.Inert)
		}
	}
	for path := range g {
		if _, ok := w[path]; !ok {
			t.Errorf("%s is swept and has no committed verdict in %s. Regenerate.",
				path, coveragePath)
		}
	}
}

// assertPartitionTotalsEqual compares the committed partition counts field by
// field. The counts are derived from the verdicts, so this is redundant with the
// comparison above for every change made by EDITING CODE -- and it is the only
// thing that fires when somebody edits the artifact instead, which is how
// inertSubsetOfInvariant: 999 survived a full suite run.
// liveExcuses is the registry as the artifact records it: sorted, keyed by
// route, with the scope stamped on. One function so that what is WRITTEN and
// what is COMPARED cannot drift apart.
func liveExcuses() []coverageExcus {
	keys := make([]string, 0, len(excusedRoutes))
	for k := range excusedRoutes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]coverageExcus, 0, len(keys))
	for _, k := range keys {
		ex := excusedRoutes[k]
		ex.Route = k
		ex.Scope = "route"
		out = append(out, ex)
	}
	return out
}

// assertProseSectionsEcho compares the three artifact sections a REVIEWER reads
// -- the excuse registry, the shape list and the deferral list -- against the
// Go values they are serialised from.
//
// They were written by -update-coverage and read back by nothing, which is the
// same defect as the sweep verdicts and worth stating in the same words: the
// artifact's whole claim on a reader's attention is that `excuses` answers "what
// is NOT covered, and why is that safe". An excuse whose Want changed in the
// code and not in the file leaves that answer stale, in the one document that
// exists to be believed. Nothing here discharges anything -- the assertions that
// do are above -- but a committed copy that is allowed to disagree with the code
// is worse than no copy, because it is quoted.
func assertProseSectionsEcho(t *testing.T, want coverageLedger) {
	t.Helper()
	assertKeyedRowsEqual(t, "excuses", want.Excuses, liveExcuses(),
		func(e coverageExcus) string { return e.Route })
	assertKeyedRowsEqual(t, "shapes", want.Shapes, emittedShapes(),
		func(s coverageShape) string { return s.Shape })
	assertKeyedRowsEqual(t, "deferred", want.Deferred, deferredWithReasons(),
		func(d coverageDefer) string { return d.ID })
}

func assertKeyedRowsEqual[T any](t *testing.T, section string, want, got []T, key func(T) string) {
	t.Helper()
	index := func(rows []T) map[string]string {
		m := map[string]string{}
		for _, r := range rows {
			b, err := json.Marshal(r)
			if err != nil {
				t.Fatalf("marshal a %s row: %v", section, err)
			}
			m[key(r)] = string(b)
		}
		return m
	}
	w, g := index(want), index(got)
	for k, wv := range w {
		gv, ok := g[k]
		if !ok {
			t.Errorf("%s: %q is committed in %s and no longer exists in the code. "+
				"Regenerate so the removal is visible in the diff.", section, k, coveragePath)
			continue
		}
		if wv != gv {
			t.Errorf("%s: %q differs between %s and the code.\ncommitted: %s\nlive:      %s\n"+
				"The artifact is what a reviewer reads to answer \"what is not covered and "+
				"why is that safe\"; a committed copy allowed to disagree with the code is "+
				"quoted anyway. Regenerate.", section, k, coveragePath, wv, gv)
		}
	}
	for k := range g {
		if _, ok := w[k]; !ok {
			t.Errorf("%s: %q exists in the code and is not in %s. Regenerate.",
				section, k, coveragePath)
		}
	}
}

// fillDerivedTotals completes the four fields of coverageTotals that are not
// counted during classification: the pair count, the non-trie probe count, and
// the two shape counts.
//
// Extracted because they used to be computed INSIDE writeLedger, which put them
// downstream of the comparison and made assertCoverageTotalsEqual read four
// zeroes. Both the write path and the assert path now derive them the same way,
// which is the only arrangement in which comparing them means anything.
func fillDerivedTotals(totals coverageTotals, routes []coverageRoute) coverageTotals {
	totals.MethodPatternPairs = len(routes)
	totals.NonTrieProbes = len(notFoundProbes()) + len(methodNotAllowedProbes())
	totals.ShapesEmitted, totals.ShapesNotInspected = 0, 0
	for _, sh := range emittedShapes() {
		if sh.Emitted {
			totals.ShapesEmitted++
			if !sh.Inspected {
				totals.ShapesNotInspected++
			}
		}
	}
	return totals
}

// assertCoverageTotalsEqual compares the committed summary block against the
// live count, field by field.
//
// THE EIGHTH INSTANCE. `totals` was computed by writeLedger, written to the
// artifact, and read back by nothing -- exactly the defect this round was
// convened to fix one struct lower down, and missed because the sweep for it
// looked at the three ARRAY sections (excuses, shapes, deferred) and not at the
// two SCALAR blocks. Replacing the committed block with
//
//	"totals": {"methodPatternPairs": 3, "swept": 999, "denied": 42, ...}
//
// left `POLYEMESIS_LEDGER=strict go test ./internal/api` at ok, and left
// `make preflight-guard` passing.
//
// Lower severity than the partition gap and worth saying why: every number here
// is derived from routes and shapes, both of which ARE compared, so no CODE
// change can hide behind it. What it permitted was an artifact edit that makes
// the summary lie indefinitely on every plain run -- and `totals` is the block
// a reviewer reads first, which is the same argument assertProseSectionsEcho
// was written on: a committed copy allowed to disagree with the code is worse
// than no copy, because it gets quoted.
func assertCoverageTotalsEqual(t *testing.T, want, got coverageTotals) {
	t.Helper()
	for _, f := range []struct {
		name      string
		want, got int
	}{
		{"methodPatternPairs", want.MethodPatternPairs, got.MethodPatternPairs},
		{"swept", want.Swept, got.Swept},
		{"excused", want.Excused, got.Excused},
		{"denied", want.Denied, got.Denied},
		{"nonTrieProbes", want.NonTrieProbes, got.NonTrieProbes},
		{"shapesEmitted", want.ShapesEmitted, got.ShapesEmitted},
		{"shapesNotInspected", want.ShapesNotInspected, got.ShapesNotInspected},
	} {
		if f.want != f.got {
			t.Errorf("totals.%s is %d in %s and computes as %d live. Like the partition "+
				"counts, these are DERIVED -- from routes and shapes -- so this fires "+
				"either because a route's verdict moved (the message naming it is above) "+
				"or because the summary block was edited without running anything.",
				f.name, f.want, coveragePath, f.got)
		}
	}
}

func assertPartitionTotalsEqual(t *testing.T, want, got partitionTotals) {
	t.Helper()
	for _, f := range []struct {
		name      string
		want, got int
	}{
		{"differential", want.Differential, got.Differential},
		{"invariant", want.Invariant, got.Invariant},
		{"inertSubsetOfInvariant", want.Inert, got.Inert},
		{"explainedVariance", want.ExplainedVariance, got.ExplainedVariance},
		{"unstable", want.Unstable, got.Unstable},
	} {
		if f.want != f.got {
			t.Errorf("partition.%s is %d in %s and computes as %d live. The counts are "+
				"DERIVED from sweepVerdicts, so this fires either because a verdict moved "+
				"-- in which case the message naming the path is above -- or because the "+
				"artifact was edited without running anything.",
				f.name, f.want, coveragePath, f.got)
		}
	}
}

// countSentinelWitnesses is the (path, sentinel) count the floor clamps.
func countSentinelWitnesses(vs []sweepVerdict) int {
	n := 0
	for _, v := range vs {
		n += len(v.Sentinels)
	}
	return n
}

func sentinelWitnessesByPath(vs []sweepVerdict) map[string]int {
	out := map[string]int{}
	for _, v := range vs {
		if len(v.Sentinels) > 0 {
			out[v.Path] = len(v.Sentinels)
		}
	}
	return out
}

// stringSetDiff reports what is in want and not in got, and vice versa.
func stringSetDiff(want, got []string) (lost, gained []string) {
	in := func(xs []string, x string) bool {
		for _, y := range xs {
			if x == y {
				return true
			}
		}
		return false
	}
	for _, x := range want {
		if !in(got, x) {
			lost = append(lost, x)
		}
	}
	for _, x := range got {
		if !in(want, x) {
			gained = append(gained, x)
		}
	}
	sort.Strings(lost)
	sort.Strings(gained)
	return lost, gained
}

// intersectCitations keeps only the citations that are BOTH live in the registry
// and already committed. Regeneration may remove; only a hand edit may add.
func intersectCitations(live, committed []string) []string {
	have := map[string]bool{}
	for _, id := range committed {
		have[id] = true
	}
	out := make([]string, 0, len(live))
	for _, id := range live {
		if have[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// assertSweptCounterpartsNameSweptRoutes is the orphan-proof check's other half.
//
// runCounterpartProofs already fails a proof that no excuse names. sweptCounterparts
// is the escape from that rule -- "this proof is named by a SWEEP instead of by an
// excuse" -- and nothing checked the claim. Renaming its key to a route the router
// does not serve, or to one that is not swept, left the proof marked as referenced
// and the whole thing passed. An orphan proof and an orphan excuse are the same
// defect seen from two sides, and both fail now.
func assertSweptCounterpartsNameSweptRoutes(t *testing.T, enumerated []coverageRoute) {
	t.Helper()
	coverage := map[string]string{}
	for _, r := range enumerated {
		coverage[r.Method+" "+r.Pattern] = r.Coverage
	}
	keys := make([]string, 0, len(sweptCounterparts))
	for k := range sweptCounterparts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		name := sweptCounterparts[key]
		if _, ok := counterpartProofs[name]; !ok {
			t.Errorf("sweptCounterparts maps %s to the proof %q, which is not in "+
				"counterpartProofs.", key, name)
		}
		switch cov, ok := coverage[key]; {
		case !ok:
			t.Errorf("sweptCounterparts names %s, which the router does not serve. That "+
				"entry is what excuses the proof %q from the \"no excuse names it\" rule, "+
				"on the grounds that a SWEEP covers the route instead -- so a key naming no "+
				"route means the proof is an orphan wearing a reference.", key, name)
		case cov != "swept":
			t.Errorf("sweptCounterparts names %s on the grounds that it is value-swept, and "+
				"the ledger classifies it as %q. The entry exists to say \"this proof is "+
				"referenced by a sweep rather than by an excuse\"; if the route is not "+
				"swept, that sentence is false and the proof %q is unreferenced.",
				key, cov, name)
		}
	}
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
			ID: "environment-skips-not-migrated",
			What: "83 t.Skip/Skipf/SkipNow sites and 11 testing.Short() sites remain outside " +
				"internal/testenv (measured; 95 and 11 at ae8df24). The twelve " +
				"SELF-SILENCING ones are handled: seven became failures or golden " +
				"assertions, four are quarantined by name, and one was rewritten to " +
				"assert the case it had been skipping. The rest are environmental -- no " +
				"FFmpeg, no GPU, -short -- and are NOT migrated to testenv this round.",
			WhySafe: "their count is frozen by the AST ratchet in internal/testenv, so no " +
				"new bare skip can land in any package, and none of them is a disclosure " +
				"guard.",
			WhatWouldMakeItUnsafe: "an environmental skip whose condition starts firing for " +
				"a non-environmental reason -- which is what \"Kick is not in Providers() " +
				"yet\" was. The ratchet is syntactic and cannot tell the two apart; a " +
				"classifier was measured at about 50% precision and rejected, because a " +
				"gate that wrong grows an override list and an override list is another " +
				"free pass. The codemod that migrates them, and the fully-provisioned CI " +
				"job that proves they are only environmental by running them, are the " +
				"follow-up.",
			Issue: "#161",
		},
		{
			ID: "empty-fixture-rows",
			What: "the routes reached only with a row this fixture does not create -- " +
				"clipper recordings, library recordings and sessions, alert rules, hooks, " +
				"schedules, renditions. Their excuses no longer discharge on prose or on " +
				"an issue number: each DRIVES the 404 it claims. What is still deferred " +
				"is the FIXTURE, not the guard.",
			WhySafe: "the 404 is now an assertion. If any of these starts answering a read " +
				"token with a body, the excuse drive fails naming the route and the " +
				"observed status, and the fix is leakRoutes().",
			WhatWouldMakeItUnsafe: "nothing silently -- that is the change. What it costs " +
				"is coverage: ten routes whose response bodies no principal has ever seen " +
				"in a test, so their leaf fields are still traced by reading rather than " +
				"by reading bytes.",
			Issue: "#163",
		},
		{
			ID: "counterpart-proofs-outside-the-preflight",
			What: "the two counterpart proofs that spawn a real FFmpeg stand-in -- " +
				"runningDestinationLogs and websocketFrames -- run as ordinary tests and " +
				"are therefore defeatable by `go test -run`, unlike everything else the " +
				"ledger asserts.",
			WhySafe: "CI sets POLYEMESIS_LEDGER=strict, which runs them from inside the " +
				"preflight as well, so no single -run filter silences both paths. Once " +
				"per process, not twice: the preflight returns immediately in TestMain's " +
				"second pass. Measured, because the first version of this change reported " +
				"+2% from `go test ./internal/api` and never ran the configuration CI " +
				"uses. `POLYEMESIS_LEDGER=strict go test -race -timeout 15m ./...`, " +
				"median of three: origin/main 301s, this branch before the fix 314s " +
				"(+4.3%), after 299s. The preflight alone costs 22.2s in that " +
				"configuration and 1.4s without it, which is the whole of the difference.",
			WhatWouldMakeItUnsafe: "CI dropping that variable. The reason they are not in " +
				"the default preflight is ~15s of wall clock on every invocation, and a " +
				"preflight nobody can afford to run is a preflight somebody deletes -- " +
				"which returns all five issues at once.",
			Issue: "#161",
		},
		{
			ID: "options-above-the-playout-gate",
			What: "OPTIONS /playout/* is answered 204 with no body ABOVE authorizePlayout, " +
				"so it is the one method+pattern pair of that mount that does not reach " +
				"the gate the other nine reach.",
			WhySafe: "found by DRIVING it, which is the point: the excuse registry used to " +
				"collapse an ANY entry to a single GET, so this pair's premise had never " +
				"been issued as a request. It is now driven with a perMethod Want stating " +
				"the 204 it really returns, byte-identical to an anonymous stranger's, with " +
				"no body -- an existence oracle at worst, of the same class as the 405 " +
				"surface in G4.",
			WhatWouldMakeItUnsafe: "the preflight response carrying anything derived from " +
				"the playout configuration -- a CORS allowlist echoing a configured origin " +
				"would do it. The behavioural fix is #170's and lands on another branch; " +
				"when it does, this Want fails with the new status and whoever rebases has " +
				"to look at it.",
			Issue: "#170",
		},
		{
			ID: "ratchet-raises-rest-on-ordinary-review",
			What: "every ratchet in this file -- excusedCeiling, " +
				"counterpartlessExcusedCeiling, differentialFloor, sentinelWitnessFloor, " +
				"unstableCeiling, inertCeiling, varianceExemptCeiling, and now citedIssues " +
				"-- is enforced by making the loosening direction a HAND EDIT of the " +
				"committed artifact. That is a control only to the extent that somebody " +
				"reads the diff.",
			WhySafe: "the edit is one line of JSON in a file whose only purpose is to be " +
				"read, next to a failure message that says in words what the edit means. " +
				"Every automatic path -- regeneration, the -update-coverage flag, the " +
				"clamps in writeLedger -- moves these numbers only in the tightening " +
				"direction, so the artifact cannot loosen itself.",
			WhatWouldMakeItUnsafe: "nothing mechanical; the residual is entirely social. " +
				"This repository has no CODEOWNERS file, so no tooling requires a " +
				"particular reviewer on a ceiling raise. That is recorded here rather than " +
				"fixed, because inventing a review process in a test file is not this " +
				"change's business, and NO ISSUE IS CITED because none is filed -- a " +
				"number typed here to fill the field would be the prose discharge this " +
				"file spent a round deleting.",
		},
		{
			ID: "invariant-is-weaker-than-differential",
			What: "the invariant verdict passes a credential that is disclosed IDENTICALLY " +
				"to every principal. Byte-equality across principals says nothing is " +
				"disclosed to one and withheld from another; it says nothing at all about " +
				"something disclosed to everyone.",
			WhySafe: "a blanket differential rule was executed and rejected: it is " +
				"unsatisfiable by construction on the routes whose credential is sealed " +
				"(hook signing secret, platform client secret, alerts.Rule.URL is " +
				"json:\"-\"), and its only available discharge there is fabricated fixture " +
				"data, which reviews identically to a real fix.",
			WhatWouldMakeItUnsafe: "a stored credential added to a response every principal " +
				"receives. The shape list is where that lives.",
			Issue: "#168",
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
