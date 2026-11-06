package db

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// The migration list's order is a contract; the comments on each entry say why.
// This is the part of that contract that can be checked: each rule names a step
// and the steps that must come before it.
func TestMigrationStepsRunInDependencyOrder(t *testing.T) {
	// Named from the function each entry runs, so the order checked is the
	// order executed.
	names := make([]string, len(migrationSteps))
	pos := map[string]int{}
	for i, s := range migrationSteps {
		full := runtime.FuncForPC(reflect.ValueOf(s.fn).Pointer()).Name()
		names[i] = full[strings.LastIndex(full, ".")+1:]
		if _, dup := pos[names[i]]; dup {
			t.Fatalf("step %s is listed twice", names[i])
		}
		pos[names[i]] = i
	}
	before := func(first, then string) {
		t.Helper()
		a, ok1 := pos[first]
		b, ok2 := pos[then]
		if !ok1 || !ok2 {
			t.Fatalf("rule names a step that is not listed: %s, %s", first, then)
		}
		if a >= b {
			t.Errorf("%s (#%d) must run before %s (#%d)", first, a, then, b)
		}
	}

	// Column-adding steps land before the steps written against their final
	// shape.
	for _, n := range []string{
		"MigrateWave3RolloverState", "MigrateWave3RecurringTemplates", "MigrateWave3CaptionSettings",
		"MigrateWave3VODTrack", "MigrateWave3YouTubeVertical", "MigrateWave3XTitle", "MigrateWave3XTweet",
	} {
		before(n, "MigrateSources")
	}
	before("MigrateSources", "MigrateUnsetSourceIngestMode")
	before("MigrateStrandedStreamKeys", "backfillDestinationStreamKeys")
	// The backfill needs every column the other steps create.
	for _, n := range names {
		switch n {
		case "backfillDestinationStreamKeys", "MigrateAutomodKeyBinding", "MigrateSchemaVersion":
		default:
			before(n, "backfillDestinationStreamKeys")
		}
	}
	// The version stamp is last of all and is the one step whose error is
	// returned as it is.
	last := migrationSteps[len(migrationSteps)-1]
	if lastName := names[len(names)-1]; lastName != "MigrateSchemaVersion" || !last.bare {
		t.Errorf("the last step is %s; MigrateSchemaVersion must be last of all", lastName)
	}
	for i, s := range migrationSteps[:len(migrationSteps)-1] {
		if s.bare {
			t.Errorf("%s returns its error unwrapped; only the version stamp does", names[i])
		}
	}
}

// Open runs migrations only through migrationSteps. A step called directly
// from Open would run outside the order the test above checks and outside the
// runner that closes the handle and wraps the error.
func TestOpenCallsNoMigrationDirectly(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "db.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != "Open" {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok &&
				(strings.HasPrefix(sel.Sel.Name, "Migrate") || strings.HasPrefix(sel.Sel.Name, "backfill")) {
				t.Errorf("Open calls %s directly; add it to migrationSteps", sel.Sel.Name)
			}
			return true
		})
	}
	if !found {
		t.Fatal("no top-level Open in db.go; this test has lost the function it guards")
	}
}

// The runner owns the handle: a failing step closes it, and wraps the error
// unless the step is the version stamp, whose own message already says what
// failed.
func TestAFailingMigrationStepClosesTheHandleAndWraps(t *testing.T) {
	saved := migrationSteps
	t.Cleanup(func() { migrationSteps = saved })
	boom := errors.New("boom")
	var seen *DB
	for _, bare := range []bool{false, true} {
		migrationSteps = []migrationStep{{bare: bare, fn: func(d *DB) error {
			seen = d
			return boom
		}}}
		_, err := Open(filepath.Join(t.TempDir(), "x.db"))
		if !errors.Is(err, boom) {
			t.Fatalf("bare=%v: err = %v, want boom", bare, err)
		}
		if wrapped := strings.Contains(err.Error(), "migrate: "); wrapped == bare {
			t.Errorf("bare=%v: err = %q", bare, err)
		}
		if seen.sql.Ping() == nil {
			t.Errorf("bare=%v: the handle was left open", bare)
		}
	}
}
