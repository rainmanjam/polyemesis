package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits on one viewer note. They are Go's to own: the wire contract carries
// them to the console, which checks them before it sends.
const (
	// MaxViewerNoteLen is the longest note, in characters.
	MaxViewerNoteLen = 1000
	// MaxViewerTags is how many tags one viewer can carry.
	MaxViewerTags = 10
	// MaxViewerTagLen is the longest single tag, in characters.
	MaxViewerTagLen = 32
)

// ViewerPresetTags are the tags the console offers as one-press chips. They are
// a convenience, not a closed set: a free tag is stored the same way.
var ViewerPresetTags = []string{"regular", "known troll", "VIP", "watch"}

// ErrViewerNoteInvalid wraps every rejection of a note's contents, so a handler
// can answer 400 for them and not for a store failure.
var ErrViewerNoteInvalid = errors.New("viewer note is not valid")

// ViewerNote is the operator's private note about one viewer in one channel.
//
// It is polyemesis's OWN record, written by an operator and read back by an
// operator. Nothing here is sent to a platform, an overlay, the event stream or
// a hook: it is personal data about a viewer, so it is stored locally and its
// routes are admin-only and refused to a read token.
type ViewerNote struct {
	Platform Platform
	Account  string
	AuthorID string
	Note     string
	Tags     []string
	// UpdatedAt is when the operator last saved it.
	UpdatedAt time.Time
	// UpdatedBy is the operator's username, or "token" for an API token.
	UpdatedBy string
}

// ViewerTagSummary is a viewer's tags without the note, for a list that marks
// tagged viewers. The note's text never rides along.
type ViewerTagSummary struct {
	Platform Platform
	Account  string
	AuthorID string
	Tags     []string
}

// MigrateViewerNotes creates the viewer-notes table.
//
// The statement is idempotent, so Open runs it on every boot. It does not move
// currentSchemaVersion: an older binary never reads this table, the same
// posture as chat_sanctions.
//
// Retention follows chat_sanctions: nothing here expires and nothing is cleared
// when the chat scrollback is pruned, because a note is something the operator
// wrote on purpose and a retention timer must not take it away. A note leaves
// only when the operator deletes it (or saves it empty). Backups copy the whole
// database file, so notes ride along with it.
func (d *DB) MigrateViewerNotes() error {
	_, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS viewer_notes (
    platform   TEXT    NOT NULL,
    account    TEXT    NOT NULL DEFAULT '',
    author_id  TEXT    NOT NULL,
    note       TEXT    NOT NULL DEFAULT '',
    tags       TEXT    NOT NULL DEFAULT '[]',  -- JSON array of strings, at most 10
    updated_ms INTEGER NOT NULL DEFAULT 0,
    updated_by TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (platform, account, author_id)
)`)
	return err
}

// NormalizeViewerNote trims and validates a note and its tags. Tags are
// trimmed, blank ones dropped, and duplicates (case-insensitive) collapsed to
// the first spelling.
func NormalizeViewerNote(note string, tags []string) (string, []string, error) {
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > MaxViewerNoteLen {
		return "", nil, errors.Join(ErrViewerNoteInvalid, errors.New("the note is longer than the limit"))
	}
	out := []string{}
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if utf8.RuneCountInString(tag) > MaxViewerTagLen {
			return "", nil, errors.Join(ErrViewerNoteInvalid, errors.New("a tag is longer than the limit"))
		}
		key := strings.ToLower(tag)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, tag)
	}
	if len(out) > MaxViewerTags {
		return "", nil, errors.Join(ErrViewerNoteInvalid, errors.New("too many tags"))
	}
	return note, out, nil
}

// PutViewerNote stores n, replacing any earlier note on the same viewer in the
// same channel. A note with no text and no tags is the same as none, so it
// deletes the row rather than storing an empty one. Contents are validated
// here as well as by the handler, so no caller can store past the caps.
func (d *DB) PutViewerNote(n ViewerNote) error {
	note, tags, err := NormalizeViewerNote(n.Note, n.Tags)
	if err != nil {
		return err
	}
	if note == "" && len(tags) == 0 {
		return d.DeleteViewerNote(n.Platform, n.Account, n.AuthorID)
	}
	raw, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	at := n.UpdatedAt
	if at.IsZero() {
		at = time.Now()
	}
	_, err = d.sql.Exec(`INSERT INTO viewer_notes
		(platform, account, author_id, note, tags, updated_ms, updated_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (platform, account, author_id) DO UPDATE SET
			note = excluded.note, tags = excluded.tags,
			updated_ms = excluded.updated_ms, updated_by = excluded.updated_by`,
		n.Platform, n.Account, n.AuthorID, note, string(raw), at.UnixMilli(), n.UpdatedBy)
	return err
}

// DeleteViewerNote removes a viewer's note. Missing is success.
func (d *DB) DeleteViewerNote(p Platform, account, authorID string) error {
	_, err := d.sql.Exec(`DELETE FROM viewer_notes WHERE platform = ? AND account = ? AND author_id = ?`,
		p, account, authorID)
	return err
}

// GetViewerNote reads a viewer's note, and reports false when there is none.
func (d *DB) GetViewerNote(p Platform, account, authorID string) (ViewerNote, bool, error) {
	var (
		n    = ViewerNote{Platform: p, Account: account, AuthorID: authorID}
		raw  string
		upMS int64
	)
	err := d.sql.QueryRow(`SELECT note, tags, updated_ms, updated_by FROM viewer_notes
		WHERE platform = ? AND account = ? AND author_id = ?`, p, account, authorID).
		Scan(&n.Note, &raw, &upMS, &n.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return ViewerNote{}, false, nil
	}
	if err != nil {
		return ViewerNote{}, false, err
	}
	n.Tags = decodeViewerTags(raw)
	n.UpdatedAt = time.UnixMilli(upMS)
	return n, true, nil
}

// maxViewerTagSummaries bounds the tagged-viewers list.
const maxViewerTagSummaries = 5000

// ListViewerTags lists every viewer who carries at least one tag, newest edit
// first, without their notes.
func (d *DB) ListViewerTags() ([]ViewerTagSummary, error) {
	rows, err := d.sql.Query(`SELECT platform, account, author_id, tags FROM viewer_notes
		WHERE tags != '[]' ORDER BY updated_ms DESC LIMIT ?`, maxViewerTagSummaries)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ViewerTagSummary{}
	for rows.Next() {
		var (
			s   ViewerTagSummary
			raw string
		)
		if err := rows.Scan(&s.Platform, &s.Account, &s.AuthorID, &raw); err != nil {
			return nil, err
		}
		s.Tags = decodeViewerTags(raw)
		out = append(out, s)
	}
	return out, rows.Err()
}

func decodeViewerTags(raw string) []string {
	tags := []string{}
	if err := json.Unmarshal([]byte(raw), &tags); err != nil || tags == nil {
		return []string{}
	}
	return tags
}
