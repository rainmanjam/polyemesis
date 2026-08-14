package routing

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// pairSource is two stereo ingest tracks, which is the smallest source that can
// tell two mixes apart: a mix carrying both is distinguishable from a mix
// carrying one.
func pairSource() Source {
	return Source{Tracks: []Track{
		{Index: 0, Channels: 2, Layout: "stereo", Codec: "aac"},
		{Index: 1, Channels: 2, Layout: "stereo", Codec: "aac"},
	}}
}

func pairProfile(tracks ...int) Profile {
	p := DefaultProfile()
	p.Tracks = nil
	for _, t := range tracks {
		p.Tracks = append(p.Tracks, TrackSel{Track: t, Enabled: true, Gain: 1})
	}
	return p
}

// TestTheEmptyNamespaceIsByteIdenticalToTheSingleMixGraph is the compatibility
// half of the label namespacing: a destination that gains a VOD track must not
// have its LIVE mix rewritten, and every destination that never asks for one
// must produce the exact command it produced before namespacing existed.
//
// It compares the primary half of a paired graph against a solo Compile of the
// same profile, byte for byte, across profiles that exercise every label site
// there is -- the per-track pans, the amix, the loudness stage, the delay, the
// resample, and all six ducking labels. A namespace accidentally applied to the
// primary, or a label site missed and left as a bare constant while its
// neighbours moved, both show up here as a diff.
//
// MUTATION: `ns{}` -> `ns{prefix: "x_"}` in Compile (filtergraph.go). Observed:
// FAIL, "solo and paired primary differ" on every subcase.
// MUTATION: `n.of("a_mix")` -> `"a_mix"` in compile. Observed: FAIL on the
// ducking and multi-track subcases (a_mix defined in the vod_ namespace but
// referenced bare). Restored from /tmp backup; `git diff --stat` clean.
func TestTheEmptyNamespaceIsByteIdenticalToTheSingleMixGraph(t *testing.T) {
	src := pairSource()

	ducked := pairProfile(0, 1)
	ducked.Ducking = &Ducking{Target: []int{0}, Trigger: []int{1}, ThresholdDB: -24, Ratio: 8, AttackMS: 20, ReleaseMS: 300}

	loud := pairProfile(0, 1)
	loud.Loudness = &Loudness{TargetLUFS: -16, TruePeakDB: -1.5, RangeLU: 11}

	delayed := pairProfile(0, 1)
	delayed.DelayMS = 250

	for _, tc := range []struct {
		name string
		prof Profile
	}{
		{"one track", pairProfile(0)},
		{"two tracks", pairProfile(0, 1)},
		{"ducking", ducked},
		{"loudness", loud},
		{"delay", delayed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			solo, err := Compile(tc.prof, src)
			if err != nil {
				t.Fatalf("solo compile: %v", err)
			}
			vod := pairProfile(0)
			paired, err := CompilePair(tc.prof, &vod, src)
			if err != nil {
				t.Fatalf("paired compile: %v", err)
			}
			// The secondary half is appended after the primary, so the primary
			// half is a literal prefix. Checking the prefix rather than cutting
			// on a marker keeps the assertion on the actual claim -- "the live
			// graph is unchanged" -- and does not depend on what the first
			// secondary chain happens to start with (it starts with an input
			// tap, [0:a:N], not with the namespace).
			if !strings.HasPrefix(paired.FilterComplex, solo.FilterComplex+";") {
				t.Errorf("the paired graph does not start with the solo graph\n solo: %s\npaired: %s", solo.FilterComplex, paired.FilterComplex)
			}
			if !strings.Contains(paired.FilterComplex, SecondaryPrefix) {
				t.Fatalf("paired graph has no secondary half at all: %s", paired.FilterComplex)
			}
			if paired.OutLabel != solo.OutLabel {
				t.Errorf("primary out label moved: solo %q, paired %q", solo.OutLabel, paired.OutLabel)
			}
		})
	}
}

// labelDefs returns every label a filter graph DEFINES, i.e. every [x] that sits
// at the end of a chain rather than at its start. Those are the ones that
// collide: FFmpeg refuses a graph that defines the same label twice, and this
// whole change exists because Compile defined a_t0/a_mix/aout unconditionally.
//
// Input taps ([0:a:0]) are deliberately NOT collected. A shared input tap is
// legal and is measured to be legal -- see TestAPairedGraphReachesFFmpegAsTwo
// DistinctMixes -- so counting it as a collision would fail a working graph.
func labelDefs(graph string) []string {
	var out []string
	re := regexp.MustCompile(`\[([A-Za-z_][A-Za-z0-9_]*)\]`)
	for _, chain := range strings.Split(graph, ";") {
		// Everything after the last filter argument: the trailing [x][y] run.
		idx := strings.LastIndex(chain, "]")
		if idx < 0 {
			continue
		}
		// Walk back over a contiguous run of [..] groups at the end.
		tail := chain
		start := len(tail)
		for start > 0 && tail[start-1] == ']' {
			open := strings.LastIndex(tail[:start], "[")
			if open < 0 {
				break
			}
			start = open
		}
		for _, m := range re.FindAllStringSubmatch(tail[start:], -1) {
			out = append(out, m[1])
		}
	}
	return out
}

// TestAPairedGraphDefinesNoLabelTwice is the structural statement of the bug
// this change fixes: before namespacing, concatenating two compiled graphs
// defined a_t0, a_mix and aout twice each, and FFmpeg refuses such a graph.
//
// This is deliberately NOT the only test of pairing, because on its own it is
// exactly the kind of guard this repo keeps getting burned by: it could pass
// against a graph FFmpeg still refuses for some other reason, and it cannot see
// whether the two mixes actually carry different audio. The measurement that
// closes both holes is TestAPairedGraphReachesFFmpegAsTwoDistinctMixes; this one
// exists to name the specific defect and to fail fast without an FFmpeg binary.
//
// MUTATION: `ns{prefix: SecondaryPrefix}` -> `ns{}` in CompilePair. Observed:
// FAIL, "label a_t0 is defined 2 times" (and a_mix, aout). Restored from /tmp
// backup; `git diff --stat` clean.
func TestAPairedGraphDefinesNoLabelTwice(t *testing.T) {
	src := pairSource()
	live := pairProfile(0, 1)
	live.Ducking = &Ducking{Target: []int{0}, Trigger: []int{1}, ThresholdDB: -24, Ratio: 8, AttackMS: 20, ReleaseMS: 300}
	vod := pairProfile(0, 1)
	vod.Ducking = &Ducking{Target: []int{0}, Trigger: []int{1}, ThresholdDB: -24, Ratio: 8, AttackMS: 20, ReleaseMS: 300}

	paired, err := CompilePair(live, &vod, src)
	if err != nil {
		t.Fatalf("compile pair: %v", err)
	}

	seen := map[string]int{}
	for _, l := range labelDefs(paired.FilterComplex) {
		seen[l]++
	}
	if len(seen) == 0 {
		t.Fatalf("no labels found at all -- labelDefs is not reading this graph: %s", paired.FilterComplex)
	}
	for l, n := range seen {
		if n > 1 {
			t.Errorf("label %s is defined %d times in one graph; FFmpeg refuses that", l, n)
		}
	}
	// The ducking labels are the ones most easily missed, because they are
	// generated in a different function. Assert the namespace actually reached
	// them rather than trusting the count above, which a graph with no ducking
	// would also satisfy.
	// a_t1_mix, not a_t0_mix: the asplit lands on the TRIGGER track (1), which is
	// the one that has to reach both the detector and the mix.
	for _, want := range []string{SecondaryPrefix + "a_duck", SecondaryPrefix + "a_t1_mix", SecondaryPrefix + "a_mix"} {
		if seen[want] == 0 {
			t.Errorf("expected the secondary namespace to claim %q, but it is not defined; labels: %v", want, seen)
		}
	}
	if seen["a_duck"] == 0 {
		t.Errorf("the PRIMARY ducking label a_duck is missing -- the namespace leaked onto the primary; labels: %v", seen)
	}
}

// TestASecondaryThatCannotCompileLeavesTheLiveMixPublishing is the owner's
// standing decision made testable: an optional VOD track must never veto a
// working broadcast.
//
// The secondary here selects track 7, which this ingest does not carry, so it
// compiles to ErrNoAudio. The live mix must still come back intact, with a
// warning naming the consequence, and with no second label for the egress to
// map.
//
// MUTATION: `return p, nil` -> `return Pair{}, serr` in the serr branch of
// CompilePair. Observed: FAIL, "a failed VOD mix took the live mix down with
// it: routing profile selects no audio". Restored from /tmp backup; `git diff
// --stat` clean.
func TestASecondaryThatCannotCompileLeavesTheLiveMixPublishing(t *testing.T) {
	src := pairSource()
	live := pairProfile(0, 1)
	vod := pairProfile(7)

	paired, err := CompilePair(live, &vod, src)
	if err != nil {
		t.Fatalf("a failed VOD mix took the live mix down with it: %v", err)
	}
	solo, err := Compile(live, src)
	if err != nil {
		t.Fatalf("solo compile: %v", err)
	}
	if paired.FilterComplex != solo.FilterComplex {
		t.Errorf("the live graph changed because an optional extra failed\n want: %s\n got: %s", solo.FilterComplex, paired.FilterComplex)
	}
	if paired.SecondOutLabel != "" {
		t.Errorf("SecondOutLabel = %q, want empty -- the egress would map a label the graph never defines", paired.SecondOutLabel)
	}
	if paired.Second != nil {
		t.Errorf("Second = %+v, want nil", paired.Second)
	}
	var found bool
	for _, w := range paired.Warnings {
		if strings.Contains(w, "VOD") && strings.Contains(w, "live mix only") {
			found = true
		}
	}
	if !found {
		t.Errorf("no warning explains why there is no VOD track; warnings: %v", paired.Warnings)
	}
}

// TestANilSecondaryIsExactlyASingleMix keeps the ordinary case honest: nearly
// every destination has no VOD mix, and asking for a pair without one must be
// indistinguishable from Compile -- same graph, no second label, and critically
// no warning, because there is nothing to warn about.
//
// MUTATION: deleted the `if secondary == nil { return p, nil }` early return so
// a nil secondary fell through to compile(*secondary, ...). Observed: panic
// (nil dereference) -> FAIL. Restored from /tmp backup; `git diff --stat` clean.
func TestANilSecondaryIsExactlyASingleMix(t *testing.T) {
	src := pairSource()
	live := pairProfile(0, 1)

	paired, err := CompilePair(live, nil, src)
	if err != nil {
		t.Fatalf("compile pair: %v", err)
	}
	solo, err := Compile(live, src)
	if err != nil {
		t.Fatalf("solo compile: %v", err)
	}
	if paired.FilterComplex != solo.FilterComplex {
		t.Errorf("graph differs\n want: %s\n got: %s", solo.FilterComplex, paired.FilterComplex)
	}
	if paired.SecondOutLabel != "" || paired.Second != nil {
		t.Errorf("a nil secondary produced a second mix: label %q, second %+v", paired.SecondOutLabel, paired.Second)
	}
	if len(paired.Warnings) != len(solo.Warnings) {
		t.Errorf("warnings differ: solo %v, paired %v", solo.Warnings, paired.Warnings)
	}
}

// TestAPrimaryThatCannotCompileIsStillAnError is the other side of the
// never-veto rule, and it is here because "never fail" is the easy overcorrection.
// There is no stream without the live mix; returning a Pair anyway would publish
// silence and report success.
//
// MUTATION: made CompilePair swallow the primary error and return an empty Pair
// with nil error. Observed: FAIL, "a primary that selects nothing compiled
// without error". Restored from /tmp backup; `git diff --stat` clean.
func TestAPrimaryThatCannotCompileIsStillAnError(t *testing.T) {
	src := pairSource()
	vod := pairProfile(0)
	if _, err := CompilePair(pairProfile(7), &vod, src); err == nil {
		t.Fatal("a primary that selects nothing compiled without error")
	}
}

// TestAPairedGraphReachesFFmpegAsTwoDistinctMixes is the measurement the rest of
// this file is scaffolding for, and the only test here that can tell the
// difference between "two labels" and "two tracks of different audio".
//
// It puts a 300 Hz tone on ingest track 0 and 5 kHz on track 1, compiles a LIVE
// mix carrying both and a VOD mix carrying only track 0, hands the real graph to
// the real FFmpeg binary, and reads the tone content back off each output track
// with a bandpass and volumedetect.
//
// WHAT THIS CATCHES THAT A LABEL COUNT CANNOT, and the reason it is written this
// way round: the failure this feature is most likely to ship with is TWO TRACKS
// THAT ARE THE SAME MIX. A track count sees two tracks and passes. The
// per-track tone content is the only assertion that separates them, so the
// 5 kHz reading on the VOD track is the load-bearing line -- it must be ABSENT
// there and PRESENT on the live track.
//
// Both graphs tap [0:a:0]. That is the shared-input case, and it is the reason
// this runs a binary at all: whether FFmpeg splits an input stream implicitly
// was the open question that decided against emitting an asplit. Answered yes on
// 6.0.1 (Alpine 3.18, the floor internal/ffmpeg/detect.go enforces) and 8.1.2.
//
// MUTATION: `p.SecondOutLabel = second.OutLabel` -> `= first.OutLabel`, i.e. map
// the live mix twice, which is the exact "two tracks, one mix" defect. Observed:
// FFmpeg refused the duplicate map outright -> FAIL at the build step. Then
// mutated the VOD profile to select tracks 0 and 1 (a legal graph that is
// nonetheless the wrong mix): FAIL, "VOD track carries 5 kHz at -18.1 dB, want
// it absent -- the two tracks are the same mix, not two mixes". Restored from
// /tmp backup; `git diff --stat` clean.
func TestAPairedGraphReachesFFmpegAsTwoDistinctMixes(t *testing.T) {
	// testenv.FFmpegBinary rather than a local exec.LookPath + t.Skip: its skip
	// lives inside internal/testenv, so it is not a free pass this package has to
	// account for, and it FAILS instead of skipping when POLYEMESIS_REQUIRE_FFMPEG
	// says the environment undertook to provide a binary (#187).
	ffmpegBin := testenv.FFmpegBinary(t, "ffmpeg",
		"no ffmpeg on PATH; this test measures a real filter graph and has nothing to measure without one")

	dir := t.TempDir()
	srcFile := dir + "/src.nut"
	// Track 0 = 300 Hz, track 1 = 5 kHz. Distinct enough that a bandpass at one
	// reads the other as absent by ~55 dB.
	//
	// PCM in NUT, and no video at all. Every codec here is built into any FFmpeg
	// -- lavfi, pcm_s16le, nut -- so there is no "this build cannot mux h264/aac"
	// case to skip on, which is the second free pass this test would otherwise
	// have needed. It also keeps the measurement off a lossy round trip: the
	// absent tone reads -73 dB rather than whatever AAC would have left behind.
	build := exec.Command(ffmpegBin, "-y", "-v", "error",
		"-f", "lavfi", "-i", "sine=frequency=300:duration=2:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=5000:duration=2:sample_rate=48000",
		"-map", "0:a", "-map", "1:a",
		"-c:a", "pcm_s16le", "-f", "nut", srcFile)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the two-track fixture failed (%v); every codec it uses is built into FFmpeg, so this is not an environment problem: %s", err, out)
	}

	src := pairSource()
	live := pairProfile(0, 1) // both tones
	vod := pairProfile(0)     // 300 Hz only

	paired, err := CompilePair(live, &vod, src)
	if err != nil {
		t.Fatalf("compile pair: %v", err)
	}
	if paired.SecondOutLabel == "" {
		t.Fatal("no second mix was compiled, so there is nothing to measure")
	}

	outFile := dir + "/out.nut"
	run := exec.Command(ffmpegBin, "-y", "-v", "error", "-i", srcFile,
		"-filter_complex", paired.FilterComplex,
		"-map", "["+paired.OutLabel+"]",
		"-map", "["+paired.SecondOutLabel+"]",
		"-c:a", "pcm_s16le", "-f", "nut", outFile)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("FFmpeg refused the paired graph: %v\ngraph: %s\n%s", err, paired.FilterComplex, out)
	}

	const (
		liveTrack = 0
		vodTrack  = 1
		// A tone that is present reads around -18 dB; one that is absent reads
		// below -70. Anything under this threshold is absent by any reading.
		presentAbove = -40.0
	)
	measure := func(track int, hz int) float64 {
		t.Helper()
		cmd := exec.Command(ffmpegBin, "-hide_banner", "-nostats", "-i", outFile,
			"-map", "0:a:"+strconv.Itoa(track),
			"-af", "bandpass=f="+strconv.Itoa(hz)+":width_type=h:w=80,volumedetect",
			"-f", "null", "-")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("measuring track %d at %d Hz: %v\n%s", track, hz, err, out)
		}
		m := regexp.MustCompile(`max_volume:\s*(-?[0-9.]+) dB`).FindStringSubmatch(string(out))
		if m == nil {
			t.Fatalf("volumedetect printed no max_volume for track %d at %d Hz; output:\n%s", track, hz, out)
		}
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			t.Fatalf("unparseable max_volume %q: %v", m[1], err)
		}
		return v
	}

	// The live mix carries BOTH tones.
	if v := measure(liveTrack, 300); v < presentAbove {
		t.Errorf("live track is missing its 300 Hz tone: %.1f dB", v)
	}
	if v := measure(liveTrack, 5000); v < presentAbove {
		t.Errorf("live track is missing its 5 kHz tone: %.1f dB", v)
	}
	// The VOD mix carries ONLY 300 Hz. This is the assertion that separates two
	// distinct mixes from the same mix sent twice.
	if v := measure(vodTrack, 300); v < presentAbove {
		t.Errorf("VOD track is missing its 300 Hz tone: %.1f dB", v)
	}
	if v := measure(vodTrack, 5000); v >= presentAbove {
		t.Errorf("VOD track carries 5 kHz at %.1f dB, want it absent -- the two tracks are the same mix, not two mixes", v)
	}
}
