package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// VideoEncoder is the FFmpeg encoder a rendition drives. Software and the
// three hardware families we can reasonably expect to meet: NVIDIA (nvenc),
// Intel Quick Sync (qsv), Apple (videotoolbox), plus VA-API and AMD AMF on
// Linux/Windows. Availability is a property of the FFmpeg build, not of this
// table, so a rendition can name an encoder the running FFmpeg lacks — that is
// a start-time failure with a clear message, not a config-time one.
type VideoEncoder string

const (
	EncoderX264 VideoEncoder = "libx264"
	EncoderX265 VideoEncoder = "libx265"

	EncoderNVENCH264 VideoEncoder = "h264_nvenc"
	EncoderNVENCHEVC VideoEncoder = "hevc_nvenc"

	EncoderQSVH264 VideoEncoder = "h264_qsv"
	EncoderQSVHEVC VideoEncoder = "hevc_qsv"

	EncoderVideoToolboxH264 VideoEncoder = "h264_videotoolbox"
	EncoderVideoToolboxHEVC VideoEncoder = "hevc_videotoolbox"

	EncoderVAAPIH264 VideoEncoder = "h264_vaapi"
	EncoderVAAPIHEVC VideoEncoder = "hevc_vaapi"

	EncoderAMFH264 VideoEncoder = "h264_amf"
	EncoderAMFHEVC VideoEncoder = "hevc_amf"
)

// KnownEncoders is every encoder a rendition may name, in the order the UI
// should offer them: software first, because it is the one that always works.
var KnownEncoders = []VideoEncoder{
	EncoderX264, EncoderX265,
	EncoderNVENCH264, EncoderNVENCHEVC,
	EncoderQSVH264, EncoderQSVHEVC,
	EncoderVideoToolboxH264, EncoderVideoToolboxHEVC,
	EncoderVAAPIH264, EncoderVAAPIHEVC,
	EncoderAMFH264, EncoderAMFHEVC,
}

// Codec returns the bitstream the encoder produces: "h264" or "hevc". RTMP
// ingests are overwhelmingly H.264-only, so callers use this to warn.
func (e VideoEncoder) Codec() string {
	if e == EncoderX265 || strings.HasPrefix(string(e), "hevc_") {
		return "hevc"
	}
	return "h264"
}

// Rendition bounds. Deliberately generous: these exist to catch a typo or a
// unit mix-up (6 instead of 6000), not to express any platform's policy.
const (
	MinRenditionDimension = 128
	MaxRenditionDimension = 7680 // 8K wide, i.e. beyond anything we would encode
	MaxRenditionFPS       = 240
	MinRenditionBitrate   = 100     // kbps
	MaxRenditionBitrate   = 100_000 // kbps
	MinRenditionGOP       = 1.0     // seconds
	MaxRenditionGOP       = 10.0    // seconds
)

// Rendition is one shared video encode.
//
// The load-bearing rule of the whole feature: a rendition re-encodes VIDEO
// ONLY and passes every audio track through with -c:a copy. Destinations still
// do "-c:v copy" plus their own routing graph, so per-destination audio routing
// keeps working on top of a shared video encode and audio is never encoded
// twice. There is deliberately no audio field here, and there must never be
// one.
//
// A destination with no rendition (rendition_id NULL) is "passthrough": no
// process, no CPU, subscribed straight to the ingest relay. That is the
// default and the behaviour every pre-renditions install already has.
type Rendition struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Width/Height are the output size; 0 on either axis means "keep the
	// source's", so setting only Height rescales while preserving aspect.
	Width  int `json:"width"`
	Height int `json:"height"`
	// FPS is the output frame rate, 0 meaning "keep the source's". Integer
	// because every platform tier is expressed in whole frames; a 59.94 source
	// that wants its exact rate left alone uses 0.
	FPS int `json:"fps"`
	// VideoBitrate is the target in kbps. Always set — a rendition that does
	// not change size, rate or bitrate has no reason to exist.
	VideoBitrate int          `json:"videoBitrate"`
	Encoder      VideoEncoder `json:"encoder"`
	// Preset is the encoder's own speed/quality knob ("veryfast" for x264,
	// "p4" for nvenc, "quality" for amf...). Free text because the vocabulary
	// is per-encoder and changes between FFmpeg releases; validated only for
	// shape, since it lands on a command line.
	Preset string `json:"preset"`
	// GOPSeconds is the keyframe interval in seconds rather than frames, so it
	// stays correct when FPS changes. Live platforms want 1-4s.
	GOPSeconds float64 `json:"gopSeconds"`
	// AspectMode decides what happens when the target shape does not match the
	// source's — the vertical-plus-horizontal case. Empty is the historical
	// behaviour, a plain scale that stretches, so every stored rendition keeps
	// producing the frame it always did.
	//
	// It only takes effect when BOTH Width and Height are set: with one axis
	// free there is no mismatch to resolve.
	AspectMode string `json:"aspectMode,omitempty"`
	// Deinterlace strips field combing before scaling. '' (off), 'auto' or
	// 'all'. Empty is off, so every stored rendition keeps producing exactly
	// the frame it always did.
	Deinterlace string `json:"deinterlace,omitempty"`
	// PadColor is the bar colour for the padding modes, in any syntax FFmpeg's
	// colour parser takes. Empty means black.
	PadColor string `json:"padColor,omitempty"`
	// Note is the "what is this tier for" line. Preset-derived renditions
	// arrive with one already filled in; the user can rewrite it.
	Note string `json:"note"`
	// SourceID is the programme this rendition re-encodes. A rendition reads
	// exactly one ingest, so it belongs to exactly one source; nil means the
	// source was deleted, which CASCADE makes unreachable in practice.
	// CreateRendition fills it with the default source when omitted.
	SourceID  *int64    `json:"sourceId,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Codec reports the bitstream this rendition produces.
func (r Rendition) Codec() string { return r.Encoder.Codec() }

// ScalesVideo reports whether the rendition changes the picture size.
func (r Rendition) ScalesVideo() bool { return r.Width > 0 || r.Height > 0 }

// presetTokenOK reports whether s is safe to hand to FFmpeg as a bare
// argument. Preset is user text that becomes an argv entry, so anything with
// whitespace or shell/filter punctuation is rejected outright rather than
// quoted: no legitimate preset name has ever needed it.
func presetTokenOK(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// Validate checks a rendition is encodable, reporting every problem at once so
// the UI can mark up the whole form instead of one field per round trip.
func (r Rendition) Validate() error {
	var probs []string
	add := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }

	if strings.TrimSpace(r.Name) == "" {
		add("name is required")
	}

	// 0 is the "keep source" sentinel on both axes and must stay legal.
	for _, d := range []struct {
		axis string
		v    int
	}{{"width", r.Width}, {"height", r.Height}} {
		if d.v == 0 {
			continue
		}
		if d.v < MinRenditionDimension || d.v > MaxRenditionDimension {
			add("%s %d out of range (%d-%d, or 0 to keep the source's)",
				d.axis, d.v, MinRenditionDimension, MaxRenditionDimension)
			continue
		}
		// H.264 and HEVC both encode in 16x16 macroblocks with 4:2:0 chroma
		// subsampling, so an odd dimension has no representable chroma plane.
		// FFmpeg fails at start with "width not divisible by 2".
		if d.v%2 != 0 {
			add("%s %d must be an even number of pixels", d.axis, d.v)
		}
	}

	if r.FPS < 0 || r.FPS > MaxRenditionFPS {
		add("fps %d out of range (1-%d, or 0 to keep the source's)", r.FPS, MaxRenditionFPS)
	}

	if r.VideoBitrate < MinRenditionBitrate || r.VideoBitrate > MaxRenditionBitrate {
		add("video bitrate %d kbps out of range (%d-%d)",
			r.VideoBitrate, MinRenditionBitrate, MaxRenditionBitrate)
	}

	known := false
	for _, e := range KnownEncoders {
		if r.Encoder == e {
			known = true
			break
		}
	}
	if !known {
		add("unknown encoder %q", r.Encoder)
	}

	if !presetTokenOK(r.Preset) {
		add("preset %q must be a single word of letters, digits, '-', '_' or '.'", r.Preset)
	}

	if r.GOPSeconds < MinRenditionGOP || r.GOPSeconds > MaxRenditionGOP {
		add("gop %.4gs out of range (%g-%g seconds)", r.GOPSeconds, MinRenditionGOP, MaxRenditionGOP)
	}

	// An unknown mode is refused here rather than at start time, because the
	// filter builder degrades it to a plain scale — which is a silently
	// different picture, and the operator would have no way to tell that the
	// mode they chose is not the one running.
	if r.AspectMode != "" {
		known := false
		for _, m := range ffmpeg.AspectModes {
			if string(m) == r.AspectMode {
				known = true
				break
			}
		}
		if !known {
			add("unknown aspect mode %q", r.AspectMode)
		}
	}
	// Aspect conversion resolves a mismatch between two known shapes. With one
	// axis free the scale already preserves aspect and the mode would do
	// nothing, so saying so beats saving a control that is quietly inert.
	if r.AspectMode != "" && (r.Width == 0 || r.Height == 0) {
		add("aspect mode %q needs both a width and a height; with one axis free there is no shape to convert", r.AspectMode)
	}
	if r.PadColor != "" && !presetTokenOK(r.PadColor) {
		// It lands on a filter graph, where a comma or a colon would end the
		// argument and start something else.
		add("pad colour %q must be a single word of letters, digits, '-', '_' or '.' (e.g. black, 0x101010)", r.PadColor)
	}

	// Refused here for exactly the reason an unknown aspect mode is: the filter
	// builder degrades an unrecognised mode to OFF, so the operator would get an
	// interlaced picture from a rendition whose stored setting says otherwise,
	// with nothing anywhere to tell them which one is running.
	if r.Deinterlace != "" {
		known := false
		for _, m := range ffmpeg.DeinterlaceModes {
			if string(m) == r.Deinterlace {
				known = true
				break
			}
		}
		if !known {
			add("unknown deinterlace mode %q", r.Deinterlace)
		}
	}

	if len(probs) > 0 {
		return fmt.Errorf("invalid rendition: %s", strings.Join(probs, "; "))
	}
	return nil
}

// PresetDisclaimer is the exact wording the UI and the docs must show beside
// any preset. Platform ceilings move, and they differ by partner status; being
// confidently wrong about one breaks a live stream, so the presets are offered
// as editable starting points and say so.
const PresetDisclaimer = "Starting point — verify current limits with the platform."

// RenditionPreset is an editable starting point offered when creating a
// rendition. Passthrough is the odd one out: it is not a row at all, it is the
// absence of one.
type RenditionPreset struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Passthrough marks the zero-cost default: no encode and no process. A
	// destination on it stores rendition_id NULL.
	Passthrough bool `json:"passthrough"`
	// Rendition is the template to seed the create form with, nil for
	// passthrough. ID and timestamps are zero.
	Rendition *Rendition `json:"rendition,omitempty"`
}

// RenditionPresets returns the starting points, most-capable first after
// passthrough. Values are conservative on purpose: where we were unsure we
// picked the lower number, because an under-spec stream is watchable and an
// over-spec one is rejected at the ingest.
func RenditionPresets() []RenditionPreset {
	tier := func(key, label string, w, h, fps, kbps int, note string) RenditionPreset {
		return RenditionPreset{
			Key:   key,
			Label: label,
			Rendition: &Rendition{
				Name:         label,
				Width:        w,
				Height:       h,
				FPS:          fps,
				VideoBitrate: kbps,
				Encoder:      EncoderX264,
				// veryfast is the live-encoding default everywhere, including
				// our own recorder: slower presets lose the race with realtime
				// on a machine that is already running several encodes.
				Preset:     "veryfast",
				GOPSeconds: 2,
				Note:       note + " " + PresetDisclaimer,
			},
		}
	}

	return []RenditionPreset{
		{
			Key:         "passthrough",
			Label:       "Source passthrough",
			Passthrough: true,
		},
		tier("1080p60", "1080p60 6000 kbps", 1920, 1080, 60, 6000,
			"Sends 1080p60 to destinations that will not take a 4K or high-bitrate source."),
		tier("1080p30", "1080p30 4500 kbps", 1920, 1080, 30, 4500,
			"Half the frame rate of the 1080p60 tier, for destinations or uplinks with less headroom."),
		tier("720p60", "720p60 4500 kbps", 1280, 720, 60, 4500,
			"Keeps motion smooth where bandwidth is the constraint; the usual choice for a constrained uplink."),
		tier("720p30", "720p30 3000 kbps", 1280, 720, 30, 3000,
			"The most conservative tier: use it when a destination keeps dropping frames on everything else."),
	}
}

func scanRendition(s interface{ Scan(...any) error }) (*Rendition, error) {
	var (
		r       Rendition
		source  sql.NullInt64
		created int64
		updated int64
	)
	err := s.Scan(&r.ID, &r.Name, &r.Width, &r.Height, &r.FPS, &r.VideoBitrate,
		&r.Encoder, &r.Preset, &r.GOPSeconds, &r.AspectMode, &r.PadColor,
		&r.Deinterlace, &r.Note, &source, &created, &updated)
	if err != nil {
		return nil, err
	}
	if source.Valid {
		v := source.Int64
		r.SourceID = &v
	} else {
		// Same reasoning as destinations: a rendition with no source is
		// re-encoding nothing, and no reconciler will start it.
		return nil, fmt.Errorf("rendition %d has no source: it belongs to no "+
			"programme and would never be started", r.ID)
	}
	r.CreatedAt = time.Unix(created, 0)
	r.UpdatedAt = time.Unix(updated, 0)
	return &r, nil
}

const renditionColumns = `id, name, width, height, fps, video_bitrate,
	encoder, preset, gop_seconds, aspect_mode, pad_color, deinterlace, note, source_id, created_at, updated_at`

// applyRenditionDefaults fills in the fields an API payload is allowed to
// omit, so a create request can be as short as {"name","height","videoBitrate"}.
func (r *Rendition) applyDefaults() {
	if r.Encoder == "" {
		r.Encoder = EncoderX264
	}
	if r.Preset == "" {
		r.Preset = "veryfast"
	}
	if r.GOPSeconds == 0 {
		r.GOPSeconds = 2
	}
}

// ListRenditions returns every rendition, newest last.
// ListRenditionsBySource returns the renditions belonging to one source, which
// is what a per-source engine reconciles against.
func (d *DB) ListRenditionsBySource(sourceID int64) ([]*Rendition, error) {
	rows, err := d.sql.Query(
		`SELECT `+renditionColumns+` FROM renditions WHERE source_id = ? ORDER BY id`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*Rendition{}
	for rows.Next() {
		r, err := scanRendition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) ListRenditions() ([]*Rendition, error) {
	rows, err := d.sql.Query(`SELECT ` + renditionColumns + ` FROM renditions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*Rendition{}
	for rows.Next() {
		r, err := scanRendition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRendition loads one rendition.
func (d *DB) GetRendition(id int64) (*Rendition, error) {
	row := d.sql.QueryRow(`SELECT `+renditionColumns+` FROM renditions WHERE id = ?`, id)
	r, err := scanRendition(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// CreateRendition inserts a rendition, defaulting anything unset.
func (d *DB) CreateRendition(r *Rendition) (*Rendition, error) {
	r.applyDefaults()
	if err := r.Validate(); err != nil {
		return nil, err
	}
	// Same reasoning as CreateDestination: a payload that names no source means
	// the one that has always been there. A NULL here would produce a rendition
	// no reconciler ever starts, which looks like a rendition that does nothing.
	if r.SourceID == nil {
		id, err := d.DefaultSourceID()
		if err != nil {
			return nil, fmt.Errorf("resolve default source: %w", err)
		}
		r.SourceID = &id
	}
	now := time.Now().Unix()
	res, err := d.sql.Exec(`INSERT INTO renditions
		(name, width, height, fps, video_bitrate, encoder, preset, gop_seconds,
		 aspect_mode, pad_color, deinterlace, note, source_id, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.Name, r.Width, r.Height, r.FPS, r.VideoBitrate,
		r.Encoder, r.Preset, r.GOPSeconds, r.AspectMode, r.PadColor, r.Deinterlace, r.Note, r.SourceID, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return d.GetRendition(id)
}

// UpdateRendition replaces a rendition's mutable fields. The engine notices
// the changed row and restarts the encode; its destinations keep their own
// audio routing untouched.
func (d *DB) UpdateRendition(r *Rendition) (*Rendition, error) {
	r.applyDefaults()
	if err := r.Validate(); err != nil {
		return nil, err
	}
	res, err := d.sql.Exec(`UPDATE renditions SET
		name=?, width=?, height=?, fps=?, video_bitrate=?,
		encoder=?, preset=?, gop_seconds=?, aspect_mode=?, pad_color=?,
		deinterlace=?, note=?, source_id=?, updated_at=? WHERE id=?`,
		r.Name, r.Width, r.Height, r.FPS, r.VideoBitrate,
		r.Encoder, r.Preset, r.GOPSeconds, r.AspectMode, r.PadColor,
		r.Deinterlace, r.Note, r.SourceID, time.Now().Unix(), r.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return d.GetRendition(r.ID)
}

// DeleteRendition removes a rendition. Its destinations are NOT deleted: the
// ON DELETE SET NULL on destinations.rendition_id drops them back to
// passthrough, so the user loses an encode tier and never an endpoint.
func (d *DB) DeleteRendition(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM renditions WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountEnabledDestinationsByRendition returns, per rendition id, how many
// ENABLED destinations select it. This is the engine's ref count: a rendition
// reaching 1 starts an encode, a rendition reaching 0 stops one, and a
// rendition absent from the map must not be burning CPU.
//
// Passthrough destinations (rendition_id NULL) are deliberately not counted —
// they have no process to ref-count.
func (d *DB) CountEnabledDestinationsByRendition() (map[int64]int, error) {
	rows, err := d.sql.Query(`SELECT rendition_id, COUNT(*) FROM destinations
		WHERE enabled = 1 AND rendition_id IS NOT NULL GROUP BY rendition_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64]int{}
	for rows.Next() {
		var (
			id int64
			n  int
		)
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// MigrateRenditions brings a database created before renditions existed up to
// date. It is idempotent and safe to call on every open.
//
// schema.sql only ever runs CREATE TABLE IF NOT EXISTS, which is a no-op
// against an existing destinations table, so the new rendition_id column has
// to be added here. SQLite has no ADD COLUMN IF NOT EXISTS, hence the
// table_info probe first; the ALTER itself is legal only because the column
// defaults to NULL, which is exactly what passthrough means. Existing rows
// therefore become passthrough destinations and keep behaving as they did.
func (d *DB) MigrateRenditions() error {
	has, err := columnExists(d.sql, "destinations", "rendition_id")
	if err != nil {
		return fmt.Errorf("inspect destinations columns: %w", err)
	}
	if !has {
		if _, err := d.sql.Exec(`ALTER TABLE destinations
			ADD COLUMN rendition_id INTEGER REFERENCES renditions(id) ON DELETE SET NULL`); err != nil {
			return fmt.Errorf("add destinations.rendition_id: %w", err)
		}
	}
	// Lives here rather than in schema.sql because schema.sql runs before the
	// column is guaranteed to exist, and a failed CREATE INDEX would abort the
	// whole script and stop the server from starting.
	if _, err := d.sql.Exec(`CREATE INDEX IF NOT EXISTS idx_destinations_rendition
		ON destinations(rendition_id)`); err != nil {
		return fmt.Errorf("index destinations.rendition_id: %w", err)
	}
	return nil
}

// MigrateRenditionAspect adds the aspect-conversion columns to a database
// created before dual-format renditions existed.
//
// Both default to the empty string, which is the historical plain scale, so an
// upgraded install re-encodes exactly the frame it did yesterday until somebody
// picks a mode.
func (d *DB) MigrateRenditionAspect() error {
	for _, col := range []struct{ name, ddl string }{
		{"aspect_mode", `ALTER TABLE renditions ADD COLUMN aspect_mode TEXT NOT NULL DEFAULT ''`},
		{"pad_color", `ALTER TABLE renditions ADD COLUMN pad_color TEXT NOT NULL DEFAULT ''`},
		{"deinterlace", `ALTER TABLE renditions ADD COLUMN deinterlace TEXT NOT NULL DEFAULT ''`},
	} {
		has, err := columnExists(d.sql, "renditions", col.name)
		if err != nil {
			return fmt.Errorf("inspect renditions columns: %w", err)
		}
		if has {
			continue
		}
		if _, err := d.sql.Exec(col.ddl); err != nil {
			return fmt.Errorf("add renditions.%s: %w", col.name, err)
		}
	}
	return nil
}

func columnExists(sqldb *sql.DB, table, column string) (bool, error) {
	rows, err := sqldb.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, rows.Err()
		}
	}
	return false, rows.Err()
}
