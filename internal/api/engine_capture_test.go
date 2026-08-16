package api

import (
	"io/fs"

	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// engineAccessors are the two ways this package asks for the default engine.
// Both re-derive from Manager.Engines under m.mu on every call, so neither
// returns a stable value.
var engineAccessors = map[string]bool{"eng": true, "engOrNil": true}

// A handler that TESTS the engine for nil must DEREFERENCE the one it tested.
//
// s.eng() is s.mgr.Default(), which walks a map the manager mutates:
// Manager.reconcile deletes from it when a source is deleted, and Manager.Stop
// empties it while in-flight requests drain. So
//
//	if s.eng() == nil { ... }
//	return s.eng().Hub().Stats()
//
// tests one engine and dereferences a second read of a set that may no longer
// contain it. The guard reads as though it makes the call safe and buys
// nothing at the one moment it is needed: the last source going away under a
// telemetry poll or a scrape. Once the delete guard is gone that stops being a
// shutdown-only window and becomes an ordinary operator action in another tab.
//
// This is a SOURCE inspector rather than a race, because the window is a few
// instructions wide and a test that waits for it would be a flake either way.
// The shape is what is wrong and the shape is what is pinned. The safe form is
// one call, captured:
//
//	e := s.engOrNil()
//	if e == nil { ... }
//	return e.Hub().Stats()
//
// Two rules, because the first one alone can be written around. A nil-tested
// accessor must be read once, AND a named set of functions -- the ones that
// have to answer on an install with no source -- must read it once however
// they are written, nil comparison or not.
//
// Functions in neither set are deliberately uncovered: they panic on a nil
// receiver today by design, and refusing at the boundary is the next commit in
// this stack.
func TestAnEngineThatIsNilTestedIsReadExactlyOnce(t *testing.T) {
	fset := token.NewFileSet()
	// The package's own directory. Nothing here walks the repository, so no
	// worktree under .claude/ can be picked up as a second copy of it.
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse the api package: %v", err)
	}

	// The functions that have to answer for an absent engine. Each is named
	// rather than derived, because the property is about what these do and a
	// derivation would go quiet the day one of them was rewritten in a way the
	// derivation stopped recognising. A rename here is a prompt to re-read the
	// function, not a licence to drop it.
	mustCaptureOnce := map[string]bool{
		"ingestBitrate":      true,
		"relayStats":         true,
		"playoutManager":     true,
		"hlsHandler":         true,
		"handleListClips":    true,
		"handleDownloadClip": true,
		"clipTracks":         true,
	}

	seen := map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				calls, nilTested := engineReads(fn.Body)
				where := fset.Position(fn.Pos())
				for name, n := range calls {
					// The general rule: a nil-tested accessor is read once.
					if nilTested[name] && n > 1 {
						t.Errorf("%s: %s calls s.%s() %d times and compares one of them "+
							"against nil. The engine set changes between the two calls, so "+
							"the guard tests one engine and the body dereferences another. "+
							"Capture it once: e := s.%s(); if e == nil { ... }; e.Method()",
							where, fn.Name.Name, name, n, name)
					}
					// The named rule, which does not need a nil comparison to
					// bite: these functions read the engine at most once,
					// however they are written.
					if mustCaptureOnce[fn.Name.Name] && n > 1 {
						t.Errorf("%s: %s is one of the functions that must answer on an "+
							"install with no source, and it reads s.%s() %d times. Two reads "+
							"are two different engine sets: the last source can be deleted "+
							"between them.", where, fn.Name.Name, name, n)
					}
				}
				if mustCaptureOnce[fn.Name.Name] {
					seen[fn.Name.Name] = true
				}
			}
		}
	}

	for name := range mustCaptureOnce {
		if !seen[name] {
			t.Errorf("%s is named here as a function that must read the engine once, and "+
				"it no longer exists under that name. Find what replaced it and put the "+
				"replacement in this list, or the rule has quietly stopped applying to it.",
				name)
		}
	}
}

// engineReads counts calls to each engine accessor in a body, and reports which
// of them appear as an operand of a nil comparison.
func engineReads(body *ast.BlockStmt) (calls map[string]int, nilTested map[string]bool) {
	calls, nilTested = map[string]int{}, map[string]bool{}

	name := func(n ast.Node) string {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return ""
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || len(call.Args) != 0 {
			return ""
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok || recv.Name != "s" || !engineAccessors[sel.Sel.Name] {
			return ""
		}
		return sel.Sel.Name
	}

	ast.Inspect(body, func(n ast.Node) bool {
		if got := name(n); got != "" {
			calls[got]++
		}
		bin, ok := n.(*ast.BinaryExpr)
		if !ok || (bin.Op != token.EQL && bin.Op != token.NEQ) {
			return true
		}
		for _, pair := range [][2]ast.Expr{{bin.X, bin.Y}, {bin.Y, bin.X}} {
			if id, ok := pair[1].(*ast.Ident); !ok || id.Name != "nil" {
				continue
			}
			if got := name(pair[0]); got != "" {
				nilTested[got] = true
			}
		}
		return true
	})
	return calls, nilTested
}
