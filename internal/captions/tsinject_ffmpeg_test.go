package captions

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// TestFFmpegsOwnCEA608DecoderReadsTheInjectedCaptionBack is the check that the
// bytes mean what the standard says. The unit tests pin the layout against the
// tables; this one hands a real muxed H.264 stream, with B-frames, to FFmpeg's
// CEA-608 decoder and reads the caption text back out. A wrong row address or
// character code fails here even when every table test passes.
//
// The stream comes from libx264 through FFmpeg at test time, so no binary
// fixture is checked in. A machine without FFmpeg fails loudly in CI and skips
// elsewhere, through the repo's testenv convention.
func TestFFmpegsOwnCEA608DecoderReadsTheInjectedCaptionBack(t *testing.T) {
	ffmpeg := testenv.FFmpegBinary(t, "ffmpeg", "ffmpeg is needed to decode CEA-608 back out of a stream")
	dir := t.TempDir()

	src := filepath.Join(dir, "src.ts")
	gen := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=30",
		"-t", "4", "-c:v", "libx264", "-preset", "ultrafast",
		"-bf", "2", "-g", "30", "-pix_fmt", "yuv420p", "-an",
		"-f", "mpegts", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generating the fixture failed: %v\n%s", err, out)
	}
	in, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}

	// A cue on the second second, with a long enough script to be read.
	const text = "HELLO 608 FROM POLYEMESIS"
	sched := NewSchedule(0)
	sched.Add(Cue{StartPTS: 90000, EndPTS: 3 * 90000, Text: text})
	inj := NewInjector(sched)
	out := append(inj.Write(in), inj.Flush()...)
	if inj.Stats().Injected == 0 {
		t.Fatalf("the injector saw no pictures in %d bytes of generated TS", len(in))
	}
	dst := filepath.Join(dir, "injected.ts")
	if err := os.WriteFile(dst, out, 0o644); err != nil {
		t.Fatal(err)
	}

	// The same readback the spike used: movie with the closed-caption output
	// pad, mapped as a subtitle stream and written out as SRT.
	srt := filepath.Join(dir, "cc.srt")
	read := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "movie=injected.ts[out0+subcc]",
		"-map", "0:s", "-c:s", "srt", "cc.srt")
	read.Dir = dir
	if msg, err := read.CombinedOutput(); err != nil {
		t.Fatalf("decoding the captions failed: %v\n%s", err, msg)
	}
	got, err := os.ReadFile(srt)
	if err != nil {
		t.Fatal(err)
	}
	// 608 spaces are carried as characters, so the decoded text can differ in
	// case or in spacing from the source. The words must survive.
	decoded := strings.ToUpper(strings.Join(strings.Fields(string(got)), " "))
	for _, word := range []string{"HELLO", "608", "FROM", "POLYEMESIS"} {
		if !strings.Contains(decoded, word) {
			t.Fatalf("decoder output does not contain %q:\n%s", word, bytes.TrimSpace(got))
		}
	}
}
