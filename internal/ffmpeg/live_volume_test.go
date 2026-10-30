package ffmpeg

import (
	"slices"
	"strings"
	"testing"
)

// A destination takes runtime commands on stdin, so -nostdin must be absent
// from its argv. Every other child keeps it: the flag still protects a
// backgrounded probe or recorder from stealing terminal input.
func TestDestinationArgsHaveNoNostdinOtherChildrenKeepIt(t *testing.T) {
	args := DestinationArgs(DestSpec{
		Kind:          DestRTMP,
		Target:        "rtmp://live.example/app/key",
		FilterComplex: "[0:a:0]anull[aout]",
		AudioOutLabel: "aout",
	})
	if slices.Contains(args, "-nostdin") {
		t.Fatalf("a destination argv still carries -nostdin, so FFmpeg would ignore its command channel: %v", args)
	}
	if !slices.Contains(commonArgs(), "-nostdin") {
		t.Fatal("commonArgs lost -nostdin; every other child would now read stdin")
	}
	if slices.Contains(commonArgsLive(), "-nostdin") {
		t.Fatal("commonArgsLive still carries -nostdin")
	}
}

// The mask is what lets a gain edit leave the signature alone: it must blank
// the level of every named volume stage and nothing else.
func TestMaskLiveVolumesBlanksOnlyTheLevels(t *testing.T) {
	a := "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1,volume@trk0=volume=0.5,aresample=async=1[a_t0];" +
		"[0:a:1]volume@trk1=volume=1,aresample=async=1[a_t1]"
	b := strings.NewReplacer("volume=0.5", "volume=2", "volume=1,", "volume=0,").Replace(a)
	if MaskLiveVolumes(a) != MaskLiveVolumes(b) {
		t.Errorf("masking did not equate two graphs that differ only in levels:\n%s\n%s",
			MaskLiveVolumes(a), MaskLiveVolumes(b))
	}
	// A structural difference must survive the mask.
	c := strings.Replace(a, "aresample=async=1[a_t0]", "afftdn=nr=12[a_t0]", 1)
	if MaskLiveVolumes(a) == MaskLiveVolumes(c) {
		t.Error("masking hid a structural change")
	}
}

func TestLiveVolumeLevelsReadsEachNamedStage(t *testing.T) {
	g := "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1,volume@trk0=volume=0.5,aresample=async=1[a_t0];" +
		"[0:a:1]volume@trk1=volume=0,aresample=async=1[a_t1]"
	got := LiveVolumeLevels(g)
	want := map[string]float64{"trk0": 0.5, "trk1": 0}
	if len(got) != len(want) {
		t.Fatalf("LiveVolumeLevels = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("LiveVolumeLevels[%s] = %v, want %v", k, got[k], v)
		}
	}
}
