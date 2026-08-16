package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// THE FRESH-INSTALL WALK. Every registered route, on an install that has never
// had a source, issued as the operator who has just set their password.
//
// This is what the four commits before it were building towards, and it is the
// only test in this package that asks the question an operator asks: I
// installed this, I signed in, and I clicked something. What happened?
//
// Nothing else asks it. The zero-source READ contract names fifteen paths by
// hand and drives those. The guard test drives the routes that carry
// requireSource, which by construction is the set somebody already thought
// about. Neither can fail for a route nobody considered, and "a route nobody
// considered" IS the failure mode: PUT /api/v1/loudness reached a nil engine
// behind a dashboard poll that appeared on no list anywhere.
//
// SO THE POPULATION IS THE ROUTER. enumerateRoutes is the ledger's own walk --
// the same one testdata/route-coverage.json is generated from -- so a route
// added tomorrow is walked tomorrow with no edit here, and a route that stops
// being registered leaves the set rather than lingering as a passing assertion
// about nothing.
//
// WHAT IS ASSERTED is a floor, not a verdict:
//
//	2xx/3xx  the route answered. Correct for everything an install with no
//	         programme genuinely knows -- the setup status, the library, the
//	         recordings on disk, the settings document -- and for the OAuth
//	         callback, which answers by redirecting.
//	4xx      the route answered ABOUT SOMETHING ELSE: there is no destination
//	         with id 1, the body was not what it wanted, this is not a
//	         WebSocket handshake. A 404 on a fresh install is a true statement.
//	503      the route refused, and said which absence it was refusing for --
//	         codeNoSource, or one of the subsystem refusals recorded below.
//
// Anything else is the failure, and on this fixture it is not hypothetical:
// every handler here holds a nil *engine.Engine.
//
// WHAT IS DELIBERATELY NOT ASSERTED is which of the three a given route gives.
// Pinning that would make this a second, worse copy of the route ledger -- one
// that fails when a 404 becomes a 400 for a reason with nothing to do with
// sources. The ledger owns the per-route word. This owns the floor underneath
// every word: whatever a route does on a fresh install, it does not fall over.
func TestEveryRegisteredRouteSurvivesAFreshInstall(t *testing.T) {
	s, h, auth := freshInstallServer(t)

	// The fixture's premise, asserted rather than assumed. dbtest's template
	// creates a source and managerServerWithoutEngines empties the table again;
	// if either end ever stops doing its half, this becomes a tour of an
	// ordinary install and every assertion below holds for the wrong reason.
	if n, err := s.store.CountSources(); err != nil || n != 0 {
		t.Fatalf("the fixture has %d sources (err %v); this walk proves nothing about a "+
			"fresh install unless there are none", n, err)
	}

	routes, _ := enumerateRoutes(t, s)
	// A walk that finds nothing satisfies every claim below. The floor is well
	// under the real count on purpose -- the exact number is the committed
	// ledger's job, where moving it is a reviewable edit -- but a population
	// that collapses means the derivation broke, not that the API shrank.
	if len(routes) < 150 {
		t.Fatalf("the router walk found %d pairs. Every assertion here is universally "+
			"quantified over that set, so a set this small is the walk having gone blind.",
			len(routes))
	}

	var answered, refused, rejected int
	for _, route := range routes {
		pair := route.Method + " " + route.Pattern
		if why, skip := freshInstallNotWalked[pair]; skip {
			t.Logf("not walked: %s -- %s", pair, why)
			continue
		}
		path := concretePath(route.Pattern)
		t.Run(pair, func(t *testing.T) {
			// A JSON object body for every method, including GET. The handlers
			// that read a body get a well-formed one they will reject on its
			// CONTENTS rather than on its syntax, which is what keeps a 400
			// here meaning "this route does not want this" instead of "this
			// route never parsed anything".
			r := jsonRequest(t, route.Method, path, map[string]any{})
			auth(r)
			w := do(t, h, r)

			switch {
			case w.Code/100 == 2, w.Code/100 == 3:
				answered++
			case w.Code == http.StatusServiceUnavailable:
				if assertFreshInstallRefusal(t, pair, path, w.Body.Bytes()) {
					refused++
				}
			case w.Code/100 == 4:
				rejected++
			default:
				t.Fatalf("%s returned %d on an install that has never had a source. Every "+
					"handler here holds a nil *engine.Engine, so this is the dereference -- "+
					"and it arrives on the first screen a new operator opens, with no other "+
					"screen left to explain it.\nbody: %s",
					pair, w.Code, truncateForFailure(w.Body.String()))
			}
		})
	}

	// The three-way split, logged rather than asserted, for whoever reads a
	// failing run: a walk that suddenly refuses everything is a middleware
	// applied one level too high, and the counts say so at a glance where two
	// hundred subtests do not.
	t.Logf("fresh install: %d answered, %d refused, %d rejected the request",
		answered, refused, rejected)
	if answered == 0 || refused == 0 {
		t.Fatalf("the walk found %d answering and %d refusing routes. An install with no "+
			"source must do both: refuse what it cannot do, and answer everything an "+
			"operator recovers through.", answered, refused)
	}
}

// assertFreshInstallRefusal checks that a 503 SAYS WHICH ABSENCE it is about,
// and reports whether it was the no-source one.
//
// A 503 is the one status on a fresh install that is both completely correct
// and completely indistinguishable, to a client, from the server being broken.
// codeNoSource exists to end that ambiguity for the condition this change is
// about; every other 503 this API can emit is about a subsystem that is not
// running, and those are recorded rather than coded because giving each one a
// code is a different change with a different UI on the other end of it.
//
// What the registry buys is the property the walk would otherwise lose: a NEW
// uncoded 503 -- a handler that starts refusing because it met something it did
// not expect -- fails here instead of being counted as fine.
func assertFreshInstallRefusal(t *testing.T, pair, path string, body []byte) bool {
	t.Helper()
	var refusal apiError
	if err := json.Unmarshal(body, &refusal); err != nil {
		t.Fatalf("%s returned 503 with a body that is not this API's error shape (%v). "+
			"A client cannot tell it from the server being down: %s",
			pair, err, truncateForFailure(string(body)))
	}
	if refusal.Code == codeNoSource {
		// The sentence has one job beyond being a sentence: saying where to go
		// next. An operator meeting this has no reason to know what a source is
		// yet.
		if !strings.Contains(refusal.Error, "Sources page") {
			t.Fatalf("%s refused for want of a source without naming the screen that ends "+
				"that state: %q", pair, refusal.Error)
		}
		return true
	}
	why, recorded := freshInstallSubsystemRefusals[pair]
	if !recorded {
		t.Fatalf("%s returned 503 with code %q on a fresh install. Either it is refusing "+
			"for want of a SOURCE, in which case it needs codeNoSource so the dashboard "+
			"can draw an empty state instead of a red toast, or it is refusing for want of "+
			"something else -- in which case say so in freshInstallSubsystemRefusals, "+
			"because from here it looks exactly like the fault this walk is hunting."+
			"\nbody: %s", pair, refusal.Code, truncateForFailure(string(body)))
	}
	t.Logf("%s refuses for a reason that is not the source: %s", path, why)
	return false
}

// freshInstallSubsystemRefusals is every 503 a fresh install answers that is
// NOT about the missing source.
//
// A Go map rather than a number in the committed JSON, for the reason
// noSourceRefusalSites next door is one: there is no `-update-coverage` that
// can regenerate it, so an entry is a sentence somebody wrote on purpose.
var freshInstallSubsystemRefusals = map[string]string{
	"POST /api/v1/playout/analytics/reset": "playout is not running, which on an " +
		"install with no source is permanent rather than incidental -- the playout " +
		"origin is an engine's. It is NOT given codeNoSource: the same 503 answers on " +
		"an install that has a source and has playout switched off, and a code the " +
		"dashboard reads as \"this install has no programme\" would be a false " +
		"statement there. Its sentence already names playout, which is the " +
		"distinction a client actually needs. This refusal predates the guard and is " +
		"the shape the guard was modelled on -- see handleResetPlayoutAnalytics.",
}

// freshInstallNotWalked is the exceptions list, and each entry names a reason
// about the TEST HARNESS rather than about the route. A route excluded because
// it fails is the exclusion this whole file exists to make impossible.
var freshInstallNotWalked = map[string]string{
	"POST /api/v1/version/check": "it dials GitHub. The only thing the walk could " +
		"learn from it is whether the machine running the tests has a network, and a " +
		"suite that fails on an aeroplane is a suite people learn to ignore. Its " +
		"zero-source behaviour is not in question either way: handleCheckUpdate reads " +
		"neither the engine nor a source.",
}

// freshInstallServer is the zero-source fixture with the OPTIONAL subsystems a
// running server wires, and wiring them is the difference between walking a
// route and knocking on a door nobody is behind.
//
// api.Options documents Jobs and Chat as optional, and their handlers answer
// 503 when they are absent. That answer is correct and it is also a
// short-circuit: it is returned before anything reads a recording, resolves a
// tool path or compiles a clip, so a walk against a fixture without them would
// record nineteen routes as covered while never reaching the code that holds
// the nil engine. cmd/polyemesis builds both unconditionally -- "a Hub with
// nothing attached is the difference between no platform is connected and this
// build has no chat" -- so a fresh install genuinely has them.
//
// Assigned after New rather than passed through Options because the fixture
// that empties the sources table is shared with the guard tests, and those want
// the plain server. The handler closes over *Server, so a field set here is the
// field the request reads.
func freshInstallServer(t *testing.T) (*Server, http.Handler, func(*http.Request)) {
	t.Helper()
	s, h, auth := zeroSourceServer(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	if s.jobq != nil || s.chat != nil {
		t.Fatal("the zero-source fixture grew a queue or a chat hub of its own; the " +
			"assignments below would be overwriting somebody else's wiring")
	}
	s.jobq = jobs.New(quiet, s.store)
	s.chat = chat.New(chat.WithStore(s.store), chat.WithLogger(quiet))
	t.Cleanup(s.chat.Close)
	repairTheListenerPortsForSaving(t, s)

	return s, h, auth
}

// repairTheListenerPortsForSaving puts the fixture's settings document back
// into a state PUT /settings can accept.
//
// managerServerWithoutEngines stores rtmpPort 0 to keep a unit test off 1935,
// and the engine Manager reads 0 as "this protocol is off" -- but
// ListenerSettings.problems() requires 1..65535, so Settings.Validate refuses
// the document the fixture itself stored. PutSettings does not validate, which
// is how the two ever came to disagree.
//
// The consequence for this walk is specific and would have been invisible: PUT
// /api/v1/settings answers 400 for that reason alone, and the walk would record
// the one unguarded mutation an operator makes before they have anything else
// to do as "rejected the request" while never reaching a line of the handler.
//
// A free port rather than the 1935 default, because the manager really does
// bind: the reconcile at the end of a settings save opens both listeners, and
// on a developer machine 1935 is very often somebody else's.
func repairTheListenerPortsForSaving(t *testing.T, s *Server) {
	t.Helper()
	st, err := s.store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if st.Listeners.RTMPPort != 0 {
		t.Fatalf("the fixture's rtmp port is %d; this repair was written for the 0 that "+
			"Settings.Validate refuses, and repairing something else would be silently "+
			"changing what the walk exercises", st.Listeners.RTMPPort)
	}
	st.Listeners.RTMPPort = testenv.FreeTCPPort(t)
	if err := st.Validate(); err != nil {
		t.Fatalf("the repaired settings still will not validate, so PUT /settings would "+
			"400 for a fixture reason and this walk would prove nothing about it: %v", err)
	}
	if err := s.store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
}

// TestTheFreshInstallRegistriesStillNameRegisteredRoutes keeps both lists from
// decaying into folklore.
//
// An entry whose route no longer exists is a reader being told a decision was
// made about something that is not there -- and it is an entry that can never
// fire again, so nothing would ever prompt its removal.
func TestTheFreshInstallRegistriesStillNameRegisteredRoutes(t *testing.T) {
	s, _, _ := zeroSourceServer(t)
	_, seen := enumerateRoutes(t, s)

	for name, registry := range map[string]map[string]string{
		"freshInstallNotWalked":         freshInstallNotWalked,
		"freshInstallSubsystemRefusals": freshInstallSubsystemRefusals,
	} {
		var stale []string
		for pair := range registry {
			if !seen[pair] {
				stale = append(stale, pair)
			}
		}
		sort.Strings(stale)
		if len(stale) > 0 {
			t.Errorf("%s names routes this router does not serve: %s. Either the pattern "+
				"was reworded and the walk has silently stopped covering the real route, "+
				"or the route is gone and the entry is folklore.",
				name, strings.Join(stale, ", "))
		}
	}
}
