package db

import (
	"database/sql"
	"errors"
	"time"

	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// The automod model's API key lives in its own sealed row rather than in the
// settings blob, for exactly the reason the MQTT broker password does: the
// settings blob is marshalled straight to the settings page, so anything in it
// is handed to every browser that opens Settings.
//
// AutomodModel.HasAPIKey is what the page gets instead -- enough to render
// "a key is set" and no more.

// PutAutomodKey seals and stores the model API key. An empty key clears the
// row, which is how an operator turns the model checker off without leaving a
// credential behind.
func (d *DB) PutAutomodKey(box *secrets.Box, key string) error {
	if key == "" {
		_, err := d.sql.Exec(`DELETE FROM automod_creds WHERE id = 1`)
		return err
	}
	enc, err := box.Seal(key)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`INSERT INTO automod_creds (id, key_enc, updated_at)
		VALUES (1,?,?)
		ON CONFLICT(id) DO UPDATE SET key_enc=excluded.key_enc,
			updated_at=excluded.updated_at`,
		enc, time.Now().Unix())
	return err
}

// GetAutomodKey returns the plaintext key, or "" when none is set.
//
// A missing row is not an error. The model checker is off by default and a
// locally hosted endpoint may need no key at all, so "no key" is a normal
// state rather than a failure.
func (d *DB) GetAutomodKey(box *secrets.Box) (string, error) {
	var enc []byte
	err := d.sql.QueryRow(`SELECT key_enc FROM automod_creds WHERE id = 1`).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return box.Open(enc)
}

// HasAutomodKey reports whether one is stored, without decrypting it. Used to
// fill AutomodModel.HasAPIKey on the way out to the settings page.
func (d *DB) HasAutomodKey() (bool, error) {
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM automod_creds WHERE id = 1`).Scan(&n)
	return n > 0, err
}

// AutomodSlotSecondary names the second model checker in the per-slot calls
// below. Anything else means the primary, whose key has always lived in
// automod_creds and still does.
const AutomodSlotSecondary = "secondary"

// PutAutomodKeyFor seals and stores a checker's key; an empty key clears it.
// Either way the checker's Test pass is forgotten: a pass was earned with the
// key it was tested with, and a new key -- or none -- has not earned one.
func (d *DB) PutAutomodKeyFor(box *secrets.Box, slot, key string) error {
	if slot != AutomodSlotSecondary {
		if err := d.PutAutomodKey(box, key); err != nil {
			return err
		}
		return d.ClearAutomodTestPass("primary")
	}
	if key == "" {
		if _, err := d.sql.Exec(`DELETE FROM automod_secondary_creds WHERE id = 1`); err != nil {
			return err
		}
		return d.ClearAutomodTestPass(AutomodSlotSecondary)
	}
	enc, err := box.Seal(key)
	if err != nil {
		return err
	}
	if _, err := d.sql.Exec(`INSERT INTO automod_secondary_creds (id, key_enc, updated_at)
		VALUES (1,?,?)
		ON CONFLICT(id) DO UPDATE SET key_enc=excluded.key_enc,
			updated_at=excluded.updated_at`,
		enc, time.Now().Unix()); err != nil {
		return err
	}
	return d.ClearAutomodTestPass(AutomodSlotSecondary)
}

// GetAutomodKeyFor returns a checker's plaintext key, or "" when none is set.
func (d *DB) GetAutomodKeyFor(box *secrets.Box, slot string) (string, error) {
	if slot != AutomodSlotSecondary {
		return d.GetAutomodKey(box)
	}
	var enc []byte
	err := d.sql.QueryRow(`SELECT key_enc FROM automod_secondary_creds WHERE id = 1`).Scan(&enc)
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
	if slot != AutomodSlotSecondary {
		return d.HasAutomodKey()
	}
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM automod_secondary_creds WHERE id = 1`).Scan(&n)
	return n > 0, err
}

// normSlot maps anything that is not the secondary to "primary", matching the
// table's CHECK.
func normSlot(slot string) string {
	if slot == AutomodSlotSecondary {
		return AutomodSlotSecondary
	}
	return "primary"
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

// ClearAutomodTestPass forgets a checker's pass.
func (d *DB) ClearAutomodTestPass(slot string) error {
	_, err := d.sql.Exec(`DELETE FROM automod_checker_tests WHERE slot = ?`, normSlot(slot))
	return err
}
