package api

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// EVERY DESTINATION WRITER REPAIRS ON THE WAY PAST, AND IT IS CHECKED RATHER
// THAN ASKED FOR. #739.
//
// A destination could be stored linking a platform account belonging to a
// DIFFERENT platform. preannounce takes the broadcaster from the destination's
// own platform and the bearer token from its account, so the pairing sends a
// YouTube token to graph.facebook.com. dropUnsendableSettings repairs it, and
// the two editing routes call it.
//
// THE FIRST VERSION OF THIS FIX SHIPPED A SENTENCE INSTEAD OF THIS TEST. Its
// comment said the invariant "holds by inspection" and that any new
// destination-writing route must remember to call the helper. That is rung 0 --
// training, not a device -- and a reviewer refused the rung-1 claim that went
// with it, correctly: two routes already skipped the helper and merely happened
// not to assign Platform or AccountID. Both are routed through it now, and this
// is what keeps the fifth route from being written without it.
//
// WHAT THIS IS NOT. It is rung 2, not rung 1: it announces the mistake in CI
// rather than making it impossible. Control would mean moving the check inside
// db.CreateDestination and db.UpdateDestination, so no caller could write the
// mismatched pair at all -- that is the better device and it is worth doing,
// but it needs the store to be able to read platform_accounts, which today it
// cannot without a dependency this package does not want to add. Recorded here
// so the next person to touch it knows the ceiling was chosen, not missed.
//
// The check is deliberately COARSE -- same function, in any order -- because a
// precise one would be a dataflow analysis, and a guard that is hard to read is
// a guard that gets deleted the first time it is inconvenient.
func TestEveryDestinationWriteIsRepairedFirst(t *testing.T) {
	const repair = "dropUnsendableSettings"
	writers := map[string]bool{"CreateDestination": true, "UpdateDestination": true}

	root := packageRoot(t)
	fset := token.NewFileSet()

	type site struct {
		fn   string
		call string
		pos  string
	}
	var unrepaired []site
	repaired := 0

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(root, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			var calls []site
			repairsHere := false
			ast.Inspect(fn.Body, func(m ast.Node) bool {
				sel, ok := m.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch {
				case sel.Sel.Name == repair:
					repairsHere = true
				case writers[sel.Sel.Name]:
					// s.store.UpdateDestination — the receiver must be the
					// store, so db.UpdateDestination's own definition and any
					// mention in another package's name do not count.
					if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "store" {
						calls = append(calls, site{
							fn:   fn.Name.Name,
							call: sel.Sel.Name,
							pos:  fset.Position(sel.Pos()).String(),
						})
					}
				}
				return true
			})
			for _, c := range calls {
				if repairsHere {
					repaired++
				} else {
					unrepaired = append(unrepaired, c)
				}
			}
			return true
		})
	}

	// POSITIVE CONTROL, and it is the one that matters. Everything below passes
	// trivially if the walk finds no writers at all -- a renamed store method, a
	// moved file, a selector shape this Inspect does not match -- and a green
	// run over zero call sites is exactly the failure this whole audit kept
	// finding in other people's guards.
	const knownWriters = 4
	if repaired+len(unrepaired) < knownWriters {
		t.Fatalf("found only %d destination write site(s); at least %d exist "+
			"(handleCreateDestination, handleUpdateDestination, saveExpertArgs, "+
			"and the Facebook stream-key refresh). The walk is broken, so a pass "+
			"here asserts nothing. Fix the walk -- do not lower the count.",
			repaired+len(unrepaired), knownWriters)
	}

	if len(unrepaired) > 0 {
		sort.Slice(unrepaired, func(i, j int) bool { return unrepaired[i].pos < unrepaired[j].pos })
		var b strings.Builder
		for _, u := range unrepaired {
			fmt.Fprintf(&b, "\n  %s: %s calls store.%s without %s", u.pos, u.fn, u.call, repair)
		}
		t.Fatalf("a destination is written without being repaired first:%s\n\n"+
			"Every route that stores a destination has to call s.%s on the row "+
			"first. Without it a destination can keep an account belonging to "+
			"another platform, and preannounce will send that platform's token "+
			"to this one's API -- a YouTube bearer to graph.facebook.com. The "+
			"repair is idempotent and costs a store read, so there is no write "+
			"path where skipping it is the right call.", b.String(), repair)
	}
}
