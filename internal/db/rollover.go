package db

import (
	"database/sql"
	"errors"
	"time"
)

// Storage for planned rollover (#19): the per-destination settings and the session a rollover counts.
//
// The table and its idempotent creation are in wave3_rollover_migrate.go. Config and session are written
// by different paths and are kept apart here on purpose: an operator saving a margin must never reset the
// session start, and a rollover recording its result must never change the operator's quiet window.

// RolloverSettings is one destination's planned-rollover configuration and the session it is counting.
type RolloverSettings struct {
	DestinationID int64 `json:"destinationId"`
	// Enabled is the operator's intent. A disabled destination is never rolled over, whatever its limit.
	Enabled bool `json:"enabled"`
	// MarginMinutes is the safety margin: a rollover is planned this long before the platform's cap.
	MarginMinutes int `json:"marginMinutes"`
	// QuietStart and QuietEnd are an optional daily quiet window, HH:MM in QuietZone. Both empty means none.
	QuietStart string `json:"quietStart,omitempty"`
	QuietEnd   string `json:"quietEnd,omitempty"`
	QuietZone  string `json:"quietZone,omitempty"`
	// OBSScene, when set, makes a switch to this OBS scene a rollover trigger.
	OBSScene string `json:"obsScene,omitempty"`
	// SessionBroadcastID is the broadcast the session start belongs to. The start is only meaningful for that
	// broadcast, so it is compared before it is used: a rollover changes the broadcast and resets the session.
	SessionBroadcastID string `json:"sessionBroadcastId,omitempty"`
	// SessionStartedAt is the platform's own live start for SessionBroadcastID, persisted so a daemon restart
	// keeps counting from the real start, not from process uptime. Zero means unknown.
	SessionStartedAt time.Time `json:"sessionStartedAt,omitempty"`
	LastRolloverAt   time.Time `json:"lastRolloverAt,omitempty"`
	LastTrigger      string    `json:"lastTrigger,omitempty"`
	LastResult       string    `json:"lastResult,omitempty"`
	UpdatedAt        time.Time `json:"updatedAt,omitempty"`
}

// DefaultRolloverMarginMinutes is the margin a new configuration starts with. Thirty minutes leaves room for a
// rollover to be retried once or twice before the platform's cap lands.
const DefaultRolloverMarginMinutes = 30

const rolloverColumns = `destination_id, enabled, margin_minutes, quiet_start, quiet_end, quiet_zone, obs_scene,
	session_broadcast_id, session_started_at, last_rollover_at, last_trigger, last_result, updated_at`

// The reads are whole compile-time constants, for the reason the recurring reads record.
const (
	rolloverByIDQuery    = `SELECT ` + rolloverColumns + ` FROM destination_rollover WHERE destination_id = ?`
	rolloverEnabledQuery = `SELECT ` + rolloverColumns + ` FROM destination_rollover WHERE enabled = 1
		ORDER BY destination_id`
)

// GetRollover returns one destination's rollover settings. A destination with no row yet gets the defaults
// with Enabled false, so callers never have to branch on "never configured".
func (d *DB) GetRollover(destinationID int64) (RolloverSettings, error) {
	s, err := scanRollover(d.sql.QueryRow(rolloverByIDQuery, destinationID))
	if errors.Is(err, sql.ErrNoRows) {
		return RolloverSettings{DestinationID: destinationID, MarginMinutes: DefaultRolloverMarginMinutes}, nil
	}
	return s, err
}

// ListEnabledRollovers returns every destination whose operator has switched rollover on.
func (d *DB) ListEnabledRollovers() ([]RolloverSettings, error) {
	rows, err := d.sql.Query(rolloverEnabledQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RolloverSettings
	for rows.Next() {
		s, err := scanRollover(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SaveRolloverConfig writes the operator's settings. It writes only the config columns, so the session and
// the last result survive an operator changing a margin in the middle of a show.
func (d *DB) SaveRolloverConfig(s RolloverSettings, now time.Time) error {
	_, err := d.sql.Exec(`INSERT INTO destination_rollover
		(destination_id, enabled, margin_minutes, quiet_start, quiet_end, quiet_zone, obs_scene, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (destination_id) DO UPDATE SET
			enabled = excluded.enabled,
			margin_minutes = excluded.margin_minutes,
			quiet_start = excluded.quiet_start,
			quiet_end = excluded.quiet_end,
			quiet_zone = excluded.quiet_zone,
			obs_scene = excluded.obs_scene,
			updated_at = excluded.updated_at`,
		s.DestinationID, boolInt(s.Enabled), s.MarginMinutes, s.QuietStart, s.QuietEnd, s.QuietZone,
		s.OBSScene, now.UTC().Unix())
	return err
}

// SetRolloverSession records the platform's live start for the broadcast a rollover is counting. Calling it
// with a new broadcast id is how a rollover moves the session on; calling it again with the same id and an
// unchanged start is a harmless no-op.
func (d *DB) SetRolloverSession(destinationID int64, broadcastID string, startedAt, now time.Time) error {
	_, err := d.sql.Exec(`INSERT INTO destination_rollover (destination_id, session_broadcast_id, session_started_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (destination_id) DO UPDATE SET
			session_broadcast_id = excluded.session_broadcast_id,
			session_started_at = excluded.session_started_at,
			updated_at = excluded.updated_at`,
		destinationID, broadcastID, unixOrZero(startedAt), now.UTC().Unix())
	return err
}

// RecordRolloverResult notes what the last rollover attempt did, for the timeline and the countdown card. It
// touches no config and no session.
func (d *DB) RecordRolloverResult(destinationID int64, trigger, result string, at time.Time) error {
	_, err := d.sql.Exec(`INSERT INTO destination_rollover (destination_id, last_rollover_at, last_trigger, last_result, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (destination_id) DO UPDATE SET
			last_rollover_at = excluded.last_rollover_at,
			last_trigger = excluded.last_trigger,
			last_result = excluded.last_result,
			updated_at = excluded.updated_at`,
		destinationID, unixOrZero(at), trigger, result, at.UTC().Unix())
	return err
}

func scanRollover(s interface{ Scan(...any) error }) (RolloverSettings, error) {
	var (
		out                   RolloverSettings
		enabled               int
		sessionAt, rolloverAt int64
		updated               int64
	)
	err := s.Scan(&out.DestinationID, &enabled, &out.MarginMinutes, &out.QuietStart, &out.QuietEnd,
		&out.QuietZone, &out.OBSScene, &out.SessionBroadcastID, &sessionAt, &rolloverAt,
		&out.LastTrigger, &out.LastResult, &updated)
	if err != nil {
		return RolloverSettings{}, err
	}
	out.Enabled = enabled != 0
	out.SessionStartedAt = timeOrZero(sessionAt)
	out.LastRolloverAt = timeOrZero(rolloverAt)
	out.UpdatedAt = timeOrZero(updated)
	return out, nil
}
