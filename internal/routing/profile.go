// Package routing implements polyemesis' audio routing engine: the model for
// "which ingest audio tracks/channels does this destination receive", its
// validation, and its compilation into an FFmpeg -filter_complex string.
//
// Everything here is pure. No I/O, no process spawning, no globals. That is
// deliberate: it is the feature most likely to be wrong in a way the user only
// discovers by ear, so it must be exhaustively unit-testable.
package routing

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// MaxTracks is the number of audio tracks polyemesis accepts from an ingest.
//
// It was 6, matching OBS' six-track limit, back when OBS over RTMP was the only
// way tracks arrived. That is no longer the ceiling: SRT carries MPEG-TS, which
// has no such limit, and Enhanced RTMP identifies tracks by a one-byte trackId,
// so the protocols allow 256. 32 is where the *implementation* stops rather
// than where the specs do -- see MaxMeterChannels.
//
// Raising this does not oblige anyone to use it. OBS still sends at most six,
// and a six-track ingest produces byte-identical commands to before.
const MaxTracks = 32

// MaxChannels is the highest per-track channel a matrix cell may ADDRESS.
//
// It never was what its old name said -- "the largest per-track channel count we
// build a downmix for". DownmixMatrix has always handled any width, and simple
// mode passes the probed count straight through, so a nine-channel track routed
// perfectly well while its ninth channel could not be named in a matrix. A width
// you can route and cannot address is an inconsistency with no reason behind it.
//
// 16 covers every layout libavutil names, including the immersive ones (7.1.4 is
// twelve channels, 9.1.6 is sixteen). Raising it only WIDENS what validates, so
// no profile that was accepted before is rejected now.
//
// It bounds a channel INDEX, not a track's real width: a cell naming a channel
// the ingest does not carry is still dropped at compile time with a warning, and
// the level that drop costs is reported in dB.
const MaxChannels = 16

// MaxMeterChannels is FFmpeg's amerge ceiling, and the reason MaxTracks is 32
// rather than 256.
//
// The meters process merges every channel of every track into one wide stream
// so a single astats can report them all (see ffmpeg.MetersArgs). amerge refuses
// beyond 64 channels -- "Too many channels (max 64)" -- which is 32 stereo
// tracks, or 8 eight-channel ones.
//
// Note this bounds tracks x channels, not tracks alone, so MaxTracks and
// MaxChannels cannot both be spent at once. Exceeding it does not invalidate a
// profile and never rejects an ingest: an ingest polyemesis cannot fully meter
// is still an ingest it can route, and routing is the product. MetersArgs
// covers as many whole tracks as fit and reports how many it dropped.
const MaxMeterChannels = 64

// PlaceholderTracks is how many tracks DefaultSource pretends exist before the
// ingest has been probed.
//
// Deliberately NOT MaxTracks. This is a guess shown in the UI so a destination
// can be configured before a stream has ever connected, and guessing 32 would
// fill the routing editor with 26 tracks that will almost certainly never
// arrive. Six is what OBS sends, which is the overwhelmingly common case.
const PlaceholderTracks = 6

// MaxGain caps any single gain coefficient. 2.0 == +6 dB, which is as much
// boost as is defensible before the limiter is doing all the work.
const MaxGain = 2.0

// Audio delay bounds, in milliseconds.
//
// The two directions are deliberately asymmetric because they serve different
// jobs. Positive is a hold — lip-sync correction *and* the profanity/moderation
// delay a broadcaster puts in front of a live audience — so it is generous.
// Negative pulls audio ahead of video, which is only ever lip-sync repair; a
// two-second advance is already far past anything a real encoder produces, and
// every millisecond of it has to be bought with video buffering downstream.
const (
	MinDelayMS = -2000
	MaxDelayMS = 30000
)

// Loudness bounds. These are FFmpeg loudnorm's own accepted ranges: staying
// inside them means a profile that validates here cannot make loudnorm refuse
// to start.
const (
	MinTargetLUFS  = -70.0
	MaxTargetLUFS  = -5.0
	MinTruePeakDB  = -9.0
	MaxTruePeakDB  = 0.0
	MinLoudnessLRA = 1.0
	MaxLoudnessLRA = 50.0
	MaxLabelLen    = 64 // free-text label ceiling; a label, not a description
	MaxLangTagLen  = 35 // longest BCP-47 tag anyone sends in practice
)

// Common loudness targets, offered as named starting points rather than forced
// on anyone: the right number depends entirely on where the stream is going.
const (
	LUFSStreaming = -14.0 // YouTube, Twitch, Spotify: what their own normalizer targets
	LUFSPodcast   = -16.0 // Apple Podcasts / spoken word
	LUFSBroadcast = -23.0 // EBU R128
)

// DefaultTruePeakDB is the ceiling used when a loudness target names none.
// -1 dBTP leaves headroom for the lossy encoder downstream to overshoot into,
// which is the whole reason true peak is measured separately from sample peak.
const DefaultTruePeakDB = -1.0

// DefaultLoudnessLRA matches the loudnorm stage that shipped before loudness
// targets were configurable, so an existing loudnorm profile that gains an
// explicit target keeps the same dynamics behaviour.
const DefaultLoudnessLRA = 11.0

// Ducking parameter bounds and defaults, matching sidechaincompress' accepted
// ranges. The defaults are a mic-over-music duck that is audible without
// pumping: about 12 dB of gain reduction, fast enough to catch a word's first
// syllable, slow enough not to chatter between them.
const (
	MinDuckThresholdDB = -60.0
	MaxDuckThresholdDB = 0.0
	MinDuckRatio       = 1.0
	MaxDuckRatio       = 20.0
	MinDuckAttackMS    = 0.01
	MaxDuckAttackMS    = 2000.0
	MinDuckReleaseMS   = 0.01
	MaxDuckReleaseMS   = 9000.0

	DefaultDuckThresholdDB = -24.0
	DefaultDuckRatio       = 8.0
	DefaultDuckAttackMS    = 20.0
	DefaultDuckReleaseMS   = 300.0
)

// Mode selects which half of the Profile is authoritative.
type Mode string

const (
	// ModeSimple: a checkbox + gain per track. Tracks are stereo-downmixed
	// with standard coefficients, then summed.
	ModeSimple Mode = "simple"
	// ModeMatrix: a full channel->output matrix with a gain per cell.
	// Subsumes ModeSimple.
	ModeMatrix Mode = "matrix"
)

// NormMode selects the clip-protection stage appended after the sum.
type NormMode string

const (
	// NormAuto engages the limiter exactly when two or more tracks are being
	// summed, which is the only case that can introduce new clipping. This is
	// the default, and it is why the field is not a plain bool: "off" and
	// "auto" mean different things for a single-track profile.
	NormAuto     NormMode = "auto"
	NormOff      NormMode = "off"
	NormLimiter  NormMode = "limiter"
	NormLoudnorm NormMode = "loudnorm"
)

// Output channel indices. v1 destinations are always stereo, because that is
// what every live platform ingest accepts.
const (
	OutL = 0
	OutR = 1
	// OutChannels is the destination channel count.
	OutChannels = 2
)

// TrackSel is one row of the simple-mode editor.
type TrackSel struct {
	Track   int     `json:"track"`   // 0-based ingest track index
	Enabled bool    `json:"enabled"` //
	Gain    float64 `json:"gain"`    // 1.0 == unity == "100%" in the UI
}

// Cell is one square of the advanced mix matrix: how much of one source
// channel of one track lands in one destination output channel.
type Cell struct {
	Track   int     `json:"track"`
	Channel int     `json:"channel"` // 0-based channel within that track
	Out     int     `json:"out"`     // OutL | OutR
	Gain    float64 `json:"gain"`    // 0.0 .. MaxGain
}

// Loudness is a destination's programme-loudness target.
//
// It parameterizes the loudnorm stage rather than replacing it: a profile with
// no Loudness compiles exactly as it always did. Setting one is how a
// destination says "-14 LUFS because that is what YouTube normalizes to"
// instead of inheriting the one hard-coded number that used to be the only
// option.
type Loudness struct {
	// TargetLUFS is the integrated programme loudness to land on, e.g. -14.
	TargetLUFS float64 `json:"targetLufs"`
	// TruePeakDB is the inter-sample peak ceiling in dBTP, e.g. -1.
	// Zero means DefaultTruePeakDB: a 0 dBTP ceiling is never what anyone
	// intends, and JSON payloads omit the fields they have no opinion about.
	TruePeakDB float64 `json:"truePeakDb,omitempty"`
	// RangeLU is the target loudness range (loudnorm's LRA). Zero means
	// DefaultLoudnessLRA.
	RangeLU float64 `json:"rangeLu,omitempty"`
}

// Ducking pulls one group of tracks down whenever another is speaking — the
// mic-over-music duck, expressed in terms of ingest tracks so it survives the
// user re-arranging their mix.
//
// Trigger and Target name source tracks, not destination channels, because the
// thing being detected ("is the streamer talking?") is a property of what was
// sent, and every destination that carries both tracks wants the same answer.
type Ducking struct {
	// Trigger tracks are listened to; when they get loud, Target tracks duck.
	Trigger []int `json:"trigger"`
	// Target tracks are the ones pushed down. A track cannot duck itself.
	Target []int `json:"target"`
	// ThresholdDB is the level the trigger must exceed. Zero means
	// DefaultDuckThresholdDB — a 0 dBFS threshold would never fire, so it
	// cannot be a meaningful explicit value.
	ThresholdDB float64 `json:"thresholdDb,omitempty"`
	// Ratio, AttackMS and ReleaseMS shape the duck. Zero means the default.
	Ratio     float64 `json:"ratio,omitempty"`
	AttackMS  float64 `json:"attackMs,omitempty"`
	ReleaseMS float64 `json:"releaseMs,omitempty"`
}

// Profile is a destination's complete audio routing configuration.
//
// Everything below SampleRate arrived after v1 and is optional: a Profile with
// those fields at their zero value compiles to byte-identical FFmpeg arguments,
// which is what lets every saved profile survive an upgrade untouched.
type Profile struct {
	Mode       Mode       `json:"mode"`
	Tracks     []TrackSel `json:"tracks"`
	Matrix     []Cell     `json:"matrix"`
	Normalize  NormMode   `json:"normalize"`
	SampleRate int        `json:"sampleRate"`

	// Loudness is the programme-loudness target. Nil keeps the fixed loudnorm
	// parameters that shipped with NormLoudnorm.
	Loudness *Loudness `json:"loudness,omitempty"`

	// DelayMS offsets this destination's audio against its video. Positive
	// holds audio back (lip-sync, moderation delay); negative pulls it ahead.
	DelayMS int `json:"delayMs,omitempty"`

	// Ducking is the optional mic-over-music duck. Nil means no ducking.
	Ducking *Ducking `json:"ducking,omitempty"`

	// ExcludeRoles drops any ingest track carrying one of these roles before
	// the mix is built. This is the DMCA switch: mark the music track once, on
	// the source, then set ExcludeRoles=[music] on the destinations that cannot
	// carry it, and the exclusion keeps working when the streamer moves music
	// to a different track.
	ExcludeRoles []TrackRole `json:"excludeRoles,omitempty"`
}

// Track describes one audio stream as it actually arrives from the ingest.
// Populated by ffprobe against the relay, not by the user.
type Track struct {
	Index    int    `json:"index"`    // 0-based; maps to the a:N stream specifier
	Channels int    `json:"channels"` //
	Codec    string `json:"codec"`
	Layout   string `json:"layout"`
	Language string `json:"language"`
	Title    string `json:"title"`
}

// TrackRole is what a streamer says an ingest track *is*. It is the difference
// between "track 3" and "the Spanish commentary", and between "track 2" and
// "the licensed music that must not reach the archive".
//
// Roles are advisory metadata, never a routing decision on their own: nothing
// in the compiler consults a role unless a destination explicitly asks it to.
type TrackRole string

const (
	// RoleUnset is every track that predates the feature, and every track the
	// operator never got around to describing. It is the zero value on purpose.
	RoleUnset      TrackRole = ""
	RoleMusic      TrackRole = "music"
	RoleMic        TrackRole = "mic"
	RoleGame       TrackRole = "game"
	RoleCommentary TrackRole = "commentary"
	RoleClean      TrackRole = "clean"
	RoleOther      TrackRole = "other"
)

// TrackRoles is the catalogue the UI offers, in the order it should show them.
// RoleUnset is omitted: "no role" is the absence of an annotation, not a
// choice to be made.
func TrackRoles() []TrackRole {
	return []TrackRole{RoleMic, RoleCommentary, RoleGame, RoleMusic, RoleClean, RoleOther}
}

// ValidRole reports whether r is a role this build understands. RoleUnset
// counts: an annotation may carry only a label.
func ValidRole(r TrackRole) bool {
	switch r {
	case RoleUnset, RoleMusic, RoleMic, RoleGame, RoleCommentary, RoleClean, RoleOther:
		return true
	}
	return false
}

// TrackAnnotation is the operator's description of one ingest track.
//
// It is deliberately not a field on Track: Track is rebuilt from ffprobe every
// time the ingest is re-probed, so anything a human typed has to live beside
// it or be destroyed by the next reconnect.
type TrackAnnotation struct {
	// Track is the 0-based ingest track index being described.
	Track int `json:"track"`
	// Role is what this track carries.
	Role TrackRole `json:"role,omitempty"`
	// Label is free text for the operator, e.g. "Guest mic (Zoom)".
	Label string `json:"label,omitempty"`
	// Language is a BCP-47 tag, e.g. "es" or "pt-BR". It overrides whatever
	// the container claimed, because encoders lie about this constantly and
	// the operator is the one who knows.
	Language string `json:"language,omitempty"`
	// Denoise asks for noise suppression on this track wherever it is used.
	// A noisy room is noisy for every destination, so the flag belongs to the
	// source rather than being re-decided per destination.
	Denoise bool `json:"denoise,omitempty"`
}

// Source is the set of audio tracks currently present on the ingest.
type Source struct {
	Tracks []Track `json:"tracks"`
	// Annotations describe those tracks. They are keyed by track index rather
	// than by position, so an annotation survives a track disappearing and
	// coming back.
	Annotations []TrackAnnotation `json:"annotations,omitempty"`
}

// TrackByIndex returns the track with the given index, if present.
func (s Source) TrackByIndex(i int) (Track, bool) {
	for _, t := range s.Tracks {
		if t.Index == i {
			return t, true
		}
	}
	return Track{}, false
}

// WithAnnotations returns a copy of s carrying the given annotations. The
// engine builds Source from a probe and then hangs the saved annotations off
// it here, so that probing never has to know they exist.
func (s Source) WithAnnotations(a []TrackAnnotation) Source {
	s.Annotations = append([]TrackAnnotation(nil), a...)
	return s
}

// Annotation returns the annotation for a track index, if one was recorded.
func (s Source) Annotation(i int) (TrackAnnotation, bool) {
	for _, a := range s.Annotations {
		if a.Track == i {
			return a, true
		}
	}
	return TrackAnnotation{}, false
}

// RoleOf returns a track's role, or RoleUnset when it has none.
func (s Source) RoleOf(i int) TrackRole {
	a, _ := s.Annotation(i)
	return a.Role
}

// LanguageOf returns the best language tag known for a track: what the
// operator said, falling back to what the container claimed.
func (s Source) LanguageOf(i int) string {
	if a, ok := s.Annotation(i); ok && a.Language != "" {
		return a.Language
	}
	t, _ := s.TrackByIndex(i)
	return t.Language
}

// LabelOf returns the best human name known for a track: the operator's label,
// then the container's title, then "" for callers to fall back to "Track N".
func (s Source) LabelOf(i int) string {
	if a, ok := s.Annotation(i); ok && a.Label != "" {
		return a.Label
	}
	t, _ := s.TrackByIndex(i)
	return t.Title
}

// DenoiseTrack reports whether a track was marked for noise suppression.
func (s Source) DenoiseTrack(i int) bool {
	a, _ := s.Annotation(i)
	return a.Denoise
}

// TracksWithRole returns the ascending track indices carrying a role. Used to
// answer "where did the Spanish commentary go?" and to resolve ExcludeRoles.
func (s Source) TracksWithRole(r TrackRole) []int {
	var out []int
	for _, a := range s.Annotations {
		if a.Role == r {
			out = append(out, a.Track)
		}
	}
	sort.Ints(out)
	return out
}

// TracksWithLanguage returns the ascending track indices whose effective
// language tag matches, compared case-insensitively on the whole tag and on
// the primary subtag, so "es" finds "es-419" and "es-419" finds a track
// labelled only "es" — the best available answer, not a miss.
//
// An annotated track the ingest is not currently carrying is still returned.
// A stream that momentarily drops a track has not changed what that track is,
// and Compile already downgrades a missing track to a warning; hiding it here
// would make the answer flap with the network.
func (s Source) TracksWithLanguage(tag string) []int {
	want := strings.ToLower(strings.TrimSpace(tag))
	if want == "" {
		return nil
	}
	seen := map[int]bool{}
	var out []int
	consider := func(idx int) {
		if seen[idx] {
			return
		}
		got := strings.ToLower(s.LanguageOf(idx))
		if got == "" {
			return
		}
		if got == want || primarySubtag(got) == want || got == primarySubtag(want) {
			seen[idx] = true
			out = append(out, idx)
		}
	}
	for _, t := range s.Tracks {
		consider(t.Index)
	}
	for _, a := range s.Annotations {
		consider(a.Track)
	}
	sort.Ints(out)
	return out
}

func primarySubtag(tag string) string {
	if i := strings.IndexByte(tag, '-'); i >= 0 {
		return tag[:i]
	}
	return tag
}

// DefaultSource is what we assume before the ingest has been probed: six
// stereo tracks. It lets the UI render a routing editor and lets a destination
// be configured before the stream has ever come up.
//
// Bounded by PlaceholderTracks rather than MaxTracks, which used to be the same
// number and no longer is. The engine leans on this being small and concrete:
// it substitutes this layout for an unprobed ingest, so widening it would mean
// compiling meter and mix commands against tracks that do not exist.
func DefaultSource() Source {
	s := Source{}
	for i := 0; i < PlaceholderTracks; i++ {
		s.Tracks = append(s.Tracks, Track{Index: i, Channels: 2, Codec: "aac", Layout: "stereo"})
	}
	return s
}

// DefaultProfile is what a newly created destination gets: track 1 only, at
// unity, no normalization (a single track cannot sum-clip).
func DefaultProfile() Profile {
	p := Profile{
		Mode:       ModeSimple,
		Normalize:  NormAuto,
		SampleRate: 48000,
	}
	for i := 0; i < MaxTracks; i++ {
		p.Tracks = append(p.Tracks, TrackSel{Track: i, Enabled: i == 0, Gain: 1.0})
	}
	return p
}

// ValidationError reports every problem with a profile at once, so the UI can
// mark up the whole form instead of surfacing one error per round trip.
type ValidationError struct {
	Problems []string
	// Subject names what was being validated. Empty means "routing profile",
	// which is what every caller predating track annotations produces — and
	// keeps their error strings byte-identical.
	Subject string `json:"subject,omitempty"`
}

func (e *ValidationError) Error() string {
	subject := e.Subject
	if subject == "" {
		subject = "routing profile"
	}
	if len(e.Problems) == 1 {
		return "invalid " + subject + ": " + e.Problems[0]
	}
	return fmt.Sprintf("invalid %s: %d problems: %v", subject, len(e.Problems), e.Problems)
}

// ErrNoAudio is returned when a profile selects nothing at all.
var ErrNoAudio = errors.New("routing profile selects no audio")

// IsUnset reports whether a profile carries no routing information at all,
// which is what a create-destination request looks like: the user names the
// endpoint first and configures the mix afterwards. Callers substitute
// DefaultProfile rather than letting ApplyDefaults produce an all-disabled
// profile that then fails validation.
func (p Profile) IsUnset() bool {
	return len(p.Tracks) == 0 && len(p.Matrix) == 0
}

// ApplyDefaults fills in defaults for a partially specified profile. It is
// applied on the way in from the API so that older/sparser payloads stay
// accepted. (It cannot be called Normalize — that name is taken by the field.)
func (p *Profile) ApplyDefaults() {
	if p.Mode == "" {
		p.Mode = ModeSimple
	}
	if p.Normalize == "" {
		p.Normalize = NormAuto
	}
	if p.SampleRate == 0 {
		p.SampleRate = 48000
	}
	if p.Mode == ModeSimple {
		// Guarantee a full set of rows so the UI always renders 6 checkboxes.
		seen := map[int]bool{}
		for i := range p.Tracks {
			seen[p.Tracks[i].Track] = true
			if p.Tracks[i].Gain == 0 && !p.Tracks[i].Enabled {
				p.Tracks[i].Gain = 1.0
			}
		}
		for i := 0; i < MaxTracks; i++ {
			if !seen[i] {
				p.Tracks = append(p.Tracks, TrackSel{Track: i, Gain: 1.0})
			}
		}
		sort.Slice(p.Tracks, func(a, b int) bool { return p.Tracks[a].Track < p.Tracks[b].Track })
	}

	// The optional stages are only ever defaulted *within* themselves. A nil
	// Loudness stays nil, a nil Ducking stays nil, DelayMS stays 0: a profile
	// that never opted in must come out of here exactly as it went in.
	if p.Loudness != nil {
		p.Loudness.applyDefaults()
	}
	if p.Ducking != nil {
		p.Ducking.applyDefaults()
	}
}

func (l *Loudness) applyDefaults() {
	if l.TruePeakDB == 0 {
		l.TruePeakDB = DefaultTruePeakDB
	}
	if l.RangeLU == 0 {
		l.RangeLU = DefaultLoudnessLRA
	}
}

func (d *Ducking) applyDefaults() {
	if d.ThresholdDB == 0 {
		d.ThresholdDB = DefaultDuckThresholdDB
	}
	if d.Ratio == 0 {
		d.Ratio = DefaultDuckRatio
	}
	if d.AttackMS == 0 {
		d.AttackMS = DefaultDuckAttackMS
	}
	if d.ReleaseMS == 0 {
		d.ReleaseMS = DefaultDuckReleaseMS
	}
}

// EffectiveLoudness returns the loudness target that actually applies, with
// unset parameters filled in, and whether one applies at all.
//
// A target arms the loudnorm stage under NormAuto — asking for -14 LUFS is
// asking for loudness normalization, and auto's job is to do the right thing.
// It does not override NormOff or NormLimiter: those are explicit instructions
// from an operator who has already decided, and quietly swapping their limiter
// for a loudness pass is exactly the kind of surprise that gets noticed on air.
func (p Profile) EffectiveLoudness() (Loudness, bool) {
	if p.Loudness == nil {
		return Loudness{}, false
	}
	switch p.Normalize {
	case NormLoudnorm, NormAuto:
		l := *p.Loudness
		l.applyDefaults()
		return l, true
	}
	return Loudness{}, false
}

// EffectiveDucking returns the duck with defaults filled in, and whether one
// is configured at all.
func (p Profile) EffectiveDucking() (Ducking, bool) {
	if p.Ducking == nil || len(p.Ducking.Trigger) == 0 || len(p.Ducking.Target) == 0 {
		return Ducking{}, false
	}
	d := *p.Ducking
	d.Trigger = append([]int(nil), d.Trigger...)
	d.Target = append([]int(nil), d.Target...)
	d.applyDefaults()
	return d, true
}

// ExcludesRole reports whether this destination refuses tracks with role r.
func (p Profile) ExcludesRole(r TrackRole) bool {
	if r == RoleUnset {
		// An unlabelled track is not evidence of anything. Excluding on
		// RoleUnset would silently drop every track the operator never got
		// around to describing, so it is rejected by Validate and ignored here.
		return false
	}
	for _, x := range p.ExcludeRoles {
		if x == r {
			return true
		}
	}
	return false
}

// ExcludedTracks returns the ascending source track indices this destination
// refuses because of their role. Empty for every profile that sets no policy,
// which is the compiler's fast path.
func (p Profile) ExcludedTracks(src Source) []int {
	if len(p.ExcludeRoles) == 0 || len(src.Annotations) == 0 {
		return nil
	}
	var out []int
	for _, a := range src.Annotations {
		if p.ExcludesRole(a.Role) {
			out = append(out, a.Track)
		}
	}
	sort.Ints(out)
	return out
}

// Validate checks a profile in isolation — i.e. without reference to what the
// ingest is actually carrying. Source-dependent problems (selecting a track
// that isn't being sent) are surfaced by Compile as warnings instead, because
// a stream that temporarily drops a track should not invalidate saved config.
func (p Profile) Validate() error {
	var probs []string

	switch p.Mode {
	case ModeSimple, ModeMatrix:
	default:
		probs = append(probs, fmt.Sprintf("unknown mode %q", p.Mode))
	}

	switch p.Normalize {
	case NormAuto, NormOff, NormLimiter, NormLoudnorm:
	default:
		probs = append(probs, fmt.Sprintf("unknown normalize mode %q", p.Normalize))
	}

	switch p.SampleRate {
	case 44100, 48000:
	default:
		probs = append(probs, fmt.Sprintf("unsupported sample rate %d (want 44100 or 48000)", p.SampleRate))
	}

	switch p.Mode {
	case ModeSimple:
		seen := map[int]bool{}
		any := false
		for _, t := range p.Tracks {
			if t.Track < 0 || t.Track >= MaxTracks {
				probs = append(probs, fmt.Sprintf("track %d out of range (0..%d)", t.Track, MaxTracks-1))
				continue
			}
			if seen[t.Track] {
				probs = append(probs, fmt.Sprintf("duplicate entry for track %d", t.Track))
			}
			seen[t.Track] = true
			if t.Gain < 0 || t.Gain > MaxGain {
				probs = append(probs, fmt.Sprintf("track %d gain %.3f out of range (0..%.1f)", t.Track, t.Gain, MaxGain))
			}
			if t.Enabled && t.Gain > 0 {
				any = true
			}
		}
		if !any {
			probs = append(probs, "no track is enabled with non-zero gain")
		}

	case ModeMatrix:
		type key struct{ t, c, o int }
		seen := map[key]bool{}
		any := false
		for _, c := range p.Matrix {
			if c.Track < 0 || c.Track >= MaxTracks {
				probs = append(probs, fmt.Sprintf("matrix cell track %d out of range (0..%d)", c.Track, MaxTracks-1))
				continue
			}
			if c.Channel < 0 || c.Channel >= MaxChannels {
				probs = append(probs, fmt.Sprintf("matrix cell track %d channel %d out of range (0..%d)", c.Track, c.Channel, MaxChannels-1))
				continue
			}
			if c.Out < 0 || c.Out >= OutChannels {
				probs = append(probs, fmt.Sprintf("matrix cell track %d channel %d has output %d (want 0 or 1)", c.Track, c.Channel, c.Out))
				continue
			}
			k := key{c.Track, c.Channel, c.Out}
			if seen[k] {
				probs = append(probs, fmt.Sprintf("duplicate matrix cell track %d channel %d out %d", c.Track, c.Channel, c.Out))
			}
			seen[k] = true
			if c.Gain < 0 || c.Gain > MaxGain {
				probs = append(probs, fmt.Sprintf("matrix cell track %d channel %d gain %.3f out of range (0..%.1f)", c.Track, c.Channel, c.Gain, MaxGain))
			}
			if c.Gain > 0 {
				any = true
			}
		}
		if !any {
			probs = append(probs, "matrix has no cell with non-zero gain")
		}
	}

	probs = append(probs, p.validateOptional()...)

	if len(probs) > 0 {
		sort.Strings(probs)
		return &ValidationError{Problems: probs}
	}
	return nil
}

// validateOptional checks the post-v1 fields. It is separate so that the shape
// of the original validation stays readable, and so that every check here can
// start from "the zero value is always fine".
func (p Profile) validateOptional() []string {
	var probs []string

	if p.DelayMS < MinDelayMS || p.DelayMS > MaxDelayMS {
		probs = append(probs, fmt.Sprintf("audio delay %d ms out of range (%d..%d)", p.DelayMS, MinDelayMS, MaxDelayMS))
	}

	if l := p.Loudness; l != nil {
		if l.TargetLUFS < MinTargetLUFS || l.TargetLUFS > MaxTargetLUFS {
			probs = append(probs, fmt.Sprintf("loudness target %.1f LUFS out of range (%.0f..%.0f)", l.TargetLUFS, MinTargetLUFS, MaxTargetLUFS))
		}
		if l.TruePeakDB < MinTruePeakDB || l.TruePeakDB > MaxTruePeakDB {
			probs = append(probs, fmt.Sprintf("true-peak ceiling %.1f dBTP out of range (%.0f..%.0f)", l.TruePeakDB, MinTruePeakDB, MaxTruePeakDB))
		}
		if l.RangeLU != 0 && (l.RangeLU < MinLoudnessLRA || l.RangeLU > MaxLoudnessLRA) {
			probs = append(probs, fmt.Sprintf("loudness range %.1f LU out of range (%.0f..%.0f)", l.RangeLU, MinLoudnessLRA, MaxLoudnessLRA))
		}
	}

	for _, r := range p.ExcludeRoles {
		switch {
		case r == RoleUnset:
			// Excluding "no role" would drop every track nobody has described
			// yet, which on a fresh install is all of them.
			probs = append(probs, "excludeRoles cannot contain the empty role")
		case !ValidRole(r):
			probs = append(probs, fmt.Sprintf("unknown track role %q in excludeRoles", r))
		}
	}

	probs = append(probs, p.Ducking.validate()...)
	return probs
}

func (d *Ducking) validate() []string {
	if d == nil {
		return nil
	}
	var probs []string

	trigger := validateDuckGroup("ducking trigger", d.Trigger, &probs)
	target := validateDuckGroup("ducking target", d.Target, &probs)
	for t := range trigger {
		if target[t] {
			probs = append(probs, fmt.Sprintf("track %d cannot duck itself: it is both a ducking trigger and a target", t))
		}
	}

	if d.ThresholdDB < MinDuckThresholdDB || d.ThresholdDB > MaxDuckThresholdDB {
		probs = append(probs, fmt.Sprintf("ducking threshold %.1f dB out of range (%.0f..%.0f)", d.ThresholdDB, MinDuckThresholdDB, MaxDuckThresholdDB))
	}
	if d.Ratio != 0 && (d.Ratio < MinDuckRatio || d.Ratio > MaxDuckRatio) {
		probs = append(probs, fmt.Sprintf("ducking ratio %.1f out of range (%.0f..%.0f)", d.Ratio, MinDuckRatio, MaxDuckRatio))
	}
	if d.AttackMS != 0 && (d.AttackMS < MinDuckAttackMS || d.AttackMS > MaxDuckAttackMS) {
		probs = append(probs, fmt.Sprintf("ducking attack %.2f ms out of range (%.2f..%.0f)", d.AttackMS, MinDuckAttackMS, MaxDuckAttackMS))
	}
	if d.ReleaseMS != 0 && (d.ReleaseMS < MinDuckReleaseMS || d.ReleaseMS > MaxDuckReleaseMS) {
		probs = append(probs, fmt.Sprintf("ducking release %.2f ms out of range (%.2f..%.0f)", d.ReleaseMS, MinDuckReleaseMS, MaxDuckReleaseMS))
	}
	return probs
}

// validateDuckGroup checks one side of a duck and returns its track set.
func validateDuckGroup(what string, tracks []int, probs *[]string) map[int]bool {
	seen := map[int]bool{}
	if len(tracks) == 0 {
		*probs = append(*probs, what+" selects no track")
		return seen
	}
	for _, t := range tracks {
		if t < 0 || t >= MaxTracks {
			*probs = append(*probs, fmt.Sprintf("%s track %d out of range (0..%d)", what, t, MaxTracks-1))
			continue
		}
		if seen[t] {
			*probs = append(*probs, fmt.Sprintf("duplicate %s track %d", what, t))
		}
		seen[t] = true
	}
	return seen
}

// ValidateAnnotations checks a set of track annotations, reporting every
// problem at once like Validate does.
//
// The language check is deliberately shallow. A registry lookup would reject
// perfectly good private-use and regional tags, and this repo has learned three
// times what an over-strict check costs: refusing "es-419" because a table was
// stale is worse than accepting a typo nobody ever reads.
func ValidateAnnotations(anns []TrackAnnotation) error {
	var probs []string
	seen := map[int]bool{}

	for _, a := range anns {
		if a.Track < 0 || a.Track >= MaxTracks {
			probs = append(probs, fmt.Sprintf("track %d out of range (0..%d)", a.Track, MaxTracks-1))
			continue
		}
		if seen[a.Track] {
			probs = append(probs, fmt.Sprintf("duplicate annotation for track %d", a.Track))
		}
		seen[a.Track] = true

		if !ValidRole(a.Role) {
			probs = append(probs, fmt.Sprintf("track %d has unknown role %q", a.Track, a.Role))
		}
		if len(a.Label) > MaxLabelLen {
			probs = append(probs, fmt.Sprintf("track %d label is %d characters (max %d)", a.Track, len(a.Label), MaxLabelLen))
		}
		if a.Language != "" && !plausibleLangTag(a.Language) {
			probs = append(probs, fmt.Sprintf("track %d language %q is not a language tag", a.Track, a.Language))
		}
	}

	if len(probs) > 0 {
		sort.Strings(probs)
		return &ValidationError{Problems: probs, Subject: "track annotations"}
	}
	return nil
}

// plausibleLangTag reports whether s has the *shape* of a BCP-47 tag: one to
// eight alphanumerics per subtag, hyphen separated. It is a typo catcher, not
// a registry.
func plausibleLangTag(s string) bool {
	if len(s) > MaxLangTagLen {
		return false
	}
	for _, sub := range strings.Split(s, "-") {
		if len(sub) < 1 || len(sub) > 8 {
			return false
		}
		for _, r := range sub {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			default:
				return false
			}
		}
	}
	return true
}

// SelectedTracks returns the 0-based track indices the profile draws from,
// ascending. Used for the destination-card summary ("Tracks 1, 2, 4 -> stereo").
func (p Profile) SelectedTracks() []int {
	seen := map[int]bool{}
	switch p.Mode {
	case ModeMatrix:
		for _, c := range p.Matrix {
			if c.Gain > 0 {
				seen[c.Track] = true
			}
		}
	default:
		for _, t := range p.Tracks {
			if t.Enabled && t.Gain > 0 {
				seen[t.Track] = true
			}
		}
	}
	out := make([]int, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}
