package db

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// Every Migrate* call in Open closes the handle, and wraps its error unless it
// is recorded here as already carrying its own context.
//
// Nine of them did and two did not, and the two that did not were the two most
// recently added -- because the way you write a migration here is to copy the
// one above it, and both had been copied from each other rather than from the
// nine. A leaked handle on a path that is about to exit is small; an
// inconsistency that the next copy inherits is not.
//
// An AST check rather than a lint rule because there is no golangci-lint config
// in this repo, and rather than a comment because a comment is what the two
// wrong ones already had above them.
//
// Warning rung, not Control: Go cannot express "this call must be followed by
// those two statements". Control would need the migrations behind a runner that
// owns the handle -- worth doing when there is a tenth, not for the eleventh
// line of a fix.
func TestEveryMigrationInOpenClosesAndWraps(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "db.go", nil, 0)
	if err != nil {
		t.Fatalf("parse db.go: %v", err)
	}

	var open *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Name.Name == "Open" && fn.Recv == nil {
			open = fn
		}
		return true
	})
	if open == nil {
		t.Fatal("no top-level Open in db.go; this test guards its migration block and has lost it")
	}

	var bare []string
	ast.Inspect(open.Body, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok || ifs.Init == nil {
			return true
		}
		name := migrateCallName(ifs.Init)
		if name == "" {
			return true
		}
		closes, wraps := false, false
		ast.Inspect(ifs.Body, func(m ast.Node) bool {
			if sel, ok := m.(*ast.SelectorExpr); ok && sel.Sel.Name == "Close" {
				closes = true
			}
			if lit, ok := m.(*ast.BasicLit); ok && strings.Contains(lit.Value, "migrate: %w") {
				wraps = true
			}
			return true
		})
		// CLOSING is the hard requirement: it is the actual resource leak, and
		// it is what the two copied-from-each-other migrations were missing.
		if !closes {
			bare = append(bare, name+" (does not close the handle)")
		}
		// Wrapping is about the message an operator reads, so a migration whose
		// own error already names the operation is allowed to skip it -- but it
		// has to say so here, where the next reader will see it.
		if !wraps && !wrapExempt[name] {
			bare = append(bare, name+" (does not wrap with \"migrate: %w\")")
		}
		return true
	})

	// A scan that finds nothing agrees with any expectation at all.
	if got := countMigrateCalls(open.Body); got < 5 {
		t.Fatalf("found only %d Migrate* calls in Open; the scan has stopped "+
			"matching and would pass however the block was written", got)
	}

	sort.Strings(bare)
	if len(bare) > 0 {
		t.Errorf("these migrations neither close the handle nor wrap their error:\n  %s\n\n"+
			"Every other one does `sqldb.Close()` and `fmt.Errorf(\"migrate: %%w\", err)`. "+
			"The next migration will be written by copying one of these, which is how "+
			"the last two came to be wrong.", strings.Join(bare, "\n  "))
	}
}

// wrapExempt records the migrations whose own error is already specific enough
// that "migrate: " would only add a prefix. An entry here is a claim someone
// checked; it is not a way to quiet the test.
var wrapExempt = map[string]bool{
	// Returns `stamp schema version %d: %w`, which already names what failed
	// and at which version. See MigrateSchemaVersion.
	"MigrateSchemaVersion": true,
}

func migrateCallName(init ast.Stmt) string {
	as, ok := init.(*ast.AssignStmt)
	if !ok || len(as.Rhs) != 1 {
		return ""
	}
	call, ok := as.Rhs[0].(*ast.CallExpr)
	if !ok {
		return ""
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !strings.HasPrefix(sel.Sel.Name, "Migrate") {
		return ""
	}
	return sel.Sel.Name
}

func countMigrateCalls(body *ast.BlockStmt) int {
	n := 0
	ast.Inspect(body, func(node ast.Node) bool {
		if ifs, ok := node.(*ast.IfStmt); ok && ifs.Init != nil && migrateCallName(ifs.Init) != "" {
			n++
		}
		return true
	})
	return n
}
