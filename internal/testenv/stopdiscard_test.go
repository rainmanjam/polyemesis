package testenv_test

// The rule: a call to (*supervisor.Process).Stop whose error is thrown away must
// say so with an explicit `_ =`.
//
// It changes no behaviour and it is not an argument that the discards are wrong.
// Most of them are right, and the source already says why -- Restart's comment
// at supervisor.go explains that a restart has no different action to take, and
// playout.go documents deliberate non-action. The problem is that "deliberately
// ignored" and "nobody noticed there was an error" are the SAME BYTES, so the
// thirteenth deliberate discard and the first accidental one are indistinguishable
// on review. `_ =` is two characters that only a person can type, and typing them
// is the whole of the obligation (#196).
//
// The error being discarded is not a nicety. ErrStopDeadline means SIGKILL was
// issued and NOT waited for: the child may still be running, still holding a
// relay port and a hub subscription that the caller is about to hand to
// somebody else.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// stopDiscardAllowlist names call sites that may keep a bare `x.Stop(arg)`
// statement, with the reason. Keyed by "<path relative to the repo root>:<the
// receiver expression, verbatim>".
//
// IT IS EMPTY, and that is the intended end state rather than an accident. The
// issue proposed landing this rule red -- the check present, the thirteen
// existing sites listed -- and burning the list down afterwards. Burning it down
// turned out to cost thirteen two-character edits, so there was nothing left to
// defer. The mechanism stays because the NEXT exception is the one that needs
// somewhere to be written down: a site that genuinely cannot use `_ =` should
// appear here with a sentence, not be silently unmatched.
var stopDiscardAllowlist = map[string]string{}

// stopSiteFloor is the number of one-argument Stop calls the walk must find
// before its silence means anything.
//
// It is deliberately below the current count rather than equal to it: this is a
// vacuity guard, not a census. What it excludes is the failure where the walk
// stops seeing the tree at all -- a moved package, a renamed directory, a broken
// path join -- and reports a clean sweep of nothing. It is NOT a ratchet on the
// number of Stop calls, which is free to move in either direction.
const stopSiteFloor = 15

// stopDiscard is one bare, unannotated discard.
type stopDiscard struct {
	key string // "<relpath>:<receiver>"
	pos string // file:line:col, for the message
}

// findStopDiscards reports every `<recv>.Stop(<one argument>)` used as a
// STATEMENT in src, which is the syntactic shape of a discarded return value.
//
// `_ = p.Stop(ctx)` is an assignment, not an expression statement, so it is not
// matched -- that is the entire mechanism, and it is why the rule needs no type
// information to separate the annotated from the unannotated.
//
// It DOES need a way to separate supervisor's Stop from every other Stop in the
// tree, and it does that syntactically: one argument. Every no-argument Stop --
// time.Ticker, time.Timer, engine.Stop, rtmpserver.Stop, srtserver.Stop, and the
// several dozen `defer tick.Stop()` in this repository -- is excluded by arity
// alone, and (*supervisor.Process).Stop(ctx) is the only one-argument Stop here.
// The honest limit: a future one-argument Stop on some other type would be
// flagged by this rule. That is what stopDiscardAllowlist is for, and being told
// to write a sentence about a new Stop-with-a-result is not a bad outcome.
func findStopDiscards(fset *token.FileSet, f *ast.File, relpath string) (discards []stopDiscard, sites int) {
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Stop" || len(call.Args) != 1 {
			return true
		}
		sites++
		return true
	})
	ast.Inspect(f, func(n ast.Node) bool {
		stmt, ok := n.(*ast.ExprStmt)
		if !ok {
			return true
		}
		call, ok := stmt.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Stop" || len(call.Args) != 1 {
			return true
		}
		discards = append(discards, stopDiscard{
			key: relpath + ":" + exprSource(fset, sel.X),
			pos: fset.Position(stmt.Pos()).String(),
		})
		return true
	})
	return discards, sites
}

// exprSource renders an expression back to source, so a key names the receiver
// an author would recognise (`d.proc`, `p.Process`) rather than a position that
// moves whenever anything above it does.
func exprSource(fset *token.FileSet, e ast.Expr) string {
	var b strings.Builder
	if err := printExpr(&b, fset, e); err != nil {
		return "?"
	}
	return b.String()
}

func TestEveryDiscardedStopSaysSo(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()

	var found []stopDiscard
	totalSites := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", "dist", "data":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		discards, sites := findStopDiscards(fset, f, rel)
		totalSites += sites
		found = append(found, discards...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	// FATAL, not an error, and before anything else is reported: a walk that
	// found nothing has no opinion, and letting it report a clean sweep is how a
	// guard becomes decoration.
	if totalSites < stopSiteFloor {
		t.Fatalf("the walk found only %d one-argument Stop call(s) under %s, and this "+
			"repository has at least %d. This guard is looking at the wrong tree and "+
			"would pass whatever was added to the right one.", totalSites, root, stopSiteFloor)
	}
	t.Logf("stop census: %d one-argument Stop call sites, %d of them bare statements",
		totalSites, len(found))

	sort.Slice(found, func(i, j int) bool { return found[i].pos < found[j].pos })
	seen := map[string]bool{}
	for _, d := range found {
		if _, excused := stopDiscardAllowlist[d.key]; excused {
			seen[d.key] = true
			continue
		}
		t.Errorf("%s: `%s` discards what Stop returned, and does not say so.\n"+
			"Stop returns ErrStopDeadline to mean \"I sent SIGKILL and did NOT wait; the "+
			"child may still be running\". Discarding it is usually right, but a bare "+
			"call cannot be told apart from one where nobody noticed there was an error "+
			"-- and the next reader has to re-derive, for every site, which of the two "+
			"this is.\n"+
			"Write `_ = %s` (and, where the reason is not obvious, a line saying why "+
			"there is nothing else to do), or add %q to stopDiscardAllowlist with a "+
			"reason.", d.pos, d.key, d.key, d.key)
	}
	for key, reason := range stopDiscardAllowlist {
		if !seen[key] {
			t.Errorf("stopDiscardAllowlist excuses %q (%q), and no bare Stop statement "+
				"matches it. Delete the entry rather than leaving a dead excuse.", key, reason)
		}
	}
}

// TestTheStopDiscardRuleFlagsItsRedFixturesAndSparesItsGreen is the guard on the
// guard. Without it, `findStopDiscards` returning nothing at all would read as
// a clean tree, and the two-character edits above would be unverified.
//
// Asserted by COUNT and by IDENTITY: a predicate that flags the right number of
// the wrong things is not a working predicate.
func TestTheStopDiscardRuleFlagsItsRedFixturesAndSparesItsGreen(t *testing.T) {
	cases := []struct {
		file string
		// want is the receiver expression of every discard the rule must report
		// in this fixture, in source order.
		want []string
	}{
		{file: "red/bare-call.go.txt", want: []string{"p", "d.proc"}},
		{file: "red/inside-a-goroutine.go.txt", want: []string{"p"}},
		{file: "green/annotated.go.txt"},
		{file: "green/result-is-used.go.txt"},
		{file: "green/no-argument-stops.go.txt"},
	}

	dir := filepath.Join("testdata", "stopguard")
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			path := filepath.Join(dir, tc.file)
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, src, 0)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			got, sites := findStopDiscards(fset, f, tc.file)
			if len(got) != len(tc.want) {
				t.Fatalf("flagged %d discard(s), want %d (fixture has %d one-argument "+
					"Stop call sites in total): %v", len(got), len(tc.want), sites, got)
			}
			for i, d := range got {
				want := tc.file + ":" + tc.want[i]
				if d.key != want {
					t.Errorf("discard %d is %q, want %q. The rule flagged the right "+
						"NUMBER of sites and the wrong ones.", i, d.key, want)
				}
			}
		})
	}
}

// printExpr renders an expression with go/printer. Its own function so the
// import stays local to the one place that needs it.
func printExpr(w *strings.Builder, fset *token.FileSet, e ast.Expr) error {
	return printer.Fprint(w, fset, e)
}
