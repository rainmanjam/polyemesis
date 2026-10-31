package db

import "testing"

// TestWave3SlotRecurringTemplatesCreatesItsTablesIdempotentlyAndLeavesTheVersionAlone pins the
// reservation for #18 recurring events: the broadcast template and occurrence tables.
//
// The slot is the only place these tables are created, so the test does what Open does on every
// boot: it runs the slot twice and checks that both tables exist afterwards and that the stamped
// schema version did not move. The version check is what stops a table-creating slot from quietly
// asking older binaries to read a shape they were never built for; these are additive tables that
// an older binary never touches, so there is no reason to bump the version.
func TestWave3SlotRecurringTemplatesCreatesItsTablesIdempotentlyAndLeavesTheVersionAlone(t *testing.T) {
	d := testDB(t)
	before, err := d.UserVersion()
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	for pass := 1; pass <= 2; pass++ {
		if err := d.MigrateWave3RecurringTemplates(); err != nil {
			t.Fatalf("MigrateWave3RecurringTemplates pass %d: %v", pass, err)
		}
	}
	for _, table := range []string{"broadcast_templates", "recurring_occurrences"} {
		var n int
		if err := d.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil {
			t.Fatalf("look up table %s: %v", table, err)
		}
		if n != 1 {
			t.Fatalf("table %s is missing after the slot ran", table)
		}
	}
	after, err := d.UserVersion()
	if err != nil {
		t.Fatalf("read schema version after slot: %v", err)
	}
	if after != before {
		t.Fatalf("MigrateWave3RecurringTemplates moved the schema version from %d to %d", before, after)
	}
}
