package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// EVERY RATCHET FIELD MUST BE CLAMPED, AND THE CLAMP DIRECTION IS DERIVED FROM
// SOURCE RATHER THAN TRUSTED.
//
// The bug #173's predecessor shipped was an ASSIGNMENT where a clamp belonged:
//
//	out.ExcusedCeiling = totals.Excused        // banked whatever it saw
//	out.ExcusedCeiling = min(totals.Excused, prev.ExcusedCeiling)   // ratchets
//
// It looked equivalent because the caller asserts the ceiling too -- but
// writeLedger writes the file BEFORE that assertion runs, so `-update-coverage`
// banked the raised number and the next plain run passed against it. Fail,
// do what the message says, regenerate, re-run, green. No bad intent required.
//
// #173 fixed the instances and wrote the rule in a comment. A comment is not
// joined to anything that emits: the next field added to writeLedger can be a
// bare assignment and nothing fails, which is how the first one arrived.
//
// So the rule becomes a derivation. This parses writeLedger's own AST and
// requires that every `out.<Field> = ...` statement is wrapped in min() or
// max(), in the direction declared below. A NEW ratchet field is a failure
// until somebody states which way it is allowed to move -- an obligation added
// by a source-derived set, never a discharge granted by one.
//
// This is a compile-independent check on purpose: it reads the file, so it
// fails even for a field whose clamp was removed in a way that still compiles,
// which is precisely the mutation that shipped.

// ratchetDirection is the committed intent for each clamped field.
//
// Ceilings use min: they may FALL freely on regeneration and rise only by hand
// edit. The floor uses max: it may RISE freely and fall only by hand edit. The
// asymmetry is the whole guarantee -- a ceiling that could rise on regeneration
// launders drift, and a floor that could fall launders a blanked fixture.
// It also covers the COMPOSITE LITERAL, which the first version of this check
// did not. writeLedger sets nine fields through `out := coverageLedger{...}`
// and the walker matched only *ast.AssignStmt, so adding
//
//	ProbeCeiling: totals.NonTrieProbes,
//
// to that literal -- unclamped, undeclared -- passed. "A NEW ratchet field is a
// failure until somebody states which way it is allowed to move" was true only
// of fields set on a line the walker happened to recognise. So every field
// written by writeLedger, in either syntax, must appear below; the fields that
// are measurements rather than ratchets say so, which is what makes the map
// complete over the struct rather than over one statement form.
var ratchetDirection = map[string]string{
	"ExcusedCeiling":                "min",
	"CounterpartlessExcusedCeiling": "min",
	"InertCeiling":                  "min",
	"UnstableCeiling":               "min",
	"VarianceExemptCeiling":         "min",
	"ShapesNotInspectedCeiling":     "min",
	"DifferentialFloor":             "max",
	"SentinelWitnessFloor":          "max",
	"NonGetDifferentialFloor":       "max",
	"ShapeFloor":                    "max",
	// MEASUREMENTS. Regeneration may refresh these wholesale because each is
	// compared, element by element, by an assertion that runs on every plain
	// run: routes and sweepVerdicts by assertRouteSetsEqual and
	// assertSweepVerdictsEqual, the two scalar blocks by
	// assertCoverageTotalsEqual and assertPartitionTotalsEqual, the three array
	// sections by assertKeyedRowsEqual, and the note by a direct comparison
	// against ledgerNote(t). CitedIssues is the odd one: it is regenerated only
	// in the direction that REMOVES, by intersectCitations.
	"Note":          "measurement",
	"Totals":        "measurement",
	"Partition":     "measurement",
	"Routes":        "measurement",
	"SweepVerdicts": "measurement",
	"Excuses":       "measurement",
	"Shapes":        "measurement",
	"Deferred":      "measurement",
	"CitedIssues":   "measurement",
}

// TestEveryRatchetFieldIsClampedNotAssigned is a thin wrapper. The work is in
// assertRatchetFieldsAreClamped, which TestLedgerPreflight CALLS -- so this
// file cannot be deleted without breaking the build, and the check runs under
// the -run filter the preflight forces. Both were holes: `rm
// internal/api/ledger_ratchet_test.go` left the suite green, because
// ratchetDirection was referenced by nothing outside its own file.
func TestEveryRatchetFieldIsClampedNotAssigned(t *testing.T) {
	assertRatchetFieldsAreClamped(t)
}

func assertRatchetFieldsAreClamped(t *testing.T) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "route_ledger_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse route_ledger_test.go: %v", err)
	}

	var writeLedgerFn *ast.FuncDecl
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name != nil && fn.Name.Name == "writeLedger" {
			writeLedgerFn = fn
		}
	}
	if writeLedgerFn == nil {
		t.Fatal("no writeLedger in route_ledger_test.go. This check reads that " +
			"function's AST; if it has been renamed, every assertion below would " +
			"pass having examined nothing.")
	}

	// how records, for each field writeLedger writes, the clamp that wraps it.
	// "" means a bare assignment or a literal element that is not a call.
	// A clamp call that does not read prev.<Field> is recorded as
	// "<name>-without-prev", because it is not a ratchet however it is spelled.
	seen := map[string]string{}
	record := func(field string, rhs ast.Expr) {
		clamp := ""
		if call, ok := rhs.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok {
				clamp = id.Name
				// THE CALLEE IS NOT THE CLAMP. min() and max() ratchet only
				// because one operand is the COMMITTED value. Measured:
				//
				//	out.ExcusedCeiling = min(totals.Excused, totals.Excused)
				//
				// passed this check and the full strict suite. It banks
				// whatever it measures -- behaviourally identical to the bare
				// assignment this guard exists to prevent, wearing a min().
				// So the operands are read too, and prev.<Field> is required
				// among them.
				if clamp != "" && !readsPrevField(call.Args, field) {
					clamp += "-without-prev"
				}
			}
		}
		seen[field] = clamp
	}
	ast.Inspect(writeLedgerFn, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				base, ok := sel.X.(*ast.Ident)
				if !ok || base.Name != "out" {
					continue
				}
				if i < len(node.Rhs) {
					record(sel.Sel.Name, node.Rhs[i])
				} else {
					seen[sel.Sel.Name] = ""
				}
			}
		case *ast.CompositeLit:
			// `out := coverageLedger{Field: expr, ...}`. Nine fields reach the
			// artifact this way and the first version of this walker saw none
			// of them.
			id, ok := node.Type.(*ast.Ident)
			if !ok || id.Name != "coverageLedger" {
				return true
			}
			for _, el := range node.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				record(key.Name, kv.Value)
			}
		}
		return true
	})

	// The deriver's positive control, and it is a FLOOR rather than "> 0" now,
	// because the walker's failure mode this round was seeing some of the
	// fields rather than none. A composite-literal blindness that hid nine
	// fields passed the old "len(seen) == 0" check comfortably.
	if len(seen) < len(ratchetDirection) {
		t.Fatalf("this walker found %d of writeLedger's %d declared fields (%v). Either "+
			"the ratchets moved, or the walker stopped matching a syntax they use -- "+
			"and a check that reads only part of the function is the exact defect that "+
			"let an unclamped field into the composite literal.",
			len(seen), len(ratchetDirection), sortedKeys(seen))
	}

	fields := make([]string, 0, len(seen))
	for k := range seen {
		fields = append(fields, k)
	}
	sort.Strings(fields)

	for _, field := range fields {
		clamp := seen[field]
		got := clamp
		switch {
		case got == "":
			got = "a bare assignment"
		case strings.HasSuffix(got, "-without-prev"):
			got = strings.TrimSuffix(got, "-without-prev") +
				"(), whose arguments never read prev." + field
		default:
			got += "()"
		}
		want, declared := ratchetDirection[field]
		if !declared {
			t.Errorf("writeLedger writes out.%s and ratchetDirection does not say which "+
				"way it is allowed to move.\nEvery number this artifact carries is either "+
				"a measurement, which regeneration may refresh because something else "+
				"compares it, or a RATCHET, which regeneration may only tighten. Add %q "+
				"to ratchetDirection with \"min\" (a ceiling: may fall freely, rises by "+
				"hand edit), \"max\" (a floor: may rise freely, falls by hand edit), or "+
				"\"measurement\" -- and if you choose measurement, name the assertion "+
				"that compares it in the comment, because an unclamped field nothing "+
				"compares is a number the artifact asserts about itself.", field, field)
			continue
		}
		switch want {
		case "min", "max":
			if clamp != want {
				t.Errorf("writeLedger sets out.%s with %s, and it must be %s(<live>, prev.%s).\n"+
					"This is the exact mutation this ledger's predecessor shipped. "+
					"writeLedger runs BEFORE the ceiling assertions, so a value that does "+
					"not read the COMMITTED number banks whatever the current run "+
					"measured; the next plain run then passes against the laundered "+
					"number, and the evidence is a two-character diff inside 2000 lines "+
					"of JSON. min(x, x) is not a clamp -- the callee's name is not the "+
					"ratchet, the operand is.", field, got, want, field)
			}
		case "measurement":
			if clamp == "min" || clamp == "max" {
				t.Errorf("ratchetDirection calls out.%s a measurement and writeLedger clamps "+
					"it with %s. One of the two is wrong: a clamped field is a ratchet and "+
					"belongs in the map as \"min\" or \"max\".", field, clamp)
			}
		default:
			t.Errorf("ratchetDirection says %q for out.%s, which is not min, max or "+
				"measurement.", want, field)
		}
	}

	for _, field := range sortedKeys(ratchetDirection) {
		if _, ok := seen[field]; !ok {
			t.Errorf("ratchetDirection declares %s and writeLedger never sets it. Either "+
				"the field was removed -- delete the row -- or it stopped being "+
				"regenerated, in which case the committed value is now frozen prose "+
				"rather than a ratchet.", field)
		}
	}
}

// readsPrevField reports whether any argument is `prev.<field>`, at any depth.
// The depth matters: min(a, max(b, prev.X)) is a legitimate spelling, and a
// check that only looked at top-level arguments would reject it.
func readsPrevField(args []ast.Expr, field string) bool {
	found := false
	for _, a := range args {
		ast.Inspect(a, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			base, ok := sel.X.(*ast.Ident)
			if ok && base.Name == "prev" && sel.Sel.Name == field {
				found = true
			}
			return true
		})
	}
	return found
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
