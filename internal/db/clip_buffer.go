package db

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// SourceClipBuffer is one programme's stored clip-buffer switch (app-council
// A14). See the source_clip_buffers comment in schema.sql for why it is its own
// table and why no row reads as off.
type SourceClipBuffer struct {
	Enabled bool
	// WindowSeconds is the window the operator last applied. 0 means none was
	// ever stored, and the engine keeps its default.
	WindowSeconds int
}

// GetSourceClipBuffer reads one programme's switch. A programme with no row --
// every one that predates the table, and every new one -- is off.
func (d *DB) GetSourceClipBuffer(sourceID int64) (SourceClipBuffer, error) {
	var enabled, window int
	err := d.sql.QueryRow(
		`SELECT enabled, window_seconds FROM source_clip_buffers WHERE source_id = ?`,
		sourceID).Scan(&enabled, &window)
	if errors.Is(err, sql.ErrNoRows) {
		return SourceClipBuffer{}, nil
	}
	if err != nil {
		return SourceClipBuffer{}, err
	}
	return SourceClipBuffer{Enabled: enabled != 0, WindowSeconds: window}, nil
}

// SetSourceClipBuffer stores one programme's switch, replacing what was there.
//
// A programme that does not exist is ErrSourceNotFound rather than the raw
// foreign-key failure, so the caller can tell "no such programme" from a disk
// that refused the write.
func (d *DB) SetSourceClipBuffer(sourceID int64, b SourceClipBuffer) error {
	_, err := d.sql.Exec(
		`INSERT INTO source_clip_buffers (source_id, enabled, window_seconds, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(source_id) DO UPDATE SET
		   enabled = excluded.enabled,
		   window_seconds = excluded.window_seconds,
		   updated_at = excluded.updated_at`,
		sourceID, boolToInt(b.Enabled), b.WindowSeconds, time.Now().Unix())
	if err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		return ErrSourceNotFound
	}
	return err
}
