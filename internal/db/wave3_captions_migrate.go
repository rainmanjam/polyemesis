package db

import "fmt"

// MigrateWave3CaptionSettings is the reserved schema slot for #8 live captions.
// It adds four columns, each the stored half of one operator choice:
//
//   - destinations.caption_mode, how captions reach that destination: 'off' (the
//     default), 'sei' (CEA-608 in the video stream, for Twitch and any destination
//     that reads it), 'youtube_post' (the YouTube caption ingestion URL) or
//     'facebook_sei' (behind a provisional flag).
//   - sources.caption_track, which audio track of the programme the captioner
//     transcribes. -1 is the automatic choice, the one the captioner makes today,
//     and it is the default so an upgrade changes nothing.
//   - sources.caption_policy, the programme's custom vocabulary and masked
//     words (CaptionPolicy) as JSON. The empty string is no policy, the
//     default, so captions read exactly as recognised until one is set.
//   - renditions.caption_burn, where the live captions are drawn into that
//     tier's picture ("bottom" or "top"). Empty, the default, draws nothing,
//     so no frame changes.
//
// Every default are the safe state. Captions are an output-by-output choice and
// an upgrade must never start sending text to a platform. The body is idempotent:
// Open runs on every boot, and each column is added only when it is missing. It
// is the same pattern MigrateScheduleAllDestinations follows.
func (d *DB) MigrateWave3CaptionSettings() error {
	for _, add := range []struct {
		table, column, ddl string
	}{
		{"destinations", "caption_mode", `ALTER TABLE destinations ADD COLUMN caption_mode TEXT NOT NULL DEFAULT 'off'`},
		{"sources", "caption_track", `ALTER TABLE sources ADD COLUMN caption_track INTEGER NOT NULL DEFAULT -1`},
		{"sources", "caption_policy", `ALTER TABLE sources ADD COLUMN caption_policy TEXT NOT NULL DEFAULT ''`},
		{"renditions", "caption_burn", `ALTER TABLE renditions ADD COLUMN caption_burn TEXT NOT NULL DEFAULT ''`},
	} {
		has, err := columnExists(d.sql, add.table, add.column)
		if err != nil {
			return fmt.Errorf("inspect %s columns: %w", add.table, err)
		}
		if has {
			continue
		}
		if _, err := d.sql.Exec(add.ddl); err != nil {
			return fmt.Errorf("add %s.%s: %w", add.table, add.column, err)
		}
	}
	return nil
}
