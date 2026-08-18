package clipper

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseKeyframesReadsBothProbeShapes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []time.Duration
	}{
		{
			name: "packet flags, keyframes only",
			raw: `{"packets":[
				{"pts_time":"0.000000","dts_time":"0.000000","flags":"K__"},
				{"pts_time":"0.033333","dts_time":"0.033333","flags":"___"},
				{"pts_time":"2.000000","dts_time":"2.000000","flags":"K_"},
				{"pts_time":"4.000000","dts_time":"4.000000","flags":"K__"}
			]}`,
			want: []time.Duration{0, 2 * time.Second, 4 * time.Second},
		},
		{
			name: "a flags field that merely contains K somewhere else is not a keyframe",
			raw: `{"packets":[
				{"pts_time":"1.000000","flags":"_K_"},
				{"pts_time":"2.000000","flags":"K__"}
			]}`,
			want: []time.Duration{2 * time.Second},
		},
		{
			name: "a packet with no pts falls back to its dts",
			raw: `{"packets":[
				{"pts_time":"N/A","dts_time":"6.500000","flags":"K__"}
			]}`,
			want: []time.Duration{6500 * time.Millisecond},
		},
		{
			name: "a packet with neither timestamp is dropped rather than treated as zero",
			raw: `{"packets":[
				{"pts_time":"N/A","dts_time":"N/A","flags":"K__"},
				{"pts_time":"3.000000","flags":"K__"}
			]}`,
			want: []time.Duration{3 * time.Second},
		},
		{
			name: "frame output with key_frame as a number",
			raw: `{"frames":[
				{"pts_time":"0.000000","key_frame":1},
				{"pts_time":"1.000000","key_frame":0},
				{"pts_time":"2.000000","key_frame":1}
			]}`,
			want: []time.Duration{0, 2 * time.Second},
		},
		{
			name: "frame output with key_frame as a string",
			raw:  `{"frames":[{"pts_time":"5.000000","key_frame":"1"}]}`,
			want: []time.Duration{5 * time.Second},
		},
		{
			name: "an I frame counts even when key_frame was not reported",
			raw:  `{"frames":[{"pts_time":"7.000000","pict_type":"I"}]}`,
			want: []time.Duration{7 * time.Second},
		},
		{
			name: "a frame with no usable timestamp falls back to the best effort one",
			raw:  `{"frames":[{"pts_time":"N/A","best_effort_timestamp_time":"8.250000","key_frame":1}]}`,
			want: []time.Duration{8250 * time.Millisecond},
		},
		{
			name: "the same instant reported twice is one keyframe",
			raw: `{"packets":[{"pts_time":"2.000000","flags":"K__"}],
			       "frames":[{"pts_time":"2.000000","key_frame":1}]}`,
			want: []time.Duration{2 * time.Second},
		},
		{
			name: "nothing keyframed means an index that admits it knows nothing",
			raw:  `{"packets":[{"pts_time":"1.000000","flags":"___"}]}`,
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kf, err := ParseKeyframes([]byte(tc.raw))
			if err != nil {
				t.Fatalf("ParseKeyframes: %v", err)
			}
			assertTimes(t, kf, tc.want)
			if kf.Known() != (len(tc.want) > 0) {
				t.Errorf("Known() = %v with %d keyframes", kf.Known(), kf.Len())
			}
		})
	}
}

// The TS recorder writes a file whose first packet is at 1.4s, not 0. FFmpeg's
// -ss counts from the start of the FILE, so a keyframe reported at 1.4 is what
// `-ss 0` reaches — and handing 1.4 back to -ss would seek 1.4s past where the
// caller pointed. Getting this wrong is invisible on MKV and wrong on every
// transport stream in the archive.
func TestParseKeyframesRebasesOntoTheContainerStartTime(t *testing.T) {
	raw := `{"format":{"start_time":"1.400000"},"packets":[
		{"pts_time":"1.400000","flags":"K__"},
		{"pts_time":"3.400000","flags":"K__"},
		{"pts_time":"5.400000","flags":"K__"}
	]}`
	kf, err := ParseKeyframes([]byte(raw))
	if err != nil {
		t.Fatalf("ParseKeyframes: %v", err)
	}
	assertTimes(t, kf, []time.Duration{0, 2 * time.Second, 4 * time.Second})
}

func TestParseKeyframesIgnoresAnUnusableContainerStart(t *testing.T) {
	tests := []struct {
		name  string
		start string
	}{
		{name: "missing", start: ""},
		{name: "not available", start: "N/A"},
		{name: "negative, which would push every keyframe later than it is", start: "-1.000000"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"format":{"start_time":"` + tc.start + `"},"packets":[{"pts_time":"2.000000","flags":"K__"}]}`
			kf, err := ParseKeyframes([]byte(raw))
			if err != nil {
				t.Fatalf("ParseKeyframes: %v", err)
			}
			assertTimes(t, kf, []time.Duration{2 * time.Second})
		})
	}
}

func TestParseKeyframesRejectsOutputThatIsNotJSON(t *testing.T) {
	if _, err := ParseKeyframes([]byte("ffprobe: command not found")); err == nil {
		t.Fatal("ParseKeyframes accepted output that is not JSON")
	}
}

func TestKeyframeLookupsFindTheRightSideOfTheInPoint(t *testing.T) {
	// A two-second GOP, the shape FFmpeg's own -g 60 at 30fps produces.
	kf := NewKeyframes([]time.Duration{0, 2 * time.Second, 4 * time.Second, 6 * time.Second})

	tests := []struct {
		name         string
		at           time.Duration
		wantBefore   time.Duration
		haveBefore   bool
		wantAfter    time.Duration
		haveAfter    bool
		wantContains bool
	}{
		{
			name:       "between two keyframes",
			at:         3 * time.Second,
			wantBefore: 2 * time.Second, haveBefore: true,
			wantAfter: 4 * time.Second, haveAfter: true,
		},
		{
			name:       "exactly on a keyframe: at-or-before returns it, after skips past it",
			at:         4 * time.Second,
			wantBefore: 4 * time.Second, haveBefore: true,
			wantAfter: 6 * time.Second, haveAfter: true,
			wantContains: true,
		},
		{
			name:       "on the first keyframe",
			at:         0,
			wantBefore: 0, haveBefore: true,
			wantAfter: 2 * time.Second, haveAfter: true,
			wantContains: true,
		},
		{
			name:       "past the last keyframe: nothing to resume copying at",
			at:         9 * time.Second,
			wantBefore: 6 * time.Second, haveBefore: true,
			haveAfter: false,
		},
		{
			name:       "within the alignment tolerance of a keyframe counts as on it",
			at:         4*time.Second + 3*time.Millisecond,
			wantBefore: 4 * time.Second, haveBefore: true,
			wantAfter: 6 * time.Second, haveAfter: true,
			wantContains: true,
		},
		{
			name:       "outside the alignment tolerance does not",
			at:         4*time.Second + 50*time.Millisecond,
			wantBefore: 4 * time.Second, haveBefore: true,
			wantAfter: 6 * time.Second, haveAfter: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := kf.AtOrBefore(tc.at)
			if ok != tc.haveBefore || (ok && got != tc.wantBefore) {
				t.Errorf("AtOrBefore(%s) = %s,%v; want %s,%v", tc.at, got, ok, tc.wantBefore, tc.haveBefore)
			}
			got, ok = kf.After(tc.at)
			if ok != tc.haveAfter || (ok && got != tc.wantAfter) {
				t.Errorf("After(%s) = %s,%v; want %s,%v", tc.at, got, ok, tc.wantAfter, tc.haveAfter)
			}
			if got := kf.Contains(tc.at, AlignTolerance); got != tc.wantContains {
				t.Errorf("Contains(%s) = %v, want %v", tc.at, got, tc.wantContains)
			}
		})
	}
}

func TestKeyframeLookupsBeforeTheFirstEntryReportThatTheyFoundNothing(t *testing.T) {
	// The probe window started late, so the head of the file is not indexed.
	kf := NewKeyframes([]time.Duration{30 * time.Second, 32 * time.Second})
	if got, ok := kf.AtOrBefore(10 * time.Second); ok {
		t.Fatalf("AtOrBefore returned %s for a point before the whole index", got)
	}
	if _, ok := kf.After(10 * time.Second); !ok {
		t.Fatal("After found nothing after a point before the whole index")
	}
}

func TestEmptyKeyframesAnswersNothingRatherThanZero(t *testing.T) {
	var kf Keyframes
	if kf.Known() {
		t.Fatal("an empty index claims to know something")
	}
	if _, ok := kf.AtOrBefore(time.Second); ok {
		t.Error("AtOrBefore found a keyframe in an empty index")
	}
	if _, ok := kf.After(time.Second); ok {
		t.Error("After found a keyframe in an empty index")
	}
	if kf.Contains(0, AlignTolerance) {
		t.Error("Contains matched in an empty index")
	}
}

func TestNewKeyframesSortsDedupesAndDropsNegatives(t *testing.T) {
	kf := NewKeyframes([]time.Duration{
		4 * time.Second, -time.Second, 0, 2 * time.Second, 2 * time.Second, 0,
	})
	assertTimes(t, kf, []time.Duration{0, 2 * time.Second, 4 * time.Second})
}

func TestKeyframesShiftAndMergeBuildATimelineWideIndex(t *testing.T) {
	first := NewKeyframes([]time.Duration{0, 5 * time.Second})
	second := NewKeyframes([]time.Duration{0, 5 * time.Second}).Shift(10 * time.Second)
	assertTimes(t, first.Merge(second), []time.Duration{
		0, 5 * time.Second, 10 * time.Second, 15 * time.Second,
	})
}

func TestKeyframeArgsAskTheCheapQuestionAndBoundTheRead(t *testing.T) {
	got := KeyframeArgs("/rec/seg0.mkv", 90*time.Second, 40*time.Second)
	joined := strings.Join(got, " ")

	for _, want := range []string{
		"-select_streams v:0",
		"format=start_time:packet=pts_time,dts_time,flags",
		"-read_intervals 90.000000%+40.000000",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	// Decoding the file to find its keyframes is the expensive mistake this
	// probe exists to avoid.
	if strings.Contains(joined, "-skip_frame") || strings.Contains(joined, "-show_frames") {
		t.Errorf("the probe decodes frames instead of reading packets: %s", joined)
	}
	if got[len(got)-1] != "/rec/seg0.mkv" {
		t.Errorf("the input is not last, so -read_intervals applies to nothing: %s", joined)
	}
}

func TestKeyframeArgsWithoutAWindowReadTheWholeFile(t *testing.T) {
	joined := strings.Join(KeyframeArgs("/rec/seg0.mkv", 0, 0), " ")
	if strings.Contains(joined, "-read_intervals") {
		t.Fatalf("an unbounded probe still bounded the read: %s", joined)
	}
}

func TestKeyframeArgsClampANegativeStart(t *testing.T) {
	joined := strings.Join(KeyframeArgs("/rec/seg0.mkv", -30*time.Second, time.Minute), " ")
	if !strings.Contains(joined, "-read_intervals 0.000000%+60.000000") {
		t.Fatalf("a negative start was not clamped: %s", joined)
	}
}

// A bounded read that finds nothing means the window missed — a very long GOP,
// or a segment shorter than the lookback. Giving up there would leave the cut
// unsnapped for a file that is perfectly readable.
// The widen is BOUNDED. It used to drop -read_intervals entirely and pay for
// the whole file, which on a multi-hour archive means millions of packet
// objects buffered in CombinedOutput -- on a path an authenticated request can
// reach through handleClipKeyframes. Both cases the fallback exists for survive
// a bounded widen: a long GOP is seconds, and a file shorter than the lookback
// fits inside FallbackWindow entirely.
func TestFFprobeWidensAroundTheCallersPointWhenABoundedReadFindsNothing(t *testing.T) {
	var calls [][]string
	p := FFprobe{
		Bin: "ffprobe",
		Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
			calls = append(calls, args)
			if len(calls) == 1 {
				return []byte(`{"packets":[]}`), nil
			}
			return []byte(`{"packets":[{"pts_time":"0.000000","flags":"K__"}]}`), nil
		},
	}

	kf, err := p.Keyframes(context.Background(), "/rec/seg0.mkv", time.Hour, time.Minute)
	if err != nil {
		t.Fatalf("Keyframes: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("ffprobe ran %d times, want 2", len(calls))
	}
	second := strings.Join(calls[1], " ")
	if !strings.Contains(second, "-read_intervals") {
		t.Error("the widened read dropped -read_intervals, so ffprobe emits one " +
			"JSON object per packet for the WHOLE file and CombinedOutput buffers " +
			"all of it. A long archive is hundreds of megabytes, and this path is " +
			"reachable from handleClipKeyframes — the allocation would be " +
			"proportional to the recording rather than to the question.")
	}
	// Centred on the caller's point, not restarted at zero: a long GOP sits just
	// outside their window, which is where the keyframe actually is.
	if !strings.Contains(second, "3300.000000%+600.000000") {
		t.Errorf("widened read asked for %q, want a %v window centred on the "+
			"caller's one-hour point", second, FallbackWindow)
	}
	assertTimes(t, kf, []time.Duration{0})
}

// The widen must not run past the start of the file.
func TestTheWidenedReadDoesNotAskForANegativeStart(t *testing.T) {
	var calls [][]string
	p := FFprobe{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		calls = append(calls, args)
		return []byte(`{"packets":[]}`), nil
	}}
	if _, err := p.Keyframes(context.Background(), "/rec/seg0.mkv", time.Second, time.Minute); err != nil {
		t.Fatalf("Keyframes: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("ffprobe ran %d times, want 2", len(calls))
	}
	if got := strings.Join(calls[1], " "); !strings.Contains(got, "0.000000%+600.000000") {
		t.Errorf("widened read asked for %q, want it clamped to the start of the file", got)
	}
}

func TestFFprobeDoesNotWidenWhenTheBoundedReadSucceeded(t *testing.T) {
	calls := 0
	p := FFprobe{Run: func(_ context.Context, _ string, _ []string) ([]byte, error) {
		calls++
		return []byte(`{"packets":[{"pts_time":"1.000000","flags":"K__"}]}`), nil
	}}
	if _, err := p.Keyframes(context.Background(), "/rec/seg0.mkv", 0, time.Minute); err != nil {
		t.Fatalf("Keyframes: %v", err)
	}
	if calls != 1 {
		t.Fatalf("ffprobe ran %d times, want 1", calls)
	}
}

// A probe that fails must not fail the cut: indexFor collects the failure as a
// warning and the planner falls open to FFmpeg's own seek.
func TestIndexForTurnsAProbeFailureIntoAWarningRatherThanAnError(t *testing.T) {
	segs := []Segment{
		{Path: "/seg0.mkv", Start: 0, Duration: 10 * time.Second},
		{Path: "/seg1.mkv", Start: 10 * time.Second, Duration: 10 * time.Second},
	}
	p := proberFunc(func(_ context.Context, path string, _, _ time.Duration) (Keyframes, error) {
		if path == "/seg0.mkv" {
			return Keyframes{}, errors.New("permission denied")
		}
		return NewKeyframes([]time.Duration{0, 2 * time.Second}), nil
	})

	kf, warns := indexFor(context.Background(), p, segs, 9*time.Second)
	if len(warns) != 1 || !strings.Contains(warns[0], "/seg0.mkv") {
		t.Fatalf("warnings = %v, want one naming /seg0.mkv", warns)
	}
	// The surviving segment's keyframes are still there, shifted onto the
	// timeline.
	assertTimes(t, kf, []time.Duration{10 * time.Second, 12 * time.Second})
}

// Every keyframe the planner uses is within a GOP or two of the in-point, so
// reading the far end of a long boundary-spanning cut costs minutes to learn
// nothing.
func TestIndexForOnlyProbesTheSegmentsNearTheInPoint(t *testing.T) {
	segs := []Segment{
		{Path: "/seg0.mkv", Start: 0, Duration: time.Hour},
		{Path: "/seg1.mkv", Start: time.Hour, Duration: time.Hour},
		{Path: "/seg2.mkv", Start: 2 * time.Hour, Duration: time.Hour},
	}
	var probed []string
	p := proberFunc(func(_ context.Context, path string, _, _ time.Duration) (Keyframes, error) {
		probed = append(probed, path)
		return NewKeyframes([]time.Duration{0}), nil
	})

	if _, warns := indexFor(context.Background(), p, segs, time.Hour+time.Minute); len(warns) != 0 {
		t.Fatalf("unexpected warnings %v", warns)
	}
	if len(probed) != 1 || probed[0] != "/seg1.mkv" {
		t.Fatalf("probed %v, want only /seg1.mkv", probed)
	}
}

func TestIndexForProbesBothSidesOfASegmentBoundary(t *testing.T) {
	segs := []Segment{
		{Path: "/seg0.mkv", Start: 0, Duration: time.Hour},
		{Path: "/seg1.mkv", Start: time.Hour, Duration: time.Hour},
	}
	var probed []string
	p := proberFunc(func(_ context.Context, path string, from, _ time.Duration) (Keyframes, error) {
		probed = append(probed, path+"@"+from.String())
		return Keyframes{}, nil
	})

	indexFor(context.Background(), p, segs, time.Hour+time.Second)
	want := []string{
		// The tail of the previous file, read from just before the in-point.
		"/seg0.mkv@" + (time.Hour - ProbeLookback + time.Second).String(),
		// The head of the next, from its own start.
		"/seg1.mkv@0s",
	}
	if len(probed) != len(want) {
		t.Fatalf("probed %v, want %v", probed, want)
	}
	for i := range want {
		if probed[i] != want[i] {
			t.Errorf("probe %d was %s, want %s", i, probed[i], want[i])
		}
	}
}

func TestIndexForWithNoProberIsSilentRatherThanFatal(t *testing.T) {
	kf, warns := indexFor(context.Background(), nil, []Segment{{Path: "/a.mkv", Duration: time.Minute}}, 0)
	if kf.Known() || len(warns) != 0 {
		t.Fatalf("got %d keyframes and %v", kf.Len(), warns)
	}
}

// proberFunc adapts a function to Prober.
type proberFunc func(ctx context.Context, path string, from, window time.Duration) (Keyframes, error)

func (f proberFunc) Keyframes(ctx context.Context, path string, from, window time.Duration) (Keyframes, error) {
	return f(ctx, path, from, window)
}

func assertTimes(t *testing.T, kf Keyframes, want []time.Duration) {
	t.Helper()
	got := kf.Times()
	if len(got) != len(want) {
		t.Fatalf("keyframes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("keyframes = %v, want %v", got, want)
		}
	}
}
