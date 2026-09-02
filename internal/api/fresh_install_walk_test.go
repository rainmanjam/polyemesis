package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/secrets"
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
// walkEveryRoute asserts before every single request that s.eng() is still nil,
// so each of the statuses above was produced by a handler holding one.
//
// WHAT A 404 DOES NOT PROVE, and this is the sentence that was wrong here and
// is worth reading before adding anything: concretePath fills every {param}
// with "1", and on an install with no rows about seventy of these pairs stop at
// their store lookup. Those routes were REACHED; their bodies were not RUN, so
// a dereference below the lookup would not fail this walk. That blind spot is
// not left to a comment -- TestEveryRegisteredRouteSurvivesEnginesThatDidNotStart
// below walks the same population against a fixture whose rows exist and whose
// engines still do not, which is where those bodies run.
//
// WHAT IS DELIBERATELY NOT ASSERTED is which of the three a given route gives.
// Pinning that would make this a second, worse copy of the route ledger -- one
// that fails when a 404 becomes a 400 for a reason with nothing to do with
// sources. The ledger owns the per-route word. This owns the floor underneath
// every word: whatever a route does on a fresh install, it does not fall over.
func TestEveryRegisteredRouteSurvivesAFreshInstall(t *testing.T) {
	s, h, auth := freshInstallServer(t)

	// The fixture's premise, asserted rather than assumed. This one opens a
	// database file that has never existed before, so the count is the
	// migration's own verdict on a first run -- if the seed ever comes back,
	// this walk stops being about a fresh install and says so here rather than
	// passing for the wrong reason.
	if n, err := s.store.CountSources(); err != nil || n != 0 {
		t.Fatalf("the fixture has %d sources (err %v); this walk proves nothing about a "+
			"fresh install unless there are none", n, err)
	}

	counts := walkEveryRoute(t, s, h, auth, nil)
	t.Logf("fresh install: %d answered, %d refused, %d rejected the request "+
		"(%d of those rejections were a 404 at a store lookup, so the handler below it "+
		"did not run here -- see the engines-that-did-not-start walk)",
		counts.answered, counts.refused, counts.rejected, counts.notFound)
	if counts.answered == 0 || counts.refused == 0 {
		t.Fatalf("the walk found %d answering and %d refusing routes. An install with no "+
			"source must do both: refuse what it cannot do, and answer everything an "+
			"operator recovers through.", counts.answered, counts.refused)
	}
}

// THE SECOND WALK: rows exist, engines do not.
//
// The first walk cannot see below a store lookup, and the guards that live
// there are not decorative. destinationBaseArgv reads s.eng().Tools(),
// s.eng().Processes() and s.eng().Source() behind `if e == nil { errNoSource }`
// (expert.go), and the three expert routes that carry no requireSource have
// nothing else standing between an engine-less install and a segfault. On a
// fresh install every one of them answers 404 before a line of that function
// runs, so deleting the guard changed nothing anybody could observe.
//
// THE STATE THIS STANDS IN FOR IS NOT INVENTED. Manager.reconcile logs and
// continues when engine.New or eng.Start fails (manager.go), so an install with
// rows and no engines is what one unbuildable source produces -- and it is the
// state PR 6's delete leaves behind for as long as a browser tab stays open on
// it. The eng() contract says so in as many words: nil is a normal state, and
// it does not imply an empty database.
//
// The population is the same enumerateRoutes walk, so the two cannot drift into
// covering different route sets.
func TestEveryRegisteredRouteSurvivesEnginesThatDidNotStart(t *testing.T) {
	s, h, auth := enginelessServerWithRows(t)

	if n, err := s.store.CountSources(); err != nil || n != 1 {
		t.Fatalf("the fixture has %d sources (err %v); the whole point of this walk is "+
			"rows that outlived their engine, and with none it is the walk above wearing "+
			"a different name", n, err)
	}

	// THE WALK DELETES AS WELL AS READS, and without this the walk's ORDER
	// would decide which guards it covers. DELETE /api/v1/alerts/rules/{id} is
	// a registered route and it is issued against the one rule that gets
	// POST /alerts/rules/{id}/test past its lookup, so that route went back to
	// 404 -- silently, and only because chi walks DELETE first. Every id column
	// is AUTOINCREMENT, so a row simply recreated after a delete comes back as
	// 2 while concretePath only ever asks for 1; restoring means putting the
	// sequence back too.
	reached := walkEveryRoute(t, s, h, auth, func(t *testing.T) {
		restoreTheSeededRows(t, s.store)
	})
	t.Logf("engines that did not start: %d answered, %d refused, %d rejected (%d 404)",
		reached.answered, reached.refused, reached.rejected, reached.notFound)

	// The routes this fixture exists to unblock, asserted by name. A 404 here
	// means the row it needed stopped being seeded, and the guard behind that
	// lookup went quietly back to being unproven -- which is exactly the state
	// this test was written to end, so it must not be able to return in silence.
	for _, pair := range mustRunTheirBodyWithNoEngine {
		if reached.status[pair] == 0 {
			t.Errorf("%s was not walked at all; the list below has drifted from the router",
				pair)
			continue
		}
		if reached.status[pair] == http.StatusNotFound {
			t.Errorf("%s still answered 404 with its row present, so the engine read below "+
				"its lookup was not executed and this walk covers it in name only. Body: %s",
				pair, truncateForFailure(reached.body[pair]))
		}
	}
}

// mustRunTheirBodyWithNoEngine is every {param} route whose handler reads the
// engine AFTER its store lookup, and which therefore proves nothing on an
// install with no rows.
//
// Derived by hand from the s.eng()/s.engOrNil() call sites, and short on
// purpose: it is the set whose coverage depends on the fixture seeding a
// particular row, so each entry is a claim that the row is still being seeded.
// Everything else behind a lookup reaches the store or the disk and never the
// engine -- /jobs/{id}, /hooks/{id}, /media/{name}, /platforms/accounts/{id} --
// and adding them here would be asking the fixture to carry rows for a property
// they do not have.
var mustRunTheirBodyWithNoEngine = []string{
	// destinationBaseArgv, the three that carry no requireSource by design.
	"GET /api/v1/destinations/{id}/expert",
	"POST /api/v1/destinations/{id}/expert/preview",
	"POST /api/v1/destinations/{id}/expert/dry-run",
	// handleTestAlertRule -> s.eng().Alerts().
	"POST /api/v1/alerts/rules/{id}/test",
	// handleClipSource -> clipTracks -> s.engOrNil().SourceKnown().
	"GET /api/v1/clipper/recordings/{id}",
}

// walkCounts is the three-way split, plus the two things a failing run needs
// that a count cannot give: which status each pair produced, and its body.
type walkCounts struct {
	answered, refused, rejected, notFound int
	status                                map[string]int
	body                                  map[string]string
}

// walkEveryRoute issues one request per registered (method, pattern) and
// asserts the floor. Shared by both walks so that the population, the
// exceptions and the meaning of each status cannot diverge between them.
//
// before, when it is not nil, runs ahead of each request. It is how the rows
// walk keeps its fixture from being dismantled by the walk itself.
func walkEveryRoute(t *testing.T, s *Server, h http.Handler, auth func(*http.Request), before func(*testing.T)) walkCounts {
	t.Helper()

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

	out := walkCounts{status: map[string]int{}, body: map[string]string{}}
	for _, route := range routes {
		pair := route.Method + " " + route.Pattern
		if why, skip := freshInstallNotWalked[pair]; skip {
			t.Logf("not walked: %s -- %s", pair, why)
			continue
		}
		path := concretePath(route.Pattern)
		t.Run(pair, func(t *testing.T) {
			if before != nil {
				before(t)
			}
			// The premise, re-checked per request rather than once per fixture.
			// Both walks are meaningless the moment an engine exists, and both
			// issue mutations -- PUT /settings reconciles -- so "no engine" has
			// to be a property of THIS request and not of the setup that ran
			// two hundred requests ago.
			if s.eng() != nil {
				t.Fatalf("an engine came up during the walk, so %s and everything after it "+
					"is a test of the ordinary path. Nothing below this line proves "+
					"anything about an install with no programme.", pair)
			}
			// A JSON object body for every method, including GET. The handlers
			// that read a body get a well-formed one they will reject on its
			// CONTENTS rather than on its syntax, which is what keeps a 400
			// here meaning "this route does not want this" instead of "this
			// route never parsed anything".
			r := jsonRequest(t, route.Method, path, map[string]any{})
			auth(r)
			w := do(t, h, r)
			out.status[pair] = w.Code
			out.body[pair] = w.Body.String()

			switch {
			case w.Code/100 == 2, w.Code/100 == 3:
				out.answered++
			case w.Code == http.StatusServiceUnavailable:
				if assertFreshInstallRefusal(t, pair, path, w.Body.Bytes()) {
					out.refused++
				}
			case w.Code/100 == 4:
				out.rejected++
				if w.Code == http.StatusNotFound {
					out.notFound++
				}
			default:
				t.Fatalf("%s returned %d on an install whose engine set is empty, which is "+
					"the dereference this stack exists to prevent -- and on a fresh install "+
					"it arrives on the first screen a new operator opens, with no other "+
					"screen left to explain it.\nbody: %s",
					pair, w.Code, truncateForFailure(w.Body.String()))
			}
		})
	}
	return out
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
// not expect -- fails here instead of being counted as fine. It bought exactly
// that: POST /alerts/rules/{id}/test answered "the alert notifier is not
// running", which is this refusal under a name that sends the operator looking
// for a subsystem to restart, and no walk could see it until one of them had a
// rule to test.
//
// Shared by both walks. "Fresh install" in the registry names is the case the
// list was written for, not a claim that the rows walk is one.
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
		t.Fatalf("%s returned 503 with code %q on an install with no engine. Either it is refusing "+
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
	"GET /api/v1/health": "this is not a refusal at all -- it is health REPORTING, " +
		"and reporting a true thing. The walk's fixture is an install with a source " +
		"and no engine running, which is precisely the state handleHealth exists to " +
		"stop answering \"ok\" to: nothing is being published. It is NOT given " +
		"codeNoSource, and it does not carry this API's error shape at all, because " +
		"it is not an error body -- it is the check document, and the callers that " +
		"read the 503 are a container healthcheck and a monitor rather than the " +
		"dashboard. The route the dashboard would draw an empty state from is " +
		"/setup, which answers 200 with sources:0. See handleHealth for why only the " +
		"database and the engine can reach 503 and the recording floor cannot.",
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

// freshInstallServer is the walk's fixture: a database file that has never
// existed before, opened through the production db.Open, with no engines.
//
// IT DOES NOT GO THROUGH dbtest. That template is built by db.Open plus a
// CreateSource, and the shared no-engine fixture next door deletes the row
// afterwards with raw SQL -- which means MigrateSources always ran with a
// source present and the genuine first-run path was never the thing under
// walk. If PR 4's discriminator regressed and started seeding "Main" on a truly
// fresh open, a fixture that deletes whatever row it finds would still be
// green. This one would not: the premise assertion at the top of the walk reads
// the count the migration itself left.
//
// The optional subsystems are wired because a running server wires them.
// api.Options documents Jobs and Chat as optional and their handlers answer 503
// when they are absent -- correct, and also a short-circuit returned before
// anything reads a recording, resolves a tool path or compiles a clip, so a
// fixture without them would record nineteen routes as covered while never
// reaching the code that holds the nil engine. cmd/polyemesis builds both
// unconditionally.
func freshInstallServer(t *testing.T) (*Server, http.Handler, func(*http.Request)) {
	t.Helper()
	return enginelessServer(t, nil)
}

// enginelessServerWithRows is the same install after somebody's source, its
// destination, its rendition, an alert rule and a recording exist -- and the
// engines still do not.
func enginelessServerWithRows(t *testing.T) (*Server, http.Handler, func(*http.Request)) {
	t.Helper()
	return enginelessServer(t, seedTheRowsAnEngineWouldHaveCarried)
}

// enginelessServer builds a Server on a first-run database whose Manager holds
// no engines, and keeps holding none for the life of the test.
//
// THE MANAGER IS DELIBERATELY NOT STARTED, and that is the whole construction
// rather than a shortcut. Manager.reconcile returns before it builds anything
// while started is false, so the engine set stays empty across every reconcile
// a walked route triggers -- and the walk triggers several, PUT /settings among
// them. A fixture that started the manager and then inserted rows would have
// built the very engine the walk is written to do without, silently, about
// forty routes in. What it costs is the shared listeners, which no route under
// walk reads.
//
// rows runs after the store is open and before the server is built, so a caller
// decides whether the walk meets an install that has never had a row or one
// whose rows outlived their engines.
func enginelessServer(t *testing.T, rows func(*testing.T, *db.DB)) (*Server, http.Handler, func(*http.Request)) {
	t.Helper()

	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "polyemesis.db"))
	if err != nil {
		t.Fatalf("db.Open on a path nothing has ever written: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateUser("admin", testPassword); err != nil {
		t.Fatalf("create admin: %v", err)
	}

	// Real ports rather than the defaults, because a test suite must not open
	// 6000 and 1935 on the machine running it -- and real ones rather than the
	// 0 the shared no-engine fixture stores, because Settings.Validate refuses
	// 0 while the Manager reads it as "off", so PUT /settings would answer 400
	// for a reason with nothing to do with sources and the walk would record
	// the one unguarded mutation a new operator makes as "rejected".
	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.Listeners.SRTPort = freeUDPPort(t)
	st.Listeners.RTMPPort = testenv.FreeTCPPort(t)
	if err := st.Validate(); err != nil {
		t.Fatalf("the fixture's own settings will not validate, so PUT /settings would "+
			"400 for a fixture reason and the walk would prove nothing about it: %v", err)
	}
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}

	if rows != nil {
		rows(t, store)
	}

	box, err := secrets.New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	cfg := config.Config{DataDir: dir}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}

	bus := events.NewBroker()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := engine.NewManager(quiet, cfg, store, defaultTools(), bus)
	t.Cleanup(mgr.Stop)

	s := New(Options{
		Log: quiet, Config: cfg, DB: store, Secrets: box,
		Engine: mgr, Events: bus, Version: "test",
	})
	s.jobq = jobs.New(quiet, store)
	s.chat = chat.New(chat.WithStore(store), chat.WithLogger(quiet))
	t.Cleanup(s.chat.Close)

	h := s.Handler()
	lastTestServer = s
	if s.eng() != nil {
		t.Fatal("the fixture came up with an engine, so nothing walked through it would " +
			"be testing an install with no programme")
	}
	return s, h, login(t, h)
}

// seedTheRowsAnEngineWouldHaveCarried creates one of each thing a {param} route
// looks up before it reads the engine.
//
// One of each, and no more. Every row here exists to get a specific handler
// past its lookup -- see mustRunTheirBodyWithNoEngine -- and a fixture that
// grew a second destination or a populated session would start asserting
// something about list ordering that this walk has no business owning.
func seedTheRowsAnEngineWouldHaveCarried(t *testing.T, store *db.DB) {
	t.Helper()

	src := &db.Source{Name: "Main", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := store.CreateSource(src); err != nil {
		t.Fatalf("create the source whose engine did not come up: %v", err)
	}
	if _, err := store.CreateDestination(&db.Destination{
		Name: "A platform", Kind: db.DestRTMP, URL: "rtmp://example.invalid/live",
		StreamKey: "k", Enabled: true, AudioBitrate: 128,
		Profile:  routing.DefaultProfile(),
		SourceID: &src.ID,
	}); err != nil {
		t.Fatalf("create destination: %v", err)
	}
	if _, err := store.CreateRendition(&db.Rendition{
		Name: "720p", Width: 1280, Height: 720, FPS: 30, VideoBitrate: 3000,
		Encoder: db.EncoderX264, Preset: "veryfast", GOPSeconds: 2,
		SourceID: &src.ID,
	}); err != nil {
		t.Fatalf("create rendition: %v", err)
	}
	if _, err := store.CreateAlertRule(&alerts.Rule{
		Name: "A rule", Enabled: true, URL: "https://example.invalid/hook",
		Format: alerts.FormatJSON, Events: []alerts.Type{alerts.TypeDestinationDown},
		MinSeverity: alerts.SeverityWarning,
	}); err != nil {
		t.Fatalf("create alert rule: %v", err)
	}
	// A measured segment, because clipTimeline refuses an unmeasured one and
	// the clipper routes would go on 404ing for that reason instead. No file on
	// disk: the timeline tolerates a missing path and the walk is not about
	// what the clipper can cut.
	if err := store.UpsertRecording(&db.Recording{
		Filename: "2026-01-01_00-00-00.ts", DurationMS: 5000, Bytes: 1024, Tracks: 2,
		StartedAt: time.Now().Add(-time.Minute), FinishedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create recording: %v", err)
	}

	// concretePath asks for id 1 and nothing else, so a row that came back at 2
	// is a row this walk cannot reach. It happens the moment somebody seeds
	// something twice, or restores without resetting the AUTOINCREMENT
	// sequence, and the only symptom otherwise is a 404 that reads exactly like
	// the empty fixture it replaced.
	for name, get := range map[string]func() error{
		"source":      func() error { _, err := store.GetSource(1); return err },
		"destination": func() error { _, err := store.GetDestination(1); return err },
		"rendition":   func() error { _, err := store.GetRendition(1); return err },
		"alert rule":  func() error { _, err := store.GetAlertRule(1); return err },
		"recording":   func() error { _, err := store.GetRecording(1); return err },
	} {
		if err := get(); err != nil {
			t.Fatalf("the seeded %s is not id 1 (%v), so every route this fixture exists "+
				"to unblock is still answering 404 at its lookup", name, err)
		}
	}
}

// restoreTheSeededRows puts the fixture back to one of each, with id 1, when a
// walked route has taken one away.
//
// ALL FIVE OR NOTHING, because the source CASCADEs: a DELETE that reached it
// took the destination and the rendition with it, and repairing them one at a
// time would rebuild a destination against a source id that no longer exists.
// The sqlite_sequence rows go with them -- the ids are AUTOINCREMENT, so
// without that reset the replacement rows come back as 2 and every {param}
// route in this walk is asking for 1.
//
// The common case is that nothing was removed and this is five point reads.
func restoreTheSeededRows(t *testing.T, store *db.DB) {
	t.Helper()
	if _, err := store.GetSource(1); err == nil {
		if _, err := store.GetDestination(1); err == nil {
			if _, err := store.GetRendition(1); err == nil {
				if _, err := store.GetAlertRule(1); err == nil {
					if _, err := store.GetRecording(1); err == nil {
						return
					}
				}
			}
		}
	}
	for _, table := range []string{"destinations", "renditions", "alert_rules", "recordings", "sources"} {
		if _, err := store.SQL().Exec("DELETE FROM " + table); err != nil {
			t.Fatalf("clear %s before reseeding: %v", table, err)
		}
		if _, err := store.SQL().Exec("DELETE FROM sqlite_sequence WHERE name = ?", table); err != nil {
			t.Fatalf("reset the %s sequence: %v", table, err)
		}
	}
	seedTheRowsAnEngineWouldHaveCarried(t, store)
}

// TestTheFreshInstallRegistriesStillNameRegisteredRoutes keeps the lists from
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
	var stale []string
	for _, pair := range mustRunTheirBodyWithNoEngine {
		if !seen[pair] {
			stale = append(stale, pair)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("mustRunTheirBodyWithNoEngine names routes this router does not serve: "+
			"%s. The guard each one stands for is now covered by nothing.",
			strings.Join(stale, ", "))
	}
}
