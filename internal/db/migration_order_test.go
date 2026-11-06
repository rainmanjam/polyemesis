package db

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// The migration list's order is a contract; the comments on each entry say why.
// This is the part of that contract that can be checked: each rule names a step
// and the steps that must come before it.
func TestMigrationStepsRunInDependencyOrder(t *testing.T) {
	pos := map[string]int{}
	for i, s := range migrationSteps {
		if _, dup := pos[s.name]; dup {
			t.Fatalf("step %s is listed twice", s.name)
		}
		pos[s.name] = i
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
	for _, s := range migrationSteps {
		switch s.name {
		case "backfillDestinationStreamKeys", "MigrateAutomodKeyBinding", "MigrateSchemaVersion":
		default:
			before(s.name, "backfillDestinationStreamKeys")
		}
	}
	// The version stamp is last of all and is the one step whose error is
	// returned as it is.
	last := migrationSteps[len(migrationSteps)-1]
	if last.name != "MigrateSchemaVersion" || !last.bare {
		t.Errorf("the last step is %s; MigrateSchemaVersion must be last of all", last.name)
	}
	for _, s := range migrationSteps[:len(migrationSteps)-1] {
		if s.bare {
			t.Errorf("%s returns its error unwrapped; only the version stamp does", s.name)
		}
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
		migrationSteps = []migrationStep{{name: "Boom", bare: bare, fn: func(d *DB) error {
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
