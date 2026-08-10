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

// Token scopes. What a token may do, as opposed to who it is.
//
// Two values rather than a permission per feature, because the enforcement has
// to be a rule a reader can restate: ScopeRead is GET and HEAD, ScopeAdmin is
// everything the signed-in operator can do. A model with a permission per route
// needs every one of the API's routes classified, and every route added
// afterwards classified correctly by whoever adds it -- which is the same
// remember-to-do-it failure #140 was.
const (
	// ScopeRead reaches reads only. A monitoring script, a dashboard, a status
	// poller: the things people actually mint tokens for.
	ScopeRead = "read"
	// ScopeAdmin is everything, which is what every token was before scopes
	// existed.
	ScopeAdmin = "admin"
)

// ValidScope reports whether s is a scope this build knows.
//
// Rejecting an unknown scope at the door matters more than it looks: the
// enforcement middleware treats anything that is not ScopeAdmin as read-only,
// so a typo stored in the column would silently mint a weaker token than the
// operator asked for rather than a stronger one. That is the safe direction to
// fail, and it is still worth refusing outright so nobody has to discover it.
func ValidScope(s string) bool { return s == ScopeRead || s == ScopeAdmin }

// APIToken is a long-lived credential for automation. It deliberately has no
// field for the secret: after creation the plaintext exists nowhere.
type APIToken struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Prefix     string    `json:"prefix"`
	Scope      string    `json:"scope"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
}

// MigrateAPITokenScope adds api_tokens.scope to a database created before
// tokens carried one.
//
// It defaults to ScopeAdmin, which is what every existing token effectively
// already was: before this column there was no restriction at all, so a token
// in somebody's CI runner can do everything today. Backfilling 'read' would
// change what a credential does without the operator touching it, and the
// failure would land as a 403 in an unattended script rather than as a message
// anyone reads. Grandfathering is the honest option; narrowing an existing
// token is then a decision the operator makes by revoking and re-minting it.
func (d *DB) MigrateAPITokenScope() error {
	has, err := columnExists(d.sql, "api_tokens", "scope")
	if err != nil {
		return fmt.Errorf("inspect api_tokens columns: %w", err)
	}
	if has {
		return nil
	}
	if _, err := d.sql.Exec(
		`ALTER TABLE api_tokens ADD COLUMN scope TEXT NOT NULL DEFAULT 'admin'`); err != nil {
		return fmt.Errorf("add api_tokens.scope: %w", err)
	}
	return nil
}

// CreateAPIToken mints a token and returns it alongside the plaintext. The
// plaintext is the caller's only chance to see it.
//
// An empty scope means ScopeRead rather than ScopeAdmin. Every caller that
// omits it gets the weaker credential, which is the direction a forgotten
// argument must fail in; the API layer asks the operator explicitly.
func (d *DB) CreateAPIToken(name, scope string) (*APIToken, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", errors.New("token name is required")
	}
	if len(name) > maxTokenNameLength {
		return nil, "", fmt.Errorf("token name must be at most %d characters", maxTokenNameLength)
	}
	if scope = strings.TrimSpace(scope); scope == "" {
		scope = ScopeRead
	}
	if !ValidScope(scope) {
		return nil, "", fmt.Errorf("unknown token scope %q; use %q or %q", scope, ScopeRead, ScopeAdmin)
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, "", err
	}
	plaintext := TokenPrefix + base64.RawURLEncoding.EncodeToString(b)

	now := time.Now().Unix()
	res, err := d.sql.Exec(
		`INSERT INTO api_tokens (name, token_hash, prefix, created_at, last_used_at, scope) VALUES (?, ?, ?, ?, 0, ?)`,
		name, hashToken(plaintext), plaintext[:tokenDisplayLength], now, scope)
	if err != nil {
		return nil, "", err
	}
	id, _ := res.LastInsertId()
	return &APIToken{
		ID:        id,
		Name:      name,
		Prefix:    plaintext[:tokenDisplayLength],
		Scope:     scope,
		CreatedAt: time.Unix(now, 0),
	}, plaintext, nil
}

// ListAPITokens returns every live token, newest first.
func (d *DB) ListAPITokens() ([]APIToken, error) {
	rows, err := d.sql.Query(
		`SELECT id, name, prefix, created_at, last_used_at, scope FROM api_tokens ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		var created, used int64
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &created, &used, &t.Scope); err != nil {
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
		`SELECT id, name, prefix, created_at, last_used_at, scope FROM api_tokens WHERE token_hash = ?`,
		hashToken(plaintext)).
		Scan(&t.ID, &t.Name, &t.Prefix, &created, &used, &t.Scope)
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
