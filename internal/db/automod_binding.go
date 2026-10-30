package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// EVERY SEALED MODEL KEY IS BOUND TO THE ENDPOINT IT WAS ISSUED FOR.
//
// An operator's model key is the one credential here whose whole value is
// where it is sent: a key that reaches a host the operator did not choose has
// been disclosed, whatever happens next. Before this file, the key and the
// address lived in two places -- the key sealed in automod_creds, the address
// in the settings document -- and nothing tied them together. Editing the
// address in a settings save, or pointing the Test button somewhere new, sent
// the sealed key to the new address: a way to read a sealed secret back out by
// asking the server to deliver it.
//
// So the binding lives WITH THE KEY, in the same row (endpoint_fp), and it is
// what every use is checked against:
//
//   - The connectors refuse to send a key whose binding is not the endpoint
//     they are about to call (automod.ModelConfig.KeyBinding). A mismatch is a
//     hard error: the message fails open, the key goes nowhere.
//   - The Test button sends the stored key only to its bound endpoint, and a
//     key typed for another address is held as PENDING, bound to that
//     address, never as the checker's key.
//   - A settings write that moves a checker's endpoint re-binds or clears its
//     key IN THE SAME TRANSACTION as the write (putSettingsTx): the pending key
//     for exactly the new address is adopted, and anything else is deleted. A
//     failure rolls the whole write back.
//
// A binding of "" matches no endpoint, so a key bound to it is never sent.

// AutomodSlotSecondary names the second model checker in the per-slot calls
// below. Anything else means the primary, whose key has always lived in
// automod_creds and still does.
const AutomodSlotSecondary = "secondary"

// normSlot maps anything that is not the secondary to "primary", matching the
// tables' CHECKs.
func normSlot(slot string) string {
	if slot == AutomodSlotSecondary {
		return AutomodSlotSecondary
	}
	return "primary"
}

// automodCredsSQL is one slot's statements, as LITERALS: no query here is
// assembled from a caller's string.
type automodCredsSQL struct{ upsert, remove, get, has, bind string }

var automodCreds = map[string]automodCredsSQL{
	"primary": {
		upsert: `INSERT INTO automod_creds (id, key_enc, updated_at, endpoint_fp)
			VALUES (1,?,?,?)
			ON CONFLICT(id) DO UPDATE SET key_enc=excluded.key_enc,
				updated_at=excluded.updated_at, endpoint_fp=excluded.endpoint_fp`,
		remove: `DELETE FROM automod_creds WHERE id = 1`,
		get:    `SELECT key_enc, COALESCE(endpoint_fp, '') FROM automod_creds WHERE id = 1`,
		has:    `SELECT COUNT(*) FROM automod_creds WHERE id = 1`,
		bind:   `UPDATE automod_creds SET endpoint_fp = ? WHERE id = 1 AND endpoint_fp IS NULL`,
	},
	AutomodSlotSecondary: {
		upsert: `INSERT INTO automod_secondary_creds (id, key_enc, updated_at, endpoint_fp)
			VALUES (1,?,?,?)
			ON CONFLICT(id) DO UPDATE SET key_enc=excluded.key_enc,
				updated_at=excluded.updated_at, endpoint_fp=excluded.endpoint_fp`,
		remove: `DELETE FROM automod_secondary_creds WHERE id = 1`,
		get:    `SELECT key_enc, COALESCE(endpoint_fp, '') FROM automod_secondary_creds WHERE id = 1`,
		has:    `SELECT COUNT(*) FROM automod_secondary_creds WHERE id = 1`,
		bind:   `UPDATE automod_secondary_creds SET endpoint_fp = ? WHERE id = 1 AND endpoint_fp IS NULL`,
	},
}

// NormalizeEndpoint is the form a key's binding is stored and compared in:
// scheme and host lower-cased, the scheme's default port made explicit, the
// path without a trailing slash, the query kept exactly. Userinfo and a
// fragment make an endpoint unmatchable -- a key is never bound to an address
// that carries credentials of its own or that can be spelled two ways. ""
// matches nothing.
func NormalizeEndpoint(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || u.Fragment != "" || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[scheme]
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	return scheme + "://" + net.JoinHostPort(host, port) + path + "?" + u.RawQuery
}

// automodEndpoint is a slot's endpoint in a settings document.
func automodEndpoint(s Settings, slot string) string {
	if normSlot(slot) == AutomodSlotSecondary {
		return s.Automod.Secondary.Endpoint
	}
	return s.Automod.Model.Endpoint
}

// PutAutomodKeyFor seals a checker's key, bound to the endpoint stored for it
// at this moment; an empty key clears it. Either way the checker's Test pass
// and any pending key are forgotten: a pass was earned with the key it was
// tested with, and a new key -- or none -- has not earned one.
//
// Under settingsMu, so a settings write cannot move the endpoint between the
// read that decides the binding and the write that stores it.
func (d *DB) PutAutomodKeyFor(box *secrets.Box, slot, key string) error {
	d.settingsMu.Lock()
	defer d.settingsMu.Unlock()
	s, err := d.GetSettings()
	if err != nil {
		return err
	}
	slot = normSlot(slot)
	q := automodCreds[slot]
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if key == "" {
		if _, err := tx.Exec(q.remove); err != nil {
			return err
		}
	} else {
		enc, err := box.Seal(key)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(q.upsert, enc, time.Now().Unix(), NormalizeEndpoint(automodEndpoint(s, slot))); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM automod_pending_creds WHERE slot = ?`, slot); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM automod_checker_tests WHERE slot = ?`, slot); err != nil {
		return err
	}
	return tx.Commit()
}

// SealTestedAutomodKey keeps a key that has just passed the Test button
// against endpoint fp. When fp IS the checker's stored endpoint the key
// becomes the checker's key, bound to it. Otherwise it is held as PENDING,
// bound to fp -- never used, never sent -- until a settings write moves the
// checker to exactly that endpoint (putSettingsTx), when it is adopted.
// pending reports which happened.
func (d *DB) SealTestedAutomodKey(box *secrets.Box, slot, key, fp string) (pending bool, err error) {
	if fp == "" {
		return false, errors.New("a key cannot be bound to an endpoint that does not normalise")
	}
	d.settingsMu.Lock()
	defer d.settingsMu.Unlock()
	s, err := d.GetSettings()
	if err != nil {
		return false, err
	}
	slot = normSlot(slot)
	enc, err := box.Seal(key)
	if err != nil {
		return false, err
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if NormalizeEndpoint(automodEndpoint(s, slot)) == fp {
		if _, err := tx.Exec(automodCreds[slot].upsert, enc, time.Now().Unix(), fp); err != nil {
			return false, err
		}
		if _, err := tx.Exec(`DELETE FROM automod_pending_creds WHERE slot = ?`, slot); err != nil {
			return false, err
		}
	} else {
		pending = true
		if _, err := tx.Exec(`INSERT INTO automod_pending_creds (slot, key_enc, endpoint_fp, updated_at)
			VALUES (?,?,?,?)
			ON CONFLICT(slot) DO UPDATE SET key_enc=excluded.key_enc,
				endpoint_fp=excluded.endpoint_fp, updated_at=excluded.updated_at`,
			slot, enc, fp, time.Now().Unix()); err != nil {
			return false, err
		}
	}
	return pending, tx.Commit()
}

// GetAutomodKeyBound returns a checker's key and the endpoint it is bound to,
// or "" for both when none is set.
func (d *DB) GetAutomodKeyBound(box *secrets.Box, slot string) (key, binding string, err error) {
	var enc []byte
	err = d.sql.QueryRow(automodCreds[normSlot(slot)].get).Scan(&enc, &binding)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	key, err = box.Open(enc)
	return key, binding, err
}

// GetAutomodKeyFor returns a checker's plaintext key, or "" when none is set.
// Only for the debug bundle's scrub set: anything that SENDS a key must use
// GetAutomodKeyBound and honour the binding.
func (d *DB) GetAutomodKeyFor(box *secrets.Box, slot string) (string, error) {
	k, _, err := d.GetAutomodKeyBound(box, slot)
	return k, err
}

// GetAutomodPendingKey returns a checker's pending key, for the scrub set.
func (d *DB) GetAutomodPendingKey(box *secrets.Box, slot string) (string, error) {
	var enc []byte
	err := d.sql.QueryRow(`SELECT key_enc FROM automod_pending_creds WHERE slot = ?`, normSlot(slot)).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return box.Open(enc)
}

// HasAutomodKeyFor reports whether a checker has a key, without decrypting it.
func (d *DB) HasAutomodKeyFor(slot string) (bool, error) {
	var n int
	err := d.sql.QueryRow(automodCreds[normSlot(slot)].has).Scan(&n)
	return n > 0, err
}

// PutAutomodTestPass records that a checker passed the Test button with the
// connection fingerprint fp.
func (d *DB) PutAutomodTestPass(slot, fp string) error {
	_, err := d.sql.Exec(`INSERT INTO automod_checker_tests (slot, fingerprint, passed_at)
		VALUES (?,?,?)
		ON CONFLICT(slot) DO UPDATE SET fingerprint=excluded.fingerprint,
			passed_at=excluded.passed_at`,
		normSlot(slot), fp, time.Now().Unix())
	return err
}

// AutomodTestPass returns the fingerprint a checker last passed with, or ""
// when it has not passed since its key last changed.
func (d *DB) AutomodTestPass(slot string) (string, error) {
	var fp string
	err := d.sql.QueryRow(`SELECT fingerprint FROM automod_checker_tests WHERE slot = ?`,
		normSlot(slot)).Scan(&fp)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return fp, err
}

// rebindAutomodKeysTx keeps each checker's key bound to its endpoint across a
// settings write, inside that write's transaction. For a checker whose
// endpoint moved: a pending key bound to exactly the new endpoint is adopted;
// otherwise the key, any pending key and the Test pass are deleted. Any
// failure fails the whole write.
func rebindAutomodKeysTx(tx *sql.Tx, before, after Settings) error {
	for _, slot := range []string{"primary", AutomodSlotSecondary} {
		was, now := NormalizeEndpoint(automodEndpoint(before, slot)), NormalizeEndpoint(automodEndpoint(after, slot))
		if was == now {
			continue
		}
		q := automodCreds[slot]
		var pendingEnc []byte
		var pendingFP string
		err := tx.QueryRow(`SELECT key_enc, endpoint_fp FROM automod_pending_creds WHERE slot = ?`, slot).
			Scan(&pendingEnc, &pendingFP)
		switch {
		case err == nil && now != "" && pendingFP == now:
			if _, err := tx.Exec(q.upsert, pendingEnc, time.Now().Unix(), now); err != nil {
				return fmt.Errorf("adopt the %s checker's pending key: %w", slot, err)
			}
		case err == nil || errors.Is(err, sql.ErrNoRows):
			// The Test pass is left alone: it is bound to a connection
			// fingerprint, so one for the old endpoint no longer matches, and
			// one for the NEW endpoint was earned with no key, a typed key
			// (adopted above) or nothing -- never with the key cleared here,
			// which its binding kept from reaching the new address.
			if _, err := tx.Exec(q.remove); err != nil {
				return fmt.Errorf("clear the %s checker's key for its old endpoint: %w", slot, err)
			}
		default:
			return err
		}
		if _, err := tx.Exec(`DELETE FROM automod_pending_creds WHERE slot = ?`, slot); err != nil {
			return err
		}
	}
	return nil
}

// decodeStoredSettings is GetSettings' decode, for a blob read inside a
// transaction.
func decodeStoredSettings(raw string) (Settings, error) {
	s := mergeBaseSettings()
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return Settings{}, fmt.Errorf("decode settings: %w", err)
	}
	return s, nil
}

// MigrateAutomodKeyBinding adds the binding column to the two key tables and
// binds every key stored before it existed to the endpoint stored at upgrade
// time -- the endpoint that key has been sent to all along. One-time: only a
// NULL binding is written, and every key sealed afterwards carries one.
func (d *DB) MigrateAutomodKeyBinding() error {
	for _, table := range []string{"automod_creds", "automod_secondary_creds"} {
		ok, err := columnExists(d.sql, table, "endpoint_fp")
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		stmt := `ALTER TABLE automod_creds ADD COLUMN endpoint_fp TEXT`
		if table == "automod_secondary_creds" {
			stmt = `ALTER TABLE automod_secondary_creds ADD COLUMN endpoint_fp TEXT`
		}
		if _, err := d.sql.Exec(stmt); err != nil {
			return err
		}
	}
	// The raw row, NOT GetSettings: that writes the defaults when there is no
	// row, and a migration must not create data an install did not have. No
	// row means no endpoint, and a key with no endpoint binds to "" -- which
	// matches nothing, so it is never sent until it is entered again.
	s := mergeBaseSettings()
	var raw string
	switch err := d.sql.QueryRow(`SELECT json FROM settings WHERE id = 1`).Scan(&raw); {
	case err == nil:
		var derr error
		if s, derr = decodeStoredSettings(raw); derr != nil {
			return derr
		}
	case errors.Is(err, sql.ErrNoRows):
		s = Settings{}
	default:
		return err
	}
	for _, slot := range []string{"primary", AutomodSlotSecondary} {
		if _, err := d.sql.Exec(automodCreds[slot].bind, NormalizeEndpoint(automodEndpoint(s, slot))); err != nil {
			return err
		}
	}
	return nil
}
