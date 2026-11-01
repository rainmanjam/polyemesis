package db

// MigrateWave3YouTubeVertical is the reserved schema slot for #7 YouTube paired vertical.
//
// It creates the pairing link between a horizontal YouTube destination and the
// vertical destination that publishes the 9:16 copy of the same broadcast.
//
// PROVISIONAL: YouTube's API cannot attach a vertical companion to a broadcast
// (see the #7 spike verdict). The link is therefore a polyemesis-side record only.
// Nothing here tells YouTube the two halves are paired.
//
// The table is a new one, not a column on destinations, so #8 and #15 do not
// have to share a scan list with this lane. No key column is allowed here:
// the vertical stream key lives in destinations.stream_key, the encrypted path.
//
// The statement is idempotent, so Open may run it on every boot. This slot does
// not move currentSchemaVersion: an older binary never reads this table.
func (d *DB) MigrateWave3YouTubeVertical() error {
	_, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS youtube_vertical_pairs (
    horizontal_id      INTEGER PRIMARY KEY REFERENCES destinations(id) ON DELETE CASCADE,
    vertical_id        INTEGER NOT NULL UNIQUE REFERENCES destinations(id) ON DELETE CASCADE,
    vertical_stream_id TEXT    NOT NULL DEFAULT '',
    pairing_confirmed_at INTEGER NOT NULL DEFAULT 0,
    created_at         INTEGER NOT NULL DEFAULT 0
)`)
	return err
}
