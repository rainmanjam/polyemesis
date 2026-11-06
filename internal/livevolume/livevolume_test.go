package livevolume_test

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/livevolume"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// THE LINK THAT ONLY COMMENTS HELD. routing writes the stage, this package reads
// it back, and the supervisor accepts its name as a command target. No test
// passed routing.Compile's output through the reader, so a change to either
// spelling would have passed every test and silently stopped live gain from
// finding any stage.
func TestEveryStageRoutingCompilesIsReadBackAndAddressable(t *testing.T) {
	prof := routing.DefaultProfile()
	res, err := routing.Compile(prof, routing.Source{Tracks: []routing.Track{
		{Index: 0, Channels: 2, Codec: "aac", Layout: "stereo"},
		{Index: 1, Channels: 1, Codec: "aac", Layout: "mono"},
	}})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !strings.Contains(res.FilterComplex, livevolume.Prefix) {
		t.Fatalf("the compiled graph has no named volume stage, so this proves nothing: %s", res.FilterComplex)
	}

	levels := livevolume.Levels(res.FilterComplex)
	if len(levels) == 0 {
		t.Fatalf("Levels read no stage out of what routing compiled: %s", res.FilterComplex)
	}
	for name, level := range levels {
		if level != 1 {
			t.Errorf("stage %s reads back as %v, want the profile's unity gain", name, level)
		}
		if !livevolume.ValidInstance(livevolume.Instance(name)) {
			t.Errorf("stage %s is not a valid live command target", name)
		}
		if len(livevolume.Instance(name)) > livevolume.MaxInstance {
			t.Errorf("stage %s is longer than FFmpeg reads whole", name)
		}
	}
	masked := livevolume.Mask(res.FilterComplex)
	if strings.Count(masked, "=volume=*") != len(levels) {
		t.Errorf("Mask blanked %d of %d stages: %s", strings.Count(masked, "=volume=*"), len(levels), masked)
	}
	if livevolume.Mask(masked) != masked {
		t.Error("Mask is not idempotent")
	}
}

func TestStageRendersWhatLevelsReads(t *testing.T) {
	g := "[0:a:0]" + livevolume.Stage("trk0", "0.5") + "[a]"
	if got := livevolume.Levels(g); len(got) != 1 || got["trk0"] != 0.5 {
		t.Errorf("Levels(%q) = %v", g, got)
	}
}

func TestOnlyAVolumeInstanceIsAValidTarget(t *testing.T) {
	for _, ok := range []string{"volume@trk0", "volume@d7_trk2"} {
		if !livevolume.ValidInstance(ok) {
			t.Errorf("%q should be a valid target", ok)
		}
	}
	for _, bad := range []string{"", "volume@", "volume@a b", "volume@a\nq", "afade@x", "volume@trk0=volume=1", "q"} {
		if livevolume.ValidInstance(bad) {
			t.Errorf("%q must not be a valid target", bad)
		}
	}
}

// The mask is what lets a gain edit leave the signature alone: it must blank
// the level of every named volume stage and nothing else.
func TestMaskBlanksOnlyTheLevels(t *testing.T) {
	a := "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1,volume@trk0=volume=0.5,aresample=async=1[a_t0];" +
		"[0:a:1]volume@trk1=volume=1,aresample=async=1[a_t1]"
	b := strings.NewReplacer("volume=0.5", "volume=2", "volume=1,", "volume=0,").Replace(a)
	if livevolume.Mask(a) != livevolume.Mask(b) {
		t.Errorf("masking did not equate two graphs that differ only in levels:\n%s\n%s",
			livevolume.Mask(a), livevolume.Mask(b))
	}
	// A structural difference must survive the mask.
	c := strings.Replace(a, "aresample=async=1[a_t0]", "afftdn=nr=12[a_t0]", 1)
	if livevolume.Mask(a) == livevolume.Mask(c) {
		t.Error("masking hid a structural change")
	}
}

func TestLevelsReadsEachNamedStage(t *testing.T) {
	g := "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1,volume@trk0=volume=0.5,aresample=async=1[a_t0];" +
		"[0:a:1]volume@trk1=volume=0,aresample=async=1[a_t1]"
	got := livevolume.Levels(g)
	want := map[string]float64{"trk0": 0.5, "trk1": 0}
	if len(got) != len(want) {
		t.Fatalf("livevolume.Levels = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("livevolume.Levels[%s] = %v, want %v", k, got[k], v)
		}
	}
}
