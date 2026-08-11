package meters

import (
	"bufio"
	"math"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// The whole claim of this tier is that it measures what a DESTINATION sends,
// not what the ingest carries. That is only true if the analyser's command line
// really does apply the destination's routing graph before ebur128 sees a
// sample — and the only way to know is to send two tracks at deliberately
// different levels and check that two destinations selecting different tracks
// come back with different loudness.
//
// Skipped without FFmpeg, and in -short: it streams in real time.
func TestAnalyserMeasuresThePerDestinationMixAndNotTheIngest(t *testing.T) {
	if testing.Short() {
		t.Skip("streams in real time")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}

	src := routing.Source{Tracks: []routing.Track{
		{Index: 0, Channels: 2, Codec: "aac", Layout: "stereo"},
		{Index: 1, Channels: 2, Codec: "aac", Layout: "stereo"},
	}}
	profileFor := func(track int) routing.Profile {
		return routing.Profile{
			Mode:       routing.ModeSimple,
			Tracks:     []routing.TrackSel{{Track: track, Enabled: true, Gain: 1}},
			Normalize:  routing.NormOff,
			SampleRate: 48000,
		}
	}

	loud := measureTrack(t, ffmpeg, src, profileFor(0))
	quiet := measureTrack(t, ffmpeg, src, profileFor(1))

	t.Run("both destinations produced a real integrated reading", func(t *testing.T) {
		if !loud.Integrated || !quiet.Integrated {
			t.Fatalf("loud=%+v quiet=%+v", loud, quiet)
		}
	})
	t.Run("the destination taking the quiet track measures about 20 LU lower", func(t *testing.T) {
		// The source puts track 0 at 0.5 and track 1 at 0.05: a factor of ten,
		// which is 20 dB. A tolerance of 3 LU absorbs the encoder and the
		// gating without letting a routing bug through.
		gap := loud.IntegratedLUFS - quiet.IntegratedLUFS
		if math.Abs(gap-20) > 3 {
			t.Fatalf("gap = %.1f LU (loud %.1f, quiet %.1f), want about 20",
				gap, loud.IntegratedLUFS, quiet.IntegratedLUFS)
		}
	})
	t.Run("true peak follows the same mix", func(t *testing.T) {
		if loud.TruePeakDBTP <= quiet.TruePeakDBTP {
			t.Fatalf("true peak did not follow the routing: loud %.1f, quiet %.1f",
				loud.TruePeakDBTP, quiet.TruePeakDBTP)
		}
	})
}

// measureTrack runs one analyser against a synthetic two-track stream and
// returns the last frame it printed.
func measureTrack(t *testing.T, ffmpeg string, src routing.Source, p routing.Profile) Frame {
	t.Helper()

	compiled, err := routing.Compile(p, src)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	port := freeUDPPort(t)
	url := "udp://127.0.0.1:" + strconv.Itoa(port)

	analyser := exec.Command(ffmpeg, Args(Spec{
		RelayURL:      url,
		FilterComplex: compiled.FilterComplex,
		OutLabel:      compiled.OutLabel,
	})...)
	stdout, err := analyser.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
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

	// The analyser binds the port; the source must not start pushing before it
	// has, or the first seconds land in nothing.
	//
	// #211: this was `time.Sleep(700 * time.Millisecond)` -- an interval assumed
	// away. The observable already existed in the shell suites as
	// poly_wait_port_ready, where it was measured to be the entire residual flake
	// rate of acceptance-failover; it did not exist in Go. WaitUDPPortBound is
	// that observer: it asks the kernel whether the port is taken, which is the
	// same question the shell asks lsof.
	//
	// Named as a hard failure rather than a warning because everything below
	// depends on it. A source pushing into an unbound port produces a frame count
	// of zero and a "the analyser printed no usable frame" three seconds later,
	// which describes the wrong component.
	if !testenv.WaitUDPPortBound(port, 10*time.Second) {
		t.Fatalf("the analyser never bound udp 127.0.0.1:%d in 10s, so every datagram the "+
			"source is about to push would land in nothing and the measurement below would "+
			"blame the meter for it", port)
	}

	source := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "sine=f=1000:d=8",
		"-f", "lavfi", "-i", "sine=f=300:d=8",
		"-filter_complex", "[0:a]volume=0.5,aformat=channel_layouts=stereo[a0];[1:a]volume=0.05,aformat=channel_layouts=stereo[a1]",
		"-map", "[a0]", "-map", "[a1]",
		"-c:a", "aac", "-f", "mpegts", url+"?pkt_size=1316")
	if out, err := source.CombinedOutput(); err != nil {
		t.Skipf("could not produce a test stream (%v): %s", err, out)
	}

	// Give the analyser a moment to drain what is still in the socket.
	deadline := time.Now().Add(2 * time.Second)
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
		t.Fatalf("the analyser printed no usable frame: %+v", last)
	}
	return last
}

// #211 found four copies of this helper and this is the fifth. One
// implementation now, in internal/testenv.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	return testenv.FreeUDPPort(t)
}
