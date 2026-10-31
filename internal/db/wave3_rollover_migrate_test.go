package db

import "testing"

// TestWave3SlotRolloverStateIsAnIdempotentNoOpUntilItsLaneFillsItIn pins the reservation for
// #19 planned rollover: per-destination rollover state.
//
// A slot that changes the schema must replace this test with its own. The
// version check is what stops a placeholder from moving the stamped schema
// version, which is a decision about older binaries and not a side effect.
func TestWave3SlotRolloverStateIsAnIdempotentNoOpUntilItsLaneFillsItIn(t *testing.T) {
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
	after, err := d.UserVersion()
	if err != nil {
		t.Fatalf("read schema version after slot: %v", err)
	}
	if after != before {
		t.Fatalf("MigrateWave3RolloverState moved the schema version from %d to %d", before, after)
	}
}
