package supervisor

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// The end-to-end proof: a REAL FFmpeg, run under the supervisor as a live
// destination would be, has one track's level changed while it runs. The mixed
// output's RMS must follow the commands, the process must not restart, and every
// command must come back ret:0.
//
// Requires FFmpeg on PATH. Not verified on Windows, where the live path is
// disabled and this test is skipped.
func TestLiveVolumeChangesARunningFFmpegWithoutRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real FFmpeg for about 8 s")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the live command channel is unverified on Windows")
	}
	bin := testenv.FFmpegBinary(t, "ffmpeg", "ffmpeg is not on PATH; the live-volume proof cannot run")

	const (
		rate   = 44100
		durSec = 8
		muteAt = 2 * time.Second
		gainAt = 4500 * time.Millisecond
	)
	out := filepath.Join(t.TempDir(), "mix.raw")
	args := []string{
		// The production prefix, minus -nostdin: stdin is the command channel.
		"-hide_banner", "-loglevel", "warning", "-nostats", "-progress", "pipe:1", "-y",
		"-re", "-f", "lavfi", "-i", "sine=frequency=440:duration=8",
		"-re", "-f", "lavfi", "-i", "sine=frequency=880:duration=8",
		"-filter_complex",
		"[0:a]volume@trkA=1.0[a0];[1:a]volume@trkB=4.0[a1];" +
			"[a0][a1]amix=inputs=2:duration=longest:normalize=0[out]",
		"-map", "[out]",
		"-f", "s16le", "-ac", "1", "-ar", "44100",
		out,
	}

	p := New(quietLog(), Spec{Name: "live-proof", Kind: "destination", Bin: bin, Args: args, LiveCommands: true})
	p.Start()
	defer func() { _ = p.Stop(t.Context()) }()
	waitFor(t, "running", func() bool { return p.Status().State == StateRunning })

	start := time.Now()
	time.Sleep(muteAt)
	muteSentAt := time.Since(start)
	t0 := time.Now()
	if err := p.SetVolume("volume@trkB", 0); err != nil {
		t.Fatalf("mute command: %v", err)
	}
	muteLatency := time.Since(t0)

	time.Sleep(gainAt - muteSentAt)
	gainSentAt := time.Since(start)
	t1 := time.Now()
	if err := p.SetVolume("volume@trkB", 2.0); err != nil {
		t.Fatalf("gain command: %v", err)
	}
	gainLatency := time.Since(t1)

	// Let the source run out; FFmpeg exits at EOF and the supervisor does not
	// restart it, since this process is not set to auto-restart.
	waitFor(t, "ffmpeg to finish", func() bool {
		s := p.Status()
		return s.State == StateStopped || s.State == StateFailed
	})

	t.Logf("reply latency: mute %v, gain %v (liveReplyTimeout %v)", muteLatency, gainLatency, liveReplyTimeout)
	if r := p.Status().Restarts; r != 0 {
		t.Fatalf("ffmpeg restarted %d time(s); a live command must not restart it", r)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]int16, len(raw)/2)
	if err := binary.Read(bytes.NewReader(raw[:len(samples)*2]), binary.LittleEndian, samples); err != nil {
		t.Fatal(err)
	}

	// The output runs ahead of wall clock under -re, so the windows are padded a
	// second past the send time, as the spike measured.
	muteS := muteSentAt.Seconds()
	gainS := gainSentAt.Seconds()
	pre := liveRMS(samples, rate, 0.5, muteS-0.3)
	muted := liveRMS(samples, rate, muteS+1.0, gainS-0.3)
	gained := liveRMS(samples, rate, gainS+1.0, durSec-0.5)
	t.Logf("RMS pre-mute=%.4f muted=%.4f gain-2.0=%.4f", pre, muted, gained)

	// Sine amplitude is 0.125. Track A at 1.0 -> 0.125; track B at 4.0 -> 0.5,
	// then 0.0 (muted), then 2.0 (0.25). Both: ~0.364; B muted: ~0.088; B at 2.0:
	// ~0.198.
	if pre < 0.34 || pre > 0.39 {
		t.Errorf("pre-mute RMS %.4f, want ~0.364", pre)
	}
	if muted < 0.08 || muted > 0.095 {
		t.Errorf("muted RMS %.4f, want ~0.088: track B must be silent", muted)
	}
	if gained < 0.18 || gained > 0.21 {
		t.Errorf("gain-2.0 RMS %.4f, want ~0.198: the new level must apply", gained)
	}
}

// liveRMS returns the RMS of little-endian s16 mono samples in [from,to) seconds.
func liveRMS(samples []int16, rate int, from, to float64) float64 {
	lo := int(from * float64(rate))
	hi := int(to * float64(rate))
	if hi > len(samples) {
		hi = len(samples)
	}
	if lo < 0 || lo >= hi {
		return math.NaN()
	}
	var sum float64
	for _, s := range samples[lo:hi] {
		v := float64(s) / 32768
		sum += v * v
	}
	return math.Sqrt(sum / float64(hi-lo))
}
