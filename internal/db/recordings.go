package db

import (
	"database/sql"
	"errors"
	"time"
)

// Recording is one MKV segment produced by the recorder.
type Recording struct {
	ID         int64     `json:"id"`
	Filename   string    `json:"filename"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	Bytes      int64     `json:"bytes"`
	DurationMS int64     `json:"durationMs"`
	// Tracks is the audio track count preserved in the file. The recorder
	// keeps every ingest track, so this is how the user confirms the archive
	// really is the full multitrack master.
	Tracks int `json:"tracks"`
}

// UpsertRecording indexes a segment file, keyed on filename so the filesystem
// scanner can run repeatedly without creating duplicates.
//
// Duration and track count survive an upsert that carries neither: the scanner
// measures a segment once and then keeps re-indexing it for its changing size,
// and that must not wipe the measurement.
func (d *DB) UpsertRecording(r *Recording) error {
	var started, finished int64
	if !r.StartedAt.IsZero() {
		started = r.StartedAt.Unix()
	}
	if !r.FinishedAt.IsZero() {
		finished = r.FinishedAt.Unix()
	}
	_, err := d.sql.Exec(`INSERT INTO recordings (filename, started_at, finished_at, bytes, duration_ms, tracks)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(filename) DO UPDATE SET
			finished_at=excluded.finished_at,
			bytes=excluded.bytes,
			duration_ms=CASE WHEN excluded.duration_ms > 0 THEN excluded.duration_ms ELSE recordings.duration_ms END,
			tracks=CASE WHEN excluded.tracks > 0 THEN excluded.tracks ELSE recordings.tracks END`,
		r.Filename, started, finished, r.Bytes, r.DurationMS, r.Tracks)
	return err
}

// ListRecordings returns segments newest first.
func (d *DB) ListRecordings() ([]Recording, error) {
	rows, err := d.sql.Query(`SELECT id, filename, started_at, finished_at, bytes, duration_ms, tracks
		FROM recordings ORDER BY started_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Recording{}
	for rows.Next() {
		var (
			r                 Recording
			started, finished int64
		)
		if err := rows.Scan(&r.ID, &r.Filename, &started, &finished, &r.Bytes, &r.DurationMS, &r.Tracks); err != nil {
			return nil, err
		}
		r.StartedAt = time.Unix(started, 0)
		if finished > 0 {
			r.FinishedAt = time.Unix(finished, 0)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRecording loads one segment by id.
func (d *DB) GetRecording(id int64) (*Recording, error) {
	var (
		r                 Recording
		started, finished int64
	)
	err := d.sql.QueryRow(`SELECT id, filename, started_at, finished_at, bytes, duration_ms, tracks
		FROM recordings WHERE id = ?`, id).
		Scan(&r.ID, &r.Filename, &started, &finished, &r.Bytes, &r.DurationMS, &r.Tracks)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.StartedAt = time.Unix(started, 0)
	if finished > 0 {
		r.FinishedAt = time.Unix(finished, 0)
	}
	return &r, nil
}

// DeleteRecording removes the index entry. Deleting the file is the caller's
// job — the recording package owns the filesystem.
func (d *DB) DeleteRecording(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM recordings WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteRecordingByFilename removes the index entry for a file that has gone.
func (d *DB) DeleteRecordingByFilename(name string) error {
	_, err := d.sql.Exec(`DELETE FROM recordings WHERE filename = ?`, name)
	return err
}

// TotalRecordingBytes is the disk usage figure shown on the recordings page
// and the input to the retention sweeper's size cap.
func (d *DB) TotalRecordingBytes() (int64, error) {
	var n sql.NullInt64
	if err := d.sql.QueryRow(`SELECT SUM(bytes) FROM recordings`).Scan(&n); err != nil {
		return 0, err
	}
	return n.Int64, nil
}
