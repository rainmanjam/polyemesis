package db

import "testing"

// The Wave 3 lanes each need one schema step, and they all touch Open at the
// same time. Each lane therefore gets a reserved, empty migration here, wired
// into Open now, so that filling one in edits only its own file and never the
// migration block in db.go that every other lane is also editing.
//
// This test pins the reservation: every slot must run on a fresh database
// without error, must be safe to run twice (Open runs on every boot), and must
// leave the stamped schema version alone. A slot that starts changing the
// schema has to bring its own test, and bumping currentSchemaVersion stays a
// decision about old binaries, not a side effect of filling a slot in.
func TestWave3ReservedMigrationSlotsAreIdempotentNoOpsOnAFreshDatabase(t *testing.T) {
	d := testDB(t)
	before, err := d.UserVersion()
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	slots := map[string]func() error{
		"MigrateWave3RolloverState":      d.MigrateWave3RolloverState,
		"MigrateWave3RecurringTemplates": d.MigrateWave3RecurringTemplates,
		"MigrateWave3CaptionSettings":    d.MigrateWave3CaptionSettings,
		"MigrateWave3VODTrack":           d.MigrateWave3VODTrack,
		"MigrateWave3YouTubeVertical":    d.MigrateWave3YouTubeVertical,
	}
	for name, run := range slots {
		for pass := 1; pass <= 2; pass++ {
			if err := run(); err != nil {
				t.Fatalf("%s pass %d: %v", name, pass, err)
			}
		}
	}
	after, err := d.UserVersion()
	if err != nil {
		t.Fatalf("read schema version after slots: %v", err)
	}
	if after != before {
		t.Fatalf("reserved slots moved the schema version from %d to %d; a slot that changes the schema must bring its own test and its own bump decision", before, after)
	}
}
