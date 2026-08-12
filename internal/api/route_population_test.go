package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// ISSUE #156. THE POPULATION THIS LEDGER RECONCILES IS NOW DERIVED FROM
// REGISTRATION RATHER THAN FROM A WALK PLUS SOMEBODY'S MEMORY.
//
// The G2 deferral states the gap in the artifact's own words: "chi.Walk is
// complete over the routing TRIE, and the trie is not the MUX." Everything the
// walk cannot emit -- r.NotFound, the method-not-allowed terminal -- was
// enumerated by notFoundProbes() and methodNotAllowedProbes(), two hand-written
// slices. That is the one place in this file where the totality claim rested on
// a human remembering to add a row, and a totality claim with a hand-list in it
// is the exact shape of every defect this ledger was convened over: a guard
// thorough over a set that excludes the thing.
//
// So Server.Handler() now registers nothing itself and delegates to
// registerRoutes(chi.Router). Handing that an INTERFACE means the ledger can
// hand it a recorder and read back what this build registered:
//
//	the trie pairs      -- cross-checked against chi.Walk, two independent
//	                       derivations of the same set, which is what makes a
//	                       registration the walk cannot see a failure by name
//	the terminals       -- r.NotFound, observed as a call; method-not-allowed,
//	                       derived from the ABSENCE of a call, because chi
//	                       supplies a default and a default nobody registered is
//	                       still a surface this process answers on
//
// The probe slices survive as WITNESSES, not as the population. A probe must
// name a terminal the recorder derived, and every derived terminal must have at
// least one probe. Deleting r.NotFound from registerRoutes and deleting
// methodNotAllowedProbes() now fail in opposite directions, and before this file
// neither did.
//
// WHAT THIS DOES NOT REACH, said plainly because a derived population invites
// the belief that it is total. The recorder derives from the registrations THIS
// PROCESS makes through Server.Handler(). A listener created somewhere else --
// cmd/polyemesis's port-80 ACME and redirect helper, which is #169 -- is
// invisible to any derivation rooted here, because it never touches this router.
// Jurisdiction totality at the outermost boundary is still a human review, and
// no mechanical repair for it is known.

// registeredRoute is one (method, pattern) the recorder saw registered. Method
// is anyMethod for Handle/HandleFunc, which chi expands to every method.
const anyMethod = "ANY"

// chiAllMethods is chi's mALL, spelled out here because it is unexported there.
// A mismatch with chi's own list shows up immediately as a disagreement between
// the recorder and the walk, which is the point of comparing the two at all.
var chiAllMethods = []string{
	http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead,
	http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut,
	"QUERY", http.MethodTrace,
}

// The two terminals of a chi mux that chi.Walk cannot emit.
const (
	terminalNotFound         = "not-found"
	terminalMethodNotAllowed = "method-not-allowed"
)

// registrationRecorder is what registerRoutes writes into.
type registrationRecorder struct {
	// pairs is method+pattern, with anyMethod left unexpanded so the record
	// says what was actually called.
	pairs map[string]bool
	// terminals maps a terminal name to how it came to be live: a registration
	// this build made, or chi's default because no registration was made.
	terminals map[string]string
	// mounted is every pattern handed to Mount. A mounted handler routes
	// internally and the recorder cannot see inside it, so a Mount is recorded
	// as a hole rather than silently flattened.
	mounted []string
}

func newRegistrationRecorder() *registrationRecorder {
	return &registrationRecorder{pairs: map[string]bool{}, terminals: map[string]string{}}
}

// recordingRouter is a chi.Router that forwards every call to a real one and
// records the registrations on the way through.
//
// It embeds the interface rather than *chi.Mux so that a method added to
// chi.Router in a future version compiles here and forwards, while the methods
// this type overrides are exactly the registration surface. The prefix is
// accumulated by Route, which is how a sub-router's "/settings" becomes
// "/api/v1/settings" without asking the walk.
type recordingRouter struct {
	chi.Router
	rec    *registrationRecorder
	prefix string
}

func (r recordingRouter) sub(inner chi.Router, prefix string) recordingRouter {
	return recordingRouter{Router: inner, rec: r.rec, prefix: prefix}
}

func (r recordingRouter) note(method, pattern string) {
	full := r.prefix + pattern
	if full != "/" {
		full = strings.TrimSuffix(full, "/")
	}
	r.rec.pairs[method+" "+full] = true
}

func (r recordingRouter) With(mw ...func(http.Handler) http.Handler) chi.Router {
	return r.sub(r.Router.With(mw...), r.prefix)
}

func (r recordingRouter) Group(fn func(chi.Router)) chi.Router {
	return r.sub(r.Router.Group(func(inner chi.Router) {
		if fn != nil {
			fn(r.sub(inner, r.prefix))
		}
	}), r.prefix)
}

func (r recordingRouter) Route(pattern string, fn func(chi.Router)) chi.Router {
	return r.sub(r.Router.Route(pattern, func(inner chi.Router) {
		if fn != nil {
			fn(r.sub(inner, strings.TrimSuffix(r.prefix+pattern, "/")))
		}
	}), r.prefix)
}

func (r recordingRouter) Mount(pattern string, h http.Handler) {
	r.rec.mounted = append(r.rec.mounted, r.prefix+pattern)
	r.Router.Mount(pattern, h)
}

func (r recordingRouter) Handle(pattern string, h http.Handler) {
	r.note(anyMethod, pattern)
	r.Router.Handle(pattern, h)
}

func (r recordingRouter) HandleFunc(pattern string, h http.HandlerFunc) {
	r.note(anyMethod, pattern)
	r.Router.HandleFunc(pattern, h)
}

func (r recordingRouter) Method(method, pattern string, h http.Handler) {
	r.note(strings.ToUpper(method), pattern)
	r.Router.Method(method, pattern, h)
}

func (r recordingRouter) MethodFunc(method, pattern string, h http.HandlerFunc) {
	r.note(strings.ToUpper(method), pattern)
	r.Router.MethodFunc(method, pattern, h)
}

func (r recordingRouter) Connect(p string, h http.HandlerFunc) {
	r.note(http.MethodConnect, p)
	r.Router.Connect(p, h)
}
func (r recordingRouter) Delete(p string, h http.HandlerFunc) {
	r.note(http.MethodDelete, p)
	r.Router.Delete(p, h)
}
func (r recordingRouter) Get(p string, h http.HandlerFunc) {
	r.note(http.MethodGet, p)
	r.Router.Get(p, h)
}
func (r recordingRouter) Head(p string, h http.HandlerFunc) {
	r.note(http.MethodHead, p)
	r.Router.Head(p, h)
}
func (r recordingRouter) Options(p string, h http.HandlerFunc) {
	r.note(http.MethodOptions, p)
	r.Router.Options(p, h)
}
func (r recordingRouter) Patch(p string, h http.HandlerFunc) {
	r.note(http.MethodPatch, p)
	r.Router.Patch(p, h)
}
func (r recordingRouter) Post(p string, h http.HandlerFunc) {
	r.note(http.MethodPost, p)
	r.Router.Post(p, h)
}
func (r recordingRouter) Put(p string, h http.HandlerFunc) {
	r.note(http.MethodPut, p)
	r.Router.Put(p, h)
}
func (r recordingRouter) Query(p string, h http.HandlerFunc) {
	r.note("QUERY", p)
	r.Router.Query(p, h)
}
func (r recordingRouter) Trace(p string, h http.HandlerFunc) {
	r.note(http.MethodTrace, p)
	r.Router.Trace(p, h)
}

func (r recordingRouter) NotFound(h http.HandlerFunc) {
	r.rec.terminals[terminalNotFound] = "registered by registerRoutes"
	r.Router.NotFound(h)
}

func (r recordingRouter) MethodNotAllowed(h http.HandlerFunc) {
	r.rec.terminals[terminalMethodNotAllowed] = "registered by registerRoutes"
	r.Router.MethodNotAllowed(h)
}

// recordRegistrations runs the real registration function against a real chi
// mux through the recorder, and returns what it saw.
//
// It builds a SECOND router rather than instrumenting the served one, and the
// thing that makes that honest is the agreement assertion below: the recorder's
// pairs and chi.Walk's pairs over the SERVED mux must be the same set, so a
// recorder that drifted from the router this build ships fails rather than
// reporting a comfortable fiction.
func recordRegistrations(t *testing.T, s *Server) *registrationRecorder {
	t.Helper()
	rec := newRegistrationRecorder()
	s.registerRoutes(recordingRouter{Router: chi.NewRouter(), rec: rec})
	// The terminal chi supplies when nobody registers one. It is still a
	// surface this process answers on -- a 405 with an Allow header, emitted
	// before any group middleware runs, which is G4 -- and deriving it from the
	// ABSENCE of a registration is the only way a hand-list-free population can
	// contain it.
	if _, ok := rec.terminals[terminalMethodNotAllowed]; !ok {
		rec.terminals[terminalMethodNotAllowed] =
			"chi's built-in default; registerRoutes registers none"
	}
	if _, ok := rec.terminals[terminalNotFound]; !ok {
		rec.terminals[terminalNotFound] =
			"chi's built-in default; registerRoutes registers none"
	}
	return rec
}

// expandedPairs is the recorder's set with anyMethod expanded the way chi
// expands it, so it is directly comparable with the walk.
func (rec *registrationRecorder) expandedPairs() map[string]bool {
	out := map[string]bool{}
	for key := range rec.pairs {
		method, pattern, _ := strings.Cut(key, " ")
		if method != anyMethod {
			out[key] = true
			continue
		}
		for _, m := range chiAllMethods {
			out[m+" "+pattern] = true
		}
	}
	return out
}

// TestTheWalkedRoutePopulationEqualsTheRegisteredOne is conjunct 1's trie half:
// two independent derivations of the same set, required to agree.
//
// chi.Walk reads the finished TRIE. The recorder reads the CALLS. They are
// different oracles over the same fact, and the interesting failures are the
// asymmetric ones: a pattern the walk emits and no recorded call produced is a
// registration that reached the mux by a route the recorder cannot see (a
// Mount, a sub-mux built elsewhere), and a recorded call the walk does not emit
// is a registration chi dropped.
func TestTheWalkedRoutePopulationEqualsTheRegisteredOne(t *testing.T) {
	h, _, _ := plantedServer(t)
	assertRegisteredPopulationEqualsWalk(t, serverUnderTest(t, h))
}

// assertRegisteredPopulationEqualsWalk is the body, extracted so
// TestLedgerPreflight can CALL it. Two reasons, both measured on
// ledger_ratchet_test.go and restated in the preflight: a guard in a file
// nothing references can be deleted with the suite staying green, and the
// TestMain preflight forces only ^TestLedgerPreflight$, so a free-standing test
// does not run in the filtered invocation the preflight exists to survive.
func assertRegisteredPopulationEqualsWalk(t *testing.T, s *Server) {
	t.Helper()
	_, walked := enumerateRoutes(t, s)
	registered := recordRegistrations(t, s).expandedPairs()

	missing := setDifference(walked, registered)
	extra := setDifference(registered, walked)
	if len(missing) > 0 {
		t.Errorf("chi.Walk emits %d method+pattern pair(s) that no recorded registration "+
			"produced:\n  %s\n"+
			"Either registerRoutes reached the mux by a path the recorder does not "+
			"override -- a Mount, a sub-mux built outside it -- or a registration happens "+
			"somewhere other than registerRoutes. Both mean the derived population is "+
			"smaller than the served one, which is the hole #156 is about pointing the "+
			"other way. Recorded mounts: %v",
			len(missing), strings.Join(missing, "\n  "), recordRegistrations(t, s).mounted)
	}
	if len(extra) > 0 {
		t.Errorf("the recorder saw %d registration(s) that chi.Walk does not emit:\n  %s\n"+
			"A registered pair the walk cannot see is a route the ledger's equality check "+
			"never reconciles. If chi's method expansion has changed, chiAllMethods in this "+
			"file is what has to change with it.",
			len(extra), strings.Join(extra, "\n  "))
	}
}

// TestEveryNonTrieTerminalIsDerivedAndWitnessed is conjunct 1's other half, and
// it is the assertion the hand-list could never make.
//
// The POPULATION of non-trie terminals comes from the recorder. The probe
// slices are witnesses over it. Both directions fail:
//
//   - a terminal with no probe is a surface this mux answers on that nothing
//     drives. Delete methodNotAllowedProbes() and this fires.
//   - a probe naming a terminal the recorder did not derive is a witness for a
//     surface that does not exist. Delete r.NotFound from registerRoutes and
//     this fires -- and before this test, ten probes went on being driven
//     against chi's default 404 while claiming to cover the embedded UI.
func TestEveryNonTrieTerminalIsDerivedAndWitnessed(t *testing.T) {
	h, _, _ := plantedServer(t)
	assertNonTrieTerminalsAreWitnessed(t, serverUnderTest(t, h))
}

// assertNonTrieTerminalsAreWitnessed is the body; see
// assertRegisteredPopulationEqualsWalk for why it is a helper the preflight
// calls rather than a free-standing test alone.
func assertNonTrieTerminalsAreWitnessed(t *testing.T, s *Server) {
	t.Helper()
	derived := recordRegistrations(t, s).terminals

	witnessed := map[string][]string{}
	for _, p := range allNonTrieProbes() {
		if p.terminal == "" {
			t.Errorf("the probe %s %s (%s) names no terminal. Every probe is a witness for "+
				"one of the mux's non-trie terminals, and a probe that names none is a "+
				"request nobody can say what it covers.", p.method, p.path, p.why)
			continue
		}
		witnessed[p.terminal] = append(witnessed[p.terminal], p.method+" "+p.path)
	}

	for name, how := range derived {
		if len(witnessed[name]) == 0 {
			t.Errorf("the mux serves the %q terminal (%s) and no probe drives it.\n"+
				"This population is DERIVED from what registerRoutes registered, so this "+
				"failure means a surface exists and the ledger's witnesses do not cover it. "+
				"Add a row to notFoundProbes() or methodNotAllowedProbes() naming this "+
				"terminal.", name, how)
		}
	}
	for name, probes := range witnessed {
		if _, ok := derived[name]; !ok {
			t.Errorf("%d probe(s) claim to witness the %q terminal and the recorder derived "+
				"no such terminal from this build's registrations: %v\n"+
				"derived terminals: %v\n"+
				"A witness for a surface that is not there is worse than no witness: the "+
				"requests still get issued, still come back, and still look like coverage.",
				len(probes), name, probes, sortedKeysOf(derived))
		}
	}
}

// TestHandlerRegistersOnlyThroughTheRecordedSeam is what stops the derivation
// from being bypassed.
//
// recordRegistrations can only see calls made through the chi.Router it is
// handed. A route registered on the concrete *chi.Mux inside Handler() is
// served and invisible, which would put the population back where #156 found
// it while every assertion above stayed green. So Handler() is required to
// register nothing: it builds the mux, calls registerRoutes, and returns.
//
// Read from SOURCE rather than by calling it, for the same reason
// ledger_ratchet_test.go parses writeLedger: a check that ran the function
// could only observe the routes that ended up in the trie, which is the walk
// again.
func TestHandlerRegistersOnlyThroughTheRecordedSeam(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "api.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse api.go: %v", err)
	}
	registrars := map[string]bool{
		"Handle": true, "HandleFunc": true, "Method": true, "MethodFunc": true,
		"Connect": true, "Delete": true, "Get": true, "Head": true, "Options": true,
		"Patch": true, "Post": true, "Put": true, "Query": true, "Trace": true,
		"NotFound": true, "MethodNotAllowed": true, "Mount": true, "Route": true,
		"Group": true, "Use": true, "With": true,
	}
	var found []string
	var handler *ast.FuncDecl
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if ok && fn.Name.Name == "Handler" && fn.Recv != nil {
			handler = fn
		}
	}
	if handler == nil {
		t.Fatalf("api.go declares no method named Handler. This guard is about that "+
			"function's body; if it has been renamed, rename it here too rather than "+
			"leaving a walk that inspects nothing. (%d decls scanned)", len(file.Decls))
	}
	sawDelegation := false
	ast.Inspect(handler.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "registerRoutes" {
			sawDelegation = true
			return true
		}
		if registrars[sel.Sel.Name] {
			found = append(found, fset.Position(call.Pos()).String()+": "+sel.Sel.Name)
		}
		return true
	})
	if !sawDelegation {
		t.Errorf("Handler() does not call registerRoutes. The recorder in this file derives " +
			"the ledger's population by calling registerRoutes with an instrumented " +
			"chi.Router; a Handler that builds its routes some other way makes that " +
			"derivation a description of a router nobody serves.")
	}
	for _, hit := range found {
		t.Errorf("Handler() registers on the mux directly at %s.\n"+
			"Every registration must go through registerRoutes, which takes a chi.Router "+
			"interface so the ledger can hand it a recorder. A call here is on the "+
			"concrete *chi.Mux, so the route is SERVED and the derived population does not "+
			"contain it -- a route outside the ledger, which is exactly what #156 is "+
			"about. Move it into registerRoutes.", hit)
	}
}

func setDifference(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
