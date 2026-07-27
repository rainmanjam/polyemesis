package db

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TokenPrefix marks a polyemesis API token in logs, config files and secret
// scanners, and lets the auth middleware skip a database round trip for
// anything that plainly is not one.
const TokenPrefix = "pmk_"

// maxTokenNameLength keeps the label a label; the name is only ever shown
// back to the operator.
const maxTokenNameLength = 64

// tokenDisplayLength is how much of the plaintext is kept in the clear. Eight
// characters after the prefix identify a token in a list without being enough
// to help anyone guess the remaining 200-odd bits.
const tokenDisplayLength = len(TokenPrefix) + 8

// APIToken is a long-lived credential for automation. It deliberately has no
// field for the secret: after creation the plaintext exists nowhere.
type APIToken struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Prefix     string    `json:"prefix"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
}

// CreateAPIToken mints a token and returns it alongside the plaintext. The
// plaintext is the caller's only chance to see it.
func (d *DB) CreateAPIToken(name string) (*APIToken, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", errors.New("token name is required")
	}
	if len(name) > maxTokenNameLength {
		return nil, "", fmt.Errorf("token name must be at most %d characters", maxTokenNameLength)
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, "", err
	}
	plaintext := TokenPrefix + base64.RawURLEncoding.EncodeToString(b)

	now := time.Now().Unix()
	res, err := d.sql.Exec(
		`INSERT INTO api_tokens (name, token_hash, prefix, created_at, last_used_at) VALUES (?, ?, ?, ?, 0)`,
		name, hashToken(plaintext), plaintext[:tokenDisplayLength], now)
	if err != nil {
		return nil, "", err
	}
	id, _ := res.LastInsertId()
	return &APIToken{
		ID:        id,
		Name:      name,
		Prefix:    plaintext[:tokenDisplayLength],
		CreatedAt: time.Unix(now, 0),
	}, plaintext, nil
}

// ListAPITokens returns every live token, newest first.
func (d *DB) ListAPITokens() ([]APIToken, error) {
	rows, err := d.sql.Query(
		`SELECT id, name, prefix, created_at, last_used_at FROM api_tokens ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		var created, used int64
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &created, &used); err != nil {
			return nil, err
		}
		t.CreatedAt = time.Unix(created, 0)
		if used > 0 {
			t.LastUsedAt = time.Unix(used, 0)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteAPIToken revokes a token.
func (d *DB) DeleteAPIToken(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM api_tokens WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// LookupAPIToken resolves a plaintext token, returning ErrNotFound if it does
// not match a live token.
func (d *DB) LookupAPIToken(plaintext string) (*APIToken, error) {
	if !strings.HasPrefix(plaintext, TokenPrefix) {
		return nil, ErrNotFound
	}

	var t APIToken
	var created, used int64
	err := d.sql.QueryRow(
		`SELECT id, name, prefix, created_at, last_used_at FROM api_tokens WHERE token_hash = ?`,
		hashToken(plaintext)).
		Scan(&t.ID, &t.Name, &t.Prefix, &created, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.CreatedAt = time.Unix(created, 0)
	if used > 0 {
		t.LastUsedAt = time.Unix(used, 0)
	}

	// Coarse: an API client can poll several times a second, and "last used"
	// to the minute is all the operator is reading it for. A write per
	// request would serialise behind the single SQLite connection.
	now := time.Now().Unix()
	if now-used >= 60 {
		if _, err := d.sql.Exec(`UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, now, t.ID); err == nil {
			t.LastUsedAt = time.Unix(now, 0)
		}
	}
	return &t, nil
}

func hashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}
