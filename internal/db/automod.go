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
