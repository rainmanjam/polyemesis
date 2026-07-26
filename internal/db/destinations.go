package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// DestKind is the transport a destination publishes over.
type DestKind string

const (
	DestRTMP DestKind = "rtmp" // rtmp:// or rtmps://
	DestSRT  DestKind = "srt"  // srt://
	DestFile DestKind = "file" // local recording of this specific mix
)

// Platform identifies an integration, for branding and for stream-key fetch.
type Platform string

const (
	PlatformCustom  Platform = "custom"
	PlatformYouTube Platform = "youtube"
	PlatformTwitch  Platform = "twitch"
	PlatformKick    Platform = "kick"
)

// ErrNotFound is returned by the typed getters.
var ErrNotFound = errors.New("not found")

// Destination is one output: where it goes, and which audio it gets.
type Destination struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Kind     DestKind `json:"kind"`
	Platform Platform `json:"platform"`
	// AccountID links to a connected OAuth account. When set, URL/StreamKey
	// are refreshed from the platform API rather than typed by the user.
	AccountID *int64 `json:"accountId,omitempty"`
	// URL is the ingest endpoint (rtmp/srt) or the output path template (file).
	URL string `json:"url"`
	// StreamKey is appended to URL for RTMP. Kept separate so the UI can mask
	// it and so a key rotation does not require retyping the endpoint.
	StreamKey string `json:"streamKey"`
	// Enabled is user intent, not live state: "this should be running".
	Enabled      bool            `json:"enabled"`
	AudioBitrate int             `json:"audioBitrate"` // kbps
	Profile      routing.Profile `json:"profile"`
	Position     int             `json:"position"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

// Target returns the full URL FFmpeg should publish to, i.e. URL with the
// stream key joined on for RTMP.
func (d Destination) Target() string {
	if d.Kind != DestRTMP || d.StreamKey == "" {
		return d.URL
	}
	return strings.TrimRight(d.URL, "/") + "/" + d.StreamKey
}

// Validate checks a destination is startable.
func (d Destination) Validate() error {
	var probs []string
	add := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }

	if strings.TrimSpace(d.Name) == "" {
		add("name is required")
	}
	switch d.Kind {
	case DestRTMP, DestSRT, DestFile:
	default:
		add("unknown destination kind %q", d.Kind)
	}
	switch d.Platform {
	case PlatformCustom, PlatformYouTube, PlatformTwitch, PlatformKick, "":
	default:
		add("unknown platform %q", d.Platform)
	}
	if d.AudioBitrate < 32 || d.AudioBitrate > 512 {
		add("audio bitrate %d kbps out of range (32-512)", d.AudioBitrate)
	}

	target := strings.TrimSpace(d.URL)
	switch d.Kind {
	case DestRTMP:
		if target == "" {
			add("an RTMP URL is required")
		} else if u, err := url.Parse(target); err != nil {
			add("malformed RTMP URL: %v", err)
		} else if u.Scheme != "rtmp" && u.Scheme != "rtmps" {
			add("RTMP destination URL must start with rtmp:// or rtmps:// (got %q)", u.Scheme)
		}
	case DestSRT:
		if target == "" {
			add("an SRT URL is required")
		} else if u, err := url.Parse(target); err != nil {
			add("malformed SRT URL: %v", err)
		} else if u.Scheme != "srt" {
			add("SRT destination URL must start with srt:// (got %q)", u.Scheme)
		}
	case DestFile:
		if target == "" {
			add("a filename is required")
		}
		// Keep file destinations inside the data directory. Without this a
		// destination is an arbitrary-file-write primitive for anyone who
		// reaches the API.
		if strings.Contains(target, "..") || strings.HasPrefix(target, "/") {
			add("file destination must be a relative name inside the recordings directory")
		}
	}

	if err := d.Profile.Validate(); err != nil {
		add("%v", err)
	}

	if len(probs) > 0 {
		return fmt.Errorf("invalid destination: %s", strings.Join(probs, "; "))
	}
	return nil
}

func scanDestination(s interface{ Scan(...any) error }) (*Destination, error) {
	var (
		d          Destination
		acct       sql.NullInt64
		profileRaw string
		created    int64
		updated    int64
	)
	err := s.Scan(&d.ID, &d.Name, &d.Kind, &d.Platform, &acct, &d.URL, &d.StreamKey,
		&d.Enabled, &d.AudioBitrate, &profileRaw, &d.Position, &created, &updated)
	if err != nil {
		return nil, err
	}
	if acct.Valid {
		v := acct.Int64
		d.AccountID = &v
	}
	if err := json.Unmarshal([]byte(profileRaw), &d.Profile); err != nil {
		return nil, fmt.Errorf("destination %d: decode routing profile: %w", d.ID, err)
	}
	d.CreatedAt = time.Unix(created, 0)
	d.UpdatedAt = time.Unix(updated, 0)
	return &d, nil
}

const destColumns = `id, name, kind, platform, account_id, url, stream_key,
	enabled, audio_bitrate, profile, position, created_at, updated_at`

// ListDestinations returns every destination in display order.
func (d *DB) ListDestinations() ([]*Destination, error) {
	rows, err := d.sql.Query(`SELECT ` + destColumns + ` FROM destinations ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*Destination{}
	for rows.Next() {
		dst, err := scanDestination(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dst)
	}
	return out, rows.Err()
}

// GetDestination loads one destination.
func (d *DB) GetDestination(id int64) (*Destination, error) {
	row := d.sql.QueryRow(`SELECT `+destColumns+` FROM destinations WHERE id = ?`, id)
	dst, err := scanDestination(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return dst, err
}

// CreateDestination inserts a destination, defaulting anything unset.
func (d *DB) CreateDestination(dst *Destination) (*Destination, error) {
	if dst.AudioBitrate == 0 {
		dst.AudioBitrate = 160
	}
	if dst.Platform == "" {
		dst.Platform = PlatformCustom
	}
	// A create request normally carries no routing profile — the user sets one
	// afterwards in the routing editor. ApplyDefaults alone would produce six
	// rows with nothing enabled, which fails validation ("no track is enabled")
	// and makes creating a destination impossible. Seed a real default instead:
	// track 1 at unity, which is what a single-track ingest wants anyway.
	if dst.Profile.IsUnset() {
		dst.Profile = routing.DefaultProfile()
	}
	dst.Profile.ApplyDefaults()
	if err := dst.Validate(); err != nil {
		return nil, err
	}

	profile, err := json.Marshal(dst.Profile)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()

	var maxPos sql.NullInt64
	if err := d.sql.QueryRow(`SELECT MAX(position) FROM destinations`).Scan(&maxPos); err != nil {
		return nil, err
	}
	dst.Position = int(maxPos.Int64) + 1

	res, err := d.sql.Exec(`INSERT INTO destinations
		(name, kind, platform, account_id, url, stream_key, enabled, audio_bitrate, profile, position, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		dst.Name, dst.Kind, dst.Platform, dst.AccountID, dst.URL, dst.StreamKey,
		dst.Enabled, dst.AudioBitrate, string(profile), dst.Position, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return d.GetDestination(id)
}

// UpdateDestination replaces a destination's mutable fields.
func (d *DB) UpdateDestination(dst *Destination) (*Destination, error) {
	if dst.AudioBitrate == 0 {
		dst.AudioBitrate = 160
	}
	if dst.Platform == "" {
		dst.Platform = PlatformCustom
	}
	dst.Profile.ApplyDefaults()
	if err := dst.Validate(); err != nil {
		return nil, err
	}
	profile, err := json.Marshal(dst.Profile)
	if err != nil {
		return nil, err
	}
	res, err := d.sql.Exec(`UPDATE destinations SET
		name=?, kind=?, platform=?, account_id=?, url=?, stream_key=?,
		enabled=?, audio_bitrate=?, profile=?, updated_at=? WHERE id=?`,
		dst.Name, dst.Kind, dst.Platform, dst.AccountID, dst.URL, dst.StreamKey,
		dst.Enabled, dst.AudioBitrate, string(profile), time.Now().Unix(), dst.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return d.GetDestination(dst.ID)
}

// SetDestinationEnabled flips the run/stop intent without touching anything
// else, so start/stop never risks rewriting a routing profile.
func (d *DB) SetDestinationEnabled(id int64, enabled bool) error {
	res, err := d.sql.Exec(`UPDATE destinations SET enabled=?, updated_at=? WHERE id=?`,
		enabled, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteDestination removes a destination.
func (d *DB) DeleteDestination(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM destinations WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
