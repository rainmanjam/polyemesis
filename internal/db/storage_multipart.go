package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// OffloadPart is one completed part of a multipart upload.
type OffloadPart struct {
	Number int    `json:"n"`
	ETag   string `json:"etag"`
	MD5    string `json:"md5"`
	Size   int64  `json:"size"`
}

// OffloadMultipart is a multipart upload in progress.
type OffloadMultipart struct {
	TargetID  int64
	RelPath   string
	Key       string
	UploadID  string
	PartSize  int64
	Bytes     int64
	SHA256    string
	Parts     []OffloadPart
	CreatedAt time.Time
}

const multipartCols = `target_id, rel_path, object_key, upload_id, part_size, bytes, sha256, parts, created_at`

func scanMultipart(row interface{ Scan(...any) error }) (OffloadMultipart, error) {
	var m OffloadMultipart
	var parts string
	var created int64
	if err := row.Scan(&m.TargetID, &m.RelPath, &m.Key, &m.UploadID, &m.PartSize, &m.Bytes, &m.SHA256, &parts, &created); err != nil {
		return m, err
	}
	m.CreatedAt = time.Unix(created, 0).UTC()
	if err := json.Unmarshal([]byte(parts), &m.Parts); err != nil {
		return m, err
	}
	return m, nil
}

// GetOffloadMultipart returns the upload in progress for a file, or ErrNotFound.
func (d *DB) GetOffloadMultipart(target int64, rel string) (OffloadMultipart, error) {
	m, err := scanMultipart(d.sql.QueryRow(`SELECT `+multipartCols+` FROM offload_multipart WHERE target_id = ? AND rel_path = ?`, target, rel))
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// ListOffloadMultipart returns every upload in progress; target 0 means all.
func (d *DB) ListOffloadMultipart(target int64) ([]OffloadMultipart, error) {
	q := `SELECT ` + multipartCols + ` FROM offload_multipart`
	var args []any
	if target != 0 {
		q += ` WHERE target_id = ?`
		args = append(args, target)
	}
	rows, err := d.sql.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OffloadMultipart
	for rows.Next() {
		m, err := scanMultipart(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PutOffloadMultipart inserts or replaces the upload's row.
func (d *DB) PutOffloadMultipart(m OffloadMultipart) error {
	parts, err := json.Marshal(m.Parts)
	if err != nil {
		return err
	}
	if m.Parts == nil {
		parts = []byte("[]")
	}
	created := m.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	_, err = d.sql.Exec(`INSERT INTO offload_multipart (`+multipartCols+`) VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(target_id, rel_path) DO UPDATE SET object_key=excluded.object_key, upload_id=excluded.upload_id,
			part_size=excluded.part_size, bytes=excluded.bytes, sha256=excluded.sha256, parts=excluded.parts`,
		m.TargetID, m.RelPath, m.Key, m.UploadID, m.PartSize, m.Bytes, m.SHA256, string(parts), created.Unix())
	return err
}

// DeleteOffloadMultipart drops the row once the upload completed or was aborted.
func (d *DB) DeleteOffloadMultipart(target int64, rel string) error {
	_, err := d.sql.Exec(`DELETE FROM offload_multipart WHERE target_id = ? AND rel_path = ?`, target, rel)
	return err
}

// RecoverInterruptedOffload is the startup half of restart safety: an item a
// previous process left mid-upload is failed and due at once -- never left
// "uploading" for ever. It spends no attempt: a restart is not the file's fault.
func (d *DB) RecoverInterruptedOffload() (int64, error) {
	res, err := d.sql.Exec(`UPDATE offload_items SET state = ?, next_attempt_at = 0,
		last_error = 'interrupted by a restart; it resumes on the next pass', updated_at = ?
		WHERE state = ?`, OffloadFailed, time.Now().Unix(), OffloadUploading)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
