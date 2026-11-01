package db

import "testing"

// TestWave3SlotCaptionSettingsAddsTheCaptionModeWithOffAsTheDefault pins the
// #8 reservation. A destination that predates captions must come back with
// captions off, because a caption is a choice the operator makes per output and
// never one an upgrade turns on. The migration also runs on every boot, so it
// must leave an existing value alone and add the column only once.
//
// This replaces the placeholder that asserted the slot was a no-op. The schema
// version does not move: an older binary can read and write a destination with
// an extra defaulted column, so nothing about it is dangerous to run.
func TestWave3SlotCaptionSettingsAddsTheCaptionModeWithOffAsTheDefault(t *testing.T) {
	d := testDB(t)
	before, err := d.UserVersion()
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	for pass := 1; pass <= 2; pass++ {
		if err := d.MigrateWave3CaptionSettings(); err != nil {
			t.Fatalf("MigrateWave3CaptionSettings pass %d: %v", pass, err)
		}
	}
	after, err := d.UserVersion()
	if err != nil {
		t.Fatalf("read schema version after slot: %v", err)
	}
	if after != before {
		t.Fatalf("MigrateWave3CaptionSettings moved the schema version from %d to %d", before, after)
	}
	has, err := columnExists(d.sql, "destinations", "caption_mode")
	if err != nil {
		t.Fatalf("inspect destinations: %v", err)
	}
	if !has {
		t.Fatalf("destinations.caption_mode was not added")
	}
	if _, err := d.sql.Exec(`INSERT INTO destinations (name, kind, profile, created_at, updated_at) VALUES ('caption-default', 'rtmp', '{}', 0, 0)`); err != nil {
		t.Fatalf("insert destination: %v", err)
	}
	var mode string
	if err := d.sql.QueryRow(`SELECT caption_mode FROM destinations WHERE name = 'caption-default'`).Scan(&mode); err != nil {
		t.Fatalf("read caption_mode: %v", err)
	}
	if mode != "off" {
		t.Fatalf("caption_mode default = %q, want \"off\"", mode)
	}
	if _, err := d.sql.Exec(`UPDATE destinations SET caption_mode = 'sei' WHERE name = 'caption-default'`); err != nil {
		t.Fatalf("set caption_mode: %v", err)
	}
	if err := d.MigrateWave3CaptionSettings(); err != nil {
		t.Fatalf("MigrateWave3CaptionSettings after a value was set: %v", err)
	}
	if err := d.sql.QueryRow(`SELECT caption_mode FROM destinations WHERE name = 'caption-default'`).Scan(&mode); err != nil {
		t.Fatalf("read caption_mode after re-run: %v", err)
	}
	if mode != "sei" {
		t.Fatalf("re-running the migration reset caption_mode to %q, want \"sei\"", mode)
	}
}
