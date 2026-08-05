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
	// PlatformFacebook is a real platform rather than a preset because Facebook
	// issues its ingest per broadcast over the Graph API: there is nothing for
	// an operator to paste, so the integration has to exist for the destination
	// to work at all. The string matches routing.PlatformFacebook, which is what
	// makes the Rights Manager music policy apply to these destinations.
	PlatformFacebook Platform = "facebook"
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
	// BackupURL and BackupStreamKey are the platform's secondary ingest,
	// stored when the broadcast was created. Empty when the platform offered
	// none, which is the normal state for every destination without backup
	// ingest enabled.
	//
	// On Destination rather than in FacebookSettings because the ENGINE
	// consumes them, and the engine should not have to know which platform a
	// destination is. Nothing but Facebook populates them today.
	BackupURL       string `json:"backupUrl,omitempty"`
	BackupStreamKey string `json:"backupStreamKey,omitempty"`
	// Enabled is user intent, not live state: "this should be running".
	Enabled      bool            `json:"enabled"`
	AudioBitrate int             `json:"audioBitrate"` // kbps
	Profile      routing.Profile `json:"profile"`
	// RenditionID selects the shared video encode this destination subscribes
	// to. nil is passthrough: no encode, no process, straight off the ingest
	// relay. Whatever the rendition, the destination still does -c:v copy plus
	// its own audio routing graph.
	RenditionID *int64 `json:"renditionId,omitempty"`
	// SourceID is the programme this destination carries.
	//
	// It is a pointer only because SQLite would not accept a NOT NULL REFERENCES
	// column through ALTER TABLE while foreign keys are on -- the nullability is
	// a migration artefact, not part of the model. In practice it is never nil:
	// CreateDestination fills it with the default source when a caller omits it,
	// the foreign key CASCADEs, and scanDestination REFUSES a NULL rather than
	// propagating one. A destination with no source belongs to no programme, so
	// no reconciler lists it as work and it would sit there created but never
	// started.
	SourceID *int64 `json:"sourceId,omitempty"`
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
	ExpertAckReencode bool `json:"expertAckReencode,omitempty"`
	// Transport is the optional muxer and socket tuning. Its zero value emits
	// no FFmpeg arguments at all, so a destination that has not opted in
	// produces exactly the command it always did.
	Transport DestTransport `json:"transport"`
	// Resilience is how hard this destination is retried, and when to stop.
	// Its zero value is the behaviour every destination had before it existed:
	// retry forever, 1s to 30s.
	Resilience DestResilience `json:"resilience"`
	// Audio is the output encoding choice. Its zero value is AAC stereo, which
	// is what every destination emitted before it existed.
	Audio AudioEncoding `json:"audio"`
	// Compliance is the obligation metadata: who the programme is for, who may
	// see it, what a viewer is about to be shown. Its zero value touches
	// nothing -- see oauth.Compliance.
	Compliance Compliance `json:"compliance"`
	// Facebook is create-time configuration for a Facebook destination. Empty
	// for every other platform, and for a Facebook destination that has not set
	// any of it.
	Facebook  FacebookSettings `json:"facebook"`
	Position  int              `json:"position"`
	CreatedAt time.Time        `json:"createdAt"`
	UpdatedAt time.Time        `json:"updatedAt"`
}

// ExpertArgsSet reports whether this destination has any hand-written
// arguments. Two empty strings and no row at all must both read as "expert
// mode off".
// Transport bounds. Wide on purpose: these catch a unit mix-up or a typo, not
// an opinion about how somebody runs their box.
const (
	// MaxMuxQueuePackets is FFmpeg's own practical ceiling; beyond this the
	// queue is a memory leak with a limit rather than a buffer.
	MaxMuxQueuePackets = 1 << 20
	// MaxMuxQueueBytes is 1 GiB. A threshold larger than the machine's RAM is
	// a typo, not a policy.
	MaxMuxQueueBytes = 1 << 30
	// MaxRWTimeoutSeconds is an hour. Anything longer is indistinguishable
	// from the hang the timeout exists to break.
	MaxRWTimeoutSeconds = 3600
	// MinRWTimeoutSeconds is 1. A sub-second timeout on a live socket fires on
	// ordinary jitter and turns a healthy stream into a restart loop.
	MinRWTimeoutSeconds = 1
)

// Resilience bounds.
const (
	MinDestBackoffSeconds = 1
	MaxDestBackoffSeconds = 300
	// MaxDestGiveUpAfter is generous: a platform that has refused a thousand
	// times is not coming back, but the number exists to catch a typo rather
	// than to express an opinion.
	MaxDestGiveUpAfter = 1000
)

// DestResilience is the per-destination reconnect policy.
//
// The one global knob that existed before this was
// settings.ingest.pull.reconnectDelayMaxSeconds, and that governs PULL INGEST
// -- the dial-out source -- not destinations. Destinations had no policy at
// all: every one retried forever on the same 1s-to-30s curve, whatever it was
// and however hopeless.
type DestResilience struct {
	// MinBackoffSeconds and MaxBackoffSeconds bracket the retry curve. 0 takes
	// the supervisor's defaults, which are 1 and 30.
	MinBackoffSeconds int `json:"minBackoffSeconds,omitempty"`
	MaxBackoffSeconds int `json:"maxBackoffSeconds,omitempty"`
	// GiveUpAfter stops retrying after this many CONSECUTIVE failed restarts.
	// 0 is forever, which is the historical behaviour and still the right
	// answer for a platform that is merely slow to come back.
	//
	// Consecutive, not cumulative: a destination that reconnects cleanly once
	// an hour for a week must never accumulate its way to the limit. A run
	// that lasts past the supervisor's stability window resets the count.
	//
	// The point is not to save CPU. It is that a destination retrying forever
	// is INDISTINGUISHABLE from one that works -- the card says
	// "reconnecting", and nothing ever says this endpoint is not coming back.
	// Giving up moves it to failed, which the alert rules already treat as an
	// incident, so the operator is told once rather than never.
	GiveUpAfter int `json:"giveUpAfter,omitempty"`
}

// Active reports whether any resilience policy is set.
func (r DestResilience) Active() bool {
	return r.MinBackoffSeconds > 0 || r.MaxBackoffSeconds > 0 || r.GiveUpAfter > 0
}

func (r DestResilience) problems() []string {
	var probs []string
	add := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }

	for _, b := range []struct {
		name string
		v    int
	}{{"minimum", r.MinBackoffSeconds}, {"maximum", r.MaxBackoffSeconds}} {
		if b.v != 0 && (b.v < MinDestBackoffSeconds || b.v > MaxDestBackoffSeconds) {
			add("%s reconnect delay %ds out of range (%d-%d, 0 for the default)",
				b.name, b.v, MinDestBackoffSeconds, MaxDestBackoffSeconds)
		}
	}
	// Refused rather than silently swapped. An inverted pair is a typo, and
	// quietly reordering it would hide the typo AND produce a retry curve the
	// operator did not ask for.
	if r.MinBackoffSeconds > 0 && r.MaxBackoffSeconds > 0 &&
		r.MinBackoffSeconds > r.MaxBackoffSeconds {
		add("minimum reconnect delay %ds is greater than the maximum %ds",
			r.MinBackoffSeconds, r.MaxBackoffSeconds)
	}
	if r.GiveUpAfter < 0 || r.GiveUpAfter > MaxDestGiveUpAfter {
		add("give up after %d retries out of range (0-%d, 0 to retry forever)",
			r.GiveUpAfter, MaxDestGiveUpAfter)
	}
	return probs
}

// Audio codec choices for a destination.
const (
	// DestAudioAAC is the default and the only thing every platform takes.
	DestAudioAAC = ""
	// DestAudioOpus is meaningfully better below ~64 kbps. SRT and file
	// destinations only -- see DestAudio.Codec.
	DestAudioOpus = "opus"
)

// DestAudioCodecs is every codec a destination may name, in the order to offer
// them: the universal one first.
var DestAudioCodecs = []string{DestAudioAAC, DestAudioOpus}

// AudioEncoding is the per-destination output encoding.
//
// Smaller than the roadmap asked for, because one of its three items does not
// exist. "AAC profile (LC / HE-AAC v1 / v2)" is NOT BUILDABLE: FFmpeg's native
// aac encoder exposes no -profile option, and `-profile:a aac_he` makes it
// refuse to open outright ("Profile not supported!"). HE-AAC needs the nonfree
// libfdk_aac, which cannot ship in a redistributable build.
//
// The goal behind that item -- good audio well below 64 kbps -- is met by Opus,
// which is free, already in the pinned build, and better than HE-AAC at those
// rates. Answered by a different means rather than abandoned.
type AudioEncoding struct {
	// Codec is empty for AAC. Opus is refused on RTMP: FFmpeg will write it
	// into FLV, because Enhanced RTMP defines a mapping, and no mainstream
	// ingest accepts it. A stream that muxes cleanly and is rejected by the
	// platform looks correct everywhere the operator can see.
	Codec string `json:"codec,omitempty"`
	// Mono folds the routing graph's stereo output to one channel. A DOWNMIX
	// of the operator's mix, not a re-route: the matrix still produces OutL and
	// OutR and this sums them. Halves the bitrate on talk content for no
	// perceptual loss.
	Mono bool `json:"mono,omitempty"`
}

func (a AudioEncoding) problems(kind DestKind) []string {
	var probs []string
	add := func(f string, v ...any) { probs = append(probs, fmt.Sprintf(f, v...)) }

	known := false
	for _, c := range DestAudioCodecs {
		if c == a.Codec {
			known = true
			break
		}
	}
	if !known {
		add("unknown audio codec %q (aac, opus)", a.Codec)
	}
	// Refused at save time rather than silently downgraded at start time. A
	// downgrade would leave the operator looking at a destination whose
	// settings say Opus and whose stream is AAC, with nothing anywhere saying
	// which is running -- the exact failure the deinterlace validation exists
	// to prevent.
	if a.Codec == DestAudioOpus && kind == DestRTMP {
		add("opus cannot be used on an RTMP destination: FFmpeg will mux it, " +
			"but no mainstream RTMP ingest accepts it, so the stream would " +
			"upload cleanly and be rejected")
	}
	return probs
}

// DestTransport is the per-destination muxer and socket tuning.
//
// Everything here was probed against the pinned FFmpeg before it was designed
// around. See ffmpeg.TransportSpec for the probe results, including the one
// that corrected the roadmap: max_muxing_queue_size and
// muxing_queue_data_threshold are a PAIR, not alternatives.
type DestTransport struct {
	// NoDurationFilesize drops FLV's zero duration and filesize metadata.
	// RTMP only; ignored elsewhere rather than refused, because a destination
	// switched from RTMP to SRT should not become unsavable.
	NoDurationFilesize bool `json:"noDurationFilesize,omitempty"`
	// MuxQueuePackets and MuxQueueBytes bound the interleave buffer. The
	// packet cap applies only once the queue passes the byte threshold, so
	// setting the threshold alone does nothing -- which is why the UI offers
	// them together.
	MuxQueuePackets int `json:"muxQueuePackets,omitempty"`
	MuxQueueBytes   int `json:"muxQueueBytes,omitempty"`
	// RWTimeoutSeconds breaks a half-open socket. Without it a far end that
	// vanished without a FIN blocks the muxer indefinitely: FFmpeg keeps
	// running, the supervisor sees a live process, and the stream is off air
	// with nothing reporting it.
	RWTimeoutSeconds int `json:"rwTimeoutSeconds,omitempty"`
}

// Active reports whether any transport tuning is set.
func (t DestTransport) Active() bool {
	return t.NoDurationFilesize || t.MuxQueuePackets > 0 || t.MuxQueueBytes > 0 ||
		t.RWTimeoutSeconds > 0
}

// problems reports everything wrong with the transport block.
func (t DestTransport) problems() []string {
	var probs []string
	add := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }

	if t.MuxQueuePackets < 0 || t.MuxQueuePackets > MaxMuxQueuePackets {
		add("muxing queue %d packets out of range (0-%d, 0 for the FFmpeg default)",
			t.MuxQueuePackets, MaxMuxQueuePackets)
	}
	if t.MuxQueueBytes < 0 || t.MuxQueueBytes > MaxMuxQueueBytes {
		add("muxing queue %d bytes out of range (0-%d, 0 for the FFmpeg default)",
			t.MuxQueueBytes, MaxMuxQueueBytes)
	}
	// Refused rather than quietly accepted, because a byte threshold with no
	// packet cap is a setting that appears to do something and does nothing:
	// FFmpeg documents the threshold as "the threshold after which
	// max_muxing_queue_size is taken into account".
	if t.MuxQueueBytes > 0 && t.MuxQueuePackets == 0 {
		add("a muxing queue byte threshold does nothing without a packet limit: " +
			"FFmpeg only applies the packet cap once the queue passes the threshold")
	}
	if t.RWTimeoutSeconds != 0 &&
		(t.RWTimeoutSeconds < MinRWTimeoutSeconds || t.RWTimeoutSeconds > MaxRWTimeoutSeconds) {
		add("socket timeout %ds out of range (%d-%d, 0 to disable)",
			t.RWTimeoutSeconds, MinRWTimeoutSeconds, MaxRWTimeoutSeconds)
	}
	return probs
}

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
	case PlatformCustom, PlatformYouTube, PlatformTwitch, PlatformKick, PlatformFacebook, "":
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

	for _, p := range d.Transport.problems() {
		add("%s", p)
	}
	for _, p := range d.Resilience.problems() {
		add("%s", p)
	}
	for _, p := range d.Audio.problems(d.Kind) {
		add("%s", p)
	}
	// Refused at save time rather than at go-live. A compliance field that
	// fails when the operator presses "go live" fails at the one moment they
	// cannot stop to fix it.
	for _, p := range d.Compliance.Problems() {
		add("%s", p)
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
		source     sql.NullInt64
		profileRaw string
		// Defaulted to "{}" so a row written before the column existed decodes
		// to a zero Compliance -- touch nothing -- rather than failing the scan.
		complianceJSON = "{}"
		// Same reasoning as complianceJSON: a row written before this column
		// existed must decode to a zero FacebookSettings, not fail the scan.
		facebookJSON = "{}"
		created      int64
		updated      int64
	)
	err := s.Scan(&d.ID, &d.Name, &d.Kind, &d.Platform, &acct, &d.URL, &d.StreamKey,
		&d.BackupURL, &d.BackupStreamKey,
		&d.Enabled, &d.AudioBitrate, &profileRaw, &rendition, &source,
		&d.ExtraInputArgs, &d.ExtraOutputArgs, &d.ExpertAckReencode,
		&d.Transport.NoDurationFilesize, &d.Transport.MuxQueuePackets,
		&d.Transport.MuxQueueBytes, &d.Transport.RWTimeoutSeconds,
		&d.Resilience.MinBackoffSeconds, &d.Resilience.MaxBackoffSeconds,
		&d.Resilience.GiveUpAfter,
		&d.Audio.Codec, &d.Audio.Mono, &complianceJSON, &facebookJSON,
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
	if source.Valid {
		v := source.Int64
		d.SourceID = &v
	} else {
		// A NULL source_id is a row that no reconciler will ever start: it
		// belongs to no programme, so nothing lists it as work to do. It is
		// created successfully, appears in the API, and silently never runs.
		//
		// Refused at the boundary rather than propagated, so the impossible
		// state stops here instead of every caller downstream having to know
		// about it. CreateDestination fills the field and the FK CASCADEs, so
		// reaching this means the database was edited by hand or a migration
		// half-finished -- both worth failing loudly over.
		return nil, fmt.Errorf("destination %d has no source: it belongs to no "+
			"programme and would never be started", d.ID)
	}
	if complianceJSON != "" {
		if err := json.Unmarshal([]byte(complianceJSON), &d.Compliance); err != nil {
			return nil, fmt.Errorf("destination %d has unreadable compliance metadata: %w", d.ID, err)
		}
	}
	if facebookJSON != "" {
		if err := json.Unmarshal([]byte(facebookJSON), &d.Facebook); err != nil {
			return nil, fmt.Errorf("destination %d has unreadable Facebook settings: %w", d.ID, err)
		}
	}
	if err := json.Unmarshal([]byte(profileRaw), &d.Profile); err != nil {
		return nil, fmt.Errorf("destination %d: decode routing profile: %w", d.ID, err)
	}
	d.CreatedAt = time.Unix(created, 0)
	d.UpdatedAt = time.Unix(updated, 0)
	return &d, nil
}

const destColumns = `id, name, kind, platform, account_id, url, stream_key,
	backup_url, backup_stream_key,
	enabled, audio_bitrate, profile, rendition_id, source_id,
	extra_input_args, extra_output_args, expert_ack_reencode,
	tr_no_duration_filesize, tr_mux_queue_packets, tr_mux_queue_bytes, tr_rw_timeout_seconds,
	rs_min_backoff_seconds, rs_max_backoff_seconds, rs_give_up_after,
	au_codec, au_mono, compliance, facebook,
	position, created_at, updated_at`

// The reads below, as whole compile-time constants.
//
// Go folds `"a" + constB + "c"` at compile time when every operand is a const,
// so these cost nothing at runtime and cannot vary. A query assembled at the
// call site is indistinguishable, to a reader and to a static analyser, from
// one that interpolates a variable; a constant is safe BY CONSTRUCTION,
// because there is no expression left for a value to reach. Fuller argument in
// chat.go.
const (
	destBySourceQuery = `SELECT ` + destColumns + ` FROM destinations WHERE source_id = ? ORDER BY position, id`
	destListQuery     = `SELECT ` + destColumns + ` FROM destinations ORDER BY position, id`
	destByIDQuery     = `SELECT ` + destColumns + ` FROM destinations WHERE id = ?`
)

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
// ListDestinationsBySource returns the destinations belonging to one source.
//
// This is what a per-source engine reconciles against: it must never see, and
// so can never start, a destination that belongs to another programme. Getting
// that wrong would fan the wrong video out to somebody's platform, which is the
// worst failure this feature can have.
func (d *DB) ListDestinationsBySource(sourceID int64) ([]*Destination, error) {
	rows, err := d.sql.Query(
		destBySourceQuery, sourceID)
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

func (d *DB) ListDestinations() ([]*Destination, error) {
	rows, err := d.sql.Query(destListQuery)
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
	row := d.sql.QueryRow(destByIDQuery, id)
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
	// A request that names no source means the one the operator has always
	// had. Every API client written before sources existed sends exactly that,
	// and the alternative is a destination with a NULL source_id that no
	// reconciler ever picks up -- created successfully, never started, with
	// nothing on screen to explain why.
	if dst.SourceID == nil {
		id, err := d.DefaultSourceID()
		if err != nil {
			return nil, fmt.Errorf("resolve default source: %w", err)
		}
		dst.SourceID = &id
	}

	profile, err := json.Marshal(dst.Profile)
	if err != nil {
		return nil, err
	}
	compliance, err := json.Marshal(dst.Compliance)
	if err != nil {
		return nil, err
	}
	facebook, err := json.Marshal(dst.Facebook)
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
		(name, kind, platform, account_id, url, stream_key, backup_url, backup_stream_key,
		 enabled, audio_bitrate, profile, rendition_id, source_id,
		 extra_input_args, extra_output_args, expert_ack_reencode,
		 tr_no_duration_filesize, tr_mux_queue_packets, tr_mux_queue_bytes, tr_rw_timeout_seconds,
		 rs_min_backoff_seconds, rs_max_backoff_seconds, rs_give_up_after,
		 au_codec, au_mono, compliance, facebook,
		 position, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		dst.Name, dst.Kind, dst.Platform, dst.AccountID, dst.URL, dst.StreamKey,
		dst.BackupURL, dst.BackupStreamKey,
		dst.Enabled, dst.AudioBitrate, string(profile), dst.RenditionID, dst.SourceID,
		dst.ExtraInputArgs, dst.ExtraOutputArgs, dst.ExpertAckReencode,
		dst.Transport.NoDurationFilesize, dst.Transport.MuxQueuePackets,
		dst.Transport.MuxQueueBytes, dst.Transport.RWTimeoutSeconds,
		dst.Resilience.MinBackoffSeconds, dst.Resilience.MaxBackoffSeconds,
		dst.Resilience.GiveUpAfter,
		dst.Audio.Codec, dst.Audio.Mono, string(compliance), string(facebook),
		dst.Position, now, now)
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
	compliance, err := json.Marshal(dst.Compliance)
	if err != nil {
		return nil, err
	}
	facebook, err := json.Marshal(dst.Facebook)
	if err != nil {
		return nil, err
	}
	res, err := d.sql.Exec(`UPDATE destinations SET
		name=?, kind=?, platform=?, account_id=?, url=?, stream_key=?,
		backup_url=?, backup_stream_key=?,
		enabled=?, audio_bitrate=?, profile=?, rendition_id=?, source_id=?,
		extra_input_args=?, extra_output_args=?, expert_ack_reencode=?,
		tr_no_duration_filesize=?, tr_mux_queue_packets=?, tr_mux_queue_bytes=?,
		tr_rw_timeout_seconds=?,
		rs_min_backoff_seconds=?, rs_max_backoff_seconds=?, rs_give_up_after=?,
		au_codec=?, au_mono=?, compliance=?, facebook=?,
		updated_at=? WHERE id=?`,
		dst.Name, dst.Kind, dst.Platform, dst.AccountID, dst.URL, dst.StreamKey,
		dst.BackupURL, dst.BackupStreamKey,
		dst.Enabled, dst.AudioBitrate, string(profile), dst.RenditionID, dst.SourceID,
		dst.ExtraInputArgs, dst.ExtraOutputArgs, dst.ExpertAckReencode,
		dst.Transport.NoDurationFilesize, dst.Transport.MuxQueuePackets,
		dst.Transport.MuxQueueBytes, dst.Transport.RWTimeoutSeconds,
		dst.Resilience.MinBackoffSeconds, dst.Resilience.MaxBackoffSeconds,
		dst.Resilience.GiveUpAfter,
		dst.Audio.Codec, dst.Audio.Mono, string(compliance), string(facebook),
		time.Now().Unix(), dst.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return d.GetDestination(dst.ID)
}

// ErrAnnouncementSkipped is what UpdateAnnouncement returns when the callback
// declined the row it was shown. Not an error in the operational sense -- it is
// the answer "the destination is no longer one this write may touch" -- so it
// is a sentinel rather than a string a caller has to match on.
var ErrAnnouncementSkipped = errors.New("announcement skipped")

// UpdateAnnouncement writes ONLY the columns the pre-announce sweep owns: the
// primary stream key, the backup endpoint, and the Facebook block that carries
// the announcement markers.
//
// WHY NOT UpdateDestination. That one is a full-row unconditional
// `UPDATE ... WHERE id=?` with no version column behind it, and the sweep holds
// a destination across a Graph call that takes up to thirty seconds. Writing
// the whole row back reverts every operator edit that landed in that window --
// a rename, a routing change, the enable switch, a key refresh -- silently, and
// for destinations late in a sweep the window is the whole sweep.
//
// So the row is READ AGAIN HERE, inside the transaction that writes it, and apply
// is handed the row as it stands now rather than the caller's snapshot. That is
// what makes the Facebook blob safe to rewrite: it also holds crossposting, the
// donate charity and the backup toggle, none of which belong to this sweep.
// Anything apply sets outside the four columns below is DISCARDED -- deliberately,
// because a caller that could write any column would be UpdateDestination again.
//
// apply reports whether to go ahead. Returning false rolls back and returns
// ErrAnnouncementSkipped, which is how the sweep refuses to rewrite the stream
// key of a destination that went live while Facebook was being asked for one:
// StreamKey is inside Target(), which is the first element of the engine's
// restart hash, so changing it under a running FFmpeg cycles the live process at
// a moment nobody chose.
func (d *DB) UpdateAnnouncement(id int64, apply func(*Destination) bool) (*Destination, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	cur, err := scanDestination(tx.QueryRow(destByIDQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !apply(cur) {
		return nil, ErrAnnouncementSkipped
	}
	facebook, err := json.Marshal(cur.Facebook)
	if err != nil {
		return nil, err
	}
	res, err := tx.Exec(`UPDATE destinations SET
		stream_key=?, backup_url=?, backup_stream_key=?, facebook=?, updated_at=?
		WHERE id=?`,
		cur.StreamKey, cur.BackupURL, cur.BackupStreamKey, string(facebook),
		time.Now().Unix(), id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.GetDestination(id)
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
		// Transport tuning. Every default is the no-op value, so an upgraded
		// install emits exactly the FFmpeg command it did yesterday.
		{"tr_no_duration_filesize", `ALTER TABLE destinations ADD COLUMN tr_no_duration_filesize INTEGER NOT NULL DEFAULT 0`},
		{"tr_mux_queue_packets", `ALTER TABLE destinations ADD COLUMN tr_mux_queue_packets INTEGER NOT NULL DEFAULT 0`},
		{"tr_mux_queue_bytes", `ALTER TABLE destinations ADD COLUMN tr_mux_queue_bytes INTEGER NOT NULL DEFAULT 0`},
		{"tr_rw_timeout_seconds", `ALTER TABLE destinations ADD COLUMN tr_rw_timeout_seconds INTEGER NOT NULL DEFAULT 0`},
		// Reconnect policy. 0 everywhere is "the behaviour you already had".
		{"rs_min_backoff_seconds", `ALTER TABLE destinations ADD COLUMN rs_min_backoff_seconds INTEGER NOT NULL DEFAULT 0`},
		{"rs_max_backoff_seconds", `ALTER TABLE destinations ADD COLUMN rs_max_backoff_seconds INTEGER NOT NULL DEFAULT 0`},
		{"rs_give_up_after", `ALTER TABLE destinations ADD COLUMN rs_give_up_after INTEGER NOT NULL DEFAULT 0`},
		// Audio encoding. '' is AAC and 0 is stereo, which is what every
		// destination emitted before these existed.
		{"au_codec", `ALTER TABLE destinations ADD COLUMN au_codec TEXT NOT NULL DEFAULT ''`},
		{"au_mono", `ALTER TABLE destinations ADD COLUMN au_mono INTEGER NOT NULL DEFAULT 0`},
		// Compliance rides as one JSON blob rather than four columns: it is a
		// map plus two scalars, edited as a unit, and '{}' is "touch nothing".
		{"compliance", `ALTER TABLE destinations ADD COLUMN compliance TEXT NOT NULL DEFAULT '{}'`},
		// Facebook's create-time block, one JSON blob for the same reason
		// compliance is one: a slice plus a scalar, edited as a unit, and '{}'
		// is "send nothing".
		{"facebook", `ALTER TABLE destinations ADD COLUMN facebook TEXT NOT NULL DEFAULT '{}'`},
		{"backup_url", `ALTER TABLE destinations ADD COLUMN backup_url TEXT NOT NULL DEFAULT ''`},
		{"backup_stream_key", `ALTER TABLE destinations ADD COLUMN backup_stream_key TEXT NOT NULL DEFAULT ''`},
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
