package jobs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Poka-yoke audit #15: adding a State constant without updating every place
// that has an opinion about State used to be invisible -- Terminal and Valid
// were switch statements with their own hand-kept case lists, and Go does not
// check a string-typed switch for exhaustiveness. This file is the exact
// pattern internal/events/alltypes_test.go uses for events.Type, applied here:
// parse the const block instead of trusting AllStates to have kept up with it.
//
// TestAllStatesIsTheWholeConstBlock is Control-rung on its own: AllStates can
// no longer silently omit a state, because this fails the moment it does.
// TestEveryStateHasATerminalEntry then rides on that guarantee to make
// stateTerminal exhaustive too -- Terminal's classification is a judgment call
// that can't be derived from the list alone, so it still needs its own map,
// but the map's COVERAGE is now enforced rather than assumed.
func TestAllStatesIsTheWholeConstBlock(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "jobs.go", nil, 0)
	if err != nil {
		t.Fatalf("parse jobs.go: %v", err)
	}

	declared := map[string]bool{}
	for _, d := range f.Decls {
		gen, ok := d.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			id, ok := vs.Type.(*ast.Ident)
			if !ok || id.Name != "State" {
				continue
			}
			for _, n := range vs.Names {
				declared[n.Name] = true
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("the AST walk found no `X State = \"y\"` constants at all; this guard is " +
			"looking at the wrong thing and would pass whatever happened to the const block")
	}

	listed := map[string]bool{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "AllStates" || fn.Recv != nil {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && declared[id.Name] {
				listed[id.Name] = true
			}
			return true
		})
	}

	for name := range declared {
		if !listed[name] {
			t.Errorf("jobs.%s is declared but not returned by AllStates(). Valid() is "+
				"derived from AllStates, so a state missing here is a state the queue will "+
				"refuse to write and reject as unknown -- and TestEveryStateHasATerminalEntry "+
				"cannot see it either, so it also gets no Terminal() classification.", name)
		}
	}
	for name := range listed {
		if !declared[name] {
			t.Errorf("AllStates() returns %s, which is no longer a declared State", name)
		}
	}
	if got, want := len(AllStates()), len(declared); got != want {
		t.Errorf("AllStates() has %d entries and the const block declares %d; a duplicate "+
			"or a missing entry", got, want)
	}
}

// TestEveryStateHasATerminalEntry closes the other half of #15: a state
// present in AllStates but absent from stateTerminal used to read as
// Terminal()==false by Go's ordinary map zero-value, indistinguishable from a
// state someone deliberately classified as non-terminal. Checked with the
// two-value map form specifically so a missing key is caught as missing,
// not read as a (possibly wrong) false.
func TestEveryStateHasATerminalEntry(t *testing.T) {
	for _, s := range AllStates() {
		if _, ok := stateTerminal[s]; !ok {
			t.Errorf("State %q has no entry in stateTerminal. A job left in this state is "+
				"either cleaned up as if it were done, or retried forever as if it were "+
				"still active, depending on which way the missing entry's zero value "+
				"happens to fall -- classify it explicitly.", s)
		}
	}
}
