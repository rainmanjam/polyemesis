package db

import "fmt"

// MigrateWave3CaptionSettings is the reserved schema slot for #8 live captions.
// It adds destinations.caption_mode, the per-output choice of how captions reach
// that destination: 'off' (the default), 'sei' (CEA-608 in the video stream, for
// Twitch and any destination that reads it), 'youtube_post' (the YouTube
// caption ingestion URL) or 'facebook_sei' (behind a provisional flag).
//
// The default is 'off', because captions are an output-by-output choice and an
// upgrade must never start sending text to a platform. The body is idempotent:
// Open runs on every boot, and the column is added only when it is missing. It
// is the same pattern MigrateScheduleAllDestinations follows.
func (d *DB) MigrateWave3CaptionSettings() error {
	has, err := columnExists(d.sql, "destinations", "caption_mode")
	if err != nil {
		return fmt.Errorf("inspect destinations columns: %w", err)
	}
	if has {
		return nil
	}
	if _, err := d.sql.Exec(`ALTER TABLE destinations ADD COLUMN caption_mode TEXT NOT NULL DEFAULT 'off'`); err != nil {
		return fmt.Errorf("add destinations.caption_mode: %w", err)
	}
	return nil
}
