// Package clipper cuts a clip out of a recording that is already on disk.
//
// NOT TO BE CONFUSED WITH internal/clips, which is a live ring buffer over the
// relay answering "give me the last thirty seconds of what is happening right
// now". This package answers a different question, hours later: "here are the
// in and out points I chose in a timeline, hand me that piece of the archive".
// The inputs are files, not datagrams; the output is keyframe-accurate rather
// than merely playable; and nothing here touches the streaming path at all.
//
// The whole design turns on one property of a stream copy: it can only start on
// a KEYFRAME. Cutting mid-GOP produces a file whose leading frames reference
// pictures that are not in it, which decodes to grey mush or to nothing. So a
// cut is either
//
//   - FAST: snap the in-point back to the keyframe at or before it, copy every
//     packet, finish in seconds with the source's exact bytes. The clip is up to
//     one GOP longer at the head than what was asked for, and this package
//     always reports by how much. Right for archiving a forty-minute segment.
//
//   - PRECISE: re-encode only the leading partial GOP, then stream-copy the
//     rest and join the two. Costs one short encode instead of a whole-file
//     one, lands on the exact requested frame, and leaves the overwhelming
//     majority of the clip bit-exact. Right for a ten-second social clip, where
//     three seconds of slack at the head is the difference between a good clip
//     and a bad one.
//
// A silently moved in-point is the failure mode this package exists to avoid.
// Every Plan carries the requested in-point, the delivered one, and the drift
// between them, so a caller can show a human "your cut starts 1.8s earlier than
// you asked" instead of leaving them to discover it in the file.
package clipper

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// Mode selects the tradeoff between speed and accuracy at the in-point.
type Mode string

const (
	// ModeFast is a pure stream copy. Lossless, seconds long, and the in-point
	// moves backwards to the nearest keyframe.
	ModeFast Mode = "fast"
	// ModePrecise re-encodes the leading partial GOP so the in-point is exact,
	// and copies everything after it.
	ModePrecise Mode = "precise"
)

// DefaultMode is what an unset Mode means.
//
// Fast, because it is the mode that cannot surprise anybody: it never re-encodes
// a single frame, so the worst case is a clip that is slightly longer than
// asked for — which the Plan states out loud.
const DefaultMode = ModeFast

// Modes is the catalogue a UI should offer, in the order to show it.
func Modes() []Mode { return []Mode{ModeFast, ModePrecise} }

// ValidMode reports whether m is a mode this build understands. The empty
// string counts: it means DefaultMode.
func ValidMode(m Mode) bool {
	switch m {
	case "", ModeFast, ModePrecise:
		return true
	}
	return false
}

// AudioMode selects which of the recording's audio tracks the clip keeps.
type AudioMode string

const (
	// AudioAll keeps every track, bit-exact. The default, because a multitrack
	// master that loses its tracks on the way into a clip is not a master.
	AudioAll AudioMode = "all"
	// AudioTracks keeps a named subset, still bit-exact. "Just the mic" out of a
	// six-track recording, with no encode.
	AudioTracks AudioMode = "tracks"
	// AudioMix folds the tracks down through a routing profile into one stream.
	// This is the only audio mode that re-encodes, because a mix is by
	// definition new samples.
	AudioMix AudioMode = "mix"
)

// AudioSelection describes what happens to the recording's audio.
type AudioSelection struct {
	Mode AudioMode `json:"mode"`
	// Tracks are 0-based ingest track indices, matching routing.Track.Index and
	// the a:N stream specifier. Only read for AudioTracks.
	Tracks []int `json:"tracks,omitempty"`

	// Profile and Source are handed straight to routing.Compile for AudioMix,
	// so a clip mixes down exactly the way a destination would.
	Profile routing.Profile `json:"profile,omitzero"`
	Source  routing.Source  `json:"source,omitzero"`

	// Codec is what a mix is encoded as. Empty picks per container: FLAC into
	// Matroska, AAC everywhere else. Ignored unless Mode is AudioMix.
	Codec string `json:"codec,omitempty"`
	// Kbps is the mix bitrate. Ignored for lossless codecs and when zero.
	Kbps int `json:"kbps,omitempty"`
}

// Container is the output muxer, inferred from the output filename.
type Container string

const (
	ContainerMatroska Container = "matroska"
	ContainerMP4      Container = "mp4"
	ContainerMPEGTS   Container = "mpegts"
)

// DefaultContainer is what an unrecognised extension falls back to. Matroska,
// because it is the only one of the three that carries an arbitrary number of
// audio tracks in an arbitrary codec — which is the whole reason the recorder
// writes MKV in the first place.
const DefaultContainer = ContainerMatroska

// containerFor maps an output filename to its muxer.
//
// Unknown extensions get Matroska rather than an error. A caller who names a
// clip ".mkv.tmp" while it is being written should get a clip, not a refusal.
func containerFor(path string) Container {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v", ".mov":
		return ContainerMP4
	case ".ts", ".m2ts", ".mts":
		return ContainerMPEGTS
	case ".mkv", ".mka":
		return ContainerMatroska
	}
	return DefaultContainer
}

// Segment is one recording file as it sits on the clip timeline.
//
// The recorder writes hourly files, so a clip whose in and out points are forty
// minutes apart may still cross a boundary. Start is what makes several files
// addressable as one continuous timeline.
type Segment struct {
	Path string `json:"path"`
	// Start is where this file begins on the timeline, i.e. the offset a caller
	// would scrub to in order to see its first frame.
	Start time.Duration `json:"start"`
	// Duration is how long the file runs.
	Duration time.Duration `json:"duration"`
}

// End is the timeline position one instant past this segment's last frame.
func (s Segment) End() time.Duration { return s.Start + s.Duration }

// Timeline is the ordered set of segment files a clip can be cut from.
type Timeline struct {
	segs []Segment
	// gaps records the timeline positions where consecutive segments do not
	// meet — a recorder restart, or a file deleted by retention out of the
	// middle of a run.
	gaps []time.Duration
}

// Errors callers are expected to branch on.
var (
	// ErrNoSegments is a timeline with nothing in it.
	ErrNoSegments = errors.New("clipper: no recording segments")
	// ErrEmptyRange is an out-point at or before the in-point.
	ErrEmptyRange = errors.New("clipper: the out-point is not after the in-point")
	// ErrOutOfRange is a range that does not overlap the timeline at all.
	ErrOutOfRange = errors.New("clipper: the requested range is outside the recording")
	// ErrInvalidRequest is a request that is wrong on its own terms. It is
	// separate from the three above so a queue can tell "this can never work"
	// from "this attempt did not work" and stop retrying the first.
	ErrInvalidRequest = errors.New("clipper: invalid request")
)

// NewTimeline lays segments out in order.
//
// Two accepted shapes, because callers arrive from two directions. A caller
// that knows each file's wall-clock start converts those to offsets and fills
// Start; a caller that only has "these files, in this order" leaves every Start
// at zero and gets them laid end to end. The second is detected rather than
// configured: several segments all claiming to start at zero cannot mean what
// it says.
func NewTimeline(segs []Segment) (Timeline, error) {
	clean := make([]Segment, 0, len(segs))
	for _, s := range segs {
		if s.Path == "" || s.Duration <= 0 {
			// A zero-length or nameless segment cannot contribute frames, and
			// keeping it would only put a gap in the middle of the timeline.
			continue
		}
		clean = append(clean, s)
	}
	if len(clean) == 0 {
		return Timeline{}, ErrNoSegments
	}

	allZero := true
	for _, s := range clean {
		if s.Start != 0 {
			allZero = false
			break
		}
	}
	if allZero && len(clean) > 1 {
		var at time.Duration
		for i := range clean {
			clean[i].Start = at
			at += clean[i].Duration
		}
	}
	sort.SliceStable(clean, func(i, j int) bool { return clean[i].Start < clean[j].Start })

	tl := Timeline{segs: clean}
	for i := 1; i < len(clean); i++ {
		if clean[i].Start > clean[i-1].End() {
			tl.gaps = append(tl.gaps, clean[i-1].End())
		}
	}
	return tl, nil
}

// Segments returns a copy of the laid-out timeline.
func (t Timeline) Segments() []Segment { return append([]Segment(nil), t.segs...) }

// Duration is where the last segment ends.
func (t Timeline) Duration() time.Duration {
	if len(t.segs) == 0 {
		return 0
	}
	return t.segs[len(t.segs)-1].End()
}

// Span returns the segments a range touches, in order.
//
// Half-open: a range ending exactly at a segment's Start does not include that
// segment, because no frame of it is in the clip.
func (t Timeline) Span(in, out time.Duration) ([]Segment, error) {
	if out <= in {
		return nil, ErrEmptyRange
	}
	var hit []Segment
	for _, s := range t.segs {
		if s.End() <= in || s.Start >= out {
			continue
		}
		hit = append(hit, s)
	}
	if len(hit) == 0 {
		return nil, ErrOutOfRange
	}
	return hit, nil
}

// gapsIn reports the discontinuities inside a range.
func (t Timeline) gapsIn(in, out time.Duration) []time.Duration {
	var hit []time.Duration
	for _, g := range t.gaps {
		if g > in && g < out {
			hit = append(hit, g)
		}
	}
	return hit
}

// Request is one cut a human asked for.
type Request struct {
	// In and Out are timeline positions, half-open: the clip contains In and
	// stops just before Out.
	In  time.Duration `json:"in"`
	Out time.Duration `json:"out"`

	Mode  Mode           `json:"mode"`
	Audio AudioSelection `json:"audio"`

	// OutPath is the absolute path of the clip to write. Its extension chooses
	// the container.
	OutPath string `json:"outPath"`

	// VideoEncoder is what re-encodes the leading partial GOP in ModePrecise.
	// Empty means HeadEncoder picks. Never used in ModeFast, which encodes
	// nothing at all.
	VideoEncoder string `json:"videoEncoder,omitempty"`
	// HeadCRF is the quality of that re-encode, for encoders that take a CRF.
	// Zero means DefaultHeadCRF.
	HeadCRF int `json:"headCrf,omitempty"`
	// HeadThreads caps how many cores the head encode may use. Zero lets FFmpeg
	// decide, which means all of them.
	//
	// This is the one knob in this package that exists purely to protect the
	// live stream, and it is the job governor's to set: a clip export that grabs
	// every core while a broadcast is going out is exactly the trade this
	// product refuses to make. It is ignored in ModeFast, which encodes nothing.
	HeadThreads int `json:"headThreads,omitempty"`

	// Title is written into the clip's metadata, and names it in an exported
	// EDL. Optional.
	Title string `json:"title,omitempty"`
}

// MaxClipDuration is a sanity ceiling, not a policy: a cut longer than this is
// a request to copy most of a day, which is a recorder's job and not a
// clipper's. Generous on purpose.
const MaxClipDuration = 12 * time.Hour

// DefaultHeadCRF is the quality of the re-encoded leading GOP.
//
// 16 is well inside "you will not see it" for h264, and the head is a fraction
// of a second: paying for it in bitrate rather than in artefacts is free here
// in a way it never is for a whole file.
const DefaultHeadCRF = 16

// Validate checks a request on its own terms, before any timeline is consulted.
// Every failure wraps ErrInvalidRequest.
func (r Request) Validate() error {
	if r.OutPath == "" {
		return fmt.Errorf("%w: no output path", ErrInvalidRequest)
	}
	if !filepath.IsAbs(r.OutPath) {
		return fmt.Errorf("%w: output path %q is not absolute", ErrInvalidRequest, r.OutPath)
	}
	if r.In < 0 {
		return fmt.Errorf("%w: in-point %s is negative", ErrInvalidRequest, r.In)
	}
	if r.Out <= r.In {
		return ErrEmptyRange
	}
	if d := r.Out - r.In; d > MaxClipDuration {
		return fmt.Errorf("%w: %s is longer than the %s ceiling for one clip",
			ErrInvalidRequest, d, MaxClipDuration)
	}
	if !ValidMode(r.Mode) {
		return fmt.Errorf("%w: unknown mode %q", ErrInvalidRequest, r.Mode)
	}
	return r.Audio.validate()
}

func (a AudioSelection) validate() error {
	switch a.Mode {
	case "", AudioAll, AudioMix:
		return nil
	case AudioTracks:
		if len(a.Tracks) == 0 {
			return fmt.Errorf("%w: audio mode %q names no tracks", ErrInvalidRequest, AudioTracks)
		}
		for _, tr := range a.Tracks {
			if tr < 0 {
				return fmt.Errorf("%w: audio track %d is negative", ErrInvalidRequest, tr)
			}
		}
		return nil
	default:
		return fmt.Errorf("%w: unknown audio mode %q", ErrInvalidRequest, a.Mode)
	}
}

// secs formats a duration the way FFmpeg's -ss and -t want it.
//
// Fixed six decimals rather than %g: a duration that renders as "1e-06" is
// accepted by nothing, and one that rounds to whole seconds would quietly
// re-introduce the frame error this package exists to measure.
func secs(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 6, 64)
}
