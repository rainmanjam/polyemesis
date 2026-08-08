package routing

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The whole point: a guess that is WRONG must be audible, not silent.
//
// Compiled against the six-stereo placeholder, a real 5.1 track produced
// pan=stereo|c0=c0|c1=c1 -- a valid graph that publishes front left and right
// and discards centre, where dialogue lives, with nothing reporting a fault.
func TestProvisionalDoesNotGuessAChannelMatrix(t *testing.T) {
	res, err := CompileProvisional(simple(NormAuto, 0), DefaultSource())
	if err != nil {
		t.Fatalf("CompileProvisional: %v", err)
	}
	if strings.Contains(res.FilterComplex, "pan=stereo") {
		t.Errorf("a provisional graph still contains a guessed pan matrix:\n%s", res.FilterComplex)
	}
	if !strings.Contains(res.FilterComplex, ProvisionalFilter) {
		t.Errorf("no runtime downmix in the graph:\n%s", res.FilterComplex)
	}
	if !res.Provisional {
		t.Error("Result.Provisional is false on a provisional compile")
	}
	if len(res.Warnings) == 0 {
		t.Error("a provisional graph carries no warning; an operator has no way to " +
			"know the mix is being decided by FFmpeg rather than by their matrix")
	}
}

// A measured compile must be untouched, byte for byte.
func TestMeasuredCompileIsUnchangedByTheProvisionalPath(t *testing.T) {
	src := stereoSource(2)
	a, err := Compile(simple(NormAuto, 0, 1), src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if a.Provisional {
		t.Error("a measured compile is marked provisional")
	}
	if !strings.Contains(a.FilterComplex, "pan=stereo") {
		t.Errorf("a measured compile lost its pan matrix:\n%s", a.FilterComplex)
	}
}

// Per-track gain still applies: it is the operator's decision and does not
// depend on the layout.
func TestProvisionalStillAppliesPerTrackGain(t *testing.T) {
	p := simple(NormAuto, 0)
	p.Tracks[0].Gain = 0.5
	res, err := CompileProvisional(p, DefaultSource())
	if err != nil {
		t.Fatalf("CompileProvisional: %v", err)
	}
	if !strings.Contains(res.FilterComplex, "volume=0.5") {
		t.Errorf("per-track gain was dropped:\n%s", res.FilterComplex)
	}
	// And unity gain adds no filter at all.
	unity, _ := CompileProvisional(simple(NormAuto, 0), DefaultSource())
	if strings.Contains(unity.FilterComplex, "volume=") {
		t.Errorf("unity gain emitted a volume filter:\n%s", unity.FilterComplex)
	}
}

// Matrix mode gets UNITY, not a scalar stand-in for the matrix.
//
// The first version used the largest cell gain, reasoning that overstating is
// safer than understating. Both reviewers rejected it for the same reason, and
// they were right: a matrix of "c0 to L at 0.25" and "c1 to R at 1.5" is not
// "the track at 1.5". Collapsing it boosts the intended left contribution
// six-fold and leaks each channel into the opposite leg -- a different mix, not
// an approximate one. Unity plus a warning that says what is not being applied
// is the honest version.
func TestMatrixModeGetsUnityGainProvisionally(t *testing.T) {
	p := Profile{Mode: ModeMatrix, Normalize: NormOff, SampleRate: 48000, Matrix: []Cell{
		{Track: 0, Channel: 0, Out: OutL, Gain: 0.25},
		{Track: 0, Channel: 1, Out: OutR, Gain: 1.5},
	}}
	if got := trackGain(p, 0); got != 1 {
		t.Errorf("trackGain(track 0) = %v, want 1: no scalar can stand in for a matrix", got)
	}

	res, err := CompileProvisional(p, DefaultSource())
	if err != nil {
		t.Fatalf("CompileProvisional: %v", err)
	}
	if strings.Contains(res.FilterComplex, "volume=") {
		t.Errorf("a matrix profile emitted a stand-in gain:\n%s", res.FilterComplex)
	}
	// And the operator is told exactly what is not happening.
	joined := strings.Join(res.Warnings, "\n")
	for _, want := range []string{"matrix is NOT being applied", "per-channel routing"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the matrix warning does not mention %q; got:\n%s", want, joined)
		}
	}
}

// Simple mode DOES have a per-track gain, and it means the same thing whatever
// the layout, so it carries through.
func TestSimpleModeGainCarriesThroughProvisionally(t *testing.T) {
	p := simple(NormOff, 0)
	p.Tracks[0].Gain = 0.5
	if got := trackGain(p, 0); got != 0.5 {
		t.Errorf("trackGain = %v, want 0.5", got)
	}
	if got := trackGain(p, 99); got != 1 {
		t.Errorf("trackGain of an unmentioned track = %v, want 1", got)
	}
}

// Measured, not argued: a real 5.1 source carrying dialogue ONLY on centre.
//
// The first version of this test built its fixture with a bare `join`, which
// silently placed the tone on FR rather than FC -- so it passed while measuring
// the wrong channel entirely, and its failure message would have been a lie. The
// explicit map is the fix; the numbers below were confirmed by hand first.
//
//	guessed matrix (the placeholder's output)  -91.0 dB   dialogue gone
//	provisional runtime downmix                -28.7 dB   centre preserved
//
// -28.7 is the tone at -18.1 dB times the BS.775 centre coefficient of 0.2929,
// which is -10.7 dB. The fold is doing exactly what downmix.go does by hand.
func TestProvisionalKeepsCentreUnderRealFFmpeg(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}

	// The engine believes it has the placeholder: six stereo tracks.
	res, err := CompileProvisional(simple(NormOff, 0), DefaultSource())
	if err != nil {
		t.Fatalf("CompileProvisional: %v", err)
	}

	// What actually arrives is 5.1 with everything silent except centre.
	path := filepath.Join(t.TempDir(), "c.nut")
	args := []string{"-nostdin", "-v", "error", "-y"}
	for i := 0; i < 6; i++ {
		src := "anullsrc=channel_layout=mono:sample_rate=48000:d=0.4"
		if i == 2 {
			src = "sine=frequency=440:duration=0.4:sample_rate=48000"
		}
		args = append(args, "-f", "lavfi", "-i", src)
	}
	// The map is NOT optional. Without it join assigns by the inputs' own
	// layouts -- all six claim mono/FC -- and the tone lands somewhere else.
	args = append(args,
		"-filter_complex", "[0:a][1:a][2:a][3:a][4:a][5:a]join=inputs=6:channel_layout=5.1:"+
			"map=0.0-FL|1.0-FR|2.0-FC|3.0-LFE|4.0-BL|5.0-BR[a]",
		"-map", "[a]", "-c:a", "pcm_s16le", "-f", "nut", path)
	if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
		t.Fatalf("building the 5.1 source failed: %v\n%s", err, out)
	}

	peakOf := func(graph, out string) float64 {
		t.Helper()
		cmd := exec.Command(bin, "-nostdin", "-v", "info", "-i", path,
			"-filter_complex", graph+";["+out+"]volumedetect[vout]",
			"-map", "[vout]", "-f", "null", "-")
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("ffmpeg rejected %q: %v\n%s", graph, err, b)
		}
		return maxVolume(t, string(b))
	}

	// The fixture really does put the tone on centre and nowhere else.
	if got := peakOf("[0:a:0]pan=mono|c0=1*c2[m]", "m"); got < -30 {
		t.Fatalf("precondition: centre carries %.1f dB, so this fixture is not testing "+
			"what it claims", got)
	}

	// What the placeholder's guessed matrix does with it.
	guessed := peakOf("[0:a:0]pan=stereo|c0=c0|c1=c1[g]", "g")
	if guessed > -60 {
		t.Fatalf("precondition: the guessed matrix passes %.1f dB, so it is not "+
			"discarding centre and there is nothing here to fix", guessed)
	}

	// What the provisional graph does with it.
	got := peakOf(res.FilterComplex, res.OutLabel)
	if got < -40 {
		t.Errorf("centre reached the output at only %.1f dB through the provisional "+
			"graph, against %.1f dB for the guessed matrix. The whole point is that a "+
			"wrong layout guess degrades audibly instead of discarding dialogue:\n%s",
			got, guessed, res.FilterComplex)
	}
	t.Logf("5.1, tone on centre only: guessed matrix %.1f dB, provisional %.1f dB", guessed, got)
}

// The duck's key tap must get the SAME fold as the mix legs.
//
// duckGraph builds a separate tap for a trigger track that is not itself in the
// mix, and it built that tap from the guessed matrix even in provisional mode --
// so on a wide track the detector listened to channels 0 and 1 while the mix
// heard a proper fold. Two different signals, one ducking the other.
func TestProvisionalDuckTapGetsTheSameFoldAsTheMix(t *testing.T) {
	p := simple(NormOff, 1)
	p.Ducking = &Ducking{Trigger: []int{0}, Target: []int{1}}

	res, err := CompileProvisional(p, DefaultSource())
	if err != nil {
		t.Fatalf("CompileProvisional: %v", err)
	}
	if strings.Contains(res.FilterComplex, "pan=stereo") {
		t.Errorf("the duck key tap still uses a guessed pan matrix:\n%s", res.FilterComplex)
	}
	// Both the mix leg and the key tap present.
	if n := strings.Count(res.FilterComplex, ProvisionalFilter); n < 2 {
		t.Errorf("expected a runtime downmix on both the mix leg and the key tap, got %d:\n%s",
			n, res.FilterComplex)
	}
	if !strings.Contains(res.FilterComplex, "sidechaincompress") {
		t.Errorf("ducking was lost in the provisional path:\n%s", res.FilterComplex)
	}
}

// Warnings about a matrix that is not being applied are noise, and they would
// bury the one warning that is true.
func TestProvisionalDropsWarningsAboutTheUnusedMatrix(t *testing.T) {
	// A saved 5.1 matrix meeting the placeholder, which claims two channels.
	p := Profile{Mode: ModeMatrix, Normalize: NormAuto, SampleRate: 48000}
	p.Matrix = CellsForTrack(0, Track{Index: 0, Channels: 6, Layout: "5.1"}, 1.0)

	res, err := CompileProvisional(p, DefaultSource())
	if err != nil {
		t.Fatalf("CompileProvisional: %v", err)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "is ignored") || strings.Contains(w, "quieter") {
			t.Errorf("a warning about the unused matrix survived: %q", w)
		}
	}
	if len(res.Warnings) != 1 {
		t.Errorf("expected exactly the provisional warning, got %d: %v",
			len(res.Warnings), res.Warnings)
	}
}

// Role exclusion is a decision about TRACKS, so it must still apply.
func TestProvisionalStillHonoursRoleExclusion(t *testing.T) {
	src := annotate(DefaultSource(), TrackAnnotation{Track: 1, Role: RoleMusic})
	p := simple(NormOff, 0, 1)
	p.ExcludeRoles = []TrackRole{RoleMusic}

	res, err := CompileProvisional(p, src)
	if err != nil {
		t.Fatalf("CompileProvisional: %v", err)
	}
	if strings.Contains(res.FilterComplex, "[0:a:1]") {
		t.Errorf("an excluded track reached a provisional graph -- this is the DMCA "+
			"switch, and it must not stop working because a probe failed:\n%s",
			res.FilterComplex)
	}
}
