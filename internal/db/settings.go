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
	// IngestUnset is a fresh install that has not been told how encoders will
	// reach it yet.
	//
	// It exists so nothing is chosen on an operator's behalf. The two real
	// options are not interchangeable and the difference is not recoverable by
	// guessing. Both are now one port for any number of sources, addressed by
	// the same publish token, so the count is no longer what separates them —
	// TRACKS are: RTMP delivers a single audio track on any FFmpeg below 7.1,
	// which includes the stock build on Ubuntu 24.04, while SRT carries all of
	// them. Defaulting to either one silently hands a share of installs the
	// wrong thing, and the RTMP failure is invisible: the stream works, and one
	// of the six tracks arrives.
	//
	// The zero value on purpose, so a settings blob that has never been through
	// the first-run choice reads as unset rather than as a real mode.
	IngestUnset IngestMode = ""
	// IngestSRT is the primary path: MPEG-TS over SRT. MPEG-TS imposes no
	// track limit of its own, so the ceiling is routing.MaxTracks.
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

// RTMPSettings configures the RTMP ingest.
//
// No port, and now for the SAME reason as SRT rather than a different one:
// every source is reached on the ONE RTMP listener and told apart by its
// publish token, carried in the URL path. How many sources an install can run
// no longer depends on which protocol the encoder speaks.
type RTMPSettings struct {
	// App is the first path element -- the "live" in rtmp://host/live/<token>.
	// Cosmetic: rtmpserver.StreamKey discards it and will accept a publisher
	// that omits it entirely. It is here because it is what goes in OBS's
	// "Server" box, and a server URL without one does not look like one.
	App string `json:"app"`
	// StreamKey NO LONGER ADDRESSES ANYTHING. The address is the source's
	// publish token, which is 192 bits of crypto/rand, unique, rotatable with a
	// grace window, and never logged -- none of which was ever true of this
	// field. Every source created from the defaults used to get the identical
	// key "stream", and nothing in the schema or the validator stopped two
	// sources sharing one; as an address that is one programme silently
	// answering for another, which is the failure the one-port work exists to
	// remove.
	//
	// Kept in the struct so a settings blob written before the change round-
	// trips byte-identically, and so an install upgrading with a live RTMP
	// encoder keeps working: engine.Manager honours a stored key as a LEGACY
	// address for the one source that claims it, so the operator's encoder does
	// not go off air on restart. New sources get no key at all (see
	// DefaultSettings), which is what stops that grandfather clause from
	// becoming a way to collide on purpose.
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
	case IngestUnset:
		// Allowed to persist, deliberately.
		//
		// This is the state a fresh install is in before anyone has chosen, and
		// the migration creates the "Main" source during DB open — so rejecting
		// it here does not force a choice, it stops the database from opening at
		// all and the server never starts. Storage has to be able to represent
		// "not decided yet".
		//
		// What refuses unset is the API handler that saves an explicit settings
		// change (nobody gets to choose "none" on purpose) and the engine, which
		// will not spawn an ingest without a mode. The effect is the same as a
		// hard error — nothing ingests until a choice is made — without taking
		// the install down to get there.
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
	if bad := srtPassphraseUnreserved(i.SRT.Passphrase); bad != "" {
		add("srt passphrase cannot contain %q: it is carried in the ingest URL an "+
			"encoder copies, and the encoder does not decode it, so anything needing "+
			"escaping arrives as its escape text and never matches. Use letters, "+
			"digits, and - _ . ~", bad)
	}
	if i.SRT.LatencyMS < 20 || i.SRT.LatencyMS > 8000 {
		add("srt latency %dms out of range (20-8000)", i.SRT.LatencyMS)
	}
	// Required, but say plainly what it does: the listener discards the app
	// segment, so this gates nothing. It is required because it is half of the
	// URL the operator pastes into their encoder, and an empty one yields
	// rtmp://host:1935//<token> -- which does work, and which nobody would
	// believe was meant.
	if i.Mode == IngestRTMP && i.RTMP.App == "" {
		add("rtmp app name is required (it is the /live in the publish URL; " +
			"the source is addressed by its token, not by this)")
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
	// MaxPlaylistItemUpload mirrors MaxSlateImagePath: the same bound for the
	// same reason, a filesystem and a form field must both be able to hold it.
	// It used to bound a data-dir-relative path; it now bounds an upload's
	// stored name, which is shorter in practice but needs no smaller a ceiling.
	MaxPlaylistItemUpload = 512
	// MaxPlaylistItems bounds how many entries one playlist may hold.
	//
	// The list is not free to walk. engine.playlistItemsReady stats every item
	// twice on EVERY reconcile while selMu is held -- the same lock an
	// operator's POST /failover/source queues behind -- and the whole settings
	// document is one JSON row that is read on most API requests. A thousand
	// items is far more than any broadcast rotation and still a bounded amount
	// of work; without a ceiling the only limit is what a client can POST.
	//
	// It also bounds what sub-project B2 will turn this list into: a concat
	// file handed to FFmpeg, one line per item.
	MaxPlaylistItems = 1000
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

// PlaylistItem is one entry in the playlist, in play order.
type PlaylistItem struct {
	// Upload is the STORED name of an upload -- what Store.List reports as
	// File.Name and what Store.Resolve accepts. Not a path, and not an id:
	// internal/uploads has no identifier other than the name it chose.
	//
	// The distinction is a security boundary rather than a convenience. The
	// concat demuxer needs -safe 0 to accept absolute paths, which disables
	// its own check; that is only defensible while every path it sees was
	// chosen by this process, which uploads.SafeName is what guarantees.
	Upload string `json:"upload"`
}

// PlaylistUploadName is THE ONE PLACE a playlist item's Upload is trimmed.
//
// It lives in this package because this package owns PlaylistItem, and because
// the three packages that have to agree about what an item names -- internal/db
// validating it, internal/engine hashing and resolving it, internal/api
// checking and enqueuing it -- can all import this one and none of them can
// import each other.
//
// THE FAILURE IT PREVENTS HAS ALREADY HAPPENED ONCE. Validation and the
// signature hash trimmed; the resolver did not. So " loop.mp4" validated,
// hashed as "loop.mp4", and resolved to a path that does not exist -- FFmpeg
// respawn-looped on a file that was never the one the operator meant. Worse,
// editing " a.mp4" to "a.mp4" moved the argv but NOT the signature, so no
// respawn fired and the correction never took effect at all. Every added
// caller is a chance for that disagreement to come back, and the only defence
// is that there is nothing to disagree with: a caller that needs an item's
// name calls this, and no caller anywhere writes its own strings.TrimSpace
// over an Upload.
//
// The one exception this used to carry is now closed: playlistmedia.
// DerivativePath used to trim independently, because it took a name out of a
// job's JSON params rather than out of a PlaylistItem, and internal/playlistmedia
// could not import this package. Nothing about that has changed except that it
// now can (internal/db does not import internal/playlistmedia, so there is no
// cycle), and B2's playlistmedia.ProfileVersion made a second, private trim in
// that function too dangerous to keep: a fifth site that ever disagreed with
// this one would key a derivative's filename on a name the rest of the system
// does not recognise as the same upload.
func PlaylistUploadName(upload string) string { return strings.TrimSpace(upload) }

// PlaylistSettings is an ordered list of uploads the selector can put on air
// when no encoder is delivering.
//
// Deliberately smaller than SlateSettings. The slate carries encoder, preset,
// colour and bitrate because it SYNTHESISES a picture; a playlist plays files
// that already have their own encoding, so it needs none of them.
type PlaylistSettings struct {
	Enabled bool `json:"enabled"`
	// Items is the playlist, in play order. Each entry names an upload rather
	// than a path -- see PlaylistItem.Upload for why that distinction is load
	// bearing rather than cosmetic. An operator-supplied path here is exactly
	// the shape SECURITY.md's path confinement section exists to defend, and
	// PlaylistFileProblem is what refuses it.
	Items []PlaylistItem `json:"items"`
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
	// Playlist is a failover candidate beside Slate: a file the selector can
	// put on air when no encoder is delivering. It lives here, rather than
	// its own top-level section, because it is one more answer to the same
	// question the slate answers -- what feeds the hub when nothing else does.
	Playlist PlaylistSettings `json:"playlist"`
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

// PlaylistFileProblem reports why the configured playlist items cannot be
// used, or nil.
//
// Kept under its old name -- it used to guard one FilePath, and it now guards
// a list -- because engine.go's playlistSig and reconcilePlaylist call it by
// this name to decide whether the tier may start at all, and renaming it
// would be a larger change than this task's model-and-migration scope.
//
// Same confinement SlateSettings.ImagePath and a file:// pull source apply,
// narrowed further: an upload name must be a BARE filename with no
// separators, because internal/uploads.SafeName has already thrown away
// whatever path the client originally sent when the file was uploaded, so a
// stored upload never has one. Anything path-shaped here can only be an
// attempt to reach outside the uploads directory -- and because the concat
// demuxer needs -safe 0 to accept the absolute paths this process resolves an
// Upload to (see PlaylistItem.Upload), this check is the only thing standing
// between that trust and an operator-chosen path reaching FFmpeg.
func (p PlaylistSettings) PlaylistFileProblem() error {
	if len(p.Items) == 0 {
		if p.Enabled {
			// An enabled playlist with nothing to play would start a feed that
			// can never deliver, and the selector would offer a candidate that
			// always loses -- accepting this now only moves the failure to
			// runtime, where it is harder to see.
			return errors.New("playlist is enabled but has no items")
		}
		return nil
	}
	if len(p.Items) > MaxPlaylistItems {
		return fmt.Errorf("playlist has %d items, more than the %d allowed",
			len(p.Items), MaxPlaylistItems)
	}
	for i, item := range p.Items {
		if err := playlistUploadProblem(item.Upload); err != nil {
			return fmt.Errorf("playlist item %d: %w", i, err)
		}
	}
	return nil
}

// playlistUploadProblem reports why an item's Upload cannot name an upload, or
// nil.
//
// This is a shape check, not an existence check, and deliberately so: it cannot
// ask internal/uploads whether the name is a real file, because settings
// validation has no Store to ask and internal/db must not grow one. Importing
// internal/uploads here would put an os.MkdirAll and a stat behind every
// db.GetSettings -- ~20 callers, several of them per-request handlers -- which
// is the defect Task 1 had to undo once already.
//
// EXISTENCE IS CHECKED, just not here. The settings handler asks the uploads
// store it already holds (api.Server.playlistUploadProblems, called from
// handlePutSettings) so that "that file is not there" is a 400 an operator
// reads, which is what the spec requires. Whether the item has also been
// NORMALISED is a third question, answered later still, by engine.go's
// readiness gate -- normalisation is asynchronous, so an item can be perfectly
// valid and not yet playable.
func playlistUploadProblem(upload string) error {
	u := PlaylistUploadName(upload)
	if u == "" {
		return errors.New("names no upload")
	}
	if u == "." {
		// Too short to trip the separator or ".." checks below, but
		// uploads.Store.Resolve refuses it outright (same as "" and ".."),
		// and a playlist item this validator waved through would never
		// resolve -- an enabled candidate that can only ever fail to start,
		// which is exactly what an enabled-with-zero-items playlist is
		// refused for above.
		return errors.New("must be a bare uploaded filename, not \".\"")
	}
	if len(u) > MaxPlaylistItemUpload {
		return fmt.Errorf("upload name is longer than %d characters", MaxPlaylistItemUpload)
	}
	if strings.ContainsAny(u, "\x00\n\r") {
		return errors.New("upload name contains control characters")
	}
	// Backslashes are separators on Windows too, so normalise before checking
	// for one, or "sub\\dir.mp4" would slip past a forward-slash-only test.
	rel := strings.ReplaceAll(u, `\`, "/")
	if strings.Contains(rel, "/") || strings.Contains(rel, "..") ||
		(len(rel) > 1 && rel[1] == ':') {
		return errors.New("must be a bare uploaded filename, not a path")
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
	if bad := srtPassphraseUnreserved(b.SRT.Passphrase); bad != "" {
		add("backup srt passphrase cannot contain %q; use letters, digits, and - _ . ~", bad)
	}
	if b.SRT.LatencyMS < 20 || b.SRT.LatencyMS > 8000 {
		add("backup srt latency %dms out of range (20-8000)", b.SRT.LatencyMS)
	}
	if b.Mode == IngestRTMP && b.RTMP.App == "" {
		add("backup rtmp app name is required (it is the /live in the publish URL; " +
			"the standby is addressed by <token>.backup, not by this)")
	}
	// No collision to check any more, on either protocol. Primary and standby
	// both arrive on their one listener and are told apart by token --
	// `<token>` and `<token>.backup` -- so "which socket does each bind" is no
	// longer a question that can have a wrong answer. RTMP used to be the
	// exception, because there was one RTMP listener and the primary held it;
	// there is still one listener, and it now carries both.
	if err := f.Slate.SlateImageProblem(); err != nil {
		add("%v", err)
	}
	if k := f.Slate.VideoKbps; k < 0 || k > 100_000 {
		add("slate bitrate %d kbps out of range (0-100000, 0 for the default)", k)
	}
	if err := f.Playlist.PlaylistFileProblem(); err != nil {
		add("%v", err)
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

// DestinationSettings is install-wide destination policy.
type DestinationSettings struct {
	// StaggerMS spaces out the FIRST connection of destinations brought up in
	// the same reconcile. 0 is off, which is what every install did before it
	// existed.
	//
	// Going live means every destination opening a connection, negotiating TLS
	// and starting to encode audio in the same tick. On a small box that is
	// the moment most likely to drop frames, and it is the exact moment an
	// operator is watching -- because it is when they went live.
	//
	// It never delays a RECONNECT. A destination that drops at 3am has to come
	// back immediately, not wait its turn behind processes that are already
	// healthy.
	StaggerMS int `json:"staggerMs"`
}

// MaxDestinationStaggerMS is five seconds. Beyond that, bringing up eight
// destinations takes most of a minute and the operator is watching a progress
// bar rather than a stream.
const MaxDestinationStaggerMS = 5000

func (d DestinationSettings) problems() []string {
	if d.StaggerMS < 0 || d.StaggerMS > MaxDestinationStaggerMS {
		return []string{fmt.Sprintf("destination stagger %dms out of range (0-%d, 0 for none)",
			d.StaggerMS, MaxDestinationStaggerMS)}
	}
	return nil
}

// -------------------------------------------------------------- multitrack

// MultitrackSettings is the hardware inventory Twitch Enhanced Broadcasting is
// negotiated with. See internal/multitrack, and Destination.Multitrack, which
// is the per-destination opt-in this block makes possible.
//
// IT IS DECLARED BY THE OPERATOR RATHER THAN MEASURED, and that is the decision
// rather than an omission. multitrack.GPU carries six fields -- model, PCI
// vendor id, PCI device id, dedicated video memory, shared system memory,
// driver version -- and this repository can enumerate exactly one of them on
// one platform: ffmpeg.GPUDevice.VendorID, read out of /sys/class/drm on Linux.
// There is no model, no device id and no memory figure anywhere in
// internal/ffmpeg, on any GOOS.
//
// Twitch VALIDATES this inventory and refuses by name: a vendor id of 0, a
// vendor it does not recognise, and an out-of-date driver were each refused in
// testing. So a request assembled from the one field that can be measured, with
// zeros in the other five, would be a statement about this machine that is not
// true -- and the refusal it earned would look like a platform problem rather
// than like the missing information it is. multitrack.Capabilities draws the
// same boundary in its own words: "The caller supplies it or the call is
// refused."
//
// EMPTY IS THE DEFAULT AND IS NOT A FAULT. multitrack.Negotiate short-circuits
// to Refused when no GPU is declared, without spending a network round trip at
// go-live to be told what it already knows, and the destination publishes to
// the ordinary Twitch ingest and says so once. Every install that never opens
// this page behaves exactly as it did before this field existed.
//
// The operator does not have to guess the vendor id: GET
// /api/v1/renditions/hardware already reports the PCI vendor id of every DRM
// node it found and the NVIDIA driver version when it could be read cheaply,
// which is what the settings form shows beside these boxes.
type MultitrackSettings struct {
	// GPUs is what this machine has. A list rather than one entry because
	// multitrack.Capabilities.GPU is a list and a two-card machine is ordinary;
	// order is the operator's, and Preferences.CompositionGPUIndex would index
	// into it if polyemesis ever composited on a chosen card.
	GPUs []MultitrackGPU `json:"gpus,omitempty"`
}

// Known PCI vendor ids, in the DECIMAL spelling Twitch's wire format uses.
// internal/ffmpeg spells the same three in hex because sysfs does; both are
// here rather than one converted into the other, because the two are read by
// different audiences and a silent base change is how an id becomes wrong.
const (
	PCIVendorNVIDIA = 4318  // 0x10de
	PCIVendorAMD    = 4098  // 0x1002
	PCIVendorIntel  = 32902 // 0x8086
)

// MultitrackGPU is one declared adapter. The field names and units are
// multitrack.GPU's, which is the wire format, so that nothing between this
// struct and the request has to reinterpret them.
type MultitrackGPU struct {
	// Model is the adapter as its vendor names it, e.g. "NVIDIA GeForce RTX
	// 4070". Twitch quotes nothing back from it, but it is the field an
	// operator reads on the settings page to check they filled in the right
	// card, so it is required rather than optional.
	Model string `json:"model"`
	// VendorID is the PCI vendor id as a DECIMAL integer: 4318 NVIDIA, 4098
	// AMD, 32902 Intel. Zero is refused -- by Twitch, naming the value, and
	// here, so the operator learns it on the settings page rather than as a
	// fallback they cannot explain three weeks later.
	VendorID uint32 `json:"vendorId"`
	// DeviceID is the PCI device id, decimal. Optional: it is not something
	// polyemesis can check, and an operator who cannot find it is better off
	// sending zero than inventing one.
	DeviceID uint32 `json:"deviceId,omitempty"`
	// DedicatedVideoMemory and SharedSystemMemory are in BYTES, which is the
	// unit obs-studio sends them in. Optional for the same reason as DeviceID.
	DedicatedVideoMemory uint64 `json:"dedicatedVideoMemory,omitempty"`
	SharedSystemMemory   uint64 `json:"sharedSystemMemory,omitempty"`
	// DriverVersion is the vendor's own version string. Twitch refuses an
	// out-of-date driver naming the version to upgrade to, so an empty one is
	// simply a refusal it cannot explain -- but it is still left optional,
	// because a wrong version invented to fill the box is worse than a missing
	// one.
	DriverVersion string `json:"driverVersion,omitempty"`
}

// Declared reports whether this machine has been told it has a GPU at all.
// Nothing negotiates without it.
func (m MultitrackSettings) Declared() bool { return len(m.GPUs) > 0 }

// MaxMultitrackGPUs bounds the list. Eight is well past any machine that
// encodes video and exists to catch a paste accident, not to express a view.
const MaxMultitrackGPUs = 8

// MaxMultitrackFieldLength bounds the two free-text fields. They land in a JSON
// request body, so the limit is about a paste accident rather than about a
// buffer.
const MaxMultitrackFieldLength = 200

// problems validates the declared inventory.
//
// A HALF-FILLED ENTRY IS AN ERROR HERE RATHER THAN A REFUSAL AT GO-LIVE. An
// entry with no vendor id is one Twitch will refuse by name, and the operator
// would meet that as a destination quietly publishing to the ordinary ingest
// with a sentence about GPUs they had, they thought, just configured. Refusing
// it on the settings page is the one place the mistake is still attached to the
// thing that caused it.
//
// AN UNRECOGNISED VENDOR ID IS NOT REFUSED, only zero is. Twitch validates
// against a list it does not publish, and a list guessed from three constants
// would refuse whatever it adds next -- the failure this repo's services
// registry exists to avoid. An unknown vendor gets a refusal from Twitch with
// Twitch's own sentence, which is more accurate than anything asserted here.
func (m MultitrackSettings) problems() []string {
	var probs []string
	if len(m.GPUs) > MaxMultitrackGPUs {
		probs = append(probs, fmt.Sprintf("%d Enhanced Broadcasting GPUs declared (max %d)",
			len(m.GPUs), MaxMultitrackGPUs))
	}
	for i, g := range m.GPUs {
		if strings.TrimSpace(g.Model) == "" {
			probs = append(probs, fmt.Sprintf("Enhanced Broadcasting GPU %d needs a model name", i+1))
		}
		if len(g.Model) > MaxMultitrackFieldLength || len(g.DriverVersion) > MaxMultitrackFieldLength {
			probs = append(probs, fmt.Sprintf("Enhanced Broadcasting GPU %d has a field over %d characters",
				i+1, MaxMultitrackFieldLength))
		}
		if g.VendorID == 0 {
			probs = append(probs, fmt.Sprintf("Enhanced Broadcasting GPU %d needs a PCI vendor ID "+
				"(decimal: %d NVIDIA, %d AMD, %d Intel) -- Twitch refuses a request that sends zero",
				i+1, PCIVendorNVIDIA, PCIVendorAMD, PCIVendorIntel))
		}
	}
	return probs
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

// unwrapURLError peels a *url.Error down to its reason.
//
// It exists because url.Error.Error() is `parse "<input>": <reason>` -- it
// embeds the ENTIRE input URL. Every validation message built from a parse
// failure of an operator-supplied URL therefore echoes that URL back, and when
// the URL carries `user:password@` the password travels with it into whatever
// the message reaches: here, a 400 response body.
//
// The reason alone is the whole diagnostic. "invalid character \" \" in host
// name" tells the operator exactly what to fix; repeating the string they just
// typed adds nothing they cannot see and carries everything they cannot unsay.
func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
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
		// The url.Error is UNWRAPPED before it is formatted. Its Error() renders
		// as `parse "<the whole URL>": <reason>`, so `%v` of the wrapper put the
		// operator's password into this string -- and these strings are returned
		// to the caller as a 400 body, not merely logged. `mqtt://user:pw@ho
		// st:1883` is the measured shape. Reordering the cases below cannot help
		// here: `u` is nil in this branch, so the credentials guard is
		// unreachable by construction.
		add("mqtt broker URL is unparseable: %v", unwrapURLError(err))
	// BEFORE the no-host case, which is the same reordering internal/mqtt's
	// parseBroker needed and for the same reason: `mqtt://user:pw@` parses
	// cleanly, carries a credential and has no host, so the no-host message --
	// which echoed %q of the raw URL -- fired first and put the password in a
	// 400 response body. The guard below promises the password is "never
	// logged"; it was never reached for this shape.
	case u.User != nil:
		// Refused rather than quietly moved into the username and password
		// fields, because the operator needs to know the URL they pasted would
		// have been written to a log.
		add("mqtt broker URL carries credentials; put the username and password in their own fields so the password is sealed and never logged")
	case u.Host == "":
		// No %q of the raw URL: see above. The operator can see what they typed.
		add("mqtt broker URL has no host")
	default:
		switch u.Scheme {
		case "mqtt", "tcp", "mqtts", "ssl", "ws", "wss":
		default:
			add("mqtt broker scheme %q is not one of mqtt, mqtts, tcp, ssl, ws or wss", u.Scheme)
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

	// WhisperModel is the transcription model a job gets when it names none.
	// Empty keeps the hardware-derived choice, which is the right default and
	// stays the default.
	//
	// It is here because model choice IS the transcription decision -- it trades
	// speed, accuracy and memory against each other, and the right answer
	// depends on hardware polyemesis can measure and on how much the operator
	// cares about the transcript, which it cannot. The per-job API already
	// accepted a model; nothing could express a preference for every job, and
	// the UI never sent one at all, so the hardware guess was the only reachable
	// answer.
	//
	// NOT validated against a fixed list. transcribe.Models() is the catalogue
	// and it can grow, an operator may have a model file this build has never
	// heard of, and a name we reject here is a model they cannot use for a
	// reason they cannot see. The worker reports an unknown model when it tries
	// to load it, which is the layer that actually knows.
	WhisperModel string `json:"whisperModel,omitempty"`
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
	// RTMPPort serves EVERY source too, and in both directions: encoders
	// publish to rtmp://host:PORT/live/<token> and this install's own FFmpeg
	// subscribes to rtmp://127.0.0.1:PORT/live/<token> on the same socket. It
	// used to serve at most one, which was an artifact of `ffmpeg -listen 1`
	// being unable to demultiplex by path rather than a decision anyone made.
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
	// Destinations is install-wide destination policy; per-destination
	// settings live on the destination row.
	Destinations DestinationSettings `json:"destinations"`
	// Multitrack is the hardware inventory Twitch Enhanced Broadcasting is
	// negotiated with. Install-wide because it describes the machine, not a
	// destination; the per-destination opt-in is Destination.Multitrack.
	Multitrack MultitrackSettings `json:"multitrack"`
	Chat       ChatSettings       `json:"chat"`
	Automod    AutomodSettings    `json:"automod"`
	Alerts     AlertSettings      `json:"alerts"`
}

// AutomodSettings is everything about automatic chat moderation except the
// model's API key, which is sealed separately -- same shape as the MQTT broker
// password, and for the same reason: a secret in the settings blob is a secret
// returned by GET /settings.
//
// The matrix is stored as the set of cells that are ON rather than a dense
// grid. A dense grid needs migrating whenever an action, checker or platform is
// added, and the migration has to invent a default for cells nobody has seen. A
// sparse set answers that by construction: absent means off, which IS the
// default.
type AutomodSettings struct {
	// Enabled is the global kill switch.
	Enabled bool `json:"enabled"`
	// PlatformEnabled is the per-platform kill switch. An absent platform means
	// enabled, so adding a platform does not silently disable it -- the global
	// switch above is the one that fails closed.
	PlatformEnabled map[Platform]bool `json:"platformEnabled,omitempty"`
	// On holds the cells switched on, keyed "platform/action/checker".
	On map[string]bool `json:"on,omitempty"`
	// Rules are the operator's patterns.
	Rules []AutomodRule `json:"rules,omitempty"`
	// History bounds the sequence detectors.
	History AutomodHistory `json:"history"`
	// Model configures the optional external checker.
	Model AutomodModel `json:"model"`
}

// AutomodRule is one stored pattern. Mirrors automod.Rule, which is where it is
// compiled and matched; this is only the persisted shape.
type AutomodRule struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Enabled        bool   `json:"enabled"`
	Pattern        string `json:"pattern"`
	Action         string `json:"action"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

// AutomodHistory bounds the per-author sequence detectors.
type AutomodHistory struct {
	WindowSeconds         int     `json:"windowSeconds"`
	MaxMessages           int     `json:"maxMessages"`
	MaxRepeats            int     `json:"maxRepeats"`
	MaxLinks              int     `json:"maxLinks"`
	MaxMentionsPerMessage int     `json:"maxMentionsPerMessage"`
	MinLengthForCaps      int     `json:"minLengthForCaps"`
	MaxCapsRatio          float64 `json:"maxCapsRatio"`
	Action                string  `json:"action"`
	TimeoutSeconds        int     `json:"timeoutSeconds"`
	RetainPerAuthor       int     `json:"retainPerAuthor"`
	IdleEvictionSeconds   int     `json:"idleEvictionSeconds"`
}

// AutomodModel configures the external checker.
type AutomodModel struct {
	Enabled  bool   `json:"enabled"`
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
	// HasAPIKey reports that a sealed key exists, so the settings page can show
	// "configured" without the key ever being returned. The key itself is set
	// through its own endpoint and never appears in this blob.
	HasAPIKey       bool    `json:"hasApiKey"`
	TimeoutSeconds  int     `json:"timeoutSeconds"`
	MaxCallsPerHour int     `json:"maxCallsPerHour"`
	Action          string  `json:"action"`
	TimeoutForBan   int     `json:"timeoutForBan,omitempty"`
	MinConfidence   float64 `json:"minConfidence"`
	Instruction     string  `json:"instruction"`
}

// ChatSettings bounds the stored chat scrollback.
//
// This became worth exposing when the moderator's user card shipped. That card
// answers "what has this person said before", and it answers it out of THIS
// table -- no platform publishes a chat-history API, so the depth of a
// moderation decision is now a direct function of these two numbers. At the old
// hard-coded two hours, a card opened on a returning troublemaker showed
// nothing, which reads as "they have never said anything" rather than "we did
// not keep it".
//
// Both are bounds, and the QUIETER one wins: a message is dropped when it is
// older than RetentionHours AND not among the newest KeepMessages. A busy
// channel therefore keeps less time than asked and a quiet one keeps more, which
// is the right way round -- the floor is what stops a slow channel's card being
// empty.
type ChatSettings struct {
	// RetentionHours drops messages older than this. 0 means keep forever,
	// matching the recorder's MaxAgeHours convention.
	//
	// Forever is a real answer here and not a trap: chat rows are small. A
	// channel averaging ten messages a second stores roughly 7 MB an hour, so a
	// year of a busy channel is tens of gigabytes, and most channels are
	// nowhere near that. An operator who wants a permanent moderation record
	// should be able to have one.
	RetentionHours int `json:"retentionHours"`
	// KeepMessages is the floor: this many newest messages survive whatever
	// their age. It is what stops a channel that was quiet overnight from
	// opening every user card empty in the morning.
	KeepMessages int `json:"keepMessages"`
	// PurgeMinutes is how often the sweep runs. Cheap -- it is one indexed
	// delete -- so this is about how promptly "deleted" becomes true on disk
	// rather than about load.
	PurgeMinutes int `json:"purgeMinutes"`
	// HistoryMessages is the in-memory ring a browser reads on connect, before
	// it falls back to querying the database.
	//
	// It pairs with RetentionHours and KeepMessages above and answers a
	// different question. Those two decide how deep a moderator's user card can
	// go; this decides how much scrollback arrives WITHOUT a query, which is
	// what a late-joining operator sees in the moment they open the page.
	//
	// Bounded much lower than KeepMessages, and the reason is where the two
	// live. KeepMessages is rows on disk, paid for only as they arrive.
	// This ring is allocated in full at construction, so the number is memory
	// reserved on a silent channel exactly as on a busy one.
	HistoryMessages int `json:"historyMessages"`
}

// ChatSettings bounds, chosen to be generous rather than tidy. The cost of the
// upper end is disk, which the operator can see; the cost of a low ceiling is a
// moderation decision made on missing evidence, which they cannot.
const (
	// MaxChatRetentionHours is five years. Not a guess at what anyone needs --
	// a bound that exists so a typo of 999999 is caught rather than stored.
	MaxChatRetentionHours = 24 * 365 * 5
	MaxChatKeepMessages   = 5_000_000
	MaxChatPurgeMinutes   = 24 * 60
	// MaxChatHistoryMessages is two orders of magnitude below
	// MaxChatKeepMessages on purpose. The ring is allocated up front, so this
	// ceiling is a memory reservation and not a limit on what may accumulate:
	// at roughly 200 bytes a message it is about 10 MB held whether or not
	// anyone ever says anything.
	MaxChatHistoryMessages = 50_000
	// MinChatHistoryMessages keeps a connecting browser from receiving nothing
	// at all. Zero would be a legitimate wish -- "do not buffer" -- but it
	// reads on the page as chat being broken, and the operator has no way to
	// tell those apart.
	MinChatHistoryMessages = 1
)

func (c ChatSettings) problems() []string {
	var probs []string
	if c.RetentionHours < 0 || c.RetentionHours > MaxChatRetentionHours {
		probs = append(probs, fmt.Sprintf(
			"chat retention %d hours out of range (0-%d, 0 to keep forever)",
			c.RetentionHours, MaxChatRetentionHours))
	}
	if c.KeepMessages < 0 || c.KeepMessages > MaxChatKeepMessages {
		probs = append(probs, fmt.Sprintf(
			"chat keep %d messages out of range (0-%d)", c.KeepMessages, MaxChatKeepMessages))
	}
	// Zero would be a sweep every tick. Bounded below rather than defaulted
	// silently, because a 0 here is far more likely to be a mistake than a wish.
	if c.PurgeMinutes < 1 || c.PurgeMinutes > MaxChatPurgeMinutes {
		probs = append(probs, fmt.Sprintf(
			"chat purge interval %d minutes out of range (1-%d)", c.PurgeMinutes, MaxChatPurgeMinutes))
	}
	if c.HistoryMessages < MinChatHistoryMessages || c.HistoryMessages > MaxChatHistoryMessages {
		probs = append(probs, fmt.Sprintf(
			"chat history %d messages out of range (%d-%d)",
			c.HistoryMessages, MinChatHistoryMessages, MaxChatHistoryMessages))
	}
	return probs
}

// AlertSettings is install-wide alert delivery policy. Per-rule matching lives
// on the rule row; this is how hard delivery tries once a rule has fired.
type AlertSettings struct {
	// RetryAttempts is how many times one delivery is tried before it is given
	// up on, first try included.
	//
	// The failure story is an endpoint that is down rather than slow. Bounded
	// is the whole point: retrying forever turns one dead webhook into a
	// permanently busy goroutine, and the queue behind it into a backlog that
	// never drains. What an operator gets to decide is how long "down" is
	// tolerated before the alert is dropped, which is a judgement about their
	// endpoint and not one this project can make for them.
	//
	// The backoff curve underneath is deliberately NOT exposed. It was chosen
	// against measured behaviour, no failure story argues for changing it, and
	// a knob nobody has a reason to turn still has to be validated, documented
	// and supported -- the same argument that leaves the Low rows in
	// docs/roadmap/UNREACHABLE-KNOBS.md alone.
	RetryAttempts int `json:"retryAttempts"`
}

// AlertSettings bounds. The upper end is chosen against the backoff curve
// rather than picked round: attempts back off to a 30s ceiling, so ten attempts
// is already several minutes of chasing one dead endpoint.
const (
	MinAlertRetryAttempts = 1
	MaxAlertRetryAttempts = 10
)

func (a AlertSettings) problems() []string {
	var probs []string
	if a.RetryAttempts < MinAlertRetryAttempts || a.RetryAttempts > MaxAlertRetryAttempts {
		probs = append(probs, fmt.Sprintf(
			"alert retry attempts %d out of range (%d-%d)",
			a.RetryAttempts, MinAlertRetryAttempts, MaxAlertRetryAttempts))
	}
	return probs
}

// DefaultSettings is what a fresh install runs with.
func DefaultSettings() Settings {
	return Settings{
		Ingest: IngestSettings{
			// Unset, so first run has to ask. See mergeBaseSettings for why the
			// value used when reading an EXISTING blob is different.
			Mode: IngestUnset,
			SRT:  SRTSettings{LatencyMS: 200},
			// No StreamKey. It used to default to "stream" for every source,
			// which was harmless while it was a playpath FFmpeg checked and
			// fatal the moment a key became an address: two sources from the
			// defaults would have claimed the same one. A stored key is still
			// honoured as a legacy address for an upgrading install (see
			// RTMPSettings.StreamKey), and minting none here is what keeps that
			// grandfather clause from ever colliding with a source created
			// afterwards.
			RTMP: RTMPSettings{App: "live"},
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
				// No StreamKey, for the same reason as the primary. The standby
				// is addressed by "<token>.backup" on the same listener.
				RTMP: RTMPSettings{App: "live"},
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
		// Deliberately the same numbers the Hub used when they were constants,
		// so an existing install behaves identically after upgrading and only
		// changes when somebody decides to change it. The constants in
		// internal/chat are still the fallback for a Hub built without these,
		// which is every test; TestChatDefaultsMatchTheChatPackage keeps the two
		// in step the way the MQTT pair above are kept.
		Chat: ChatSettings{
			RetentionHours: 2,
			KeepMessages:   2000,
			PurgeMinutes:   5,
			// chat.DefaultHistory. Unchanged from the value every install ran
			// before this was reachable, so making it settable does not also
			// change it -- pinned by TestChatDefaultsMatchTheChatPackage.
			HistoryMessages: 500,
		},
		// alerts' own defaultAttempts, for the same reason: exposing a knob is
		// not an occasion to move it. Pinned by
		// TestAlertDefaultsMatchTheAlertsPackage.
		Alerts: AlertSettings{RetryAttempts: 4},
		// Automod is ON, and does NOTHING except flag for review.
		//
		// Those two facts together are the point. Shipping it off means an
		// operator has to discover it exists; shipping it acting means it
		// surprises somebody mid-broadcast. Flagging changes nothing an
		// audience can see, so it is the one thing safe to start armed --
		// every other cell is absent, and absent means off.
		//
		// The On map is populated by automod.DefaultMatrix(), which is the
		// single source of that default; duplicating the keys here would be
		// two truths about the same thing.
		Automod: AutomodSettings{
			Enabled: true,
			History: AutomodHistory{
				WindowSeconds:         30,
				MaxMessages:           8,
				MaxRepeats:            3,
				MaxLinks:              3,
				MaxMentionsPerMessage: 5,
				MinLengthForCaps:      12,
				MaxCapsRatio:          0.8,
				Action:                "timeout",
				TimeoutSeconds:        60,
				RetainPerAuthor:       24,
				IdleEvictionSeconds:   600,
			},
			Model: AutomodModel{
				Enabled:         false,
				Model:           "gpt-4o-mini",
				TimeoutSeconds:  4,
				MaxCallsPerHour: 500,
				Action:          "flag",
				MinConfidence:   0.8,
				Instruction:     "Flag harassment, threats, slurs and targeted abuse. Ordinary criticism, banter and strong language are not abuse.",
			},
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
	for _, p := range s.Multitrack.problems() {
		add("%s", p)
	}
	for _, p := range s.Destinations.problems() {
		add("%s", p)
	}
	for _, p := range s.Chat.problems() {
		add("%s", p)
	}
	for _, p := range s.Alerts.problems() {
		add("%s", p)
	}

	if len(probs) > 0 {
		return fmt.Errorf("invalid settings: %v", probs)
	}
	return nil
}

// mergeBaseSettings is DefaultSettings with the pre-first-run-choice ingest
// mode filled in, for use as the base when decoding a blob that already exists.
func mergeBaseSettings() Settings {
	s := DefaultSettings()
	s.Ingest.Mode = IngestSRT
	s.Failover.Backup.Mode = IngestSRT
	return s
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
	//
	// NOT DefaultSettings(): that leaves the ingest mode unset so first run has
	// to ask, which is right for a new install and wrong here. A stored blob
	// that predates the mode field, or that omits it, would inherit the unset
	// value and the install would stop ingesting on upgrade — a silent
	// regression on exactly the servers that were working. Existing installs
	// keep the mode they have always had.
	s := mergeBaseSettings()
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return Settings{}, fmt.Errorf("decode settings: %w", err)
	}
	// Deliberately NOT migrating failover.playlist.filePath here any more.
	// GetSettings has around twenty callers, several of them per-request API
	// handlers, and confirming a legacy value honestly needs a filesystem
	// resolve plus a Stat -- see LegacyPlaylistFilePath's comment. Doing that
	// on every read would block every one of those callers on I/O, and since
	// nothing here persists the result, it would redo the same work and log
	// the same warning forever instead of once. cmd/polyemesis runs the real
	// migration once at startup, where the data directory and a configured
	// logger already exist, and PutSettings makes it stick.
	return s, nil
}

// LegacyPlaylistFilePath reports the playlist FilePath recorded before it
// became Items (DESIGN 2026-08-01-playlist-items), or "" if there is none.
//
// Pure past the SQL read: no filesystem access beyond this table and no
// logging, on purpose. Confirming that value still names something real
// needs a data directory and belongs to a caller that has one and a
// configured logger to report the decision with -- this package deliberately
// carries neither. See cmd/polyemesis's startup migration, which is the only
// caller.
//
// Naturally idempotent without any extra bookkeeping: once that migration
// persists Items via PutSettings, the stored blob is marshalled from the
// current Settings struct, which has no FilePath field left to write --
// so the very next call finds no legacy key at all and reports "".
func (d *DB) LegacyPlaylistFilePath() (string, error) {
	var raw string
	err := d.sql.QueryRow(`SELECT json FROM settings WHERE id = 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var legacy struct {
		Failover struct {
			Playlist struct {
				FilePath string `json:"filePath"`
			} `json:"playlist"`
		} `json:"failover"`
	}
	// Best-effort: a blob that fails even this loose a decode is not one this
	// migration can act on either way.
	_ = json.Unmarshal([]byte(raw), &legacy)
	return strings.TrimSpace(legacy.Failover.Playlist.FilePath), nil
}

// ErrSettingsUnchanged is what a mutate function reports when it looked at the
// stored document and found nothing to do.
//
// It exists because "already in the wanted state" is a legitimate outcome that
// is neither a failure nor a reason to write. The scheduled playlist flip fires
// on every occurrence -- an overlapping schedule, or a restart inside a window,
// arrives at a playlist that is already enabled -- and reporting that as an
// error would leave the occurrence unhandled and retried until its grace window
// ran out.
//
// What the no-write half buys is smaller and worth stating exactly, because an
// earlier version of this comment claimed a change notification and there is no
// settings watcher in this codebase to notify -- no broadcast, no MQTT topic,
// no WebSocket frame. It saves a whole-document Validate and a marshal-and-
// insert per occurrence, and it keeps the stored bytes untouched so anything
// that ever does watch them, or any backup that diffs them, sees no event where
// nothing happened.
var ErrSettingsUnchanged = errors.New("settings unchanged")

// InvalidSettingsError reports that the mutated document failed
// Settings.Validate, so nothing was written.
//
// It exists to be TYPED, not to be worded: the API answers 400 for a document
// the operator can fix and 500 for a store that failed, and telling those apart
// by matching on error strings is how that distinction rots. Error() therefore
// passes the validator's own message straight through -- that message is
// already what the settings endpoint puts in the response body, and decorating
// it here would change what every client reads.
type InvalidSettingsError struct{ Err error }

func (e InvalidSettingsError) Error() string { return e.Err.Error() }
func (e InvalidSettingsError) Unwrap() error { return e.Err }

// UpdateSettings is THE door for changing the stored settings.
//
// The settings are one JSON document and PutSettings writes all of it, so a
// caller that reads, edits one field and writes back is only safe while nobody
// else is doing the same thing at the same time -- see DB.settingsMu. This
// holds that lock across the whole read-mutate-validate-write span so the four
// callers that do it cannot interleave and drop each other's fields.
//
// It VALIDATES before it writes, which PutSettings does not. That is not
// belt-and-braces: PutSettings marshals and inserts, so any caller that skips
// Settings.Validate can store a document the settings API would have refused,
// and every later PUT /settings then answers 400 for a state the operator did
// not cause and cannot see. The scheduled playlist start shipped exactly that
// lockout once already; validating here means no future door can bring it back
// by forgetting.
//
// Returns the STORED document, because callers need it afterwards: PUT
// /settings hands it to the normalisation queue, the chat retention sweeper and
// the automod engine; PUT /jobs/policy reads PostProd.Policy() out of it.
//
// mutate's error is returned UNWRAPPED so a caller can test it with errors.Is,
// and nothing is written when it is non-nil. Two of those errors are special:
//
//   - ErrSettingsUnchanged means "nothing to do": no write, no error, and the
//     document handed back is what is STORED, not what mutate was holding when
//     it decided. A mutate may edit and then think better of it; the edits are
//     simply discarded, and the caller never sees a field that is not in the
//     database.
//
// It is NOT re-entrant. settingsMu is a plain Mutex, so a mutate that reaches
// UpdateSettings again -- directly, or through any helper that writes settings
// -- deadlocks the whole store rather than failing. A mutate should be a pure
// edit of the document it is handed.
//   - A validation failure comes back as InvalidSettingsError, so a caller can
//     tell "the operator sent something impossible" from "the database broke".
func (d *DB) UpdateSettings(mutate func(*Settings) error) (Settings, error) {
	d.settingsMu.Lock()
	defer d.settingsMu.Unlock()

	s, err := d.GetSettings()
	if err != nil {
		return Settings{}, err
	}
	switch err := mutate(&s); {
	case errors.Is(err, ErrSettingsUnchanged):
		// Re-read rather than hand back s. A mutate is free to have edited the
		// document before deciding there was nothing worth storing, and s
		// carries those edits; returning it would hand the caller fields that
		// are not in the database. This function promises the STORED document
		// on every path, and one extra read of a single row is a cheap way to
		// keep that true no matter what a mutate did on its way to refusing.
		return d.GetSettings()
	case err != nil:
		return Settings{}, err
	}
	if err := s.Validate(); err != nil {
		return Settings{}, InvalidSettingsError{Err: err}
	}
	if err := d.PutSettings(s); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// PutSettings stores the settings blob.
//
// It does NOT take DB.settingsMu, and it does not validate. It is the raw
// write: first-run seeding from GetSettings, the startup playlist migration in
// cmd/polyemesis, and tests arranging a fixture all reach it directly, and none
// of those is racing another writer. Anything in the RUNNING server that reads
// the document before writing it belongs in UpdateSettings instead.
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

// srtPassphraseUnreserved is the alphabet an SRT passphrase may use.
//
// RFC 3986's unreserved set: nothing in it is escaped by url.Values.Encode(),
// which is what makes ffmpeg.PublicIngestURL safe.
//
// THE RESTRICTION EXISTS BECAUSE THE CONSUMER DOES NOT DECODE. FFmpeg's libsrt
// reads the passphrase out of that URL with av_find_info_tag, which copies the
// raw bytes -- so a `;` rendered as `%3B` is SENT as `%3B` and compared,
// literally, against the value stored here. It cannot match. A live install hit
// exactly that: a correct passphrase, refused every time, and the URL the
// dashboard told the operator to copy was the reason.
//
// Refusing at the form is the only place the message is useful. The alternative
// -- escape it and hope -- produces a URL that PARSES and never connects, and
// the operator sees a rejected handshake they cannot read. SRT itself permits
// any bytes here, so this is polyemesis's restriction rather than SRT's, and it
// is stated as such in the message.
func srtPassphraseUnreserved(p string) string {
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.' || r == '~':
		default:
			return string(r)
		}
	}
	return ""
}
