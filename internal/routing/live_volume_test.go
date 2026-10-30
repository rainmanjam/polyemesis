package routing

import (
	"regexp"
	"strings"
	"testing"
)

// volumeRE matches one named volume stage the way FFmpeg's command channel
// addresses it. The graph text is the contract between this package and the
// live path in internal/ffmpeg, so the tests pin the exact spelling.
var volumeRE = regexp.MustCompile(`volume@([A-Za-z0-9_]+)=volume=([0-9.]+)`)

// volumesOf returns instance name -> value for every named volume stage.
func volumesOf(graph string) map[string]string {
	out := map[string]string{}
	for _, m := range volumeRE.FindAllStringSubmatch(graph, -1) {
		out[m[1]] = m[2]
	}
	return out
}

// maskVolumes replaces every volume value, so two graphs compare equal exactly
// when they differ at most in gain.
func maskVolumes(graph string) string {
	return volumeRE.ReplaceAllString(graph, "volume@$1=volume=*")
}

// Every track that reaches the mix carries a named volume stage, including at
// unity. A stage that appears only when gain != 1 would make the first gain
// edit a structural change, which is a restart rather than a live command.
func TestEveryMixedTrackHasANamedVolumeStage(t *testing.T) {
	p := simple(NormOff, 0, 1)
	p.Tracks[0].Gain = 1
	p.Tracks[1].Gain = 0.5
	res, err := Compile(p, stereoSource(2))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	vols := volumesOf(res.FilterComplex)
	if vols["trk0"] != "1" {
		t.Errorf("track 0 at unity: volume stage = %q, want \"1\"; graph:\n%s", vols["trk0"], res.FilterComplex)
	}
	if vols["trk1"] != "0.5" {
		t.Errorf("track 1 at 0.5: volume stage = %q, want \"0.5\"; graph:\n%s", vols["trk1"], res.FilterComplex)
	}
}

// The gain lives in the volume stage and nowhere else. If it were also folded
// into the pan, a live volume change would be applied on top of a level the
// pan already set, and the output would not follow the command.
func TestPanCarriesUnityGainInSimpleMode(t *testing.T) {
	p := simple(NormOff, 0)
	p.Tracks[0].Gain = 0.5
	res, err := Compile(p, stereoSource(2))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if strings.Contains(res.FilterComplex, "0.5*") {
		t.Errorf("the 0.5 gain is folded into the pan as well as the volume stage:\n%s", res.FilterComplex)
	}
	if got := volumesOf(res.FilterComplex)["trk0"]; got != "0.5" {
		t.Errorf("volume stage = %q, want 0.5", got)
	}
}

// Mute is a value, not a removal. The track stays in the graph, so muting it
// does not change the input count, the alignment stage, or the normaliser.
func TestMuteKeepsTheTrackAndSilencesIt(t *testing.T) {
	p := simple(NormOff, 0, 1)
	p.Tracks[1].Muted = true
	res, err := Compile(p, stereoSource(2))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(res.Tracks) != 2 {
		t.Fatalf("muting removed a track from the mix: Tracks = %v", res.Tracks)
	}
	if got := volumesOf(res.FilterComplex)["trk1"]; got != "0" {
		t.Errorf("muted track volume = %q, want 0", got)
	}
	if got := volumesOf(res.FilterComplex)["trk0"]; got != "1" {
		t.Errorf("unmuted track volume = %q, want 1", got)
	}
}

// THE INVARIANT the live path rests on: a mute, an unmute, or a gain edit that
// stays under the normaliser's threshold changes nothing in the graph except
// volume values. If this fails, a live command is no longer enough to get the
// running graph to the stored one, and the engine would have to restart.
func TestMuteAndGainChangeOnlyVolumeValues(t *testing.T) {
	base := simple(NormOff, 0, 1)
	base.Tracks[0].Gain = 0.4
	base.Tracks[1].Gain = 0.5
	a, err := Compile(base, stereoSource(2))
	if err != nil {
		t.Fatalf("Compile base: %v", err)
	}

	edited := simple(NormOff, 0, 1)
	edited.Tracks[0].Gain = 0.9
	edited.Tracks[1].Gain = 0.5
	edited.Tracks[1].Muted = true
	b, err := Compile(edited, stereoSource(2))
	if err != nil {
		t.Fatalf("Compile edited: %v", err)
	}

	if a.FilterComplex == b.FilterComplex {
		t.Fatal("the edit did not change the graph at all; the test proves nothing")
	}
	if maskVolumes(a.FilterComplex) != maskVolumes(b.FilterComplex) {
		t.Errorf("a gain/mute edit changed more than volume values:\nbefore:\n%s\nafter:\n%s",
			a.FilterComplex, b.FilterComplex)
	}
}

// A gain that crosses the normaliser's threshold is NOT live: the limiter text
// changes, the masked graphs differ, and the engine restarts. This is the
// boundary the mutation guard in the engine relies on.
func TestGainCrossingTheNormaliserThresholdIsStructural(t *testing.T) {
	low := simple(NormAuto, 0)
	low.Tracks[0].Gain = 0.9
	high := simple(NormAuto, 0)
	high.Tracks[0].Gain = 1.2

	a, err := Compile(low, DefaultSource())
	if err != nil {
		t.Fatalf("Compile low: %v", err)
	}
	b, err := Compile(high, DefaultSource())
	if err != nil {
		t.Fatalf("Compile high: %v", err)
	}
	if maskVolumes(a.FilterComplex) == maskVolumes(b.FilterComplex) {
		t.Errorf("gain 0.9 and 1.2 produced the same structure; NormAuto did not change its limiter decision:\n%s", b.FilterComplex)
	}
}

// Provisional graphs get the same named stage, so the live path works on them
// too. The gain is no longer baked into the provisional chain.
func TestProvisionalGraphAlsoCarriesVolumeStages(t *testing.T) {
	p := simple(NormAuto, 0)
	p.Tracks[0].Gain = 0.5
	res, err := CompileProvisional(p, DefaultSource())
	if err != nil {
		t.Fatalf("CompileProvisional: %v", err)
	}
	if got := volumesOf(res.FilterComplex)["trk0"]; got != "0.5" {
		t.Errorf("provisional volume stage = %q, want 0.5; graph:\n%s", got, res.FilterComplex)
	}
	if strings.Count(res.FilterComplex, "volume=") != 1 {
		t.Errorf("provisional graph should carry exactly one volume stage:\n%s", res.FilterComplex)
	}
}

// Matrix mode: the per-track mute is live, but its per-cell gains stay in the
// pan and are structural. So a matrix mute must leave the graph unchanged
// except for the volume value.
func TestMatrixMuteIsLiveAndCellGainIsNot(t *testing.T) {
	mk := func(muted bool, cellGain float64) Profile {
		p := Profile{Mode: ModeMatrix, Normalize: NormOff, SampleRate: 48000, Matrix: []Cell{
			{Track: 0, Channel: 0, Out: OutL, Gain: cellGain},
			{Track: 0, Channel: 1, Out: OutR, Gain: 1},
		}, Tracks: []TrackSel{{Track: 0, Enabled: true, Gain: 1, Muted: muted}}}
		return p
	}
	a, err := Compile(mk(false, 1), stereoSource(2))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	b, err := Compile(mk(true, 1), stereoSource(2))
	if err != nil {
		t.Fatalf("Compile muted: %v", err)
	}
	if maskVolumes(a.FilterComplex) != maskVolumes(b.FilterComplex) {
		t.Errorf("matrix mute changed structure:\n%s\nvs\n%s", a.FilterComplex, b.FilterComplex)
	}
	if got := volumesOf(b.FilterComplex)["trk0"]; got != "0" {
		t.Errorf("matrix muted volume = %q, want 0", got)
	}

	c, err := Compile(mk(false, 0.25), stereoSource(2))
	if err != nil {
		t.Fatalf("Compile cell 0.25: %v", err)
	}
	if maskVolumes(a.FilterComplex) == maskVolumes(c.FilterComplex) {
		t.Error("a per-cell matrix gain change was treated as live; it must restart")
	}
}

// withoutVolumeStages removes every named volume stage. The byte-identity tests
// predate the stage; they still prove the rest of the mix is unchanged, which
// is the property they exist for. The stage itself is pinned by the tests above.
func withoutVolumeStages(graph string) string {
	return regexp.MustCompile(`,volume@[A-Za-z0-9_]+=volume=[0-9.]+`).ReplaceAllString(graph, "")
}
