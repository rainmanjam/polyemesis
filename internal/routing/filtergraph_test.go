package routing

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// These tests cover the optional stages Compile gained after v1: per-track
// denoise, ducking, a configurable loudness target, audio delay and role
// exclusion. Every case asserts the exact filter string, because "roughly the
// right filters" is indistinguishable from "wrong" until someone hears it.

func annotate(s Source, anns ...TrackAnnotation) Source {
	return s.WithAnnotations(anns)
}

// duckable is a two-track profile with the mic on track 1 and music on track 2,
// which is the shape every ducking case wants.
func duckable(norm NormMode) Profile {
	return simple(norm, 0, 1)
}

// ------------------------------------------------------------ opt-in is opt-in

// The whole feature set is worthless if it moves a byte of anyone's existing
// audio. This is the same guarantee routing_test.go makes, restated against the
// new fields so that a change to any one of them fails here first.
func TestZeroValuedOptionalFieldsChangeNothing(t *testing.T) {
	tests := []struct {
		name string
		p    Profile
		src  Source
		want string
	}{
		{
			name: "single stereo track",
			p:    simple(NormAuto, 0),
			src:  stereoSource(3),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[a_t0]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "two tracks, auto limiter",
			p:    simple(NormAuto, 0, 1),
			src:  stereoSource(3),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
				"[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
				"[a_mix]alimiter=limit=0.95:level=disabled[a_norm];" +
				"[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "loudnorm keeps its original fixed parameters forever",
			p:    simple(NormLoudnorm, 0, 1),
			src:  stereoSource(2),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
				"[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
				"[a_mix]loudnorm=I=-16:TP=-1.5:LRA=11[a_norm];" +
				"[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "5.1 downmix",
			p:    simple(NormAuto, 0),
			src:  Source{Tracks: []Track{{Index: 0, Channels: 6, Layout: "5.1"}}},
			want: "[0:a:0]pan=stereo|c0=0.4143*c0+0.2929*c2+0.2929*c4|c1=0.4143*c1+0.2929*c2+0.2929*c5[a_t0];" +
				"[a_t0]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "matrix mode",
			p: Profile{Mode: ModeMatrix, Normalize: NormOff, SampleRate: 48000, Matrix: []Cell{
				{Track: 1, Channel: 0, Out: OutL, Gain: 1},
				{Track: 1, Channel: 1, Out: OutR, Gain: 1},
			}},
			src: stereoSource(2),
			want: "[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
				"[a_t1]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "annotated source with no destination policy and no denoise",
			p:    simple(NormOff, 0, 1),
			src: annotate(stereoSource(2),
				TrackAnnotation{Track: 0, Role: RoleMic, Label: "Host", Language: "en"},
				TrackAnnotation{Track: 1, Role: RoleMusic, Label: "Bed"}),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
				"[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
				"[a_mix]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "excludeRoles set but the source is unannotated",
			p:    withExcludes(simple(NormOff, 0, 1), RoleMusic),
			src:  stereoSource(2),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
				"[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
				"[a_mix]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "loudness target present but normalization explicitly off",
			p:    withLoudness(simple(NormOff, 0, 1), Loudness{TargetLUFS: LUFSStreaming}),
			src:  stereoSource(2),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
				"[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
				"[a_mix]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "loudness target present but the limiter was chosen explicitly",
			p:    withLoudness(simple(NormLimiter, 0), Loudness{TargetLUFS: LUFSBroadcast}),
			src:  stereoSource(2),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[a_t0]alimiter=limit=0.95:level=disabled[a_norm];" +
				"[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "zero delay adds no adelay",
			p:    withDelay(simple(NormOff, 0), 0),
			src:  stereoSource(1),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[a_t0]aresample=48000:async=1:first_pts=0[aout]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Compile(tc.p, tc.src)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if res.FilterComplex != tc.want {
				t.Errorf("\n got  %s\n want %s", res.FilterComplex, tc.want)
			}
			if res.VideoDelayMS != 0 {
				t.Errorf("VideoDelayMS = %d, want 0", res.VideoDelayMS)
			}
		})
	}
}

// ------------------------------------------------------------------- denoise

func TestDenoiseIsPerTrackAndBeforeTheSum(t *testing.T) {
	tests := []struct {
		name string
		p    Profile
		src  Source
		want string
	}{
		{
			name: "single annotated track",
			p:    simple(NormOff, 0),
			src:  annotate(stereoSource(2), TrackAnnotation{Track: 0, Role: RoleMic, Denoise: true}),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1," + DenoiseFilter + "[a_t0];" +
				"[a_t0]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "only the annotated track is denoised",
			p:    simple(NormOff, 0, 1),
			src: annotate(stereoSource(2),
				TrackAnnotation{Track: 1, Role: RoleMic, Denoise: true}),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1," + DenoiseFilter + "[a_t1];" +
				"[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
				"[a_mix]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "denoise sits after the downmix, on the two surviving channels",
			p:    simple(NormOff, 0),
			src: annotate(Source{Tracks: []Track{{Index: 0, Channels: 6, Layout: "5.1"}}},
				TrackAnnotation{Track: 0, Denoise: true}),
			want: "[0:a:0]pan=stereo|c0=0.4143*c0+0.2929*c2+0.2929*c4|c1=0.4143*c1+0.2929*c2+0.2929*c5," + DenoiseFilter + "[a_t0];" +
				"[a_t0]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "an annotation for a track this destination does not take is inert",
			p:    simple(NormOff, 0),
			src: annotate(stereoSource(2),
				TrackAnnotation{Track: 1, Denoise: true}),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[a_t0]aresample=48000:async=1:first_pts=0[aout]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Compile(tc.p, tc.src)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if res.FilterComplex != tc.want {
				t.Errorf("\n got  %s\n want %s", res.FilterComplex, tc.want)
			}
		})
	}
}

// arnndn without a model file makes FFmpeg refuse to build the graph, which
// takes the whole destination down. Whatever else changes, the denoiser must
// stay one that needs nothing on disk.
func TestDenoiseFilterNeedsNoModelFile(t *testing.T) {
	if strings.Contains(DenoiseFilter, "arnndn") || strings.Contains(DenoiseFilter, "m=") {
		t.Fatalf("denoise filter %q depends on an external model file", DenoiseFilter)
	}
}

// -------------------------------------------------------------------- ducking

func TestDuckingGraphTopology(t *testing.T) {
	const params = "threshold=0.0631:ratio=8:attack=20:release=300:detection=rms:link=maximum"

	tests := []struct {
		name string
		p    Profile
		src  Source
		want string
	}{
		{
			name: "mic ducks music and still reaches the output",
			p:    withDucking(duckable(NormOff), Ducking{Trigger: []int{0}, Target: []int{1}}),
			src:  stereoSource(2),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
				"[a_t0]asplit=2[a_t0_mix][a_t0_key];" +
				"[a_t1][a_t0_key]sidechaincompress=" + params + "[a_duck];" +
				"[a_t0_mix][a_duck]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
				"[a_mix]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "two targets are summed once and ducked once",
			p:    withDucking(simple(NormOff, 0, 1, 2), Ducking{Trigger: []int{0}, Target: []int{1, 2}}),
			src:  stereoSource(3),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
				"[0:a:2]pan=stereo|c0=1*c0|c1=1*c1[a_t2];" +
				"[a_t0]asplit=2[a_t0_mix][a_t0_key];" +
				"[a_t1][a_t2]amix=inputs=2:duration=longest:normalize=0[a_duckin];" +
				"[a_duckin][a_t0_key]sidechaincompress=" + params + "[a_duck];" +
				"[a_t0_mix][a_duck]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
				"[a_mix]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "two triggers are summed into one detector key",
			p:    withDucking(simple(NormOff, 0, 1, 2), Ducking{Trigger: []int{0, 1}, Target: []int{2}}),
			src:  stereoSource(3),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
				"[0:a:2]pan=stereo|c0=1*c0|c1=1*c1[a_t2];" +
				"[a_t0]asplit=2[a_t0_mix][a_t0_key];" +
				"[a_t1]asplit=2[a_t1_mix][a_t1_key];" +
				"[a_t0_key][a_t1_key]amix=inputs=2:duration=longest:normalize=0[a_duckkey];" +
				"[a_t2][a_duckkey]sidechaincompress=" + params + "[a_duck];" +
				"[a_t0_mix][a_t1_mix][a_duck]amix=inputs=3:duration=longest:normalize=0[a_mix];" +
				"[a_mix]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "the duck takes the first target's place in the mix order",
			p:    withDucking(simple(NormOff, 0, 1, 2), Ducking{Trigger: []int{2}, Target: []int{0, 1}}),
			src:  stereoSource(3),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
				"[0:a:2]pan=stereo|c0=1*c0|c1=1*c1[a_t2];" +
				"[a_t2]asplit=2[a_t2_mix][a_t2_key];" +
				"[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_duckin];" +
				"[a_duckin][a_t2_key]sidechaincompress=" + params + "[a_duck];" +
				"[a_duck][a_t2_mix]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
				"[a_mix]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "a trigger this destination does not carry is tapped straight off the ingest",
			p:    withDucking(simple(NormOff, 1), Ducking{Trigger: []int{0}, Target: []int{1}}),
			src:  stereoSource(2),
			want: "[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
				"[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_k0];" +
				"[a_t1][a_k0]sidechaincompress=" + params + "[a_duck];" +
				"[a_duck]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "a tapped trigger is denoised too, so room noise cannot open the duck",
			p:    withDucking(simple(NormOff, 1), Ducking{Trigger: []int{0}, Target: []int{1}}),
			src:  annotate(stereoSource(2), TrackAnnotation{Track: 0, Role: RoleMic, Denoise: true}),
			want: "[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
				"[0:a:0]pan=stereo|c0=1*c0|c1=1*c1," + DenoiseFilter + "[a_k0];" +
				"[a_t1][a_k0]sidechaincompress=" + params + "[a_duck];" +
				"[a_duck]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "an unsorted target list compiles to the same graph as a sorted one",
			p:    withDucking(simple(NormOff, 0, 1, 2), Ducking{Trigger: []int{0}, Target: []int{2, 1}}),
			src:  stereoSource(3),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
				"[0:a:2]pan=stereo|c0=1*c0|c1=1*c1[a_t2];" +
				"[a_t0]asplit=2[a_t0_mix][a_t0_key];" +
				"[a_t1][a_t2]amix=inputs=2:duration=longest:normalize=0[a_duckin];" +
				"[a_duckin][a_t0_key]sidechaincompress=" + params + "[a_duck];" +
				"[a_t0_mix][a_duck]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
				"[a_mix]aresample=48000:async=1:first_pts=0[aout]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Compile(tc.p, tc.src)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if res.FilterComplex != tc.want {
				t.Errorf("\n got  %s\n want %s", res.FilterComplex, tc.want)
			}
		})
	}
}

// The trigger reaching the output is the part that is easy to get wrong and
// catastrophic when wrong: the mic simply disappears from the stream.
func TestDuckingKeepsTheTriggerInTheMix(t *testing.T) {
	p := withDucking(duckable(NormOff), Ducking{Trigger: []int{0}, Target: []int{1}})
	res, err := Compile(p, stereoSource(2))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.FilterComplex, "asplit=2[a_t0_mix][a_t0_key]") {
		t.Fatalf("trigger must be split, not consumed by the detector: %s", res.FilterComplex)
	}
	if !strings.Contains(res.FilterComplex, "[a_t0_mix][a_duck]amix") {
		t.Errorf("trigger's mix leg never reaches amix: %s", res.FilterComplex)
	}
	if got := strings.Count(res.FilterComplex, "[a_t0_key]"); got != 2 {
		t.Errorf("detector key used %d times, want exactly 2 (one producer, one consumer)", got)
	}
}

func TestDuckingParametersAndDefaults(t *testing.T) {
	tests := []struct {
		name string
		d    Ducking
		want string
	}{
		{
			name: "defaults",
			d:    Ducking{Trigger: []int{0}, Target: []int{1}},
			want: "threshold=0.0631:ratio=8:attack=20:release=300:detection=rms:link=maximum",
		},
		{
			name: "explicit values",
			d: Ducking{Trigger: []int{0}, Target: []int{1},
				ThresholdDB: -12, Ratio: 4, AttackMS: 5, ReleaseMS: 450},
			want: "threshold=0.2512:ratio=4:attack=5:release=450:detection=rms:link=maximum",
		},
		{
			name: "the quietest legal threshold stays above sidechaincompress' floor",
			d: Ducking{Trigger: []int{0}, Target: []int{1},
				ThresholdDB: MinDuckThresholdDB},
			want: "threshold=0.001:ratio=8:attack=20:release=300:detection=rms:link=maximum",
		},
		{
			name: "fractional attack survives formatting",
			d: Ducking{Trigger: []int{0}, Target: []int{1},
				AttackMS: MinDuckAttackMS, ReleaseMS: 0.5},
			want: "threshold=0.0631:ratio=8:attack=0.01:release=0.5:detection=rms:link=maximum",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Compile(withDucking(duckable(NormOff), tc.d), stereoSource(2))
			if err != nil {
				t.Fatal(err)
			}
			want := "sidechaincompress=" + tc.want + "["
			if !strings.Contains(res.FilterComplex, want) {
				t.Errorf("\n got  %s\n want it to contain %s", res.FilterComplex, want)
			}
		})
	}
}

// sidechaincompress takes a linear threshold in 0.000976563..1. Handing it dB
// would be accepted as a number and mean something wildly different.
func TestDuckThresholdIsConvertedToLinear(t *testing.T) {
	res, err := Compile(withDucking(duckable(NormOff), Ducking{Trigger: []int{0}, Target: []int{1}}), stereoSource(2))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.FilterComplex, "threshold=-24") {
		t.Fatalf("dB leaked into sidechaincompress: %s", res.FilterComplex)
	}
}

// A duck that cannot be built must leave the mix alone, not break the graph.
// Silence is a far worse failure than an un-ducked music bed.
func TestDuckingThatCannotBeBuiltIsSkippedWithAWarning(t *testing.T) {
	tests := []struct {
		name    string
		p       Profile
		src     Source
		want    string
		warning string
	}{
		{
			name: "no target track is in this destination's mix",
			p:    withDucking(simple(NormOff, 0), Ducking{Trigger: []int{0}, Target: []int{1}}),
			src:  stereoSource(2),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[a_t0]aresample=48000:async=1:first_pts=0[aout]",
			warning: "none of its target tracks are in this destination's mix",
		},
		{
			name: "the trigger track is not on the ingest at all",
			p:    withDucking(simple(NormOff, 0, 1), Ducking{Trigger: []int{4}, Target: []int{1}}),
			src:  stereoSource(2),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
				"[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
				"[a_mix]aresample=48000:async=1:first_pts=0[aout]",
			warning: "none of its trigger tracks are present on the ingest",
		},
		{
			name: "the target was excluded by this destination's role policy",
			p: withExcludes(
				withDucking(duckable(NormOff), Ducking{Trigger: []int{0}, Target: []int{1}}),
				RoleMusic),
			src: annotate(stereoSource(2), TrackAnnotation{Track: 1, Role: RoleMusic}),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[a_t0]aresample=48000:async=1:first_pts=0[aout]",
			warning: "none of its target tracks are in this destination's mix",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Compile(tc.p, tc.src)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if res.FilterComplex != tc.want {
				t.Errorf("\n got  %s\n want %s", res.FilterComplex, tc.want)
			}
			if !hasWarning(res.Warnings, tc.warning) {
				t.Errorf("warnings %v, want one containing %q", res.Warnings, tc.warning)
			}
		})
	}
}

// An empty Trigger or Target is not a duck at all. Validate refuses it outright
// and Compile must surface that rather than emitting half a sidechain — a
// sidechaincompress whose key never arrives stalls the whole graph.
func TestHalfConfiguredDuckingIsRefused(t *testing.T) {
	for _, d := range []Ducking{
		{Trigger: []int{0}},
		{Target: []int{1}},
		{},
	} {
		p := duckable(NormOff)
		p.Ducking = &d
		if _, err := Compile(p, stereoSource(2)); err == nil {
			t.Errorf("ducking %+v was accepted", d)
		}
	}
}

// ------------------------------------------------------------------- loudness

func TestLoudnessTarget(t *testing.T) {
	tests := []struct {
		name     string
		p        Profile
		src      Source
		wantNorm NormMode
		want     string
	}{
		{
			name:     "explicit loudnorm with a target",
			p:        withLoudness(simple(NormLoudnorm, 0), Loudness{TargetLUFS: LUFSStreaming}),
			src:      stereoSource(1),
			wantNorm: NormLoudnorm,
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[a_t0]loudnorm=I=-14:TP=-1:LRA=11[a_norm];" +
				"[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name:     "a target arms loudnorm under auto, replacing the limiter",
			p:        withLoudness(simple(NormAuto, 0, 1), Loudness{TargetLUFS: LUFSStreaming}),
			src:      stereoSource(2),
			wantNorm: NormLoudnorm,
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
				"[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
				"[a_mix]loudnorm=I=-14:TP=-1:LRA=11[a_norm];" +
				"[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name:     "a target arms loudnorm under auto even for a single track",
			p:        withLoudness(simple(NormAuto, 0), Loudness{TargetLUFS: LUFSPodcast}),
			src:      stereoSource(1),
			wantNorm: NormLoudnorm,
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[a_t0]loudnorm=I=-16:TP=-1:LRA=11[a_norm];" +
				"[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name: "every parameter is honoured",
			p: withLoudness(simple(NormLoudnorm, 0),
				Loudness{TargetLUFS: LUFSBroadcast, TruePeakDB: -2.5, RangeLU: 7}),
			src:      stereoSource(1),
			wantNorm: NormLoudnorm,
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[a_t0]loudnorm=I=-23:TP=-2.5:LRA=7[a_norm];" +
				"[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name:     "explicit off is never overridden by a target",
			p:        withLoudness(simple(NormOff, 0), Loudness{TargetLUFS: LUFSStreaming}),
			src:      stereoSource(1),
			wantNorm: NormOff,
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[a_t0]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name:     "an explicit limiter is never overridden by a target",
			p:        withLoudness(simple(NormLimiter, 0, 1), Loudness{TargetLUFS: LUFSStreaming}),
			src:      stereoSource(2),
			wantNorm: NormLimiter,
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
				"[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
				"[a_mix]alimiter=limit=0.95:level=disabled[a_norm];" +
				"[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Compile(tc.p, tc.src)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if res.FilterComplex != tc.want {
				t.Errorf("\n got  %s\n want %s", res.FilterComplex, tc.want)
			}
			if res.Normalization != tc.wantNorm {
				t.Errorf("normalization = %q, want %q", res.Normalization, tc.wantNorm)
			}
		})
	}
}

// The defaults exist so a UI that only asks for "how loud?" still produces a
// safe true-peak ceiling and the dynamics behaviour the fixed stage always had.
func TestLoudnessDefaultsAreFilledIn(t *testing.T) {
	res, err := Compile(withLoudness(simple(NormLoudnorm, 0), Loudness{TargetLUFS: LUFSStreaming}), stereoSource(1))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.FilterComplex, "TP=-1:LRA=11") {
		t.Errorf("unset TP/LRA must default to %v/%v: %s", DefaultTruePeakDB, DefaultLoudnessLRA, res.FilterComplex)
	}
}

// ---------------------------------------------------------------------- delay

func TestDelay(t *testing.T) {
	tests := []struct {
		name      string
		delay     int
		want      string
		wantVideo int
	}{
		{
			name:  "positive delay holds the audio back",
			delay: 250,
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[a_t0]adelay=delays=250:all=1[a_delay];" +
				"[a_delay]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name:  "the broadcast moderation delay is just a big positive delay",
			delay: MaxDelayMS,
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[a_t0]adelay=delays=30000:all=1[a_delay];" +
				"[a_delay]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			name:  "negative delay leaves the audio graph alone and asks for video to be held",
			delay: -120,
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[a_t0]aresample=48000:async=1:first_pts=0[aout]",
			wantVideo: 120,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Compile(withDelay(simple(NormOff, 0), tc.delay), stereoSource(1))
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if res.FilterComplex != tc.want {
				t.Errorf("\n got  %s\n want %s", res.FilterComplex, tc.want)
			}
			if res.VideoDelayMS != tc.wantVideo {
				t.Errorf("VideoDelayMS = %d, want %d", res.VideoDelayMS, tc.wantVideo)
			}
		})
	}
}

// The delay is the destination's, so it must hold the finished mix — after
// loudness, before the resample that pins the encoder's clock.
func TestDelaySitsAfterLoudnessAndBeforeResample(t *testing.T) {
	p := withDelay(withLoudness(simple(NormLoudnorm, 0, 1), Loudness{TargetLUFS: LUFSStreaming}), 40)
	res, err := Compile(p, stereoSource(2))
	if err != nil {
		t.Fatal(err)
	}
	want := "[a_mix]loudnorm=I=-14:TP=-1:LRA=11[a_norm];" +
		"[a_norm]adelay=delays=40:all=1[a_delay];" +
		"[a_delay]aresample=48000:async=1:first_pts=0[aout]"
	if !strings.Contains(res.FilterComplex, want) {
		t.Errorf("\n got  %s\n want it to contain %s", res.FilterComplex, want)
	}
}

// -------------------------------------------------------------- role policy

func TestExcludeRolesDropsTracksBeforeTheMix(t *testing.T) {
	src := annotate(stereoSource(3),
		TrackAnnotation{Track: 0, Role: RoleMic},
		TrackAnnotation{Track: 1, Role: RoleMusic},
		TrackAnnotation{Track: 2, Role: RoleGame})

	tests := []struct {
		name       string
		p          Profile
		want       string
		wantTracks []int
	}{
		{
			name: "music is dropped, everything else survives",
			p:    withExcludes(simple(NormOff, 0, 1, 2), RoleMusic),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[0:a:2]pan=stereo|c0=1*c0|c1=1*c1[a_t2];" +
				"[a_t0][a_t2]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
				"[a_mix]aresample=48000:async=1:first_pts=0[aout]",
			wantTracks: []int{0, 2},
		},
		{
			name: "two roles at once",
			p:    withExcludes(simple(NormOff, 0, 1, 2), RoleMusic, RoleGame),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[a_t0]aresample=48000:async=1:first_pts=0[aout]",
			wantTracks: []int{0},
		},
		{
			name: "an excluded role nobody carries changes nothing",
			p:    withExcludes(simple(NormOff, 0, 1, 2), RoleCommentary),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
				"[0:a:2]pan=stereo|c0=1*c0|c1=1*c1[a_t2];" +
				"[a_t0][a_t1][a_t2]amix=inputs=3:duration=longest:normalize=0[a_mix];" +
				"[a_mix]aresample=48000:async=1:first_pts=0[aout]",
			wantTracks: []int{0, 1, 2},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Compile(tc.p, src)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if res.FilterComplex != tc.want {
				t.Errorf("\n got  %s\n want %s", res.FilterComplex, tc.want)
			}
			if len(res.Tracks) != len(tc.wantTracks) {
				t.Fatalf("tracks = %v, want %v", res.Tracks, tc.wantTracks)
			}
			for i, got := range res.Tracks {
				if got != tc.wantTracks[i] {
					t.Fatalf("tracks = %v, want %v", res.Tracks, tc.wantTracks)
				}
			}
		})
	}
}

// A track vanishing because of a policy set on another screen weeks ago is the
// hardest kind of missing audio to diagnose, so it has to be said out loud.
func TestExcludedTrackIsExplainedInTheWarnings(t *testing.T) {
	src := annotate(stereoSource(2), TrackAnnotation{Track: 1, Role: RoleMusic, Label: "Bed"})
	res, err := Compile(withExcludes(simple(NormOff, 0, 1), RoleMusic), src)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(res.Warnings, `track 2 carries the "music" role`) {
		t.Errorf("warnings %v do not explain the exclusion", res.Warnings)
	}
	if len(res.Warnings) != 1 {
		t.Errorf("one warning per excluded track, got %v", res.Warnings)
	}
}

// Refusing to stream is the right answer here: the destination that excludes
// music is the one that must not carry it, and streaming it anyway is the
// failure the whole feature exists to prevent.
func TestExcludingEverythingIsAnErrorThatSaysWhy(t *testing.T) {
	src := annotate(stereoSource(2),
		TrackAnnotation{Track: 0, Role: RoleMusic},
		TrackAnnotation{Track: 1, Role: RoleMusic})
	_, err := Compile(withExcludes(simple(NormOff, 0, 1), RoleMusic), src)
	if err == nil {
		t.Fatal("want an error when the policy removes every track")
	}
	if !errors.Is(err, ErrNoAudio) {
		t.Errorf("error %v does not wrap ErrNoAudio; callers switch on it", err)
	}
	if !strings.Contains(err.Error(), "role policy") {
		t.Errorf("error %q does not say the policy caused it", err)
	}
}

// ---------------------------------------------------------------- everything

// Every stage at once, in signal-chain order: denoise, duck, sum, loudness,
// delay, resample.
func TestAllStagesTogetherInSignalChainOrder(t *testing.T) {
	src := annotate(stereoSource(4),
		TrackAnnotation{Track: 0, Role: RoleMic, Denoise: true},
		TrackAnnotation{Track: 1, Role: RoleMusic},
		TrackAnnotation{Track: 2, Role: RoleGame},
		TrackAnnotation{Track: 3, Role: RoleCommentary})

	p := simple(NormAuto, 0, 1, 2, 3)
	p.ExcludeRoles = []TrackRole{RoleCommentary}
	p.Ducking = &Ducking{Trigger: []int{0}, Target: []int{1, 2}}
	p.Loudness = &Loudness{TargetLUFS: LUFSStreaming}
	p.DelayMS = 200

	res, err := Compile(p, src)
	if err != nil {
		t.Fatal(err)
	}
	want := "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1," + DenoiseFilter + "[a_t0];" +
		"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
		"[0:a:2]pan=stereo|c0=1*c0|c1=1*c1[a_t2];" +
		"[a_t0]asplit=2[a_t0_mix][a_t0_key];" +
		"[a_t1][a_t2]amix=inputs=2:duration=longest:normalize=0[a_duckin];" +
		"[a_duckin][a_t0_key]sidechaincompress=threshold=0.0631:ratio=8:attack=20:release=300:detection=rms:link=maximum[a_duck];" +
		"[a_t0_mix][a_duck]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
		"[a_mix]loudnorm=I=-14:TP=-1:LRA=11[a_norm];" +
		"[a_norm]adelay=delays=200:all=1[a_delay];" +
		"[a_delay]aresample=48000:async=1:first_pts=0[aout]"
	if res.FilterComplex != want {
		t.Errorf("\n got  %s\n want %s", res.FilterComplex, want)
	}
	if res.Summary != "Tracks 1, 2, 3 → stereo" {
		t.Errorf("summary = %q", res.Summary)
	}
	if res.Normalization != NormLoudnorm {
		t.Errorf("normalization = %q, want loudnorm", res.Normalization)
	}
}

// Compiling twice must produce the same bytes; a map iterated in the wrong
// place would make destinations restart on every reconcile.
func TestCompilationIsDeterministic(t *testing.T) {
	src := annotate(stereoSource(4),
		TrackAnnotation{Track: 0, Role: RoleMic, Denoise: true},
		TrackAnnotation{Track: 3, Role: RoleMusic})
	p := simple(NormAuto, 0, 1, 2, 3)
	p.Ducking = &Ducking{Trigger: []int{0, 1}, Target: []int{2, 3}}
	p.Loudness = &Loudness{TargetLUFS: LUFSStreaming}

	first, err := Compile(p, src)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		got, err := Compile(p, src)
		if err != nil {
			t.Fatal(err)
		}
		if got.FilterComplex != first.FilterComplex {
			t.Fatalf("run %d differs\n got  %s\n want %s", i, got.FilterComplex, first.FilterComplex)
		}
	}
}

// Compile must not scribble on the profile it was handed; the engine hands it
// the same struct on every reconcile.
func TestCompileDoesNotMutateItsInputs(t *testing.T) {
	p := simple(NormAuto, 0, 1)
	p.Ducking = &Ducking{Trigger: []int{0}, Target: []int{1}}
	p.Loudness = &Loudness{TargetLUFS: LUFSStreaming}
	src := annotate(stereoSource(2), TrackAnnotation{Track: 0, Denoise: true})

	if _, err := Compile(p, src); err != nil {
		t.Fatal(err)
	}
	if p.Ducking.ThresholdDB != 0 || p.Ducking.Ratio != 0 {
		t.Errorf("ducking defaults were written back into the profile: %+v", *p.Ducking)
	}
	if p.Loudness.TruePeakDB != 0 || p.Loudness.RangeLU != 0 {
		t.Errorf("loudness defaults were written back into the profile: %+v", *p.Loudness)
	}
	if p.Normalize != NormAuto {
		t.Errorf("normalize was rewritten to %q", p.Normalize)
	}
}

// ------------------------------------------------------------- real FFmpeg

// A filter string that only satisfies a unit test is worthless: FFmpeg is the
// only authority on whether a graph parses, whether asplit's outputs are all
// consumed, and whether sidechaincompress will accept the threshold we computed.
func TestGeneratedGraphsRunUnderRealFFmpeg(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}

	stereo6 := []int{2, 2, 2, 2, 2, 2}
	surround := []int{6, 2, 2, 2, 2, 2}

	anns := []TrackAnnotation{
		{Track: 0, Role: RoleMic, Denoise: true},
		{Track: 1, Role: RoleMusic},
		{Track: 2, Role: RoleGame},
	}

	duck := &Ducking{Trigger: []int{0}, Target: []int{1, 2}}
	loud := &Loudness{TargetLUFS: LUFSStreaming}

	tests := []struct {
		name    string
		p       Profile
		src     Source
		layouts []int
	}{
		{"plain two-track sum", simple(NormAuto, 0, 1), stereoSource(6), stereo6},
		{"loudnorm with the fixed parameters", simple(NormLoudnorm, 0, 1), stereoSource(6), stereo6},
		{"denoise", simple(NormOff, 0, 1), annotate(stereoSource(6), anns...), stereo6},
		{"denoise on a 5.1 downmix", simple(NormOff, 0),
			annotate(Source{Tracks: []Track{{Index: 0, Channels: 6, Layout: "5.1"}}}, anns...), surround},
		{"ducking", withDucking(simple(NormOff, 0, 1, 2), *duck), stereoSource(6), stereo6},
		{"ducking with a single target", withDucking(simple(NormOff, 0, 1), Ducking{Trigger: []int{0}, Target: []int{1}}),
			stereoSource(6), stereo6},
		{"ducking with two triggers", withDucking(simple(NormOff, 0, 1, 2), Ducking{Trigger: []int{0, 1}, Target: []int{2}}),
			stereoSource(6), stereo6},
		{"ducking off a trigger this destination does not carry",
			withDucking(simple(NormOff, 1), Ducking{Trigger: []int{0}, Target: []int{1}}), stereoSource(6), stereo6},
		{"ducking at the extreme threshold",
			withDucking(simple(NormOff, 0, 1), Ducking{Trigger: []int{0}, Target: []int{1}, ThresholdDB: MinDuckThresholdDB, AttackMS: MinDuckAttackMS}),
			stereoSource(6), stereo6},
		{"loudness target", withLoudness(simple(NormAuto, 0, 1), *loud), stereoSource(6), stereo6},
		{"loudness at the range extremes",
			withLoudness(simple(NormLoudnorm, 0), Loudness{TargetLUFS: MinTargetLUFS, TruePeakDB: MinTruePeakDB, RangeLU: MaxLoudnessLRA}),
			stereoSource(6), stereo6},
		{"delay", withDelay(simple(NormOff, 0), 250), stereoSource(6), stereo6},
		{"role exclusion", withExcludes(simple(NormOff, 0, 1, 2), RoleMusic),
			annotate(stereoSource(6), anns...), stereo6},
		{"every stage at once", everything(duck, loud), annotate(stereoSource(6), anns...), stereo6},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Compile(tc.p, tc.src)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			in := lavfiSource(t, bin, tc.layouts)
			cmd := exec.Command(bin, "-nostdin", "-v", "error", "-i", in,
				"-filter_complex", res.FilterComplex,
				"-map", "["+res.OutLabel+"]", "-f", "null", "-")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("ffmpeg rejected the graph: %v\ngraph: %s\noutput: %s", err, res.FilterComplex, out)
			}
			if len(out) > 0 {
				t.Errorf("ffmpeg complained about the graph: %s\ngraph: %s", out, res.FilterComplex)
			}
		})
	}
}

func everything(d *Ducking, l *Loudness) Profile {
	p := simple(NormAuto, 0, 1, 2, 3)
	p.ExcludeRoles = []TrackRole{RoleCommentary}
	dd, ll := *d, *l
	p.Ducking = &dd
	p.Loudness = &ll
	p.DelayMS = 200
	return p
}

// lavfiSource renders a short multi-track file matching the given per-track
// channel counts, so that [0:a:N] in a generated graph means at runtime exactly
// what the compiler meant by it.
func lavfiSource(t *testing.T, bin string, channels []int) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "src.nut")
	args := []string{"-nostdin", "-v", "error", "-y"}
	for i, ch := range channels {
		layout := map[int]string{1: "mono", 2: "stereo", 6: "5.1", 8: "7.1"}[ch]
		if layout == "" {
			t.Fatalf("no lavfi layout for %d channels", ch)
		}
		// A different tone per track, so a graph that mixed up its inputs would
		// still be running audible nonsense rather than silence.
		freq := strconv.Itoa(200 + 110*i)
		args = append(args, "-f", "lavfi", "-i",
			"sine=frequency="+freq+":duration=0.4,aformat=channel_layouts="+layout)
	}
	for i := range channels {
		args = append(args, "-map", strconv.Itoa(i)+":a")
	}
	args = append(args, "-c:a", "pcm_s16le", "-f", "nut", path)

	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("building the test source failed: %v\n%s", err, out)
	}
	return path
}

func hasWarning(warns []string, substr string) bool {
	for _, w := range warns {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func withLoudness(p Profile, l Loudness) Profile {
	p.Loudness = &l
	return p
}

func withDucking(p Profile, d Ducking) Profile {
	p.Ducking = &d
	return p
}

func withDelay(p Profile, ms int) Profile {
	p.DelayMS = ms
	return p
}

func withExcludes(p Profile, roles ...TrackRole) Profile {
	p.ExcludeRoles = roles
	return p
}
