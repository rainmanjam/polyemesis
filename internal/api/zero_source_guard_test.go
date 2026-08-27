package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/clips"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// The zero-source BOUNDARY GUARD, driven through the real router.
//
// PR 2 made the reads answer. This is the other half: the routes that act on a
// pipeline cannot answer, because there is no pipeline, and every one of them
// dereferenced a nil *engine.Engine to find that out. What they do instead is
// refuse -- 503, with a machine-readable code, before the handler runs.
//
// THE POPULATION IS DERIVED, not listed. guardedPairs walks the router this
// build serves and reads requireSource out of the middleware chain, so a route
// added to the guard list is tested the day it is added and a route that has
// its guard removed leaves this population rather than failing inside it.
//
// That last sentence is the vacuity this file has to answer for, and the answer
// is not here: it is the committed artifact. testdata/route-coverage.json
// carries the zeroSource word for every registered pair, and assertRouteSetsEqual
// compares it, so a guard that quietly disappears fails TestLedgerPreflight by
// name and appears in the diff. A derived population makes this test correct;
// the artifact is what makes it hard to empty.

// guardedPairs is every (method, pattern) the router serves behind
// requireSource, in the ledger's own derivation.
func guardedPairs(t *testing.T, s *Server) []coverageRoute {
	t.Helper()
	enumerated, _ := enumerateRoutes(t, s)
	var out []coverageRoute
	for _, r := range enumerated {
		if r.ZeroSource == "guarded" {
			out = append(out, r)
		}
	}
	// A walk that finds nothing reports every claim below as met. The floor is
	// deliberately not the exact count -- that is the artifact's job, where
	// changing it is a reviewable edit -- but a population that collapses to a
	// handful means the derivation broke, not that the API did.
	if len(out) < 15 {
		t.Fatalf("the derivation found %d guarded pairs. Every assertion in this file is "+
			"universally quantified over that set, so a set this small is the walk having "+
			"gone blind rather than the guard having shrunk.", len(out))
	}
	return out
}

// noSourceRefusalSites is every function in this package that can answer the
// zero-source refusal, and WHY it is not the middleware.
//
// The route ledger's zeroSource word is derived from the middleware chain, so it
// sees requireSource and nothing else. That is exactly right for what it claims
// and it is not the whole of the behaviour: four other functions here can put
// the same 503 with the same code on the wire, and a route that refuses through
// one of them is recorded `unguarded` -- indistinguishable, in the artifact,
// from GET /setup, which genuinely must answer. Adding a third word was the
// obvious repair and is refused: a word in that vocabulary is DRIVEN, and none
// of these refusals can be driven through the router today (see each entry).
//
// So the instrument is this list instead. It is a Go map rather than a number in
// the committed JSON on purpose -- there is no `-update-coverage` that can
// regenerate it, and adding an entry is a sentence somebody writes on purpose.
// A new helper refusal fails this test until its route's zero-source behaviour
// is stated, which is the obligation the ledger's derived population cannot
// impose.
var noSourceRefusalSites = map[string]string{
	"requireSource": "the middleware. This is the one the ledger's zeroSource word " +
		"IS, and every pair carrying it is driven by the test below.",
	"writeCreateError": "POST /destinations and POST /renditions, both of which " +
		"carry requireSource as well. Reached only when the install loses its " +
		"sources between the middleware and the store, which is why it is driven " +
		"at the function rather than through the router.",
	"refuseIfSilent": "POST /destinations, likewise guarded, likewise a SECOND read " +
		"of the engine set. Driven at the function by " +
		"TestTheSilenceCheckRefusesRatherThanReadingAnAbsentIngest.",
	"destinationBaseArgv": "the three expert routes that write nothing -- GET " +
		"/destinations/{id}/expert and the preview and dry-run POSTs -- which carry " +
		"no requireSource by design. Driven at the helper by " +
		"TestResolvingAnExpertCommandWithNoEngineRefusesRatherThanPanicking, and " +
		"THROUGH THE ROUTER by " +
		"TestEveryRegisteredRouteSurvivesEnginesThatDidNotStart. This entry used to " +
		"say the router route was undriveable because source_id CASCADEs -- true of " +
		"an install with no ROWS, and the wrong reading of what nil means: eng() is " +
		"nil whenever no engine is running, which includes an install whose rows are " +
		"all present and whose engines did not come up. On a fresh install those " +
		"three requests do 404 at the store first, which is why the walk needs the " +
		"second fixture to reach them at all.",
	"writeExpertCommandError": "the lift for the entry above, at the five " +
		"resolveExpertCommand call sites. Same test.",
	"handlePutSettings": "PUT /settings, which carries NO requireSource and must " +
		"not: the same document holds the listeners, recording, chat, automod and " +
		"alerts, all of which an operator configures before creating a source. It " +
		"refuses for ONE field -- an ingest block changed on an install with nowhere " +
		"to write it through to -- and saves everything else in the request first. " +
		"Driven through the router by " +
		"TestChangingTheIngestWithNoSourceIsRefusedRatherThanSavedIntoNothing.",
	"handleTestAlertRule": "POST /alerts/rules/{id}/test, which carries no " +
		"requireSource because creating, editing and deleting a rule are all " +
		"install-wide and work before the first source exists. Only the SEND needs " +
		"a notifier, and Engine.Alerts() answers nil for exactly one reason -- there " +
		"is no engine -- so the 503 it used to write (\"the alert notifier is not " +
		"running\") was this refusal under a name that sent the operator looking for " +
		"a subsystem to restart. Driven through the router by " +
		"TestEveryRegisteredRouteSurvivesEnginesThatDidNotStart, which is where it " +
		"was found: on a fresh install there is no rule to test and the route 404s " +
		"before reaching it.",
}

// TestEveryNoSourceRefusalIsAGuardOrIsRecorded reads the package's own source.
//
// Source-derived rather than listed twice: the set of functions that can emit
// this refusal is a property of the code, and a hand-list of it would be
// complete on the day it was written -- the failure the route ledger next door
// is a monument to.
func TestEveryNoSourceRefusalIsAGuardOrIsRecorded(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse the api package: %v", err)
	}
	pkg, ok := pkgs["api"]
	if !ok {
		t.Fatal("no `api` package parsed from this directory, so the scan below would " +
			"report zero refusal sites and pass having read nothing")
	}

	found := map[string]bool{}
	for _, f := range pkg.Files {
		for _, d := range f.Decls {
			fn, isFunc := d.(*ast.FuncDecl)
			if !isFunc || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				id, isIdent := n.(*ast.Ident)
				if isIdent && (id.Name == "writeNoSource" || id.Name == "errNoSource") {
					found[fn.Name.Name] = true
				}
				return true
			})
		}
	}
	// A scan that finds nothing agrees with any list at all.
	if len(found) == 0 {
		t.Fatal("the scan found no function emitting the no-source refusal, in the " +
			"package that defines it. The identifiers were renamed and this test went " +
			"vacuous rather than red.")
	}

	for name := range found {
		if _, recorded := noSourceRefusalSites[name]; !recorded {
			t.Errorf("%s can answer the zero-source refusal and is not in "+
				"noSourceRefusalSites. Every route it refuses for is recorded `unguarded` "+
				"in testdata/route-coverage.json -- the same word GET /api/v1/setup "+
				"carries, and that route must ANSWER -- so nothing derived from that "+
				"artifact will notice a 503 arriving on a screen an operator recovers "+
				"through. Add an entry saying which routes it can refuse for and how "+
				"those are driven at zero source; if the answer is \"they are guarded\", "+
				"then this function is defence in depth and the entry says so.", name)
		}
	}
	for name := range noSourceRefusalSites {
		if !found[name] {
			t.Errorf("noSourceRefusalSites records %s and nothing in this package's source "+
				"emits the refusal from it. A stale entry is a reader being told a route "+
				"is covered by a branch that is gone.", name)
		}
	}
}

// TestEveryGuardedRouteRefusesOnAnInstallWithNoSource is the guard itself.
//
// Not a status alone: the CODE is asserted too, because that is what the
// dashboard branches on. A 503 whose body says only "reconcile failed" is
// indistinguishable, to a client, from the server being broken -- and the
// difference between "this install has nothing yet" and "something is wrong" is
// the whole of what a first-time operator needs from this screen.
func TestEveryGuardedRouteRefusesOnAnInstallWithNoSource(t *testing.T) {
	s, h, auth := zeroSourceServer(t)

	for _, pair := range guardedPairs(t, s) {
		path := concretePath(pair.Pattern)
		t.Run(pair.Method+" "+pair.Pattern, func(t *testing.T) {
			r := jsonRequest(t, pair.Method, path, map[string]any{})
			auth(r)
			w := do(t, h, r)
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s %s returned %d on an install with no source, want 503. A 500 "+
					"here is the nil dereference this guard exists to prevent; a 2xx is a "+
					"success report for something that did not happen.\nbody: %s",
					pair.Method, path, w.Code, w.Body.String())
			}
			var body apiError
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode the refusal: %v: %s", err, w.Body.String())
			}
			if body.Code != codeNoSource {
				t.Fatalf("%s %s refused with code %q, want %q. Without it the UI is back to "+
					"matching on the English sentence.\nbody: %s",
					pair.Method, path, body.Code, codeNoSource, w.Body.String())
			}
			// The sentence has one job beyond being a sentence: saying where to
			// go next. An operator meeting this has no reason to know what a
			// source is yet.
			if !strings.Contains(body.Error, "Sources page") {
				t.Fatalf("%s %s refused without naming the screen that ends this state: %q",
					pair.Method, path, body.Error)
			}
		})
	}
}

// TestNoGuardedRouteRefusesOnceASourceExists is the DIFFERENTIAL, and without
// it the test above is discharged by a middleware that refuses everything.
//
// It drives the same derived population against a fixture that has an engine
// and requires that NOTHING answers the zero-source refusal. What each route
// answers instead is deliberately not asserted: half of them 404 because this
// fixture has no destination with that id, and pinning those statuses here
// would make this a test of the fixture. The claim is the one that can be
// falsified by the guard being wrong -- that the refusal is a statement about
// the install and not a permanent property of the route.
func TestNoGuardedRouteRefusesOnceASourceExists(t *testing.T) {
	h, _, auth := renditionServer(t, defaultTools())
	s := serverUnderTest(t, h)
	if s.eng() == nil {
		t.Fatal("the fixture has no engine, so this differential compares two refusals")
	}

	for _, pair := range guardedPairs(t, s) {
		path := concretePath(pair.Pattern)
		t.Run(pair.Method+" "+pair.Pattern, func(t *testing.T) {
			r := jsonRequest(t, pair.Method, path, map[string]any{})
			auth(r)
			w := do(t, h, r)
			var body apiError
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if body.Code == codeNoSource {
				t.Fatalf("%s %s answered %q on an install that HAS a source, so the guard is "+
					"not reading the engine set: %d %s",
					pair.Method, path, codeNoSource, w.Code, w.Body.String())
			}
		})
	}
}

// The clip DELETE is the one that was first decided the other way, and this is
// the consequence that decided it back.
//
// Guarding it left an install with no source able to list clips and download
// them and never remove them. That is not a stalemate that resolves itself: the
// clips directory is install-wide and outlives every programme, so once the
// last-source delete becomes possible a box with a full one would have had no
// API that could clear it -- while DELETE /recordings/{id}, three routes up and
// over material that outlives its source in exactly the same way, has always
// answered. An operator whose disk is full and whose only remedy is a shell is
// worse off than one who never had the button.
//
// What the guard was protecting is kept: with a capturer running the engine
// still owns the delete, because it owns the index the listing is built from.
// This drives the OTHER side, where there is no capturer to race.
func TestAClipOnDiskCanBeDeletedOnAnInstallWithNoSource(t *testing.T) {
	s, h, auth := zeroSourceServer(t)

	if err := os.MkdirAll(s.clipDir(), 0o755); err != nil {
		t.Fatalf("create the clips directory: %v", err)
	}
	name := clips.Prefix + "20260301-201500" + clips.Ext
	path := filepath.Join(s.clipDir(), name)
	if err := os.WriteFile(path, []byte("a clip that outlived its programme"), 0o644); err != nil {
		t.Fatalf("plant a clip: %v", err)
	}

	r := jsonRequest(t, http.MethodDelete, "/api/v1/clips/"+name, nil)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE the clip returned %d on an install with no source. A 503 here is "+
			"a listing an operator can see and cannot act on, and a disk that only "+
			"fills: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the route answered 200 and the file is still on disk (%v), which is the "+
			"success report for something that did not happen that the guard exists to "+
			"prevent -- arriving from the other side", err)
	}
}

// And the confinement, which the fallback must still go through.
//
// Same shape as the download's, and the same limit on what it proves:
// clips.Resolve refuses a name that is not a clip name BEFORE it joins
// anything, so this passes against any base including "". That the base is a
// REAL directory is what the test above pins, by removing a file it planted
// under recordings/clips. The pair is why this is a re-plumb and not a nil-safe
// accessor -- see MUST NOT #6.
func TestTheClipDeleteStillRefusesToEscapeItsDirectoryWithNoSource(t *testing.T) {
	_, h, auth := zeroSourceServer(t)

	for _, name := range []string{"..%2f..%2fpolyemesis.db", "polyemesis.db"} {
		r := jsonRequest(t, http.MethodDelete, "/api/v1/clips/"+name, nil)
		auth(r)
		if w := do(t, h, r); w.Code == http.StatusOK {
			t.Fatalf("the delete answered 200 for %q, so the name was confined against "+
				"nothing: %s", name, w.Body.String())
		}
	}
}

// The three expert routes that write nothing carry no requireSource: their
// refusal comes from destinationBaseArgv, which is where the engine was
// actually needed.
//
// Exercised at the helper rather than through the router, and the reason is
// worth recording because it took a failing test to find it. On an install with
// no source there is no destination to ask about EITHER: source_id CASCADEs, so
// every row went with the programme, and a row left holding NULL is refused by
// scanDestination ("belongs to no programme") long before any command is
// resolved. So the HTTP path answers about the destination, correctly, and this
// branch is defence in depth for the state where an engine goes away underneath
// a request that has already found its row.
func TestResolvingAnExpertCommandWithNoEngineRefusesRatherThanPanicking(t *testing.T) {
	s, _, _ := zeroSourceServer(t)

	_, _, _, _, err := s.destinationBaseArgv(&db.Destination{ID: 1, Kind: db.DestRTMP})
	if !errors.Is(err, errNoSource) {
		t.Fatalf("destinationBaseArgv on an install with no engine returned %v, want the "+
			"no-source refusal. Reaching Engine.Processes here is the nil dereference.", err)
	}

	// And the mapping the five callers share: this one error becomes the
	// install's 503, while everything else destinationBaseArgv can fail with
	// stays the destination's 409.
	w := httptest.NewRecorder()
	writeExpertCommandError(w, err)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503. A 409 sends the operator to the routing editor "+
			"of a destination that is fine: %s", w.Code, w.Body.String())
	}
	var body apiError
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode the refusal: %v: %s", err, w.Body.String())
	}
	if body.Code != codeNoSource {
		t.Fatalf("code = %q, want %q: %s", body.Code, codeNoSource, w.Body.String())
	}

	w = httptest.NewRecorder()
	writeExpertCommandError(w, errors.New("this destination's routing profile does not compile"))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a destination that genuinely cannot be built: %s",
			w.Code, w.Body.String())
	}
}

// refuseIfSilent is the other helper that reads the engine set a second time,
// and it answers the same sentence rather than proceeding.
//
// Directly, for the reason the expert helper is: POST /destinations carries
// requireSource, so the only way here with no engine is the window between the
// two reads. What it must not do is what it did before -- dereference -- and
// what it must not do instead is shrug and let the create through, because the
// reconcile that follows would have nothing to reconcile onto.
func TestTheSilenceCheckRefusesRatherThanReadingAnAbsentIngest(t *testing.T) {
	s, _, _ := zeroSourceServer(t)

	w := httptest.NewRecorder()
	// A nil programme, which is what a body with no sourceId carries. The
	// install-wide check comes first and answers here whatever was named, which
	// is the property this test is about.
	if !s.refuseIfSilent(w, nil, routing.Profile{}) {
		t.Fatal("refuseIfSilent let a destination through on an install with no engine, " +
			"having written nothing")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body.String())
	}
	var body apiError
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v: %s", err, w.Body.String())
	}
	if body.Code != codeNoSource {
		t.Fatalf("code = %q, want %q: %s", body.Code, codeNoSource, w.Body.String())
	}
}

// writeCreateError's lift, exercised directly.
//
// Directly, because the two routes it serves are themselves guarded, so the
// only way to reach it through HTTP is an install whose sources vanish between
// the middleware and the store -- a state a test cannot produce and a server
// really can. That makes this the belt to the guard's braces, and an untested
// belt is the one that turns out to have been cut.
func TestACreateThatCannotResolveASourceIsNotABadRequest(t *testing.T) {
	t.Run("no source", func(t *testing.T) {
		w := httptest.NewRecorder()
		// WRAPPED, exactly as db.CreateDestination returns it. A helper that
		// only recognises the bare sentinel would pass a test written against
		// the bare sentinel and fail in production.
		writeCreateError(w, fmt.Errorf("resolve default source: %w", db.ErrSourceNotFound))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503: %s", w.Code, w.Body.String())
		}
		var body apiError
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v: %s", err, w.Body.String())
		}
		if body.Code != codeNoSource {
			t.Fatalf("code = %q, want %q", body.Code, codeNoSource)
		}
	})
	// AND THE PLACE THE LIFT MUST NOT REACH. db.ErrSourceNotFound means two
	// things -- "no row with this id" and "this install has no source" -- and
	// only the creates can be sure which. Every caller of writeStoreError asked
	// for a row by id, so a 503 there tells an install with four sources that it
	// has none, and the UI draws the fresh-install empty state over it.
	t.Run("but not through writeStoreError, which is about one row", func(t *testing.T) {
		w := httptest.NewRecorder()
		writeStoreError(w, db.ErrSourceNotFound)
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: writeStoreError is reached by handlers that "+
				"resolved ONE source by id -- PUT /source/annotations among them -- and the "+
				"install-wide refusal there is a false statement about every other source "+
				"on the box: %s", w.Code, w.Body.String())
		}
		var body apiError
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v: %s", err, w.Body.String())
		}
		if body.Code == codeNoSource {
			t.Fatalf("code = %q: a client branching on it renders \"no programme yet\" for a "+
				"row that is merely missing", body.Code)
		}
	})
	t.Run("every other failure", func(t *testing.T) {
		w := httptest.NewRecorder()
		writeCreateError(w, errors.New("stream key is required"))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: a create that is genuinely malformed must stay "+
				"the client's problem: %s", w.Code, w.Body.String())
		}
		var body apiError
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v: %s", err, w.Body.String())
		}
		if body.Code != "" {
			t.Fatalf("code = %q on an ordinary validation failure; the field is for the "+
				"conditions a client BRANCHES on, and one that is always set is one nothing "+
				"can branch on", body.Code)
		}
	})
}

// PR 3b, and it is a different bug that happens to share this branch.
//
// Every mutating handler in this package called the DEFAULT engine's Reconcile.
// With one programme that is right by accident; with two it means editing the
// second programme's destination saves the row, answers 200, and reconciles the
// first -- so nothing takes effect until a restart and nothing on screen says
// why. sources.go has carried a comment about this hazard since sources landed,
// and it was never true only of the source routes.
//
// THE WITNESS IS THE ENGINE SET, because that is the difference between the two
// calls rather than a consequence of it. Manager.Reconcile re-derives which
// engines should exist from the sources table and then reconciles each of them;
// an engine's own Reconcile cannot do either. So a source that appeared without
// the API being told has an engine afterwards, and did not before.
//
// A destination process would be the more obvious witness and does not work: no
// engine plans one until the ingest layout has been MEASURED, and nothing is
// arriving in a unit test, so neither engine spawns anything and the two are
// indistinguishable that way.
func TestAMutationReconcilesEveryProgrammeRatherThanTheDefault(t *testing.T) {
	h, store, auth := renditionServer(t, defaultTools())
	s := serverUnderTest(t, h)

	dest, err := store.CreateDestination(&db.Destination{
		Name: "Main out", Kind: db.DestRTMP, URL: "rtmp://ingest.example/app",
		StreamKey: "sk-main",
	})
	if err != nil {
		t.Fatalf("create a destination: %v", err)
	}

	// Written straight to the store, ON PURPOSE: POST /sources reconciles
	// through the manager already, so creating it through the route would build
	// the engine and this test would pass whatever the destination handler
	// does.
	second := &db.Source{Name: "Vertical", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := store.CreateSource(second); err != nil {
		t.Fatalf("create the second source: %v", err)
	}
	if s.mgr.Engine(second.ID) != nil {
		t.Fatal("the second source already has an engine before any reconcile, so this " +
			"test cannot tell the two calls apart")
	}

	r := jsonRequest(t, http.MethodPut, fmt.Sprintf("/api/v1/destinations/%d", dest.ID),
		map[string]any{"name": "Main out, renamed"})
	auth(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("update the destination: %d %s", w.Code, w.Body.String())
	}

	if s.mgr.Engine(second.ID) == nil {
		t.Fatalf("after a destination update, source %d still has no engine. The handler "+
			"reconciled the default programme and nothing else, which is the whole of "+
			"PR 3b: with two programmes, a mutation takes effect on one of them.", second.ID)
	}
}

// The nil-manager half, which every server in this package's unit tests has and
// which is why the check lives in the helper rather than at seventeen call
// sites.
func TestReconcilingWithNoManagerIsNothingRatherThanAPanic(t *testing.T) {
	s, _, _ := testServer(t, config.Config{})
	if s.mgr != nil {
		t.Fatal("this fixture grew a manager; it is here for the case where there is none")
	}
	if err := s.reconcile(); err != nil {
		t.Fatalf("reconcile with no manager returned %v, want nil: nothing is running, "+
			"which is not a failure to report", err)
	}
}
