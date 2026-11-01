package db

import "fmt"

// MigrateWave3XTitle adds destinations.go_live_title, the title the lifecycle
// coordinator sends when it publishes an X broadcast (#3). X takes a title only
// on the PUBLISH transition, so the title is stored before the show rather than
// pushed during it. The default is the empty string, which sends no title and
// leaves X's own, so an upgrade changes nothing. Idempotent, like every slot.
func (d *DB) MigrateWave3XTitle() error {
	has, err := columnExists(d.sql, "destinations", "go_live_title")
	if err != nil {
		return fmt.Errorf("inspect destinations columns: %w", err)
	}
	if has {
		return nil
	}
	if _, err := d.sql.Exec(`ALTER TABLE destinations ADD COLUMN go_live_title TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add destinations.go_live_title: %w", err)
	}
	return nil
}

// XGoLiveTitleMax bounds the X go-live title. X documents the PUBLISH title as
// "subject to X post length limits" without a number, so the 280-character
// post limit is used. PROVISIONAL until a live PUBLISH on a whitelisted app
// confirms or corrects it.
const XGoLiveTitleMax = 280
