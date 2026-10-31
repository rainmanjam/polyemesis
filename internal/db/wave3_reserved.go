package db

// Reserved schema slots for the Wave 3 lanes. See wave3_reserved_test.go for
// why these exist and what they promise. Each lane fills in ONLY its own
// function body, in this file, and keeps the body idempotent: ALTER TABLE
// behind columnExists, the way every other Migrate* in this package does it.
//
//	MigrateWave3RolloverState      #19 planned rollover: per-destination rollover state
//	MigrateWave3RecurringTemplates #18 recurring events: templates and occurrences
//	MigrateWave3CaptionSettings    #8  live captions: caption track and burn-in settings
//	MigrateWave3VODTrack           #15 Twitch VOD audio track: the per-destination setting
//	MigrateWave3YouTubeVertical    #7  YouTube paired vertical: the pairing link
//
// Lane #3 (X) and the bug-fix lane need no schema slot; see the wave3 kit doc.

// MigrateWave3RolloverState is the reserved slot for #19. It does nothing yet.
func (d *DB) MigrateWave3RolloverState() error { return nil }

// MigrateWave3RecurringTemplates is the reserved slot for #18. It does nothing yet.
func (d *DB) MigrateWave3RecurringTemplates() error { return nil }

// MigrateWave3CaptionSettings is the reserved slot for #8. It does nothing yet.
func (d *DB) MigrateWave3CaptionSettings() error { return nil }

// MigrateWave3VODTrack is the reserved slot for #15. It does nothing yet.
func (d *DB) MigrateWave3VODTrack() error { return nil }

// MigrateWave3YouTubeVertical is the reserved slot for #7. It does nothing yet.
func (d *DB) MigrateWave3YouTubeVertical() error { return nil }
