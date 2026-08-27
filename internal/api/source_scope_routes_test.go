package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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

	// --- the second sweep: the reads an operator decides from, and the writes
	// that act on a programme's pipeline. Every one of these answered for
	// s.mgr.Default() until now, and the register next door said so in as many
	// words for each.
	{http.MethodGet, "/api/v1/status", "is the payload the whole dashboard renders from", nil},
	{http.MethodGet, "/api/v1/source", "is the track list the routing editor picks numbers off", nil},
	{http.MethodGet, "/api/v1/levels", "is the meters, where silence on one programme reads as audio on another", nil},
	{http.MethodGet, "/api/v1/loudness", "is a compliance verdict a broadcaster relies on", nil},
	{http.MethodPut, "/api/v1/loudness", "starts or stops a real analyser child",
		map[string]any{"enabled": true}},
	{http.MethodGet, "/api/v1/clips", "reports a retention window a capturer is enforcing", nil},
	{http.MethodPost, "/api/v1/clips", "writes a file off a rolling buffer that has already moved on",
		map[string]any{"seconds": 5}},
	{http.MethodPut, "/api/v1/clips/buffer", "spends disk and CPU for as long as it is on",
		map[string]any{"enabled": true}},
	// The socket, refused BEFORE the upgrade so that the refusal can still be
	// an HTTP status. Its opening burst is the status payload and the track
	// layout, which is the same question /status is asked.
	{http.MethodGet, "/api/v1/ws", "opens the live socket every screen renders from", nil},
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

	// --- install-wide questions asked of one engine, and why that is right ---
	"defaultSourceID": "which source an UNSCOPED endpoint speaks for, and the " +
		"reach is the point of the function rather than an accident: it prefers " +
		"the source that actually BUILT an engine over the store's first row, so " +
		"/system cannot advertise an ingest port with no listener behind it. Its " +
		"one caller now accepts ?source= and labels which programme it answered " +
		"for (#551).",
	"handleTestAlertRule": "POST /alerts/rules/{id}/test. The reach chooses which " +
		"NOTIFIER sends, and that is install-wide by construction: there is one " +
		"alert_rules table, every engine's notifier reads it, and the rule under " +
		"test is read from the store rather than from the body -- so the message " +
		"goes to the operator's endpoint whichever notifier carries it. Same " +
		"argument as publishAudit. It is engOrNil rather than eng because the " +
		"route carries no requireSource and is reached on a build with no manager " +
		"at all; the zero-source half is recorded in noSourceRefusalSites.",
	"publishAudit": "the audit trail. ONE notifier on purpose, and audit.go " +
		"argues it: every engine builds its own but they all read the same " +
		"install-wide alert_rules table, so the default engine's notifier already " +
		"reaches every rule, and publishing through each engine would deliver the " +
		"same event once per source because the coalescer is per-notifier. The " +
		"nil case no longer drops silently -- it logs which event went unpublished " +
		"(#576), because the events that reach here with no engine are the " +
		"security ones.",
	"refuseIfSilent": "the create-time silence guard. The LAYOUT it compiles " +
		"against is now the destination's own programme's, through " +
		"sourceForDestination (#527). The reach that remains is the install-wide " +
		"question that comes first -- is there any engine at all -- which is the " +
		"zero-source refusal and is recorded in noSourceRefusalSites too. Keeping " +
		"the two apart is deliberate: 'this install has no programme' and 'the " +
		"programme you named is not running' are different answers.",

	// --- readers that legitimately answer for an absent programme ---
	"ingestBitrate": "the arrival series the dashboard graphs, and the ONE " +
		"unscoped graph left. Per-programme bitrate now goes to Prometheus " +
		"through ingestSnapshots (#528); this feeds GET /stats and the WebSocket " +
		"stats frame, which are box-shaped surfaces with no programme selector on " +
		"them. STILL UNSCOPED, and the residual is a graph that draws programme " +
		"1's arrival rate on an install running several.",
	"relayStats": "GET /stats' relay block, same surface and same residual as " +
		"ingestBitrate. The per-programme relay counters are in the scrape.",
	"playoutManager": "the whole public playout surface -- player page, HLS, " +
		"poster -- resolves through this one function. STILL UNSCOPED, and it is " +
		"the surface with an EXTERNAL audience: a two-programme install serves " +
		"programme 1's segments whatever it is running (#550). Not fixed here on " +
		"purpose: scopedEngine is the wrong device for it in two ways -- its " +
		"refusal lists the install's programme names, which is a disclosure to an " +
		"unauthenticated viewer, and a public URL has nowhere to carry ?source= " +
		"that an audience would ever type. Control means a per-programme public " +
		"path (/playout/{source}/...), which is a route-table change: the ledger, " +
		"the docs and the share links all move with it.",
	"handleDeleteClip": "DELETE /clips/{name}. The engine is preferred only to " +
		"SERIALISE the delete against a running capturer's eviction; the file is " +
		"in one install-wide directory and the fallback removes it directly, so " +
		"which capturer is asked changes nothing about what is deleted.",
	"handleDownloadClip": "GET /clips/{name}. Same reasoning as handleDeleteClip: " +
		"the engine supplies the confinement base so it cannot drift from the " +
		"directory the capturer writes into, and every engine computes the same " +
		"one from the same config.",
	"clipTracks": "the clip editor's track picker. The engine reach is a FALLBACK " +
		"for a recording whose track count was never measured, and it is dropped " +
		"unless the layout is known. STILL UNSCOPED, together with the track " +
		"LABELS beside it, which come from the settings singleton and so are the " +
		"default programme's (#577). Not fixed here: db.Recording does not carry " +
		"its source_id -- the column exists, the model does not expose it -- so " +
		"the owning programme cannot be resolved from this layer at all. It needs " +
		"internal/db first.",
	"hlsHandler": "the /hls preview file server. The reach is PreviewRequested, " +
		"which keeps an on-demand encoder alive, and the directory below it is " +
		"already resolved per source off the same single read. STILL UNSCOPED: " +
		"the preview poked is the default programme's, and /hls/{id}/ next door " +
		"is the scoped spelling that does not have this problem.",
	"destinationBaseArgv": "the expert argv preview. STILL UNSCOPED -- it searches " +
		"the DEFAULT engine's process list for a destination that may belong to " +
		"another programme, so the command line shown can carry the wrong " +
		"ingest's tracks, and on a multi-source install the search simply finds " +
		"nothing and falls back to a rebuild. It writes nothing, and its " +
		"zero-source half is recorded in noSourceRefusalSites.",
	"eng": "the accessor's own definition. Not a route, and the one place " +
		"mgr.Default() is meant to be spelled.",
	"requireSource": "the middleware. The reach is the EXISTENCE test -- is any " +
		"engine running at all -- and not a choice of programme: it dereferences " +
		"nothing and passes no engine on. Which programme a guarded route then " +
		"acts on is scopedEngine's question, asked in the handler.",
}

// isDefaultEngineReach reports whether a call reaches the DEFAULT engine, by
// any of the three spellings this package has for it.
//
// ALL THREE, and that is #539. The scan matched `eng` alone, so it saw the one
// spelling a careless author uses and missed both spellings a careful one does:
//
//	s.eng()          -- matched
//	s.engOrNil()     -- NOT matched, and it is the spelling api.go's own comment
//	                    tells you to prefer ("Capture the return value and use
//	                    THAT"), so writing the handler correctly was what made it
//	                    invisible
//	s.mgr.Default()  -- NOT matched, the accessor's own definition
//
// engOrNil appeared in the register only because ITS OWN BODY calls s.eng().
// Its callers -- eleven of them on origin/main, including the create-time
// silence guard that #527 is about -- did not. A register that records the
// obvious spelling and not the recommended one is not a Warning device; it is a
// device that agrees with whatever it happens to see.
//
// `Default` is matched only on a `mgr` receiver. Bare `.Default()` would catch
// db.DefaultSettings-shaped helpers and turn this into noise, and noise is how
// a register stops being read.
func isDefaultEngineReach(sel *ast.SelectorExpr) bool {
	switch sel.Sel.Name {
	case "eng", "engOrNil":
		return true
	case "Default":
		// s.mgr.Default() and mgr.Default() both, so the manager reached
		// through a local variable is not a way around this.
		switch recv := sel.X.(type) {
		case *ast.SelectorExpr:
			return recv.Sel.Name == "mgr"
		case *ast.Ident:
			return recv.Name == "mgr"
		}
	}
	return false
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
				if sel, isSel := call.Fun.(*ast.SelectorExpr); isSel && isDefaultEngineReach(sel) {
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
	// AND EACH SPELLING SEPARATELY, because a scan that has gone blind to ONE of
	// them is exactly the state #539 describes and is invisible to the total
	// above: eleven engOrNil callers went unrecorded for as long as the pattern
	// matched `eng` alone, while the count of found sites looked perfectly
	// healthy. Renaming an accessor must break this test, not quietly shrink
	// what it covers.
	for _, spelling := range []struct{ selector, mustFind string }{
		{"eng", "handlePutAnnotations"},
		{"engOrNil", "refuseIfSilent"},
		{"mgr.Default", "defaultSourceID"},
	} {
		if !found[spelling.mustFind] {
			t.Errorf("the scan did not find %s, which reaches the default engine as "+
				"%s(). Either that accessor was renamed -- in which case "+
				"isDefaultEngineReach has to learn the new name -- or the site moved and "+
				"this sentinel has to move with it. A scan that silently stops matching "+
				"one spelling is the whole of #539.", spelling.mustFind, spelling.selector)
		}
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

// managerDefaultCall matches a reach for Manager.Default() through a variable
// named for a manager. `slog.Default()` is the reason this is not a bare
// `\.Default\(`: six packages call it and none of them mean an engine.
var managerDefaultCall = regexp.MustCompile(`\b(mgr|Mgr|manager|Manager|m)\.Default\(\)`)

// THE REGISTER STOPS AT ITS OWN PACKAGE, AND THAT IS THE SECOND BLIND SPOT
// (#539).
//
// defaultEngineSites is derived from internal/api's source and from nothing
// else, so it can only ever record reaches made here. Manager.Default is
// EXPORTED: cmd/polyemesis, internal/engine and anything added later can reach
// the first engine with no register anywhere noticing, and the failure mode is
// the one this whole family has -- a plausible answer for programme 1 rather
// than an error.
//
// The obligation this imposes is deliberately blunt: OUTSIDE internal/api,
// nothing may call Manager.Default at all. There is no legitimate caller today
// -- cmd/polyemesis/mqtt.go walks Engines() per source, which is the shape
// every other consumer should copy -- and "there are none, keep it that way" is
// a rule a test can hold where "each one needs a sentence" would need a second
// register in every package.
//
// Textual rather than an AST walk, because the population is every .go file in
// the repository and parsing all of them to catch a call that must not exist is
// a great deal of machinery for a rule with no exceptions.
func TestNothingOutsideTheAPIPackageReachesTheDefaultEngine(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}
	// The accessor's own definition, which is the one place the name may
	// appear. Recorded as a path so that moving it is a decision somebody
	// makes rather than a silent widening of the rule.
	definition := filepath.Join(root, "internal", "engine", "manager.go")

	scanned := 0
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "ui", "web", "dist":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// internal/api is the package the register above already covers, entry
		// by entry. This test is about everywhere it cannot see.
		if strings.HasPrefix(path, filepath.Join(root, "internal", "api")+string(filepath.Separator)) {
			return nil
		}
		scanned++
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, line := range strings.Split(string(b), "\n") {
			// A comment naming the accessor is not a reach, and manager.go's
			// own explanation of #515 names it twice.
			if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "//") {
				continue
			}
			if !managerDefaultCall.MatchString(line) {
				continue
			}
			if path == definition && strings.Contains(line, "func (m *Manager) Default()") {
				continue
			}
			t.Errorf("%s reaches Manager.Default():\n\t%s\nDefault() is Engines()[0] -- "+
				"the right programme only on an install with one source, and it always "+
				"answers, so getting this wrong produces a plausible response rather than "+
				"a failure (#497). internal/api records its reaches in defaultEngineSites "+
				"and nothing outside it does, so outside this package the rule is simply "+
				"no. Walk mgr.Engines() and answer for each programme, the way "+
				"cmd/polyemesis/mqtt.go's snapshot does.",
				strings.TrimPrefix(path, root+string(filepath.Separator)), strings.TrimSpace(line))
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk the repository: %v", walkErr)
	}
	// A walk that read nothing agrees with any rule at all -- and this one
	// starts from a relative path, which is exactly the sort of thing that
	// silently resolves to an empty directory.
	if scanned < 50 {
		t.Fatalf("the walk read %d Go files outside internal/api; the repository has far "+
			"more, so this test passed having looked at almost nothing", scanned)
	}
}
