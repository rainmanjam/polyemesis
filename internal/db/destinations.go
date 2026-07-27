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
	// DestAudio carries no video at all — an Icecast mount for a radio or
	// podcast feed, or an audio file. The routing profile is the whole output,
	// which makes this the one kind where the per-destination mix is not a
	// feature of the stream but the entire stream.
	DestAudio DestKind = "audio" // icecast://user:pass@host:port/mount, or a filename
)

// IcecastScheme is the URL prefix an audio-only destination uses for a live
// mount. Credentials and mount point ride in the URL, the way FFmpeg's icecast
// protocol expects them, so no new column is needed to hold them.
const IcecastScheme = "icecast://"

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
	// RenditionID selects the shared video encode this destination subscribes
	// to. nil is passthrough: no encode, no process, straight off the ingest
	// relay. Whatever the rendition, the destination still does -c:v copy plus
	// its own audio routing graph.
	RenditionID *int64 `json:"renditionId,omitempty"`
	// Expert mode: arguments an operator hand-wrote for this destination,
	// stored as the raw strings they typed so the editor shows them back
	// unchanged. Parsing and the guard acknowledgement live in the API, which
	// is the only place allowed to set these — see handleUpdateDestination.
	//
	// Empty for every destination that has not opted in, which is why they are
	// omitempty: a payload for an ordinary destination looks exactly as it did
	// before expert mode existed.
	ExtraInputArgs  string `json:"extraInputArgs,omitempty"`
	ExtraOutputArgs string `json:"extraOutputArgs,omitempty"`
	// ExpertAckReencode records the operator agreeing, in as many words, that
	// an argument here overrides something the product otherwise guarantees.
	// Stored rather than treated as a one-shot confirmation, so a later edit
	// that keeps the same override does not lose the record of who agreed.
	ExpertAckReencode bool      `json:"expertAckReencode,omitempty"`
	Position          int       `json:"position"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// ExpertArgsSet reports whether this destination has any hand-written
// arguments. Two empty strings and no row at all must both read as "expert
// mode off".
func (d Destination) ExpertArgsSet() bool {
	return strings.TrimSpace(d.ExtraInputArgs) != "" ||
		strings.TrimSpace(d.ExtraOutputArgs) != ""
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
	case DestRTMP, DestSRT, DestFile, DestAudio:
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
	case DestAudio:
		// Two shapes, one kind: a live Icecast mount, or an audio file. The
		// file form gets the same confinement DestFile gets, because "audio
		// only" changes what is written, never where it may be written.
		switch {
		case target == "":
			add("an Icecast URL or an output filename is required")
		case strings.Contains(target, "://"):
			if u, err := url.Parse(target); err != nil {
				add("malformed audio destination URL: %v", err)
			} else if u.Scheme != strings.TrimSuffix(IcecastScheme, "://") {
				add("audio destination URL must start with %s (got %q)", IcecastScheme, u.Scheme)
			} else if strings.Trim(u.Path, "/") == "" {
				add("Icecast destination needs a mount point, e.g. %sHOST:8000/live.mp3", IcecastScheme)
			}
		case strings.Contains(target, ".."), strings.HasPrefix(target, "/"):
			add("audio file destination must be a relative name inside the recordings directory")
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
		rendition  sql.NullInt64
		profileRaw string
		created    int64
		updated    int64
	)
	err := s.Scan(&d.ID, &d.Name, &d.Kind, &d.Platform, &acct, &d.URL, &d.StreamKey,
		&d.Enabled, &d.AudioBitrate, &profileRaw, &rendition,
		&d.ExtraInputArgs, &d.ExtraOutputArgs, &d.ExpertAckReencode,
		&d.Position, &created, &updated)
	if err != nil {
		return nil, err
	}
	if acct.Valid {
		v := acct.Int64
		d.AccountID = &v
	}
	// NULL stays nil: that is passthrough, which is what every destination
	// created before renditions existed reads back as.
	if rendition.Valid {
		v := rendition.Int64
		d.RenditionID = &v
	}
	if err := json.Unmarshal([]byte(profileRaw), &d.Profile); err != nil {
		return nil, fmt.Errorf("destination %d: decode routing profile: %w", d.ID, err)
	}
	d.CreatedAt = time.Unix(created, 0)
	d.UpdatedAt = time.Unix(updated, 0)
	return &d, nil
}

const destColumns = `id, name, kind, platform, account_id, url, stream_key,
	enabled, audio_bitrate, profile, rendition_id,
	extra_input_args, extra_output_args, expert_ack_reencode,
	position, created_at, updated_at`

// checkRendition rejects a rendition_id that names no rendition. The foreign
// key would catch it anyway, but only as "FOREIGN KEY constraint failed",
// which tells the user nothing about which field is wrong. A nil id is
// passthrough and always valid.
func (d *DB) checkRendition(id *int64) error {
	if id == nil {
		return nil
	}
	_, err := d.GetRendition(*id)
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("invalid destination: rendition %d does not exist", *id)
	}
	return err
}

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
	if err := d.checkRendition(dst.RenditionID); err != nil {
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
		(name, kind, platform, account_id, url, stream_key, enabled, audio_bitrate, profile, rendition_id,
		 extra_input_args, extra_output_args, expert_ack_reencode, position, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		dst.Name, dst.Kind, dst.Platform, dst.AccountID, dst.URL, dst.StreamKey,
		dst.Enabled, dst.AudioBitrate, string(profile), dst.RenditionID,
		dst.ExtraInputArgs, dst.ExtraOutputArgs, dst.ExpertAckReencode, dst.Position, now, now)
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
	if err := d.checkRendition(dst.RenditionID); err != nil {
		return nil, err
	}
	profile, err := json.Marshal(dst.Profile)
	if err != nil {
		return nil, err
	}
	res, err := d.sql.Exec(`UPDATE destinations SET
		name=?, kind=?, platform=?, account_id=?, url=?, stream_key=?,
		enabled=?, audio_bitrate=?, profile=?, rendition_id=?,
		extra_input_args=?, extra_output_args=?, expert_ack_reencode=?,
		updated_at=? WHERE id=?`,
		dst.Name, dst.Kind, dst.Platform, dst.AccountID, dst.URL, dst.StreamKey,
		dst.Enabled, dst.AudioBitrate, string(profile), dst.RenditionID,
		dst.ExtraInputArgs, dst.ExtraOutputArgs, dst.ExpertAckReencode,
		time.Now().Unix(), dst.ID)
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

func destinationIDs(tx *sql.Tx) (map[int64]bool, error) {
	rows, err := tx.Query(`SELECT id FROM destinations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

// checkPermutation reports whether ids names every member of known exactly
// once. Anything less is rejected rather than partially honoured: a subset
// would leave the unnamed rows sharing positions with the named ones, and the
// resulting order is not one any client asked for.
func checkPermutation(ids []int64, known map[int64]bool) error {
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if !known[id] {
			return fmt.Errorf("cannot reorder: destination %d does not exist", id)
		}
		if seen[id] {
			return fmt.Errorf("cannot reorder: destination %d listed twice", id)
		}
		seen[id] = true
	}
	if len(ids) != len(known) {
		return fmt.Errorf("cannot reorder: got %d ids for %d destinations", len(ids), len(known))
	}
	return nil
}

// ReorderDestinations rewrites display order so it matches ids, which must
// name every destination exactly once. Position is presentation only, so
// updated_at is deliberately left alone: moving a card up the dashboard is not
// an edit to the destination.
func (d *DB) ReorderDestinations(ids []int64) error {
	// One transaction, because a half-applied order leaves rows sharing a
	// position and the dashboard in an arrangement nobody asked for.
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	known, err := destinationIDs(tx)
	if err != nil {
		return err
	}
	if err := checkPermutation(ids, known); err != nil {
		return err
	}

	stmt, err := tx.Prepare(`UPDATE destinations SET position=? WHERE id=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for pos, id := range ids {
		if _, err := stmt.Exec(pos, id); err != nil {
			return err
		}
	}
	return tx.Commit()
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

// MigrateDestinationExpertArgs adds the expert-mode columns to a destinations
// table created before expert mode existed, and folds in anything the earlier
// sidecar table held.
//
// Same reasoning as MigrateRenditions, and the same constraint: schema.sql only
// runs CREATE TABLE IF NOT EXISTS, which is a no-op against a table that is
// already there, so a new column can only arrive by ALTER. Idempotent, safe on
// every open, and every existing row reads back as two empty strings — which is
// precisely "expert mode off".
//
// The destination_expert_args table is the shape expert mode shipped in while
// internal/db was owned by another workstream. It is drained and dropped here
// rather than left in place, so there is exactly one answer to "what arguments
// does this destination run with".
func (d *DB) MigrateDestinationExpertArgs() error {
	columns := []struct{ name, ddl string }{
		{"extra_input_args", `ALTER TABLE destinations ADD COLUMN extra_input_args TEXT NOT NULL DEFAULT ''`},
		{"extra_output_args", `ALTER TABLE destinations ADD COLUMN extra_output_args TEXT NOT NULL DEFAULT ''`},
		{"expert_ack_reencode", `ALTER TABLE destinations ADD COLUMN expert_ack_reencode INTEGER NOT NULL DEFAULT 0`},
	}
	for _, c := range columns {
		has, err := columnExists(d.sql, "destinations", c.name)
		if err != nil {
			return fmt.Errorf("inspect destinations columns: %w", err)
		}
		if has {
			continue
		}
		if _, err := d.sql.Exec(c.ddl); err != nil {
			return fmt.Errorf("add destinations.%s: %w", c.name, err)
		}
	}

	sidecar, err := tableExists(d.sql, "destination_expert_args")
	if err != nil {
		return fmt.Errorf("inspect destination_expert_args: %w", err)
	}
	if !sidecar {
		return nil
	}
	// One UPDATE, then the drop. A destination whose sidecar row was deleted
	// between the two statements simply keeps its empty columns, which is the
	// same answer either order would have given.
	if _, err := d.sql.Exec(`UPDATE destinations SET
		extra_input_args    = COALESCE((SELECT input_args   FROM destination_expert_args e WHERE e.destination_id = destinations.id), extra_input_args),
		extra_output_args   = COALESCE((SELECT output_args  FROM destination_expert_args e WHERE e.destination_id = destinations.id), extra_output_args),
		expert_ack_reencode = COALESCE((SELECT ack_reencode FROM destination_expert_args e WHERE e.destination_id = destinations.id), expert_ack_reencode)`); err != nil {
		return fmt.Errorf("fold destination_expert_args into destinations: %w", err)
	}
	if _, err := d.sql.Exec(`DROP TABLE destination_expert_args`); err != nil {
		return fmt.Errorf("drop destination_expert_args: %w", err)
	}
	return nil
}

func tableExists(sqldb *sql.DB, table string) (bool, error) {
	var name string
	err := sqldb.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
