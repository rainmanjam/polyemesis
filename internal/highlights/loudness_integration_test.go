package highlights

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// The loudness pass against a real FFmpeg: a deterministic fixture -- twenty
// seconds of a quiet tone with three seconds at +24 dB starting at 10 s -- run
// through the exact command a scan runs, parsed, and detected. The peak must be
// found where it was put, and nowhere else.
//
// This is what proves LoudnessArgs' filter string and ParseMomentary's format
// agree with the FFmpeg that ships, which no fixture string can.
func TestARealLoudnessPassFindsTheShout(t *testing.T) {
	bin := testenv.FFmpegBinary(t, "ffmpeg", "without ffmpeg the loudness pass cannot be measured")
	src := filepath.Join(t.TempDir(), "shout.mkv")
	gen := exec.Command(bin, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=f=440:r=48000:d=20,volume=volume=0.05,volume=enable='between(t,10,13)':volume=16",
		"-c:a", "flac", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, out)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(context.Background(), bin, LoudnessArgs(src)...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("loudness pass: %v\n%s", err, stderr.String())
	}
	samples, err := ParseMomentary(&stdout, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) < 150 {
		t.Fatalf("%d momentary readings for 20 s; the format did not parse", len(samples))
	}
	peaks := LoudnessPeaks(samples, 20_000, DefaultLoudParams())
	if len(peaks) != 1 {
		t.Fatalf("found %d peaks, want the one shout: %+v", len(peaks), peaks)
	}
	// Momentary loudness is a 400 ms window, so the edges smear by up to that.
	p := peaks[0]
	if p.StartMS < 9_000 || p.StartMS > 11_000 || p.EndMS < 12_000 || p.EndMS > 14_000 {
		t.Errorf("peak %d..%d, want about 10000..13000", p.StartMS, p.EndMS)
	}
	t.Logf("peak %d..%d ms at %.1f LUFS over a %.1f LUFS baseline", p.StartMS, p.EndMS, p.Peak, p.Baseline)
}
