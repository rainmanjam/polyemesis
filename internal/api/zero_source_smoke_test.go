package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The fresh-install smoke test: EVERY registered route, on a database that has
// never had a source.
//
// The other zero-source tests each drive a chosen population -- the reads an
// operator's first tab makes, the pairs behind requireSource. This one drives
// the whole router, and it exists because the failure this whole change is
// about was never in the routes anybody thought to list. It was in
// GET /hls/{name}.m3u8, which no plan mentioned; in GET /clips, which a
// reviewer found; in GET /clipper/recordings/{id}, which only fails when the
// recording has no tracks. Three routes, three separate discoveries, all in a
// design that had already enumerated what it believed the surface to be.
//
// So the population is not enumerated by a person. It is chi.Walk over the
// router this build serves, which is the same derivation the route ledger uses,
// and a route added tomorrow is in this test tomorrow.
//
// # What it asserts, and why it is that and not more
//
// The claim is: NOTHING FALLS OVER. Concretely, no response is a 5xx other than
// 503, and every 503 that is about a source carries the code a client branches
// on.
//
// It deliberately does not assert a status per route. Driving 200-odd routes as
// admin with an empty JSON body produces a spray of 400s (that is not a payload
// this route accepts), 404s (no such row in this fixture) and 401/403s, and
// pinning any of them would make this a test of the fixture rather than of the
// install state. Those are all fine answers. A 500 is not: on this fixture it is
// the nil dereference, every time, because there is nothing else here for a
// handler to fall over on -- no row, no running child, no network peer.
//
// # The 503s that are NOT about a source, reported rather than asserted away
//
// This fixture wires no chat runner and no job queue, so `/chat/*` and `/jobs/*`
// answer "chat is not running on this server" and "the background job queue is
// not running on this server" with a bare 503 and no code. Those are properties
// of the FIXTURE, not of an install with no source, and a real install running
// either subsystem answers them normally.
//
// They are excluded by the shape of the claim rather than by a hand-list of
// route names, which would go stale the way every hand-list in this package has:
// a 503 must carry codeNoSource IF it says anything about a source, and must not
// mention one otherwise. A refusal that means "you have no programme" and forgets
// its code therefore still fails here, which is the case worth catching -- "this
// install has nothing yet" and "the reconcile failed" are the same status and
// opposite meanings, and a dashboard that cannot tell them apart shows a
// first-time operator a red fault where an empty state belongs.
//
// The count of routes that DID answer the refusal is asserted to be non-trivial,
// because a guard that stopped firing altogether would otherwise leave every
// claim above satisfied by 200s.
func TestEveryRegisteredRouteAnswersOnAFreshInstall(t *testing.T) {
	s, h, auth := zeroSourceServer(t)

	enumerated, _ := enumerateRoutes(t, s)
	// The floor is the derivation's own alarm, not a count of the API. A walk
	// that returns a handful means chi.Walk stopped seeing the router, and
	// every claim below would then be vacuously true.
	if len(enumerated) < 100 {
		t.Fatalf("the walk found %d registered pairs. This test is universally quantified "+
			"over that set, so a set this small is the enumeration having gone blind "+
			"rather than the API having shrunk.", len(enumerated))
	}

	var refused, otherwiseUnavailable int
	for _, pair := range enumerated {
		path := concretePath(pair.Pattern)
		t.Run(pair.Method+" "+pair.Pattern, func(t *testing.T) {
			r := jsonRequest(t, pair.Method, path, map[string]any{})
			auth(r)
			w := do(t, h, r)

			if w.Code < 500 {
				return
			}
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s %s answered %d on an install with no source. On this fixture a "+
					"5xx that is not the designed refusal is the nil *engine.Engine "+
					"dereference: there is no database row, no running child and no network "+
					"peer here for anything else to fail on.\nbody: %s",
					pair.Method, path, w.Code, w.Body.String())
			}
			var body apiError
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s %s answered 503 with a body that is not an apiError: %v\nbody: %s",
					pair.Method, path, err, w.Body.String())
			}
			if body.Code == codeNoSource {
				refused++
				// The sentence has one job beyond being a sentence: saying where
				// to go next. An operator meeting this has no reason to know
				// what a source is yet.
				if !strings.Contains(body.Error, "Sources page") {
					t.Fatalf("%s %s refused without naming the screen that ends this state: %q",
						pair.Method, path, body.Error)
				}
				return
			}
			if strings.Contains(strings.ToLower(body.Error), "source") {
				t.Fatalf("%s %s answered 503 about a source with code %q, want %q. Without the "+
					"code a client cannot tell an install that has nothing yet from a server "+
					"that is broken, and is back to matching on the English sentence.\nbody: %s",
					pair.Method, path, body.Code, codeNoSource, w.Body.String())
			}
			otherwiseUnavailable++
		})
	}

	// Reported, so the honest width of the measurement is in the log rather than
	// only in this file's prose.
	t.Logf("%d registered pairs driven on a database that has never had a source: "+
		"%d answered the no_source refusal, %d were 503 for a subsystem this fixture does "+
		"not run, and none was any other 5xx.", len(enumerated), refused, otherwiseUnavailable)
	if refused < 15 {
		t.Fatalf("only %d routes answered the zero-source refusal. The guard population is "+
			"derived from the middleware chain and should be twenty-odd; a number this low "+
			"means the guard stopped firing, and every claim above is then discharged by "+
			"handlers that answered 200 for something that did not happen.", refused)
	}
}

// The differential, and without it the test above is discharged by a server
// that answers 503 to everything.
//
// Same derived population, driven against a fixture that HAS a source: nothing
// may answer the zero-source refusal. What each route answers instead is again
// not asserted, for the same reason.
//
// TestNoGuardedRouteRefusesOnceASourceExists next door makes this claim over
// the GUARDED pairs. This one makes it over all of them, which is what catches
// a refusal that arrives from somewhere other than the middleware -- the four
// helper sites noSourceRefusalSites records, none of which the ledger's derived
// word can see.
func TestNoRegisteredRouteRefusesForWantOfASourceOnceOneExists(t *testing.T) {
	h, _, auth := renditionServer(t, defaultTools())
	s := serverUnderTest(t, h)
	if s.eng() == nil {
		t.Fatal("the fixture has no engine, so this differential compares two refusals")
	}

	enumerated, _ := enumerateRoutes(t, s)
	for _, pair := range enumerated {
		path := concretePath(pair.Pattern)
		t.Run(pair.Method+" "+pair.Pattern, func(t *testing.T) {
			r := jsonRequest(t, pair.Method, path, map[string]any{})
			auth(r)
			w := do(t, h, r)
			var body apiError
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if body.Code == codeNoSource {
				t.Fatalf("%s %s answered %q on an install that HAS a source, so that refusal "+
					"is not reading the engine set: %d %s",
					pair.Method, path, codeNoSource, w.Code, w.Body.String())
			}
		})
	}
}
