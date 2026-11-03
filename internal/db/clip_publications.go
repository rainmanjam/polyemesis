package db

// The ledger behind clip publishing (Wave 4 #5c).
//
// ONE ROW PER CLIP + PLATFORM + ACCOUNT, AND THE DATABASE ENFORCES IT. The job
// queue's Unique flag only folds submissions into an ACTIVE job, so it says
// nothing once a job has finished or failed; a second click a day later would
// post the clip again. This table is what remembers. UNIQUE(clip_job_id,
// platform, account_id) turns "post it twice" into "the same row", and the
// row carries the platform's own post id once there is one.
//
// THE ROW IS ALSO THE PROOF OF A CLICK. The only code that creates one is the
// session-only publish handler, which stamps it with a nonce that never leaves
// the server except inside the job it queues. BeginPublication, the worker's
// first call, refuses unless that nonce matches and the row is in a state that
// may advance. A job that did not come from that handler -- a generic retry, a
// job somebody hand-edited -- has no valid nonce and does nothing.
//
// New table only: it is created idempotently on every boot and does not move
// currentSchemaVersion, because an older binary never reads it.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// Publication states. A row is only ever created in confirmed, because the only
// creator is the confirmed click.
const (
	PublicationConfirmed = "confirmed" // clicked, job queued, nothing sent yet
	PublicationUploading = "uploading" // a worker has been on it
	PublicationPosted    = "posted"    // the platform has the post; terminal
	PublicationFailed    = "failed"    // gave up; only a new click re-arms it
)

var (
	// ErrPublicationNotArmed is the worker's refusal: the nonce is wrong or the
	// row is failed. It is permanent for the job that hit it.
	ErrPublicationNotArmed = errors.New("this publication was not confirmed by a click, or has already been given up on")
	// ErrPublicationPosted means the post already exists; the worker's correct
	// answer is success without sending anything.
	ErrPublicationPosted = errors.New("this clip is already posted to that account")
)

// ClaimOutcome says what ClaimPublication did.
type ClaimOutcome int

const (
	// ClaimCreated: a new row, a job should be queued.
	ClaimCreated ClaimOutcome = iota
	// ClaimRearmed: a failed row was re-armed by this click, a job should be queued.
	ClaimRearmed
	// ClaimInFlight: a job is already working on it; queue nothing.
	ClaimInFlight
	// ClaimPosted: already on the platform; queue nothing.
	ClaimPosted
)

// Publication is one clip going to one account.
type Publication struct {
	ID int64 `json:"id"`
	// IdemKey is the idempotency key: clip + platform + account.
	IdemKey     string   `json:"idempotencyKey"`
	ClipJobID   int64    `json:"clipJobId"`
	Platform    string   `json:"platform"`
	AccountID   int64    `json:"accountId"`
	AccountName string   `json:"accountName"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	// Privacy is what the post was made with. For TikTok it is always SELF_ONLY.
	Privacy string `json:"privacy"`
	State   string `json:"state"`

	// ContentHash fingerprints exactly what the human confirmed.
	ContentHash string `json:"-"`
	// Nonce is the proof of the click. Never serialised.
	Nonce       string `json:"-"`
	ConfirmedBy int64  `json:"confirmedBy"`

	PublishJobID int64 `json:"publishJobId,omitempty"`
	// PlatformPostID is the platform's id for the post: a YouTube video id, a
	// TikTok publish_id (or post id once public), an X post id.
	PlatformPostID string `json:"platformPostId,omitempty"`
	PostURL        string `json:"postUrl,omitempty"`
	// ActualPrivacy is what the platform reports back, which can be stricter
	// than what was asked (an unaudited app is held private).
	ActualPrivacy string `json:"actualPrivacy,omitempty"`

	FailCause  string `json:"failCause,omitempty"`
	FailDetail string `json:"failDetail,omitempty"`
	Attempts   int    `json:"attempts"`
	// HasSession is true while a resumable upload session is stored. The
	// session itself is a secret and is never part of this struct.
	HasSession bool `json:"-"`

	FinalizingAt time.Time `json:"-"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	PostedAt     time.Time `json:"postedAt,omitzero"`
}

// PublicationKey is the idempotency key for a clip + platform + account.
func PublicationKey(clipJobID int64, platform string, accountID int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("clip:%d|%s|%d", clipJobID, platform, accountID)))
	return "pub_" + hex.EncodeToString(sum[:12])
}

// MigrateClipPublications creates the ledger. Idempotent, run on every boot.
func (d *DB) MigrateClipPublications() error {
	_, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS clip_publications (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    idem_key        TEXT    NOT NULL UNIQUE,
    clip_job_id     INTEGER NOT NULL,
    platform        TEXT    NOT NULL,
    account_id      INTEGER NOT NULL,
    account_name    TEXT    NOT NULL DEFAULT '',
    title           TEXT    NOT NULL DEFAULT '',
    description     TEXT    NOT NULL DEFAULT '',
    tags            TEXT    NOT NULL DEFAULT '[]',
    privacy         TEXT    NOT NULL DEFAULT '',
    state           TEXT    NOT NULL DEFAULT 'confirmed',  -- confirmed | uploading | posted | failed
    content_hash    TEXT    NOT NULL DEFAULT '',
    nonce           TEXT    NOT NULL DEFAULT '',
    confirmed_by    INTEGER NOT NULL DEFAULT 0,
    publish_job_id  INTEGER NOT NULL DEFAULT 0,
    platform_post_id TEXT   NOT NULL DEFAULT '',
    post_url        TEXT    NOT NULL DEFAULT '',
    actual_privacy  TEXT    NOT NULL DEFAULT '',
    fail_cause      TEXT    NOT NULL DEFAULT '',
    fail_detail     TEXT    NOT NULL DEFAULT '',
    attempts        INTEGER NOT NULL DEFAULT 0,
    session_enc     BLOB,
    session_hash    TEXT    NOT NULL DEFAULT '',
    finalizing_ms   INTEGER NOT NULL DEFAULT 0,
    created_ms      INTEGER NOT NULL DEFAULT 0,
    updated_ms      INTEGER NOT NULL DEFAULT 0,
    posted_ms       INTEGER NOT NULL DEFAULT 0,
    UNIQUE (clip_job_id, platform, account_id)
);
CREATE INDEX IF NOT EXISTS idx_clip_publications_clip ON clip_publications(clip_job_id);`)
	return err
}

const publicationColumns = `id, idem_key, clip_job_id, platform, account_id, account_name, title,
	description, tags, privacy, state, content_hash, nonce, confirmed_by, publish_job_id,
	platform_post_id, post_url, actual_privacy, fail_cause, fail_detail, attempts,
	session_enc IS NOT NULL AND length(session_enc) > 0, finalizing_ms, created_ms, updated_ms, posted_ms`

func scanPublication(row interface{ Scan(...any) error }) (Publication, error) {
	var p Publication
	var tags string
	var finalizing, created, updated, posted int64
	if err := row.Scan(&p.ID, &p.IdemKey, &p.ClipJobID, &p.Platform, &p.AccountID, &p.AccountName,
		&p.Title, &p.Description, &tags, &p.Privacy, &p.State, &p.ContentHash, &p.Nonce,
		&p.ConfirmedBy, &p.PublishJobID, &p.PlatformPostID, &p.PostURL, &p.ActualPrivacy,
		&p.FailCause, &p.FailDetail, &p.Attempts, &p.HasSession, &finalizing, &created, &updated,
		&posted); err != nil {
		return Publication{}, err
	}
	p.Tags = []string{}
	_ = json.Unmarshal([]byte(tags), &p.Tags)
	if finalizing > 0 {
		p.FinalizingAt = time.UnixMilli(finalizing).UTC()
	}
	p.CreatedAt = time.UnixMilli(created).UTC()
	p.UpdatedAt = time.UnixMilli(updated).UTC()
	if posted > 0 {
		p.PostedAt = time.UnixMilli(posted).UTC()
	}
	return p, nil
}

// GetPublication loads one row.
func (d *DB) GetPublication(id int64) (Publication, error) {
	p, err := scanPublication(d.sql.QueryRow(`SELECT `+publicationColumns+` FROM clip_publications WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Publication{}, ErrNotFound
	}
	return p, err
}

// ListPublicationsForClip is every publication of one clip export, newest first.
func (d *DB) ListPublicationsForClip(clipJobID int64) ([]Publication, error) {
	rows, err := d.sql.Query(`SELECT `+publicationColumns+` FROM clip_publications
		WHERE clip_job_id = ? ORDER BY id DESC`, clipJobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Publication{}
	for rows.Next() {
		p, err := scanPublication(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ClaimPublication is the atomic step between a click and a queued job.
//
// In one transaction it creates the row, re-arms a failed one, or reports that
// the post is already in flight or already made. Only ClaimCreated and
// ClaimRearmed mean a job should be queued; the caller must queue nothing for
// the others, which is how a double click, a second tab and a retry all
// converge on one post.
func (d *DB) ClaimPublication(c Publication) (Publication, ClaimOutcome, error) {
	if c.ClipJobID <= 0 || c.Platform == "" || c.AccountID <= 0 || c.Nonce == "" {
		return Publication{}, 0, errors.New("a publication needs a clip, a platform, an account and a confirmation nonce")
	}
	tags, _ := json.Marshal(nonNilTags(c.Tags))
	key := PublicationKey(c.ClipJobID, c.Platform, c.AccountID)
	now := time.Now().UnixMilli()

	tx, err := d.sql.Begin()
	if err != nil {
		return Publication{}, 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT INTO clip_publications
		(idem_key, clip_job_id, platform, account_id, account_name, title, description, tags, privacy,
		 state, content_hash, nonce, confirmed_by, created_ms, updated_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (clip_job_id, platform, account_id) DO NOTHING`,
		key, c.ClipJobID, c.Platform, c.AccountID, c.AccountName, c.Title, c.Description, string(tags),
		c.Privacy, PublicationConfirmed, c.ContentHash, c.Nonce, c.ConfirmedBy, now, now)
	if err != nil {
		return Publication{}, 0, err
	}
	outcome := ClaimCreated
	if n, _ := res.RowsAffected(); n == 0 {
		var state string
		if err := tx.QueryRow(`SELECT state FROM clip_publications WHERE idem_key = ?`, key).Scan(&state); err != nil {
			return Publication{}, 0, err
		}
		switch state {
		case PublicationPosted:
			outcome = ClaimPosted
		case PublicationFailed:
			if _, err := tx.Exec(`UPDATE clip_publications SET account_name = ?, title = ?, description = ?,
				tags = ?, privacy = ?, state = ?, content_hash = ?, nonce = ?, confirmed_by = ?,
				publish_job_id = 0, fail_cause = '', fail_detail = '', attempts = 0, finalizing_ms = 0,
				updated_ms = ? WHERE idem_key = ?`,
				c.AccountName, c.Title, c.Description, string(tags), c.Privacy, PublicationConfirmed,
				c.ContentHash, c.Nonce, c.ConfirmedBy, now, key); err != nil {
				return Publication{}, 0, err
			}
			outcome = ClaimRearmed
		default:
			outcome = ClaimInFlight
		}
	}
	row, err := scanPublication(tx.QueryRow(`SELECT `+publicationColumns+` FROM clip_publications WHERE idem_key = ?`, key))
	if err != nil {
		return Publication{}, 0, err
	}
	return row, outcome, tx.Commit()
}

func nonNilTags(t []string) []string {
	if t == nil {
		return []string{}
	}
	return t
}

// SetPublicationJob records the queue job working on a row, but only while the
// nonce is still the live one.
func (d *DB) SetPublicationJob(id int64, nonce string, jobID int64) error {
	_, err := d.sql.Exec(`UPDATE clip_publications SET publish_job_id = ?, updated_ms = ?
		WHERE id = ? AND nonce = ?`, jobID, time.Now().UnixMilli(), id, nonce)
	return err
}

// BeginPublication is the worker's gate and the heart of "never without a
// click". It succeeds only when the nonce matches the one the confirmed click
// minted AND the row is confirmed or uploading. It counts the attempt.
func (d *DB) BeginPublication(id int64, nonce string) (Publication, error) {
	if nonce == "" {
		return Publication{}, ErrPublicationNotArmed
	}
	res, err := d.sql.Exec(`UPDATE clip_publications SET state = ?, attempts = attempts + 1, updated_ms = ?
		WHERE id = ? AND nonce = ? AND state IN (?, ?)`,
		PublicationUploading, time.Now().UnixMilli(), id, nonce, PublicationConfirmed, PublicationUploading)
	if err != nil {
		return Publication{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		p, gerr := d.GetPublication(id)
		if gerr != nil {
			return Publication{}, gerr
		}
		if p.State == PublicationPosted && p.Nonce == nonce {
			return p, ErrPublicationPosted
		}
		return Publication{}, ErrPublicationNotArmed
	}
	return d.GetPublication(id)
}

// SetPublicationNote stores a short, non-secret progress line on an in-flight
// row ("retrying: the platform was busy").
func (d *DB) SetPublicationNote(id int64, note string) error {
	_, err := d.sql.Exec(`UPDATE clip_publications SET fail_detail = ?, updated_ms = ?
		WHERE id = ? AND state IN (?, ?)`, note, time.Now().UnixMilli(), id, PublicationConfirmed, PublicationUploading)
	return err
}

// SetPublicationSession seals and stores a resumable upload session (a YouTube
// session URI, a TikTok upload URL), with a hash of the metadata it was opened
// with. They are capabilities to write to the platform, so they never go in a
// job's params, log or result.
func (d *DB) SetPublicationSession(box *secrets.Box, id int64, session, metaHash string) error {
	sealed, err := box.Seal(session)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`UPDATE clip_publications SET session_enc = ?, session_hash = ?, updated_ms = ? WHERE id = ?`,
		sealed, metaHash, time.Now().UnixMilli(), id)
	return err
}

// GetPublicationSession opens the stored session; empty when there is none.
func (d *DB) GetPublicationSession(box *secrets.Box, id int64) (session, metaHash string, err error) {
	var sealed []byte
	if err = d.sql.QueryRow(`SELECT session_enc, session_hash FROM clip_publications WHERE id = ?`, id).
		Scan(&sealed, &metaHash); err != nil {
		return "", "", err
	}
	if len(sealed) == 0 {
		return "", "", nil
	}
	session, err = box.Open(sealed)
	return session, metaHash, err
}

// ClearPublicationSession forgets the stored session.
func (d *DB) ClearPublicationSession(id int64) error {
	_, err := d.sql.Exec(`UPDATE clip_publications SET session_enc = NULL, session_hash = '', updated_ms = ? WHERE id = ?`,
		time.Now().UnixMilli(), id)
	return err
}

// SetPublicationRemote records the platform's id for a post that exists but is
// not finished (a TikTok publish_id before processing completes), so a retry
// polls it instead of starting a second post.
func (d *DB) SetPublicationRemote(id int64, remoteID string) error {
	_, err := d.sql.Exec(`UPDATE clip_publications SET platform_post_id = ?, updated_ms = ?
		WHERE id = ? AND state IN (?, ?)`, remoteID, time.Now().UnixMilli(), id,
		PublicationConfirmed, PublicationUploading)
	return err
}

// StampPublicationFinalizing marks the moment just before the one call that
// cannot be asked about afterwards. It reports true only for the caller that
// set it, so a retry that finds it already set knows the outcome is unknown
// and must not repeat the call.
func (d *DB) StampPublicationFinalizing(id int64) (bool, error) {
	res, err := d.sql.Exec(`UPDATE clip_publications SET finalizing_ms = ?, updated_ms = ?
		WHERE id = ? AND finalizing_ms = 0`, time.Now().UnixMilli(), time.Now().UnixMilli(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// RecordPublicationPost is the terminal success. Recording the same post id
// again is a no-op; a different id on a posted row is refused, because it would
// mean a second post.
func (d *DB) RecordPublicationPost(id int64, postID, postURL, actualPrivacy string) error {
	if postID == "" {
		return errors.New("a post needs a platform id")
	}
	now := time.Now().UnixMilli()
	res, err := d.sql.Exec(`UPDATE clip_publications SET state = ?, platform_post_id = ?, post_url = ?,
		actual_privacy = ?, fail_cause = '', fail_detail = '', session_enc = NULL, posted_ms = ?, updated_ms = ?
		WHERE id = ? AND (state IN (?, ?) OR (state = ? AND platform_post_id = ?))`,
		PublicationPosted, postID, postURL, actualPrivacy, now, now, id,
		PublicationConfirmed, PublicationUploading, PublicationPosted, postID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("publication %d cannot record post %q: it is failed or already holds a different post", id, postID)
	}
	return nil
}

// FailPublication gives up on a row. Only a new click re-arms it.
func (d *DB) FailPublication(id int64, cause, detail string) error {
	_, err := d.sql.Exec(`UPDATE clip_publications SET state = ?, fail_cause = ?, fail_detail = ?, updated_ms = ?
		WHERE id = ? AND state IN (?, ?)`, PublicationFailed, cause, detail, time.Now().UnixMilli(), id,
		PublicationConfirmed, PublicationUploading)
	return err
}
