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

// OffloadFetchPin keeps a fetched-back recording local this long: neither move
// mode nor retention removes it from disk inside the window, so a clip being
// cut from it is never pulled out from under the editor.
const OffloadFetchPin = 24 * time.Hour

// OffloadPinnedRecordings names the recordings fetched back since cutoff.
func (d *DB) OffloadPinnedRecordings(cutoff time.Time) (map[string]bool, error) {
	rows, err := d.sql.Query(`SELECT DISTINCT rel_path FROM offload_items WHERE kind = ? AND fetched_at > ?`,
		OffloadKindRecording, cutoff.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, rows.Err()
}

// ErrNotCovered refuses an eviction the stored state does not justify.
var ErrNotCovered = errors.New("not every enabled storage target holds a verified copy")

// BeginEvict is the one gate a local recording passes through before its file
// is unlinked: in a single transaction it re-reads the ENABLED targets, needs
// at least one in move mode, needs a verified row with a digest on every one
// of them, and -- when checked is non-nil -- needs that set to be exactly the
// targets the caller just re-checked on the service. Only then are the rows
// marked evicted, and only then may the caller unlink, undoing the mark with
// UndoEvict if the unlink fails.
//
// Being one transaction is what serialises it against a target being deleted
// or rebound: SQLite runs write transactions one at a time, so either the
// delete commits first and this sees the target gone, or this commits first
// and the delete sees the evicted row and refuses.
func (d *DB) BeginEvict(rel string, checked []int64) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query(`SELECT id, mode FROM storage_targets WHERE enabled = 1`)
	if err != nil {
		return err
	}
	enabled := map[int64]bool{}
	move := false
	for rows.Next() {
		var id int64
		var mode string
		if err := rows.Scan(&id, &mode); err != nil {
			rows.Close()
			return err
		}
		enabled[id] = true
		move = move || mode == OffloadMove
	}
	rows.Close()
	if !move || len(enabled) == 0 {
		return ErrNotCovered
	}
	if checked != nil {
		if len(checked) != len(enabled) {
			return ErrNotCovered
		}
		for _, id := range checked {
			if !enabled[id] {
				return ErrNotCovered
			}
		}
	}
	for id := range enabled {
		var state, sha string
		err := tx.QueryRow(`SELECT state, sha256 FROM offload_items WHERE target_id = ? AND rel_path = ?`, id, rel).Scan(&state, &sha)
		if err != nil || state != OffloadVerified || sha == "" {
			return ErrNotCovered
		}
	}
	if _, err := tx.Exec(`UPDATE offload_items SET evicted = 1, updated_at = ? WHERE rel_path = ?`, time.Now().Unix(), rel); err != nil {
		return err
	}
	return tx.Commit()
}

// UndoEvict clears the mark when the unlink BeginEvict allowed did not happen.
func (d *DB) UndoEvict(rel string) error {
	_, err := d.sql.Exec(`UPDATE offload_items SET evicted = 0, updated_at = ? WHERE rel_path = ?`, time.Now().Unix(), rel)
	return err
}

// LockEviction holds the eviction lock until the returned func is called. The
// mark and the file it describes change together under it: nothing can
// remove a file between another step's check that it exists and its clearing
// of the mark.
func (d *DB) LockEviction() func() {
	d.evictMu.Lock()
	return d.evictMu.Unlock
}
