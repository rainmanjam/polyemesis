package api

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/hooks"
)

// THE DONE-CRITERION FOR THIS LEDGER, decided by a council and written here so
// that the next person inherits the CONDITION rather than the argument.
//
// The ledger is DONE at a commit iff `POLYEMESIS_LEDGER=strict go test
// ./internal/api` passes while establishing all five conjuncts below. Each is
// independently falsifiable; none of them asserts a count.
//
//  1. TOTALITY OVER A DERIVED POPULATION. Every method+pattern pair the mux
//     serves reaches exactly one word of the closed vocabulary, and the
//     POPULATION is derived from REGISTRATION -- a recorder on
//     Server.Handler() -- rather than from chi.Walk plus a hand-list. Each word
//     is produced by a request actually issued against the handler this build
//     ships, through the real mux.
//     See route_population_test.go and, for the mux half of #167, muxServingUI.
//
//  2. EVIDENCE, NOT REFUSAL. Every excused and denied word carries either an
//     executed differential -- a planted sentinel one principal demonstrably
//     receives and another does not -- or an entry on an explicit
//     declared-invariance list recording the counter-experiment that WAS
//     EXECUTED and produced no differential. A bare 403, 404 or 405 discharges
//     nothing.
//     See nonget_differential_test.go.
//
//  3. VERDICT PURITY. No verdict function reads prose, a count, or the
//     open/closed state of any issue. Blanking every free-text field changes no
//     verdict; deleting every Issue field changes no verdict except the
//     citation-LIVENESS checks, which are about citations and not about
//     coverage. Every emitted shape is either inspected by a func the preflight
//     CALLS or carries a jurisdiction record (package, test name) that resolves
//     via `go test -list` in the owning package. No shape may be emitted,
//     uninspected and citation-only.
//     See shape_jurisdiction_test.go, TestDeletingEveryProseReasonChangesNoVerdict,
//     TestBlankingEveryShapeNoteChangesNoVerdict.
//
//  4. READBACK. Perturbing any committed field of route-coverage.json fails at
//     least one named test, and exactly one declared canary field is reported
//     unasserted on every run.
//     See ledger_readback_test.go.
//
//  5. DEPTH TERMINATION. Exactly one meta-guard exists over the ledger's
//     artifact, and it contains its own live canary, which is what terminates
//     the guard ladder at depth 2. The rule is mechanical rather than a matter
//     of taste: recursion stops at the first level whose vacuity is
//     SELF-DETECTING. L1 cannot see its own vacuity -- the Inert, Sentinels and
//     Pointers history in this file is three proofs of that -- and L2 can,
//     because a perturbation loop that stops perturbing reports its canary as
//     covered and fails itself. So the canary is load-bearing: a meta-guard
//     ships with its canary or does not ship, since a canary-less L2 genuinely
//     justifies an L3 and then the ladder never ends.
//     See shape_caller_guard_test.go's noteCanary and ledger_readback_test.go's
//     declared canary field.
//
// COUNTS VERSUS PROPERTIES, because this ledger has been talked out of the
// distinction before. A count is legitimate as a RATCHET -- a bound with a
// hand-edit cost, which survives population drift -- and illegitimate as a
// CRITERION, because an equality is falsified by regeneration and carries no
// information when it holds. #163 drifted 22 -> 39 -> 32 while #176 changed the
// predicate underneath it; the G3 residual has been 123, then 83, then 76, twice
// inside one review round. So every conjunct above is universally quantified
// over a derived population, and the twelve ratchets in this file stay exactly
// where they are, as evidence rather than as criteria.
//
// THE WEAKEST PART, inherited and owned rather than argued away. Conjunct 2's
// declared-invariance list is a NEW DECLARATION CHANNEL, and every declaration
// channel this ledger has had -- Reason, DeferredIssue, By, Issue -- became a
// free pass within one round. Worse, its safeguard is a NEGATIVE measurement
// ("the counter-experiment ran and found no differential"), which is
// byte-identical to what a vacuous harness produces. The mitigation is that the
// list's own detector carries a positive control: the same code path is run
// against a pair MEASURED to disclose, and must find the sentinel there. That
// answers "the harness went blind"; it does not answer "the declaration is
// wrong about what it declares".
//
// SECOND WEAKNESS. Conjunct 1's derived population can only derive from
// registrations this process makes through Server.Handler(). A listener created
// elsewhere -- exactly #169's port 80, which binds in package main before any
// router here exists -- is invisible to any derivation rooted at this router.
// Jurisdiction totality at the outermost boundary remains a human review, and no
// mechanical repair for it is known.
//
// ------------------------------------------------------------------------------
//
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
	SentinelWitnessFloor int `json:"sentinelWitnessFloor"`
	// NonGetDifferentialFloor is the same idea on the WRITE surface, where
	// there was no positive control at all. 83 non-GET pairs are classified on
	// an executed 403, which is an invariant: it says a read principal was
	// refused and says nothing about whether anything was being withheld. Blank
	// every credential in the fixture and all 83 stay green.
	//
	// This counts (pair, sentinel) witnesses over the pairs that were MEASURED
	// to hand an admin a planted credential from the same request the read
	// principal is refused. max()-clamped like the two floors above: it may
	// rise freely and falls only by a hand edit whose sentence is "this write
	// route stopped disclosing this credential to anybody".
	NonGetDifferentialFloor int `json:"nonGetDifferentialFloor"`
	UnstableCeiling         int `json:"unstableCeiling"`
	InertCeiling            int `json:"inertCeiling"`
	VarianceExemptCeiling   int `json:"varianceExemptCeiling"`
	// THE SHAPE REGISTRY HAD NO RATCHET AT ALL, and it is the surface #169 is
	// about. Seven numbers here were clamped and all seven were about routes.
	// Deleting a shape row -- the documented response to a shape check that
	// fires -- and running the documented regeneration command moved
	// `shapesEmitted` 11 -> 10 inside a 2,064-line JSON and left the whole
	// strict suite green. `-update-coverage` cannot launder a route and
	// laundered a shape without complaint.
	//
	// ShapeFloor is max()-clamped like the two differential floors: the number
	// of shapes this API is known to emit may rise freely and falls only by a
	// hand edit that says, on purpose, "this API stopped emitting this shape".
	ShapeFloor int `json:"shapeFloor"`
	// GuardedFloor is the zero-source word's ratchet, and it is the reason that
	// word is evidence rather than a description.
	//
	// The route rows carry `zeroSource: guarded|unguarded` and
	// assertRouteSetsEqual compares them, so removing an `r.With(s.requireSource)`
	// fails TestLedgerPreflight -- with a message that names one pair and says
	// the committed word disagrees with the live one. It does not say a guard
	// vanished, and the sentence beside it in this file tells the reader to
	// regenerate. One `-update-coverage` later the word is `unguarded`, the
	// whole package is green, and the route nil-derefs to a 500 for the first
	// operator on a fresh install. Measured, on this branch, against
	// PUT /api/v1/loudness.
	//
	// The derived guard test cannot backstop it: its population comes off the
	// same live walk, so a route that loses its guard LEAVES the set it would
	// have been quantified over rather than failing inside it.
	//
	// So the count of guarded pairs is floored, max()-clamped exactly like the
	// differential floors. It may rise freely -- guarding a new route is always
	// allowed -- and falls only by a hand edit whose sentence somebody has to
	// write on purpose: "this route no longer needs a programme to act on".
	GuardedFloor int `json:"guardedFloor"`
	// And the mirror, min()-clamped like every ceiling: the number of emitted
	// shapes nobody inspects may fall freely and rises only by hand. This is
	// the one number this round moved in the loosening direction (4 -> 6, two
	// jurisdiction downgrades) and it was the one number with nothing resisting
	// it -- computed by fillDerivedTotals, written, and compared only for
	// equality against a copy that regeneration rewrites.
	ShapesNotInspectedCeiling int             `json:"shapesNotInspectedCeiling"`
	Totals                    coverageTotals  `json:"totals"`
	Partition                 partitionTotals `json:"partition"`
	Routes                    []coverageRoute `json:"routes"`
	SweepVerdicts             []sweepVerdict  `json:"sweepVerdicts"`
	Excuses                   []coverageExcus `json:"excuses"`
	Shapes                    []coverageShape `json:"shapes"`
	Deferred                  []coverageDefer `json:"deferred"`
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
	// ZeroSource is the SECOND word, and it is about a different question from
	// every other verdict in this file: not "does a read token see a
	// credential" but "what does this pair do on an install that has no
	// programme".
	//
	// A closed vocabulary of two, and both are DERIVED FROM REGISTRATION:
	//
	//	guarded    requireSource is in the middleware chain chi.Walk reports
	//	           for this pair, so the request is refused before its handler
	//	           runs
	//	unguarded  it is not, so the handler runs with s.eng() possibly nil and
	//	           is expected to answer
	//
	// Derived rather than listed, because a hand-list of guarded routes is the
	// failure this ledger is a monument to: it would be complete on the day it
	// was written and silently short by one the next time a route was added.
	// The chain comes off the SAME walk the population does, so a route that
	// loses its guard changes this artifact and a reviewer sees it in the diff.
	//
	// What the word does NOT claim is that an `unguarded` pair answers
	// correctly -- that is the every-route fresh-install walk, which lands with
	// the seed removal because that is the first branch where an empty install
	// is a real state. What it does claim is driven: every `guarded` pair is
	// issued against a server whose manager has no engines and observed to
	// return 503 with code "no_source". See
	// TestEveryGuardedRouteRefusesOnAnInstallWithNoSource.
	//
	// AND THE LIMIT OF THE DERIVATION, stated because "the list IS the
	// registration" is true of the MIDDLEWARE and not of the refusal. A route
	// whose handler reaches the same 503 through a helper -- destinationBaseArgv
	// on the three write-nothing expert routes, refuseIfSilent, writeCreateError
	// -- has no requireSource in its chain and is recorded `unguarded`, which
	// reads here exactly like GET /setup, a route that genuinely must answer. No
	// third word, because a word in this vocabulary is DRIVEN and those refusals
	// cannot be driven through the router today: a destination row cannot exist
	// on an install with no source, so every one of those requests 404s at the
	// store long before the helper is asked. The instrument that does watch them
	// is TestEveryNoSourceRefusalIsAGuardOrIsRecorded, in
	// zero_source_guard_test.go, which enumerates the refusal sites out of this
	// package's source and fails when one arrives unrecorded.
	//
	// This artifact keeps the middleware half only, and keeps it honest with
	// guardedFloor.
	ZeroSource string `json:"zeroSource"`
}

// zeroSourceWord reads the verdict off the middleware chain chi.Walk reports.
//
// Function IDENTITY, not a name and not a path prefix. reflect's code pointer
// for a method VALUE is the compiler's wrapper for that method, which is the
// same code whatever the receiver -- hence the nil one below, which is never
// called and exists only to name the wrapper. So this recognises
// Server.requireSource whichever Server built the router, and cannot be fooled
// by a middleware that merely looks like it.
// countGuardedRoutes is the measurement GuardedFloor ratchets against.
func countGuardedRoutes(routes []coverageRoute) int {
	n := 0
	for _, r := range routes {
		if r.ZeroSource == "guarded" {
			n++
		}
	}
	return n
}

func zeroSourceWord(mws []func(http.Handler) http.Handler) string {
	guard := reflect.ValueOf((*Server)(nil).requireSource).Pointer()
	for _, mw := range mws {
		if reflect.ValueOf(mw).Pointer() == guard {
			return "guarded"
		}
	}
	return "unguarded"
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

// coverageShape is the ARTIFACT PROJECTION of a shapeRow, and every field of it
// is now derived rather than written.
//
// Inspected was a hand-set bool sitting next to a hand-written By string, and
// #176's whole complaint is that the two could disagree with reality and with
// each other: `Inspected: true, By: ""` passed every check in this ledger, and
// three rows named proofs this package cannot run. Both fields are now computed
// from shapeRow.Inspector -- a func value the preflight CALLS -- so there is no
// longer any way to spell "inspected" that does not come with something that
// runs. See shapeRegistry and TestEveryInspectedShapeWitnessesItself.
type coverageShape struct {
	Shape   string `json:"shape"`
	Emitted bool   `json:"emitted"`
	// Inspected is Inspector != nil. It is not settable.
	Inspected bool `json:"inspected"`
	// InspectedBy is the runtime name of the inspector func, read out of the
	// program rather than typed into the row. It replaces `by`, which was free
	// text: a name that resolved to nothing ("TestPlayoutCookieHandoff"), to
	// another package's test, or to a helper that asserts nothing, and which the
	// ledger had to run an AST walk over the whole repository to second-guess.
	// A derived name cannot be wrong about which function will run, because it
	// is that function's name.
	InspectedBy string `json:"inspectedBy"`
	// Issue is the STRUCTURED deferral for an emitted-but-uninspected shape,
	// and it replaces a substring search over Note.
	//
	// The predicate it replaces was `Index(note,"Deferred:") >= 0 &&
	// Contains(note[i:], "#")`, which is discharge by punctuation: "Deferred: #",
	// "Deferred: #0" and "Deferred: the Set-Cookie # header, which defers
	// nothing" all discharged, and the last of those was the negative case the
	// guard's own table listed as must-fail. Worse, a citation living inside
	// free text never reached citedIssues(), so shape deferrals were the one
	// class of citation in this ledger that skipped BOTH the form check
	// (^#[0-9]{1,5}$) and the git liveness scan. Two shapes downgraded in this
	// round cited #168 in exactly that unchecked channel.
	//
	// Being a field rather than prose is what makes both of those free: it is
	// fed to citedIssues() like every other citation, and it is what step 7
	// reads, so blanking every Note cannot move a verdict. See
	// TestBlankingEveryShapeNoteChangesNoVerdict, which now mutates this
	// registry and re-runs the real decision rather than a helper written to
	// ignore prose.
	Issue string `json:"issue,omitempty"`
	// Jurisdiction is THE DISCHARGE for an emitted shape this package cannot
	// inspect, and it replaces the issue number that used to be one.
	//
	// #164 spent a round deleting "an issue number is a discharge" from the
	// route channel and the shape channel re-admitted it: shapeVerdict returned
	// "deferred" on issueRef.MatchString(sh.Issue), so four shapes rode on a
	// citation alone and closing #169 -- whose row was discharged by the literal
	// string "#169" -- broke the build. An issue's state is not evidence about
	// this API, and a verdict that reads it makes an external tracker part of
	// the test suite.
	//
	// What replaces it is a record of WHERE the assertion lives: a package and a
	// top-level test name, RESOLVED by `go test -list` in that package. That is
	// strictly weaker than an inspector -- it says an assertion exists and runs
	// somewhere, not that it read these bytes -- and strictly stronger than a
	// number, because it cannot name a test that does not exist, and because the
	// resolution is mechanical rather than a reader's trust.
	//
	// See TestEveryJurisdictionRecordResolvesToALiveTest.
	Jurisdiction *shapeJurisdiction `json:"jurisdiction,omitempty"`
	Note         string             `json:"note"`
}

// shapeJurisdiction names the package and test that assert a shape this package
// cannot reach.
type shapeJurisdiction struct {
	// Package is an import path relative to the module root, e.g.
	// "cmd/polyemesis". It is what `go test -list` is pointed at.
	Package string `json:"package"`
	// Test is a top-level Test function in that package. Resolved, never
	// trusted.
	Test string `json:"test"`
	// Why is PROSE and NOTHING READS IT, on the same rule as coverageExcus.Why.
	Why string `json:"why"`
}

// shapeVerdict is THE decision step 7 acts on, extracted so that the guard
// against prose-derived coverage can re-run the real thing.
//
// IT READS NO ISSUE, and that is this round's change. It reads Emitted,
// Inspected and Jurisdiction. It does not read Note and it does not read Issue,
// and there are two tests that blank each of those across the LIVE registry and
// re-run this function -- which is what makes them capable of failing.
//
// The consequence worth stating plainly: the open/closed state of every issue
// this ledger cites is now verdict-neutral. Closing #169 requires editing a
// reference, not repairing a build.
func shapeVerdict(sh coverageShape) string {
	switch {
	case !sh.Emitted:
		return "absent"
	case sh.Inspected:
		return "inspected"
	case sh.Jurisdiction != nil && sh.Jurisdiction.Package != "" && sh.Jurisdiction.Test != "":
		return "out-of-jurisdiction"
	default:
		return "FAILS-THE-LEDGER"
	}
}

// shapeVerdicts is shapeVerdict over a whole registry, keyed by shape.
func shapeVerdicts(shapes []coverageShape) map[string]string {
	out := make(map[string]string, len(shapes))
	for _, sh := range shapes {
		out[sh.Shape] = shapeVerdict(sh)
	}
	return out
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

	// ---- reached only with a row this fixture does not create. SEVEN ENTRIES
	// LEFT THIS BLOCK when plantRows landed: clipper recordings, library
	// recordings and sessions, alert rules, hooks, schedules and renditions are
	// now value-swept, because the fixture creates the row each of them needed
	// and the discharge rule leaves no excuse available for a pair that answers
	// a read principal 200 with a body. See leakRoutes().
	//
	// What is left here is not "a row nobody wrote yet". Each of the two
	// survivors needs something a store INSERT cannot produce, and each says
	// which thing.
	"GET /api/v1/recordings/{id}/download":             denied(),
	"GET /api/v1/recordings/stems/{name}/download":     denied(),
	"GET /api/v1/clips/{name}/download":                denied(),
	"GET /api/v1/clipper/recordings/{id}/transcript":   denied(),
	"GET /api/v1/clipper/jobs/{id}/download":           denied(),
	"GET /api/v1/library/recordings/{id}/transcript":   denied(),
	"GET /api/v1/library/recordings/{id}/media/{file}": denied(),
	"GET /api/v1/metadata/push/{id}": {
		Why: "reached only with a LIVE metadata push, which is an entry in the " +
			"in-process metadataRegistry rather than a row in any table: it is created " +
			"by POST /metadata/push making a real call to a platform, and its snapshot " +
			"moves while the push runs. Neither half is plantable by this fixture, and " +
			"a body that changes between samples is an unstable sweep rather than a " +
			"covered route. The 404 body is the handler's own \"no such metadata " +
			"push\", not the router's",
		Want:  &routeWant{As: "read", Status: http.StatusNotFound},
		Issue: "#163",
	},
	"GET /api/v1/library/search": denied(),
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
			"counterpart. OPTIONS reaches a gate of its own: playoutPreflightAllowed " +
			"applies the CONFIGURATION half -- enabled, and public unless the caller is " +
			"the operator -- and denies with the same 404 GET gives, so the two agree. " +
			"It omits the CREDENTIAL half deliberately: a preflight carries no " +
			"credentials by specification, and answering one 401 would break " +
			"cross-origin playback of a token-protected public stream.",
		Want:        &routeWant{As: "read", Status: http.StatusUnauthorized, AnonMatchesRead: true},
		Counterpart: "playoutManifestBytes",
		// The OPTIONS override survives #170's fix, and the reason it survives is
		// worth more than the override.
		//
		// This entry used to carry a tripwire: "when #170's fix lands, this Want
		// FAILS with the new status and whoever rebases has to look at it." The
		// fix landed. The Want did not fail, and nobody was sent to look.
		//
		// It did not fail because this fixture's playout is enabled and public,
		// which is the branch playoutPreflightAllowed ALLOWS -- so the status is
		// still 204, now for a different reason. The old 204 was "no gate ran";
		// today's is "the gate ran and said yes". A single status cannot tell
		// those apart, so a Want pinned to one status could never have detected
		// the change it promised to detect.
		//
		// The lesson generalises past this row: a claim that a future edit will
		// break an assertion is only as good as the fixture's coverage of the
		// branch that edit adds. Here the fixture exercises one branch of two.
		// The denial half is asserted where it can actually be driven --
		// TestPlayoutPreflightGateMatrix drives the full config x principal
		// matrix and pins the 404s, including the private-stream denials this
		// row's fixture never reaches.
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

// needsRow() USED TO STAND HERE, and its deletion is the point of this change
// rather than a tidy-up beside it.
//
// It read "reached only with a row this fixture does not create, so it answers
// 404 ... the FIXTURE is what is deferred, not the guard", and it was the single
// most-used constructor in this registry: seven routes shared it. A constructor
// that seven routes share is a constructor whose premise nobody re-checks, and
// the premise was a statement about the FIXTURE -- the one thing in this file
// that a test can change. plantRows changed it. There is now no route excused on
// the grounds that no row exists, so there is no constructor for saying so, and
// re-introducing one costs writing the sentence out again where a reviewer sees
// it.

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
	// ReadRaw is the rawResponse the identity predicate actually compares.
	// The failure message used to print ReadBody against AnonBody -- a body
	// against a whole rendered response -- so the two sides of "byte-identical"
	// were never the two things it compared. With rawResponse now rendering
	// every header, that asymmetry stopped being survivable: the leaked header
	// appeared on one side of the diff and the other side had no headers at all.
	ReadRaw   string
	AnonBody  string
	Identical bool
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
		ReadStatus: w.Code, ReadBody: w.Body.String(), ReadRaw: rd,
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
					pair, truncateForFailure(obs.ReadRaw), truncateForFailure(obs.AnonBody))
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
	// bare and built are the branch of internal/web.HandlerFor this probe
	// ENTERS, in a checkout with no UI compiled and in one with a real bundle
	// embedded. Both are DRIVEN -- see assertNotFoundProbesEnterTheirBranches --
	// which is the whole of #167: the columns used to be one column, and it was
	// the one nobody was running.
	bare, built string
	// terminal is which of the mux's non-trie terminals this probe is a WITNESS
	// for, and it is stamped by the slice the probe lives in rather than typed
	// on the row. It is the join between these probes and the population
	// recordRegistrations derives: a terminal with no probe, and a probe naming
	// a terminal this build did not register, both fail by name in
	// TestEveryNonTrieTerminalIsDerivedAndWitnessed. Before #156 these slices
	// WERE the population, so neither failure existed.
	terminal string
	// provenance is whether that terminal is a handler THIS BUILD REGISTERED or
	// chi's own default. It is part of the probe's claim, not bookkeeping: a
	// notFound probe declaring which branch of internal/web it enters is making
	// a statement about a registered handler, and the same probe against chi's
	// default 404 is driving something else entirely. See the provenance
	// constants in route_population_test.go for the mutant that proved it.
	provenance string
}

// allNonTrieProbes is every witness this package drives against the mux's
// non-trie terminals, in one place, because the derived population has to be
// reconciled against all of them and not against whichever slice a caller
// remembered.
func allNonTrieProbes() []nonTrieProbe {
	return append(append(notFoundProbes(), assetProbe()), methodNotAllowedProbes()...)
}

// The OBSERVABLE branches of internal/web.HandlerFor. Every one of them is
// distinguishable from outside by status, Content-Type and Cache-Control, which
// is what lets a test say which one a request took without reading the source
// of the thing it is testing.
const (
	webBranchAsset      = "asset-immutable" // a real file under assets/, cached forever
	webBranchAPIJSON404 = "api-json-404"    // an /api path that matched no route
	webBranchUINotBuilt = "ui-not-built"    // no index.html to fall back to
	webBranchSPAIndex   = "spa-index"       // the SPA fallback, or index.html itself
)

// notFoundProbes is the NotFound surface, hand-declared because chi.Walk cannot
// emit it, and EXECUTED rather than merely listed.
//
// #167 measured what "executed" was worth here: `.github/workflows/ci.yml` does
// not build the UI before the Go job, so internal/web/dist holds only .gitkeep,
// and eight of these nine took the "UI not built" branch in every run that has
// ever claimed to cover this surface. `/assets/app.js` did not reach the asset
// branch -- and, found by driving it, does not reach the asset branch even with
// a real bundle embedded, because Vite fingerprints its output and no file of
// that name is ever produced. The name was aspirational in both configurations.
func notFoundProbes() []nonTrieProbe {
	return stampTerminal(terminalNotFound, provenanceRegistered, []nonTrieProbe{
		{http.MethodGet, "/", "the SPA root", webBranchUINotBuilt, webBranchSPAIndex, "", ""},
		{http.MethodGet, "/assets/app.js", "a bundled asset PATH -- and not a bundled asset: " +
			"Vite fingerprints its output, so nothing is ever named this. It reaches the " +
			"asset branch in neither configuration, which is why assetProbe below exists",
			webBranchUINotBuilt, webBranchSPAIndex, "", ""},
		{http.MethodGet, "/.env", "the credential file every scanner asks for first",
			webBranchUINotBuilt, webBranchSPAIndex, "", ""},
		{http.MethodGet, "/debug/pprof/", "the profiler surface, if anything ever mounted it",
			webBranchUINotBuilt, webBranchSPAIndex, "", ""},
		{http.MethodGet, "/metrics", "the Prometheus convention, unrouted here",
			webBranchUINotBuilt, webBranchSPAIndex, "", ""},
		{http.MethodGet, "/API/V1/SETTINGS", "a case-varied spelling of a real route",
			webBranchUINotBuilt, webBranchSPAIndex, "", ""},
		{http.MethodPost, "/.env", "an unmatched METHOD as well as an unmatched path",
			webBranchUINotBuilt, webBranchSPAIndex, "", ""},
		{http.MethodDelete, "/anything", "a destructive method on the catch-all",
			webBranchUINotBuilt, webBranchSPAIndex, "", ""},
		{http.MethodGet, "/api/v1/no-such-route", "an unrouted path INSIDE the API prefix",
			webBranchAPIJSON404, webBranchAPIJSON404, "", ""},
		// THE TENTH PROBE, added because the branch table made its absence
		// legible. Opening a DIRECTORY under the sub-FS succeeds, so this used to
		// reach http.FileServer and be answered 200 with an index of the bundle
		// inventory -- the disclosure #156's review named and nothing drove.
		// internal/web now falls a directory through to the SPA branch, and this
		// row is what pins it from the ledger's side.
		{http.MethodGet, "/assets/", "the asset ROOT: a directory, not a file",
			webBranchUINotBuilt, webBranchSPAIndex, "", ""},
	})
}

// stampTerminal sets the terminal on a whole slice, because it is a property of
// which surface the slice is about rather than of the individual row. A field
// typed once per row is a field that can be typed wrong, and this ledger has
// already deleted two of those.
func stampTerminal(name, provenance string, probes []nonTrieProbe) []nonTrieProbe {
	for i := range probes {
		probes[i].terminal = name
		probes[i].provenance = provenance
	}
	return probes
}

// assetProbe is the tenth probe, and it exists because the ninth was named for a
// branch it never entered.
//
// It is not in notFoundProbes because it is only meaningful against a populated
// filesystem: through the real mux, in this repository's build, it is just
// another 404. The branch assertion drives it in the built column, where it is
// the ONLY probe that reaches the immutable-cache branch.
func assetProbe() nonTrieProbe {
	return stampTerminal(terminalNotFound, provenanceRegistered, []nonTrieProbe{{http.MethodGet,
		"/assets/index-abc123.js",
		"a fingerprinted bundle, the only path shape that reaches the asset branch",
		webBranchUINotBuilt, webBranchAsset, "", ""}})[0]
}

// builtUIFS is a synthetic `dist` -- an index.html and one fingerprinted bundle
// -- standing in for what `npm run build` produces.
//
// The alternative was to make CI build the UI before the Go job, which is a
// four-minute npm install on every Go run to make nine probes honest. This
// drives the same branches at no cost, and it is the filesystem the handler
// actually reads rather than a description of one.
// bareUIFS is a checkout with no UI compiled: an empty filesystem, constructed
// here rather than borrowed from the embedded one.
//
// ISSUE #284. This column used to be web.FS(), the real embedded filesystem,
// which is bare only while internal/web/dist holds nothing but .gitkeep. That
// is true in CI because the go job does not run `npm run build`, and false for
// any developer who has -- so `npm run build && go test ./internal/api` failed
// with every bare probe entering "spa-index", and the failure named the handler
// rather than the environment.
//
// The same coupling has bitten from the other side. The /assets/ directory
// listing shipped in v0.6.0 because "CI's Go job does not run npm run build, so
// the tests ran against an empty dist -- and against an empty filesystem the
// fixed and unfixed handlers are byte-identical". A probe whose verdict depends
// on whether somebody ran a build is measuring the environment as much as the
// code, in both directions.
//
// Constructed, so both columns are driven in the same run whatever is on disk,
// exactly as builtUIFS below already was.
func bareUIFS() fs.FS { return fstest.MapFS{} }

func builtUIFS() fs.FS {
	return fstest.MapFS{
		"index.html":              {Data: []byte("<!doctype html><title>polyemesis</title>")},
		"assets/index-abc123.js":  {Data: []byte("export default 1;\n")},
		"assets/index-abc123.css": {Data: []byte(":root{}\n")},
	}
}

// muxServingUI builds a REAL router from the server under test whose NotFound
// terminal serves the given filesystem.
//
// It is the whole mechanism behind driving the built column end to end: every
// middleware registerRoutes installs is in front of the terminal, exactly as in
// the shipped binary, and the only thing that differs from production is which
// `dist` the embedded-UI closure reads. The field is restored immediately --
// Handler() captures the closure at build time, so the returned mux keeps
// serving the chosen filesystem and every other caller of s.Handler() goes on
// getting the embedded one.
func muxServingUI(t *testing.T, s *Server, fsys fs.FS) http.Handler {
	t.Helper()
	prev := s.uiFS
	defer func() { s.uiFS = prev }()
	s.uiFS = fsys
	return s.Handler()
}

// observedWebBranch classifies a response into one of the four branches, from
// the bytes a client receives.
//
// From OUTSIDE, deliberately. A classifier that read internal/web's source
// would pass just as happily on the day the handler stops calling the thing it
// claims to call, which is #107's finding one package to the left.
func observedWebBranch(w *httptest.ResponseRecorder) string {
	cc := w.Header().Get("Cache-Control")
	ct := w.Header().Get("Content-Type")
	body := w.Body.String()
	switch {
	case cc == "public, max-age=31536000, immutable":
		return webBranchAsset
	case w.Code == http.StatusNotFound && strings.HasPrefix(ct, "application/json") &&
		strings.Contains(body, `"no such endpoint"`):
		return webBranchAPIJSON404
	case w.Code == http.StatusNotFound && strings.Contains(body, "UI not built."):
		return webBranchUINotBuilt
	case w.Code == http.StatusOK && strings.HasPrefix(ct, "text/html"):
		return webBranchSPAIndex
	}
	return fmt.Sprintf("unclassified(%d, %q, %q)", w.Code, ct, cc)
}

// assertNotFoundProbesEnterTheirBranches is #167.
//
// The finding was not that this surface is dangerous -- it is verified benign,
// and the invariance assertion in TestTheNonTrieSurfacesAreDriven is what says
// so. The finding was that the CI run claiming to cover it was covering a
// DIFFERENT HANDLER from the one production ships: dist holds only .gitkeep in
// the Go job, so the SPA and asset branches never executed and nobody could
// tell, because nothing recorded which branch a probe took.
//
// Both columns are driven here THROUGH THE REAL CHI MUX, over a filesystem this
// test chooses. That is the difference between this and the tripwire retired in
// #240: a claim about the other configuration is worthless unless the fixture
// reaches it, so this fixture reaches it.
//
// THROUGH THE MUX IS THIS ROUND'S CHANGE, and it is the rest of #167. The
// columns used to be driven against web.HandlerFor DIRECTLY -- no requestLogger,
// no securityHeaders, no chi routing in front of them -- so the built column
// described a handler in isolation while the configuration production ships was
// the one nothing had entered end to end. Server.uiHandler is a seam for exactly
// this: registerRoutes mounts whatever it returns, and a test can choose the
// filesystem without any of the middleware above it changing. If a middleware
// ever rewrites a response on the way out -- the "what would make it unsafe" of
// the deferral this replaces -- the branch a probe enters moves and this fails.
//
// It also asserts the PRECONDITION rather than assuming it: whether this
// checkout embedded a real UI is read from web.Built(), and the branch every
// probe takes through the REAL mux must agree with it. Build the UI and re-run,
// and the bare column stops being the one asserted through the mux.
func assertNotFoundProbesEnterTheirBranches(t *testing.T, s *Server) {
	t.Helper()

	columns := []struct {
		name   string
		fsys   fs.FS
		branch func(nonTrieProbe) string
	}{
		{"bare", bareUIFS(), func(p nonTrieProbe) string { return p.bare }},
		{"built", builtUIFS(), func(p nonTrieProbe) string { return p.built }},
	}

	probes := append(notFoundProbes(), assetProbe())
	entered := map[string]map[string]bool{}
	for _, col := range columns {
		h := muxServingUI(t, s, col.fsys)
		entered[col.name] = map[string]bool{}
		for _, p := range probes {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(p.method, p.path, nil))
			got := observedWebBranch(w)
			entered[col.name][got] = true
			if want := col.branch(p); got != want {
				t.Errorf("%s %s (%s), %s filesystem: entered the %q branch of "+
					"internal/web.HandlerFor THROUGH THE REAL MUX, and the probe "+
					"declares %q.\n"+
					"This column is DRIVEN, which it was not before #167: the probe set "+
					"used to record only that the responses were principal-independent, "+
					"and could not distinguish \"the SPA served index.html\" from \"there "+
					"was no index.html to serve\". status %d, Content-Type %q, "+
					"Cache-Control %q.",
					p.method, p.path, p.why, col.name, got, want,
					w.Code, w.Header().Get("Content-Type"), w.Header().Get("Cache-Control"))
			}
		}
	}

	// THE COVERAGE FACT, stated as an assertion rather than left implicit. Each
	// column must enter every branch the other does not, or the split is
	// decoration: if the built column stopped reaching the asset branch, every
	// per-probe check above would still pass and this surface would be back to
	// one configuration under test.
	for _, want := range []struct{ column, branch string }{
		{"bare", webBranchUINotBuilt},
		{"bare", webBranchAPIJSON404},
		{"built", webBranchSPAIndex},
		{"built", webBranchAsset},
		{"built", webBranchAPIJSON404},
	} {
		if !entered[want.column][want.branch] {
			t.Errorf("no probe entered the %q branch of internal/web.HandlerFor against the "+
				"%s filesystem. Every assertion above is per-probe and a probe set that "+
				"stopped reaching a branch would satisfy all of them; this is the check "+
				"that says the branch was executed at all.\nbranches entered: %v",
				want.branch, want.column, sortedSet(entered[want.column]))
		}
	}

}

// assertNotFoundProbesMatchThisBuild is the precondition half of #167, and it is
// the half that has to run through the REAL mux.
//
// Which column above describes this invocation is a fact about the checkout, not
// a choice, and it was exactly the fact CI was getting silently wrong: the run
// asserted a surface and never recorded that the surface it drove had no UI
// behind it. UIBuilt() is the server's own answer, and the branch each probe
// takes through r.NotFound must agree with it.
func assertNotFoundProbesMatchThisBuild(t *testing.T, h http.Handler) {
	t.Helper()
	built := UIBuilt()
	for _, p := range notFoundProbes() {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(p.method, p.path, nil))
		got := observedWebBranch(w)
		want := p.bare
		if built {
			want = p.built
		}
		if got != want {
			t.Errorf("%s %s through the real mux entered the %q branch; this checkout "+
				"reports UIBuilt()=%v, for which the probe declares %q.\n"+
				"Before #167 nothing compared these, so a CI job that never runs "+
				"`npm run build` reported coverage of a handler serving an empty "+
				"embed.FS while the shipped binary serves a populated one.",
				p.method, p.path, got, built, want)
		}
	}
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// methodNotAllowedProbes is G4: chi's methodNotAllowed short-circuits BEFORE
// group middleware, so requireAuth never runs and an anonymous caller can tell a
// registered (method, path) pair from an unregistered one.
//
// Enumerated and asserted here rather than fixed. Low severity -- docs/API.md
// publishes the table -- and the behavioural change is filed. What was true
// before is that no test drove this response class at all.
func methodNotAllowedProbes() []nonTrieProbe {
	return stampTerminal(terminalMethodNotAllowed, provenanceChiDefault, []nonTrieProbe{
		// No bare/built branch: these never reach internal/web at all. chi's
		// methodNotAllowed answers them before r.NotFound is consulted, which is
		// the whole of G4.
		{method: http.MethodHead, path: "/api/v1/settings",
			why: "a GET-only route answered 405 with Allow: GET"},
		{method: http.MethodPut, path: "/api/v1/upgrade/stage",
			why: "a POST-only route answered 405 with Allow: POST"},
	})
}

// ------------------------------------------------------------------ the shapes

// shapeRig is the fixture the shape inspectors run against: ONE planted server,
// built once per inspection pass and shared by every inspector that can use it.
//
// #176's fourth acceptance criterion, and the reason this is a struct rather
// than each inspector standing up its own server: "the preflight's wall clock
// does not materially grow -- piggyback on the existing planted server rather
// than standing up new fixtures. A preflight that doubles in cost is a preflight
// somebody deletes." Two inspectors genuinely cannot share it and each says so
// on its own line.
type shapeRig struct {
	h    http.Handler
	sign func(*http.Request)
	// read is a read-scoped bearer on h, minted once.
	read string
	// s is the *Server behind h, CAPTURED HERE rather than fetched later.
	//
	// serverUnderTest returns package-global lastTestServer -- "the Server most
	// recently built by renditionServer" -- and two of the inspectors below
	// build a playout origin of their own. Reading the global after they have
	// run hands back THEIR server, so the websocket inspector dialled a
	// different process from the one it holds a token for and got 401. Found by
	// running it; the first version of this rig did exactly that.
	s *Server
}

func newShapeRig(t *testing.T) shapeRig {
	t.Helper()
	h, _, sign := plantedServer(t)
	return shapeRig{
		h:    h,
		sign: sign,
		read: createScopedToken(t, h, sign, "ledger-shapes", db.ScopeRead),
		s:    serverUnderTest(t, h),
	}
}

// shapeObservation is what an inspector must produce, and it is the shape
// registry's answer to the same question the counterpart registry answers with
// proofResult: BYTES, not a claim.
//
// Shape is not decoration. It must equal the row's Shape, which is what makes an
// inspector wired to the wrong row a failure by name rather than a silent
// re-attribution -- the mistake `By: "TestPlayoutCookieHandoff"` was one typo
// away from, on a field nothing resolved.
type shapeObservation struct {
	Shape  string
	Sample string
}

type shapeInspector func(*testing.T, shapeRig) shapeObservation

// shapeRow is the registry entry. It replaces the six-field positional literal
// whose third element was a hand-set `Inspected` bool.
//
// THERE IS NO `Inspected` FIELD, and that is the point of #176's first
// acceptance criterion ("`Inspected: true` with a nil inspector does not
// compile"). The stronger form landed instead: the claim is not checkable-but-
// unwritable, it is UNSPELLABLE. Inspection is `Inspector != nil`, computed in
// emittedShapes, and the only way to move a shape into the inspected column is
// to hand the ledger a function it will call.
type shapeRow struct {
	Shape   string
	Emitted bool
	// Inspector is the proof. Nil means not inspected; non-nil means the
	// preflight CALLS it and requires it to witness this row's shape in real
	// emitted bytes.
	Inspector shapeInspector
	// LiveTools marks the one inspector that cannot run on the shared rig
	// because its shape only exists once a real child process has spawned and
	// written. It costs an FFmpeg stand-in and a spawn wait, so it runs in
	// strict mode with the counterpart proofs rather than on every `go test`.
	// That residual is a deferral row, not an implicit gap.
	LiveTools bool
	// Jurisdiction is the discharge for a shape whose assertion lives in another
	// package. Issue is a citation for a reader and discharges nothing; see
	// coverageShape.Jurisdiction for the round that made that true.
	Jurisdiction *shapeJurisdiction
	Issue        string
	Note         string
}

// emittedShapes is I5: coverage is (method, pattern, SHAPE), because the two
// things that escaped were both shapes rather than routes. The playout manifest
// is a streaming response and the argv leak travelled through a WebSocket frame;
// a ledger that only counted routes would have called both covered.
//
// It is now a PROJECTION of shapeRegistry rather than the registry itself, so
// the artifact's `inspected` and `inspectedBy` columns are read out of the
// program instead of typed next to it.
func emittedShapes() []coverageShape {
	rows := shapeRegistry()
	out := make([]coverageShape, 0, len(rows))
	for _, r := range rows {
		out = append(out, coverageShape{
			Shape:        r.Shape,
			Emitted:      r.Emitted,
			Inspected:    r.Inspector != nil,
			InspectedBy:  inspectorName(r.Inspector),
			Issue:        r.Issue,
			Jurisdiction: r.Jurisdiction,
			Note:         r.Note,
		})
	}
	return out
}

// inspectorName is the DERIVED `inspectedBy`: the runtime name of the function
// the ledger is going to call, trimmed to package.Func.
//
// The whole of shape_reference_test.go's symbol index existed to second-guess a
// hand-written string with an AST walk over the repository. A name taken from
// the func value cannot name something that does not exist, cannot name another
// package's test, and cannot name a helper the ledger will not run -- the three
// defects that walk was built to find.
func inspectorName(fn shapeInspector) string {
	if fn == nil {
		return ""
	}
	full := runtime.FuncForPC(reflect.ValueOf(fn).Pointer()).Name()
	if i := strings.LastIndex(full, "/"); i >= 0 {
		full = full[i+1:]
	}
	return full
}

func shapeRegistry() []shapeRow {
	return []shapeRow{
		{Shape: "json-body", Emitted: true, Inspector: inspectJSONBody,
			Note: "the value sweep: real read-bearer bytes scanned for every planted sentinel"},
		// THE RESPONSE-HEADER FAMILY IS DERIVED (#168). Every row whose shape
		// starts "response-header/" is joined to an AST scan of this package's
		// own source: assertDerivedHeaderShapesAreRegistered fails if this
		// package writes a header with no row here, and fails if a row here
		// names a header no site in this package writes. See
		// shape_derivation_test.go for what that scan can and cannot see.
		//
		// THE MEASUREMENT THAT JUSTIFIES THE WHOLE FAMILY. Before the scan
		// existed there were FOUR rows -- Location, Set-Cookie, Cache-Control,
		// Content-Disposition -- and this package emits SIXTEEN distinct
		// response headers. Twelve had never been written down, and nothing in
		// this ledger could have said so, because the population was the list.
		// That is #168's sentence, measured.
		//
		// CORRECTED, and this is the second thing the derivation found. This
		// row carried a jurisdiction record pointing at package main on the
		// reading that Location is emitted by the HTTPS redirect wrapper. It is
		// also emitted HERE: http.Redirect is called twice in this package, both
		// in oauth_handlers.go, and the OAuth flow's Location comes off this
		// router. The ledger was excusing itself out of a header its own routes
		// write, and no test in this package read a Location before
		// inspectLocationHeader. cmd/polyemesis's redirect is still a second
		// surface and is still covered as such -- see the plain-http-listener
		// row, which is about that listener rather than about this header.
		{Shape: "response-header/Location", Emitted: true, Inspector: inspectLocationHeader,
			Note: "a redirect's target, and a credential-bearing shape twice over: the OAuth " +
				"callback puts an outcome in it, and the package-main HTTPS redirect copies " +
				"the request URI verbatim, watch token included. Two sites here, both " +
				"through oauthDone; the inspector witnesses the callback's."},
		{Shape: "response-header/Set-Cookie", Emitted: true, Inspector: inspectPlayoutCookie,
			Note: "the playout watch cookie"},
		// CORRECTED with Location, and for the same reason: five sites in this
		// package write Cache-Control, one of them the principalVaryingResponse
		// that exists because those bodies depend on who asked. The package-main
		// jurisdiction record was true about a surface and wrong about the
		// header.
		{Shape: "response-header/Cache-Control", Emitted: true, Inspector: inspectCacheControlHeader,
			Note: "whether a credential-bearing response may be stored. Five sites; the " +
				"inspector witnesses principalVaryingResponse's `private, no-store` on a " +
				"redacted body, which is the one this row is about. The poster's `public, " +
				"max-age` is the same header and not the same claim, which is why the " +
				"inspector requires the substring rather than mere presence."},
		{Shape: "response-header/Content-Disposition", Emitted: true,
			Inspector: inspectContentDispositionHeader,
			Note: "download filenames; media names only, no stored credential. Four sites " +
				"(clips, automation, recordings, the CA certificate); the inspector drives " +
				"the CA download, which is the one reachable without planting a media file. " +
				"What is still true and is now the file-download row's business rather than " +
				"this one's: the download routes are excused from the value sweep entirely, " +
				"so no principal-pair comparison reads their headers."},
		{Shape: "response-header/Content-Type", Emitted: true, Inspector: inspectContentTypeHeader,
			Note: "eight sites, from JSON to PEM to image/jpeg to the HLS manifest type. The " +
				"inspector witnesses writeJSON's, which is the one nearly every route in " +
				"this API emits."},
		{Shape: "response-header/X-Content-Type-Options", Emitted: true,
			Inspector: inspectNosniffHeader,
			Note: "nosniff. Four sites, one of them the security middleware every response " +
				"passes through, so a browser is never invited to guess a type for bytes " +
				"this API labelled."},
		{Shape: "response-header/Vary", Emitted: true, Inspector: inspectVaryHeader,
			Note: "the cache key. Five sites across principalVaryingResponse and " +
				"playoutVaryingResponse; the inspector requires Authorization to be named, " +
				"because a shared cache that keys a redacted body without it hands one " +
				"principal another's response."},
		{Shape: "response-header/Content-Security-Policy", Emitted: true, Inspector: inspectCSPHeader,
			Note: "the security middleware's policy, plus the watch page's relaxed " +
				"frame-ancestors when cross-origin embedding is on. Never previously a " +
				"shape row despite being on every response this API sends."},
		{Shape: "response-header/X-Frame-Options", Emitted: true, Inspector: inspectFrameOptionsHeader,
			Note: "DENY, from the security middleware; deleted again by the watch handler " +
				"when embedding is allowed. Del is not an emission and the derivation does " +
				"not count it."},
		{Shape: "response-header/Referrer-Policy", Emitted: true,
			Inspector: inspectReferrerPolicyHeader,
			Note: "no-referrer, so a watch URL carrying a playback token is not handed to " +
				"whatever the page links to."},
		{Shape: "response-header/Permissions-Policy", Emitted: true,
			Inspector: inspectPermissionsPolicyHeader,
			Note:      "camera, microphone and geolocation refused for this origin."},
		{Shape: "response-header/Strict-Transport-Security", Emitted: true,
			Inspector: inspectHSTSHeader,
			Note: "emitted only on a certificate a browser will validate AND a genuinely " +
				"encrypted hop, which is why no fixture in this package had ever produced " +
				"one. The inspector drives securityHeaders directly with both conditions " +
				"true; it is the only inspector in this registry with no server behind it."},
		{Shape: "response-header/WWW-Authenticate", Emitted: true,
			Inspector: inspectWWWAuthenticateHeader,
			Note: "the Basic challenge a protected playout answers a tokenless viewer with. " +
				"Asserted elsewhere in this package by TestPlayoutMediaChallengeNamesBasic; " +
				"the ledger had never counted it."},
		{Shape: "response-header/Access-Control-Allow-Origin", Emitted: true,
			Inspector: inspectCORSHeader,
			Note: "the constant `*`, never a reflected origin -- which is measured, and is " +
				"why Vary: Origin is deliberately absent. Three sites, all behind the " +
				"AllowCrossOrigin setting, so the inspector needs a fixture with embedding " +
				"turned on."},
		{Shape: "response-header/Retry-After", Emitted: true, Inspector: inspectRetryAfterHeader,
			Note: "the login throttle's delay, and the one header in this API whose VALUE is " +
				"a measurement rather than a constant. The inspector requires a positive " +
				"number of seconds: a present `0` is a throttle inviting the caller " +
				"straight back."},
		{Shape: "response-header/Content-Length", Emitted: true,
			Inspector: inspectContentLengthHeader,
			Note: "one site, the playout poster, and the emission NO TEST IN THIS PACKAGE " +
				"HAD EVER REACHED: posterJPEG needs a segment on disk and a real FFmpeg, so " +
				"every fixture here gets the 404 branch. Surfaced by the derivation and " +
				"inspected by priming the poster cache, which drives the real handler down " +
				"the real 200 branch for one request."},
		{Shape: "streaming-media", Emitted: true, Inspector: inspectStreamingManifest,
			Note: "the HLS manifest and its segments -- the shape a body sweep reads none of, " +
				"and the one that escaped the previous audit. The old `by` string named the " +
				"playoutManifestBytes counterpart, which never reads a manifest: on the " +
				"planted fixture that route answers a read token 50 bytes of " +
				"{\"error\":\"this stream requires a playback token\"}. Correct behaviour, " +
				"asserted by that excuse's Want -- and not this shape."},
		// THE ROW THE MEDIA-TYPE CENSUS PRODUCED (#168, half two), and the
		// second time a derivation in this ledger has answered "there is a
		// shape here nobody wrote down". The first was twelve response
		// headers; this is one payload.
		//
		// image/jpeg is spelled at exactly one site in this package --
		// playout.go's poster handler -- and it belonged to no row. Not
		// json-body, not file-download (nothing sets Content-Disposition and it
		// is served inline to a player), and not streaming-media, whose row is
		// about the manifest and its segments. A still frame of a stream is its
		// own kind of bytes, and it is a disclosure surface: authorizePlayout
		// gates it, it is cached `public, max-age=10`, and it answers
		// Access-Control-Allow-Origin: * when embedding is on.
		{Shape: "playout-poster", Emitted: true, Inspector: inspectPlayoutPoster,
			Note: "a JPEG still rendered off the live stream by FFmpeg and served from the " +
				"poster cache. The RENDER is out of this package's fixture budget for the " +
				"same reason response-header/Content-Length's was -- posterJPEG needs a .ts " +
				"segment on disk and a real FFmpeg, so every fixture here takes the 404 " +
				"branch -- so the inspector primes the cache and witnesses the SERVING: the " +
				"200 branch rather than the 404, the bytes passed through verbatim, and a " +
				"body that is not JSON wearing an image label."},
		{Shape: "file-download", Emitted: true, Issue: "#168",
			Jurisdiction: &shapeJurisdiction{
				Package: "internal/api", Test: "TestStemDownloadServesAStemInsideTheStemsDirectory",
				Why: "the positive case of the stem download: a principal entitled to the " +
					"file receives its bytes and they are compared. Same caveat as " +
					"Content-Disposition -- one route, not the shape, and not called by " +
					"the preflight."},
			Note: "recordings, stems, clips and exports. #154 decided this and is CLOSED by " +
				"ae8df24: every download route now answers a read token 403, which the " +
				"excuse registry drives. What is still uninspected is the shape itself " +
				"for the principals entitled to it."},
		{Shape: "websocket-frame", Emitted: true, Inspector: inspectWebsocketFrame,
			Note: "one policy row per events.Type over a CLOSED table; an unclassified type " +
				"fails the build and is dropped for a read scope"},
		// THE ONLY NEGATIVE CLAIM IN THIS REGISTRY, and until the media-type
		// census it was the only row nothing could check. `Emitted: false` gives
		// step 7 the verdict "absent", so the row needs neither an inspector nor
		// a jurisdiction record -- which means adding an event-stream handler to
		// this package would have left every test in the repository green while
		// a row went on saying the shape does not exist. It is now discharged by
		// a scan that FINDS NOTHING, in the same run in which the same scan
		// finds the eight media types this package does emit. That second half
		// is not decoration: an absence proved by a broken scanner is free.
		{Shape: "sse", Note: "ABSENT: this API emits no server-sent events, and " +
			"assertDerivedPayloadShapesAreRegistered fails if text/event-stream ever " +
			"appears in this package's source"},
		{Shape: "mqtt-retained-topic", Emitted: true, Issue: "#160",
			Jurisdiction: &shapeJurisdiction{
				Package: "cmd/polyemesis", Test: "TestTheRetainedDestTopicIsScrubbedAtTheSink",
				Why: "cmd/polyemesis/mqtt.go is what publishes it, and that test asserts " +
					"the retained payload is scrubbed before it reaches the broker."},
			Note: "cmd/polyemesis/mqtt.go publishes Status.LastError RETAINED, with no principal " +
				"and never any. Scrubbed at source by supervisor.scrub; the broker-side " +
				"consumer audit is the deferral."},
		// THE OTHER TWO PRINCIPAL-LESS EGRESSES (#169). Structurally identical to
		// the retained MQTT topic above -- a payload this process sends outward,
		// to a party it chose, with nobody to redact for -- and absent from this
		// list until now. The read-token value sweep that covers the rest of this
		// ledger cannot reach them: it asks whether one principal receives what
		// another does not, and there is no principal here at all. So the
		// question asked instead is the one the argv leak answered wrongly,
		// whether a stored credential reaches the wire for anybody.
		//
		// Both are INSPECTED rather than deferred, and that is a claim #176's
		// model can hold this time: an outbound egress is reachable from the
		// shared rig without a child process. The endpoint is an httptest server
		// in this process, so what these cost is one HTTP round trip each --
		// nearer inspectJSONBody than the LiveTools row below.
		{Shape: "outbound-hook-body", Emitted: true, Inspector: inspectOutboundHookBody,
			Note: "internal/hooks.Dispatcher's webhook POST: the Envelope and the signature " +
				"header, read off a real socket, over the deliver path rather than " +
				"Dispatcher.Test. Redacted at Publish; the inspector reads the far end."},
		{Shape: "outbound-alert-body", Emitted: true, Inspector: inspectOutboundAlertBody,
			Note: "internal/alerts.Notifier's alert POST, witnessed on the COALESCED path -- " +
				"the one every real alert takes and the only one that can carry process " +
				"state. Notifier.Test's synthetic body travels the same encoder and is " +
				"read by TestOutboundAlertPayloadCarriesNoStoredCredential rather than " +
				"here, because a synthetic item cannot discriminate this shape from a " +
				"fixed sentence."},
		// THE PORT-80 LISTENER, which #169 records as outside the ledger entirely
		// rather than merely uninspected. Not downgraded from anything: it has
		// never been here.
		//
		// Uninspected under #176's rule for the same reason response-header/
		// Location is: an inspector would have to stand up something that lives
		// in package main. Giving it a token inspector that witnessed a redirect
		// from some other handler is precisely the substitution #176 caught
		// streaming-media making.
		{Shape: "plain-http-listener", Emitted: true, Issue: "#169",
			Jurisdiction: &shapeJurisdiction{
				Package: "cmd/polyemesis", Test: "TestOrdinaryHostsStillRedirect",
				Why: "THIS ROW IS WHY THE JURISDICTION FIELD EXISTS. It used to discharge " +
					"on the literal string \"#169\" -- an issue number as a discharge, the " +
					"thing #164 spent a round deleting, re-admitted through the shape " +
					"channel -- which made closing #169 break the build. #169 is " +
					"permanently open BY CONSTRUCTION: the port-80 listener binds in " +
					"package main before any router in this package exists, and that is " +
					"an architectural record rather than unfinished work. Its state is " +
					"now verdict-neutral, and this record is what answers the ledger."},
			Note: "cmd/polyemesis.startHTTPHelper binds :80 and serves the ACME http-01 " +
				"responder plus a permanent redirect, on a listener no ledger row has ever " +
				"named. Its Location header IS covered as a shape -- response-header/" +
				"Location, deferred under #168 for the same jurisdiction reason -- but the " +
				"LISTENER is a second surface: it answers before any router in this " +
				"package exists, in package main, with no principal and no auth. The " +
				"assertion that exists lives in cmd/polyemesis/redirect_test.go and does " +
				"not run when this ledger runs."},
		{Shape: "on-disk-process-log", Emitted: true, Inspector: inspectProcessLog, LiveTools: true,
			Note: "the file that goes into support tarballs; asserted from disk. Its inspector " +
				"needs a spawned child, so it runs under POLYEMESIS_LEDGER=strict with the " +
				"counterpart proofs -- see the counterpart-proofs-outside-the-preflight " +
				"deferral, which now covers it too."},
		{Shape: "slog-output", Emitted: true, Inspector: inspectSlogOutput,
			Note: "the server's own structured log. PROMOTED from a citation to an " +
				"inspector in the round that deleted issue-numbers-as-discharge from the " +
				"shape channel: the row cited #160 and nothing read the bytes, and the " +
				"bytes turned out to be one field swap away -- s.log is a plain field, so " +
				"an inspector points it at a buffer, drives one request through the real " +
				"mux and reads what the request logger emitted. A citation was never the " +
				"cheapest option here; it was the one nobody had priced."},
	}
}

// ------------------------------------------------------------ the inspectors
//
// Every one of these RETURNS THE BYTES IT READ and asserts the property that
// distinguishes its shape from any other -- valid JSON, an #EXTM3U line, a
// cookie by name. A sample that is merely non-empty would let a 50-byte error
// body stand in for a manifest, which is exactly what the string this registry
// used to carry was doing.

func inspectJSONBody(t *testing.T, rig shapeRig) shapeObservation {
	t.Helper()
	body := rawBody(t, rig.h, bearer(rig.read), "/api/v1/settings")
	if !json.Valid([]byte(body)) {
		t.Errorf("the json-body inspector read %d bytes from GET /api/v1/settings and they "+
			"are not valid JSON, so what it witnessed is not the shape this row claims.\n%s",
			len(body), truncateForFailure(body))
	}
	return shapeObservation{Shape: "json-body", Sample: body}
}

// inspectPlayoutCookie cannot use the shared rig: the planted fixture's playout
// is unprotected, so no handoff cookie is ever issued on it. It builds the same
// token-protected origin TestPlayoutCookieHandoffSurvives uses, which costs a
// server and no child process.
func inspectPlayoutCookie(t *testing.T, _ shapeRig) shapeObservation {
	t.Helper()
	_, h, _ := playoutOriginServer(t, enabledPlayout(true),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
	r := httptest.NewRequest(http.MethodGet,
		"/playout/master.m3u8?"+playoutTokenParam+"="+testToken, nil)
	r.RemoteAddr = "203.0.113.9:5555"
	w := do(t, h, r)
	got := w.Header().Get("Set-Cookie")
	if !strings.HasPrefix(got, playoutTokenCookie+"=") {
		t.Errorf("the Set-Cookie inspector drove the token handoff and got status %d with "+
			"Set-Cookie %q, which does not begin with %q. This shape is recorded as "+
			"emitted; if the handoff has stopped issuing a cookie, the row is what has "+
			"to change.", w.Code, got, playoutTokenCookie+"=")
	}
	return shapeObservation{Shape: "response-header/Set-Cookie", Sample: got}
}

// inspectStreamingManifest is the row #176 turned up as more than a rename.
//
// The old `by` string was "playoutManifestBytes", a counterpart proof that reads
// PlayoutPrefix+"master.m3u8" off the PLANTED server -- where the stream is
// token-protected and a read bearer is answered 50 bytes of
// {"error":"this stream requires a playback token"}. That proof is correct and
// its excuse's Want asserts exactly that 401; what it is not is an inspection of
// the streaming-media shape, because no manifest byte was ever in it. Naming a
// green proof was enough while nothing ran the name.
func inspectStreamingManifest(t *testing.T, _ shapeRig) shapeObservation {
	t.Helper()
	_, h, _ := playoutOriginServer(t, enabledPlayout(true),
		playoutPublish{Protection: PlayoutProtectToken, Token: testToken})
	r := httptest.NewRequest(http.MethodGet,
		"/playout/master.m3u8?"+playoutTokenParam+"="+testToken, nil)
	r.RemoteAddr = "203.0.113.9:5555"
	w := do(t, h, r)
	body := w.Body.String()
	if !strings.HasPrefix(body, "#EXTM3U") {
		t.Errorf("the streaming-media inspector drove the playout origin with a valid "+
			"playback token and got status %d with a body that does not start with "+
			"#EXTM3U, so it read no manifest. A sample that is merely non-empty would "+
			"have accepted the error page here -- which is precisely what the string "+
			"this row used to carry was accepting.\nbody: %s",
			w.Code, truncateForFailure(body))
	}
	return shapeObservation{Shape: "streaming-media", Sample: body}
}

// inspectWebsocketFrame reads real frames off a real upgrade on the SHARED rig.
//
// 750ms rather than the 2-3s the counterpart proofs use: the status, source and
// stats frames are written at subscribe time, so the window only has to cover
// the handshake. wsFrames burns whatever window it is given, so this number is
// the whole of this inspector's cost.
func inspectWebsocketFrame(t *testing.T, rig shapeRig) shapeObservation {
	t.Helper()
	frames := wsFrames(t, rig.s, "Bearer "+rig.read, 750*time.Millisecond)
	for _, f := range frames {
		var env struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(f), &env); err != nil || env.Type == "" {
			t.Errorf("the websocket-frame inspector read a frame that is not a typed event "+
				"envelope (%v): %s", err, truncateForFailure(f))
			break
		}
	}
	return shapeObservation{Shape: "websocket-frame", Sample: strings.Join(frames, "\n")}
}

// inspectOutboundHookBody and inspectOutboundAlertBody are the two
// principal-less egresses (#169), and they are the first inspectors here that
// read bytes this process SENT rather than bytes it served.
//
// That inverts the rig's usual direction and costs nothing extra to do: the
// far end is an httptest server in this process, so a delivery is one in-process
// round trip. Neither needs LiveTools -- no child is spawned and no FFmpeg
// stand-in is involved -- which is what makes them inspectable at all under
// #176's budget rather than deferred with the jurisdiction excuse the port-80
// listener has to use.
//
// Each asserts the property that DISCRIMINATES its shape, per the rule this
// section opens with. "Some bytes arrived at an endpoint I stood up" is exactly
// the substitution that let a 50-byte error page stand in for a manifest: an
// endpoint that 404s, a dispatcher that never loaded the hook, or an encoder
// that shipped an empty object would all deliver bytes. So the hook body must
// parse as a hook Envelope and carry the signature header, and the alert body
// must parse as an alert payload with at least one alert in it.
//
// SHARED-RIG ETIQUETTE. Both create a row -- a hook, an alert rule -- on the
// rig's planted server, which is the only mutation any inspector here makes.
// Nothing else in the registry reads hooks or alert rules, and the two rows the
// value sweep does read (settings, sources) are untouched. Stated because a
// shared fixture that inspectors mutate is a fixture whose inspectors have an
// order, and this one must not acquire a hidden one.
func inspectOutboundHookBody(t *testing.T, rig shapeRig) shapeObservation {
	t.Helper()
	endpoint, rec := egressEndpoint(t)
	const path = "/receiver/ledger-shape"
	created := createHook(t, rig.h, rig.sign, map[string]any{
		"name": "ledger-shape-egress", "url": endpoint.URL + path,
	})
	id := int64(created["id"].(float64))
	secret, _ := created["secret"].(string)

	d := hooks.NewDispatcher(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		hooks.SourceFunc(func() ([]hooks.Hook, error) {
			return []hooks.Hook{hooks.Hook{
				ID: id, Name: "ledger-shape-egress", Enabled: true,
				URL: endpoint.URL + path, Secret: secret,
				MaxAttempts: 1, TimeoutSeconds: 5,
			}.Normalized()}, nil
		}),
		hooks.WithReloadInterval(5*time.Millisecond),
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.Run(ctx)
	d.Publish(hooks.Event{
		Trigger: hooks.TriggerIngestPublished,
		Source:  hooks.SourceRef{ID: 1, Name: "Main"},
	})
	waitForEgress(t, rec, 1, "the hook endpoint (shape inspector)")

	body := rec.lastBody()
	var env struct {
		SpecVersion string `json:"specVersion"`
		ID          string `json:"id"`
		Trigger     string `json:"trigger"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil ||
		env.SpecVersion == "" || env.ID == "" || env.Trigger == "" {
		t.Errorf("the outbound-hook-body inspector read %d bytes off the receiver and they "+
			"are not a hook Envelope (%v): specVersion=%q id=%q trigger=%q.\n"+
			"An endpoint that answered 404, or a dispatcher that never loaded the hook, "+
			"would also have delivered bytes here. This row claims the ENVELOPE shape is "+
			"inspected, so the envelope is what has to be witnessed.\n%s",
			len(body), err, env.SpecVersion, env.ID, env.Trigger, truncateForFailure(body))
	}
	// The signature header is the other half of this egress and the reason the
	// hook's own secret can stay home. A delivery without it is a different
	// shape from the one this row describes.
	headers := rec.lastHeaders()
	if !strings.Contains(headers, hooks.SignatureHeader) {
		t.Errorf("the outbound hook delivery carried no %s header, so this inspector is "+
			"witnessing an unsigned egress and the row's note is wrong about what it "+
			"reads.\nheaders: %s", hooks.SignatureHeader, truncateForFailure(headers))
	}
	return shapeObservation{Shape: "outbound-hook-body", Sample: headers + body}
}

// inspectOutboundAlertBody witnesses the COALESCED delivery, which is the one
// every real alert takes.
//
// Not Notifier.Test: that builds one synthetic Item from a fixed sentence, and a
// fixed sentence cannot discriminate this shape from any other endpoint that
// echoes a rule name. The coalesced body is also the only one that can carry
// process state, which is #169's entire worry. Test's body is read by
// TestOutboundAlertPayloadCarriesNoStoredCredential instead.
//
// Flushed FORWARD rather than waited out. Rule.Debounce defaults a zero to ten
// seconds and the shortest the form allows is one; passing a future `now` to
// Flush -- the shipped test-and-shutdown entry point into one coalescing pass --
// makes the pending delivery due immediately. Polled rather than called once,
// because Publish hands the event to the Run goroutine and a single Flush can
// land before it has been coalesced. That is the whole of this inspector's
// wall clock: no sleep, and no budget that a slower platform can fail.
//
// A Notifier of its own, over the rig server's OWN store: the engine's alerter
// is built inside engine.New with no seam and its Run loop is not driven by this
// fixture -- measured, a published event never left. The rule under test is
// still a stored one, created through the real route above.
func inspectOutboundAlertBody(t *testing.T, rig shapeRig) shapeObservation {
	t.Helper()
	endpoint, rec := egressEndpoint(t)
	createRule(t, rig.h, rig.sign, map[string]any{
		"name": "ledger-shape-egress", "url": endpoint.URL + "/receiver/ledger-shape",
	})

	n := alerts.New(
		slog.New(slog.NewTextHandler(io.Discard, nil)), rig.s.store,
		alerts.WithFlushInterval(5*time.Millisecond),
		alerts.WithRetry(1, time.Millisecond, time.Millisecond),
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go n.Run(ctx)
	n.Publish(alerts.Event{
		Type: alerts.TypeDestinationDown, Severity: alerts.SeverityCritical,
		Key: "ledger-shape", Title: "twitch is down",
		Text: "the child exited: Broken pipe (Connection reset by peer)",
	})
	deadline := time.Now().Add(20 * time.Second)
	for rec.count() == 0 && time.Now().Before(deadline) {
		n.Flush(time.Now().Add(time.Hour))
		time.Sleep(2 * time.Millisecond)
	}
	waitForEgress(t, rec, 1, "the alert endpoint (shape inspector)")

	body := rec.lastBody()
	var payload struct {
		Source string `json:"source"`
		Rule   string `json:"rule"`
		Alerts []struct {
			Type  string `json:"type"`
			Title string `json:"title"`
			Count int    `json:"count"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil ||
		payload.Source == "" || len(payload.Alerts) == 0 || payload.Alerts[0].Type == "" {
		t.Errorf("the outbound-alert-body inspector read %d bytes off the receiver and they "+
			"are not an alert payload (%v): source=%q rule=%q alerts=%d.\n"+
			"A delivery that coalesced to nothing, or an endpoint answering something "+
			"else, would still have put bytes here. The payload with its alerts in it is "+
			"what this row claims is inspected.\n%s",
			len(body), err, payload.Source, payload.Rule, len(payload.Alerts),
			truncateForFailure(body))
	}
	return shapeObservation{Shape: "outbound-alert-body", Sample: rec.lastHeaders() + body}
}

// inspectSlogOutput reads the server's OWN structured log, by pointing it at a
// buffer and driving one request through the real mux.
//
// ITS OWN SERVER, NOT THE SHARED RIG'S, and that is the whole reason this
// inspector pays for a fixture the others do not. Server.log is a plain field
// and this reads by swapping it; the websocket inspector that runs before it
// leaves a HIJACKED connection being served on the shared rig, and
// requestLogger reads s.log when that request finally ends. Swapping the field
// underneath it is a data race on the shared Server -- reported by -race as
// route_ledger_test.go against api.go's requestLogger, intermittently, because
// it depends on when the socket closes. A server nobody else is holding is
// the fix; inspectProcessLog already builds its own for a different reason.
//
// The DISCRIMINATING property, per the rule this section opens with: the sample
// must be a slog text record -- level=, msg= and the method and path of the
// request that produced it. "Some bytes appeared in the buffer" would accept a
// panic trace, an empty write, or a log line from something else entirely.
func inspectSlogOutput(t *testing.T, _ shapeRig) shapeObservation {
	t.Helper()
	h, _, sign := plantedServer(t)
	s := serverUnderTest(t, h)
	read := createScopedToken(t, h, sign, "ledger-slog", db.ScopeRead)

	var buf bytes.Buffer
	s.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// NOT /health and NOT /stats: requestLogger drops those by design, and an
	// inspector that drove one of them would read an empty buffer and report
	// the shape absent.
	rawBody(t, h, bearer(read), "/api/v1/settings")

	got := buf.String()
	for _, want := range []string{"level=", "msg=", "/api/v1/settings"} {
		if !strings.Contains(got, want) {
			t.Errorf("the slog-output inspector drove GET /api/v1/settings with the server's "+
				"logger pointed at a buffer and the %d bytes it captured do not contain %q, "+
				"so what it witnessed is not this API's structured log.\n%s",
				len(got), want, truncateForFailure(got))
			break
		}
	}
	return shapeObservation{Shape: "slog-output", Sample: got}
}

// inspectProcessLog is the LiveTools one. The file only exists once a child has
// spawned and written to it, so there is no version of this that shares a rig
// with a fixture that starts no process.
func inspectProcessLog(t *testing.T, _ shapeRig) shapeObservation {
	t.Helper()
	h, _, _ := runningDestServer(t)
	return shapeObservation{
		Shape:  "on-disk-process-log",
		Sample: processLogFile(t, serverUnderTest(t, h)),
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
		func(method, route string, _ http.Handler, mws ...func(http.Handler) http.Handler) error {
			if route != "/" {
				route = strings.TrimSuffix(route, "/")
			}
			key := method + " " + route
			if seen[key] {
				return nil
			}
			seen[key] = true
			enumerated = append(enumerated, coverageRoute{
				Method: method, Pattern: route,
				// The same walk, so the zero-source verdict cannot be derived
				// from a different population than the coverage one.
				ZeroSource: zeroSourceWord(mws),
			})
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
			//
			// DENIED-BY-METHOD IS AN INVARIANT, and the word splits here for
			// that reason. The 403 above depends on nothing being in the
			// database: blank every planted credential and all 83 of these
			// pairs stay green, which is #168's problem on the write surface.
			// The pairs in nonGetDifferentialCensus are the ones where the same
			// request, driven as admin, was MEASURED to return a planted
			// credential -- so for those the 403 is withholding something
			// demonstrable rather than something assumed. The word is carried
			// into the artifact and compared by assertRouteSetsEqual on every
			// plain run, which is what makes deleting a census row visible here
			// as well as at nonGetDifferentialFloor.
			if nonGetDifferentialPairs()[key] {
				enumerated[i].Coverage = "denied-differential"
			} else {
				enumerated[i].Coverage = "denied-by-method"
			}
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

// TestLedgerPreflight is the whole ledger, and TestMain forces it through a
// second m.Run with the caller's -run, -skip and -count set aside, whatever the
// caller asked for and whatever their own pass reported -- see main_test.go.
// That is #161's jurisdiction problem solved for this package: `go test
// ./internal/api -run TestSomethingElse` no longer leaves every one of these
// obligations unchecked.
//
// It is the SECOND pass and not the first, since #217: the coverage profile is
// written on the way out of whichever m.Run returns first, so a preflight that
// ran first was the only thing `go test -cover ./internal/api` ever measured.
// The consequence to be honest about is that a failing ledger no longer stops
// the caller's tests from running -- by the time this runs, they have.
//
// Every failure message below names the route, the observed status, the byte
// count and the exact edit that fixes it. A preflight with bad messages is a
// preflight that gets deleted, and then all five issues return at once.
func TestLedgerPreflight(t *testing.T) {
	// ALREADY RUN, IN THIS PROCESS. TestMain drives this package twice: the
	// caller's own selection, then a forced ^TestLedgerPreflight$. An unfiltered
	// invocation therefore reaches this body in the caller's pass and again in
	// the forced one, and would otherwise re-drive 55 paths x 3 principals x 3
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
		t.Logf("the route coverage preflight already ran in this process, in the other " +
			"one of TestMain's two passes, with every assertion live. Recomputing it here " +
			"cannot reach a different verdict; see main_test.go.")
		return
	}

	// Whichever pass got here owns the computation, and it owns it even if the
	// computation fails: the flag is set on the way out including the Goexit a
	// t.Fatalf takes, so a red ledger is reported once rather than in both
	// passes. TestMain cannot set this itself -- after #217 it does not know
	// whether the caller's own -run and -skip selected this test.
	defer func() { ledgerPreflightDone = true }()

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

	// 1b. THE POPULATION IS DERIVED, NOT REMEMBERED. #156. Both halves run from
	// here as well as standing alone, for the two reasons measured on
	// ledger_ratchet_test.go: a file nothing references is deletable in silence,
	// and TestMain forces only ^TestLedgerPreflight$. The AST half needs no
	// server and the two drives share this one, so the preflight's wall clock
	// does not move.
	assertRegisteredPopulationEqualsWalk(t, s)
	assertNonTrieTerminalsAreWitnessed(t, s)
	TestHandlerRegistersOnlyThroughTheRecordedSeam(t)

	// 2. ENUMERATE AND CLASSIFY.
	enumerated, totals := classifyRoutes(t, h, s)
	assertSweptCounterpartsNameSweptRoutes(t, enumerated)
	assertCensusPairsAreClassified(t, enumerated)

	// 3. THE PARTITION. #165: every swept path gets a computed verdict, and
	// "swept" stops being one word covering two different claims.
	verdicts := sweepCensus(t, h, sign)
	part := countPartition(verdicts)
	assertEverySentinelIsWitnessed(t, h, sign)

	// 3b. THE NON-GET DIFFERENTIAL CENSUS. #157's residual: 83 non-GET pairs
	// are classified on an executed 403, which proves a read principal was
	// refused and proves nothing about whether anything was being withheld.
	// This drives the pairs measured to hand an ADMIN a planted credential from
	// the same request, so the denial next to it has a positive control.
	//
	// LAST among the drives that touch a fixture, and on a fixture of its own.
	// Two of its rows mutate -- a publish secret is rotated, the settings
	// document is re-saved -- and the record from the round that specified this
	// is explicit that such a census must not run against the shared fixture,
	// must run in sorted order, and must re-run assertEverySentinelIsWitnessed
	// at the end, or a destructive route poisoning the fixture is silent rather
	// than caught. All three are properties of assertNonGetDifferential, and
	// the third one is checked rather than promised: see its doc comment for
	// the row that was measured to break it.
	//
	// It is called HERE, before writeLedger, because the floor it feeds is
	// written by that call. Nothing above it re-reads the shared fixture.
	nonGetWitnesses := assertNonGetDifferential(t)

	// 3c. THE DECLARED-INVARIANCE COUNTER-EXPERIMENT. #157's other half, and
	// conjunct 2's second clause. The census above covers the pairs MEASURED to
	// disclose; this drives every remaining denied-by-method pair as ADMIN and
	// requires that no planted credential comes back -- so "invariant" is a
	// measurement taken on this build rather than a sentence in the artifact.
	// Its own fixtures, like the census, and for the same reason: the first run
	// of it destroyed the control's row with a DELETE. See
	// declared_invariance_test.go.
	assertDeclaredInvariance(t, enumerated)

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
		writeLedger(t, want, enumerated, totals, verdicts, part, nonGetWitnesses)
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
		if live := ledgerNote(t); want.Note != live {
			t.Errorf("the note in %s is not the one this build writes. It is the first "+
				"thing a reader trusts and nothing was comparing it.\ncommitted: %q\nlive: %q",
				coveragePath, want.Note, live)
		}
		assertProseSectionsEcho(t, want)
	}

	// 5. THE RATCHETS. Extracted so the readback meta-guard can re-run THE
	// ratchet comparison rather than a copy of it -- a guard watching a
	// restatement is blocker 2 of #150, and projection_guard_test.go refuses the
	// shape outright. See ledger_readback_test.go.
	assertLedgerRatchets(t, want, ledgerFacts{
		Excused:         totals.Excused,
		Part:            part,
		Verdicts:        verdicts,
		NonGetWitnesses: nonGetWitnesses,
		Guarded:         countGuardedRoutes(enumerated),
		RegenCommand:    ledgerRegenCommand(t),
	})

	// THE READBACK, conjunct 4, called from here for the two reasons every other
	// guard in this file is: `rm` on a file nothing references leaves the suite
	// green, and TestMain forces only ^TestLedgerPreflight$. It costs no server
	// and no request -- it perturbs the committed artifact against itself -- so
	// it is 80ms on a five-second preflight.
	//
	// NOT under -update-coverage: on a regenerating run the artifact is being
	// rewritten and the file on disk is the one from before the write.
	if !*updateCoverage {
		assertLedgerReadback(t)
	}

	// THE RATCHETS' OWN GUARD, called from here rather than left as a free-
	// standing test. Two reasons, both found by measurement: `rm
	// internal/api/ledger_ratchet_test.go` deleted 152 lines and left the whole
	// suite green because nothing referenced it, and the TestMain preflight
	// forces only ^TestLedgerPreflight$, so a guard outside it does not run in
	// the filtered invocation the preflight exists to survive. A compile-time
	// call fixes both.
	assertRatchetFieldsAreClamped(t)

	// The same two reasons, for the file that holds THIS ROUND's headline fix.
	// Measured: `rm internal/api/shape_reference_test.go` deletes 314 lines
	// containing both shape guards and leaves `POLYEMESIS_LEDGER=strict go test
	// ./internal/api` at ok. The round diagnosed the hatch for
	// ledger_ratchet_test.go and closed it, then wrote a new file for the
	// headline defect and left the same hatch open in it — which is the pattern
	// this whole exercise is about, appearing one file to the left.
	//
	// Calling the Test functions directly rather than extracting helpers: the
	// call IS the compile-time reference, and it is the smallest change that
	// buys both properties.
	TestEveryInspectedShapeWitnessesItself(t)
	TestBlankingEveryShapeNoteChangesNoVerdict(t)
	// #168's half one, and the same two reasons a third time. It parses this
	// package's own source and drives nothing, so it costs the preflight under a
	// tenth of a second -- and it is the only check in this ledger whose failure
	// means "a shape appeared that nobody wrote down", which is the sentence the
	// issue is made of. See shape_derivation_test.go.
	assertDerivedHeaderShapesAreRegistered(t)
	// #168's half two, and the same two reasons a fourth time. It censuses every
	// media-type literal in this package's source and joins it to the registry,
	// which is what found playout-poster; it also enforces the ACCOUNTING RULE
	// that every shape row is derived, out-of-package, or anchorless-with-a-
	// measured-reason, so the residual this issue is about cannot grow in
	// silence. Parses only; measured at 0.03s. See
	// shape_payload_derivation_test.go.
	assertDerivedPayloadShapesAreRegistered(t)
	// The same two reasons again, for the file that deletes issue-numbers-as-
	// discharge from the shape channel. The resolver shells out to `go test
	// -list` in three packages; measured, and the number is in the PR body,
	// because a preflight nobody can afford to run is a preflight somebody
	// deletes.
	TestDeletingEveryShapeIssueChangesNoVerdict(t)
	assertJurisdictionRecordsResolve(t)

	// #167's branch table, from here as well, and for the same two reasons: it
	// needs no server, so it costs the preflight nothing, and a guard that only
	// lives in TestTheNonTrieSurfacesAreDriven is one `-run` away from silence.
	// The precondition half stays there, because it needs the mux.
	assertNotFoundProbesEnterTheirBranches(t, s)

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
		// shapeVerdict, NOT a substring search over the note. The predicate
		// this replaces was Index(note,"Deferred:") >= 0 && Contains(note[i:],
		// "#"), under which "Deferred: #", "Deferred: #0" and "Deferred: the
		// Set-Cookie # header, which defers nothing" all discharged. The
		// deferral is now the Issue FIELD, which means it is also carried into
		// citedIssues() and therefore inherits the form check and the git
		// liveness scan that every other citation in this ledger already had.
		if shapeVerdict(sh) != "FAILS-THE-LEDGER" {
			continue
		}
		t.Errorf("the shape %q is emitted, is not inspected, and carries no jurisdiction "+
			"record. Give the row an Inspector -- a func this preflight CALLS, which is the "+
			"strong discharge -- or a `Jurisdiction: &shapeJurisdiction{Package: ..., "+
			"Test: ...}` naming where the assertion lives, which %s resolves with "+
			"`go test -list`.\n"+
			"AN ISSUE NUMBER IS NOT AN OPTION AND HAS NOT BEEN SINCE THE ROUND THAT WROTE "+
			"THIS MESSAGE. It was, for four shapes: shapeVerdict returned \"deferred\" on a "+
			"regex match against this field (%q), which put an external tracker inside a "+
			"verdict and made #169 uncloseable. Issue is a citation for a reader and is "+
			"read by nothing that decides anything.\n"+
			"The playout manifest was a streaming response and the argv leak travelled "+
			"through a WebSocket frame; both are SHAPES, not routes, and a ledger that "+
			"counted only routes called both covered.",
			sh.Shape, "shape_jurisdiction_test.go", sh.Issue)
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
	s := serverUnderTest(t, h)
	read := createScopedToken(t, h, sign, "nontrie", db.ScopeRead)

	// #167: WHICH BRANCH did the probes above actually drive. Both halves --
	// the two-column branch table and the precondition through this mux.
	t.Run("branches", func(t *testing.T) {
		assertNotFoundProbesEnterTheirBranches(t, s)
		assertNotFoundProbesMatchThisBuild(t, h)
	})

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
	// Shapes cite here too, which they did not until this round. Their
	// deferrals used to be free text inside Note, so they reached neither the
	// form check nor the git liveness scan -- the only citations in this ledger
	// that were checked by nothing. Reading the field puts them under both.
	for _, sh := range emittedShapes() {
		if sh.Issue != "" {
			set[sh.Issue] = true
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
func assertCitationsAreWellFormed(t ledgerReporter, want coverageLedger) {
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
		t.Fatalf("read %s: %v\nRun `%s` to create it.",
			coveragePath, err, ledgerRegenCommand(t))
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
	totals coverageTotals, verdicts []sweepVerdict, part partitionTotals,
	nonGetWitnesses int) {
	t.Helper()
	totals = fillDerivedTotals(totals, routes)
	shapes := emittedShapes()

	out := coverageLedger{
		Note:          ledgerNote(t),
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
	// The same mirror again, on the write surface. Deleting a row from
	// nonGetDifferentialCensus is the cheap way to make a failing positive
	// control go away, and max() is what stops the documented regeneration
	// command from banking it.
	out.NonGetDifferentialFloor = max(nonGetWitnesses, prev.NonGetDifferentialFloor)
	// The shape registry's two ratchets. Deleting a row lowers ShapeFloor and
	// max() refuses to bank it; downgrading a row to not-inspected raises
	// shapesNotInspected and min() refuses to bank that.
	out.ShapeFloor = max(totals.ShapesEmitted, prev.ShapeFloor)
	// The guard's floor, on the one evidence field in the route rows that had
	// none. Regeneration may bank a route BECOMING guarded and may not bank one
	// losing its guard. See the field's comment for the measured laundering.
	out.GuardedFloor = max(countGuardedRoutes(routes), prev.GuardedFloor)
	out.ShapesNotInspectedCeiling = min(totals.ShapesNotInspected, prev.ShapesNotInspectedCeiling)
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
func assertSweepVerdictsEqual(t ledgerReporter, want, got []sweepVerdict) {
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
		// THE NINTH INSTANCE, and it was found by the readback meta-guard on its
		// FIRST RUN rather than by a ninth human noticing. `pattern` is the chi
		// pattern the swept path resolved to; it is computed, committed, and was
		// read by nothing, so editing it in the artifact -- or a path silently
		// re-resolving to a different route after a registration change -- left
		// the whole suite green with the evidence pointing at the wrong route.
		// Exactly sentinels, pointers and inert, one field to the right.
		if wv.Pattern != gv.Pattern {
			t.Errorf("THE PATTERN A SWEPT PATH RESOLVES TO CHANGED. %s is recorded against "+
				"the chi pattern %q in %s and resolves to %q live. Every per-path verdict "+
				"below it is attributed to a route, and this is the field that says which "+
				"one; a path that starts matching a different pattern is a sweep whose "+
				"evidence is now filed under the wrong route.",
				path, wv.Pattern, coveragePath, gv.Pattern)
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
func assertProseSectionsEcho(t ledgerReporter, want coverageLedger) {
	t.Helper()
	assertKeyedRowsEqual(t, "excuses", want.Excuses, liveExcuses(),
		func(e coverageExcus) string { return e.Route })
	assertKeyedRowsEqual(t, "shapes", want.Shapes, emittedShapes(),
		func(s coverageShape) string { return s.Shape })
	assertKeyedRowsEqual(t, "deferred", want.Deferred, deferredWithReasons(),
		func(d coverageDefer) string { return d.ID })
}

func assertKeyedRowsEqual[T any](t ledgerReporter, section string, want, got []T, key func(T) string) {
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
func assertCoverageTotalsEqual(t ledgerReporter, want, got coverageTotals) {
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

func assertPartitionTotalsEqual(t ledgerReporter, want, got partitionTotals) {
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

func assertRouteSetsEqual(t ledgerReporter, want, got []coverageRoute) {
	t.Helper()
	// BOTH WORDS, and the second one is here rather than in a comparison of its
	// own for the reason this file keeps rediscovering: a field that is
	// measured, written and never compared is committed evidence nothing reads,
	// which has happened eight times. Carrying it in the same map means a route
	// that silently loses requireSource fails the same assertion a route that
	// silently loses its sweep does.
	index := func(rows []coverageRoute) map[string]string {
		m := map[string]string{}
		for _, r := range rows {
			m[r.Method+" "+r.Pattern] = r.Coverage + " / zero-source: " + r.ZeroSource
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

// ledgerNote is a FUNCTION rather than a const because the regeneration command
// it quotes is derived from the running test's name. The previous version was a
// const naming TestRouteCoverageLedger, which had been renamed to
// TestLedgerPreflight; the note stayed, and the command it published printed
// "[no tests to run]". Committed prose does not follow a rename. A t.Name() does.
func ledgerNote(t *testing.T) string {
	t.Helper()
	return ledgerNoteWith(ledgerRegenCommand(t))
}

// ledgerNoteWith is ledgerNote with the regeneration command supplied.
//
// The split exists because ledgerRegenCommand derives the command from the
// RUNNING test's name -- so a failure message never tells a reader to run a
// command that would regenerate nothing -- and the readback meta-guard has to
// reproduce the note the PREFLIGHT writes rather than the one its own name would
// produce.
func ledgerNoteWith(regen string) string {
	return "The route coverage ledger for internal/api. Regenerated ONLY by " +
		"`" + regen + "`, which " +
		"refreshes the route list and the derived totals and NOTHING else -- it cannot " +
		"suppress a missing counterpart, a failing positive control, or a raised ceiling. " +
		"Read `excuses` to answer \"what is NOT covered, and why is that safe\". " +
		"MinSecretLen is 8: the exact-literal scrub in internal/alerts refuses a shorter " +
		"credential, which is a real permanent residual on the stderr path for very short " +
		"secrets, covered only by the best-effort alerts.Redact pass. Every credential this " +
		"system mints or accepts is longer (SRT refuses a passphrase under 10, publish " +
		"tokens are 24), and the failure direction of the floor itself is over-masking, " +
		"never disclosure."
}

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
			ID: "G2",
			What: "chi.Walk is complete over the routing TRIE, and the trie is not the " +
				"MUX. The population is no longer the walk plus a hand-list: " +
				"Server.Handler() registers nothing itself and delegates to " +
				"registerRoutes(chi.Router), so recordRegistrations derives both terminals " +
				"-- r.NotFound from the call, method-not-allowed from the ABSENCE of one " +
				"-- and the probe slices are witnesses over that derivation rather than " +
				"the enumeration authority. What is left is the ROOT of the derivation: " +
				"it can only see registrations this process makes through " +
				"Server.Handler().",
			WhySafe: "the two derivations of the trie are required to AGREE (the walk over " +
				"the served mux, the recorder over the calls), a terminal with no witness " +
				"fails, a witness naming a terminal this build did not register fails, and " +
				"an AST guard refuses a registration made in Handler() where the recorder " +
				"cannot see it. rawResponse renders EVERY response header rather than a " +
				"hand-listed five, so a header that varies by principal on any swept or " +
				"probed route is a failure by name; the hand-listed set was measured to " +
				"miss an Authorization echo into X-Principal-Echo on an immutable, " +
				"publicly-cacheable asset response.",
			WhatWouldMakeItUnsafe: "a listener created OUTSIDE this router -- " +
				"cmd/polyemesis's port-80 ACME and redirect helper is the live example, " +
				"and it is #169 -- which no derivation rooted at Server.Handler() can " +
				"reach. Jurisdiction totality at the outermost boundary is a human review " +
				"and no mechanical repair for it is known. Also unsafe: the UI handler " +
				"reading server state, or varying by principal.",
		},
		{
			// THE MEASUREMENT THAT JUSTIFIED THE OLD ROW, kept because it is what
			// makes this one's scope legible: with internal/web/dist populated, a
			// Cache-Control that varies by principal FAILS the notFound probe by
			// name; with dist carrying only .gitkeep -- the state of a CI checkout
			// and of this repository -- the same mutation passed in 0.71s.
			// fs.WalkDir over web.FS() returns "." and ".gitkeep", so eight of the
			// nine probes took the "UI not built" branch and nothing said so.
			//
			// RETIRED, and replaced by a narrower residual rather than deleted
			// outright. The old row said the asset branch is entered by nothing
			// and that closing it "needs the UI built before the Go job runs,
			// which is a second CI build". It does not: web.HandlerFor takes the
			// filesystem, so both columns are driven by
			// assertNotFoundProbesEnterTheirBranches at no CI cost, and
			// assertNotFoundProbesMatchThisBuild pins which column the real mux
			// serves. What is left is smaller and worth stating on its own.
			ID: "built-ui-column-uses-a-synthetic-dist",
			What: "the SPA and asset branches are now driven THROUGH THE REAL MUX -- " +
				"Server.uiHandler is a seam, so registerRoutes mounts the chosen " +
				"filesystem under the same requestLogger and securityHeaders the binary " +
				"ships -- but the filesystem itself is SYNTHETIC: one index.html and one " +
				"fingerprinted bundle, not the output of `npm run build`.",
			WhySafe: "what the built column buys is BRANCH REACHABILITY, and every branch " +
				"internal/web can take is distinguishable from outside by status, " +
				"Content-Type and Cache-Control regardless of which bytes the file holds. " +
				"The two properties that could have been assumptions rather than " +
				"observations -- that no middleware rewrites the response on the way out, " +
				"and that the asset branch never consults the principal -- are now " +
				"observed, because the requests go through everything that is mounted " +
				"above the terminal.",
			WhatWouldMakeItUnsafe: "a real bundle whose LAYOUT differs from the synthetic " +
				"one in a way the handler branches on -- a nested assets directory, a " +
				"service worker at the root, a file the sub-FS refuses to open. Nothing " +
				"here can see that, and the honest fix for it is a CI job that builds the " +
				"UI before the Go job, which costs a four-minute npm install on every Go " +
				"run. NO ISSUE IS CITED: #167's two stated fix directions -- drive a build " +
				"with assets embedded, and assert the emptiness precondition so the " +
				"divergence is visible -- are both done and both done through the mux, so " +
				"carrying the number forward would be citing, as a reason, work that has " +
				"been carried out.",
		},
		{
			// #168'S RESIDUAL, NARROWED IN THE ROUND ITS SECOND HALF LANDED, and
			// the narrowing is the interesting part. The row this replaces said
			// the other eleven shapes were hand-written "because a header names
			// its own shape at the call site and a whole-payload shape has no
			// such literal". That reason was right about `Header().Set` and
			// wrong about the rows it deferred: six of the eleven DO name
			// themselves, in a media type, and a seventh named itself in a
			// websocket upgrade. A media-type census over every string literal
			// in this package derived those, and found an eighth shape --
			// playout-poster -- that no row had ever mentioned.
			//
			// It also stops being a LIST. The old row enumerated eleven names,
			// and nothing joined that enumeration to the registry: a twelfth
			// hand-written row would not have changed a character of it, which
			// is the same defect the shape list itself had. The residual is now
			// two buckets that assertEveryShapeRowIsAccountedFor checks every
			// row against.
			ID: "shape-derivation-cannot-reach-shapes-this-package-does-not-emit",
			What: "six shape rows are still hand-written. FIVE are emitted by another " +
				"package -- outbound-hook-body (internal/hooks), outbound-alert-body " +
				"(internal/alerts), on-disk-process-log (internal/supervisor), " +
				"mqtt-retained-topic and plain-http-listener (cmd/polyemesis) -- and the " +
				"derivations here read THIS package's directory. ONE, slog-output, is " +
				"emitted here and has no anchor: s.log.Log/Info/Warn/Error/Debug at 81 " +
				"sites in 18 files, so a scan for it returns \"emitted\" for every build " +
				"this package will ever have. Also still underived: the header names " +
				"net/http writes at this package's four http.ServeContent sites, which are " +
				"net/http's and appear in no source line here.",
			WhySafe: "a derivation whose answer cannot change is not a join, and that is " +
				"the criterion each of these six failed rather than a shortage of effort. " +
				"For the five out-of-package rows the alternative is an AST walk over " +
				"another package's source from a test in this one, which is the mechanism " +
				"#245 DELETED with the symbol index that resolved `By` strings; all five " +
				"are meanwhile discharged the way that deletion left intact -- an " +
				"Inspector the preflight calls, or a Jurisdiction record " +
				"assertJurisdictionRecordsResolve checks. What is NOT deferred any more is " +
				"the enumeration: every row must fall in one of four accounted buckets, so " +
				"a new hand-written shape fails instead of joining a list.",
			WhatWouldMakeItUnsafe: "a new egress from a package this one merely calls. The " +
				"two outbound bodies were exactly that -- principal-less sends absent from " +
				"the list until #169 went looking -- and no census of this package's " +
				"literals would find the next one either. Also unsafe: a media type this " +
				"package composes at runtime rather than spelling out, which the census " +
				"cannot see and which would narrow it back towards a list without " +
				"anything saying so.",
			Issue: "#168",
		},
		{
			ID: "run-filter-scan-is-limited-to-regeneration-commands",
			What: "TestEveryDocumentedRunFilterNamesALiveTest reads every .go file in " +
				"this package and the artifact, but only matches a -run whose line also " +
				"carries -update-coverage. A -run naming a dead test in any other kind " +
				"of sentence is not checked.",
			WhySafe: "the failure this catches is a maintainer copying a regeneration " +
				"command that silently regenerates nothing, and -update-coverage is what " +
				"makes a mention that instruction. The package contains at least one " +
				"genuine illustration (main_test.go's sentence about arbitrary -run " +
				"filters) that a wider pattern would report as a failure.",
			WhatWouldMakeItUnsafe: "a documented workflow other than regeneration that " +
				"tells a reader to run a named test. Nothing here can tell an " +
				"illustration from an instruction, and the version of this guard that " +
				"answered that by hand-picking two files missed the identical dead " +
				"string in a third.",
			Issue: "#168",
		},
		{
			// REWRITTEN from measurement. The old text said the non-GET pairs
			// were enumerated and classified but their response bodies were not
			// scanned, cited a test that no longer exists, and carried the
			// original count of 123. All three had gone stale: every non-GET
			// pair is driven with a read bearer today (83 by readScopeIsRefused,
			// 38 by driveExcuse, 2 by readScopeWriteSweep, which reads bytes),
			// and six of them are now driven as ADMIN as well with the planted
			// credential REQUIRED present. What is left is stated below, in the
			// terms the residual actually has.
			ID: "G3",
			What: "the non-GET pairs a read scope is refused split into two words, and " +
				"neither of them is now discharged by the 403 alone. denied-differential " +
				"is nonGetDifferentialCensus: driven at both privilege levels with the " +
				"planted credential REQUIRED present for the admin and the 403 required " +
				"beside it. denied-by-method is the DECLARED-INVARIANCE population, and " +
				"the counter-experiment for it is EXECUTED on every run rather than " +
				"summarised: every such pair is driven as admin with {}, and no planted " +
				"credential may come back. The population is derived from the live " +
				"classification, so a pair that changes word changes sweep on the same " +
				"run with no list to edit.",
			WhySafe: "a negative measurement is byte-identical to what a vacuous harness " +
				"produces, so the detector carries a POSITIVE CONTROL through the same " +
				"code path, run after EVERY drive: a pair the differential census has " +
				"independently measured to disclose must still be disclosing at that " +
				"moment, or the silence just recorded is not evidence. The bracket " +
				"version of that control -- once before, once after -- was written first " +
				"and went red immediately, because DELETE /api/v1/destinations/{id} is in " +
				"this population and removes the control's own row. The per-drive form " +
				"re-plants the fixture when that happens and requires the re-planted " +
				"control to hold, which separates \"the fixture was destroyed\" from " +
				"\"the detector is broken\".",
			WhatWouldMakeItUnsafe: "the pairs that never reach a handler are the honest " +
				"hole and they are REPORTED on every run rather than counted here: they " +
				"answer 4xx or 5xx because this fixture has no such row, the subsystem is " +
				"not running, or {} is not a payload they accept, so the counter-" +
				"experiment ran and could not read a body. A pair that discloses only " +
				"through a payload {} does not reach is invisible to this, and no " +
				"positive control can see it. Also unsafe: any non-GET handler that " +
				"begins echoing stored configuration back in a 2xx body, which is the " +
				"normal REST idiom and the shape PUT /api/v1/settings already has -- that " +
				"one IS caught, by this sweep, naming the pair and the credential.",
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
			What: "SEVEN OF THE NINE ARE GONE. plantRows now creates a row of each kind, " +
				"so clipper recordings, library recordings and sessions, alert rules, " +
				"hooks, schedules and renditions answer 200 and are value-swept rather " +
				"than excused; their bodies are read by three principals and scanned for " +
				"every planted sentinel, which is the coverage this row used to record as " +
				"missing. What is left is TWO routes that no row can drive. GET " +
				"/metadata/push/{id} reads an in-process registry entry created by a real " +
				"outbound platform call, whose snapshot moves while the push runs. GET " +
				"/playout/poster.jpg renders a JPEG out of an MPEG-TS segment, and there " +
				"is no segment on disk without running FFmpeg in a unit test. Both keep a " +
				"driven excuse. Separately, /hooks/1/deliveries stays INERT: the row " +
				"exists now, but a delivery record does not, and manufacturing one means " +
				"an outbound HTTP attempt.",
			WhySafe: "the 404 was already an assertion and still is for the two that " +
				"remain: if either starts answering a read token with a body, the excuse " +
				"drive fails naming the route and the observed status. For the seven that " +
				"left, the claim is no longer a 404 at all -- it is the ordinary sweep, " +
				"which is strictly stronger, and two of them (the alert rule and the " +
				"hook) carry a planted webhook secret in the URL path so that " +
				"alerts.RedactWebhookURL is under the sweep rather than merely under " +
				"review.",
			WhatWouldMakeItUnsafe: "a fixture row being deleted. That is not silent " +
				"either: leakRoutes() requires a 200 from every path it names, so losing " +
				"a row fails by name rather than quietly reverting these routes to " +
				"unread. The residual cost is the two routes above, whose leaf fields are " +
				"still traced by reading the handler rather than by reading bytes.",
			Issue: "#163",
		},
		{
			ID: "counterpart-proofs-outside-the-preflight",
			What: "the two counterpart proofs that spawn a real FFmpeg stand-in -- " +
				"runningDestinationLogs and websocketFrames -- run as ordinary tests and " +
				"are therefore defeatable by `go test -run`, unlike everything else the " +
				"ledger asserts. #176's shape inspectors join them: inspectProcessLog is " +
				"marked LiveTools because the on-disk process.log only exists once a child " +
				"has spawned and written, so it runs in strict mode with them rather than " +
				"on every invocation. The other four inspectors run unconditionally in the " +
				"preflight on the shared planted rig.",
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
			ID: "ratchet-raises-rest-on-ordinary-review",
			What: "every ratchet in this file -- excusedCeiling, " +
				"counterpartlessExcusedCeiling, differentialFloor, sentinelWitnessFloor, " +
				"unstableCeiling, inertCeiling, varianceExemptCeiling, shapeFloor, " +
				"shapesNotInspectedCeiling, and citedIssues " +
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

// rawResponse renders status, EVERY response header, and the body into one
// comparable string, so "byte-identical for two principals" is an assertion
// about the whole response rather than about its body.
//
// It used to render a HAND-LISTED five headers -- Content-Type, Location,
// Set-Cookie, Cache-Control, Allow -- which is this ledger's own thesis (a
// guard complete over a set that excludes something) sitting inside the proof
// cited as bringing the NotFound surface into frame. Measured: with
// internal/web/dist populated, echoing the caller's bearer token into
// `X-Principal-Echo` on an immutable, publicly-cacheable asset response passed
// the entire suite. That is the same species as the cacheable 301 carrying a
// watch token in Location that this project has already shipped once.
//
// Enumerating the map instead of a list is smaller AND complete, and it is
// direction-safe: a header that legitimately varies by principal now has to be
// argued for in nonCredentialVariance rather than omitted silently. The keys
// are sorted because http.Header is a map and an unsorted render would make
// this comparison order-dependent -- a flake that reads as a leak.
func rawResponse(t *testing.T, h http.Handler, sign func(*http.Request), method, path string) string {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	if sign != nil {
		sign(r)
	}
	w := do(t, h, r)
	var b strings.Builder
	b.WriteString(strconv.Itoa(w.Code))
	keys := make([]string, 0, len(w.Header()))
	for k := range w.Header() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range w.Header().Values(k) {
			b.WriteString("|" + k + ": " + v)
		}
	}
	b.WriteString("|" + w.Body.String())
	return b.String()
}

// ledgerFacts is everything the ratchet comparison needs that is MEASURED
// rather than read out of the artifact.
//
// It exists so that assertLedgerRatchets has exactly two inputs -- the committed
// ledger and the live measurement -- and can therefore be re-run by the readback
// meta-guard with the committed values standing in for the live ones. That is
// what makes the readback a re-run of the real rule instead of a copy of it.
type ledgerFacts struct {
	Excused         int
	Part            partitionTotals
	Verdicts        []sweepVerdict
	NonGetWitnesses int
	// Guarded is the live count of pairs whose middleware chain carries
	// requireSource, derived from the same walk the population comes from.
	Guarded int
	// RegenCommand is passed in rather than derived, because deriving it needs a
	// *testing.T and this function takes a reporter.
	RegenCommand string
}

// assertLedgerRatchets is step 5, with the preflight and the readback as its two
// callers.
func assertLedgerRatchets(t ledgerReporter, want coverageLedger, f ledgerFacts) {
	t.Helper()
	// 5. THE RATCHETS. Ceilings clamp DOWN with min() and the floor clamps UP
	// with max(); neither is ever an assignment. The ratchet bug this PR's
	// predecessor shipped was exactly an assignment where a clamp belonged.
	if f.Excused > want.ExcusedCeiling {
		t.Errorf("%d routes are excused and the committed ceiling is %d. Going UP requires "+
			"editing excusedCeiling in %s by hand. Going down is free -- regenerate and "+
			"the ceiling comes with it.", f.Excused, want.ExcusedCeiling, coveragePath)
	}
	if n := counterpartlessExcused(); n > want.CounterpartlessExcusedCeiling {
		t.Errorf("%d excuses discharge on a driven Want alone, with no counterpart proof "+
			"reading any bytes, and the committed ceiling is %d. A Want is a real "+
			"assertion and a weaker one than a proof; raising this is a hand edit.",
			n, want.CounterpartlessExcusedCeiling)
	}
	if f.Part.Differential < want.DifferentialFloor {
		t.Errorf("THE POSITIVE CONTROL FELL. %d swept paths carry a planted credential in "+
			"the high-privilege body, and the committed floor is %d. This is #165's "+
			"decisive assertion: blank a fixture credential and the route it covered "+
			"stops being a differential, so the sweep over it becomes a statement about "+
			"an empty body -- which is exactly what "+
			"TestReadScopedTokenCannotReadAPublishToken was. A floor going DOWN is never "+
			"free; it is a hand edit of differentialFloor in %s.",
			f.Part.Differential, want.DifferentialFloor, coveragePath)
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
	if n := countSentinelWitnesses(f.Verdicts); n < want.SentinelWitnessFloor {
		t.Errorf("A PER-ROUTE CREDENTIAL WITNESS DISAPPEARED. %d (path, sentinel) pairs are "+
			"witnessed in a high-privilege body across the sweep, and the committed floor "+
			"is %d. The route still being a differential is NOT enough: a route that "+
			"carries four planted credentials and starts carrying three has stopped "+
			"proving anything about the fourth, while its class, its floor and its "+
			"absence assertion all stay green. The message above from "+
			"assertSweepVerdictsEqual names which path lost which sentinel.\n"+
			"per-path witnesses: %v",
			n, want.SentinelWitnessFloor, sentinelWitnessesByPath(f.Verdicts))
	}
	// THE WRITE SURFACE'S POSITIVE CONTROL, and the number that did not exist
	// before this round. Everything above it is about routes a read token can
	// GET. The 83 non-GET pairs classified on an executed 403 had no floor at
	// all, because "was refused" costs nothing to keep true: an empty database
	// answers 403 exactly as readily as a full one.
	if f.NonGetWitnesses < want.NonGetDifferentialFloor {
		t.Errorf("THE NON-GET POSITIVE CONTROL FELL. %d (pair, sentinel) witnesses were "+
			"observed across the write-surface census and the committed floor is %d. A "+
			"pair in that census is recorded as denied-differential rather than "+
			"denied-by-method, which is a claim that its 403 withholds a credential an "+
			"admin demonstrably receives from the same request. Below the floor, some "+
			"pair's 403 is now withholding nothing that this package can show. The "+
			"message naming the pair and the sentinel is above; lowering this is a hand "+
			"edit of nonGetDifferentialFloor in %s.",
			f.NonGetWitnesses, want.NonGetDifferentialFloor, coveragePath)
	}
	if f.Part.Unstable > want.UnstableCeiling {
		t.Errorf("%d swept paths return different bytes to the same principal on two "+
			"consecutive samples, and the committed ceiling is %d.",
			f.Part.Unstable, want.UnstableCeiling)
	}
	// INERT is a ceiling for the same reason unstable is. An inert row is a
	// sweep reading "[]" or "{}": it costs three requests and asserts absence
	// over nothing. Eleven of the 43 invariants are in that state today. The
	// number was computed and committed and nothing compared it, so a route
	// whose list quietly emptied moved into the inert count in silence -- which
	// is a sweep going vacuous by exactly the mechanism this file is named for.
	if f.Part.Inert > want.InertCeiling {
		t.Errorf("%d swept paths return a body that is entirely \"[]\" or \"{}\", so the "+
			"sweep over them scans nothing, and the committed ceiling is %d. A route that "+
			"has just become inert is a route whose fixture stopped producing rows: find "+
			"it in the sweepVerdicts diff (inert: true) and either give it a row or raise "+
			"inertCeiling in %s by hand.", f.Part.Inert, want.InertCeiling, coveragePath)
	}
	if len(nonCredentialVariance) > want.VarianceExemptCeiling {
		t.Errorf("%d (pattern, pointer) pairs are exempted from the variance rule and the "+
			"committed ceiling is %d.", len(nonCredentialVariance), want.VarianceExemptCeiling)
	}
	// THE SHAPE RATCHETS. Everything above this line is about routes, which is
	// exactly why deleting a shape row and regenerating was green.
	liveShapes := fillDerivedTotals(coverageTotals{}, nil)
	if liveShapes.ShapesEmitted < want.ShapeFloor {
		t.Errorf("A SHAPE ROW DISAPPEARED. This API is recorded as emitting %d shapes and "+
			"the committed floor is %d. Deleting the row is the documented response to a "+
			"shape check firing, and until this floor existed `%s` banked the deletion: "+
			"shapesEmitted moved inside 2000 lines of JSON and the strict suite stayed "+
			"green. A shape that genuinely stopped being emitted is a hand edit of "+
			"shapeFloor in %s, and the sentence somebody has to write is \"this API no "+
			"longer produces this kind of output\".",
			liveShapes.ShapesEmitted, want.ShapeFloor, f.RegenCommand, coveragePath)
	}
	// THE GUARD'S FLOOR. Every other clamp in this function is about what the
	// sweep can see; this one is about what the API refuses.
	if f.Guarded < want.GuardedFloor {
		t.Errorf("A ZERO-SOURCE GUARD DISAPPEARED. %d registered pairs carry requireSource "+
			"and the committed floor is %d. The route-row mismatch above names WHICH pair, "+
			"and its message -- like every other mismatch in this file -- reads as though "+
			"regenerating were the remedy. It is not, here: the word this artifact carries "+
			"for that pair is the difference between a refusal and a nil dereference, and "+
			"`%s` would commit the dereference as the new truth. A route that genuinely no "+
			"longer needs a programme is a hand edit of guardedFloor in %s, and the "+
			"sentence is \"this route can act on an install with no source\".",
			f.Guarded, want.GuardedFloor, f.RegenCommand, coveragePath)
	}
	if liveShapes.ShapesNotInspected > want.ShapesNotInspectedCeiling {
		t.Errorf("%d emitted shapes are inspected by nothing and the committed ceiling is "+
			"%d. Downgrading a shape to Inspected:false is sometimes the honest move -- "+
			"two rows in this registry were downgraded because their proof lives in "+
			"package main -- but it is a LOOSENING, and regeneration may not bank it. "+
			"Raise shapesNotInspectedCeiling in %s by hand.",
			liveShapes.ShapesNotInspected, want.ShapesNotInspectedCeiling, coveragePath)
	}
}
