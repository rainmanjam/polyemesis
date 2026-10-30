package eventstream

import (
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	repoRoot = "../.."
	eventsMD = "../../docs/EVENTS.md"
)

// typeConstants type-checks this package's source and returns every constant
// of type Type, by Go name, with its string value.
//
// Through go/types rather than an AST string match because several constants
// are spelled Type(hooks.TriggerX): their value lives in another package, and a
// guard that only understood string literals would skip exactly the ones that
// are shared with webhooks.
func typeConstants(t *testing.T) map[string]Type {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, p := range pkgs {
		for _, f := range p.Files {
			files = append(files, f)
		}
	}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := conf.Check("eventstream", fset, files, nil)
	if err != nil {
		t.Fatalf("type-check: %v", err)
	}
	out := map[string]Type{}
	for _, name := range pkg.Scope().Names() {
		c, ok := pkg.Scope().Lookup(name).(*types.Const)
		if !ok {
			continue
		}
		if named, ok := c.Type().(*types.Named); ok && named.Obj().Name() == "Type" {
			out[name] = Type(constant.StringVal(c.Val()))
		}
	}
	if len(out) == 0 {
		t.Fatal("found no Type constants; this guard is looking at the wrong thing")
	}
	return out
}

// TestEveryTypeConstantIsCatalogued: a Type declared and not catalogued is a
// type Publish drops on the floor -- which is the safe direction, and still a
// bug somebody should hear about at build time rather than in production.
func TestEveryTypeConstantIsCatalogued(t *testing.T) {
	for name, v := range typeConstants(t) {
		if _, ok := Lookup(v); !ok {
			t.Errorf("%s (%q) is declared but has no catalogue row, so Publish refuses it", name, v)
		}
	}
	seen := map[Type]bool{}
	for _, s := range Catalogue() {
		if seen[s.Type] {
			t.Errorf("%s is catalogued twice", s.Type)
		}
		seen[s.Type] = true
		if s.Topic == "" || s.Summary == "" {
			t.Errorf("%s has no topic or no summary", s.Type)
		}
		if (s.ReadScope == Partly) != (s.ReadIf != nil) {
			t.Errorf("%s: a Partly row needs a ReadIf, and only a Partly row may have one", s.Type)
		}
	}
}

var docRow = regexp.MustCompile("^\\| `([a-z._]+)` \\| ([a-z]+) \\| (yes|no|partly) \\| (.+) \\|$")

func docSection(t *testing.T, doc, heading string) string {
	t.Helper()
	i := strings.Index(doc, "\n"+heading+"\n")
	if i < 0 {
		t.Fatalf("docs/EVENTS.md has no %q section", heading)
	}
	rest := doc[i+len(heading)+2:]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// TestTheDocsListExactlyTheCatalogue: docs/EVENTS.md's catalogue table is the
// catalogue, row for row, in order -- type, topic, whether a read token gets it,
// and the summary. A consumer writes code against that table; a type in it that
// is never sent, or one sent and not in it, is a broken promise either way.
func TestTheDocsListExactlyTheCatalogue(t *testing.T) {
	raw, err := os.ReadFile(eventsMD)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	var rows [][]string
	for _, line := range strings.Split(docSection(t, doc, "## Event catalogue"), "\n") {
		if m := docRow.FindStringSubmatch(line); m != nil {
			rows = append(rows, m[1:])
		}
	}
	cat := Catalogue()
	if len(rows) != len(cat) {
		t.Fatalf("docs/EVENTS.md lists %d types; the catalogue has %d", len(rows), len(cat))
	}
	for i, s := range cat {
		read := map[Access]string{Granted: "yes", Withheld: "no", Partly: "partly"}[s.ReadScope]
		want := []string{string(s.Type), string(s.Topic), read, s.Summary}
		for k := range want {
			if rows[i][k] != want[k] {
				t.Errorf("docs row %d column %d = %q, catalogue says %q", i+1, k+1, rows[i][k], want[k])
			}
		}
	}

	// The deferred section exists, names the three items, and lists NOTHING
	// that is catalogued: a type moves out of it in the same change that
	// catalogues it.
	// #6 left this section when degraded-link failover shipped: its degraded:
	// reason rides on failover.switched, which is catalogued and has an
	// emission test driving a real trip through the selector sweep.
	coming := docSection(t, doc, "## Coming with #14 and #4")
	for _, item := range []string{"#14", "#4"} {
		if !strings.Contains(coming, item) {
			t.Errorf("the deferred section does not mention %s", item)
		}
	}
	for _, s := range cat {
		if strings.Contains(coming, "`"+string(s.Type)+"`") {
			t.Errorf("%s is both catalogued (emitted) and listed as coming later", s.Type)
		}
	}
}

// TestEveryCataloguedTypeHasAnEmissionTest is the plan's "a test that every
// documented type is actually emitted", made structural.
//
// It reads every _test.go file in the repository OUTSIDE this package, finds
// each call streamtest.Expect(t, sub, eventstream.TypeX) -- or ExpectSSE, its
// twin over an HTTP response -- and requires every
// catalogued type to be named by at least one. Outside this package because a
// hub test publishes envelopes by hand and proves nothing about a producer; a
// test anywhere else has had to drive the code that really emits the type --
// the engine's sweep, the recorder, the chat hub, the lifecycle coordinator,
// the handler.
//
// A new type -- #14's polls, #4's viewer samples -- therefore cannot be
// catalogued without a test that makes its producer emit it.
func TestEveryCataloguedTypeHasAnEmissionTest(t *testing.T) {
	consts := typeConstants(t)
	expected := map[Type][]string{}
	calls := 0
	fset := token.NewFileSet()
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "ui", "web", "graphify-out":
				return filepath.SkipDir
			}
			if filepath.Clean(path) == filepath.Clean(filepath.Join(repoRoot, "internal", "eventstream")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 3 ||
				!(isSelector(call.Fun, "streamtest", "Expect") || isSelector(call.Fun, "streamtest", "ExpectSSE")) {
				return true
			}
			sel, ok := call.Args[2].(*ast.SelectorExpr)
			if !ok || !isIdent(sel.X, "eventstream") {
				t.Errorf("%s: streamtest.Expect's type argument must be an eventstream.TypeX constant",
					fset.Position(call.Pos()))
				return true
			}
			v, ok := consts[sel.Sel.Name]
			if !ok {
				t.Errorf("%s: %s is not an eventstream.Type constant", fset.Position(call.Pos()), sel.Sel.Name)
				return true
			}
			calls++
			expected[v] = append(expected[v], fset.Position(call.Pos()).Filename+":"+
				strconv.Itoa(fset.Position(call.Pos()).Line))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Fatal("found no streamtest.Expect calls at all; this guard is looking in the wrong place")
	}
	for _, s := range Catalogue() {
		if len(expected[s.Type]) == 0 {
			t.Errorf("%s is catalogued and documented, but no test outside internal/eventstream "+
				"drives its producer and expects it (streamtest.Expect(t, sub, eventstream.%s))",
				s.Type, constName(consts, s.Type))
		}
	}
}

func constName(consts map[string]Type, v Type) string {
	for k, c := range consts {
		if c == v {
			return k
		}
	}
	return "?"
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func isSelector(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	return ok && isIdent(sel.X, pkg) && sel.Sel.Name == name
}
