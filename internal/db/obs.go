package db

import (
	"database/sql"
	"errors"
	"time"

	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// The OBS bridge's storage (#16): its configuration as one JSON document, and
// the obs-websocket password sealed in a row of its own -- the MQTT broker
// password's arrangement, for the MQTT broker password's reason. The
// configuration is served to the settings page; the password never is.
//
// This package stores the configuration as bytes and does not interpret them.
// internal/obsbridge owns the type and its validation, and keeping the db
// package from importing it keeps the dependency pointing one way.

// GetOBSConfig returns the stored configuration document, or nil when none has
// been saved -- which is a bridge that is switched off, not an error.
func (d *DB) GetOBSConfig() ([]byte, error) {
	var raw string
	err := d.sql.QueryRow(`SELECT config FROM obs_bridge WHERE id = 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(raw), nil
}

// PutOBSConfig replaces the stored configuration document.
func (d *DB) PutOBSConfig(doc []byte) error {
	_, err := d.sql.Exec(`INSERT INTO obs_bridge (id, config, updated_at) VALUES (1,?,?)
		ON CONFLICT(id) DO UPDATE SET config=excluded.config, updated_at=excluded.updated_at`,
		string(doc), time.Now().Unix())
	return err
}

// PutOBSPassword seals and stores the obs-websocket password. An empty one
// clears the row: obs-websocket with authentication switched off is a normal
// LAN setup, and a stale credential left behind is not.
func (d *DB) PutOBSPassword(box *secrets.Box, password string) error {
	if password == "" {
		_, err := d.sql.Exec(`DELETE FROM obs_creds WHERE id = 1`)
		return err
	}
	enc, err := box.Seal(password)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`INSERT INTO obs_creds (id, password_enc, updated_at) VALUES (1,?,?)
		ON CONFLICT(id) DO UPDATE SET password_enc=excluded.password_enc, updated_at=excluded.updated_at`,
		enc, time.Now().Unix())
	return err
}

// GetOBSPassword returns the plaintext password, or "" when none is set.
func (d *DB) GetOBSPassword(box *secrets.Box) (string, error) {
	var enc []byte
	err := d.sql.QueryRow(`SELECT password_enc FROM obs_creds WHERE id = 1`).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return box.Open(enc)
}

// HasOBSPassword reports whether one is stored, without decrypting it.
func (d *DB) HasOBSPassword() (bool, error) {
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM obs_creds WHERE id = 1`).Scan(&n)
	return n > 0, err
}
