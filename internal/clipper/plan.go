package clipper

import (
	"context"
	"fmt"
	"time"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// Plan is a resolved cut: every decision made, every number a caller has to be
// told, and nothing run yet.
//
// It exists as a separate value from the cut itself so a UI can show a human
// "this is what pressing the button will do, and your in-point will move by
// 1.8 seconds" BEFORE anything is written.
type Plan struct {
	// Mode is what will actually happen. RequestedMode is what was asked for.
	// They differ when a precise cut had to degrade — see Warnings.
	Mode          Mode `json:"mode"`
	RequestedMode Mode `json:"requestedMode"`

	// Sources are the segment files the cut reads, in timeline order.
	Sources []Segment `json:"sources"`
	// Concat is true when the cut spans a segment boundary and the sources have
	// to be joined before they can be cut.
	Concat bool `json:"concat"`
	// Base is the timeline position of the first frame of the input FFmpeg will
	// open. Every seek below is relative to it.
	Base time.Duration `json:"base"`

	RequestedIn  time.Duration `json:"requestedIn"`
	RequestedOut time.Duration `json:"requestedOut"`
	// In and Out are what the clip will actually contain.
	In  time.Duration `json:"in"`
	Out time.Duration `json:"out"`

	// InDrift is In minus RequestedIn: how far the delivered start landed from
	// the one that was asked for. Negative means the clip begins EARLIER, which
	// is the only direction a fast cut ever moves. Zero for a precise cut.
	InDrift time.Duration `json:"inDrift"`
	// DriftKnown is false when the keyframe index could not be read. The cut
	// still happens — FFmpeg's own seek snaps it — but nobody can say by how
	// much it moved, and saying "no drift" would be a lie.
	DriftKnown bool `json:"driftKnown"`
	// Keyframe is the random-access point the cut snapped to, in timeline
	// coordinates. Only meaningful when DriftKnown.
	Keyframe time.Duration `json:"keyframe"`

	// HeadDuration is how much video the precise mode re-encodes, measured from
	// In. Zero means nothing is re-encoded and the cut is a pure copy.
	HeadDuration time.Duration `json:"headDuration"`
	// HeadSeek and HeadTrim are the two halves of the head's seek: seek the
	// input to a keyframe, then drop the remainder on the output side. See
	// headCommand for why it is done in two stages.
	HeadSeek time.Duration `json:"headSeek"`
	HeadTrim time.Duration `json:"headTrim"`
	// TailSeek and TailDuration are the copied remainder, input-relative.
	TailDuration time.Duration `json:"tailDuration"`
	TailSeek     time.Duration `json:"tailSeek"`

	Audio AudioSelection `json:"audio"`
	// FilterComplex is the compiled routing graph for AudioMix, empty otherwise.
	FilterComplex string `json:"filterComplex,omitempty"`
	// AudioCodec is what a mix is encoded as. Empty when nothing is mixed.
	AudioCodec string `json:"audioCodec,omitempty"`
	AudioKbps  int    `json:"audioKbps,omitempty"`

	OutPath   string    `json:"outPath"`
	Container Container `json:"container"`
	Title     string    `json:"title,omitempty"`

	// VideoEncoder, HeadCRF and HeadThreads only matter when HeadDuration is
	// non-zero.
	VideoEncoder string `json:"videoEncoder,omitempty"`
	HeadCRF      int    `json:"headCrf,omitempty"`
	HeadThreads  int    `json:"headThreads,omitempty"`

	// Warnings are everything the caller should be told but that does not stop
	// the cut: a moved in-point, a gap in the recording, a degraded mode.
	Warnings []string `json:"warnings,omitempty"`
}

// Duration is how long the clip will be.
func (p Plan) Duration() time.Duration { return p.Out - p.In }

// ReEncodes reports whether any video will be re-encoded. A false answer is the
// promise that the clip's pictures are the source's, bit for bit.
func (p Plan) ReEncodes() bool { return p.HeadDuration > 0 }

// LosslessFraction is how much of the clip is copied rather than re-encoded,
// 0..1. It is what makes the precise mode's cost legible: 0.98 means a
// ten-second clip paid for a fifth of a second of encoding.
func (p Plan) LosslessFraction() float64 {
	d := p.Duration()
	if d <= 0 {
		return 0
	}
	if p.HeadDuration >= d {
		return 0
	}
	return 1 - p.HeadDuration.Seconds()/d.Seconds()
}

// Describe is the one-sentence summary to put on a confirm dialog.
func (p Plan) Describe() string {
	what := "Lossless copy"
	if p.ReEncodes() {
		what = fmt.Sprintf("Precise cut (re-encodes the first %s)", round(p.HeadDuration))
	}
	where := fmt.Sprintf("%s from %s", round(p.Duration()), plural(len(p.Sources), "segment"))
	switch {
	case !p.DriftKnown:
		return fmt.Sprintf("%s, %s. The start could not be checked against the keyframes, "+
			"so the clip may begin up to one GOP early.", what, where)
	case p.InDrift == 0:
		return fmt.Sprintf("%s, %s, starting exactly where you asked.", what, where)
	default:
		return fmt.Sprintf("%s, %s, starting %s earlier than you asked "+
			"(the nearest keyframe before your in-point).", what, where, round(-p.InDrift))
	}
}

// round trims a duration to something worth showing a human.
func round(d time.Duration) time.Duration {
	if d < 0 {
		d = -d
	}
	if d < time.Second {
		return d.Round(time.Millisecond)
	}
	return d.Round(10 * time.Millisecond)
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// PlanCut resolves a request against a timeline and a keyframe index.
//
// Pure: no process is run and no file is touched, which is what lets the
// keyframe snapping and the boundary-spanning arithmetic be tested against
// synthetic GOP structures instead of against a video card.
//
// An empty index is not an error. It means nobody could read the keyframes,
// and the cut proceeds with DriftKnown false — refusing to cut because a probe
// failed would be a restrictive check standing between a user and their own
// recording.
func PlanCut(tl Timeline, kf Keyframes, req Request) (Plan, error) {
	p, err := newPlan(tl, req)
	if err != nil {
		return Plan{}, err
	}
	p.snap(kf)
	if err := p.resolveSources(tl); err != nil {
		return Plan{}, err
	}
	if err := p.resolveAudio(); err != nil {
		return Plan{}, err
	}
	if p.HeadDuration > 0 {
		p.VideoEncoder = req.VideoEncoder
		if p.VideoEncoder == "" {
			p.VideoEncoder = FallbackHeadEncoder
		}
	}
	return p, nil
}

// newPlan validates the request against the timeline and fills in the defaults,
// leaving only the keyframe arithmetic to snap.
func newPlan(tl Timeline, req Request) (Plan, error) {
	if err := req.Validate(); err != nil {
		return Plan{}, err
	}
	if len(tl.segs) == 0 {
		return Plan{}, ErrNoSegments
	}
	if req.In >= tl.Duration() {
		return Plan{}, fmt.Errorf("%w: the in-point %s is past the end of the recording (%s)",
			ErrOutOfRange, round(req.In), round(tl.Duration()))
	}

	mode := req.Mode
	if mode == "" {
		mode = DefaultMode
	}
	crf := req.HeadCRF
	if crf <= 0 {
		crf = DefaultHeadCRF
	}
	p := Plan{
		Mode:          mode,
		RequestedMode: mode,
		RequestedIn:   req.In,
		RequestedOut:  req.Out,
		In:            req.In,
		Out:           req.Out,
		Audio:         req.Audio,
		OutPath:       req.OutPath,
		Container:     containerFor(req.OutPath),
		Title:         req.Title,
		HeadCRF:       crf,
		HeadThreads:   req.HeadThreads,
	}
	if p.Out > tl.Duration() {
		// Clamped rather than refused. Asking for "to the end" by naming a
		// number slightly past it is what a person in a hurry does, and the
		// honest answer is the rest of the recording.
		p.warn("the out-point %s is past the end of the recording; the clip stops at %s",
			round(p.Out), round(tl.Duration()))
		p.Out = tl.Duration()
	}
	if p.Out <= p.In {
		return Plan{}, ErrEmptyRange
	}
	return p, nil
}

// resolveSources settles which files the cut reads and rebases every seek onto
// the first of them.
//
// It runs after snap, because a fast cut moves the in-point backwards and a
// precise one reads from the keyframe before it — either can pull the clip into
// an earlier segment than the request alone would have touched.
func (p *Plan) resolveSources(tl Timeline) error {
	from := p.In
	if p.HeadDuration > 0 && p.HeadSeek < from {
		from = p.HeadSeek
	}
	src, err := tl.Span(from, p.Out)
	if err != nil {
		return err
	}
	p.Sources = src
	p.Concat = len(src) > 1
	p.Base = src[0].Start

	for _, g := range tl.gapsIn(from, p.Out) {
		// A gap is a recorder restart, or a segment retention deleted out of the
		// middle of a run. The clip is still produced: part of a clip is what the
		// archive can offer, and refusing gives the user nothing at all.
		p.warn("the recording is discontinuous at %s; the clip jumps across the missing span", round(g))
	}

	// Seeks become input-relative here and nowhere else, so every consumer of a
	// Plan can take them at face value.
	if p.HeadDuration > 0 {
		p.HeadSeek -= p.Base
		if p.HeadSeek < 0 {
			p.HeadSeek = 0
		}
	} else {
		p.HeadSeek, p.HeadTrim = 0, 0
	}
	if p.TailDuration > 0 {
		p.TailSeek -= p.Base
	} else {
		p.TailSeek = 0
	}
	return nil
}

// snap decides where the cut actually starts, and how much of it (if any) has
// to be re-encoded to put it there. Everything it sets is in timeline
// coordinates; PlanCut rebases them onto the input afterwards.
func (p *Plan) snap(kf Keyframes) {
	if !kf.Known() {
		// FAIL OPEN. FFmpeg's own input seek lands on the keyframe at or before
		// the in-point whether or not we know where that is, so the clip is
		// still watchable — we simply cannot say how far it moved. A precise cut
		// is impossible without the index, and degrading loudly beats refusing.
		p.DriftKnown = false
		if p.Mode == ModePrecise {
			p.Mode = ModeFast
			p.warn("the keyframe positions could not be read, and a precise cut needs them; " +
				"this clip was cut fast instead")
		}
		p.warn("the keyframe positions could not be read, so the clip may begin up to one GOP " +
			"before the requested in-point and end short by the same amount")
		return
	}

	if p.Mode == ModeFast {
		k, ok := kf.AtOrBefore(p.In)
		if !ok {
			// The index starts after the in-point: the probe window missed the
			// head of the file. Leave the in-point alone and let FFmpeg seek.
			p.DriftKnown = false
			p.warn("no keyframe was found at or before the in-point, so the exact start " +
				"is whatever FFmpeg's own seek finds")
			return
		}
		p.Keyframe = k
		p.In = k
		p.InDrift = k - p.RequestedIn
		p.DriftKnown = true
		return
	}

	// Precise. The in-point does not move, so drift is zero by construction —
	// what varies is how much has to be re-encoded to make that true.
	p.DriftKnown = true
	p.InDrift = 0
	if kf.Contains(p.In, AlignTolerance) {
		// Already on a keyframe. Snap onto it exactly and copy: re-encoding a
		// zero-length head would produce an empty file and a confusing error.
		if k, ok := kf.AtOrBefore(p.In + AlignTolerance); ok {
			p.Keyframe = k
			p.InDrift = k - p.RequestedIn
			p.In = k
		}
		p.warn("the in-point is already on a keyframe, so nothing had to be re-encoded")
		return
	}

	next, ok := kf.After(p.In)
	if !ok || next >= p.Out {
		// The whole clip lives inside one GOP. There is no keyframe to resume
		// copying at, so all of it is re-encoded — still correct, and the reason
		// the caller is told rather than left to wonder why a 3-second clip took
		// as long as it did.
		p.HeadDuration = p.Out - p.In
		p.warn("the clip is shorter than one GOP, so all of it is re-encoded")
	} else {
		p.Keyframe = next
		p.HeadDuration = next - p.In
		p.TailSeek = next
		p.TailDuration = p.Out - next
	}

	// Seek the input to a keyframe and drop the remainder on the output side.
	// Where no earlier keyframe is known, seek to the in-point itself: FFmpeg's
	// accurate seek decodes from the preceding keyframe and discards, which
	// gets to the same frame by a route we cannot measure.
	if k, ok := kf.AtOrBefore(p.In); ok {
		p.HeadSeek = k
		p.HeadTrim = p.In - k
	} else {
		p.HeadSeek = p.In
		p.HeadTrim = 0
	}
}

// resolveAudio settles which tracks survive and, for a mix, compiles the
// routing graph that produces it.
func (p *Plan) resolveAudio() error {
	if p.Audio.Mode == "" {
		p.Audio.Mode = AudioAll
	}
	if p.Audio.Mode != AudioMix {
		return nil
	}

	prof := p.Audio.Profile
	if prof.IsUnset() {
		prof.ApplyDefaults()
	}
	res, err := routing.Compile(prof, p.Audio.Source)
	if err != nil {
		return fmt.Errorf("clipper: audio mix: %w", err)
	}
	p.FilterComplex = res.FilterComplex
	p.Warnings = append(p.Warnings, res.Warnings...)

	p.AudioCodec = p.Audio.Codec
	if p.AudioCodec == "" {
		p.AudioCodec = defaultMixCodec(p.Container)
	}
	p.AudioKbps = p.Audio.Kbps
	// A mix is the one thing in this package that is not bit-exact, and saying
	// so is cheaper than a support ticket about a clip that does not match its
	// recording.
	p.warn("the mixed audio is re-encoded to %s; the video is still copied untouched", p.AudioCodec)
	return nil
}

// defaultMixCodec picks the encoder for a mix from the container.
//
// FLAC into Matroska, because that is what the rest of this product does with
// audio it intends somebody to keep, and a mix of a multitrack master is
// usually headed for an editor rather than a timeline. AAC everywhere else,
// because MP4 and MPEG-TS players cannot be relied on for FLAC.
func defaultMixCodec(c Container) string {
	if c == ContainerMatroska {
		return "flac"
	}
	return "aac"
}

func (p *Plan) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	for _, w := range p.Warnings {
		if w == msg {
			return
		}
	}
	p.Warnings = append(p.Warnings, msg)
}

// FallbackHeadEncoder is what re-encodes the leading GOP when nothing better
// has been established. x264 rather than a hardware encoder on purpose: the
// head is a fraction of a second, so throughput is irrelevant and quality per
// bit is not, and x264 is the encoder most likely to exist at all.
const FallbackHeadEncoder = "libx264"

// EncoderProber is the part of ffmpeg.Tools this package needs. An interface so
// the clipper does not have to import the detector, and so the choice can be
// tested without a GPU.
type EncoderProber interface {
	// EncoderWorks reports whether an encoder demonstrably encodes here. It
	// must answer true for an encoder nobody probed.
	EncoderWorks(name string) (bool, string)
	// HasEncoder reports whether the build registers the encoder at all.
	HasEncoder(name string) bool
}

// HeadEncoder chooses what re-encodes the leading partial GOP.
//
// x264 wins whenever it is available, for the reason above. A hardware encoder
// is only reached for when x264 is genuinely absent from the build, and even
// then a nil prober or an unprobed machine gets x264 anyway: a check that
// refuses to name an encoder because detection did not run is the restrictive
// failure this repo has already paid for.
func HeadEncoder(p EncoderProber, hardware []string) string {
	if p == nil {
		return FallbackHeadEncoder
	}
	if works, _ := p.EncoderWorks(FallbackHeadEncoder); works && p.HasEncoder(FallbackHeadEncoder) {
		return FallbackHeadEncoder
	}
	for _, name := range hardware {
		if works, _ := p.EncoderWorks(name); works && p.HasEncoder(name) {
			return name
		}
	}
	// Nothing was demonstrated. Naming x264 keeps the failure legible; an empty
	// -c:v is a command line nobody can debug.
	return FallbackHeadEncoder
}

// PlanWith probes the keyframes a request needs and then plans against them.
//
// The probe is bounded to a window around the in-point, so planning a cut out
// of a four-hour recording costs a few hundred milliseconds rather than a full
// read of every file.
func PlanWith(ctx context.Context, pr Prober, tl Timeline, req Request) (Plan, error) {
	if err := req.Validate(); err != nil {
		return Plan{}, err
	}
	kf, warns := indexFor(ctx, pr, tl.Segments(), req.In)
	p, err := PlanCut(tl, kf, req)
	if err != nil {
		return Plan{}, err
	}
	p.Warnings = append(warns, p.Warnings...)
	return p, nil
}
