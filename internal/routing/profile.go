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
)

// MaxTracks is the number of audio tracks polyemesis accepts from an ingest.
// Matches OBS' six-track limit.
const MaxTracks = 6

// MaxChannels is the largest per-track channel count we build a downmix for.
const MaxChannels = 8

// MaxGain caps any single gain coefficient. 2.0 == +6 dB, which is as much
// boost as is defensible before the limiter is doing all the work.
const MaxGain = 2.0

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

// Profile is a destination's complete audio routing configuration.
type Profile struct {
	Mode       Mode       `json:"mode"`
	Tracks     []TrackSel `json:"tracks"`
	Matrix     []Cell     `json:"matrix"`
	Normalize  NormMode   `json:"normalize"`
	SampleRate int        `json:"sampleRate"`
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

// Source is the set of audio tracks currently present on the ingest.
type Source struct {
	Tracks []Track `json:"tracks"`
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

// DefaultSource is what we assume before the ingest has been probed: six
// stereo tracks. It lets the UI render a routing editor and lets a destination
// be configured before the stream has ever come up.
func DefaultSource() Source {
	s := Source{}
	for i := 0; i < MaxTracks; i++ {
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
}

func (e *ValidationError) Error() string {
	if len(e.Problems) == 1 {
		return "invalid routing profile: " + e.Problems[0]
	}
	return fmt.Sprintf("invalid routing profile: %d problems: %v", len(e.Problems), e.Problems)
}

// ErrNoAudio is returned when a profile selects nothing at all.
var ErrNoAudio = errors.New("routing profile selects no audio")

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

	if len(probs) > 0 {
		sort.Strings(probs)
		return &ValidationError{Problems: probs}
	}
	return nil
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
