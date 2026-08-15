package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	if !s.refuseIfSilent(w, routing.Profile{}) {
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
	// The same lift on the general store mapping, which is what the handlers
	// that read a source through writeStoreError go through.
	t.Run("through writeStoreError", func(t *testing.T) {
		w := httptest.NewRecorder()
		writeStoreError(w, db.ErrSourceNotFound)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503: a store that cannot find a source on an "+
				"install with none is not a broken server: %s", w.Code, w.Body.String())
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
