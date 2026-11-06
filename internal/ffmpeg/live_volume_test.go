package ffmpeg

import (
	"runtime"
	"slices"
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
	if runtime.GOOS != "windows" && slices.Contains(args, "-nostdin") {
		t.Fatalf("a destination argv still carries -nostdin, so FFmpeg would ignore its command channel: %v", args)
	}
	if !slices.Contains(CommonArgs(LogWarning, false), "-nostdin") {
		t.Fatal("CommonArgs lost -nostdin; every other child would now read stdin")
	}
	if runtime.GOOS != "windows" && slices.Contains(commonArgsLive(), "-nostdin") {
		t.Fatal("commonArgsLive still carries -nostdin")
	}
}

// Windows has no verified command channel: the supervisor gives the child no
// pipe there and the engine always restarts. Its destination argv therefore
// keeps -nostdin, so the unverified platform runs exactly the command it ran
// before live gain existed.
//
// MUTATION: return the unix form for every goos. Observed: FAIL.
func TestWindowsDestinationsKeepNostdin(t *testing.T) {
	if !slices.Contains(commonArgsLiveFor("windows"), "-nostdin") {
		t.Error("a Windows destination lost -nostdin, though nothing on Windows writes to its stdin")
	}
	for _, goos := range []string{"linux", "darwin", "freebsd"} {
		if slices.Contains(commonArgsLiveFor(goos), "-nostdin") {
			t.Errorf("%s destination carries -nostdin, so its command channel is deaf", goos)
		}
	}
}
