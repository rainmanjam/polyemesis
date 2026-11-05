package db

// MigrateWave3RecurringTemplates creates the #18 recurring-events tables: one broadcast template per
// start schedule, one row per occurrence per destination that a look-ahead has created, and one row per
// Twitch or Kick show whose go-live title was attempted.
//
// Both tables are CREATE TABLE IF NOT EXISTS, run on every boot, so the body is idempotent and the
// schema version does not move: an older binary never reads these tables, so there is nothing it
// could misread. Neither table declares a foreign key. A schedule or destination can be deleted while
// a platform event for one of its occurrences still exists, and a cascade would drop the only record
// of that event. The sweep reconciles orphans instead, deleting the event first and then the row.
//
// The reads and writes are in recurring.go. Its test is wave3_recurring_migrate_test.go.
func (d *DB) MigrateWave3RecurringTemplates() error {
	_, err := d.sql.Exec(`CREATE TABLE IF NOT EXISTS broadcast_templates (
		schedule_id    INTEGER PRIMARY KEY,
		title_pattern  TEXT    NOT NULL,
		description    TEXT    NOT NULL DEFAULT '',
		privacy        TEXT    NOT NULL DEFAULT 'public',
		category       TEXT    NOT NULL DEFAULT '',
		lookahead      INTEGER NOT NULL DEFAULT 2,
		thumbnail      BLOB,
		thumbnail_mime TEXT    NOT NULL DEFAULT '',
		created_at     INTEGER NOT NULL,
		updated_at     INTEGER NOT NULL
	)`)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`CREATE TABLE IF NOT EXISTS recurring_occurrences (
		id                INTEGER PRIMARY KEY AUTOINCREMENT,
		schedule_id       INTEGER NOT NULL,
		destination_id    INTEGER NOT NULL,
		occurrence_at     INTEGER NOT NULL,
		state             TEXT    NOT NULL,
		platform_event_id TEXT    NOT NULL DEFAULT '',
		title             TEXT    NOT NULL DEFAULT '',
		created_at        INTEGER NOT NULL,
		updated_at        INTEGER NOT NULL,
		UNIQUE (schedule_id, destination_id, occurrence_at)
	)`)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`CREATE INDEX IF NOT EXISTS recurring_occurrences_at ON recurring_occurrences (occurrence_at)`)
	if err != nil {
		return err
	}
	// One row per (destination, schedule, show) whose go-live title was attempted. Twitch and Kick
	// have no event to create, so this row is the only record that the title was written for a show.
	// It is the claim: the write happens once, and a restart does not write it again over an
	// operator's own edit.
	_, err = d.sql.Exec(`CREATE TABLE IF NOT EXISTS recurring_go_live (
		destination_id INTEGER NOT NULL,
		schedule_id    INTEGER NOT NULL,
		occurrence_at  INTEGER NOT NULL,
		created_at     INTEGER NOT NULL,
		PRIMARY KEY (destination_id, schedule_id, occurrence_at)
	)`)
	if err != nil {
		return err
	}
	// The last look-ahead refusal, so the template screen can show a platform limit. Added to a table
	// that may already exist from an earlier boot, so the column is checked first, as the schedule
	// migration does.
	for _, col := range []struct{ name, decl string }{
		{"last_error", "TEXT NOT NULL DEFAULT ''"},
		{"last_error_at", "INTEGER NOT NULL DEFAULT 0"},
	} {
		has, err := columnExists(d.sql, "broadcast_templates", col.name)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := d.sql.Exec("ALTER TABLE broadcast_templates ADD COLUMN " + col.name + " " + col.decl); err != nil {
			return err
		}
	}
	// Templates whose schedule was deleted before DeleteSchedule took the
	// template with it. Safe on every boot: schedule ids are never reused, and
	// the sweep needs only the occurrence rows, never the template, to cancel
	// an orphan's events. Touches nothing on an ordinary boot.
	_, err = d.sql.Exec(`DELETE FROM broadcast_templates WHERE schedule_id NOT IN (SELECT id FROM schedules)`)
	return err
}
