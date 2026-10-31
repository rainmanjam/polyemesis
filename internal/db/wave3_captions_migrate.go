package db

// MigrateWave3CaptionSettings is the reserved schema slot for #8 live captions: caption track and burn-in settings.
//
// The slot is empty. Its lane fills in this body, and nothing else in
// db.go, so that lanes do not collide in the Open block. The body must stay
// idempotent: Open runs on every boot. Use columnExists and ALTER TABLE ADD
// COLUMN, the pattern MigrateScheduleAllDestinations follows. Its test is
// wave3_captions_migrate_test.go.
func (d *DB) MigrateWave3CaptionSettings() error { return nil }
