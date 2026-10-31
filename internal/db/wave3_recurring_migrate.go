package db

// MigrateWave3RecurringTemplates is the reserved schema slot for #18 recurring events: templates and occurrences.
//
// The slot is empty. Its lane fills in this body, and nothing else in
// db.go, so that lanes do not collide in the Open block. The body must stay
// idempotent: Open runs on every boot. Use columnExists and ALTER TABLE ADD
// COLUMN, the pattern MigrateScheduleAllDestinations follows. Its test is
// wave3_recurring_migrate_test.go.
func (d *DB) MigrateWave3RecurringTemplates() error { return nil }
