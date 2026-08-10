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

// THE SHAPE REGISTRY'S `By` FIELD WAS RESOLVED BY NOTHING.
//
// coverageShape.By is a bare string naming the proof that inspects a shape, and
// until this change nothing anywhere read it. Step 7 of the preflight skips any
// row with Inspected==true without ever asking whether By names something that
// exists, so `{shape, true, true, "", note}` -- inspected by the empty string --
// passed silently.
//
// That is not hypothetical. Measured on this branch, three of the seven rows
// claiming Inspected:true named a proof that this package cannot run:
//
//	response-header/Location      -> TestAConfiguredRedirectNeverCachesAWatchToken
//	response-header/Cache-Control -> TestAConfiguredRedirectNeverCachesAWatchToken
//	    both live in cmd/polyemesis/redirect_test.go, package main. No test in
//	    package api runs them, so the ledger for internal/api recorded these two
//	    shapes as inspected by something outside its own reach.
//	response-header/Set-Cookie    -> TestPlayoutCookieHandoff
//	    no such function exists anywhere. The real one is
//	    TestPlayoutCookieHandoffSurvives.
//
// Same disease as the regeneration command in the commit before this one, and
// the same cure: derive the set of things that could satisfy the reference, and
// reconcile.
//
// This is the SCOPED form of the fix. The full form replaces By with an
// `Inspector func(*testing.T, shapeRig) shapeObservation` resolved at compile
// time and RUN by the ledger, so a shape has to witness itself rather than name
// a witness. That is filed as a follow-up; see the deferral row. What lands
// here is the part that makes the existing strings honest, which is what the
// three findings above actually needed.
//
// THE DIRECTION RULE, which applies to this file and to every source-derived
// set in this ledger: A SOURCE-DERIVED SET MAY ONLY ADD AN OBLIGATION, NEVER
// DISCHARGE ONE. Nothing below computes a verdict, marks a shape inspected, or
// satisfies a sweep. It can only fail. A deriver that could discharge would let
// a reference be satisfied by writing the right words in a comment, which is
// the free pass this replaces.

// symbolIndex is every top-level func declared in the repository's Go test and
// non-test files, mapped to the packages that declare it. Derived from ASTs, so
// a reference is checked against declarations rather than against text that
// happens to contain the name.
type symbolIndex map[string][]string // func name -> ["api", "main", ...]

func buildSymbolIndex(t *testing.T) symbolIndex {
	t.Helper()
	idx := symbolIndex{}
	// scripts/ carries 19 .go files and was excluded while the failure message
	// below said "anywhere in this repository". Direction-safe either way -- the
	// omission could only over-report -- but a message that overstates its own
	// reach is the species this ledger keeps finding.
	roots := []string{"..", "../../cmd", "../../internal", "../../scripts"}
	seen := map[string]bool{}
	fset := token.NewFileSet()
	files := 0
	for _, root := range roots {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			abs, aerr := filepath.Abs(path)
			if aerr != nil || seen[abs] {
				return nil
			}
			seen[abs] = true
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return nil
			}
			files++
			pkg := f.Name.Name
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || fn.Name == nil {
					continue
				}
				name := fn.Name.Name
				if !pkgListContains(idx[name], pkg) {
					idx[name] = append(idx[name], pkg)
				}
			}
			return nil
		})
	}
	// The deriver's own floor, in the same spirit as every other floor here: a
	// scan that reads nothing would make every reference below "unresolvable"
	// -- or, if the assertion were ever inverted, would make all of them pass.
	// Zero is never a legitimate answer.
	if files < 50 || len(idx) < 200 {
		t.Fatalf("symbol deriver read %d Go files and found %d distinct funcs; "+
			"that is not this repository. A deriver that finds nothing cannot "+
			"reconcile anything.", files, len(idx))
	}
	return idx
}

func pkgListContains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// shapeProofReferences pulls the identifiers out of a By string. The field is
// free text -- one row reads "websocketFrames + TestEveryEventTypeHasAWebSocketPolicy"
// -- so every space/plus-separated token that looks like a Go identifier is
// treated as a reference and must resolve.
func shapeProofReferences(by string) []string {
	var out []string
	for _, tok := range strings.FieldsFunc(by, func(r rune) bool {
		return r == ' ' || r == '+' || r == ',' || r == '\t'
	}) {
		tok = strings.TrimSpace(tok)
		if tok == "" || tok == "+" {
			continue
		}
		out = append(out, tok)
	}
	return out
}

func TestEveryInspectedShapeNamesAProofThisPackageCanRun(t *testing.T) {
	idx := buildSymbolIndex(t)
	tests := declaredTestNames(t)

	// Positive control on the deriver, before any absence check reads it. The
	// checks below are "no reference is unresolvable", and an index that
	// resolved everything -- or a reference extractor that produced no
	// references -- would pass them all while proving nothing.
	for _, known := range []string{
		"TestLedgerPreflight",                           // this package
		"TestAConfiguredRedirectNeverCachesAWatchToken", // package main
		"proveWebsocketFrames",                          // an unexported helper
	} {
		if len(idx[known]) == 0 {
			t.Fatalf("symbol index does not know %q, which is declared in this "+
				"repository. Every check below is an absence check and would "+
				"pass vacuously against an index this broken.", known)
		}
	}
	if got := shapeProofReferences("websocketFrames + TestEveryEventTypeHasAWebSocketPolicy"); len(got) != 2 {
		t.Fatalf("shapeProofReferences extracted %v from a two-reference By string; "+
			"an extractor that yields nothing makes every row below trivially clean", got)
	}
	// The counterpart-key escape hatch below must not be a hole big enough to
	// swallow the check: prove it is a real registry lookup and not a
	// "contains" that would let any string through.
	if _, ok := counterpartProofs["playoutManifestBytes"]; !ok {
		t.Fatal("counterpartProofs has no playoutManifestBytes; the key namespace " +
			"this check honours is not the one the registry uses")
	}
	if _, ok := counterpartProofs["NoSuchCounterpartProof"]; ok {
		t.Fatal("counterpartProofs resolved a name that does not exist; the escape " +
			"hatch below would discharge any reference at all")
	}

	for _, sh := range emittedShapes() {
		if !sh.Emitted || !sh.Inspected {
			continue
		}
		if strings.TrimSpace(sh.By) == "" {
			t.Errorf("the shape %q claims Inspected:true and names no proof at all. "+
				"`Inspected: true, By: \"\"` was accepted silently by every check in "+
				"this ledger: step 7 skips any inspected row without asking what "+
				"inspects it. A claim of coverage that names nothing is the free "+
				"pass this ledger exists to remove.", sh.Shape)
			continue
		}
		for _, ref := range shapeProofReferences(sh.By) {
			// A By reference may name a COUNTERPART PROOF KEY rather than a Go
			// function -- "playoutManifestBytes" is a key in counterpartProofs,
			// whose value is provePlayoutManifest. Found by running this check,
			// not by predicting it.
			//
			// That namespace resolves legitimately, and this is not a weakening:
			// a counterpartProofs key is already joined to an emitter at compile
			// time (the map value is a func), the ledger already requires every
			// excuse's key to exist in the registry, and it already fails any
			// proof in the registry that nothing references. A key is a real
			// binding; the bare test-name strings were not.
			if _, isProof := counterpartProofs[ref]; isProof {
				continue
			}
			pkgs, ok := idx[ref]
			if !ok {
				t.Errorf("the shape %q says it is inspected by %q, and no function of "+
					"that name is declared anywhere in this repository.\n"+
					"Nothing read this field until now, so the name was free to rot. "+
					"Point it at a real proof, or set Inspected:false and record the "+
					"deferral -- an honest \"not inspected\" is worth more than an "+
					"unresolvable claim that it is.", sh.Shape, ref)
				continue
			}
			if !pkgListContains(pkgs, "api") {
				t.Errorf("the shape %q says it is inspected by %q, which is declared "+
					"only in package %v -- not in package api.\n"+
					"This is the ledger for internal/api. A proof in another package "+
					"does not run when this ledger runs, cannot be reached by its "+
					"-run filter, and is not covered by the TestMain preflight that "+
					"gives these obligations jurisdiction. Either move the shape's "+
					"proof into this package, or mark the row not-inspected and say "+
					"where the real proof lives.", sh.Shape, ref, pkgs)
				continue
			}
			// AND IT MUST BE SOMETHING `go test` CAN RUN. "declared in package
			// api" was too weak by itself: `By: "readLedger"` -- a helper that
			// asserts nothing, is not a test, and can never be selected by a
			// -run filter -- resolved clean. That is wider than the "names a
			// proof but does not run it" case filed as #176; it is "names
			// something that is not a proof at all".
			if !tests[ref] {
				t.Errorf("the shape %q says it is inspected by %q, which is declared in "+
					"package api but is not a test function.\n"+
					"A By reference must be something this package can RUN: a Test "+
					"function, or a key in counterpartProofs, whose value is a func the "+
					"registry executes. A helper name resolves and proves nothing -- "+
					"`By: \"readLedger\"` passed every check in this file before this "+
					"line existed.", sh.Shape, ref)
			}
		}
	}
}

// TestBlankingEveryShapeNoteChangesNoVerdict is D's R2, scoped to shapes.
//
// THE PREVIOUS VERSION OF THIS TEST COULD NOT FAIL, and saying so plainly is
// the point of this comment. It blanked Note on the OUTPUT of emittedShapes()
// and re-ran shapeInspectionVerdicts, a helper added in the same commit,
// twenty lines below, commented "Deliberately does NOT consult Note." Blanking
// a field the reducer never reads cannot change the reducer's answer. It was a
// tautology wearing the name of a real guarantee, inside the file whose stated
// purpose is preventing exactly that, and it shipped green through an
// adversarial review and twenty CI checks.
//
// Worse, the property it announced was FALSE of the ledger at the time. The
// real discharge in step 7 was shapeDeferralIsDeclared(sh.Note) -- a substring
// search over an explanation. Mirroring the actual rule and blanking the notes
// moved six of eleven shape verdicts from discharged to failing. Prose decided.
//
// Two things had to change for this test to mean anything, and only one of them
// was in this file:
//
//  1. The discharge moved out of prose into coverageShape.Issue, a structured
//     field that also reaches citedIssues() and therefore the form check and the
//     git liveness scan.
//  2. This test now re-runs shapeVerdict -- THE function step 7 calls, not a
//     helper written to be this test's subject -- over the LIVE registry.
//
// That is what the route-level analogue, TestDeletingEveryProseReasonChanges-
// NoVerdict, has always done: mutate the real input, re-run the real
// classifier. The acceptance criterion for this test is that a mutation makes
// it fail. Add `strings.Contains(sh.Note, "Deferred")` to shapeVerdict and it
// fails by name on six shapes.
func TestBlankingEveryShapeNoteChangesNoVerdict(t *testing.T) {
	live := emittedShapes()

	// The positive control, and it is not optional here. Every assertion below
	// is "nothing changed"; a registry with no deferred rows, or a verdict
	// function collapsed to one answer, would satisfy all of them having
	// examined nothing. At least one shape must currently be discharged BY the
	// field this test claims is load-bearing.
	deferred := 0
	for _, sh := range live {
		if shapeVerdict(sh) == "deferred" {
			deferred++
		}
	}
	if deferred == 0 {
		t.Fatal("no shape in the registry is currently discharged by its Issue field, so " +
			"blanking the notes cannot demonstrate anything. This test asserts that the " +
			"deferral is structural rather than prose; with nothing deferred it is an " +
			"assertion about an empty set.")
	}

	before := shapeVerdicts(live)

	blanked := emittedShapes()
	for i := range blanked {
		blanked[i].Note = ""
	}
	after := shapeVerdicts(blanked)

	if len(before) != len(after) {
		t.Fatalf("blanking notes changed the shape COUNT: %d -> %d", len(before), len(after))
	}
	for _, shape := range sortedKeys(before) {
		if after[shape] != before[shape] {
			t.Errorf("blanking every note changed the ledger's verdict for the shape %q: "+
				"%q -> %q.\n"+
				"shapeVerdict is what step 7 acts on, and it must read Emitted, Inspected "+
				"and Issue -- never Note. A shape's coverage is a fact about what runs and "+
				"about which issue defers it; if prose can move this, prose can discharge "+
				"it, and the discharge stops being reachable by the citation form check "+
				"and the git liveness scan.\n"+
				"%d of %d shapes are currently discharged by Issue; move the deferral into "+
				"that field rather than into the note.", shape, before[shape], after[shape],
				deferred, len(live))
		}
	}
}
