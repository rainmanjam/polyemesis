package clipper

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// gop builds the keyframe index of a synthetic file with a known GOP length:
// count keyframes, every interval, starting at zero. Every snapping test below
// is stated against one of these so the expected answer is arithmetic rather
// than a guess about what an encoder did.
func gop(count int, interval time.Duration) Keyframes {
	times := make([]time.Duration, 0, count)
	for i := 0; i < count; i++ {
		times = append(times, time.Duration(i)*interval)
	}
	return NewKeyframes(times)
}

func oneHourTimeline(t *testing.T) Timeline {
	t.Helper()
	tl, err := NewTimeline([]Segment{{Path: "/rec/seg0.mkv", Duration: time.Hour}})
	if err != nil {
		t.Fatalf("NewTimeline: %v", err)
	}
	return tl
}

func req(in, out time.Duration, mut ...func(*Request)) Request {
	r := Request{In: in, Out: out, OutPath: "/clips/out.mkv"}
	for _, fn := range mut {
		fn(&r)
	}
	return r
}

// A fast cut can only start on a keyframe, so the in-point moves BACKWARDS to
// the nearest one and the caller is told by exactly how much. Reporting the
// drift is the whole contract: silently moving somebody's in-point is the
// failure this package exists to prevent.
func TestFastCutSnapsBackwardsAndReportsTheDrift(t *testing.T) {
	tl := oneHourTimeline(t)
	kf := gop(60, 2*time.Second) // a two-second GOP out to 118s

	tests := []struct {
		name        string
		in, out     time.Duration
		wantIn      time.Duration
		wantDrift   time.Duration
		wantLossles bool
	}{
		{
			name: "an in-point one second into a GOP moves back one second",
			in:   5 * time.Second, out: 15 * time.Second,
			wantIn: 4 * time.Second, wantDrift: -time.Second,
		},
		{
			name: "an in-point already on a keyframe does not move at all",
			in:   6 * time.Second, out: 15 * time.Second,
			wantIn: 6 * time.Second, wantDrift: 0,
		},
		{
			name: "an in-point just before a keyframe moves back nearly a whole GOP",
			in:   7900 * time.Millisecond, out: 20 * time.Second,
			wantIn: 6 * time.Second, wantDrift: -1900 * time.Millisecond,
		},
		{
			name: "an in-point at zero is already a keyframe",
			in:   0, out: 10 * time.Second,
			wantIn: 0, wantDrift: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := PlanCut(tl, kf, req(tc.in, tc.out))
			if err != nil {
				t.Fatalf("PlanCut: %v", err)
			}
			if p.Mode != ModeFast {
				t.Errorf("mode = %s, want %s", p.Mode, ModeFast)
			}
			if p.In != tc.wantIn {
				t.Errorf("in = %s, want %s", p.In, tc.wantIn)
			}
			if p.InDrift != tc.wantDrift {
				t.Errorf("drift = %s, want %s", p.InDrift, tc.wantDrift)
			}
			if !p.DriftKnown {
				t.Error("the drift was measured but not reported as known")
			}
			if p.RequestedIn != tc.in {
				t.Errorf("the request was overwritten: requestedIn = %s, want %s", p.RequestedIn, tc.in)
			}
			if p.Out != tc.out {
				t.Errorf("out = %s, want %s: a fast cut must not move the out-point", p.Out, tc.out)
			}
			if p.ReEncodes() {
				t.Error("a fast cut re-encoded something")
			}
			if p.HeadDuration != 0 {
				t.Errorf("head = %s, want 0", p.HeadDuration)
			}
		})
	}
}

// Precise trades a short encode for an exact in-point: only the partial GOP in
// front of the next keyframe is re-encoded, and everything after it is copied.
func TestPreciseCutReEncodesOnlyTheLeadingPartialGOP(t *testing.T) {
	tl := oneHourTimeline(t)
	kf := gop(60, 2*time.Second)

	tests := []struct {
		name         string
		in, out      time.Duration
		wantHead     time.Duration
		wantHeadSeek time.Duration
		wantHeadTrim time.Duration
		wantTailSeek time.Duration
		wantTail     time.Duration
	}{
		{
			name: "an in-point one second into a GOP re-encodes the second that is left of it",
			in:   5 * time.Second, out: 15 * time.Second,
			wantHead: time.Second, wantHeadSeek: 4 * time.Second, wantHeadTrim: time.Second,
			wantTailSeek: 6 * time.Second, wantTail: 9 * time.Second,
		},
		{
			name: "an in-point just after a keyframe re-encodes nearly a whole GOP",
			in:   4100 * time.Millisecond, out: 30 * time.Second,
			wantHead: 1900 * time.Millisecond, wantHeadSeek: 4 * time.Second, wantHeadTrim: 100 * time.Millisecond,
			wantTailSeek: 6 * time.Second, wantTail: 24 * time.Second,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := PlanCut(tl, kf, req(tc.in, tc.out, precise))
			if err != nil {
				t.Fatalf("PlanCut: %v", err)
			}
			if p.In != tc.in || p.InDrift != 0 {
				t.Errorf("in = %s drift = %s, want %s and no drift at all", p.In, p.InDrift, tc.in)
			}
			if !p.DriftKnown {
				t.Error("a precise cut must be able to state that it did not drift")
			}
			if p.HeadDuration != tc.wantHead {
				t.Errorf("head = %s, want %s", p.HeadDuration, tc.wantHead)
			}
			if p.HeadSeek != tc.wantHeadSeek {
				t.Errorf("head seek = %s, want %s", p.HeadSeek, tc.wantHeadSeek)
			}
			if p.HeadTrim != tc.wantHeadTrim {
				t.Errorf("head trim = %s, want %s", p.HeadTrim, tc.wantHeadTrim)
			}
			if p.TailSeek != tc.wantTailSeek {
				t.Errorf("tail seek = %s, want %s", p.TailSeek, tc.wantTailSeek)
			}
			if p.TailDuration != tc.wantTail {
				t.Errorf("tail = %s, want %s", p.TailDuration, tc.wantTail)
			}
			// The head plus the tail is the clip, exactly. A gap here is a
			// dropped frame; an overlap is a stutter.
			if got := p.HeadDuration + p.TailDuration; got != p.Duration() {
				t.Errorf("head+tail = %s but the clip is %s", got, p.Duration())
			}
		})
	}
}

// Re-encoding a zero-length head produces an empty file and a confusing error,
// so an in-point that is already aligned degenerates to a pure copy.
func TestPreciseCutOnAKeyframeReEncodesNothing(t *testing.T) {
	tl := oneHourTimeline(t)
	p, err := PlanCut(tl, gop(60, 2*time.Second), req(6*time.Second, 15*time.Second, precise))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	if p.ReEncodes() {
		t.Errorf("head = %s, want nothing re-encoded", p.HeadDuration)
	}
	if p.In != 6*time.Second || p.InDrift != 0 {
		t.Errorf("in = %s drift = %s, want 6s and no drift", p.In, p.InDrift)
	}
	wantWarning(t, p, "already on a keyframe")
}

// The in-point can be within a millisecond of a keyframe without being exactly
// on it: a scrubber reports milliseconds and a container reports fractional
// seconds. Demanding exact equality would re-encode a head that is already
// aligned, every time.
func TestPreciseCutTreatsANearlyAlignedInPointAsAligned(t *testing.T) {
	tl := oneHourTimeline(t)
	p, err := PlanCut(tl, gop(60, 2*time.Second),
		req(6*time.Second+2*time.Millisecond, 15*time.Second, precise))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	if p.ReEncodes() {
		t.Errorf("head = %s, want nothing re-encoded for a 2ms misalignment", p.HeadDuration)
	}
	if p.In != 6*time.Second {
		t.Errorf("in = %s, want it snapped onto the keyframe at 6s", p.In)
	}
}

// A ten-second social clip out of a stream with a thirty-second GOP has no
// keyframe inside it to resume copying at. Re-encoding the whole thing is the
// only way to honour the in-point, and the caller is told why it took longer.
func TestPreciseCutShorterThanOneGOPReEncodesAllOfIt(t *testing.T) {
	tl := oneHourTimeline(t)
	p, err := PlanCut(tl, gop(10, 30*time.Second), req(35*time.Second, 45*time.Second, precise))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	if p.HeadDuration != 10*time.Second {
		t.Errorf("head = %s, want the whole 10s clip", p.HeadDuration)
	}
	if p.TailDuration != 0 {
		t.Errorf("tail = %s, want nothing copied", p.TailDuration)
	}
	if p.LosslessFraction() != 0 {
		t.Errorf("lossless fraction = %v, want 0", p.LosslessFraction())
	}
	wantWarning(t, p, "shorter than one GOP")
}

func TestLosslessFractionReportsHowMuchOfTheClipIsCopied(t *testing.T) {
	tl := oneHourTimeline(t)
	p, err := PlanCut(tl, gop(60, 2*time.Second), req(5*time.Second, 15*time.Second, precise))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	// One second re-encoded out of ten.
	if got := p.LosslessFraction(); got < 0.89 || got > 0.91 {
		t.Fatalf("lossless fraction = %v, want about 0.9", got)
	}
}

// A check that is wrong in the restrictive direction is worse than no check. A
// probe that failed must not stop somebody clipping their own recording — the
// cut still happens, FFmpeg's own seek still lands on a keyframe, and the only
// thing lost is the ability to say how far it moved.
func TestAnUnreadableKeyframeIndexStillProducesACut(t *testing.T) {
	tl := oneHourTimeline(t)

	tests := []struct {
		name     string
		mode     Mode
		wantMode Mode
		wantWarn string
	}{
		{
			name: "a fast cut proceeds with an unknown drift",
			mode: ModeFast, wantMode: ModeFast,
			wantWarn: "may begin up to one GOP",
		},
		{
			name: "a precise cut degrades loudly to a fast one",
			mode: ModePrecise, wantMode: ModeFast,
			wantWarn: "cut fast instead",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := PlanCut(tl, Keyframes{}, req(5*time.Second, 15*time.Second, func(r *Request) { r.Mode = tc.mode }))
			if err != nil {
				t.Fatalf("PlanCut refused to cut because a probe failed: %v", err)
			}
			if p.Mode != tc.wantMode {
				t.Errorf("mode = %s, want %s", p.Mode, tc.wantMode)
			}
			if p.RequestedMode != tc.mode {
				t.Errorf("requested mode = %s, want %s", p.RequestedMode, tc.mode)
			}
			if p.In != 5*time.Second {
				t.Errorf("in = %s, want the requested 5s left alone", p.In)
			}
			if p.DriftKnown {
				t.Error("the plan claims to know a drift it could not measure")
			}
			wantWarning(t, p, tc.wantWarn)
		})
	}
}

// The probe window can start after the head of the file, leaving nothing
// indexed before the in-point. Leaving the in-point alone and letting FFmpeg
// seek is the fail-open answer.
func TestAFastCutWithNoKeyframeBeforeTheInPointLeavesItAlone(t *testing.T) {
	tl := oneHourTimeline(t)
	kf := NewKeyframes([]time.Duration{30 * time.Second, 32 * time.Second})
	p, err := PlanCut(tl, kf, req(10*time.Second, 20*time.Second))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	if p.In != 10*time.Second {
		t.Errorf("in = %s, want the requested 10s", p.In)
	}
	if p.DriftKnown {
		t.Error("the plan claims a drift it could not measure")
	}
	wantWarning(t, p, "no keyframe was found at or before")
}

// The recorder writes hourly files. A clip across the boundary has to pull in
// both and concat them; getting this wrong produces a clip that stops dead at
// the seam.
func TestACutSpanningASegmentBoundaryPullsInBothFiles(t *testing.T) {
	tl := hourlyTimeline(t, 3, time.Hour)
	kf := gop(400, 10*time.Second).Shift(0)

	tests := []struct {
		name       string
		in, out    time.Duration
		wantPaths  []string
		wantConcat bool
		wantBase   time.Duration
	}{
		{
			name: "a cut inside one segment reads one file",
			in:   10 * time.Minute, out: 20 * time.Minute,
			wantPaths: []string{"/seg0.mkv"}, wantConcat: false, wantBase: 0,
		},
		{
			name: "a cut across one boundary reads both files",
			in:   time.Hour - time.Minute, out: time.Hour + time.Minute,
			wantPaths: []string{"/seg0.mkv", "/seg1.mkv"}, wantConcat: true, wantBase: 0,
		},
		{
			name: "a cut across two boundaries reads three files",
			in:   time.Hour - time.Minute, out: 2*time.Hour + time.Minute,
			wantPaths: []string{"/seg0.mkv", "/seg1.mkv", "/seg2.mkv"}, wantConcat: true, wantBase: 0,
		},
		{
			name: "a cut inside a later segment is based on that segment, not on the timeline",
			in:   time.Hour + 10*time.Minute, out: time.Hour + 20*time.Minute,
			wantPaths: []string{"/seg1.mkv"}, wantConcat: false, wantBase: time.Hour,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := PlanCut(tl, kf, req(tc.in, tc.out))
			if err != nil {
				t.Fatalf("PlanCut: %v", err)
			}
			if got := paths(p.Sources); !equalStrings(got, tc.wantPaths) {
				t.Errorf("sources = %v, want %v", got, tc.wantPaths)
			}
			if p.Concat != tc.wantConcat {
				t.Errorf("concat = %v, want %v", p.Concat, tc.wantConcat)
			}
			if p.Base != tc.wantBase {
				t.Errorf("base = %s, want %s", p.Base, tc.wantBase)
			}
		})
	}
}

// A fast cut moves the in-point backwards, which can land it in the PREVIOUS
// segment. The span has to be recomputed after snapping, not before, or the
// concat list is missing the file the clip now starts in.
func TestASnapAcrossASegmentBoundaryPullsInTheEarlierFile(t *testing.T) {
	tl := hourlyTimeline(t, 2, 10*time.Second)
	// Keyframes at 8s and 12s: an in-point at 10.5s snaps back to 8s, which is
	// in the first segment.
	kf := NewKeyframes([]time.Duration{8 * time.Second, 12 * time.Second})

	p, err := PlanCut(tl, kf, req(10500*time.Millisecond, 15*time.Second))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	if p.In != 8*time.Second {
		t.Fatalf("in = %s, want the 8s keyframe", p.In)
	}
	if got := paths(p.Sources); !equalStrings(got, []string{"/seg0.mkv", "/seg1.mkv"}) {
		t.Fatalf("sources = %v, want both segments", got)
	}
	if !p.Concat {
		t.Error("a cut reading two files is not marked for concat")
	}
}

// A precise cut reads from the keyframe BEFORE the in-point in order to decode
// the head, which can also reach into the previous segment even though the clip
// itself does not.
func TestAPreciseHeadReachingIntoTheEarlierFilePullsItIn(t *testing.T) {
	tl := hourlyTimeline(t, 2, 10*time.Second)
	kf := NewKeyframes([]time.Duration{9500 * time.Millisecond, 11 * time.Second})

	p, err := PlanCut(tl, kf, req(10200*time.Millisecond, 15*time.Second, precise))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	if got := paths(p.Sources); !equalStrings(got, []string{"/seg0.mkv", "/seg1.mkv"}) {
		t.Fatalf("sources = %v, want both segments: the head decodes from 9.5s", got)
	}
	// Seeks are input-relative, and the input now starts at the first segment.
	if p.Base != 0 {
		t.Errorf("base = %s, want 0", p.Base)
	}
	if p.HeadSeek != 9500*time.Millisecond {
		t.Errorf("head seek = %s, want 9.5s", p.HeadSeek)
	}
	if p.HeadTrim != 700*time.Millisecond {
		t.Errorf("head trim = %s, want 700ms", p.HeadTrim)
	}
}

// Seeks are stated relative to the file FFmpeg opens, never to the timeline.
// A caller who forgets that seeks an hour into a one-hour file.
func TestSeeksAreRebasedOntoTheFirstSourceFile(t *testing.T) {
	tl := hourlyTimeline(t, 3, time.Hour)
	kf := gop(400, 10*time.Second)

	p, err := PlanCut(tl, kf, req(time.Hour+35*time.Second, time.Hour+95*time.Second, precise))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	if p.Base != time.Hour {
		t.Fatalf("base = %s, want 1h", p.Base)
	}
	if p.HeadSeek != 30*time.Second {
		t.Errorf("head seek = %s, want 30s into the second file", p.HeadSeek)
	}
	if p.TailSeek != 40*time.Second {
		t.Errorf("tail seek = %s, want 40s into the second file", p.TailSeek)
	}
}

func TestAnOutPointPastTheEndIsClampedRatherThanRefused(t *testing.T) {
	tl := oneHourTimeline(t)
	p, err := PlanCut(tl, gop(2000, 2*time.Second), req(time.Hour-10*time.Second, time.Hour+time.Minute))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	if p.Out != time.Hour {
		t.Errorf("out = %s, want the end of the recording", p.Out)
	}
	wantWarning(t, p, "past the end of the recording")
}

func TestPlanCutRefusesOnlyWhatItCannotDo(t *testing.T) {
	tl := oneHourTimeline(t)
	kf := gop(60, 2*time.Second)

	tests := []struct {
		name    string
		req     Request
		wantErr error
	}{
		{
			name:    "an in-point past the end of the recording",
			req:     req(2*time.Hour, 2*time.Hour+time.Minute),
			wantErr: ErrOutOfRange,
		},
		{
			name:    "an out-point at the in-point",
			req:     Request{In: time.Second, Out: time.Second, OutPath: "/clips/out.mkv"},
			wantErr: ErrEmptyRange,
		},
		{
			name:    "a request with no output path",
			req:     Request{In: 0, Out: time.Second},
			wantErr: ErrInvalidRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := PlanCut(tl, kf, tc.req); !errors.Is(err, tc.wantErr) {
				t.Fatalf("PlanCut: got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// A gap is a recorder restart, or a segment deleted by retention out of the
// middle of a run. The clip is still produced — part of a clip is what the
// archive can offer — but nobody should discover the jump by watching it.
func TestAGapInTheRecordingIsReportedRatherThanHidden(t *testing.T) {
	tl, err := NewTimeline([]Segment{
		{Path: "/seg0.mkv", Start: 0, Duration: 10 * time.Second},
		{Path: "/seg1.mkv", Start: 20 * time.Second, Duration: 10 * time.Second},
	})
	if err != nil {
		t.Fatalf("NewTimeline: %v", err)
	}
	p, err := PlanCut(tl, gop(20, 2*time.Second), req(5*time.Second, 25*time.Second))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	wantWarning(t, p, "discontinuous at 10s")
}

func TestAudioSelectionResolvesToAMixOnlyWhenAsked(t *testing.T) {
	tl := oneHourTimeline(t)
	kf := gop(60, 2*time.Second)

	t.Run("the default keeps every track and encodes nothing", func(t *testing.T) {
		p, err := PlanCut(tl, kf, req(0, 10*time.Second))
		if err != nil {
			t.Fatalf("PlanCut: %v", err)
		}
		if p.Audio.Mode != AudioAll {
			t.Errorf("audio mode = %q, want %q", p.Audio.Mode, AudioAll)
		}
		if p.FilterComplex != "" || p.AudioCodec != "" {
			t.Errorf("the default selection compiled a mix: %q %q", p.FilterComplex, p.AudioCodec)
		}
	})

	t.Run("a mix compiles a routing graph and says it re-encodes", func(t *testing.T) {
		p, err := PlanCut(tl, kf, req(0, 10*time.Second, mixOfTracks(0, 1)))
		if err != nil {
			t.Fatalf("PlanCut: %v", err)
		}
		if !strings.Contains(p.FilterComplex, "[0:a:0]") || !strings.Contains(p.FilterComplex, "[0:a:1]") {
			t.Errorf("the graph does not read both tracks: %s", p.FilterComplex)
		}
		if p.AudioCodec != "flac" {
			t.Errorf("audio codec = %q, want flac into Matroska", p.AudioCodec)
		}
		wantWarning(t, p, "re-encoded")
	})

	t.Run("a mix into mp4 falls back to a codec mp4 players can read", func(t *testing.T) {
		p, err := PlanCut(tl, kf, req(0, 10*time.Second, mixOfTracks(0, 1), func(r *Request) {
			r.OutPath = "/clips/out.mp4"
		}))
		if err != nil {
			t.Fatalf("PlanCut: %v", err)
		}
		if p.AudioCodec != "aac" {
			t.Errorf("audio codec = %q, want aac into MP4", p.AudioCodec)
		}
	})

	t.Run("a mix with no source audio is refused rather than silently silent", func(t *testing.T) {
		_, err := PlanCut(tl, kf, req(0, 10*time.Second, func(r *Request) {
			r.Audio = AudioSelection{Mode: AudioMix}
		}))
		if err == nil {
			t.Fatal("PlanCut produced a mix of nothing")
		}
	})
}

func TestHeadEncoderPrefersX264AndNeverAnswersNothing(t *testing.T) {
	tests := []struct {
		name     string
		prober   EncoderProber
		hardware []string
		want     string
	}{
		{
			name:   "no prober at all",
			prober: nil,
			want:   FallbackHeadEncoder,
		},
		{
			name:   "x264 present and working",
			prober: fakeEncoders{has: []string{"libx264", "h264_nvenc"}, works: []string{"libx264", "h264_nvenc"}},
			want:   "libx264",
		},
		{
			name:     "x264 missing from the build falls through to the hardware that works",
			prober:   fakeEncoders{has: []string{"h264_videotoolbox"}, works: []string{"h264_videotoolbox"}},
			hardware: []string{"h264_nvenc", "h264_videotoolbox"},
			want:     "h264_videotoolbox",
		},
		{
			name:     "nothing demonstrated still names an encoder rather than an empty flag",
			prober:   fakeEncoders{},
			hardware: []string{"h264_nvenc"},
			want:     FallbackHeadEncoder,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HeadEncoder(tc.prober, tc.hardware); got != tc.want {
				t.Fatalf("HeadEncoder = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPlanWithProbesAndThenPlans(t *testing.T) {
	tl := oneHourTimeline(t)
	p := proberFunc(func(_ context.Context, _ string, _, _ time.Duration) (Keyframes, error) {
		return gop(60, 2*time.Second), nil
	})
	plan, err := PlanWith(context.Background(), p, tl, req(5*time.Second, 15*time.Second))
	if err != nil {
		t.Fatalf("PlanWith: %v", err)
	}
	if plan.In != 4*time.Second || plan.InDrift != -time.Second {
		t.Fatalf("in = %s drift = %s, want 4s and -1s", plan.In, plan.InDrift)
	}
}

func TestDescribeSaysWhetherTheInPointMoved(t *testing.T) {
	tl := oneHourTimeline(t)
	kf := gop(60, 2*time.Second)

	tests := []struct {
		name string
		req  Request
		want string
	}{
		{
			name: "a snapped fast cut",
			req:  req(5*time.Second, 15*time.Second),
			want: "earlier than you asked",
		},
		{
			name: "a fast cut that happened to land on a keyframe",
			req:  req(6*time.Second, 15*time.Second),
			want: "exactly where you asked",
		},
		{
			name: "a precise cut",
			req:  req(5*time.Second, 15*time.Second, precise),
			want: "re-encodes the first",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := PlanCut(tl, kf, tc.req)
			if err != nil {
				t.Fatalf("PlanCut: %v", err)
			}
			if got := p.Describe(); !strings.Contains(got, tc.want) {
				t.Fatalf("Describe() = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestDescribeAdmitsWhenTheStartCouldNotBeChecked(t *testing.T) {
	tl := oneHourTimeline(t)
	p, err := PlanCut(tl, Keyframes{}, req(5*time.Second, 15*time.Second))
	if err != nil {
		t.Fatalf("PlanCut: %v", err)
	}
	if got := p.Describe(); !strings.Contains(got, "could not be checked") {
		t.Fatalf("Describe() = %q", got)
	}
}

func precise(r *Request) { r.Mode = ModePrecise }

// mixOfTracks asks for a clip whose audio is those ingest tracks folded down
// through the same routing compiler a live destination uses.
func mixOfTracks(tracks ...int) func(*Request) {
	return func(r *Request) {
		prof := routing.Profile{Mode: routing.ModeSimple, Normalize: routing.NormAuto, SampleRate: 48000}
		var src routing.Source
		for _, tr := range tracks {
			prof.Tracks = append(prof.Tracks, routing.TrackSel{Track: tr, Enabled: true, Gain: 1})
			src.Tracks = append(src.Tracks, routing.Track{Index: tr, Channels: 2, Codec: "aac", Layout: "stereo"})
		}
		r.Audio = AudioSelection{Mode: AudioMix, Profile: prof, Source: src}
	}
}

func wantWarning(t *testing.T, p Plan, substr string) {
	t.Helper()
	for _, w := range p.Warnings {
		if strings.Contains(w, substr) {
			return
		}
	}
	t.Fatalf("no warning containing %q in %v", substr, p.Warnings)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// fakeEncoders stands in for ffmpeg.Tools.
type fakeEncoders struct {
	has   []string
	works []string
}

func (f fakeEncoders) HasEncoder(name string) bool { return contains(f.has, name) }

func (f fakeEncoders) EncoderWorks(name string) (bool, string) {
	if contains(f.works, name) {
		return true, ""
	}
	return false, "did not encode a frame"
}

func contains(hay []string, want string) bool {
	for _, s := range hay {
		if s == want {
			return true
		}
	}
	return false
}
