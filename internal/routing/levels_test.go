package routing

import (
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The four defects in this file are all the same shape as the rest of the
// session's: a protection that is present in name and absent in effect. Each
// test states the level the operator hears, because that is the only unit in
// which any of them is observable.

// ------------------------------------------------------- NormAuto vs one track

// resolveNorm used to reason "only summing creates clipping, so one track needs
// no limiter". But a single track's own cells are summed too — per output
// channel, by pan — and Validate caps only the per-cell gain, never the row.
func TestNormAutoCatchesASingleTrackThatCanClip(t *testing.T) {
	src := Source{Tracks: []Track{{Index: 0, Channels: 6, Codec: "aac", Layout: "5.1"}}}

	// One track. Three cells on the left leg, each at MaxGain: 6x full scale.
	p := Profile{
		Mode: ModeMatrix, Normalize: NormAuto, SampleRate: 48000,
		Matrix: []Cell{
			{Track: 0, Channel: 0, Out: OutL, Gain: MaxGain},
			{Track: 0, Channel: 2, Out: OutL, Gain: MaxGain},
			{Track: 0, Channel: 4, Out: OutL, Gain: MaxGain},
			{Track: 0, Channel: 1, Out: OutR, Gain: 1.0},
		},
	}
	res, err := Compile(p, src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if res.Normalization != NormLimiter {
		t.Errorf("Normalization = %q, want %q: the left leg reaches 6.0x full scale",
			res.Normalization, NormLimiter)
	}
	if !strings.Contains(res.FilterComplex, "alimiter") {
		t.Errorf("no limiter in the graph:\n%s", res.FilterComplex)
	}
}

// Simple mode reaches the same place by a different road: the downmix table
// normalizes to unity, and then the per-track gain multiplies straight through.
func TestNormAutoCatchesASingleBoostedSimpleTrack(t *testing.T) {
	src := Source{Tracks: []Track{{Index: 0, Channels: 2, Codec: "aac", Layout: "stereo"}}}

	p := simple(NormAuto, 0)
	p.Tracks[0].Gain = MaxGain // +6 dB on the only track

	res, err := Compile(p, src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if res.Normalization != NormLimiter {
		t.Errorf("Normalization = %q, want %q: +6 dB with nothing above it",
			res.Normalization, NormLimiter)
	}
}

// The other half of the contract, and the reason the fix is a widening rather
// than a replacement: a single track that cannot clip must still compile to the
// exact string it always did.
func TestNormAutoStillLeavesAnUnboostedSingleTrackAlone(t *testing.T) {
	src := stereoSource(1)
	res, err := Compile(simple(NormAuto, 0), src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if res.Normalization != NormOff {
		t.Errorf("Normalization = %q, want %q", res.Normalization, NormOff)
	}
	if strings.Contains(res.FilterComplex, "alimiter") {
		t.Errorf("limiter added to a mix that peaks at unity:\n%s", res.FilterComplex)
	}
}

// A quiet single track must not be boosted into a limiter either.
func TestNormAutoIgnoresAttenuation(t *testing.T) {
	src := stereoSource(1)
	p := simple(NormAuto, 0)
	p.Tracks[0].Gain = 0.5
	res, err := Compile(p, src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if res.Normalization != NormOff {
		t.Errorf("Normalization = %q, want %q", res.Normalization, NormOff)
	}
}

// ------------------------------------------------------------ LFE, by layout

// DownmixMatrix keyed on channel count alone, so every 3-channel track was
// treated as 3.0 = FL FR FC. A 2.1 track is FL FR LFE, and folding LFE into
// both legs is exactly what the file header promises never happens.
func TestLFEIsDroppedByLayoutNotByChannelCount(t *testing.T) {
	const eps = 1e-4

	tests := []struct {
		layout   string
		channels int
		lfe      int // index that must be silent
	}{
		{"2.1", 3, 2},
		{"3.1", 4, 3},
		{"4.1", 5, 3},
		{"5.1", 6, 3},
		{"5.1(side)", 6, 3},
		{"6.1", 7, 3},
		{"7.1", 8, 3},
		{"7.1(wide-side)", 8, 3},
	}

	for _, tc := range tests {
		t.Run(tc.layout, func(t *testing.T) {
			m := DownmixMatrix(tc.channels, tc.layout)
			for out := 0; out < OutChannels; out++ {
				if got := m[out][tc.lfe]; math.Abs(got) > eps {
					t.Errorf("%s: LFE (channel %d) reaches output %d at %.4f, want 0",
						tc.layout, tc.lfe, out, got)
				}
			}
		})
	}
}

// The converse: a layout with no LFE must not have a channel dropped just
// because something at that index usually is one. hexagonal is 6 channels of
// real audio; the count table treated index 3 as LFE and discarded BL.
func TestLayoutsWithoutLFEKeepEveryChannel(t *testing.T) {
	for _, tc := range []struct {
		layout   string
		channels int
	}{
		{"hexagonal", 6},
		{"octagonal", 8},
		{"6.0", 6},
		{"7.0", 7},
	} {
		t.Run(tc.layout, func(t *testing.T) {
			m := DownmixMatrix(tc.channels, tc.layout)
			for ch := 0; ch < tc.channels; ch++ {
				if m[OutL][ch] == 0 && m[OutR][ch] == 0 {
					t.Errorf("%s: channel %d reaches neither output", tc.layout, ch)
				}
			}
		})
	}
}

// Every layout the compiler will build a matrix for must be safe against a
// fully correlated source, and must not silently discard audio.
func TestNamedLayoutsNeitherClipNorLoseChannels(t *testing.T) {
	for layout, chans := range layoutChannels {
		if layout == "mono" {
			continue // deliberately unnormalized; see DownmixMatrix
		}
		m := DownmixMatrix(len(chans), layout)
		for out := 0; out < OutChannels; out++ {
			var sum float64
			for _, v := range m[out] {
				sum += v
			}
			if sum > 1.0001 {
				t.Errorf("%s: output %d sums to %.4f, which clips a correlated source", layout, out, sum)
			}
		}
		for i, name := range chans {
			if name == chLFE {
				continue
			}
			if m[OutL][i] == 0 && m[OutR][i] == 0 {
				t.Errorf("%s: channel %d (%s) reaches neither output", layout, i, name)
			}
		}
	}
}

// The whole point of the refactor: the layouts that were already right must
// come out byte for byte identical, so no running destination restarts.
func TestLayoutTableReproducesTheOldCountTable(t *testing.T) {
	const eps = 1e-9
	for _, tc := range []struct {
		layout   string
		channels int
	}{
		{"mono", 1}, {"stereo", 2}, {"3.0", 3}, {"quad", 4},
		{"5.0", 5}, {"5.1", 6}, {"6.1", 7}, {"7.1", 8},
	} {
		byLayout := DownmixMatrix(tc.channels, tc.layout)
		byCount := DownmixMatrix(tc.channels, "")
		for out := 0; out < OutChannels; out++ {
			for ch := range byCount[out] {
				if math.Abs(byLayout[out][ch]-byCount[out][ch]) > eps {
					t.Errorf("%s out %d ch %d: layout table %.6f, count table %.6f",
						tc.layout, out, ch, byLayout[out][ch], byCount[out][ch])
				}
			}
		}
	}
}

// ------------------------------------------------- wide layouts, stereo balance

// The unknown-wide fallback split even channels left and odd right, then
// normalized each row by its own sum. An odd channel count puts one more
// channel on the left, so the two rows were scaled by different divisors and
// the image sat permanently off-centre.
func TestWideUnknownLayoutsStayCentred(t *testing.T) {
	const eps = 1e-6
	for ch := 9; ch <= 16; ch++ {
		m := DownmixMatrix(ch, "")
		var sumL, sumR float64
		for _, v := range m[OutL] {
			sumL += v
		}
		for _, v := range m[OutR] {
			sumR += v
		}
		// Equal-energy content on every input channel must arrive centred.
		perL := sumL / math.Ceil(float64(ch)/2)
		perR := sumR / math.Floor(float64(ch)/2)
		if math.Abs(perL-perR) > eps {
			t.Errorf("%d channels: per-channel gain L=%.6f R=%.6f, a %.2f dB imbalance",
				ch, perL, perR, 20*math.Log10(perL/perR))
		}
		if sumL > 1.0001 || sumR > 1.0001 {
			t.Errorf("%d channels: rows sum to %.4f/%.4f, which clips", ch, sumL, sumR)
		}
	}
}

// A wide track routed through simple mode reaches the same fallback, and is
// the path an operator actually takes: MaxChannels never stood in its way.
func TestWideTrackCompilesCentred(t *testing.T) {
	src := Source{Tracks: []Track{{Index: 0, Channels: 9, Codec: "pcm_s16le", Layout: "9 channels"}}}
	res, err := Compile(simple(NormAuto, 0), src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	l, r := legGains(t, res.FilterComplex)
	if math.Abs(l-r) > 1e-6 {
		t.Errorf("9-channel track compiles to L=%.4f R=%.4f per channel (%.2f dB off-centre)\n%s",
			l, r, 20*math.Log10(l/r), res.FilterComplex)
	}
}

// legGains pulls the first coefficient off each leg of the first pan filter.
func legGains(t *testing.T, graph string) (float64, float64) {
	t.Helper()
	i := strings.Index(graph, "pan=stereo|")
	if i < 0 {
		t.Fatalf("no pan filter in graph:\n%s", graph)
	}
	rest := graph[i+len("pan=stereo|"):]
	if j := strings.IndexAny(rest, "[,;"); j >= 0 {
		rest = rest[:j]
	}
	legs := strings.Split(rest, "|")
	if len(legs) != 2 {
		t.Fatalf("expected two legs, got %q", rest)
	}
	return firstCoeff(t, legs[0]), firstCoeff(t, legs[1])
}

func firstCoeff(t *testing.T, leg string) float64 {
	t.Helper()
	eq := strings.Index(leg, "=")
	term := leg[eq+1:]
	if k := strings.Index(term, "+"); k >= 0 {
		term = term[:k]
	}
	star := strings.Index(term, "*")
	f, err := strconv.ParseFloat(term[:star], 64)
	if err != nil {
		t.Fatalf("parsing %q: %v", term, err)
	}
	return f
}

// ------------------------------------------------- a track that lost channels

// A saved matrix outlives the layout it was drawn against. When the ingest
// narrows, the cells that addressed the missing channels are dropped — and the
// survivors carry coefficients that were normalized for the *old* width, so the
// destination goes quiet by however much was dropped. The warning named the
// channels but never the level, which is the part anyone would notice.
func TestNarrowedTrackWarnsAboutTheLevelNotJustTheChannels(t *testing.T) {
	// A 5.1 matrix, exactly as buildSurround would have written it.
	p := Profile{Mode: ModeMatrix, Normalize: NormAuto, SampleRate: 48000}
	p.Matrix = CellsForTrack(0, Track{Index: 0, Channels: 6, Layout: "5.1"}, 1.0)

	// The encoder has since been reconfigured to send plain stereo.
	src := Source{Tracks: []Track{{Index: 0, Channels: 2, Codec: "aac", Layout: "stereo"}}}

	res, err := Compile(p, src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, "dB") {
		t.Errorf("no warning quantifies the level change; got:\n%s", joined)
	}
	if !strings.Contains(joined, "quieter") {
		t.Errorf("no warning says the mix got quieter; got:\n%s", joined)
	}
}

// ------------------------------------------------------- measured, not argued

// The LFE fix is only worth anything if it is audible, so this measures it.
//
// The source is a 2.1 track whose FL and FR are digital silence and whose LFE
// carries a tone. Under the count-keyed table that track was a 3.0, its LFE was
// "the centre channel", and the tone arrived in both legs at -7.7 dB. Correct
// behaviour is an output with nothing in it at all.
func TestLFEDoesNotReachTheOutputUnderRealFFmpeg(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}

	src := Source{Tracks: []Track{{Index: 0, Channels: 3, Codec: "pcm_s16le", Layout: "2.1"}}}
	res, err := Compile(simple(NormOff, 0), src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	path := filepath.Join(t.TempDir(), "lfe.nut")
	build := exec.Command(bin, "-nostdin", "-v", "error", "-y",
		"-f", "lavfi", "-i", "anullsrc=channel_layout=mono:sample_rate=48000:d=0.4",
		"-f", "lavfi", "-i", "anullsrc=channel_layout=mono:sample_rate=48000:d=0.4",
		"-f", "lavfi", "-i", "sine=frequency=60:duration=0.4:sample_rate=48000",
		"-filter_complex", "[0:a][1:a][2:a]join=inputs=3:channel_layout=2.1[a]",
		"-map", "[a]", "-c:a", "pcm_s16le", "-f", "nut", path)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the 2.1 source failed: %v\n%s", err, out)
	}

	// volumedetect goes on as its own chain fed by [aout]; tacking it onto the
	// end of the string would leave it with an unlabeled input pad.
	measure := exec.Command(bin, "-nostdin", "-v", "info", "-i", path,
		"-filter_complex", fmt.Sprintf("%s;[%s]volumedetect[vout]", res.FilterComplex, res.OutLabel),
		"-map", "[vout]", "-f", "null", "-")
	out, err := measure.CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg rejected the graph: %v\ngraph: %s\n%s", err, res.FilterComplex, out)
	}

	peak := maxVolume(t, string(out))
	if peak > -90 {
		t.Errorf("LFE reached the output at %.1f dB; graph:\n%s", peak, res.FilterComplex)
	}
	t.Logf("2.1 with only LFE excited: output peak %.1f dB (graph: %s)", peak, res.FilterComplex)
}

// maxVolume pulls volumedetect's peak out of ffmpeg's stderr.
func maxVolume(t *testing.T, log string) float64 {
	t.Helper()
	const marker = "max_volume: "
	i := strings.Index(log, marker)
	if i < 0 {
		t.Fatalf("volumedetect reported no max_volume:\n%s", log)
	}
	rest := log[i+len(marker):]
	rest = strings.TrimSpace(strings.SplitN(rest, " dB", 2)[0])
	if rest == "-inf" {
		return math.Inf(-1)
	}
	v, err := strconv.ParseFloat(rest, 64)
	if err != nil {
		t.Fatalf("parsing max_volume %q: %v", rest, err)
	}
	return v
}

// Every layout the table names must produce a graph real FFmpeg accepts against
// a real track of that layout. A coefficient row that is right on paper and
// misnames a channel is still a destination that will not start.
func TestEveryNamedLayoutCompilesAndRuns(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}

	layouts := make([]string, 0, len(layoutChannels))
	for l := range layoutChannels {
		layouts = append(layouts, l)
	}
	sort.Strings(layouts)

	for _, layout := range layouts {
		t.Run(layout, func(t *testing.T) {
			n := len(layoutChannels[layout])
			src := Source{Tracks: []Track{{Index: 0, Channels: n, Codec: "pcm_s16le", Layout: layout}}}
			res, err := Compile(simple(NormOff, 0), src)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			path := filepath.Join(t.TempDir(), "src.nut")
			args := []string{"-nostdin", "-v", "error", "-y",
				"-f", "lavfi", "-i", "sine=frequency=200:duration=0.2:sample_rate=48000",
				"-filter_complex", fmt.Sprintf("[0:a]aformat=channel_layouts=%s[a]", layout),
				"-map", "[a]", "-c:a", "pcm_s16le", "-f", "nut", path}
			if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
				t.Skipf("this FFmpeg cannot build a %s source: %s", layout, out)
			}
			cmd := exec.Command(bin, "-nostdin", "-v", "error", "-i", path,
				"-filter_complex", res.FilterComplex,
				"-map", "["+res.OutLabel+"]", "-f", "null", "-")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("ffmpeg rejected the graph: %v\ngraph: %s\n%s", err, res.FilterComplex, out)
			}
			if len(out) > 0 {
				t.Errorf("ffmpeg complained: %s\ngraph: %s", out, res.FilterComplex)
			}
		})
	}
}

// A matrix that loses nothing must gain no warning.
func TestIntactMatrixGetsNoLevelWarning(t *testing.T) {
	p := Profile{Mode: ModeMatrix, Normalize: NormAuto, SampleRate: 48000}
	p.Matrix = CellsForTrack(0, Track{Index: 0, Channels: 6, Layout: "5.1"}, 1.0)
	src := Source{Tracks: []Track{{Index: 0, Channels: 6, Codec: "aac", Layout: "5.1"}}}

	res, err := Compile(p, src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "quieter") {
			t.Errorf("unexpected level warning on an intact matrix: %q", w)
		}
	}
}
