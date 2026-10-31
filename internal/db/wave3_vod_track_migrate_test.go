package db

import "testing"

// TestWave3SlotVODTrackIsAnIdempotentNoOpUntilItsLaneFillsItIn pins the reservation for
// #15 Twitch VOD audio track: the per-destination setting.
//
// A slot that changes the schema must replace this test with its own. The
// version check is what stops a placeholder from moving the stamped schema
// version, which is a decision about older binaries and not a side effect.
func TestWave3SlotVODTrackIsAnIdempotentNoOpUntilItsLaneFillsItIn(t *testing.T) {
	d := testDB(t)
	before, err := d.UserVersion()
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	for pass := 1; pass <= 2; pass++ {
		if err := d.MigrateWave3VODTrack(); err != nil {
			t.Fatalf("MigrateWave3VODTrack pass %d: %v", pass, err)
		}
	}
	after, err := d.UserVersion()
	if err != nil {
		t.Fatalf("read schema version after slot: %v", err)
	}
	if after != before {
		t.Fatalf("MigrateWave3VODTrack moved the schema version from %d to %d", before, after)
	}
}
