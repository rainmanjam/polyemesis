package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// secondProgramme adds a source and brings its engine up, so a test can tell
// "asked the owning engine" from "asked the only engine".
//
// Every bug in #497 is invisible on a single-source install -- which is every
// development box, and every fixture in this package that does not call this.
func secondProgramme(t *testing.T, s *Server) *db.Source {
	t.Helper()
	src := &db.Source{Name: "Studio B", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := s.store.CreateSource(src); err != nil {
		t.Fatalf("create the second source: %v", err)
	}
	if err := s.mgr.Sync(); err != nil {
		t.Fatalf("sync after creating the second source: %v", err)
	}
	if len(s.mgr.Engines()) < 2 {
		t.Fatalf("the manager runs %d engine(s); with fewer than two, every assertion "+
			"below passes on an install where the default engine is the right answer",
			len(s.mgr.Engines()))
	}
	if s.eng() == nil || s.eng().SourceID() == src.ID {
		t.Fatal("the new source IS the default engine's, so nothing here can show a " +
			"request being served by the wrong programme")
	}
	return src
}

// THE HEADLINE OF #497: A TRACK LABEL SAVED ON ONE PROGRAMME MUST NOT REWRITE
// ANOTHER'S INGEST.
//
// PUT /source/annotations resolved its source through s.eng() -- s.mgr.Default(),
// Engines()[0] -- so on a multi-source install every save landed on programme 1
// whatever the operator was looking at. It is not a display bug: the handler
// writes the ingest row and then reconciles, which restarts every destination
// whose filter graph changed. On a live broadcast that is a destination going
// off air, and a completed YouTube broadcast cannot return to live.
//
// And there was no click to blame it on. The routing editor autosaves on a
// 500 ms debounce, so the whole sequence is "type a letter, wait half a second".
func TestATrackLabelSavedOnOneProgrammeDoesNotRewriteAnother(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	first := s.eng()
	if first == nil {
		t.Fatal("no default engine in the fixture, so there is nothing to be wrongly " +
			"preferred and this test would pass having observed nothing")
	}
	second := secondProgramme(t, s)

	send(t, h, sign, http.MethodPut,
		"/api/v1/source/annotations?source="+itoa(second.ID),
		map[string]any{"annotations": []map[string]any{
			{"track": 0, "label": "Studio B music"},
		}}, http.StatusOK)

	got, err := s.store.GetSource(second.ID)
	if err != nil {
		t.Fatalf("read back the second source: %v", err)
	}
	if len(got.Ingest.Annotations) != 1 || got.Ingest.Annotations[0].Label != "Studio B music" {
		t.Fatalf("the programme the request NAMED did not receive the annotation: %+v",
			got.Ingest.Annotations)
	}

	// THE ASSERTION THAT IS THE BUG. The default programme must be untouched.
	other, err := s.store.GetSource(first.SourceID())
	if err != nil {
		t.Fatalf("read back the default source: %v", err)
	}
	if len(other.Ingest.Annotations) != 0 {
		t.Errorf("source %d was rewritten by a save addressed to source %d: %+v. That is "+
			"a programme's ingest changing, and its live destinations restarting, while "+
			"nobody was looking at it", first.SourceID(), second.ID, other.Ingest.Annotations)
	}

	// AND THE SETTINGS MIRROR, which is the same confusion arriving through the
	// compatibility door: there is ONE settings document and several sources,
	// so mirroring programme 2's labels into it overwrites what every reader of
	// GET /settings believes about programme 1.
	st, err := s.store.GetSettings()
	if err != nil {
		t.Fatalf("read back the settings: %v", err)
	}
	if len(st.Ingest.Annotations) != 0 {
		t.Errorf("the settings singleton now describes source %d's tracks (%+v); a client "+
			"reading GET /settings is told these are the DEFAULT programme's",
			second.ID, st.Ingest.Annotations)
	}
}

// programmeScopedRoutes is every route in this file's assignment that acts on
// one programme and had no way to say which.
//
// Listed rather than derived, and the derivation that would replace it is
// TestEveryDefaultEngineReachIsScopedOrIsRecorded below: this table is what the
// requests LOOK like, which cannot be read off an AST.
var programmeScopedRoutes = []struct {
	method, path, what string
	body               any
}{
	{http.MethodPut, "/api/v1/source/annotations", "rewrites an ingest and restarts its destinations",
		map[string]any{"annotations": []map[string]any{}}},
	{http.MethodPost, "/api/v1/failover/source", "puts a backup feed on air",
		map[string]any{"source": "auto"}},
	{http.MethodPost, "/api/v1/routing/compile", "claims the graph shown is the graph that will run",
		map[string]any{"profile": map[string]any{}}},
	{http.MethodGet, "/api/v1/processes", "lists a programme's children", nil},
	{http.MethodGet, "/api/v1/processes/ingest/logs", "serves one child's FFmpeg log", nil},
}

// AN INSTALL WITH SEVERAL PROGRAMMES MUST BE ASKED WHICH ONE, NOT GUESSED AT.
//
// The fallback is the defect. Every route below answered for s.mgr.Default()
// when the request named nobody, and Default() is Engines()[0] -- the right
// answer only where there is nothing else it could be. Refusing is what makes
// the mistake unavailable rather than merely less likely: an operator, a
// script, or a UI build that has not learned to send the id gets a 400 that
// names the programmes instead of a silent write to the first one.
func TestAProgrammeRouteThatNamesNoProgrammeIsRefusedOnAMultiSourceInstall(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	secondProgramme(t, s)

	for _, tc := range programmeScopedRoutes {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			r := jsonRequest(t, tc.method, tc.path, tc.body)
			sign(r)
			w := do(t, h, r)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400. This route %s, and it did so for a "+
					"programme the request never named: %s", w.Code, tc.what, w.Body.String())
			}
			if got := noSourceCode(t, w.Body); got != codeSourceRequired {
				t.Errorf("code = %q, want %q -- the code a client branches on to ask "+
					"which programme rather than draw a red toast", got, codeSourceRequired)
			}
			// A refusal that names no way forward is a dead end. Same reasoning
			// as the create refusal in destination_source_scope_test.go.
			if b := w.Body.String(); !strings.Contains(b, "Available:") {
				t.Errorf("the refusal lists no programmes to choose from: %s", b)
			}
		})
	}
}

// A NAMED PROGRAMME THAT IS NOT RUNNING IS REFUSED, NOT QUIETLY REPLACED.
//
// The half of the fallback that survives a scoping parameter: a route that
// reads the id, finds no engine and shrugs back to the default is the original
// bug with an extra step. 409 rather than 404 because the row may well exist --
// what is missing is its engine.
func TestNamingAProgrammeThatIsNotRunningIsRefusedRatherThanServedByTheDefault(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	if s.eng() == nil {
		t.Fatal("no default engine in the fixture, so a fallback to it could not be seen")
	}

	for _, tc := range programmeScopedRoutes {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			r := jsonRequest(t, tc.method, tc.path+"?source=2147483647", tc.body)
			sign(r)
			w := do(t, h, r)

			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409: source 2147483647 is not running, so "+
					"anything this route answered came from a programme the caller did "+
					"not ask about: %s", w.Code, w.Body.String())
			}
		})
	}

	// THE POSITIVE CONTROL. Every case above is a refusal, and a route that
	// refused every request would satisfy all of them while being useless.
	t.Run("positive control: the running programme is served", func(t *testing.T) {
		send(t, h, sign, http.MethodGet,
			"/api/v1/processes?source="+itoa(s.eng().SourceID()), nil, http.StatusOK)
	})
}

// A RESTART REPORTED FOR A PROGRAMME THAT NEVER HEARD OF THE RENDITION.
//
// handleRestartRendition asked s.eng() to restart an id belonging to any
// programme, and Engine.RestartRendition answers nil for an id it does not
// hold -- so the handler wrote 200 {"status":"restarting"} having restarted
// nothing. This is the quietest member of the family: the operator's wedged
// encoder stays wedged and the API says it is being fixed.
//
// db.Rendition.SourceID already names the owner, exactly as
// db.Destination.SourceID does for handleRestartDestination, so this route
// needs no new parameter -- only that it reads the one it has.
func TestRestartingARenditionIsNotReportedForAProgrammeThatCannotHaveDoneIt(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	if s.eng() == nil {
		t.Fatal("no default engine in the fixture, so the wrong answer could not be given")
	}

	// A programme whose row exists and whose engine does not -- a source
	// created and not yet reconciled, which is a state a running server reaches
	// on its own. Sync is deliberately NOT called.
	ghost := &db.Source{Name: "not reconciled yet", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := s.store.CreateSource(ghost); err != nil {
		t.Fatalf("create the second source: %v", err)
	}
	if s.engineForSource(&ghost.ID) != nil {
		t.Fatal("the fixture built an engine for the unsynced source, so this test cannot " +
			"distinguish the owning engine from the default one")
	}

	rend, err := s.store.CreateRendition(&db.Rendition{
		Name: "720p", Height: 720, VideoBitrate: 3000, SourceID: &ghost.ID,
	})
	if err != nil {
		t.Fatalf("create a rendition on the second programme: %v", err)
	}

	body := send(t, h, sign, http.MethodPost,
		"/api/v1/renditions/"+itoa(rend.ID)+"/restart", nil, http.StatusConflict)
	if strings.Contains(string(body), "restarting") {
		t.Errorf("the refusal still claims a restart: %s", body)
	}
}

// defaultEngineSites is every function in this package that reaches for
// s.eng(), and WHY that is the right programme to answer for.
//
// THIS IS THE DEVICE, and it matters more than the four routes #497 named.
// Scoping here is REMEMBERING: one helper (engineForSource) used at a handful
// of call sites against a larger number that need it, and a default engine that
// always answers, so forgetting produces a plausible response rather than a
// failure. Every new handler is a fresh chance to forget, silently.
//
// So the obligation is derived from the source rather than listed twice. A
// function that reaches for the default engine and is not recorded here fails
// this test until somebody writes the sentence saying which programme it means
// -- an obligation ADDED by a source-derived set, never a discharge granted by
// one. It is a Go map and not a regenerable JSON artifact for the same reason
// noSourceRefusalSites next door is: there is no -update flag that can bank a
// new entry, so adding one is a sentence a person wrote on purpose.
//
// It does NOT claim every entry is correct. Several say plainly that the site
// is still wrong on a multi-source install and name what it costs. That is the
// point of writing them down: the ledger is honest about the remainder, and the
// remainder cannot grow without being noticed.
//
// The counterpart it cannot reach is a per-route `sourceScope` word in
// testdata/route-coverage.json, alongside `zeroSource`. That would bind the
// verdict to the ROUTE TABLE rather than to a function name, which is the
// population that actually matters. Building it means editing writeLedger and
// ratchetDirection in route_ledger_test.go; see the note in the #497 fix report
// for the exact shape.
var defaultEngineSites = map[string]string{
	"engOrNil": "the accessor itself -- s.eng() with the nil-manager case " +
		"answered. Not a route.",
	"scopedEngine": "the device. It answers the default ONLY when the install " +
		"has one source or none, which is the one case where the default is not " +
		"a choice; with two or more it refuses. Every #497 route resolves through it.",
	"handlePutAnnotations": "the write is scoped through scopedEngine; this " +
		"reach is the COMPARISON that decides whether the settings singleton may " +
		"be mirrored, and the singleton can only ever describe the default " +
		"programme.",

	// --- reads that still answer for the default programme ---
	"statusPayload": "the assembler behind GET /status and the WebSocket status " +
		"push. The DESTINATION LIST is no longer from the default engine -- it is " +
		"mgr.DestinationStatuses(), every programme's, each compiled by the engine " +
		"that owns it -- because a multi-source install was showing the selected " +
		"programme's destinations and zero for the rest, and Prometheus was " +
		"emitting series for one programme only. The remaining reach is the rest " +
		"of the payload: relay counters, ingest and renditions. STILL UNSCOPED " +
		"there, same consequence as before -- it describes programme 1 whichever " +
		"the operator is looking at. It is a read, so nothing moves on air, but it " +
		"is the read the operator decides from.",
	"handleSource": "GET /source, the ingest layout. STILL UNSCOPED, same shape " +
		"as handleStatus and the same consequence: track numbers from the wrong " +
		"ingest.",
	"handleLevels": "GET /levels, the meters. STILL UNSCOPED -- an operator " +
		"reading silence on programme 2's meters is reading programme 1's.",
	"handleMetrics": "GET /metrics, the Prometheus exposition. Its DESTINATION " +
		"series now cover every programme via mgr.DestinationStatuses(); a scrape " +
		"that silently covered one source was worse than a failing one, because a " +
		"missing series is indistinguishable from a destination nobody configured " +
		"and the alert simply never evaluates. The remaining reach is the relay " +
		"and uptime block. STILL UNSCOPED there, and arguably worse than a wrong " +
		"screen: an alerting rule fires or fails to fire on numbers from a " +
		"programme nobody asked about.",

	// --- outside internal/api/handlers.go and renditions.go ---
	"handleAlertsMeta": "internal/api/automation.go. Unscoped; not this " +
		"change's file assignment.",
	"handleTestAlertRule": "internal/api/automation.go. Unscoped; not this " +
		"change's file assignment. Already recorded in noSourceRefusalSites for " +
		"the zero-source half of the same reach.",
	"handleCaptureClip": "internal/api/automation.go. Unscoped, and it WRITES: " +
		"a clip captured from the default programme's rolling buffer. Not this " +
		"change's file assignment.",
	"handleSetClipBuffer": "internal/api/automation.go. Unscoped, and it writes. " +
		"Not this change's file assignment.",
	"handleLoudness": "internal/api/automation.go. Unscoped; not this change's " +
		"file assignment.",
	"handleSetLoudnessMonitor": "internal/api/automation.go. Unscoped, and it " +
		"writes. Not this change's file assignment.",
	"handlePlayoutPoster": "internal/api/playout.go. Unscoped; not this change's " +
		"file assignment.",
	"handleWS": "internal/api/ws.go. The live socket every screen reads from, " +
		"and the reason the routing editor shows the default programme's tracks " +
		"even now. Unscoped; not this change's file assignment.",
	"publishAudit": "internal/api/audit.go. The audit trail's engine reference. " +
		"Unscoped; not this change's file assignment.",
}

// TestEveryDefaultEngineReachIsScopedOrIsRecorded reads the package's own
// source, for the reason its neighbour TestEveryNoSourceRefusalIsAGuardOrIsRecorded
// does: the set of functions that reach for the default engine is a property of
// the code, and a hand-list of it is complete on the day it is written.
func TestEveryDefaultEngineReachIsScopedOrIsRecorded(t *testing.T) {
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
			"report zero reaches and pass having read nothing")
	}

	found := map[string]bool{}
	for _, f := range pkg.Files {
		for _, d := range f.Decls {
			fn, isFunc := d.(*ast.FuncDecl)
			if !isFunc || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, isCall := n.(*ast.CallExpr)
				if !isCall {
					return true
				}
				// The CALL, not the identifier: a comment or a doc reference to
				// s.eng() is not a reach, and half the comments in this package
				// mention it.
				if sel, isSel := call.Fun.(*ast.SelectorExpr); isSel && sel.Sel.Name == "eng" {
					found[fn.Name.Name] = true
				}
				return true
			})
		}
	}
	// A scan that finds nothing agrees with any list at all.
	if len(found) == 0 {
		t.Fatal("the scan found no call to s.eng() in the package that defines it. The " +
			"accessor was renamed and this test went vacuous rather than red.")
	}

	for name := range found {
		if _, recorded := defaultEngineSites[name]; !recorded {
			t.Errorf("%s reaches for the DEFAULT engine and is not in defaultEngineSites. "+
				"s.mgr.Default() is Engines()[0]: it is the right programme only on an "+
				"install with one source, and it always answers -- so getting this wrong "+
				"produces a plausible response rather than a failure (#497). Either "+
				"resolve the programme the request names (scopedEngine, or engineForSource "+
				"off a row that carries a source id), or add an entry here saying which "+
				"programme this means and why the default is it.", name)
		}
	}
	for name := range defaultEngineSites {
		if !found[name] {
			t.Errorf("defaultEngineSites records %s and nothing in this package's source "+
				"reaches the default engine from it. A stale entry is a reader being told "+
				"a site is accounted for that is gone -- or worse, being told a route is "+
				"known-unscoped when it has since been fixed.", name)
		}
	}
}
