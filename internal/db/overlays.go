package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// OverlayKeyPrefix marks an overlay key. Distinct from TokenPrefix on purpose:
// the API's bearer check only looks up pmk_ keys, so an overlay key presented
// anywhere else is simply not a credential there.
const OverlayKeyPrefix = "pov_"

// maxOverlayNameLength keeps the name a label.
const maxOverlayNameLength = 64

// ErrOverlayNotFound is returned for an id or key that names no overlay.
var ErrOverlayNotFound = fmt.Errorf("overlay: %w", ErrNotFound)

// ChatOverlay is one configured overlay.
//
// OBSKey is the plaintext key (see schema.sql). Only the session-only
// management routes serialise a ChatOverlay; the overlay's own config route
// builds its response from the other fields.
type ChatOverlay struct {
	ID           int64           `json:"id"`
	Name         string          `json:"name"`
	OBSKey       string          `json:"obsKey"`
	Platforms    []string        `json:"platforms"`
	Style        json.RawMessage `json:"style"`
	CreatedAt    time.Time       `json:"createdAt"`
	KeyRotatedAt time.Time       `json:"keyRotatedAt"`
}

func newOverlayKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return OverlayKeyPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

func cleanOverlayName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("overlay name is required")
	}
	if len([]rune(name)) > maxOverlayNameLength {
		return "", fmt.Errorf("overlay name must be at most %d characters", maxOverlayNameLength)
	}
	return name, nil
}

func encodePlatforms(p []string) string {
	if p == nil {
		p = []string{}
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// CreateChatOverlay stores a new overlay with a fresh key. The caller has
// validated platforms and style.
func (d *DB) CreateChatOverlay(name string, platforms []string, style json.RawMessage) (*ChatOverlay, error) {
	name, err := cleanOverlayName(name)
	if err != nil {
		return nil, err
	}
	k, err := newOverlayKey()
	if err != nil {
		return nil, err
	}
	if len(style) == 0 {
		style = json.RawMessage("{}")
	}
	now := time.Now().Unix()
	res, err := d.sql.Exec(
		`INSERT INTO chat_overlays (name, overlay_key, platforms, style, created_at, key_rotated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		name, k, encodePlatforms(platforms), string(style), now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return d.ChatOverlay(id)
}

const overlayColumns = `id, name, overlay_key, platforms, style, created_at, key_rotated_at`

func scanOverlay(row interface{ Scan(...any) error }) (*ChatOverlay, error) {
	var (
		o              ChatOverlay
		plat, style    string
		created, rotAt int64
	)
	if err := row.Scan(&o.ID, &o.Name, &o.OBSKey, &plat, &style, &created, &rotAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOverlayNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(plat), &o.Platforms); err != nil || o.Platforms == nil {
		o.Platforms = []string{}
	}
	o.Style = json.RawMessage(style)
	if !json.Valid(o.Style) {
		o.Style = json.RawMessage("{}")
	}
	o.CreatedAt = time.Unix(created, 0)
	if rotAt > 0 {
		o.KeyRotatedAt = time.Unix(rotAt, 0)
	}
	return &o, nil
}

// ChatOverlay returns one overlay.
func (d *DB) ChatOverlay(id int64) (*ChatOverlay, error) {
	return scanOverlay(d.sql.QueryRow(`SELECT `+overlayColumns+` FROM chat_overlays WHERE id = ?`, id))
}

// ChatOverlayByKey resolves a key. Anything not overlay-shaped never matches,
// so a revoked overlay (key ”) cannot be reached by presenting ”.
func (d *DB) ChatOverlayByKey(key string) (*ChatOverlay, error) {
	if !strings.HasPrefix(key, OverlayKeyPrefix) || len(key) <= len(OverlayKeyPrefix) {
		return nil, ErrOverlayNotFound
	}
	return scanOverlay(d.sql.QueryRow(`SELECT `+overlayColumns+` FROM chat_overlays WHERE overlay_key = ?`, key))
}

// ListChatOverlays returns every overlay, oldest first.
func (d *DB) ListChatOverlays() ([]ChatOverlay, error) {
	rows, err := d.sql.Query(`SELECT ` + overlayColumns + ` FROM chat_overlays ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChatOverlay{}
	for rows.Next() {
		o, err := scanOverlay(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

func overlayAffected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrOverlayNotFound
	}
	return nil
}

// UpdateChatOverlay replaces an overlay's name, platforms and style. The key is
// untouched: restyling an overlay must not break the URL already in OBS.
func (d *DB) UpdateChatOverlay(id int64, name string, platforms []string, style json.RawMessage) (*ChatOverlay, error) {
	name, err := cleanOverlayName(name)
	if err != nil {
		return nil, err
	}
	if len(style) == 0 {
		style = json.RawMessage("{}")
	}
	if err := overlayAffected(d.sql.Exec(
		`UPDATE chat_overlays SET name = ?, platforms = ?, style = ? WHERE id = ?`,
		name, encodePlatforms(platforms), string(style), id)); err != nil {
		return nil, err
	}
	return d.ChatOverlay(id)
}

// RotateChatOverlayKey replaces the key. The old one stops matching at once.
func (d *DB) RotateChatOverlayKey(id int64) (*ChatOverlay, error) {
	k, err := newOverlayKey()
	if err != nil {
		return nil, err
	}
	if err := overlayAffected(d.sql.Exec(
		`UPDATE chat_overlays SET overlay_key = ?, key_rotated_at = ? WHERE id = ?`,
		k, time.Now().Unix(), id)); err != nil {
		return nil, err
	}
	return d.ChatOverlay(id)
}

// RevokeChatOverlayKey removes the key and keeps the overlay and its style. A
// new key comes from RotateChatOverlayKey.
func (d *DB) RevokeChatOverlayKey(id int64) (*ChatOverlay, error) {
	if err := overlayAffected(d.sql.Exec(
		`UPDATE chat_overlays SET overlay_key = '', key_rotated_at = ? WHERE id = ?`,
		time.Now().Unix(), id)); err != nil {
		return nil, err
	}
	return d.ChatOverlay(id)
}

// DeleteChatOverlay removes an overlay, and its key with it.
func (d *DB) DeleteChatOverlay(id int64) error {
	return overlayAffected(d.sql.Exec(`DELETE FROM chat_overlays WHERE id = ?`, id))
}
