package meters

import (
	"bufio"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// A destination with a VOD track sends TWO mixes, and both are compiled into one
// graph: the live mix's label and the VOD mix's label (vod_aout) sit side by
// side in FilterComplex. The analyser for one mix must not be handed a graph
// whose other half dangles. FFmpeg refuses that ("has output unconnected"), so
// before Spec.Sink an analyser for ANY paired destination could not start at all.
//
// These tests use the real binary, like the single-mix integration test, and
// check the claim the tier exists to make: each mix is measured on its own, and
// the VOD reading follows the VOD mix's track rather than the live mix's.

// pairedVODSource is two tracks at a 20 dB difference, so the mix that selects
// the quiet track reads about 20 LU lower than the one selecting the loud track.
func pairedVODSource() routing.Source {
	return routing.Source{Tracks: []routing.Track{
		{Index: 0, Channels: 2, Codec: "aac", Layout: "stereo"},
		{Index: 1, Channels: 2, Codec: "aac", Layout: "stereo"},
	}}
}

func pairedVODProfile(track int) routing.Profile {
	return routing.Profile{
		Mode:       routing.ModeSimple,
		Tracks:     []routing.TrackSel{{Track: track, Enabled: true, Gain: 1}},
		Normalize:  routing.NormOff,
		SampleRate: 48000,
	}
}

// measureMix runs ONE analyser on the paired graph for the named mix and
// returns its last usable frame. which selects the output label; the other label
// is sunk, exactly as the engine's analyser is built.
func measureMix(t *testing.T, ffmpeg string, pair routing.Pair, vod bool) Frame {
	t.Helper()
	out, sink := pair.OutLabel, pair.SecondOutLabel
	if vod {
		out, sink = pair.SecondOutLabel, pair.OutLabel
	}
	if sink == "" {
		t.Fatalf("the pair has no second mix to sink; compile it with a VOD profile")
	}

	port := freeUDPPort(t)
	url := "udp://127.0.0.1:" + strconv.Itoa(port)
	analyser := exec.Command(ffmpeg, Args(Spec{
		RelayURL:      url,
		FilterComplex: pair.FilterComplex,
		OutLabel:      out,
		Sink:          []string{sink},
	})...)
	stdout, err := analyser.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderr lockedBuffer
	analyser.Stderr = &stderr
	if err := analyser.Start(); err != nil {
		t.Fatalf("start analyser: %v", err)
	}
	defer func() {
		_ = analyser.Process.Kill()
		_ = analyser.Wait()
	}()

	var mu sync.Mutex
	var last Frame
	go func() {
		_ = Parse(bufio.NewReader(stdout), func(f Frame) {
			mu.Lock()
			last = f
			mu.Unlock()
		})
	}()

	if !testenv.WaitUDPPortBound(port, 10*time.Second) {
		t.Fatalf("the analyser never bound udp 127.0.0.1:%d", port)
	}
	source := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "sine=f=1000:d=8",
		"-f", "lavfi", "-i", "sine=f=300:d=8",
		"-filter_complex", "[0:a]volume=0.5,aformat=channel_layouts=stereo[a0];[1:a]volume=0.05,aformat=channel_layouts=stereo[a1]",
		"-map", "[a0]", "-map", "[a1]",
		"-c:a", "aac", "-f", "mpegts", url+"?pkt_size=1316")
	if out, err := source.CombinedOutput(); err != nil {
		t.Fatalf("could not produce the test stream the analyser measures (%v): %s", err, out)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := last
		mu.Unlock()
		if got.Seconds >= 5 {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if !last.Integrated {
		t.Fatalf("the analyser printed no usable frame for the %s mix; FFmpeg said: %s",
			map[bool]string{true: "VOD", false: "live"}[vod], stderr.String())
	}
	return last
}

// The live and the VOD mix each get a real reading, and the VOD reading follows
// the VOD mix's own track. Live selects the loud track, VOD the quiet one, so
// the VOD mix must come back about 20 LU lower.
//
// MUTATION: in Args, drop the Sink terminator. Observed: FAIL, "the analyser
// printed no usable frame for the live mix" with FFmpeg's "unconnected" error.
func TestBothMixesOfAPairedGraphAreMeasuredSeparatelyAndTheVODOneFollowsItsOwnTrack(t *testing.T) {
	ffmpeg := testenv.FFmpegBinary(t, "ffmpeg", "no ffmpeg on PATH")
	pair, err := routing.CompilePair(pairedVODProfile(0), ptrTo(pairedVODProfile(1)), pairedVODSource())
	if err != nil {
		t.Fatalf("CompilePair: %v", err)
	}

	live := measureMix(t, ffmpeg, pair, false)
	vod := measureMix(t, ffmpeg, pair, true)
	if !live.Integrated || !vod.Integrated {
		t.Fatalf("live=%+v vod=%+v", live, vod)
	}
	gap := live.IntegratedLUFS - vod.IntegratedLUFS
	if math.Abs(gap-20) > 3 {
		t.Fatalf("the VOD mix is %.1f LU below the live mix, want about 20 (live %.1f, VOD %.1f)",
			gap, live.IntegratedLUFS, vod.IntegratedLUFS)
	}
}

func ptrTo[T any](v T) *T { return &v }

// lockedBuffer collects an analyser's stderr so a failure can quote FFmpeg.
type lockedBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b = append(l.b, p...)
	return len(p), nil
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.b)
}

// THE TAP NEVER TOUCHES WHAT IS PUBLISHED. The analyser is a separate process
// that reads the destination's relay, so the destination's own command line is
// the same with or without it. What the analyser itself must not do is map any
// video, which would decode it, or alter the graph it is handed: the graph goes
// through verbatim and only the sink is appended.
//
// MUTATION: in Args, add "-map 0:v" to the output. Observed: FAIL, "the analyser
// maps video".
func TestTheAnalyserArgvNeverMapsVideoAndPassesThePairedGraphThrough(t *testing.T) {
	pair, err := routing.CompilePair(pairedVODProfile(0), ptrTo(pairedVODProfile(1)), pairedVODSource())
	if err != nil {
		t.Fatalf("CompilePair: %v", err)
	}
	argv := Args(Spec{
		RelayURL:      "udp://127.0.0.1:9",
		FilterComplex: pair.FilterComplex,
		OutLabel:      pair.OutLabel,
		Sink:          []string{pair.SecondOutLabel},
	})
	for i, a := range argv {
		if a == "-map" && i+1 < len(argv) && strings.Contains(argv[i+1], "0:v") {
			t.Fatalf("the analyser maps video (%s); it must decode audio only", argv[i+1])
		}
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, pair.FilterComplex+";") {
		t.Errorf("the analyser's graph does not start with the destination's graph verbatim")
	}
}
