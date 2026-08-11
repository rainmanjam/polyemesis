package api

// ISSUE #204. THE GUARD OVER THE GUARD, and the smallest version of it that is
// worth the machinery.
//
// TestBlankingEveryShapeNoteChangesNoVerdict proves that shapeVerdict() ignores
// prose. It does not prove that step 7, which CALLS shapeVerdict, ignores
// prose. #204 demonstrated the difference with two realistic lines on
// fix/frame-completeness:
//
//	// step 7, beside the existing check -- shapeVerdict itself untouched
//	if strings.Contains(sh.Note, "Deferred:") { continue }
//
//	// and one row moves its deferral back into prose
//	{"slog-output", true, false, "", "", "... Deferred: #160"}
//
// `-update-coverage` came back ok, the full strict suite came back ok 54.897s,
// and TestBlankingEveryShapeNoteChangesNoVerdict PASSED. Prose discharge was
// back in the ledger and every guard written to prevent it was green.
//
// WHY THIS SHAPE. #204 names the fix -- "enumerate the call sites of
// shapeVerdict from the AST and assert none of them reads Note" -- and rejects
// it as a fifth layer over a system already at 4,239 lines. What is written
// here is the fix WITHOUT the layer: no registry, no ratchet, no committed
// artifact, no regeneration command. It is one AST walk over this package's own
// source, about a hundred lines, and it holds a property no amount of testing
// shapeVerdict in isolation can reach.
//
// THE RULE, stated so nobody has to infer it from the code:
//
//	A value whose verdict is taken by shapeVerdict must not have its Note read
//	anywhere in the same function.
//
// The subject is the VALUE, not the function, and that is what keeps the rule
// exact without a type checker. `shapeVerdict(sh)` puts `sh` under the rule;
// `want.Note` two hundred lines away is the LEDGER's note, a different field on
// a different type, and it is compared against the live build on purpose --
// flagging it would be a false positive and the first step towards an
// allowlist.
//
// Reads are refused in every position: in a condition, in a message, passed to
// a helper. Not because a message is dangerous, but because "only inside a
// t.Errorf" needs a list of which calls count as formatting, and a list is the
// seventh free pass this repository has invented. Step 7's message names
// sh.Shape and sh.Issue and does not need the note, so the strict form costs
// nothing today.
//
// WRITES ARE ALLOWED, narrowly: only the left-hand side of an assignment.
// `blanked[i].Note = ""` is how the sibling proof mutates the registry and is
// the reason the exemption exists at all. `note := sh.Note` is a read that
// happens to look like one and is not exempted.
//
// WHAT IT DELIBERATELY DOES NOT CATCH, so a green run is not read as more than
// it is: a helper called BY step 7 that reads Note itself. The rule is about
// the callers of the verdict function, which is where #204's regression landed
// and where the ledger's own control flow lives. A prose check pushed one call
// deeper would need a full call graph, and that is the fifth layer #204
// declined -- correctly.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// verdictFuncs are the two entry points step 7's decision flows through.
var verdictFuncs = map[string]bool{"shapeVerdict": true, "shapeVerdicts": true}

// canaryFunc is the one function in this package that is ALLOWED to read a
// shape's note, and required to.
const canaryFunc = "noteCanary"

// noteCanary is a violation on purpose. It exists to be found.
//
// THE HARNESS CAN GO BLIND AND STILL PRINT ok, which is the failure mode a
// guard over guards is most prone to and the one #177 is about: a walk whose
// subject set never populates, a write exemption that swallows reads, a
// ParseDir pointed at the wrong directory, an ast.Inspect that returns false
// too early. Every one of those produces an empty result set, and an empty
// result set satisfies "no caller reads the note" perfectly.
//
// So one read is planted where the rule says a read must never be, and the
// test below REQUIRES to see it -- once, at this line. If the canary ever comes
// back clean, the walk is lying and the whole guard is void, whatever it says
// about the real callers.
//
// It is never called, and that is the point: it changes no behaviour and can
// never discharge a shape. It is a fixture for the AST, not for the ledger.
//
//nolint:unused // read by the AST walk below, not by the compiler
func noteCanary(shapes []coverageShape) string {
	for _, sh := range shapes {
		if shapeVerdict(sh) == "deferred" {
			return sh.Note
		}
	}
	return ""
}

// noteRead is one place a caller of shapeVerdict looks at prose.
type noteRead struct {
	fn   string
	file string
	line int
	src  string
}

func TestNoCallerOfShapeVerdictReadsTheNote(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse this package: %v", err)
	}

	var callers, judged []string
	var reads []noteRead

	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				subjects := verdictSubjects(fn.Body)
				if len(subjects) == 0 {
					continue
				}
				callers = append(callers, fn.Name.Name)
				for name := range subjects {
					judged = append(judged, fn.Name.Name+":"+name)
				}
				for _, sel := range noteReadsIn(fn.Body, subjects) {
					pos := fset.Position(sel.Pos())
					reads = append(reads, noteRead{
						fn: fn.Name.Name, file: path, line: pos.Line,
						src: exprText(sel),
					})
				}
			}
		}
	}

	// THE POSITIVE CONTROL, and this guard is worth nothing without it. Every
	// assertion below is "no caller does X"; a derivation that found no callers
	// at all -- shapeVerdict renamed, the ledger split into another package,
	// ParseDir handed a directory with nothing in it -- satisfies that
	// perfectly while having examined nothing.
	//
	// Three is what the package carries today: shapeVerdicts, step 7's
	// enclosing test, and the blanking proof. Two is the floor, because a
	// derivation that can only see the trivial wrapper is not seeing the
	// caller this issue is about.
	sort.Strings(callers)
	sort.Strings(judged)
	if len(callers) < 2 {
		t.Fatalf("the AST walk found %d caller(s) of shapeVerdict/shapeVerdicts (%v) in this "+
			"package. This guard asserts a property OF those callers, so with fewer than two "+
			"it is an assertion about an empty set -- which is the exact failure mode "+
			"TestBlankingEveryShapeNoteChangesNoVerdict shipped with for a whole round. "+
			"Either the ledger moved out of this package, or the verdict function was "+
			"renamed; in both cases this guard must be repointed rather than left green.",
			len(callers), callers)
	}
	t.Logf("shapeVerdict callers under guard: %v", callers)
	t.Logf("values whose prose is refused: %v", judged)

	// THE CANARY, checked before the verdict and not after. A walk that has
	// gone blind reports nothing, and "nothing" is indistinguishable from a
	// clean package unless something is KNOWN to be dirty.
	var canary, real []noteRead
	for _, r := range reads {
		if r.fn == canaryFunc {
			canary = append(canary, r)
			continue
		}
		real = append(real, r)
	}
	if len(canary) != 1 {
		t.Fatalf("the canary read in %s was observed %d time(s), want exactly 1. This walk "+
			"cannot see a note being read where the rule forbids it, so its verdict on the "+
			"real callers below is worthless -- a blinded harness and a clean package "+
			"produce the same empty result. Fix the walk, or, if the canary was deleted, "+
			"understand that deleting it is how this guard is silently switched off.",
			canaryFunc, len(canary))
	}
	t.Logf("canary observed at %s:%d (%s) -- the walk can see a violation",
		canary[0].file, canary[0].line, canary[0].src)

	for _, r := range real {
		t.Errorf("%s:%d: %s reads %s, and it calls shapeVerdict.\n"+
			"A shape's coverage is a fact about what runs and about which issue defers it. "+
			"The moment a caller of the verdict function consults prose, the discharge "+
			"stops being reachable by the citation form check and the git liveness scan, "+
			"and TestBlankingEveryShapeNoteChangesNoVerdict cannot see it: that test proves "+
			"shapeVerdict ignores Note, not that its CALLERS do. That is issue #204, and it "+
			"was demonstrated with `if strings.Contains(sh.Note, \"Deferred:\") { continue }` "+
			"sitting beside the existing check while the whole strict suite came back green.\n"+
			"Put the deferral in the Issue field. If prose genuinely has to be read here, "+
			"this guard is the thing to argue with.",
			r.file, r.line, r.fn, r.src)
	}
}

// verdictSubjects returns the base identifiers of every argument handed to a
// verdict function anywhere in the body, including from inside a nested
// function literal -- a closure is still that function's control flow.
//
// `shapeVerdict(sh)` yields "sh"; `shapeVerdicts(blanked)` yields "blanked";
// `shapeVerdict(shapes[i])` yields "shapes". The base is what the rule attaches
// to, because that is the thing whose coverage is being decided.
func verdictSubjects(body *ast.BlockStmt) map[string]bool {
	subjects := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok || !verdictFuncs[id.Name] {
			return true
		}
		for _, arg := range call.Args {
			if base := baseIdent(arg); base != "" {
				subjects[base] = true
			}
		}
		return true
	})
	return subjects
}

// baseIdent walks left through indexes, selectors and stars to the identifier
// an expression is rooted at. Anything else (a call, a literal) has no name to
// attach a rule to and yields "".
func baseIdent(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return x.Name
		case *ast.IndexExpr:
			e = x.X
		case *ast.SelectorExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.UnaryExpr:
			e = x.X
		default:
			return ""
		}
	}
}

// noteReadsIn returns every `X.Note` selector rooted at one of the subjects and
// not the target of an assignment.
func noteReadsIn(body *ast.BlockStmt, subjects map[string]bool) []*ast.SelectorExpr {
	written := map[ast.Node]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range as.Lhs {
			if sel, ok := lhs.(*ast.SelectorExpr); ok {
				written[sel] = true
			}
		}
		return true
	})

	var out []*ast.SelectorExpr
	ast.Inspect(body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "Note" || written[sel] {
			return true
		}
		if !subjects[baseIdent(sel.X)] {
			return true
		}
		out = append(out, sel)
		return true
	})
	return out
}

// exprText renders a selector for the failure message. Only the shapes that
// actually occur are handled; anything else is reported as its field name,
// which still names the line and the function.
func exprText(sel *ast.SelectorExpr) string {
	switch x := sel.X.(type) {
	case *ast.Ident:
		return x.Name + ".Note"
	case *ast.IndexExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id.Name + "[...].Note"
		}
	case *ast.SelectorExpr:
		return exprText(x) + ".Note"
	}
	return strings.TrimSpace(".Note")
}
