package db

import "testing"

// TestWave3SlotRolloverStateCreatesItsTableIdempotentlyAndLeavesTheVersionAlone pins the
// reservation for #19 planned rollover: the per-destination rollover table.
//
// The slot is the only place this table is created, so the test does what Open does on every boot:
// it runs the slot twice, checks that the table exists afterwards, and checks that the stamped schema
// version did not move. An older binary never reads this table, so nothing it could misread exists,
// and there is no reason to bump the version.
func TestWave3SlotRolloverStateCreatesItsTableIdempotentlyAndLeavesTheVersionAlone(t *testing.T) {
	d := testDB(t)
	before, err := d.UserVersion()
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	for pass := 1; pass <= 2; pass++ {
		if err := d.MigrateWave3RolloverState(); err != nil {
			t.Fatalf("MigrateWave3RolloverState pass %d: %v", pass, err)
		}
	}
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'destination_rollover'`).Scan(&n); err != nil {
		t.Fatalf("look up destination_rollover: %v", err)
	}
	if n != 1 {
		t.Fatalf("destination_rollover is missing after the slot ran")
	}
	after, err := d.UserVersion()
	if err != nil {
		t.Fatalf("read schema version after slot: %v", err)
	}
	if after != before {
		t.Fatalf("MigrateWave3RolloverState moved the schema version from %d to %d", before, after)
	}
}
