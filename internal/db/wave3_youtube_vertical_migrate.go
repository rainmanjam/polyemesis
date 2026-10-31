package db

// MigrateWave3YouTubeVertical is the reserved schema slot for #7 YouTube paired vertical: the pairing link.
//
// The slot is empty. Its lane fills in this body, and nothing else in
// db.go, so that lanes do not collide in the Open block. The body must stay
// idempotent: Open runs on every boot. Use columnExists and ALTER TABLE ADD
// COLUMN, the pattern MigrateScheduleAllDestinations follows. Its test is
// wave3_youtube_vertical_migrate_test.go.
func (d *DB) MigrateWave3YouTubeVertical() error { return nil }
