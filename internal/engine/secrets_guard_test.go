package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// secretsExempt lists the supervisor.New call sites in this package that
// deliberately declare no Secrets, with the reason.
//
// Keyed by the Spec's Kind and Name expressions exactly as they appear in the
// source. Kind alone is not unique -- two sites build destinations and two build
// sources -- and Name alone is not either, because several sites name themselves
// from the same `subName` variable. The pair is. The value is the argument, not a note: a site with no
// credential on its command line must be able to SAY why, and a site whose argv
// starts carrying one has to come back here and change the sentence.
//
// Every entry below was traced to the arguments the site actually builds, not
// assumed from its name.
var secretsExempt = map[string]string{
	// ffmpeg.SilenceArgs: two loopback relay URLs and a synthetic source. No
	// operator input reaches it.
	`"silence" silenceSubName`: "relay URLs only; the slate is generated, not fetched",

	// relayFeedArgs / SlateArgs / playlistFeedArgs: a loopback relay URL, a
	// timestamp offset and, for the playlist tier, a concat list PATH that the
	// server wrote itself. The pull URL is consumed by the ingest child, which
	// is a different process and does declare its secrets.
	`"source" "source:" + string(kind)`: "a loopback relay URL and a PTS offset; the " +
		"pull URL belongs to the ingest child, which declares it",
	`"source" "playlist"`: "a concat list path this server wrote; media filenames, no credential",

	// The remaining sidecars all read a loopback relay and write either a
	// loopback relay, a local file, or nothing.
	`"recorder" "recorder"`: "an output path under the recordings directory",
	`"preview" "preview"`:   "a relay URL in, HLS segments out under the data directory",
	`"meters" "meters"`:     "a relay URL in, astats on stdout, no output URL at all",
	`"rendition" subName`: "relay in, relay out. A rendition carries operator TEXT for " +
		"drawtext, which is a caption rather than a credential and is drawn on the " +
		"picture anyway",
	`"loudness" subName`: "relay in, null out; meters.Parse on stdout",

	// playout's muxer. The spawn callback receives an argv the playout manager
	// built from segment paths under the data directory; the watch token is a
	// URL query parameter checked by the HTTP handler and never reaches an argv.
	`"playout" name`: "the playout muxer: HLS segment paths under the data directory. " +
		"The watch token gates the HTTP route and is not a command-line argument",
}

// TestEverySupervisorSpecDeclaresItsSecrets fails the build when a
// supervisor.New site in this package neither populates Secrets nor appears in
// secretsExempt.
//
// The rule exists because the leak it guards is a leak of OMISSION. Nine of the
// egress paths were already correct; what shipped the credential was one
// construction site that did not say what was on its command line. Adding a
// tenth site and forgetting is the same bug again, and nothing else in the tree
// would notice -- the process starts, the destination works, and only a
// read-scoped token reading /processes ever sees the difference.
//
// AST, not a grep over source text (#107). A text search for "Secrets:" would
// match this file, the comment above, and a commented-out line.
func TestEverySupervisorSpecDeclaresItsSecrets(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	seen := map[string]bool{}
	sites := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isSupervisorNew(call) {
				return true
			}
			lit := specLiteral(call)
			if lit == nil {
				t.Errorf("%s: supervisor.New is called with a Spec this guard cannot read. "+
					"Build the Spec as a composite literal at the call so its Secrets field "+
					"is visible here; a Spec assembled elsewhere is exactly the shape that "+
					"lets a credential-bearing site go unclassified.",
					fset.Position(call.Pos()))
				return true
			}
			sites++
			key := fieldSource(fset, lit, "Kind") + " " + fieldSource(fset, lit, "Name")
			if hasField(lit, "Secrets") {
				if reason, excused := secretsExempt[key]; excused {
					t.Errorf("%s: the Spec named %s populates Secrets AND is listed in "+
						"secretsExempt (%q). Delete the exemption -- an excuse that is no "+
						"longer true is how the list becomes a place to silence this test.",
						fset.Position(call.Pos()), key, reason)
				}
				return true
			}
			seen[key] = true
			if secretsExempt[key] == "" {
				t.Errorf("%s: supervisor.Spec{Name: %s} sets no Secrets and has no entry in "+
					"secretsExempt.\n"+
					"Everything the supervisor renders for a reader -- the command line on "+
					"GET /processes, the log ring, the on-disk process.log, the /ws log "+
					"frames and Status.LastError -- removes ONLY the exact literals this "+
					"field declares. A site that omits it is masked by alerts.Redact alone, "+
					"which is a grammar over FFmpeg's open flag namespace and provably does "+
					"not hold: that is #150's argv disclosure, verbatim.\n"+
					"Either populate Secrets, or add an entry here saying which arguments "+
					"this site builds and why none of them is a credential.",
					fset.Position(call.Pos()), key)
			}
			return true
		})
	}

	if sites < 10 {
		t.Errorf("the walk found only %d supervisor.New sites in this package, which is "+
			"fewer than it has; this guard is looking at the wrong thing and would pass "+
			"whatever was added", sites)
	}
	for key, reason := range secretsExempt {
		if !seen[key] {
			t.Errorf("secretsExempt names the Spec %s (%q), which no supervisor.New call in "+
				"this package builds. Delete the entry rather than leaving a dead excuse.",
				key, reason)
		}
	}
}

func isSupervisorNew(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "New" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "supervisor"
}

func specLiteral(call *ast.CallExpr) *ast.CompositeLit {
	for _, arg := range call.Args {
		lit, ok := arg.(*ast.CompositeLit)
		if !ok {
			continue
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "Spec" {
			return lit
		}
	}
	return nil
}

func hasField(lit *ast.CompositeLit, name string) bool {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*ast.Ident); ok && id.Name == name {
			return true
		}
	}
	return false
}

// fieldSource renders a field's VALUE EXPRESSION back to source, so a Name built
// from a variable is identified by that expression rather than collapsing every
// such site onto one key.
func fieldSource(fset *token.FileSet, lit *ast.CompositeLit, name string) string {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		id, ok := kv.Key.(*ast.Ident)
		if !ok || id.Name != name {
			continue
		}
		return exprSource(fset, kv.Value)
	}
	return "<unnamed>"
}

func exprSource(fset *token.FileSet, e ast.Expr) string {
	var b strings.Builder
	writeExpr(&b, fset, e)
	return b.String()
}

func writeExpr(b *strings.Builder, fset *token.FileSet, e ast.Expr) {
	switch v := e.(type) {
	case *ast.Ident:
		b.WriteString(v.Name)
	case *ast.BasicLit:
		b.WriteString(v.Value)
	case *ast.BinaryExpr:
		writeExpr(b, fset, v.X)
		b.WriteString(" " + v.Op.String() + " ")
		writeExpr(b, fset, v.Y)
	case *ast.CallExpr:
		writeExpr(b, fset, v.Fun)
		b.WriteString("(")
		for i, a := range v.Args {
			if i > 0 {
				b.WriteString(", ")
			}
			writeExpr(b, fset, a)
		}
		b.WriteString(")")
	case *ast.SelectorExpr:
		writeExpr(b, fset, v.X)
		b.WriteString("." + v.Sel.Name)
	default:
		b.WriteString("<expr>")
	}
}
