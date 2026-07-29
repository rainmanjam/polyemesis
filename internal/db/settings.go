package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/routing"
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

// SRTSettings configures one source's SRT ingest.
//
// There is no port here. Every source is reached on the ONE SRT listener and
// told apart by its token, so a port per source is neither needed nor offered.
// See docs/DESIGN-ONE-PORT-ONLY.md.
type SRTSettings struct {
	// Passphrase enables AES encryption. SRT requires 10..79 characters.
	Passphrase string `json:"passphrase"`
	// LatencyMS is SRT's receive buffer, in milliseconds. Higher survives
	// worse networks at the cost of glass-to-glass delay.
	LatencyMS int `json:"latencyMs"`
}

// RTMPSettings configures the fallback RTMP ingest.
//
// No port, for the same reason as SRT — but for a different mechanism. RTMP has
// no token routing here, so the single RTMP listener serves at most ONE source;
// a second is refused rather than left to fight over the socket.
type RTMPSettings struct {
	App string `json:"app"`
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
	// Annotations describe what each incoming audio track IS — mic, music,
	// commentary, its language — and they live here rather than on a
	// destination because they are a property of the feed, not of anyone
	// listening to it. Every destination compiles against the same set.
	//
	// omitempty so a settings blob written before roles existed round-trips
	// byte-identically.
	Annotations []routing.TrackAnnotation `json:"annotations,omitempty"`
}

// problems reports everything wrong with one ingest configuration.
//
// Extracted from Settings.Validate when sources arrived: an ingest block is now
// validated in two places -- inside the settings singleton, and on each row of
// the sources table -- and two copies of these rules would drift. The first
// divergence would be a source accepting an SRT passphrase that settings
// rejects, which surfaces as a child process that will not start rather than as
// a form error.
func (i IngestSettings) problems() []string {
	var probs []string
	add := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }

	switch i.Mode {
	case IngestSRT, IngestRTMP:
	case IngestPull:
		// The source only has to be dialable when it is actually the ingest;
		// a half-filled pull form must not block someone saving an SRT change.
		if err := ffmpeg.ValidatePullURL(i.Pull.URL); err != nil {
			add("%v", err)
		}
	default:
		add("unknown ingest mode %q", i.Mode)
	}
	probs = append(probs, i.Pull.problems()...)
	// Track roles are the operator's description of the feed. An invalid one
	// would compile into a graph nobody asked for, so it is caught here rather
	// than by a destination that will not start.
	if err := routing.ValidateAnnotations(i.Annotations); err != nil {
		add("%v", err)
	}
	// SRT's own constraint, enforced here so the user sees it in a form field
	// rather than in an FFmpeg stderr line.
	if p := i.SRT.Passphrase; p != "" && (len(p) < 10 || len(p) > 79) {
		add("srt passphrase must be 10-79 characters (got %d)", len(p))
	}
	if i.SRT.LatencyMS < 20 || i.SRT.LatencyMS > 8000 {
		add("srt latency %dms out of range (20-8000)", i.SRT.LatencyMS)
	}
	if i.Mode == IngestRTMP && i.RTMP.App == "" {
		add("rtmp app name is required")
	}
	return probs
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
	// Stems ALSO writes every ingest audio track to its own file — mic.flac,
	// music.flac, game.flac — beside the multitrack master, from the same
	// process so they stay sample-aligned with each other. It is what makes
	// polyemesis a multitrack field recorder as well as a restreamer.
	//
	// Default FALSE, and it must stay false: it multiplies what a session
	// writes by roughly the track count, which is a decision about someone's
	// disk rather than a default anyone should inherit from an upgrade.
	Stems bool `json:"stems"`
	// StemCodec is what those files are written as. Both choices are lossless;
	// FLAC is the default because it is about half the size, and WAV exists for
	// the one tool in the chain that still cannot open a FLAC. Empty means
	// FLAC, so a settings blob that predates stems needs no migration.
	StemCodec ffmpeg.StemCodec `json:"stemCodec"`
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

// ----------------------------------------------------------------- failover

// FailoverReturn decides what happens when the primary ingest comes back.
type FailoverReturn string

const (
	// FailoverReturnManual leaves the backup on air until an operator says
	// otherwise. It is the default because an encoder that dropped once usually
	// drops again, and an automatic return turns that into a broadcast that
	// flaps between two sources — each flap a visible cut for every viewer.
	FailoverReturnManual FailoverReturn = "manual"
	// FailoverReturnAuto goes back to the primary once it has been delivering
	// steadily for ReturnStableSeconds.
	FailoverReturnAuto FailoverReturn = "auto"
)

// Failover bounds. Wide enough to be an opinion about units rather than about
// how someone runs their show.
const (
	MinFailoverGraceSeconds = 1
	MaxFailoverGraceSeconds = 300
	MaxFailoverReturnStable = 3600
	// MaxSlateImagePath keeps the stored path to something a filesystem and a
	// form field can both hold.
	MaxSlateImagePath = 512
)

// BackupIngestSettings is the second listener, running alongside the primary on
// its own port.
//
// It is a full ingest configuration rather than "the primary with a different
// port" on purpose: the redundant path is usually a different box on a
// different network, and the encoder that feeds it is often a different model
// that can only speak RTMP. Forcing it to mirror the primary's protocol would
// make the feature useless in exactly the case it exists for.
type BackupIngestSettings struct {
	Enabled bool         `json:"enabled"`
	Mode    IngestMode   `json:"mode"`
	SRT     SRTSettings  `json:"srt"`
	RTMP    RTMPSettings `json:"rtmp"`
	Pull    PullSettings `json:"pull"`
}

// SlateSettings is the standby picture published while no ingest is delivering.
//
// There is deliberately no width, height or frame rate here. The slate has to
// match the departed ingest closely enough that a `-c:v copy` destination does
// not choke on the change, and the only thing that knows what the ingest was is
// the probe — an operator typing 1080p into a form while their camera sends
// 720p would produce exactly the silent corruption this feature must not cause.
type SlateSettings struct {
	Enabled bool `json:"enabled"`
	// ImagePath is a still image, relative to the data directory and confined
	// there the same way a file:// pull source is. Empty paints Color instead,
	// and that is the fallback precisely because a flat colour has no file to
	// fail to open.
	ImagePath string `json:"imagePath"`
	// Color is any spelling FFmpeg's colour parser accepts. Empty means black.
	Color string `json:"color"`
	// VideoKbps is a floor, not a budget: a static frame needs almost nothing,
	// but platforms watch for a bitrate before they call a stream unhealthy.
	VideoKbps int `json:"videoKbps"`
	// Encoder is empty for libx264, which is the right default even on a box
	// full of hardware: a static frame costs a software encoder nothing, and the
	// one job the slate has is to start when everything else has already failed.
	Encoder VideoEncoder `json:"encoder"`
	Preset  string       `json:"preset"`
}

// FailoverSettings turns on the source-selector tier: a permanent relay between
// the ingest and everything downstream, fed by whichever source is currently
// live.
//
// Default OFF, and that is not timidity. With it off the pipeline is
// byte-for-byte what it was before this feature existed — destinations
// subscribe straight to the ingest (or the silence tier) and no extra process
// runs. With it on there is one more remux hop, which is cheap but not free,
// and it is a decision rather than something an upgrade should make for you.
type FailoverSettings struct {
	Enabled bool `json:"enabled"`
	// GraceSeconds is how long the current source may deliver nothing before
	// the selector switches away from it. Too low and a network hiccup becomes
	// a cut; too high and the platform sees a stall before the slate arrives.
	GraceSeconds int            `json:"graceSeconds"`
	Return       FailoverReturn `json:"return"`
	// ReturnStableSeconds is how long the primary must deliver continuously
	// before an automatic return trusts it. Ignored in manual mode.
	ReturnStableSeconds int                  `json:"returnStableSeconds"`
	Backup              BackupIngestSettings `json:"backup"`
	Slate               SlateSettings        `json:"slate"`
}

// SlateImageProblem reports why the configured still cannot be used, or nil.
//
// Same confinement as a file:// pull source, and for the same reason: the path
// is operator input that becomes an FFmpeg argument, and an absolute path here
// would be a read primitive for whoever reaches the settings API.
func (s SlateSettings) SlateImageProblem() error {
	p := strings.TrimSpace(s.ImagePath)
	if p == "" {
		return nil
	}
	if len(p) > MaxSlateImagePath {
		return fmt.Errorf("slate image path is longer than %d characters", MaxSlateImagePath)
	}
	if strings.ContainsAny(p, "\x00\n\r") {
		return errors.New("slate image path contains control characters")
	}
	// Backslashes are separators on Windows, so normalise before the traversal
	// check or "..\..\secret.key" walks straight past it.
	rel := strings.ReplaceAll(p, `\`, "/")
	switch {
	case strings.HasPrefix(rel, "/"), strings.Contains(rel, ".."),
		len(rel) > 1 && rel[1] == ':':
		return errors.New("slate image must be a relative path inside the data directory")
	}
	return nil
}

// problems reports the failover configuration that would produce a process that
// cannot start.
//
// Ranges are checked whether or not the feature is enabled, so a half-filled
// form is caught when it is saved.
func (f FailoverSettings) problems(primary IngestSettings) []string {
	var probs []string
	add := func(fs string, a ...any) { probs = append(probs, fmt.Sprintf(fs, a...)) }

	if g := f.GraceSeconds; g < MinFailoverGraceSeconds || g > MaxFailoverGraceSeconds {
		add("failover grace period %ds out of range (%d-%d)", g, MinFailoverGraceSeconds, MaxFailoverGraceSeconds)
	}
	switch f.Return {
	case FailoverReturnManual, FailoverReturnAuto:
	default:
		add("unknown failover return mode %q (manual, auto)", f.Return)
	}
	if t := f.ReturnStableSeconds; t < 0 || t > MaxFailoverReturnStable {
		add("failover return delay %ds out of range (0-%d)", t, MaxFailoverReturnStable)
	}

	b := f.Backup
	switch b.Mode {
	case IngestSRT, IngestRTMP:
	case IngestPull:
		if b.Enabled {
			if err := ffmpeg.ValidatePullURL(b.Pull.URL); err != nil {
				add("backup ingest: %v", err)
			}
		}
	default:
		add("unknown backup ingest mode %q", b.Mode)
	}
	for _, p := range b.Pull.problems() {
		add("backup ingest: %s", p)
	}
	if p := b.SRT.Passphrase; p != "" && (len(p) < 10 || len(p) > 79) {
		add("backup srt passphrase must be 10-79 characters (got %d)", len(p))
	}
	if b.SRT.LatencyMS < 20 || b.SRT.LatencyMS > 8000 {
		add("backup srt latency %dms out of range (20-8000)", b.SRT.LatencyMS)
	}
	if b.Mode == IngestRTMP && b.RTMP.App == "" {
		add("backup rtmp app name is required")
	}
	// No port collision to check any more. Primary and backup both arrive on
	// the one SRT listener and are told apart by token, so "which socket does
	// each bind" is no longer a question that can have a wrong answer.
	//
	// The RTMP backup is the exception: it would need the single RTMP listener
	// that the primary may already hold. Refused here rather than at runtime.
	if f.Enabled && b.Enabled && b.Mode == IngestRTMP && primary.Mode == IngestRTMP {
		add("the backup ingest cannot also use RTMP: there is one RTMP listener " +
			"and the primary has it. Use SRT for the backup, which is addressed by token")
	}
	if err := f.Slate.SlateImageProblem(); err != nil {
		add("%v", err)
	}
	if k := f.Slate.VideoKbps; k < 0 || k > 100_000 {
		add("slate bitrate %d kbps out of range (0-100000, 0 for the default)", k)
	}
	return probs
}

// SynthSettings controls the synthetic sources.
//
// Only silence is here. The slate lives in FailoverSettings, because a standby
// picture and a backup ingest are the same piece of work: both need the
// permanent source-selector tier, and both are just another answer to "what is
// feeding the hub the destinations already subscribe to".
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

// ------------------------------------------------------------------- mqtt

// MQTT bounds. The interval floor is 1s because the underlying state is
// republished only on change, so a fast tick costs a comparison rather than a
// message; the ceiling is an hour because past that a retained topic is a
// historical record rather than telemetry.
const (
	MinMQTTIntervalSeconds = 1
	MaxMQTTIntervalSeconds = 3600
	MaxMQTTPrefixLength    = 128
	MaxMQTTInstanceLength  = 64
	MaxMQTTClientIDLength  = 128
)

// MQTTSettings publishes retained telemetry to a broker.
//
// The password is deliberately NOT here. It lives sealed in its own table, the
// same way an OAuth client secret does, because this struct is marshalled into
// the settings blob and served to the settings page. See DB.PutMQTTPassword.
type MQTTSettings struct {
	Enabled bool `json:"enabled"`
	// BrokerURL is mqtt://, mqtts://, ws:// or wss://. Credentials in the URL
	// are refused rather than accepted: a password in a URL reaches logs, `ps`
	// output and error strings, and there is no taking it back afterwards.
	BrokerURL string `json:"brokerUrl"`
	Username  string `json:"username"`
	// HasPassword reports that a sealed password exists, so the settings page
	// can show that one is set without ever receiving it.
	HasPassword bool `json:"hasPassword"`
	// Prefix roots the topic tree. Separators are preserved, not slugged: an
	// operator who writes `home/av` means two levels.
	Prefix string `json:"prefix"`
	// Instance distinguishes two polyemesis installs sharing one broker, and is
	// what a Home Assistant device is keyed on. Slugged before use.
	Instance string `json:"instance"`
	// ClientID must be unique on the broker. A collision is the number-one
	// cause of an unexplained reconnect loop: the broker disconnects the older
	// session on every connect and both clients reconnect forever. Empty means
	// "derive one from Instance", which is unique for the same reason Instance
	// is.
	ClientID       string `json:"clientId"`
	IntervalSecond int    `json:"intervalSeconds"`
	KeepAliveSec   int    `json:"keepAliveSeconds"`
	// TLSSkipVerify accepts a self-signed broker certificate. Named for what it
	// does rather than for what an operator wishes it did.
	TLSSkipVerify bool `json:"tlsSkipVerify"`
	// Discovery publishes Home Assistant device-discovery payloads. Separate
	// from Enabled because a Node-RED or Telegraf consumer wants the telemetry
	// and not the discovery topics.
	Discovery bool `json:"discovery"`
}

// problems reports everything wrong with the MQTT block.
//
// Nothing here is checked when MQTT is switched off. A half-configured block an
// operator is still filling in must not block saving an unrelated setting, and
// a disabled publisher cannot misbehave.
func (m MQTTSettings) problems() []string {
	if !m.Enabled {
		return nil
	}
	var probs []string
	add := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }

	switch u, err := url.Parse(strings.TrimSpace(m.BrokerURL)); {
	case strings.TrimSpace(m.BrokerURL) == "":
		add("mqtt is enabled but no broker URL is set")
	case err != nil:
		add("mqtt broker URL is unparseable: %v", err)
	case u.Host == "":
		add("mqtt broker URL %q has no host", m.BrokerURL)
	case u.User != nil:
		// Refused rather than quietly moved into the username and password
		// fields, because the operator needs to know the URL they pasted would
		// have been written to a log.
		add("mqtt broker URL carries credentials; put the username and password in their own fields so the password is sealed and never logged")
	default:
		switch u.Scheme {
		case "mqtt", "tcp", "mqtts", "ssl", "ws", "wss":
		default:
			add("mqtt broker scheme %q is not one of mqtt, mqtts, ws or wss", u.Scheme)
		}
	}

	// A `$` prefix is refused rather than escaped. Brokers reserve those
	// topics, and a subscriber using `#` -- which is what anyone debugging
	// reaches for first -- is specified never to receive them, so the telemetry
	// would publish successfully and be invisible in exactly the view the
	// operator would use to look for it.
	if strings.HasPrefix(strings.TrimSpace(m.Prefix), "$") {
		add("mqtt topic prefix must not begin with $: brokers reserve those topics and a '#' subscription never receives them")
	}
	if strings.ContainsAny(m.Prefix, "+#\x00") {
		add("mqtt topic prefix %q contains a wildcard or NUL, which are legal in a subscription filter but not in a published topic", m.Prefix)
	}
	if len(m.Prefix) > MaxMQTTPrefixLength {
		add("mqtt topic prefix is %d characters (maximum %d)", len(m.Prefix), MaxMQTTPrefixLength)
	}
	if len(m.Instance) > MaxMQTTInstanceLength {
		add("mqtt instance name is %d characters (maximum %d)", len(m.Instance), MaxMQTTInstanceLength)
	}
	if len(m.ClientID) > MaxMQTTClientIDLength {
		add("mqtt client id is %d characters (maximum %d)", len(m.ClientID), MaxMQTTClientIDLength)
	}
	if m.IntervalSecond < MinMQTTIntervalSeconds || m.IntervalSecond > MaxMQTTIntervalSeconds {
		add("mqtt publish interval %ds out of range (%d-%d)",
			m.IntervalSecond, MinMQTTIntervalSeconds, MaxMQTTIntervalSeconds)
	}
	// The MQTT keep-alive is a 16-bit field. 0 is legal on the wire and means
	// "no keep-alive", which would leave a half-open connection looking healthy
	// forever and the will message -- the entire availability story -- never
	// firing.
	if m.KeepAliveSec < 1 || m.KeepAliveSec > 65535 {
		add("mqtt keep-alive %ds out of range (1-65535); 0 disables the liveness check the will message depends on", m.KeepAliveSec)
	}
	return probs
}

// ---------------------------------------------------------- post-production

// Post-production bounds. Wide on purpose: they catch a unit mix-up or a typo,
// not an opinion about how somebody runs their box.
const (
	MaxPostProdConcurrency = 16
	MaxPostProdKinds       = 64
	MaxPostProdRetainDays  = 3650
	MaxPostProdRetainJobs  = 100_000
)

// PostProdKindSettings overrides how one kind of background work is governed.
//
// Kind is a free string because the queue never interprets one either: a
// processor registers whatever constant it likes and this block has to be able
// to name it without db knowing the processor exists.
type PostProdKindSettings struct {
	Kind string `json:"kind"`
	// Mode is realtime, deferred, scheduled or manual. Empty inherits
	// DefaultMode, so a row that only sets UsesGPU is legal.
	Mode string `json:"mode,omitempty"`
	// Windows are the local time ranges a scheduled kind may run in.
	Windows []jobs.Window `json:"windows,omitempty"`
	// UsesGPU marks work that would compete with a GPU-accelerated rendition
	// encoder, and is the only work the GPU gate applies to.
	UsesGPU bool `json:"usesGpu,omitempty"`
	// IgnoreIngest exempts cheap work from the yield-to-the-stream gate without
	// promoting it to realtime.
	IgnoreIngest bool `json:"ignoreIngest,omitempty"`
}

// PostProdSettings is the resource policy for the background job queue: the
// answer to "may heavy work have the machine right now".
//
// The defaults are the governing principle of the whole tier written down.
// Yielding to the stream is on, every kind is deferred, one job at a time, and
// every heavy child is niced — so an operator who never opens this page still
// gets a box that will not drop a frame for a transcript.
type PostProdSettings struct {
	// Enabled false makes the governor inert: it releases whatever it was
	// holding and gates nothing. Jobs still queue and still run; they simply
	// stop yielding, which is a decision an operator with a dedicated encoder
	// box is entitled to make.
	Enabled bool `json:"enabled"`
	// Concurrency is how many jobs may run at once, across every kind. One,
	// because a second transcode buys throughput nobody asked for at a risk
	// nobody accepted.
	Concurrency int `json:"concurrency"`
	// DefaultMode governs a kind with no row of its own.
	DefaultMode string `json:"defaultMode"`
	// YieldToStream is the default and most important gate: a live ingest holds
	// back every deferred kind.
	YieldToStream bool `json:"yieldToStream"`

	// CPUCeilingPercent is the host CPU level above which nothing new starts.
	// 0 disables the gate.
	CPUCeilingPercent int `json:"cpuCeilingPercent"`
	// CPUResumePercent is where it releases. It must sit below the ceiling; the
	// gap is the hysteresis that stops a load on the threshold oscillating.
	CPUResumePercent int `json:"cpuResumePercent"`
	// CPUSustainedSeconds is how long the ceiling must be held before RUNNING
	// work is suspended as well as held back.
	CPUSustainedSeconds int `json:"cpuSustainedSeconds"`
	// CPUSettleSeconds is how long it must be calm again before running work is
	// released.
	CPUSettleSeconds int `json:"cpuSettleSeconds"`

	// AvoidGPUWhenStreaming applies the GPU gate to kinds marked UsesGPU.
	AvoidGPUWhenStreaming bool `json:"avoidGpuWhenStreaming"`
	// GPUBusy is the manual "the GPU is in use by streaming" switch. It exists
	// because GPU contention is close to undetectable on every platform we run
	// on, and an operator who knows their ladder is on NVENC can say so instead
	// of having us guess — a guess of "free" being the one that hurts the
	// broadcast.
	GPUBusy bool `json:"gpuBusy"`

	// BatteryFloorPercent holds deferred work back on a discharging laptop
	// below this level. 0 disables it. Best effort: on a platform whose power
	// state we cannot read, nothing is gated.
	BatteryFloorPercent int `json:"batteryFloorPercent"`
	// ThermalCeilingC stops everything, realtime included, because a CPU that
	// is thermally throttling has already begun degrading the stream. 0
	// disables it, and it is likewise gated on being able to read a sensor.
	ThermalCeilingC int `json:"thermalCeilingC"`

	// NiceLevel is the OS priority heavy children start at, 0..19. It applies
	// regardless of every other policy, which is why it is cheap insurance
	// rather than a gate.
	NiceLevel int `json:"niceLevel"`
	// IdleIO additionally drops those children to the idle IO class where
	// ionice exists, so a transcode reading a recording loses the disk to the
	// recorder writing the next segment.
	IdleIO bool `json:"idleIo"`

	// IngestLingerSeconds keeps the stream gate closed after the ingest stops,
	// so a reconnect is not raced by a transcode pouncing on the gap.
	IngestLingerSeconds int `json:"ingestLingerSeconds"`
	// DeferSeconds is how far ahead blocked work is parked before the governor
	// reconsiders it. Short, because the deferral is renewed while the block
	// lasts and a governor that dies must leave work that comes back on its own.
	DeferSeconds int `json:"deferSeconds"`

	// RetainDays and RetainJobs bound the finished-job history. A job row is
	// tiny, but "tiny forever" is still a leak.
	RetainDays int `json:"retainDays"`
	RetainJobs int `json:"retainJobs"`

	// Kinds are the per-kind overrides.
	Kinds []PostProdKindSettings `json:"kinds,omitempty"`
}

// Policy converts the stored settings into the governor's own policy, which is
// the only place these numbers mean anything. Durations are stored as seconds
// because that is what a form field holds.
func (p PostProdSettings) Policy() jobs.Policy {
	mode := jobs.Mode(p.DefaultMode)
	if !mode.Valid() {
		mode = jobs.DefaultMode
	}
	pol := jobs.Policy{
		Enabled:       p.Enabled,
		YieldToStream: p.YieldToStream,
		Default:       jobs.KindPolicy{Mode: mode},
		Kinds:         make(map[jobs.Kind]jobs.KindPolicy, len(p.Kinds)),
		CPU: jobs.CPUPolicy{
			CeilingPercent: float64(p.CPUCeilingPercent),
			ResumePercent:  float64(p.CPUResumePercent),
			Sustained:      time.Duration(p.CPUSustainedSeconds) * time.Second,
			Settle:         time.Duration(p.CPUSettleSeconds) * time.Second,
		},
		GPU: jobs.GPUPolicy{AvoidWhenStreaming: p.AvoidGPUWhenStreaming, Busy: p.GPUBusy},
		Power: jobs.PowerPolicy{
			BatteryFloorPercent: float64(p.BatteryFloorPercent),
			ThermalCeilingC:     float64(p.ThermalCeilingC),
		},
		NiceLevel:    p.NiceLevel,
		IdleIO:       p.IdleIO,
		DeferFor:     time.Duration(p.DeferSeconds) * time.Second,
		IngestLinger: time.Duration(p.IngestLingerSeconds) * time.Second,
	}
	for _, k := range p.Kinds {
		name := jobs.Kind(strings.TrimSpace(k.Kind))
		if name == "" {
			continue
		}
		km := jobs.Mode(k.Mode)
		if !km.Valid() {
			km = mode
		}
		pol.Kinds[name] = jobs.KindPolicy{
			Mode:         km,
			Windows:      k.Windows,
			UsesGPU:      k.UsesGPU,
			IgnoreIngest: k.IgnoreIngest,
		}
	}
	// Normalized fills what a half-filled form left out, so a settings blob
	// written before a field existed still produces a policy that evaluates.
	return pol.Normalized()
}

// problems reports the post-production policy that could not be evaluated.
//
// Everything is checked whether or not the governor is enabled, so a
// half-filled form is caught when it is saved rather than when the machine
// gets busy.
func (p PostProdSettings) problems() []string {
	var probs []string
	add := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }

	if n := p.Concurrency; n < 1 || n > MaxPostProdConcurrency {
		add("job concurrency %d out of range (1-%d)", n, MaxPostProdConcurrency)
	}
	// Empty is accepted as "the default" so a client that predates this block
	// can still save the rest of the settings.
	if m := p.DefaultMode; m != "" && !jobs.Mode(m).Valid() {
		add("unknown job mode %q (realtime, deferred, scheduled, manual)", m)
	}
	if c := p.CPUCeilingPercent; c < 0 || c > 100 {
		add("cpu ceiling %d%% out of range (0-100, 0 to disable)", c)
	}
	if r := p.CPUResumePercent; r < 0 || r > 100 {
		add("cpu resume level %d%% out of range (0-100)", r)
	}
	if p.CPUCeilingPercent > 0 && p.CPUResumePercent >= p.CPUCeilingPercent {
		add("cpu resume level %d%% must be below the ceiling %d%%", p.CPUResumePercent, p.CPUCeilingPercent)
	}
	if s := p.CPUSustainedSeconds; s < 0 || s > 3600 {
		add("cpu sustained window %ds out of range (0-3600, 0 for the default)", s)
	}
	if s := p.CPUSettleSeconds; s < 0 || s > 3600 {
		add("cpu settle window %ds out of range (0-3600, 0 for the default)", s)
	}
	if b := p.BatteryFloorPercent; b < 0 || b > 100 {
		add("battery floor %d%% out of range (0-100, 0 to disable)", b)
	}
	if t := p.ThermalCeilingC; t < 0 || t > 150 {
		add("thermal ceiling %d°C out of range (0-150, 0 to disable)", t)
	}
	if n := p.NiceLevel; n < 0 || n > jobs.MaxNiceLevel {
		add("nice level %d out of range (0-%d)", n, jobs.MaxNiceLevel)
	}
	if s := p.IngestLingerSeconds; s < 0 || s > 3600 {
		add("ingest linger %ds out of range (0-3600)", s)
	}
	if s := p.DeferSeconds; s < 0 || s > 3600 {
		add("job deferral %ds out of range (0-3600, 0 for the default)", s)
	}
	if d := p.RetainDays; d < 0 || d > MaxPostProdRetainDays {
		add("job history retention %d days out of range (0-%d, 0 to keep forever)", d, MaxPostProdRetainDays)
	}
	if n := p.RetainJobs; n < 0 || n > MaxPostProdRetainJobs {
		add("job history floor %d out of range (0-%d)", n, MaxPostProdRetainJobs)
	}
	if len(p.Kinds) > MaxPostProdKinds {
		add("post-production policy names %d job kinds (maximum %d)", len(p.Kinds), MaxPostProdKinds)
	}

	seen := map[string]bool{}
	for _, k := range p.Kinds {
		name := strings.TrimSpace(k.Kind)
		switch {
		case name == "":
			add("a post-production policy row has no job kind")
			continue
		case len(name) > jobs.MaxKindLen:
			add("job kind %q is longer than %d characters", name, jobs.MaxKindLen)
			continue
		// Two rows for one kind would silently pick whichever the map iteration
		// landed on last.
		case seen[name]:
			add("duplicate post-production policy for job kind %q", name)
			continue
		}
		seen[name] = true

		if k.Mode != "" && !jobs.Mode(k.Mode).Valid() {
			add("job kind %q has an unknown mode %q", name, k.Mode)
		}
		if len(k.Windows) > jobs.MaxWindows {
			add("job kind %q has %d windows (maximum %d)", name, len(k.Windows), jobs.MaxWindows)
		}
		for _, w := range k.Windows {
			if err := w.Validate(); err != nil {
				add("job kind %q: %v", name, err)
			}
		}
	}
	return probs
}

// ListenerSettings is where the server actually binds. Install-wide, because
// there is exactly one of each listener however many sources exist.
//
// This replaced a per-source port on every source plus an opt-in "shared"
// listener alongside them. The opt-in shipped OFF, defaulted to a port no
// compose file published and no document mentioned, and reported itself as
// enforcing while bound to something unreachable. Deleting the choice deleted
// the trap: there is one SRT port, sources are told apart by token, and adding
// a source never changes what has to be published.
//
// docs/DESIGN-ONE-PORT-ONLY.md has the reasoning, including what it costs.
type ListenerSettings struct {
	// SRTPort serves EVERY source, demultiplexed by publish token.
	SRTPort int `json:"srtPort"`
	// RTMPPort serves at most one source. RTMP has no token routing here, so
	// a second RTMP source is refused at validation rather than left to
	// discover at runtime that it never receives anything.
	RTMPPort int `json:"rtmpPort"`
}

func (l ListenerSettings) problems() []string {
	var probs []string
	if l.SRTPort < 1 || l.SRTPort > 65535 {
		probs = append(probs, fmt.Sprintf("srt listener port %d out of range", l.SRTPort))
	}
	if l.RTMPPort < 1 || l.RTMPPort > 65535 {
		probs = append(probs, fmt.Sprintf("rtmp listener port %d out of range", l.RTMPPort))
	}
	if l.SRTPort == l.RTMPPort {
		probs = append(probs, fmt.Sprintf("srt and rtmp listeners cannot share port %d", l.SRTPort))
	}
	return probs
}

// Settings is everything the user can change from the web UI.
type Settings struct {
	Ingest IngestSettings `json:"ingest"`
	// Listeners is install-wide rather than per-source: it is one socket
	// for every programme, so it cannot live on a source row.
	Listeners ListenerSettings  `json:"listeners"`
	Recording RecordingSettings `json:"recording"`
	Preview   PreviewSettings   `json:"preview"`
	Playout   PlayoutSettings   `json:"playout"`
	Failover  FailoverSettings  `json:"failover"`
	Synth     SynthSettings     `json:"synth"`
	Meters    MeterSettings     `json:"meters"`
	Logging   LoggingSettings   `json:"logging"`
	PostProd  PostProdSettings  `json:"postProd"`
	MQTT      MQTTSettings      `json:"mqtt"`
}

// DefaultSettings is what a fresh install runs with.
func DefaultSettings() Settings {
	return Settings{
		Ingest: IngestSettings{
			Mode: IngestSRT,
			SRT:  SRTSettings{LatencyMS: 200},
			RTMP: RTMPSettings{App: "live", StreamKey: "stream"},
			Pull: PullSettings{
				ReconnectDelayMaxSeconds: ffmpeg.DefaultPullReconnectDelayMax,
				RTSPTransport:            ffmpeg.DefaultPullRTSPTransport,
			},
		},
		// The ports every install publishes. Not configurable per source.
		Listeners: ListenerSettings{SRTPort: 6000, RTMPPort: 1935},
		Recording: RecordingSettings{
			Enabled:        false,
			SegmentSeconds: 3600,
			MaxGB:          50,
			MaxAgeHours:    24 * 30,
			MinFreeGB:      5,
			Stems:          false,
			StemCodec:      ffmpeg.DefaultStemCodec,
		},
		Preview: PreviewSettings{
			Enabled: true,
			// One second, not two. The player holds back
			// liveSyncDurationCount (2) x the target duration, so the segment
			// length is multiplied on its way to the screen: halving it takes
			// roughly 2.5s off the preview, measured, for +0.9% bytes and no
			// measurable quality cost (PSNR at 360p/800k was marginally HIGHER
			// with the shorter GOP).
			//
			// This is the DEFAULT only. An operator who has stored a value
			// keeps it -- see docs/roadmap/LL-HLS.md.
			SegmentSeconds:     1,
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
		// Off, so an upgrade changes nothing at all, but described in full so
		// that turning it on is one switch rather than a form to design. The
		// backup listener sits one port above the primary's, the slate is
		// already enabled inside it, and the return is manual — which is the
		// choice that cannot surprise anyone mid-broadcast.
		Failover: FailoverSettings{
			Enabled:             false,
			GraceSeconds:        5,
			Return:              FailoverReturnManual,
			ReturnStableSeconds: 30,
			Backup: BackupIngestSettings{
				Enabled: false,
				Mode:    IngestSRT,
				SRT:     SRTSettings{LatencyMS: 200},
				RTMP:    RTMPSettings{App: "live", StreamKey: "backup"},
				Pull: PullSettings{
					ReconnectDelayMaxSeconds: ffmpeg.DefaultPullReconnectDelayMax,
					RTSPTransport:            ffmpeg.DefaultPullRTSPTransport,
				},
			},
			Slate: SlateSettings{Enabled: true, Color: "black", VideoKbps: 2000},
		},
		Synth:   SynthSettings{SilenceOnVideoOnly: true},
		Meters:  MeterSettings{Enabled: true, IntervalMS: 100},
		Logging: LoggingSettings{PersistProcessLogs: true, MaxFileMB: 8, MaxFiles: 3},
		// The governing principle as a default: yield to the stream, one job at
		// a time, everything deferred, every heavy child niced. An operator who
		// never opens this page still gets a box that will not drop a frame for
		// a transcript.
		PostProd: PostProdSettings{
			Enabled:               true,
			Concurrency:           jobs.DefaultConcurrency,
			DefaultMode:           string(jobs.DefaultMode),
			YieldToStream:         true,
			CPUCeilingPercent:     jobs.DefaultCPUCeilingPercent,
			CPUResumePercent:      jobs.DefaultCPUResumePercent,
			CPUSustainedSeconds:   int(jobs.DefaultCPUSustained / time.Second),
			CPUSettleSeconds:      int(jobs.DefaultCPUSettle / time.Second),
			AvoidGPUWhenStreaming: true,
			GPUBusy:               false,
			BatteryFloorPercent:   jobs.DefaultBatteryFloorPercent,
			ThermalCeilingC:       jobs.DefaultThermalCeilingC,
			NiceLevel:             jobs.DefaultNiceLevel,
			IdleIO:                true,
			IngestLingerSeconds:   int(jobs.DefaultIngestLinger / time.Second),
			DeferSeconds:          int(jobs.DefaultDeferFor / time.Second),
			RetainDays:            30,
			RetainJobs:            200,
		},
		// Off, and pre-filled with values that work the moment it is switched
		// on. An operator who enables MQTT should have to supply a broker URL
		// and nothing else.
		//
		// The literals below duplicate mqtt.DefaultPrefix and
		// mqtt.DefaultKeepAliveSec on purpose: importing internal/mqtt here
		// would link paho into the database layer, which has no business
		// speaking a network protocol. TestMQTTDefaultsMatchTheMQTTPackage is a
		// test-only import that keeps the two in step.
		MQTT: MQTTSettings{
			Enabled:        false,
			Prefix:         "polyemesis",
			Instance:       "polyemesis",
			IntervalSecond: 10,
			KeepAliveSec:   30,
			Discovery:      true,
		},
	}
}

// Validate rejects settings that would produce a process that cannot start.
func (s Settings) Validate() error {
	var probs []string
	add := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }

	for _, p := range s.Ingest.problems() {
		add("%s", p)
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
	// Empty is deliberately accepted as "the default": a client that has never
	// heard of stems must be able to save the rest of the recording settings.
	if !ffmpeg.ValidStemCodec(s.Recording.StemCodec) {
		add("unknown stem codec %q (flac, wav)", s.Recording.StemCodec)
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
	for _, p := range s.Failover.problems(s.Ingest) {
		add("%s", p)
	}
	if s.Meters.IntervalMS < 40 || s.Meters.IntervalMS > 2000 {
		add("meter interval %dms out of range (40-2000)", s.Meters.IntervalMS)
	}
	for _, p := range s.PostProd.problems() {
		add("%s", p)
	}
	for _, p := range s.Listeners.problems() {
		add("%s", p)
	}
	for _, p := range s.MQTT.problems() {
		add("%s", p)
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
