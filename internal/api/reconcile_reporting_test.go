package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// THERE IS ONE SPELLING OF "APPLY THIS MUTATION", AND IT CANNOT BE DROPPED. #709.
//
// Sixteen handlers called s.reconcile() and two spellings lived side by side in
// this package: three turned the error into a 500, twelve logged it at Warn and
// returned success. Nothing in the signature said which was right, so a handler
// written by copying its nearest neighbour got whichever that neighbour was.
//
// The silent spelling is worse than it looks. Engine.Reconcile returns early on
// a reconcileOutputs error, so preview, clips, captions and loudness are skipped
// with it, and reconcile is EVENT-DRIVEN WITH NO TICKER -- so the failure is
// never retried. Stored state and the running FFmpeg diverge until the next
// successful mutation or a restart, while the response said 200 and the UI
// raised a green toast. The worst case was a destination delete: the row left
// the list, the response said "deleted", and the child kept publishing.
//
// Two rules, both checkable at build time, and together they remove the choice:
//
//  1. s.reconcile() has exactly one caller, reconcileNow. There is no second
//     spelling left to copy.
//  2. A bare `s.reconcileNow(...)` STATEMENT fails. Go will let you discard a
//     returned string, and discarding this one is precisely the mistake -- so
//     the call must appear in an assignment or a condition, which forces the
//     author to say what happens to it.
func TestReconcileHasOneSpellingAndItsResultCannotBeDropped(t *testing.T) {
	root := packageRoot(t)
	fset := token.NewFileSet()

	var (
		rawCallers []string // functions calling s.reconcile() directly
		dropped    []string // bare `s.reconcileNow(...)` statements
		users      int      // calls whose result is used
	)

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading %s: %v", root, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(root, e.Name())
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("parsing %s: %v", path, perr)
		}

		var fnName string
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncDecl:
				fnName = node.Name.Name
			case *ast.ExprStmt:
				// A CALL AS A STATEMENT is a discarded result. This is the only
				// shape that drops the warning, and it is the whole rule.
				if isServerCall(node.X, "reconcileNow") {
					dropped = append(dropped,
						e.Name()+":"+itoaLine(fset, node.Pos())+" in "+fnName)
				}
			case *ast.CallExpr:
				if isServerCall(node, "reconcile") {
					rawCallers = append(rawCallers, e.Name()+":"+itoaLine(fset, node.Pos())+" in "+fnName)
				}
				if isServerCall(node, "reconcileNow") {
					users++
				}
			}
			return true
		})
	}

	// THE WALKER MUST FIND THINGS, or every assertion below passes over an
	// empty set and reports a rule it never checked.
	if users < 10 {
		t.Fatalf("found only %d call(s) to reconcileNow in %s; the walker is broken "+
			"and this test is asserting nothing", users, root)
	}

	sort.Strings(rawCallers)
	if len(rawCallers) != 1 || !strings.Contains(rawCallers[0], "reconcileNow") {
		t.Errorf("s.reconcile() has %d caller(s) and should have exactly one, "+
			"reconcileNow:\n  %s\n\n"+
			"A second caller is a second spelling, and the difference between them "+
			"-- 500 or a silent 200 -- is invisible in the signature. Call "+
			"reconcileNow and decide what to do with the sentence it returns.",
			len(rawCallers), strings.Join(rawCallers, "\n  "))
	}

	sort.Strings(dropped)
	if len(dropped) > 0 {
		t.Errorf("these calls discard the reconcile warning:\n  %s\n\n"+
			"A discarded warning is a handler that answers 200 for a change that did "+
			"not take effect. Nothing retries the reconcile, so the divergence lasts "+
			"until the next successful save or a restart. Pass the result to "+
			"writeMutation, writeMutationNoContent, or a writeError.",
			strings.Join(dropped, "\n  "))
	}
}

// isServerCall reports whether call is `<recv>.<name>(...)`.
func isServerCall(n ast.Node, name string) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "s"
}

func itoaLine(fset *token.FileSet, p token.Pos) string {
	n := fset.Position(p).Line
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func packageRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return dir
}
