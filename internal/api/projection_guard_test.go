package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The recurrence guard for #150's SECOND blocker: a drift test that watched a
// hand copy of the code instead of the code.
//
// redactSourceViewLikeViewSource and redactPlayoutViewLikeHandler restated, in
// the test file, what viewSource and handleGetPlayout did inline. Reverting the
// production line left the whole repository green -- the guard was watching the
// restatement, and the restatement had not been reverted. Deleting them once is
// not enough; the shape has to stop being available.
//
// Three rules, all AST-based (#107: no test may grep production source text).

// restatementExempt registers a test helper that is deliberately a restatement,
// with the argument for it.
//
// Empty today, and that is the point: both entries it would have held are
// deleted. It exists so that a future restatement has to be an explicit,
// reviewable act with a reason attached rather than a quietly-added function.
var restatementExempt = map[string]string{}

// projectionCallers names the functions permitted to call the read-safe
// primitives directly.
//
// The rule is not "these primitives are dangerous" -- they are the fix. It is
// that a redaction assembled INLINE in a handler is unreachable from a unit
// test, and unreachable-from-a-test is precisely how the legacyRtmpKey blanking
// came to be guarded by a copy of itself. A projection is a named, pure function
// or it is a place a guard cannot go.
var projectionCallers = map[string]bool{
	// The pure projections themselves.
	"readSafeSourceView":  true,
	"readSafePlayoutView": true,
	"readSafeSource":      true,
	"readSafeSettings":    true,
	"readSafeDestination": true,

	// Handlers that apply a whole-struct projection and nothing else. These
	// call readSafeX(x) and assign the result; there is no per-field logic in
	// them to drift, which is the distinction the rule is drawing.
	"handleGetSettings":      true,
	"handleListDestinations": true,
	"handleGetDestination":   true,
}

func apiFiles(t *testing.T, tests bool) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	out := map[string]*ast.File{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") != tests {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		out[name] = f
	}
	if len(out) == 0 {
		t.Fatal("the AST walk found no files; this guard is looking at the wrong directory " +
			"and would pass whatever the package contained")
	}
	return out
}

// TestNoReadSafeViewTakesARequest is the rule that makes the projections
// testable, stated as a constraint rather than a convention.
//
// The moment a readSafe*View takes an *http.Request it needs a fixture with a
// real read-scoped bearer and an engine behind it to call, and the next author
// who wants to assert on it writes a hand copy instead -- which is the entire
// mechanism of blocker 2, reproduced.
func TestNoReadSafeViewTakesARequest(t *testing.T) {
	found := 0
	for name, f := range apiFiles(t, false) {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || !strings.HasPrefix(fn.Name.Name, "readSafe") ||
				!strings.HasSuffix(fn.Name.Name, "View") {
				continue
			}
			found++
			for _, p := range fn.Type.Params.List {
				star, ok := p.Type.(*ast.StarExpr)
				if !ok {
					continue
				}
				sel, ok := star.X.(*ast.SelectorExpr)
				if ok && sel.Sel.Name == "Request" {
					t.Errorf("%s: %s takes an *http.Request. A read-safe projection must be "+
						"a PURE function of the view: taking a request means a test can only "+
						"reach it through a full engine-backed fixture, and the last time "+
						"that was true the drift guard was written against a hand copy "+
						"instead. Decide the principal at the call site and pass the view.",
						name, fn.Name.Name)
				}
			}
		}
	}
	if found < 2 {
		t.Errorf("the walk found only %d readSafe*View functions; this guard is looking at "+
			"the wrong thing", found)
	}
}

// TestRedactionPrimitivesAreCalledOnlyFromNamedProjections keeps per-field
// redaction out of handlers.
func TestRedactionPrimitivesAreCalledOnlyFromNamedProjections(t *testing.T) {
	primitives := map[string]bool{
		"redactInPlace": true, "readSafeSource": true,
		"readSafeSettings": true, "readSafeDestination": true,
	}
	calls := 0
	for name, f := range apiFiles(t, false) {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok || !primitives[id.Name] {
					return true
				}
				calls++
				if !projectionCallers[fn.Name.Name] {
					t.Errorf("%s: %s calls %s. Per-field redaction assembled inside a "+
						"handler cannot be driven by a unit test, so the only guard "+
						"available is a hand copy of it -- which is #150's blocker 2 "+
						"verbatim. Move it into a named readSafe* projection that takes "+
						"the value and returns it, then add that projection to "+
						"projectionCallers.", name, fn.Name.Name, id.Name)
				}
				return true
			})
		}
	}
	if calls < 5 {
		t.Errorf("the walk found only %d calls to the redaction primitives, which is fewer "+
			"than this package makes; the guard is not looking at the right files", calls)
	}
}

// TestNoTestHelperRestatesAHandler refuses the shape itself.
//
// A helper whose NAME says it imitates production code, or whose doc comment
// admits to it, is a guard watching a copy. Either is enough to fail, because
// the two deleted helpers had both.
func TestNoTestHelperRestatesAHandler(t *testing.T) {
	for name, f := range apiFiles(t, true) {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			doc := ""
			if fn.Doc != nil {
				doc = strings.ToLower(fn.Doc.Text())
			}
			named := strings.Contains(fn.Name.Name, "Like") &&
				(strings.HasPrefix(fn.Name.Name, "redact") || strings.HasPrefix(fn.Name.Name, "readSafe"))
			admitted := strings.Contains(doc, "like the handler") ||
				strings.Contains(doc, "same blanking their handlers do") ||
				strings.Contains(doc, "restates what the handler")
			if !named && !admitted {
				continue
			}
			if reason, ok := restatementExempt[fn.Name.Name]; ok && reason != "" {
				continue
			}
			t.Errorf("%s: %s is a test-side RESTATEMENT of production redaction.\n"+
				"A guard that watches a copy passes while the original is reverted -- "+
				"measured on redactSourceViewLikeViewSource, where reverting "+
				"sources.go left `go test ./...` green across the whole repository.\n"+
				"Delete it and drive the production projection. If a restatement is "+
				"genuinely unavoidable, register it in restatementExempt with the "+
				"argument, and record it in testdata/route-coverage.json under an "+
				"excuse of scope \"restatement\" so it is visible in the diff.",
				name, fn.Name.Name)
		}
	}
}
