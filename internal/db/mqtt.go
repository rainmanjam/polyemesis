package db

import (
	"database/sql"
	"errors"
	"time"

	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// The MQTT broker password lives in its own sealed row rather than in the
// settings blob, for the same reason an OAuth client secret does: the settings
// blob is marshalled straight to the settings page, so anything in it is handed
// to every browser that opens Settings.
//
// MQTTSettings.HasPassword is what the page gets instead -- enough to render
// "a password is set" and no more.

// PutMQTTPassword seals and stores the broker password. An empty password
// clears the row, which is how an operator moves to an anonymous broker without
// leaving a stale credential behind.
func (d *DB) PutMQTTPassword(box *secrets.Box, password string) error {
	if password == "" {
		_, err := d.sql.Exec(`DELETE FROM mqtt_creds WHERE id = 1`)
		return err
	}
	enc, err := box.Seal(password)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`INSERT INTO mqtt_creds (id, password_enc, updated_at)
		VALUES (1,?,?)
		ON CONFLICT(id) DO UPDATE SET password_enc=excluded.password_enc,
			updated_at=excluded.updated_at`,
		enc, time.Now().Unix())
	return err
}

// GetMQTTPassword returns the plaintext password, or "" when none is set.
//
// A missing row is not an error: an anonymous broker is a normal deployment,
// and treating "no password" as a failure would stop the publisher starting.
func (d *DB) GetMQTTPassword(box *secrets.Box) (string, error) {
	var enc []byte
	err := d.sql.QueryRow(`SELECT password_enc FROM mqtt_creds WHERE id = 1`).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return box.Open(enc)
}

// HasMQTTPassword reports whether one is stored, without decrypting it. Used to
// fill MQTTSettings.HasPassword on the way out to the settings page.
func (d *DB) HasMQTTPassword() (bool, error) {
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM mqtt_creds WHERE id = 1`).Scan(&n)
	return n > 0, err
}
