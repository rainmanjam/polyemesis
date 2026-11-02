package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// Storage target providers. They differ only in defaults the UI pre-fills
// (endpoint shape, region, addressing style); on the wire every one of them is
// the same S3 API.
const (
	StorageProviderS3     = "s3"
	StorageProviderR2     = "r2"
	StorageProviderB2     = "b2"
	StorageProviderMinIO  = "minio"
	StorageProviderWasabi = "wasabi"
)

// Offload item states.
const (
	OffloadPending   = "pending"
	OffloadUploading = "uploading"
	OffloadVerified  = "verified"
	OffloadFailed    = "failed"
	// OffloadStuck is a failure offload stopped retrying on its own: too many
	// consecutive attempts, or an error waiting cannot cure (a refused
	// credential, a missing bucket, a key the file cannot have). It moves only
	// when an operator presses Retry now.
	OffloadStuck = "stuck"
)

// DefaultOffloadMaxAttempts is how many consecutive failures make an item stuck.
const DefaultOffloadMaxAttempts = 8

// Offload item kinds.
const (
	OffloadKindRecording  = "recording"
	OffloadKindStem       = "stem"
	OffloadKindTranscript = "transcript"
	OffloadKindClip       = "clip"
)

// StorageTarget is one S3-compatible bucket finished media is copied to.
// Offload is MIRROR ONLY: it copies, and never deletes or moves a local file.
//
// SecretAccessKey is json:"-": it is written once on create or update, sealed
// with the install's secret box (passed explicitly, like the hook and platform
// credentials -- a handful of methods with a handful of callers), and never
// marshalled in any response. There is no plaintext fallback: without a box
// the secret is refused, not stored. The page is told HasSecret instead.
// AccessKeyID is not a secret on its own but names the account, so the
// read-safe view blanks it.
type StorageTarget struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Endpoint string `json:"endpoint"`
	Bucket   string `json:"bucket"`
	Region   string `json:"region"`
	// PrefixTemplate is rendered into each object key's prefix. Tokens:
	// {kind} {source} {yyyy} {mm} {dd}. Empty means DefaultOffloadPrefix.
	PrefixTemplate string `json:"prefixTemplate"`
	PathStyle      bool   `json:"pathStyle"`
	AccessKeyID    string `json:"accessKeyId"`
	// SecretAccessKey is plaintext in memory only, filled on read when the box
	// opens it.
	SecretAccessKey string `json:"-"`
	HasSecret       bool   `json:"hasSecret"`
	// SecretUnreadable is set when a sealed secret exists but this machine's
	// key cannot open it (a restored database without its key file).
	SecretUnreadable   bool `json:"secretUnreadable,omitempty"`
	OffloadStems       bool `json:"offloadStems"`
	OffloadTranscripts bool `json:"offloadTranscripts"`
	OffloadClips       bool `json:"offloadClips"`
	VerifyDownload     bool `json:"verifyDownload"`
	AllowPrivateTarget bool `json:"allowPrivateTarget"`
	Enabled            bool `json:"enabled"`
	// MaxAttempts is how many consecutive failures make an item stuck.
	MaxAttempts int `json:"maxAttempts"`
	// BindingGen counts the times the target was moved to another address
	// (RebindStorageTarget). An upload captures it when its pass starts and
	// may write its result only while it is unchanged, so an upload to the
	// OLD address can never mark a row verified for the new one.
	BindingGen int64     `json:"-"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// DefaultOffloadPrefix is the key prefix a target with no template uses.
const DefaultOffloadPrefix = "polyemesis/{kind}/{yyyy}/{mm}/{dd}"

// OffloadItem is the offload state of one file on one target.
type OffloadItem struct {
	TargetID      int64     `json:"targetId"`
	RelPath       string    `json:"relPath"`
	Kind          string    `json:"kind"`
	Key           string    `json:"key"`
	Bytes         int64     `json:"bytes"`
	SHA256        string    `json:"sha256"`
	State         string    `json:"state"`
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"nextAttemptAt,omitzero"`
	LastError     string    `json:"lastError,omitempty"`
	VerifiedAt    time.Time `json:"verifiedAt,omitzero"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// ErrBindingChanged refuses an item write from a pass that started before the
// target was moved to another address.
var ErrBindingChanged = errors.New("the storage target moved while this upload ran; its result belongs to the old address")

const storageTargetCols = `id, name, provider, endpoint, bucket, region, prefix_template, path_style,
	access_key_id, secret_enc, offload_stems, offload_transcripts,
	offload_clips, verify_download, allow_private_target, enabled, max_attempts, binding_gen, created_at, updated_at`

func scanStorageTarget(box *secrets.Box, row interface{ Scan(...any) error }) (StorageTarget, error) {
	var t StorageTarget
	var enc []byte
	var created, updated int64
	err := row.Scan(&t.ID, &t.Name, &t.Provider, &t.Endpoint, &t.Bucket, &t.Region, &t.PrefixTemplate,
		&t.PathStyle, &t.AccessKeyID, &enc, &t.OffloadStems,
		&t.OffloadTranscripts, &t.OffloadClips, &t.VerifyDownload, &t.AllowPrivateTarget, &t.Enabled,
		&t.MaxAttempts, &t.BindingGen, &created, &updated)
	if err != nil {
		return t, err
	}
	t.CreatedAt = time.Unix(created, 0).UTC()
	t.UpdatedAt = time.Unix(updated, 0).UTC()
	if len(enc) > 0 {
		t.HasSecret = true
		if box == nil {
			t.SecretUnreadable = true
		} else if plain, oerr := box.Open(enc); oerr != nil {
			// Not a row failure: the target still lists, says why it cannot
			// upload, and is cured by re-entering the secret.
			t.SecretUnreadable = true
		} else {
			t.SecretAccessKey = plain
		}
	}
	return t, nil
}

// ListStorageTargets returns every target, oldest first.
func (d *DB) ListStorageTargets(box *secrets.Box) ([]StorageTarget, error) {
	rows, err := d.sql.Query(`SELECT ` + storageTargetCols + ` FROM storage_targets ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StorageTarget{}
	for rows.Next() {
		t, err := scanStorageTarget(box, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetStorageTarget returns one target or ErrNotFound.
func (d *DB) GetStorageTarget(box *secrets.Box, id int64) (StorageTarget, error) {
	t, err := scanStorageTarget(box, d.sql.QueryRow(`SELECT `+storageTargetCols+` FROM storage_targets WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

func sealTargetSecret(box *secrets.Box, secret string) ([]byte, error) {
	if box == nil {
		return nil, fmt.Errorf("no secret key is configured, so the storage credential cannot be stored sealed")
	}
	return box.Seal(secret)
}

// CreateStorageTarget inserts t, sealing t.SecretAccessKey, and returns its id.
func (d *DB) CreateStorageTarget(box *secrets.Box, t StorageTarget) (int64, error) {
	var enc []byte
	if t.SecretAccessKey != "" {
		var err error
		if enc, err = sealTargetSecret(box, t.SecretAccessKey); err != nil {
			return 0, err
		}
	}
	now := time.Now().Unix()
	res, err := d.sql.Exec(`INSERT INTO storage_targets (name, provider, endpoint, bucket, region,
		prefix_template, path_style, access_key_id, secret_enc, offload_stems,
		offload_transcripts, offload_clips, verify_download, allow_private_target, enabled, max_attempts, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.Name, t.Provider, t.Endpoint, t.Bucket, t.Region, t.PrefixTemplate, t.PathStyle, t.AccessKeyID,
		enc, t.OffloadStems, t.OffloadTranscripts, t.OffloadClips,
		t.VerifyDownload, t.AllowPrivateTarget, t.Enabled, maxAttemptsOr(t.MaxAttempts), now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateStorageTarget rewrites every field of t. An empty SecretAccessKey
// leaves the stored secret as it was, so a form that never displays the
// secret can still be saved. An edit that changes where objects live goes
// through RebindStorageTarget instead.
func (d *DB) UpdateStorageTarget(box *secrets.Box, t StorageTarget) error {
	q, args, err := updateTargetQuery(box, t, false)
	if err != nil {
		return err
	}
	res, err := d.sql.Exec(q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func updateTargetQuery(box *secrets.Box, t StorageTarget, rebind bool) (string, []any, error) {
	q := `UPDATE storage_targets SET name=?, provider=?, endpoint=?, bucket=?, region=?, prefix_template=?,
		path_style=?, access_key_id=?, offload_stems=?, offload_transcripts=?,
		offload_clips=?, verify_download=?, allow_private_target=?, enabled=?, max_attempts=?, updated_at=?`
	args := []any{t.Name, t.Provider, t.Endpoint, t.Bucket, t.Region, t.PrefixTemplate, t.PathStyle,
		t.AccessKeyID, t.OffloadStems, t.OffloadTranscripts, t.OffloadClips,
		t.VerifyDownload, t.AllowPrivateTarget, t.Enabled, maxAttemptsOr(t.MaxAttempts), time.Now().Unix()}
	if rebind {
		q += `, binding_gen = binding_gen + 1`
	}
	if t.SecretAccessKey != "" {
		enc, err := sealTargetSecret(box, t.SecretAccessKey)
		if err != nil {
			return "", nil, err
		}
		q += `, secret_enc=?`
		args = append(args, enc)
	}
	q += ` WHERE id=?`
	args = append(args, t.ID)
	return q, args, nil
}

func maxAttemptsOr(n int) int {
	if n <= 0 {
		return DefaultOffloadMaxAttempts
	}
	return n
}

// RetryOffloadItems puts failed and stuck items back in the queue with their
// attempt count reset: the operator's Retry now. target 0 means every target;
// rel "" means every file. It returns how many items moved.
func (d *DB) RetryOffloadItems(target int64, rel string) (int64, error) {
	q := `UPDATE offload_items SET state = ?, attempts = 0, next_attempt_at = 0, updated_at = ?
		WHERE state IN (?, ?)`
	args := []any{OffloadPending, time.Now().Unix(), OffloadFailed, OffloadStuck}
	if target != 0 {
		q += ` AND target_id = ?`
		args = append(args, target)
	}
	if rel != "" {
		q += ` AND rel_path = ?`
		args = append(args, rel)
	}
	res, err := d.sql.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteStorageTarget removes a target, its item rows and its multipart
// progress. Mirror mode never deletes a local file, so there is no copy a
// target can be the only holder of; objects already in the bucket stay there.
func (d *DB) DeleteStorageTarget(id int64) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		`DELETE FROM offload_multipart WHERE target_id = ?`,
		`DELETE FROM offload_items WHERE target_id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	res, err := tx.Exec(`DELETE FROM storage_targets WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// RebindStorageTarget is UpdateStorageTarget for an edit that changes WHERE
// objects live (endpoint, bucket, region, access key, addressing). In one
// transaction it saves, advances the target's binding generation -- so an
// upload still running against the old address can no longer write its
// result -- puts every row back to pending, since nothing is verified at the
// new address yet, and drops multipart progress made at the old one.
func (d *DB) RebindStorageTarget(box *secrets.Box, t StorageTarget) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q, args, err := updateTargetQuery(box, t, true)
	if err != nil {
		return err
	}
	res, err := tx.Exec(q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`UPDATE offload_items SET state = ?, attempts = 0, next_attempt_at = 0, verified_at = 0,
		last_error = 'target moved; uploading to the new address', updated_at = ? WHERE target_id = ?`,
		OffloadPending, time.Now().Unix(), t.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM offload_multipart WHERE target_id = ?`, t.ID); err != nil {
		return err
	}
	return tx.Commit()
}

const offloadItemCols = `target_id, rel_path, kind, object_key, bytes, sha256, state, attempts,
	next_attempt_at, last_error, verified_at, updated_at`

func scanOffloadItem(row interface{ Scan(...any) error }) (OffloadItem, error) {
	var it OffloadItem
	var next, verified, updated int64
	err := row.Scan(&it.TargetID, &it.RelPath, &it.Kind, &it.Key, &it.Bytes, &it.SHA256, &it.State,
		&it.Attempts, &next, &it.LastError, &verified, &updated)
	if err != nil {
		return it, err
	}
	it.NextAttemptAt = offloadTime(next)
	it.VerifiedAt = offloadTime(verified)
	it.UpdatedAt = time.Unix(updated, 0).UTC()
	return it, nil
}

func offloadTime(s int64) time.Time {
	if s == 0 {
		return time.Time{}
	}
	return time.Unix(s, 0).UTC()
}

func zeroOrUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// ListOffloadItems returns every item row.
func (d *DB) ListOffloadItems() ([]OffloadItem, error) {
	rows, err := d.sql.Query(`SELECT ` + offloadItemCols + ` FROM offload_items ORDER BY rel_path, target_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OffloadItem{}
	for rows.Next() {
		it, err := scanOffloadItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// OffloadItemsFor returns the item rows for one file across every target.
func (d *DB) OffloadItemsFor(relPath string) ([]OffloadItem, error) {
	rows, err := d.sql.Query(`SELECT `+offloadItemCols+` FROM offload_items WHERE rel_path = ? ORDER BY target_id`, relPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OffloadItem{}
	for rows.Next() {
		it, err := scanOffloadItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

const upsertItem = `INSERT INTO offload_items (` + offloadItemCols + `)
	SELECT ?,?,?,?,?,?,?,?,?,?,?,? %s
	ON CONFLICT(target_id, rel_path) DO UPDATE SET kind=excluded.kind, object_key=excluded.object_key,
		bytes=excluded.bytes, sha256=excluded.sha256, state=excluded.state, attempts=excluded.attempts,
		next_attempt_at=excluded.next_attempt_at, last_error=excluded.last_error,
		verified_at=excluded.verified_at, updated_at=excluded.updated_at`

func itemArgs(it OffloadItem) []any {
	return []any{it.TargetID, it.RelPath, it.Kind, it.Key, it.Bytes, it.SHA256, it.State, it.Attempts,
		zeroOrUnix(it.NextAttemptAt), it.LastError, zeroOrUnix(it.VerifiedAt), time.Now().Unix()}
}

// PutOffloadItem inserts or replaces one item row.
func (d *DB) PutOffloadItem(it OffloadItem) error {
	if it.TargetID == 0 || strings.TrimSpace(it.RelPath) == "" {
		return errors.New("offload item needs a target and a path")
	}
	_, err := d.sql.Exec(fmt.Sprintf(upsertItem, "WHERE true"), itemArgs(it)...)
	return err
}

// PutOffloadItemIfBinding is PutOffloadItem for a write an upload makes: it
// lands only while the target still exists at binding generation gen, the
// one the upload's pass started with. A target moved (or deleted) since gets
// ErrBindingChanged and its row is left exactly as the move left it.
func (d *DB) PutOffloadItemIfBinding(it OffloadItem, gen int64) error {
	if it.TargetID == 0 || strings.TrimSpace(it.RelPath) == "" {
		return errors.New("offload item needs a target and a path")
	}
	res, err := d.sql.Exec(fmt.Sprintf(upsertItem,
		"WHERE (SELECT binding_gen FROM storage_targets WHERE id = ?) = ?"),
		append(itemArgs(it), it.TargetID, gen)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBindingChanged
	}
	return nil
}

// OffloadKeyHolder returns the file that owns key on target, "" when none.
func (d *DB) OffloadKeyHolder(target int64, key string) (string, error) {
	var rel string
	err := d.sql.QueryRow(`SELECT rel_path FROM offload_items WHERE target_id = ? AND object_key = ?`, target, key).Scan(&rel)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return rel, err
}

// DeleteOffloadItemsFor drops the rows of a file that no longer exists
// anywhere the index knows about.
func (d *DB) DeleteOffloadItemsFor(relPath string) error {
	_, err := d.sql.Exec(`DELETE FROM offload_items WHERE rel_path = ?`, relPath)
	return err
}
