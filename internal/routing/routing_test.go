package routing

import (
	"math"
	"strings"
	"testing"
)

func stereoSource(n int) Source {
	s := Source{}
	for i := 0; i < n; i++ {
		s.Tracks = append(s.Tracks, Track{Index: i, Channels: 2, Codec: "aac", Layout: "stereo"})
	}
	return s
}

func simple(norm NormMode, sel ...int) Profile {
	p := Profile{Mode: ModeSimple, Normalize: norm, SampleRate: 48000}
	on := map[int]bool{}
	for _, s := range sel {
		on[s] = true
	}
	for i := 0; i < MaxTracks; i++ {
		p.Tracks = append(p.Tracks, TrackSel{Track: i, Enabled: on[i], Gain: 1.0})
	}
	return p
}

// ---------------------------------------------------------------- downmix

func TestDownmixMatrix(t *testing.T) {
	const eps = 1e-4

	tests := []struct {
		name     string
		channels int
		wantL    map[int]float64
		wantR    map[int]float64
	}{
		{
			name: "mono is centred at unity, not halved",
			// A mono mic must not lose 6 dB just for being mono.
			channels: 1,
			wantL:    map[int]float64{0: 1},
			wantR:    map[int]float64{0: 1},
		},
		{
			name:     "stereo passes through untouched",
			channels: 2,
			wantL:    map[int]float64{0: 1},
			wantR:    map[int]float64{1: 1},
		},
		{
			name:     "3.0 folds centre into both legs, normalized",
			channels: 3,
			// 1 / (1+0.707) = 0.5858 ; 0.707 / 1.707 = 0.4142
			wantL: map[int]float64{0: 0.5858, 2: 0.4142},
			wantR: map[int]float64{1: 0.5858, 2: 0.4142},
		},
		{
			name:     "quad folds rears into their own side",
			channels: 4,
			wantL:    map[int]float64{0: 0.5858, 2: 0.4142},
			wantR:    map[int]float64{1: 0.5858, 3: 0.4142},
		},
		{
			name:     "5.1 uses normalized ITU coefficients and drops LFE",
			channels: 6,
			// FL FR FC LFE BL BR ; 1/2.414=0.4143, 0.707/2.414=0.2929
			wantL: map[int]float64{0: 0.4143, 2: 0.2929, 3: 0, 4: 0.2929},
			wantR: map[int]float64{1: 0.4143, 2: 0.2929, 3: 0, 5: 0.2929},
		},
		{
			name:     "7.1 drops LFE and folds both surround pairs",
			channels: 8,
			// 1/(1+3*0.707)=0.3204 ; 0.707/3.121=0.2265
			wantL: map[int]float64{0: 0.3204, 2: 0.2265, 3: 0, 4: 0.2265, 6: 0.2265},
			wantR: map[int]float64{1: 0.3204, 2: 0.2265, 3: 0, 5: 0.2265, 7: 0.2265},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := DownmixMatrix(tc.channels)
			if len(m[OutL]) != tc.channels || len(m[OutR]) != tc.channels {
				t.Fatalf("matrix width = %d/%d, want %d", len(m[OutL]), len(m[OutR]), tc.channels)
			}
			for ch, want := range tc.wantL {
				if math.Abs(m[OutL][ch]-want) > eps {
					t.Errorf("L[%d] = %.4f, want %.4f", ch, m[OutL][ch], want)
				}
			}
			for ch, want := range tc.wantR {
				if math.Abs(m[OutR][ch]-want) > eps {
					t.Errorf("R[%d] = %.4f, want %.4f", ch, m[OutR][ch], want)
				}
			}
		})
	}
}

// The whole point of normalizing: a fully correlated surround source must not
// exceed full scale after the fold-down.
func TestDownmixCannotClipCorrelatedSource(t *testing.T) {
	for _, ch := range []int{3, 4, 5, 6, 7, 8} {
		m := DownmixMatrix(ch)
		for out := 0; out < OutChannels; out++ {
			var sum float64
			for _, v := range m[out] {
				sum += v
			}
			if sum > 1.0001 {
				t.Errorf("%d channels: output %d coefficients sum to %.4f, which clips a correlated source", ch, out, sum)
			}
		}
	}
}

// ---------------------------------------------------------------- pan strings

func TestPanFilter(t *testing.T) {
	tests := []struct {
		name  string
		cells []Cell
		want  string
	}{
		{
			name:  "stereo passthrough",
			cells: CellsForTrack(0, 2, 1.0),
			want:  "pan=stereo|c0=1*c0|c1=1*c1",
		},
		{
			name:  "stereo at 50% gain",
			cells: CellsForTrack(0, 2, 0.5),
			want:  "pan=stereo|c0=0.5*c0|c1=0.5*c1",
		},
		{
			name:  "mono to both legs",
			cells: CellsForTrack(3, 1, 1.0),
			want:  "pan=stereo|c0=1*c0|c1=1*c0",
		},
		{
			name:  "5.1 downmix",
			cells: CellsForTrack(0, 6, 1.0),
			want:  "pan=stereo|c0=0.4143*c0+0.2929*c2+0.2929*c4|c1=0.4143*c1+0.2929*c2+0.2929*c5",
		},
		{
			name:  "5.1 downmix at 120% gain scales every coefficient",
			cells: CellsForTrack(0, 6, 1.2),
			want:  "pan=stereo|c0=0.4971*c0+0.3514*c2+0.3514*c4|c1=0.4971*c1+0.3514*c2+0.3514*c5",
		},
		{
			name: "mono panned hard left leaves an explicitly silent right leg",
			cells: []Cell{
				{Track: 2, Channel: 0, Out: OutL, Gain: 1.0},
			},
			want: "pan=stereo|c0=1*c0|c1=0*c0",
		},
		{
			name: "channel swap",
			cells: []Cell{
				{Track: 0, Channel: 1, Out: OutL, Gain: 1.0},
				{Track: 0, Channel: 0, Out: OutR, Gain: 1.0},
			},
			want: "pan=stereo|c0=1*c1|c1=1*c0",
		},
		{
			name: "zero-gain cells are omitted entirely",
			cells: []Cell{
				{Track: 0, Channel: 0, Out: OutL, Gain: 1.0},
				{Track: 0, Channel: 1, Out: OutL, Gain: 0},
				{Track: 0, Channel: 1, Out: OutR, Gain: 1.0},
			},
			want: "pan=stereo|c0=1*c0|c1=1*c1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := PanFilter(tc.cells); got != tc.want {
				t.Errorf("PanFilter()\n got  %s\n want %s", got, tc.want)
			}
		})
	}
}

// Taking only the rear channels of a 5.1 track is the headline capability of
// matrix mode, so it gets its own case.
func TestPanFilterRearsOnly(t *testing.T) {
	got := PanFilter([]Cell{
		{Track: 0, Channel: 4, Out: OutL, Gain: 1.0},
		{Track: 0, Channel: 5, Out: OutR, Gain: 1.0},
	})
	want := "pan=stereo|c0=1*c4|c1=1*c5"
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// ---------------------------------------------------------------- compile

func TestCompileSimpleSingleTrack(t *testing.T) {
	res, err := Compile(simple(NormAuto, 0), stereoSource(3))
	if err != nil {
		t.Fatal(err)
	}
	want := "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
		"[a_t0]aresample=48000:async=1:first_pts=0[aout]"
	if res.FilterComplex != want {
		t.Errorf("\n got  %s\n want %s", res.FilterComplex, want)
	}
	// A single track cannot sum-clip, so auto must not insert a limiter.
	if res.Normalization != NormOff {
		t.Errorf("normalization = %q, want %q", res.Normalization, NormOff)
	}
	if res.Summary != "Track 1 → stereo" {
		t.Errorf("summary = %q", res.Summary)
	}
}

func TestCompileSimpleTwoTracksAutoLimits(t *testing.T) {
	res, err := Compile(simple(NormAuto, 0, 1), stereoSource(3))
	if err != nil {
		t.Fatal(err)
	}
	want := "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
		"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
		"[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
		"[a_mix]alimiter=limit=0.95:level=disabled[a_norm];" +
		"[a_norm]aresample=48000:async=1:first_pts=0[aout]"
	if res.FilterComplex != want {
		t.Errorf("\n got  %s\n want %s", res.FilterComplex, want)
	}
	if res.Normalization != NormLimiter {
		t.Errorf("normalization = %q, want limiter", res.Normalization)
	}
}

// amix's normalize=1 default divides by the input count. If this regresses, a
// 3-track mix quietly loses ~9.5 dB and every user reports "it sounds thin".
func TestCompileNeverLetsAmixSelfNormalize(t *testing.T) {
	res, err := Compile(simple(NormAuto, 0, 1, 2), stereoSource(6))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.FilterComplex, "amix=inputs=3:duration=longest:normalize=0") {
		t.Fatalf("amix must be explicitly normalize=0, got: %s", res.FilterComplex)
	}
}

func TestCompileTracks124(t *testing.T) {
	res, err := Compile(simple(NormAuto, 0, 1, 3), stereoSource(6))
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "Tracks 1, 2, 4 → stereo" {
		t.Errorf("summary = %q, want %q", res.Summary, "Tracks 1, 2, 4 → stereo")
	}
	for _, want := range []string{"[0:a:0]", "[0:a:1]", "[0:a:3]"} {
		if !strings.Contains(res.FilterComplex, want) {
			t.Errorf("missing input %s in %s", want, res.FilterComplex)
		}
	}
	if strings.Contains(res.FilterComplex, "[0:a:2]") {
		t.Errorf("track 3 must not appear: %s", res.FilterComplex)
	}
}

func TestCompileGainIsAppliedPerTrack(t *testing.T) {
	p := simple(NormOff, 0, 1)
	p.Tracks[1].Gain = 0.25
	res, err := Compile(p, stereoSource(2))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.FilterComplex, "[0:a:1]pan=stereo|c0=0.25*c0|c1=0.25*c1[a_t1]") {
		t.Errorf("track 2 gain not applied: %s", res.FilterComplex)
	}
}

func TestCompile51SourceDownmixesBeforeSumming(t *testing.T) {
	src := Source{Tracks: []Track{
		{Index: 0, Channels: 6, Layout: "5.1"},
		{Index: 1, Channels: 2, Layout: "stereo"},
	}}
	res, err := Compile(simple(NormAuto, 0, 1), src)
	if err != nil {
		t.Fatal(err)
	}
	want := "[0:a:0]pan=stereo|c0=0.4143*c0+0.2929*c2+0.2929*c4|c1=0.4143*c1+0.2929*c2+0.2929*c5[a_t0];" +
		"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
		"[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
		"[a_mix]alimiter=limit=0.95:level=disabled[a_norm];" +
		"[a_norm]aresample=48000:async=1:first_pts=0[aout]"
	if res.FilterComplex != want {
		t.Errorf("\n got  %s\n want %s", res.FilterComplex, want)
	}
}

func TestCompileMatrixMode(t *testing.T) {
	// Take only the rears of a 5.1 track, and pan a mono mic hard left.
	src := Source{Tracks: []Track{
		{Index: 0, Channels: 6, Layout: "5.1"},
		{Index: 2, Channels: 1, Layout: "mono"},
	}}
	p := Profile{
		Mode:       ModeMatrix,
		Normalize:  NormLoudnorm,
		SampleRate: 48000,
		Matrix: []Cell{
			{Track: 0, Channel: 4, Out: OutL, Gain: 1.0},
			{Track: 0, Channel: 5, Out: OutR, Gain: 1.0},
			{Track: 2, Channel: 0, Out: OutL, Gain: 0.8},
		},
	}
	res, err := Compile(p, src)
	if err != nil {
		t.Fatal(err)
	}
	want := "[0:a:0]pan=stereo|c0=1*c4|c1=1*c5[a_t0];" +
		"[0:a:2]pan=stereo|c0=0.8*c0|c1=0*c0[a_t2];" +
		"[a_t0][a_t2]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
		"[a_mix]loudnorm=I=-16:TP=-1.5:LRA=11[a_norm];" +
		"[a_norm]aresample=48000:async=1:first_pts=0[aout]"
	if res.FilterComplex != want {
		t.Errorf("\n got  %s\n want %s", res.FilterComplex, want)
	}
}

func TestCompileExplicitNormOffOnMultiTrack(t *testing.T) {
	res, err := Compile(simple(NormOff, 0, 1), stereoSource(2))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.FilterComplex, "alimiter") || strings.Contains(res.FilterComplex, "loudnorm") {
		t.Errorf("explicit off must not insert normalization: %s", res.FilterComplex)
	}
}

func TestCompileSampleRate44100(t *testing.T) {
	p := simple(NormOff, 0)
	p.SampleRate = 44100
	res, err := Compile(p, stereoSource(1))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.FilterComplex, "aresample=44100:") {
		t.Errorf("sample rate not honoured: %s", res.FilterComplex)
	}
}

// A profile that outlives a stream layout change must degrade, not explode.
func TestCompileMissingTrackWarnsAndContinues(t *testing.T) {
	res, err := Compile(simple(NormAuto, 0, 4), stereoSource(2))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "track 5") {
		t.Fatalf("warnings = %v, want one mentioning track 5", res.Warnings)
	}
	if len(res.Tracks) != 1 || res.Tracks[0] != 0 {
		t.Errorf("tracks = %v, want [0]", res.Tracks)
	}
}

func TestCompileAllTracksMissingIsAnError(t *testing.T) {
	_, err := Compile(simple(NormAuto, 4, 5), stereoSource(2))
	if err != ErrNoAudio {
		t.Fatalf("err = %v, want ErrNoAudio", err)
	}
}

func TestCompileMatrixChannelBeyondTrackWidthWarns(t *testing.T) {
	src := Source{Tracks: []Track{{Index: 0, Channels: 2}}}
	p := Profile{Mode: ModeMatrix, Normalize: NormOff, SampleRate: 48000, Matrix: []Cell{
		{Track: 0, Channel: 0, Out: OutL, Gain: 1},
		{Track: 0, Channel: 1, Out: OutR, Gain: 1},
		{Track: 0, Channel: 5, Out: OutR, Gain: 1}, // track is stereo
	}}
	res, err := Compile(p, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "channel 6") {
		t.Fatalf("warnings = %v", res.Warnings)
	}
	if strings.Contains(res.FilterComplex, "c5") {
		t.Errorf("dropped channel leaked into filter: %s", res.FilterComplex)
	}
}

// ---------------------------------------------------------------- validation

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		profile Profile
		wantErr string // substring; "" means valid
	}{
		{
			name:    "default profile is valid",
			profile: DefaultProfile(),
		},
		{
			name:    "valid matrix profile",
			profile: Profile{Mode: ModeMatrix, Normalize: NormAuto, SampleRate: 48000, Matrix: []Cell{{Track: 0, Channel: 0, Out: OutL, Gain: 1}}},
		},
		{
			name:    "unknown mode",
			profile: Profile{Mode: "chaos", Normalize: NormOff, SampleRate: 48000, Tracks: []TrackSel{{Track: 0, Enabled: true, Gain: 1}}},
			wantErr: `unknown mode "chaos"`,
		},
		{
			name:    "unknown normalize mode",
			profile: Profile{Mode: ModeSimple, Normalize: "squash", SampleRate: 48000, Tracks: []TrackSel{{Track: 0, Enabled: true, Gain: 1}}},
			wantErr: `unknown normalize mode "squash"`,
		},
		{
			name:    "unsupported sample rate",
			profile: Profile{Mode: ModeSimple, Normalize: NormOff, SampleRate: 96000, Tracks: []TrackSel{{Track: 0, Enabled: true, Gain: 1}}},
			wantErr: "unsupported sample rate 96000",
		},
		{
			name:    "track index above the six-track ceiling",
			profile: Profile{Mode: ModeSimple, Normalize: NormOff, SampleRate: 48000, Tracks: []TrackSel{{Track: 9, Enabled: true, Gain: 1}}},
			wantErr: "track 9 out of range",
		},
		{
			name:    "negative track index",
			profile: Profile{Mode: ModeSimple, Normalize: NormOff, SampleRate: 48000, Tracks: []TrackSel{{Track: -1, Enabled: true, Gain: 1}}},
			wantErr: "track -1 out of range",
		},
		{
			name: "duplicate track rows",
			profile: Profile{Mode: ModeSimple, Normalize: NormOff, SampleRate: 48000, Tracks: []TrackSel{
				{Track: 0, Enabled: true, Gain: 1}, {Track: 0, Enabled: true, Gain: 1},
			}},
			wantErr: "duplicate entry for track 0",
		},
		{
			name:    "gain above the ceiling",
			profile: Profile{Mode: ModeSimple, Normalize: NormOff, SampleRate: 48000, Tracks: []TrackSel{{Track: 0, Enabled: true, Gain: 5}}},
			wantErr: "gain 5.000 out of range",
		},
		{
			name:    "negative gain",
			profile: Profile{Mode: ModeSimple, Normalize: NormOff, SampleRate: 48000, Tracks: []TrackSel{{Track: 0, Enabled: true, Gain: -1}}},
			wantErr: "gain -1.000 out of range",
		},
		{
			name:    "nothing enabled",
			profile: Profile{Mode: ModeSimple, Normalize: NormOff, SampleRate: 48000, Tracks: []TrackSel{{Track: 0, Enabled: false, Gain: 1}}},
			wantErr: "no track is enabled",
		},
		{
			name:    "enabled but zero gain is the same as nothing enabled",
			profile: Profile{Mode: ModeSimple, Normalize: NormOff, SampleRate: 48000, Tracks: []TrackSel{{Track: 0, Enabled: true, Gain: 0}}},
			wantErr: "no track is enabled",
		},
		{
			name:    "matrix cell with an out-of-range output channel",
			profile: Profile{Mode: ModeMatrix, Normalize: NormOff, SampleRate: 48000, Matrix: []Cell{{Track: 0, Channel: 0, Out: 7, Gain: 1}}},
			wantErr: "has output 7",
		},
		{
			name:    "matrix cell with an out-of-range source channel",
			profile: Profile{Mode: ModeMatrix, Normalize: NormOff, SampleRate: 48000, Matrix: []Cell{{Track: 0, Channel: 99, Out: OutL, Gain: 1}}},
			wantErr: "channel 99 out of range",
		},
		{
			name: "duplicate matrix cell",
			profile: Profile{Mode: ModeMatrix, Normalize: NormOff, SampleRate: 48000, Matrix: []Cell{
				{Track: 0, Channel: 0, Out: OutL, Gain: 1}, {Track: 0, Channel: 0, Out: OutL, Gain: 0.5},
			}},
			wantErr: "duplicate matrix cell",
		},
		{
			name:    "empty matrix",
			profile: Profile{Mode: ModeMatrix, Normalize: NormOff, SampleRate: 48000},
			wantErr: "no cell with non-zero gain",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.profile.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	p := Profile{Mode: ModeSimple, Normalize: "nope", SampleRate: 1, Tracks: []TrackSel{{Track: 99, Enabled: true, Gain: 9}}}
	err := p.Validate()
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("want *ValidationError, got %T", err)
	}
	if len(ve.Problems) < 4 {
		t.Errorf("want every problem reported at once, got %d: %v", len(ve.Problems), ve.Problems)
	}
}

func TestCompileRejectsInvalidProfile(t *testing.T) {
	if _, err := Compile(Profile{Mode: "bogus"}, stereoSource(2)); err == nil {
		t.Fatal("want error")
	}
}

func TestApplyDefaults(t *testing.T) {
	p := Profile{Tracks: []TrackSel{{Track: 2, Enabled: true, Gain: 1}}}
	p.ApplyDefaults()
	if p.Mode != ModeSimple || p.Normalize != NormAuto || p.SampleRate != 48000 {
		t.Fatalf("defaults not applied: %+v", p)
	}
	if len(p.Tracks) != MaxTracks {
		t.Fatalf("want %d rows, got %d", MaxTracks, len(p.Tracks))
	}
	for i, tr := range p.Tracks {
		if tr.Track != i {
			t.Fatalf("rows not sorted/filled: index %d has track %d", i, tr.Track)
		}
		if !tr.Enabled && tr.Gain != 1.0 {
			t.Errorf("track %d: unset rows should default to unity gain, got %v", i, tr.Gain)
		}
	}
	if err := p.Validate(); err != nil {
		t.Errorf("defaulted profile should be valid: %v", err)
	}
}

// ---------------------------------------------------------------- presets

func TestPresets(t *testing.T) {
	src := stereoSource(4)
	opts := DefaultPresetOpts()

	t.Run("everything selects every present track", func(t *testing.T) {
		p, err := ApplyPreset(PresetEverything, src, opts)
		if err != nil {
			t.Fatal(err)
		}
		got := p.SelectedTracks()
		if len(got) != 4 {
			t.Fatalf("selected = %v, want all 4", got)
		}
	})

	t.Run("no-music excludes exactly the music track", func(t *testing.T) {
		p, err := ApplyPreset(PresetNoMusic, src, PresetOpts{MusicTrack: 0})
		if err != nil {
			t.Fatal(err)
		}
		for _, tr := range p.SelectedTracks() {
			if tr == 0 {
				t.Fatalf("music track leaked into the mix: %v", p.SelectedTracks())
			}
		}
		if len(p.SelectedTracks()) != 3 {
			t.Errorf("selected = %v, want 3 tracks", p.SelectedTracks())
		}
	})

	t.Run("no-music errors rather than producing a silent stream", func(t *testing.T) {
		if _, err := ApplyPreset(PresetNoMusic, stereoSource(1), PresetOpts{MusicTrack: 0}); err == nil {
			t.Fatal("want error when the only track is the excluded one")
		}
	})

	t.Run("mic-only selects one track", func(t *testing.T) {
		p, err := ApplyPreset(PresetMicOnly, src, PresetOpts{MicTrack: 2})
		if err != nil {
			t.Fatal(err)
		}
		got := p.SelectedTracks()
		if len(got) != 1 || got[0] != 2 {
			t.Fatalf("selected = %v, want [2]", got)
		}
	})

	t.Run("surround preset emits an editable 5.1 matrix", func(t *testing.T) {
		s := Source{Tracks: []Track{{Index: 0, Channels: 6, Layout: "5.1"}}}
		p, err := ApplyPreset(PresetSurround, s, PresetOpts{SurroundTrack: 0})
		if err != nil {
			t.Fatal(err)
		}
		if p.Mode != ModeMatrix {
			t.Fatalf("mode = %q, want matrix", p.Mode)
		}
		res, err := Compile(p, s)
		if err != nil {
			t.Fatal(err)
		}
		want := "pan=stereo|c0=0.4143*c0+0.2929*c2+0.2929*c4|c1=0.4143*c1+0.2929*c2+0.2929*c5"
		if !strings.Contains(res.FilterComplex, want) {
			t.Errorf("\n got  %s\n want it to contain %s", res.FilterComplex, want)
		}
	})

	t.Run("every preset compiles", func(t *testing.T) {
		for _, pre := range Presets() {
			p, err := ApplyPreset(pre.ID, src, opts)
			if err != nil {
				t.Fatalf("%s: %v", pre.ID, err)
			}
			if err := p.Validate(); err != nil {
				t.Fatalf("%s: %v", pre.ID, err)
			}
			if _, err := Compile(p, src); err != nil {
				t.Fatalf("%s: %v", pre.ID, err)
			}
		}
	})

	t.Run("unknown preset", func(t *testing.T) {
		if _, err := ApplyPreset("nope", src, opts); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestFmtCoeff(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{1, "1"}, {0.5, "0.5"}, {0, "0"}, {1.25, "1.25"},
		{0.70710678, "0.7071"}, {0.29287, "0.2929"}, {2, "2"},
	}
	for _, tc := range tests {
		if got := fmtCoeff(tc.in); got != tc.want {
			t.Errorf("fmtCoeff(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A create-destination request carries no routing profile: the user names the
// endpoint first and picks the mix afterwards. ApplyDefaults on a zero profile
// produces six rows with nothing enabled, which then fails validation — so
// callers must substitute DefaultProfile. Without this, creating a destination
// from the UI is impossible.
func TestUnsetProfileIsDetectableAndDefaultIsValid(t *testing.T) {
	var zero Profile
	if !zero.IsUnset() {
		t.Fatal("a zero profile must report IsUnset")
	}

	// The trap: defaulting a zero profile in place yields an invalid profile.
	defaulted := zero
	defaulted.ApplyDefaults()
	if err := defaulted.Validate(); err == nil {
		t.Fatal("expected an all-disabled profile to be invalid; if this changes, " +
			"the IsUnset substitution in db.CreateDestination is no longer needed")
	}

	// The fix: DefaultProfile is valid and compiles.
	d := DefaultProfile()
	if d.IsUnset() {
		t.Fatal("DefaultProfile must not look unset")
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("DefaultProfile must be valid: %v", err)
	}
	res, err := Compile(d, stereoSource(2))
	if err != nil {
		t.Fatalf("DefaultProfile must compile: %v", err)
	}
	if res.Summary != "Track 1 → stereo" {
		t.Errorf("summary = %q, want track 1 selected by default", res.Summary)
	}
}
