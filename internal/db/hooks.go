package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// Hooks are stored as hooks.Hook rather than a row struct of their own, the
// same way alert rules are stored as alerts.Rule: the domain package owns the
// validation and the defaults, and a second copy here would be one more place
// for them to disagree.
//
// The one asymmetry with alert_rules is the secret. A webhook URL is stored in
// the clear because it must be, being the thing the request is sent to and
// something an operator occasionally has to recover. An HMAC key is never
// displayed and never recovered, so it is sealed -- a database file on a backup
// drive should not hand anybody the ability to forge deliveries.

const hookColumns = `id, name, enabled, url, secret, triggers,
	timeout_seconds, max_attempts, allow_private_target, created_at, updated_at`

const (
	hooksQuery        = `SELECT ` + hookColumns + ` FROM hooks ORDER BY id`
	hooksEnabledQuery = `SELECT ` + hookColumns + ` FROM hooks WHERE enabled = 1 ORDER BY id`
	hookByIDQuery     = `SELECT ` + hookColumns + ` FROM hooks WHERE id = ?`
)

func scanHook(box *secrets.Box, s interface{ Scan(...any) error }) (*hooks.Hook, error) {
	var (
		h                hooks.Hook
		enabled          int
		allowPrivate     int
		sealed           []byte
		triggersJSON     string
		created, updated int64
	)
	if err := s.Scan(&h.ID, &h.Name, &enabled, &h.URL, &sealed, &triggersJSON,
		&h.TimeoutSeconds, &h.MaxAttempts, &allowPrivate, &created, &updated); err != nil {
		return nil, err
	}
	h.Enabled = enabled != 0
	// Dropped on the way through storage, this reads back false and
	// safeDialContext refuses a hook the operator deliberately allowed -- so
	// the hook is accepted at create time and then silently never fires.
	h.AllowPrivateTarget = allowPrivate != 0
	// A secret that will not open leaves the hook UNSIGNED rather than
	// unreadable. The alternative -- failing the whole read -- would take every
	// other hook down with it, and an unsigned delivery that arrives is more
	// useful to an operator than a signed one that never does. The API reports
	// hasSecret:false, which is how they find out.
	if len(sealed) > 0 {
		if plain, err := box.Open(sealed); err == nil {
			h.Secret = plain
		}
	}
	if triggersJSON != "" && triggersJSON != "[]" {
		var list []hooks.Trigger
		// A subscription list that will not parse subscribes to everything, for
		// the reason spelled out in alerts.go:33 -- the alternative is a hook
		// that has silently stopped firing.
		if err := json.Unmarshal([]byte(triggersJSON), &list); err == nil {
			h.Triggers = list
		}
	}
	h.CreatedAt = time.Unix(created, 0)
	h.UpdatedAt = time.Unix(updated, 0)
	out := h.Normalized()
	return &out, nil
}

func (d *DB) queryHooks(box *secrets.Box, q string) ([]hooks.Hook, error) {
	rows, err := d.sql.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []hooks.Hook{}
	for rows.Next() {
		h, err := scanHook(box, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *h)
	}
	return out, rows.Err()
}

// ListHooks returns every hook, oldest first.
func (d *DB) ListHooks(box *secrets.Box) ([]hooks.Hook, error) {
	return d.queryHooks(box, hooksQuery)
}

// EnabledHooks returns the enabled hooks. It is what satisfies hooks.Source, so
// the dispatcher never sees one that is switched off.
func (d *DB) EnabledHooks(box *secrets.Box) ([]hooks.Hook, error) {
	return d.queryHooks(box, hooksEnabledQuery)
}

// GetHook loads one hook.
func (d *DB) GetHook(box *secrets.Box, id int64) (*hooks.Hook, error) {
	h, err := scanHook(box, d.sql.QueryRow(hookByIDQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return h, err
}

// CreateHook stores a new hook and returns the plaintext signing secret.
//
// The plaintext is returned exactly once, here, and never again -- the same
// contract as an API token (see CreateAPIToken and its handler at
// api/token_handlers.go:54). An operator pasting the key into their receiver
// needs it at this moment and at no other.
func (d *DB) CreateHook(box *secrets.Box, h *hooks.Hook) (*hooks.Hook, string, error) {
	norm := h.Normalized()
	if err := norm.Validate(); err != nil {
		return nil, "", err
	}
	// Generated when the operator did not supply one, because an unsigned
	// webhook is one that anybody who learns the URL can forge, and a URL leaks
	// through proxy logs, browser history and screenshots.
	if norm.Secret == "" {
		s, err := hooks.NewSecret()
		if err != nil {
			return nil, "", err
		}
		norm.Secret = s
	}
	sealed, err := box.Seal(norm.Secret)
	if err != nil {
		return nil, "", err
	}
	triggers, err := marshalTriggers(norm.Triggers)
	if err != nil {
		return nil, "", err
	}
	now := time.Now().Unix()
	res, err := d.sql.Exec(`INSERT INTO hooks
		(name, enabled, url, secret, triggers, timeout_seconds, max_attempts, allow_private_target, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		norm.Name, boolToInt(norm.Enabled), norm.URL, sealed, triggers,
		norm.TimeoutSeconds, norm.MaxAttempts, boolToInt(norm.AllowPrivateTarget), now, now)
	if err != nil {
		return nil, "", err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, "", err
	}
	out, err := d.GetHook(box, id)
	return out, norm.Secret, err
}

// UpdateHook replaces a hook in place. An empty Secret means "unchanged": the
// UI never renders it, so every edit form submits an empty one, and storing
// that would silently unsign every future delivery.
func (d *DB) UpdateHook(box *secrets.Box, h *hooks.Hook) (*hooks.Hook, error) {
	norm := h.Normalized()
	if err := norm.Validate(); err != nil {
		return nil, err
	}
	// KEPT IN SQL, NOT READ BACK AND RE-SEALED. An empty Secret means "leave the
	// stored one alone", and the previous implementation honoured that by asking
	// GetHook for the current value -- but scanHook DELIBERATELY SWALLOWS a
	// decrypt failure and leaves Secret empty, so that one bad row cannot take
	// every other hook down with it.
	//
	// Those two designs compose into data destruction. When the ciphertext will
	// not open -- a restore against the wrong key, a half-finished rotation --
	// GetHook returns "", the update reads that as the current secret, and seals
	// it. A row that was unreadable but RECOVERABLE, and that the API was
	// honestly reporting as hasSecret:false, becomes a valid seal of the empty
	// string. Renaming a hook was enough to trigger it.
	//
	// Not re-sealing at all removes the failure: the column keeps whatever bytes
	// it already held, so restoring the right box still opens it.
	keepSecret := norm.Secret == ""
	var sealed []byte
	if !keepSecret {
		var err error
		if sealed, err = box.Seal(norm.Secret); err != nil {
			return nil, err
		}
	}
	triggers, err := marshalTriggers(norm.Triggers)
	if err != nil {
		return nil, err
	}
	res, err := d.sql.Exec(`UPDATE hooks SET
		name=?, enabled=?, url=?, secret=CASE WHEN ? THEN secret ELSE ? END, triggers=?,
		timeout_seconds=?, max_attempts=?, allow_private_target=?, updated_at=? WHERE id=?`,
		norm.Name, boolToInt(norm.Enabled), norm.URL, boolToInt(keepSecret), sealed, triggers,
		norm.TimeoutSeconds, norm.MaxAttempts, boolToInt(norm.AllowPrivateTarget),
		time.Now().Unix(), norm.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return d.GetHook(box, norm.ID)
}

// DeleteHook removes a hook.
func (d *DB) DeleteHook(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM hooks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// marshalTriggers encodes a subscription list. An empty list is stored as "[]",
// meaning "every trigger", so the column is never NULL.
func marshalTriggers(list []hooks.Trigger) (string, error) {
	if len(list) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(list)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// MigrateHookAllowPrivateTarget adds hooks.allow_private_target to a database
// created before the SSRF guard existed.
//
// WHY THIS EXISTS AT ALL, since schema.sql already declares the column: that
// file is CREATE TABLE IF NOT EXISTS, so on an install whose hooks table is
// already there it does nothing. The column arrived with the guard (poka-yoke
// audit #4) and every hook read now names it, so without this an upgraded
// install answers "no such column: allow_private_target" on the first hook
// query and keeps doing it -- the hooks page empty, deliveries stopped, and
// nothing in the schema to suggest why. Measured against a database built from
// the previous schema before this was written.
//
// DEFAULT 0, so an upgraded install keeps refusing private targets. The safe
// direction: an operator who wants one opts in deliberately, exactly as a new
// install would.
func (d *DB) MigrateHookAllowPrivateTarget() error {
	// Checked before any transaction opens, for the reason
	// MigrateDestinationExpertArgs records: db.go sets SetMaxOpenConns(1), so a
	// read issued while a transaction holds the one connection waits for a
	// connection that transaction will not release, and startup hangs for ever.
	has, err := columnExists(d.sql, "hooks", "allow_private_target")
	if err != nil {
		return fmt.Errorf("inspect hooks columns: %w", err)
	}
	if has {
		return nil
	}
	if _, err := d.sql.Exec(
		`ALTER TABLE hooks ADD COLUMN allow_private_target INTEGER NOT NULL DEFAULT 0`,
	); err != nil {
		return fmt.Errorf("add hooks.allow_private_target: %w", err)
	}
	return nil
}
