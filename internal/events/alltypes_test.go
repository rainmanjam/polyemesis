package events

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestAllTypesIsTheWholeConstBlock keeps AllTypes honest by PARSING the
// declaration rather than by trusting whoever last added a Type to have
// remembered this list.
//
// AST, not a grep over source text (#107): a text search would match the
// identifier wherever it appeared -- in this test, in a comment -- and would
// pass just as happily against a const block that had drifted.
//
// The consequence of drift is not cosmetic. internal/api keys its WebSocket
// redaction policy on Type and requires an entry for every member of AllTypes,
// so a Type missing from here is a Type nobody was forced to classify, sent
// unredacted to whatever principal is listening.
func TestAllTypesIsTheWholeConstBlock(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "events.go", nil, 0)
	if err != nil {
		t.Fatalf("parse events.go: %v", err)
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
			if !ok || id.Name != "Type" {
				continue
			}
			for _, n := range vs.Names {
				declared[n.Name] = true
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("the AST walk found no `X Type = \"y\"` constants at all; this guard is " +
			"looking at the wrong thing and would pass whatever happened to the const block")
	}

	listed := map[string]bool{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "AllTypes" || fn.Recv != nil {
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
			t.Errorf("events.%s is declared but not returned by AllTypes(). Every consumer "+
				"that must have an opinion about each event type -- internal/api's WebSocket "+
				"policy table above all -- reads AllTypes, so a type missing from it is a "+
				"type nobody is required to classify and which is therefore sent to every "+
				"principal by default.", name)
		}
	}
	for name := range listed {
		if !declared[name] {
			t.Errorf("AllTypes() returns %s, which is no longer a declared Type", name)
		}
	}
	if got, want := len(AllTypes()), len(declared); got != want {
		t.Errorf("AllTypes() has %d entries and the const block declares %d; a duplicate "+
			"or a missing entry", got, want)
	}
}
