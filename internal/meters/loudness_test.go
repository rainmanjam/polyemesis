package meters

import (
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"math"
	"strings"
	"testing"
)

func TestArgsSplicesTheAnalyserOntoTheDestinationGraph(t *testing.T) {
	const graph = "[0:a:0]pan=stereo|c0=c0|c1=c1[a_t0];[a_t0]aresample=48000[aout]"

	args := Args(Spec{RelayURL: "udp://127.0.0.1:21005", FilterComplex: graph, OutLabel: "aout"})
	joined := strings.Join(args, " ")

	tests := []struct {
		name string
		want string
	}{
		{"reuses the destination's compiled graph verbatim", graph + ";"},
		{"measures the finished mix, not an intermediate label", ";[aout]ebur128="},
		{"asks ebur128 for true peak", "peak=true"},
		{"routes measurements to stdout as metadata", "ametadata=mode=print:file=-"},
		{"flushes each frame instead of buffering twenty seconds of them", "direct=1"},
		{"maps only the metadata stream", "-map [" + analyserOut + "]"},
		{"discards the samples", "-f null -"},
		{"keeps stderr a human log", "-loglevel warning"},
		// DERIVED, NOT A LITERAL. This assertion's point is that the analyser
		// reads the relay through the SAME builder every other consumer uses,
		// so pinning the number here made it a second place the number lives --
		// and raising the shared one broke this test rather than agreeing with
		// it. ffmpeg.RelayInputURL is the single definition; ask it.
		{"reads the relay with the same overrun policy as every other consumer",
			strings.TrimPrefix(ffmpeg.RelayInputURL("x"), "x?")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("args missing %q\ngot: %s", tc.want, joined)
			}
		})
	}

	// A video map would double the cost of the whole tier for a number that
	// has nothing to do with pictures.
	if strings.Contains(joined, "0:v") {
		t.Fatalf("analyser maps video; it must decode audio only:\n%s", joined)
	}
}

func TestArgsFallsBackToRoutingsDefaultOutLabel(t *testing.T) {
	args := Args(Spec{FilterComplex: "[0:a:0]anull[aout]"})
	if !strings.Contains(strings.Join(args, " "), ";[aout]ebur128=") {
		t.Fatalf("empty OutLabel must resolve to routing's default:\n%v", args)
	}
}

const sampleFrames = `frame:0    pts:0       pts_time:0
lavfi.r128.M=-120.691
lavfi.r128.S=-120.691
lavfi.r128.I=-70.000
lavfi.r128.LRA=0.000
lavfi.r128.true_peaks_ch0=0.125
lavfi.r128.true_peak=0.125
frame:1    pts:4410    pts_time:24.5
lavfi.r128.M=-14.2
lavfi.r128.S=-14.4
lavfi.r128.I=-13.8
lavfi.r128.LRA=6.5
lavfi.r128.true_peak=0.891
`

func TestParseEmitsOneFrameForEachMetadataBlock(t *testing.T) {
	var got []Frame
	if err := Parse(strings.NewReader(sampleFrames), func(f Frame) { got = append(got, f) }); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 frames, got %d: %+v", len(got), got)
	}

	t.Run("the trailing frame is flushed at end of stream", func(t *testing.T) {
		if got[1].ShortTermLUFS != -14.4 {
			t.Fatalf("short-term = %v, want -14.4", got[1].ShortTermLUFS)
		}
	})
	t.Run("programme time comes from pts_time", func(t *testing.T) {
		if got[1].Seconds != 24.5 {
			t.Fatalf("seconds = %v, want 24.5", got[1].Seconds)
		}
	})
	t.Run("the ebur128 floor is not reported as a measurement", func(t *testing.T) {
		if got[0].Integrated {
			t.Fatalf("I=%v at the floor must not count as integrated", got[0].IntegratedLUFS)
		}
		if !got[1].Integrated {
			t.Fatalf("I=%v is a real reading", got[1].IntegratedLUFS)
		}
	})
	t.Run("true peak is converted from linear amplitude to dBTP", func(t *testing.T) {
		if math.Abs(got[0].TruePeakDBTP-(-18.06)) > 0.05 {
			t.Fatalf("true peak = %v dBTP, want about -18.06", got[0].TruePeakDBTP)
		}
		if math.Abs(got[1].TruePeakDBTP-(-1.0)) > 0.05 {
			t.Fatalf("true peak = %v dBTP, want about -1.0", got[1].TruePeakDBTP)
		}
	})
	t.Run("the per-channel peak key is not mistaken for the overall one", func(t *testing.T) {
		// true_peaks_ch0 arrives before true_peak in the same block and has the
		// same prefix; a prefix match would make the two indistinguishable.
		if got[1].TruePeakDBTP == got[0].TruePeakDBTP {
			t.Fatal("frames must not share a peak value parsed from the wrong key")
		}
	})
}

func TestParseIgnoresLinesItCannotUse(t *testing.T) {
	in := "frame:0 pts_time:1.0\nlavfi.r128.M=notanumber\nlavfi.astats.1.Peak_level=-3\nlavfi.r128.S=-9.5\n"
	var got []Frame
	if err := Parse(strings.NewReader(in), func(f Frame) { got = append(got, f) }); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 1 || got[0].ShortTermLUFS != -9.5 || got[0].MomentaryLUFS != 0 {
		t.Fatalf("unparseable and foreign keys must be skipped, got %+v", got)
	}
}

func TestDBTPFloorsDigitalSilence(t *testing.T) {
	tests := []struct {
		name   string
		linear float64
		want   float64
	}{
		{"silence is the floor, not negative infinity", 0, TruePeakFloor},
		{"a negative amplitude cannot happen and is floored anyway", -1, TruePeakFloor},
		{"unity is 0 dBTP", 1, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DBTP(tc.linear); math.Abs(got-tc.want) > 0.001 {
				t.Fatalf("DBTP(%v) = %v, want %v", tc.linear, got, tc.want)
			}
		})
	}
}
