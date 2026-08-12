package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ISSUE #168, HALF ONE: ONE SHAPE FAMILY IS NOW DERIVED FROM THE CODE THAT
// EMITS IT RATHER THAN FROM A ROW SOMEBODY WROTE.
//
// The issue's sentence is "the shape list is maintained by hand and not joined
// to anything that emits. Nothing fails when a new response shape appears."
// That was exactly true, and the measurement that proves it is the first thing
// this file did when it ran: shapeRegistry() carried FOUR response-header rows
// (Location, Set-Cookie, Cache-Control, Content-Disposition) and this package
// emits SIXTEEN distinct response headers. Twelve header shapes had never been
// written down, several of them for years, and no test in this ledger could
// have noticed -- because the population was the list.
//
// So the population is now the SOURCE. derivedResponseHeaders parses every
// non-test .go file in this package and collects the header name at every site
// that writes one, and the guard below requires the registry and the derivation
// to be the same set in both directions:
//
//   - a header this package emits with no shape row FAILS. That is the "nothing
//     fails when a new response shape appears" hole, closed for this family.
//   - a `response-header/X` row that no site emits FAILS. That is the opposite
//     staleness: a row describing output this build no longer produces, which
//     the shapeFloor ratchet cannot see because the count does not move.
//
// WHY THIS FAMILY, said plainly because a derivation invites the belief that it
// is total. A response header is derivable because the emission is a CALL WITH
// THE NAME IN IT: `w.Header().Set("Vary", ...)` names its own shape.
//
// THIS COMMENT USED TO GO ON TO SAY that the whole-payload rows had no such
// literal and were therefore underivable, and that claim is retracted. It was
// right that a payload shape does not name itself in a header-setting call and
// wrong that it names itself nowhere: six of those rows spell a MEDIA TYPE in
// this package's source, and a seventh spells a websocket upgrade.
// shape_payload_derivation_test.go derives them by censusing every string
// literal in the package, and that census found an eighth shape --
// playout-poster -- that no row had ever mentioned. What survives of the old
// paragraph is only the narrow part, and it is the reason the two files are
// separate: a literal is why a ROW must exist; whether the bytes really are
// that shape is the INSPECTOR's job, and #176's failure was an inspector
// reading 50 bytes of {"error":...}, not a population that was too wide.
//
// The residual after both files is stated as a rule rather than a list --
// see assertEveryShapeRowIsAccountedFor -- and it has a row in
// deferredWithReasons citing this issue.

// headerEmission is one response header this package writes, with every site
// that writes it. The sites are the evidence: a failure names them, so the
// reader of a failing ledger is pointed at the line to look at rather than told
// a name.
type headerEmission struct {
	Name  string
	Sites []string
}

// derivedResponseHeaders is the population.
//
// It matches four things, and the fourth one is there because the first three
// missed a third of the set:
//
//	w.Header().Set("X", ...) / .Add     -- the inline form
//	http.SetCookie(w, ...)              -- Set-Cookie, whose name is in net/http
//	http.Redirect(w, r, ...)            -- Location, likewise
//	h.Set("X", ...) where h = w.Header() -- THE ALIASED FORM
//
// The first version of this scan had only the first three and derived eleven
// headers. securityHeaders() in security.go writes five of this API's headers
// -- Content-Security-Policy, X-Frame-Options, Referrer-Policy,
// Permissions-Policy and Strict-Transport-Security -- through a local `h :=
// w.Header()`, and the playout watch page rewrites the CSP the same way. A
// derivation that reads a syntactic form rather than a meaning is blind to a
// spelling, and the spelling it was blind to was the security middleware every
// response in this API passes through. Recorded because "derived, therefore
// total" is the belief this ledger exists to disbelieve: the alias pass is the
// second draft, and there may be a third spelling nobody has written yet.
//
// WHAT IT DELIBERATELY DOES NOT CLAIM:
//
//   - Del is not an emission. `h.Del("X-Frame-Options")` in playout.go removes a
//     header the middleware set; the name is already derived from the Set that
//     put it there, and counting a removal as an emission would be a lie in the
//     safe-looking direction.
//   - http.ServeContent and http.ServeFile emit headers this scan cannot name,
//     because the names are inside net/http (Content-Type sniffing,
//     Last-Modified, Accept-Ranges, Content-Range, Etag). The four delegation
//     sites are RETURNED so the guard can report them rather than pass over
//     them in silence, and that hole has its own deferral row.
//   - a non-literal header name is unresolvable, and the guard FAILS on one
//     rather than skipping it: a name computed at runtime is a shape this
//     derivation cannot see, so the derivation would silently stop being the
//     population. There are none today, which is what makes that a live ratchet
//     rather than a decorative branch.
func derivedResponseHeaders(t *testing.T) (emissions []headerEmission, dynamic, delegations []string) {
	t.Helper()

	sites := map[string][]string{}
	fset := token.NewFileSet()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	parsed := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		parsed++

		// Pass one: every identifier bound to the result of a `.Header()` call
		// anywhere in the file. File-scoped rather than function-scoped on
		// purpose: over-collecting an alias can only ever make the scan derive
		// MORE header names, which fails towards demanding a row, and a
		// derivation whose error direction is "you must account for this" is
		// the one worth having here.
		alias := map[string]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, rhs := range as.Rhs {
				call, ok := rhs.(*ast.CallExpr)
				if !ok {
					continue
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Header" || len(call.Args) != 0 {
					continue
				}
				if i < len(as.Lhs) {
					if id, ok := as.Lhs[i].(*ast.Ident); ok && id.Name != "_" {
						alias[id.Name] = true
					}
				}
			}
			return true
		})

		record := func(header string, pos token.Pos) {
			sites[header] = append(sites[header], fset.Position(pos).String())
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// The net/http helpers that write a header whose name never
			// appears in this package's source.
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "http" {
				switch sel.Sel.Name {
				case "SetCookie":
					record("Set-Cookie", call.Pos())
				case "Redirect":
					record("Location", call.Pos())
				case "ServeContent", "ServeFile":
					delegations = append(delegations,
						fset.Position(call.Pos()).String()+": http."+sel.Sel.Name)
				}
				return true
			}
			if sel.Sel.Name != "Set" && sel.Sel.Name != "Add" {
				return true
			}
			// Either `<expr>.Header().Set(...)` or `<alias>.Set(...)`.
			onHeader := false
			if inner, ok := sel.X.(*ast.CallExpr); ok {
				if isel, ok := inner.Fun.(*ast.SelectorExpr); ok &&
					isel.Sel.Name == "Header" && len(inner.Args) == 0 {
					onHeader = true
				}
			}
			if id, ok := sel.X.(*ast.Ident); ok && alias[id.Name] {
				onHeader = true
			}
			if !onHeader || len(call.Args) == 0 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				dynamic = append(dynamic, fset.Position(call.Pos()).String())
				return true
			}
			name, err := strconv.Unquote(lit.Value)
			if err != nil {
				dynamic = append(dynamic, fset.Position(call.Pos()).String())
				return true
			}
			record(name, call.Pos())
			return true
		})
	}

	// A derivation that parsed nothing derives the empty set, and the empty set
	// satisfies "every derived header has a row" perfectly. This is the same
	// positive control runShapeInspectors keeps for the same reason.
	if parsed == 0 {
		t.Fatal("the response-header derivation parsed no production file. It reads the " +
			"package directory at test time; with nothing parsed it derives the empty " +
			"set, and every assertion joined to it passes having examined nothing.")
	}

	for name, where := range sites {
		sort.Strings(where)
		emissions = append(emissions, headerEmission{Name: name, Sites: where})
	}
	sort.Slice(emissions, func(i, j int) bool { return emissions[i].Name < emissions[j].Name })
	sort.Strings(dynamic)
	sort.Strings(delegations)
	return emissions, dynamic, delegations
}

// responseHeaderShapePrefix is the registry's naming convention for this family,
// and it is what joins the two sets.
const responseHeaderShapePrefix = "response-header/"

// TestEveryEmittedResponseHeaderHasAShapeRow is the join.
//
// Called from TestLedgerPreflight as well as declared here, for the two reasons
// every other guard in this ledger is: `rm` on a file nothing references leaves
// the suite green, and the TestMain preflight forces only ^TestLedgerPreflight$,
// so a guard outside it does not run in the filtered invocation the preflight
// exists to survive. The call IS the compile-time reference.
//
// It costs no server and no request -- it parses this package's source, which is
// measured at 0.02s -- so it is affordable in the preflight, which is the budget
// rule the shape rig is built around.
//
// MUTATION TESTED IN ALL THREE OF ITS DIRECTIONS, because a join that only ever
// fires one way is a join with an untested half:
//
//   - a new emission. Adding `w.Header().Set("X-Ledger-Mutation", "1")` to
//     writeJSON. Observed FAIL: "this package emits the response header
//     "X-Ledger-Mutation" at 1 site(s) and the shape registry has no
//     response-header/X-Ledger-Mutation row". THIS IS #168's SENTENCE: before
//     this guard, that edit was invisible to every test in the repository.
//   - a row with no emission. Deleting the Content-Length Set in the poster
//     handler. Observed FAIL: "the shape registry carries a
//     response-header/Content-Length row and no site in this package writes
//     that header".
//   - a name the scan cannot read. Replacing security.go's literal
//     "Referrer-Policy" with a local variable holding it. Observed FAIL: "1
//     header write(s) in this package pass a name this scan cannot read",
//     which is the branch that stops the derivation from silently shrinking
//     back into a list.
func TestEveryEmittedResponseHeaderHasAShapeRow(t *testing.T) {
	assertDerivedHeaderShapesAreRegistered(t)
}

func assertDerivedHeaderShapesAreRegistered(t *testing.T) {
	t.Helper()
	emissions, dynamic, delegations := derivedResponseHeaders(t)

	rows := map[string]shapeRow{}
	for _, r := range shapeRegistry() {
		if strings.HasPrefix(r.Shape, responseHeaderShapePrefix) {
			rows[strings.TrimPrefix(r.Shape, responseHeaderShapePrefix)] = r
		}
	}

	derived := map[string]bool{}
	for _, e := range emissions {
		derived[e.Name] = true
		row, ok := rows[e.Name]
		if !ok {
			t.Errorf("this package emits the response header %q at %d site(s) and the shape "+
				"registry has no %s%s row:\n  %s\n"+
				"THIS IS THE FAILURE #168 IS ABOUT. The shape list was hand-maintained and "+
				"joined to nothing that emits, so a new response shape appeared and nothing "+
				"failed. Add a row to shapeRegistry() with an Inspector -- a func this "+
				"preflight CALLS, which is the strong discharge -- or a Jurisdiction naming "+
				"the package and test that assert it.",
				e.Name, len(e.Sites), responseHeaderShapePrefix, e.Name,
				strings.Join(e.Sites, "\n  "))
			continue
		}
		if !row.Emitted {
			t.Errorf("the shape %s%s is recorded as NOT emitted and this package writes it at "+
				"%d site(s):\n  %s\n"+
				"`Emitted: false` is the ledger's word for a shape this API does not produce "+
				"(sse is the only honest one), and step 7 gives such a row the verdict "+
				"\"absent\" -- so it needs neither an inspector nor a jurisdiction record. A "+
				"row that is emitted and says otherwise is a blind spot with a discharge "+
				"already stamped on it.",
				responseHeaderShapePrefix, e.Name, len(e.Sites), strings.Join(e.Sites, "\n  "))
		}
	}

	for name := range rows {
		if derived[name] {
			continue
		}
		t.Errorf("the shape registry carries a %s%s row and no site in this package writes "+
			"that header.\n"+
			"This is the staleness the shapeFloor ratchet cannot see: the count does not "+
			"move when a row stops describing this build, only when a row disappears. "+
			"Either the header moved to another package -- in which case the row wants a "+
			"Jurisdiction rather than an inspector, and #169's plain-http-listener row is "+
			"the worked example -- or the emission is gone and so is the row.\n"+
			"derived headers: %v", responseHeaderShapePrefix, name, sortedSet(derived))
	}

	if len(dynamic) > 0 {
		t.Errorf("%d header write(s) in this package pass a name this scan cannot read:\n  %s\n"+
			"A computed header name is a shape the derivation cannot see, which means the "+
			"derivation has quietly stopped being the population and gone back to being a "+
			"list. There were none when this guard was written. Either write the name as a "+
			"literal, or teach derivedResponseHeaders to resolve the constant -- not skip "+
			"it.", len(dynamic), strings.Join(dynamic, "\n  "))
	}

	// NOT A FAILURE, and the reason is that there is nothing here for a
	// maintainer to do. The names net/http writes at a ServeContent site are
	// net/http's, and no rule this package can state would make a row for them
	// meaningful. It is logged because a hole that is measured and never
	// mentioned is the same decoration this ledger keeps catching, and it is
	// written into deferredWithReasons under this issue so it lives in the
	// artifact rather than in a log line nobody reads.
	//
	// STILL UNDERIVED, but no longer unaccounted for: every one of these four
	// sites serves a file whose MEDIA TYPE is a literal in this package, so the
	// media-type census claims all four for the file-download row. What net/http
	// writes on top of that -- Last-Modified, Accept-Ranges, Content-Range,
	// Etag, and a sniffed Content-Type -- is what this log is about, and it is
	// the part no scan here can name.
	if len(delegations) > 0 {
		t.Logf("the response-header derivation names no header for %d delegation(s) to "+
			"net/http, whose header set is net/http's rather than this package's:\n  %s",
			len(delegations), strings.Join(delegations, "\n  "))
	}

	// The positive control for the whole join. Every assertion above is a
	// comparison between two sets, and two empty sets agree.
	if len(emissions) == 0 {
		t.Fatal("the derivation found no response header at all. Every check above compares " +
			"it with the registry, and an empty derivation agrees with any registry.")
	}
	if len(rows) == 0 {
		t.Fatalf("the shape registry has no %s row at all, so the reverse direction of this "+
			"join examined nothing while the forward direction reported %d missing rows.",
			responseHeaderShapePrefix, len(emissions))
	}
}
