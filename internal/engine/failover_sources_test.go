package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"testing"
)

// FailoverSources feeds the metrics series (one per source, at 0 until used).
// A sourceKind constant it forgets would never get a series, so it is held to
// the const block rather than to a second hand list.
func TestFailoverSourcesCoverEverySourceKind(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "selector.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var consts []string
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "sourceKind" {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if ok && lit.Value != `""` {
				consts = append(consts, lit.Value[1:len(lit.Value)-1])
			}
		}
	}
	got := FailoverSources()
	if len(consts) == 0 || !slices.Equal(consts, got) {
		t.Errorf("FailoverSources() = %v, the sourceKind constants say %v", got, consts)
	}
}
