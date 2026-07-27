package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// IngestMode selects which listener the ingest supervisor runs.
type IngestMode string

const (
	// IngestSRT is the primary path: MPEG-TS over SRT, up to six AAC tracks.
	IngestSRT IngestMode = "srt"
	// IngestRTMP is the fallback for encoders that cannot do SRT. Single
	// audio track, by protocol.
	IngestRTMP IngestMode = "rtmp"
	// IngestPull inverts the direction: rather than waiting for an encoder,
	// polyemesis dials a source. That is what lets an IP camera, another
	// server's HLS, or a looped file become an ingest.
	IngestPull IngestMode = "pull"
)

// SRTSettings configures the SRT listener.
type SRTSettings struct {
	Port int `json:"port"`
	// Passphrase enables AES encryption. SRT requires 10..79 characters.
	Passphrase string `json:"passphrase"`
	// LatencyMS is SRT's receive buffer, in milliseconds. Higher survives
	// worse networks at the cost of glass-to-glass delay.
	LatencyMS int `json:"latencyMs"`
}

// RTMPSettings configures the fallback RTMP listener.
type RTMPSettings struct {
	Port int    `json:"port"`
	App  string `json:"app"`
	// StreamKey is matched against the publisher's playpath, so a stranger
	// who finds the port still cannot publish.
	StreamKey string `json:"streamKey"`
}

// PullSettings configures the dial-out ingest.
type PullSettings struct {
	// URL is the source polyemesis dials. Its scheme must be one of
	// ffmpeg.PullSchemes(); a file:// source is a path relative to the data
	// directory, confined there the same way file destinations are.
	URL string `json:"url"`
	// ReconnectDelayMaxSeconds caps FFmpeg's HTTP reconnect backoff. A pull
	// source that drops must retry, and this bounds how long it waits before
	// each attempt. 0 uses the built-in default.
	ReconnectDelayMaxSeconds int `json:"reconnectDelayMaxSeconds"`
	// RTSPTransport is an escape hatch. Empty means TCP, which is right for
	// almost every camera; UDP is here for the ones it is not.
	RTSPTransport string `json:"rtspTransport"`
}

// IngestSettings is the whole ingest configuration.
type IngestSettings struct {
	Mode IngestMode   `json:"mode"`
	SRT  SRTSettings  `json:"srt"`
	RTMP RTMPSettings `json:"rtmp"`
	Pull PullSettings `json:"pull"`
}

// rtspTransports is FFmpeg's own closed set for -rtsp_transport. Listed so a
// typo is caught in the settings form rather than by a child that exits
// immediately and looks like a dead camera.
var rtspTransports = map[string]bool{
	"tcp": true, "udp": true, "udp_multicast": true, "http": true, "https": true,
}

// problems reports the pull tuning that is out of range regardless of mode.
// Zero and empty stay legal so a settings blob written before pull existed
// still validates.
func (p PullSettings) problems() []string {
	var probs []string
	if d := p.ReconnectDelayMaxSeconds; d < 0 || d > 3600 {
		probs = append(probs, fmt.Sprintf("pull reconnect delay %ds out of range (0-3600, 0 for the default)", d))
	}
	if t := p.RTSPTransport; t != "" && !rtspTransports[t] {
		probs = append(probs, fmt.Sprintf("unknown rtsp transport %q (tcp, udp, udp_multicast, http, https)", t))
	}
	return probs
}

// RecordingSettings controls the recorder and the retention sweeper.
type RecordingSettings struct {
	Enabled bool `json:"enabled"`
	// SegmentSeconds is the length of each MKV segment. Segmenting means a
	// crash costs you one segment, not the whole session.
	SegmentSeconds int `json:"segmentSeconds"`
	// MaxGB is the total size cap for the recordings directory. 0 = no cap.
	MaxGB float64 `json:"maxGb"`
	// MaxAgeHours deletes segments older than this. 0 = never.
	MaxAgeHours int `json:"maxAgeHours"`
	// MinFreeGB halts recording once the volume has less than this much room
	// left. Retention caps only bound what polyemesis wrote; anything else
	// sharing the volume can still fill it, and a full volume fails far more
	// than the recording. 0 = no floor.
	MinFreeGB float64 `json:"minFreeGb"`
}

// LoggingSettings controls whether captured process stderr outlives the
// process that wrote it.
type LoggingSettings struct {
	// PersistProcessLogs mirrors each captured stderr line to a rotating file
	// under the data directory. The in-memory ring dies with the server, which
	// loses exactly the lines that explain why it died.
	PersistProcessLogs bool `json:"persistProcessLogs"`
	// MaxFileMB and MaxFiles bound the log directory at their product, so
	// persistence cannot be the thing that fills the disk.
	MaxFileMB int `json:"maxFileMb"`
	MaxFiles  int `json:"maxFiles"`
}

// PreviewSettings controls the low-latency HLS preview shown on the dashboard.
type PreviewSettings struct {
	Enabled bool `json:"enabled"`
	// SegmentSeconds trades preview latency against playback stability.
	SegmentSeconds int `json:"segmentSeconds"`
	// VideoHeight is the preview's scaled height. The preview is the only
	// place polyemesis re-encodes video, and it never touches a destination.
	VideoHeight int `json:"videoHeight"`
	VideoKbps   int `json:"videoKbps"`
	// IdleTimeoutSeconds is how long the encoder outlives the last playlist
	// request. Because it is the only re-encode, the preview is started on
	// demand and stopped again once nobody is watching, so on a small box it
	// costs nothing while the dashboard is closed. 0 means the built-in
	// default.
	IdleTimeoutSeconds int `json:"idleTimeoutSeconds"`
}

// PlayoutFormat selects which packaging the playout muxes.
type PlayoutFormat string

const (
	// PlayoutHLS is HLS alone: MPEG-TS segments, which every player and every
	// set-top box in the field can already read.
	PlayoutHLS PlayoutFormat = "hls"
	// PlayoutHLSDASH adds a per-variant DASH manifest with fMP4 segments,
	// muxed by the same process from the same copied video.
	PlayoutHLSDASH PlayoutFormat = "hls+dash"
)

// Playout bounds. Wide on purpose: they catch a unit mix-up, not an opinion
// about what a sensible window is.
const (
	MinPlayoutSegmentSeconds = 1
	MaxPlayoutSegmentSeconds = 30
	MinPlayoutPlaylist       = 3
	MaxPlayoutPlaylist       = 100
	MaxPlayoutDVRSeconds     = 12 * 3600
	MinPlayoutDiskMB         = 64
	MaxPlayoutDiskMB         = 1024 * 1024 // 1 TiB
	MinPlayoutAudioKbps      = 32
	MaxPlayoutAudioKbps      = 512
	MaxPlayoutVariants       = 8
	// MaxPlayoutVariantName also bounds a directory name, since the variant
	// name is the path segment its playlist lives under.
	MaxPlayoutVariantName = 32
)

// PlayoutVariant is one publicly served ladder rung.
//
// It is a RENDITION CONSUMER, not an encoder: the video it publishes is copied
// bit-for-bit out of the rendition it names, so adding a variant costs a muxer
// and an audio encode, never a second video encode. RenditionID nil means it
// packages the ingest itself, exactly as a passthrough destination does.
type PlayoutVariant struct {
	// Name is both the label and the URL path segment the variant is served
	// under, so it is restricted to characters that are safe in both.
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	RenditionID *int64 `json:"renditionId,omitempty"`
	// AudioTrack is which ingest track this variant publishes. Playout serves
	// viewers, and a viewer's player wants exactly one stereo track, so a
	// variant picks one rather than carrying the multitrack ingest. The
	// destinations' own per-destination routing is untouched by this choice.
	AudioTrack int `json:"audioTrack"`
}

// PlayoutSettings controls the public HLS/DASH output.
//
// This is not the dashboard preview. The preview is admin-only, on-demand and
// re-encodes video at 360p; playout is a viewer-facing origin that runs while
// it is enabled and copies video from the rendition tier.
type PlayoutSettings struct {
	Enabled bool `json:"enabled"`
	// Public serves the playlists and segments without a session cookie. Off by
	// default: turning a box into a public origin is a decision, not a default.
	Public bool `json:"public"`
	// AllowCrossOrigin sends Access-Control-Allow-Origin on media responses so
	// a player embedded on another site can fetch them. Separate from Public
	// because same-origin embedding needs no CORS at all.
	AllowCrossOrigin bool          `json:"allowCrossOrigin"`
	Format           PlayoutFormat `json:"format"`
	// SegmentSeconds trades viewer latency against playlist churn. Video is
	// copied, so a segment can only start on a keyframe the upstream already
	// produced: a variant on a rendition gets the rendition's forced GOP, and a
	// passthrough variant inherits whatever the encoder that fed the ingest
	// chose.
	SegmentSeconds int `json:"segmentSeconds"`
	// PlaylistSegments is the live window. Three is the HLS minimum that lets a
	// player buffer; more costs the viewer latency but survives worse networks.
	PlaylistSegments int `json:"playlistSegments"`
	// DVRWindowSeconds keeps a rolling seekable window on disk. 0 is live-only.
	// The whole window is published, because an HLS segment that is on disk but
	// absent from the playlist is a file nobody can reach.
	DVRWindowSeconds int `json:"dvrWindowSeconds"`
	// MaxDiskMB caps the playout directory across every variant. The muxer
	// prunes its own window, but a restart orphans the previous run's segments
	// and nothing else would ever collect them, so this is the backstop that
	// keeps playout from being a disk-fill bug.
	MaxDiskMB int `json:"maxDiskMb"`
	// AudioKbps is the AAC bitrate of the single stereo track each variant
	// publishes.
	AudioKbps int `json:"audioKbps"`
	// SessionIdleSeconds is how long after its last request a viewer is still
	// counted. It has to exceed one segment or every viewer would flicker out
	// between polls.
	SessionIdleSeconds int `json:"sessionIdleSeconds"`
	// MaxSessions bounds the viewer table. Reached, further new viewers are
	// served normally and simply go uncounted: accounting must never be the
	// reason a stream stops playing.
	MaxSessions int              `json:"maxSessions"`
	Variants    []PlayoutVariant `json:"variants"`
}

// variantNameOK reports whether s is safe as both a URL path segment and a
// directory name. Rejected outright rather than escaped: the name is chosen in
// a form, so there is no cost to insisting it be boring.
func variantNameOK(s string) bool {
	if s == "" || len(s) > MaxPlayoutVariantName {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// EnabledVariants returns the variants that should be running, in order.
func (p PlayoutSettings) EnabledVariants() []PlayoutVariant {
	out := make([]PlayoutVariant, 0, len(p.Variants))
	for _, v := range p.Variants {
		if v.Enabled {
			out = append(out, v)
		}
	}
	return out
}

// problems reports the playout configuration that would produce a process that
// cannot start, or a directory that could not be bounded.
//
// Everything here is checked even when playout is disabled, so a half-filled
// form is caught when it is saved rather than when it is switched on. The one
// exception is the empty variant list, which is only a problem once enabled.
func (p PlayoutSettings) problems() []string {
	var probs []string
	add := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }

	switch p.Format {
	case PlayoutHLS, PlayoutHLSDASH:
	default:
		add("unknown playout format %q (hls, hls+dash)", p.Format)
	}
	if s := p.SegmentSeconds; s < MinPlayoutSegmentSeconds || s > MaxPlayoutSegmentSeconds {
		add("playout segment length %ds out of range (%d-%d)", s, MinPlayoutSegmentSeconds, MaxPlayoutSegmentSeconds)
	}
	if n := p.PlaylistSegments; n < MinPlayoutPlaylist || n > MaxPlayoutPlaylist {
		add("playout playlist window %d segments out of range (%d-%d)", n, MinPlayoutPlaylist, MaxPlayoutPlaylist)
	}
	if d := p.DVRWindowSeconds; d < 0 || d > MaxPlayoutDVRSeconds {
		add("playout dvr window %ds out of range (0-%d, 0 for live only)", d, MaxPlayoutDVRSeconds)
	}
	if m := p.MaxDiskMB; m < MinPlayoutDiskMB || m > MaxPlayoutDiskMB {
		add("playout disk cap %dMB out of range (%d-%d)", m, MinPlayoutDiskMB, MaxPlayoutDiskMB)
	}
	if k := p.AudioKbps; k < MinPlayoutAudioKbps || k > MaxPlayoutAudioKbps {
		add("playout audio bitrate %d kbps out of range (%d-%d)", k, MinPlayoutAudioKbps, MaxPlayoutAudioKbps)
	}
	if t := p.SessionIdleSeconds; t < 5 || t > 3600 {
		add("playout session idle timeout %ds out of range (5-3600)", t)
	}
	if n := p.MaxSessions; n < 1 || n > 1_000_000 {
		add("playout session cap %d out of range (1-1000000)", n)
	}
	if len(p.Variants) > MaxPlayoutVariants {
		add("playout has %d variants (maximum %d)", len(p.Variants), MaxPlayoutVariants)
	}
	if p.Enabled && len(p.EnabledVariants()) == 0 {
		add("playout is enabled but no variant is")
	}

	seen := map[string]bool{}
	for _, v := range p.Variants {
		if !variantNameOK(v.Name) {
			add("playout variant name %q must be 1-%d characters of letters, digits, '-' or '_'",
				v.Name, MaxPlayoutVariantName)
			continue
		}
		// Two variants sharing a name would share a directory, and the second
		// muxer would overwrite the first's playlist mid-stream.
		if seen[strings.ToLower(v.Name)] {
			add("duplicate playout variant name %q", v.Name)
		}
		seen[strings.ToLower(v.Name)] = true

		if v.AudioTrack < 0 || v.AudioTrack > 63 {
			add("playout variant %q audio track %d out of range (0-63)", v.Name, v.AudioTrack)
		}
	}
	return probs
}

// SynthSettings controls the synthetic sources.
//
// Only silence is here. The slate — a standby picture published while the
// ingest is away — needs a permanent source-selector tier between the ingest
// and the destinations, because switching a destination's subscription means
// restarting its process and dropping the platform connection, which is the
// exact failure a slate exists to prevent. ffmpeg.SlateArgs is built and
// tested; the tier that would drive it is not, and a setting for a feature the
// engine cannot honour would be worse than no setting at all.
type SynthSettings struct {
	// SilenceOnVideoOnly synthesises a silent stereo track when the ingest
	// probes with zero audio tracks.
	//
	// Default TRUE. A video-only stream is rejected by every major platform,
	// and without this a destination on one either refuses to compile or
	// crash-loops mapping an audio track that is not there — so the default
	// that "just works" is on. It can never affect an ingest that does carry
	// audio: the tier is started only on a probe that positively reported zero
	// tracks.
	SilenceOnVideoOnly bool `json:"silenceOnVideoOnly"`
}

// MeterSettings controls the audio-level sidecar.
type MeterSettings struct {
	Enabled bool `json:"enabled"`
	// IntervalMS is how often levels are pushed over the WebSocket.
	IntervalMS int `json:"intervalMs"`
}

// Settings is everything the user can change from the web UI.
type Settings struct {
	Ingest    IngestSettings    `json:"ingest"`
	Recording RecordingSettings `json:"recording"`
	Preview   PreviewSettings   `json:"preview"`
	Playout   PlayoutSettings   `json:"playout"`
	Synth     SynthSettings     `json:"synth"`
	Meters    MeterSettings     `json:"meters"`
	Logging   LoggingSettings   `json:"logging"`
}

// DefaultSettings is what a fresh install runs with.
func DefaultSettings() Settings {
	return Settings{
		Ingest: IngestSettings{
			Mode: IngestSRT,
			SRT:  SRTSettings{Port: 6000, LatencyMS: 200},
			RTMP: RTMPSettings{Port: 1935, App: "live", StreamKey: "stream"},
			Pull: PullSettings{
				ReconnectDelayMaxSeconds: ffmpeg.DefaultPullReconnectDelayMax,
				RTSPTransport:            ffmpeg.DefaultPullRTSPTransport,
			},
		},
		Recording: RecordingSettings{
			Enabled:        false,
			SegmentSeconds: 3600,
			MaxGB:          50,
			MaxAgeHours:    24 * 30,
			MinFreeGB:      5,
		},
		Preview: PreviewSettings{
			Enabled:            true,
			SegmentSeconds:     2,
			VideoHeight:        360,
			VideoKbps:          800,
			IdleTimeoutSeconds: 30,
		},
		// Off, public off, and one passthrough variant already described: a
		// fresh install can turn playout on without first designing a ladder,
		// and an upgrade of an existing install changes nothing until it does.
		Playout: PlayoutSettings{
			Enabled:            false,
			Public:             false,
			AllowCrossOrigin:   false,
			Format:             PlayoutHLS,
			SegmentSeconds:     4,
			PlaylistSegments:   6,
			DVRWindowSeconds:   0,
			MaxDiskMB:          2048,
			AudioKbps:          128,
			SessionIdleSeconds: 20,
			MaxSessions:        5000,
			Variants: []PlayoutVariant{
				{Name: "source", Enabled: true},
			},
		},
		Synth:   SynthSettings{SilenceOnVideoOnly: true},
		Meters:  MeterSettings{Enabled: true, IntervalMS: 100},
		Logging: LoggingSettings{PersistProcessLogs: true, MaxFileMB: 8, MaxFiles: 3},
	}
}

// Validate rejects settings that would produce a process that cannot start.
func (s Settings) Validate() error {
	var probs []string
	add := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }

	switch s.Ingest.Mode {
	case IngestSRT, IngestRTMP:
	case IngestPull:
		// The source only has to be dialable when it is actually the ingest;
		// a half-filled pull form must not block someone saving an SRT change.
		if err := ffmpeg.ValidatePullURL(s.Ingest.Pull.URL); err != nil {
			add("%v", err)
		}
	default:
		add("unknown ingest mode %q", s.Ingest.Mode)
	}
	for _, p := range s.Ingest.Pull.problems() {
		add("%s", p)
	}
	if s.Ingest.SRT.Port < 1 || s.Ingest.SRT.Port > 65535 {
		add("srt port %d out of range", s.Ingest.SRT.Port)
	}
	// SRT's own constraint, enforced here so the user sees it in a form field
	// rather than in an FFmpeg stderr line.
	if p := s.Ingest.SRT.Passphrase; p != "" && (len(p) < 10 || len(p) > 79) {
		add("srt passphrase must be 10-79 characters (got %d)", len(p))
	}
	if s.Ingest.SRT.LatencyMS < 20 || s.Ingest.SRT.LatencyMS > 8000 {
		add("srt latency %dms out of range (20-8000)", s.Ingest.SRT.LatencyMS)
	}
	if s.Ingest.RTMP.Port < 1 || s.Ingest.RTMP.Port > 65535 {
		add("rtmp port %d out of range", s.Ingest.RTMP.Port)
	}
	if s.Ingest.Mode == IngestRTMP && s.Ingest.RTMP.App == "" {
		add("rtmp app name is required")
	}
	if s.Ingest.SRT.Port == s.Ingest.RTMP.Port {
		add("srt and rtmp cannot share port %d", s.Ingest.SRT.Port)
	}
	if s.Recording.SegmentSeconds < 10 || s.Recording.SegmentSeconds > 24*3600 {
		add("recording segment length %ds out of range (10-86400)", s.Recording.SegmentSeconds)
	}
	if s.Recording.MaxGB < 0 {
		add("recording size cap cannot be negative")
	}
	if s.Recording.MaxAgeHours < 0 {
		add("recording age cap cannot be negative")
	}
	if s.Recording.MinFreeGB < 0 {
		add("recording free-space floor cannot be negative")
	}
	if s.Logging.MaxFileMB < 1 || s.Logging.MaxFileMB > 1024 {
		add("log file size %dMB out of range (1-1024)", s.Logging.MaxFileMB)
	}
	if s.Logging.MaxFiles < 1 || s.Logging.MaxFiles > 100 {
		add("log file count %d out of range (1-100)", s.Logging.MaxFiles)
	}
	if s.Preview.SegmentSeconds < 1 || s.Preview.SegmentSeconds > 10 {
		add("preview segment length %ds out of range (1-10)", s.Preview.SegmentSeconds)
	}
	if s.Preview.VideoHeight < 144 || s.Preview.VideoHeight > 1080 {
		add("preview height %d out of range (144-1080)", s.Preview.VideoHeight)
	}
	// Anything under a few seconds would tear the encoder down between a
	// player's own playlist polls.
	if t := s.Preview.IdleTimeoutSeconds; t != 0 && (t < 5 || t > 3600) {
		add("preview idle timeout %ds out of range (5-3600, or 0 for the default)", t)
	}
	for _, p := range s.Playout.problems() {
		add("%s", p)
	}
	if s.Meters.IntervalMS < 40 || s.Meters.IntervalMS > 2000 {
		add("meter interval %dms out of range (40-2000)", s.Meters.IntervalMS)
	}

	if len(probs) > 0 {
		return fmt.Errorf("invalid settings: %v", probs)
	}
	return nil
}

// GetSettings returns the stored settings, seeding defaults on first run.
func (d *DB) GetSettings() (Settings, error) {
	var raw string
	err := d.sql.QueryRow(`SELECT json FROM settings WHERE id = 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		s := DefaultSettings()
		return s, d.PutSettings(s)
	}
	if err != nil {
		return Settings{}, err
	}
	// Start from defaults so a settings blob written by an older build gains
	// sane values for fields it has never heard of.
	s := DefaultSettings()
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return Settings{}, fmt.Errorf("decode settings: %w", err)
	}
	return s, nil
}

// PutSettings stores the settings blob.
func (d *DB) PutSettings(s Settings) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(
		`INSERT INTO settings (id, json) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET json = excluded.json`,
		string(b))
	return err
}
