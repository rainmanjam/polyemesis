package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Highlight suggestions and saved vertical-export templates (Wave 4 #5).
//
// A suggestion is a candidate short the analysis found and a human has not yet
// judged. It is never a clip: the ONLY way one becomes a clip is an operator's
// accept, which is a one-shot state change (DecideHighlight) followed by an
// ordinary clip.export job. Nothing in this file submits a job.

// The states a suggestion moves through. Stored and sent to the UI; not renamed.
const (
	HighlightPending   = "pending"
	HighlightAccepted  = "accepted"
	HighlightDismissed = "dismissed"
)

// ErrHighlightDecided is a decision on a suggestion somebody already decided:
// the second of two accepts, or a dismiss after an accept.
var ErrHighlightDecided = errors.New("that suggestion has already been decided")

// Highlight is one stored suggestion. Signals is the detector output as JSON;
// this package does not need to know its shape.
type Highlight struct {
	ID          int64           `json:"id"`
	RecordingID int64           `json:"recordingId"`
	StartMS     int64           `json:"startMs"`
	EndMS       int64           `json:"endMs"`
	Score       float64         `json:"score"`
	SignalScore float64         `json:"signalScore"`
	ModelScore  *float64        `json:"modelScore,omitempty"`
	Signals     json.RawMessage `json:"signals"`
	Excerpt     string          `json:"excerpt"`
	State       string          `json:"state"`
	// JobID is the clip.export job an accept created, zero until then.
	JobID     int64     `json:"jobId,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	DecidedAt time.Time `json:"decidedAt,omitzero"`
}

// ClipTemplate is a saved vertical layout and caption style, by name.
type ClipTemplate struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Body      json.RawMessage `json:"body"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

// MigrateClipHighlights creates both tables. Idempotent, run on every boot; it
// does not move currentSchemaVersion, because an older binary never reads
// either table.
func (d *DB) MigrateClipHighlights() error {
	_, err := d.sql.Exec(`
CREATE TABLE IF NOT EXISTS clip_highlights (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    recording_id INTEGER NOT NULL,
    start_ms     INTEGER NOT NULL,
    end_ms       INTEGER NOT NULL,
    score        REAL    NOT NULL DEFAULT 0,
    signal_score REAL    NOT NULL DEFAULT 0,
    model_score  REAL,
    signals      TEXT    NOT NULL DEFAULT '[]',
    excerpt      TEXT    NOT NULL DEFAULT '',
    state        TEXT    NOT NULL DEFAULT 'pending',  -- pending | accepted | dismissed
    job_id       INTEGER NOT NULL DEFAULT 0,
    created_ms   INTEGER NOT NULL DEFAULT 0,
    decided_ms   INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (recording_id) REFERENCES recordings(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_clip_highlights_recording ON clip_highlights(recording_id, score DESC);
CREATE TABLE IF NOT EXISTS clip_templates (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL UNIQUE,
    body       TEXT    NOT NULL DEFAULT '{}',
    updated_ms INTEGER NOT NULL DEFAULT 0
);`)
	return err
}

const highlightColumns = `id, recording_id, start_ms, end_ms, score, signal_score, model_score,
	signals, excerpt, state, job_id, created_ms, decided_ms`

func scanHighlight(row interface{ Scan(...any) error }) (Highlight, error) {
	var h Highlight
	var model sql.NullFloat64
	var signals string
	var created, decided int64
	if err := row.Scan(&h.ID, &h.RecordingID, &h.StartMS, &h.EndMS, &h.Score, &h.SignalScore, &model,
		&signals, &h.Excerpt, &h.State, &h.JobID, &created, &decided); err != nil {
		return Highlight{}, err
	}
	if model.Valid {
		v := model.Float64
		h.ModelScore = &v
	}
	h.Signals = json.RawMessage(signals)
	h.CreatedAt = time.UnixMilli(created).UTC()
	if decided > 0 {
		h.DecidedAt = time.UnixMilli(decided).UTC()
	}
	return h, nil
}

// decidedOverlap is how much of a new suggestion has to lie inside one the
// operator already decided for it to be the same moment.
const decidedOverlap = 0.5

// ReplacePendingHighlights stores a scan's suggestions for a recording.
//
// Undecided suggestions from an earlier scan are replaced; decided ones are
// kept, and a new suggestion that is mostly the same span as a decided one is
// dropped -- a moment the operator dismissed is not offered again because the
// analysis ran twice, and one they clipped is not offered for clipping twice.
// It reports how many were stored.
func (d *DB) ReplacePendingHighlights(recordingID int64, hs []Highlight) (int, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM clip_highlights WHERE recording_id = ? AND state = ?`,
		recordingID, HighlightPending); err != nil {
		return 0, err
	}
	rows, err := tx.Query(`SELECT start_ms, end_ms FROM clip_highlights WHERE recording_id = ?`, recordingID)
	if err != nil {
		return 0, err
	}
	var decided [][2]int64
	for rows.Next() {
		var s, e int64
		if err := rows.Scan(&s, &e); err != nil {
			rows.Close()
			return 0, err
		}
		decided = append(decided, [2]int64{s, e})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	now := time.Now().UnixMilli()
	stored := 0
	for _, h := range hs {
		if sameMoment(h, decided) {
			continue
		}
		signals := string(h.Signals)
		if signals == "" {
			signals = "[]"
		}
		var model any
		if h.ModelScore != nil {
			model = *h.ModelScore
		}
		if _, err := tx.Exec(`INSERT INTO clip_highlights
			(recording_id, start_ms, end_ms, score, signal_score, model_score, signals, excerpt, state, created_ms)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			recordingID, h.StartMS, h.EndMS, h.Score, h.SignalScore, model, signals, h.Excerpt,
			HighlightPending, now); err != nil {
			return 0, err
		}
		stored++
	}
	return stored, tx.Commit()
}

// sameMoment reports whether h is mostly a moment already decided. The
// overlap is measured against the SHORTER of the two spans: a dismissed 15 s
// moment wholly inside a new 60 s suggestion is that moment, though it is only
// a quarter of the new span.
func sameMoment(h Highlight, decided [][2]int64) bool {
	span := h.EndMS - h.StartMS
	if span <= 0 {
		return false
	}
	for _, d := range decided {
		overlap := min(h.EndMS, d[1]) - max(h.StartMS, d[0])
		shorter := min(span, d[1]-d[0])
		if overlap > 0 && shorter > 0 && float64(overlap)/float64(shorter) >= decidedOverlap {
			return true
		}
	}
	return false
}

// ListHighlights is a recording's suggestions, best first.
func (d *DB) ListHighlights(recordingID int64) ([]Highlight, error) {
	rows, err := d.sql.Query(`SELECT `+highlightColumns+` FROM clip_highlights
		WHERE recording_id = ? ORDER BY score DESC, start_ms ASC, id ASC`, recordingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Highlight{}
	for rows.Next() {
		h, err := scanHighlight(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// GetHighlight is one suggestion.
func (d *DB) GetHighlight(id int64) (Highlight, error) {
	h, err := scanHighlight(d.sql.QueryRow(`SELECT `+highlightColumns+` FROM clip_highlights WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Highlight{}, ErrNotFound
	}
	return h, err
}

// DecideHighlight moves a PENDING suggestion to accepted or dismissed, once.
//
// One conditional UPDATE rather than a read and a write: two accepts arriving
// together both read "pending", and only the database can say which one won.
// The loser gets ErrHighlightDecided and must not submit a clip.
func (d *DB) DecideHighlight(id int64, state string) (Highlight, error) {
	if state != HighlightAccepted && state != HighlightDismissed {
		return Highlight{}, fmt.Errorf("unknown suggestion state %q", state)
	}
	res, err := d.sql.Exec(`UPDATE clip_highlights SET state = ?, decided_ms = ?
		WHERE id = ? AND state = ?`, state, time.Now().UnixMilli(), id, HighlightPending)
	if err != nil {
		return Highlight{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := d.GetHighlight(id); err != nil {
			return Highlight{}, err
		}
		return Highlight{}, ErrHighlightDecided
	}
	return d.GetHighlight(id)
}

// SetHighlightJob records the clip.export job an accept created.
func (d *DB) SetHighlightJob(id, jobID int64) error {
	_, err := d.sql.Exec(`UPDATE clip_highlights SET job_id = ? WHERE id = ?`, jobID, id)
	return err
}

// ReopenHighlight hands an accepted suggestion back to the queue, for when the
// clip it was accepted for could not be queued at all.
func (d *DB) ReopenHighlight(id int64) error {
	_, err := d.sql.Exec(`UPDATE clip_highlights SET state = ?, decided_ms = 0, job_id = 0 WHERE id = ?`,
		HighlightPending, id)
	return err
}

// ChatStamp is one stored chat message's id and arrival time, Unix ms.
type ChatStamp struct {
	ID   int64
	AtMS int64
}

// ChatStampsBetween is every chat message in [fromMS, toMS) -- the chat
// detector's whole input -- with its id, so a caller asking about overlapping
// spans can count each message once. Messages polyemesis relayed itself are
// not viewers reacting, and are left out.
func (d *DB) ChatStampsBetween(fromMS, toMS int64) ([]ChatStamp, error) {
	rows, err := d.sql.Query(`SELECT id, at_ms FROM chat_messages
		WHERE at_ms >= ? AND at_ms < ? AND echo = 0 ORDER BY at_ms, id`, fromMS, toMS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatStamp
	for rows.Next() {
		var c ChatStamp
		if err := rows.Scan(&c.ID, &c.AtMS); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListClipTemplates is every saved template, by name.
func (d *DB) ListClipTemplates() ([]ClipTemplate, error) {
	rows, err := d.sql.Query(`SELECT id, name, body, updated_ms FROM clip_templates ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ClipTemplate{}
	for rows.Next() {
		var t ClipTemplate
		var body string
		var at int64
		if err := rows.Scan(&t.ID, &t.Name, &body, &at); err != nil {
			return nil, err
		}
		t.Body = json.RawMessage(body)
		t.UpdatedAt = time.UnixMilli(at).UTC()
		out = append(out, t)
	}
	return out, rows.Err()
}

// PutClipTemplate saves a template, replacing one of the same name.
func (d *DB) PutClipTemplate(name string, body []byte) (ClipTemplate, error) {
	now := time.Now().UnixMilli()
	if _, err := d.sql.Exec(`INSERT INTO clip_templates (name, body, updated_ms) VALUES (?,?,?)
		ON CONFLICT(name) DO UPDATE SET body = excluded.body, updated_ms = excluded.updated_ms`,
		name, string(body), now); err != nil {
		return ClipTemplate{}, err
	}
	var t ClipTemplate
	var raw string
	var at int64
	err := d.sql.QueryRow(`SELECT id, name, body, updated_ms FROM clip_templates WHERE name = ?`, name).
		Scan(&t.ID, &t.Name, &raw, &at)
	t.Body = json.RawMessage(raw)
	t.UpdatedAt = time.UnixMilli(at).UTC()
	return t, err
}

// DeleteClipTemplate removes one template.
func (d *DB) DeleteClipTemplate(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM clip_templates WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
