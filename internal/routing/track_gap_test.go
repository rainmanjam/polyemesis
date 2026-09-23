package routing

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// A failover to a source with fewer tracks takes the missing tracks off the
// selector's relay for the length of the outage, and a destination mixing
// several tracks then has one leg that stops and one that keeps going. amix
// sums its inputs by SAMPLE COUNT, not by timestamp, so the leg that stopped
// comes back exactly where it left off in sample terms: every sample it
// delivers after the return is summed with a sample the other leg delivered an
// outage earlier. The two tracks stay that far apart until the destination
// restarts. Measured in the field as an 18 s offset after an 18 s outage with a
// 3-track primary and a 1-track slate.
//
// These run the compiled graph through real FFmpeg over an MPEG-TS in which
// track 2 is simply ABSENT for a stretch, which is exactly what a destination
// reading the relay sees while the slate is on air. A 0.5 s tone sits on track 2
// at t=20; the only correct place for it in the mix is t=20.

// firstSound returns when the mixed output first rises above silence.
func firstSound(t *testing.T, bin, input string, res Result) float64 {
	t.Helper()
	cmd := exec.Command(bin, "-nostdin", "-v", "info", "-i", input,
		"-filter_complex", fmt.Sprintf("%s;[%s]silencedetect=n=-40dB:d=1[det]", res.FilterComplex, res.OutLabel),
		"-map", "[det]", "-f", "null", "-")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg rejected the graph: %v\ngraph: %s\n%s", err, res.FilterComplex, out)
	}
	m := regexp.MustCompile(`silence_end: ([0-9.]+)`).FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("the tone never reached the output\ngraph: %s\n%s", res.FilterComplex, out)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatalf("silence_end %q: %v", m[1], err)
	}
	return v
}

// threeTrackTS builds a 30 s three-track stream: tracks 0 and 1 silent and
// continuous, track 2 silent but for a tone at 20-20.5 s, with track 2's
// packets filtered by keep (an aselect expression over t).
func threeTrackTS(t *testing.T, bin, keep string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tracks.ts")
	tone := "if(between(t,20,20.5),0.5*sin(2*PI*1000*t),0)"
	build := exec.Command(bin, "-nostdin", "-v", "error", "-y",
		"-f", "lavfi", "-i", "anullsrc=channel_layout=stereo:sample_rate=48000:d=30",
		"-f", "lavfi", "-i", "anullsrc=channel_layout=stereo:sample_rate=48000:d=30",
		"-f", "lavfi", "-i", fmt.Sprintf("aevalsrc='%s|%s':s=48000:d=30", tone, tone),
		"-filter_complex", fmt.Sprintf("[2:a]aselect='%s'[g]", keep),
		"-map", "0:a", "-map", "1:a", "-map", "[g]",
		"-c:a", "aac", "-b:a", "128k", "-f", "mpegts", path)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the source failed: %v\n%s", err, out)
	}
	return path
}

func TestATrackThatGoesMissingComesBackOnItsOwnTimeline(t *testing.T) {
	bin := testenv.FFmpegBinary(t, "ffmpeg",
		"ffmpeg not on PATH: lost the real-FFmpeg check that a missing track rejoins the mix on its own timeline")
	src := Source{Tracks: []Track{
		{Index: 0, Channels: 2, Codec: "aac", Layout: "stereo"},
		{Index: 1, Channels: 2, Codec: "aac", Layout: "stereo"},
		{Index: 2, Channels: 2, Codec: "aac", Layout: "stereo"},
	}}

	cases := []struct {
		name string
		// keep is which of track 2's packets survive.
		keep string
	}{
		// The failover: track 2 vanishes for five seconds mid-stream and returns.
		{"a five-second outage mid-stream", "not(between(t,10,15))"},
		// A destination (re)started while the slate is on air: its track 2
		// appears for the first time at the return, five seconds after track 0.
		{"a track that first appears five seconds late", "gte(t,5)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := threeTrackTS(t, bin, tc.keep)
			for _, compiled := range []struct {
				name string
				fn   func(Profile, Source) (Result, error)
			}{{"Compile", Compile}, {"CompileProvisional", CompileProvisional}} {
				res, err := compiled.fn(simple(NormOff, 0, 2), src)
				if err != nil {
					t.Fatalf("%s: %v", compiled.name, err)
				}
				at := firstSound(t, bin, in, res)
				// The tone is at 20 s on its own track. Where it lands in the
				// mix is where track 2 sits relative to track 0; a five-second
				// early arrival is the outage folded out of track 2's timeline.
				if at < 19.5 || at > 20.5 {
					t.Errorf("%s: track 2's tone reached the mix at %.2fs, want 20s: the track is "+
						"offset from track 0 by %.1fs\ngraph: %s",
						compiled.name, at, 20-at, res.FilterComplex)
				}
			}
		})
	}
}

// One track has nothing to be aligned against, so its graph must stay the
// single-leg shape: the final resample already fills its gaps.
func TestASingleTrackGraphGainsNoAlignmentStage(t *testing.T) {
	src := Source{Tracks: []Track{{Index: 0, Channels: 2, Codec: "aac", Layout: "stereo"}}}
	res, err := Compile(simple(NormOff, 0), src)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(res.FilterComplex, "aresample"); n != 1 {
		t.Errorf("a one-track graph has %d resample stages, want the final one only:\n%s", n, res.FilterComplex)
	}
}
