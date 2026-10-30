package db

import (
	"time"
	"unicode/utf8"
)

// Retention for the operator timeline. Both caps apply on every write: the
// row cap bounds a flapping destination's contribution, and the age cap bounds
// a quiet install's. Measured against the event being written, not the wall
// clock, so the same history prunes the same way every time.
const (
	timelineMaxRows   = 1000
	timelineMaxAge    = 30 * 24 * time.Hour
	timelineTextLimit = 300
)

// TimelineEvent is one row of the stream history. Title and Detail are the
// notifier's already-redacted text, clipped again here.
type TimelineEvent struct {
	ID       int64
	At       time.Time
	Type     string
	Severity string
	Title    string
	Detail   string
}

// clip cuts s to the text limit without splitting a multi-byte character: it
// backs off to the last rune boundary at or before the limit.
func clip(s string) string {
	if len(s) <= timelineTextLimit {
		return s
	}
	cut := timelineTextLimit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// RecordTimelineEvent appends one event and prunes what the caps no longer
// keep. It is called from the notifier's recorder goroutine, so it may wait on
// the database without holding up the reconcile loop.
func (d *DB) RecordTimelineEvent(ev TimelineEvent) error {
	cutoff := ev.At.Add(-timelineMaxAge).UnixMilli()
	if _, err := d.sql.Exec(
		`INSERT INTO timeline_events (at, type, severity, title, detail) VALUES (?, ?, ?, ?, ?)`,
		ev.At.UnixMilli(), ev.Type, ev.Severity, clip(ev.Title), clip(ev.Detail),
	); err != nil {
		return err
	}
	if _, err := d.sql.Exec(`DELETE FROM timeline_events WHERE at < ?`, cutoff); err != nil {
		return err
	}
	_, err := d.sql.Exec(
		`DELETE FROM timeline_events WHERE id NOT IN (SELECT id FROM timeline_events ORDER BY id DESC LIMIT ?)`,
		timelineMaxRows,
	)
	return err
}

// ListTimelineEvents returns the newest events first.
func (d *DB) ListTimelineEvents(limit int) ([]TimelineEvent, error) {
	if limit <= 0 || limit > timelineMaxRows {
		limit = timelineMaxRows
	}
	rows, err := d.sql.Query(
		`SELECT id, at, type, severity, title, detail FROM timeline_events ORDER BY id DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TimelineEvent{}
	for rows.Next() {
		var e TimelineEvent
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.Type, &e.Severity, &e.Title, &e.Detail); err != nil {
			return nil, err
		}
		e.At = time.UnixMilli(at).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
