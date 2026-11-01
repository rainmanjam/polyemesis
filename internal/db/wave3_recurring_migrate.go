package db

// MigrateWave3RecurringTemplates creates the #18 recurring-events tables: one broadcast template per
// start schedule, and one row per occurrence per destination that a look-ahead has created.
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
	return nil
}
