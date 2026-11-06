package db

import (
	"strings"
	"testing"
)

// A table the migration hands to backfillSourceIDs without a statement must
// fail loudly. An empty statement would execute as a no-op and leave that
// table's rows sourceless with nothing to say so.
func TestBackfillSourceIDsRefusesATableWithNoStatement(t *testing.T) {
	d := testDB(t)
	tx, err := d.sql.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	err = backfillSourceIDs(tx, []string{"destinations", "no_such_table"}, 1)
	if err == nil || !strings.Contains(err.Error(), "backfill no_such_table.source_id: no statement for table") {
		t.Fatalf("got %v, want the no-statement error for no_such_table", err)
	}
}

// Every table the upgrade can backfill has its own statement, and a zero id
// does nothing at all, even for an unknown table.
func TestBackfillSourceIDsCoversTheMigrationTables(t *testing.T) {
	for _, table := range []string{"destinations", "renditions", "recordings"} {
		if stmt := backfillSourceIDSQL[table]; !strings.Contains(stmt, "UPDATE "+table+" SET source_id") {
			t.Errorf("no backfill statement for %s: %q", table, stmt)
		}
	}
	if err := backfillSourceIDs(nil, []string{"no_such_table"}, 0); err != nil {
		t.Fatalf("id 0 must be a no-op, got %v", err)
	}
}
