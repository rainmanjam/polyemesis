package db

// MigrateWave3RolloverState is the reserved schema slot for #19 planned rollover: per-destination rollover state.
//
// The slot is empty. Its lane fills in this body, and nothing else in
// db.go, so that lanes do not collide in the Open block. The body must stay
// idempotent: Open runs on every boot. Use columnExists and ALTER TABLE ADD
// COLUMN, the pattern MigrateScheduleAllDestinations follows. Its test is
// wave3_rollover_migrate_test.go.
func (d *DB) MigrateWave3RolloverState() error { return nil }
