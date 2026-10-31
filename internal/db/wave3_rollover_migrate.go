package db

// MigrateWave3RolloverState creates the #19 planned-rollover table: one row per destination that has
// rollover configured, holding the operator's settings and the session the rollover is counting.
//
// It is a separate table rather than new columns on destinations, so the destination shape the UI
// mirrors and the drift guards that check it stay untouched. The row is keyed by destination id with
// no foreign key, for the same reason recurring_occurrences has none: a destination can be deleted and
// the row is then simply ignored by the planner, which asks the destinations table who exists.
//
// CREATE TABLE IF NOT EXISTS, run on every boot, so the body is idempotent and the schema version does
// not move: an older binary never reads this table. The reads and writes are in rollover.go, and the
// test is wave3_rollover_migrate_test.go.
func (d *DB) MigrateWave3RolloverState() error {
	_, err := d.sql.Exec(`CREATE TABLE IF NOT EXISTS destination_rollover (
		destination_id       INTEGER PRIMARY KEY,
		enabled              INTEGER NOT NULL DEFAULT 0,
		margin_minutes       INTEGER NOT NULL DEFAULT 30,
		quiet_start          TEXT    NOT NULL DEFAULT '',
		quiet_end            TEXT    NOT NULL DEFAULT '',
		quiet_zone           TEXT    NOT NULL DEFAULT '',
		obs_scene            TEXT    NOT NULL DEFAULT '',
		session_broadcast_id TEXT    NOT NULL DEFAULT '',
		session_started_at   INTEGER NOT NULL DEFAULT 0,
		last_rollover_at     INTEGER NOT NULL DEFAULT 0,
		last_trigger         TEXT    NOT NULL DEFAULT '',
		last_result          TEXT    NOT NULL DEFAULT '',
		updated_at           INTEGER NOT NULL DEFAULT 0
	)`)
	return err
}
