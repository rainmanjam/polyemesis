package ffmpeg

import (
	"context"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A variable-frame-rate source keeps its timing through a rendition.
//
// This is #342, and the answer turned out to be that nothing is broken --
// which is worth a test precisely because nothing asserted it. `-fps_mode` and
// `-vsync` are never set anywhere in this codebase, so what happens to an
// irregular source through a rendition is FFmpeg's default rather than a
// decision this project made. A default nobody has verified is indistinguishable
// from one nobody has noticed is wrong.
//
// The realistic source is screen capture: static stretches produce no new
// frames, so the frame intervals are irregular while the timestamps stay
// monotonic. Staged here by dropping four frames in every ten and keeping the
// original timestamps, which leaves exactly that shape -- 30 fps nominal, ~18
// fps actual, two distinct frame intervals.
//
// WHAT IT ASSERTS, AND WHY NOT THE OBVIOUS THING. `duration_time` per frame is
// the wrong measurement and the first version of this test used it. MPEG-TS
// does not store per-frame durations, so ffprobe DERIVES them from
// r_frame_rate: every frame comes back a uniform 0.0333s and the stream looks
// like it was silently resampled to CFR, losing 1.6 seconds of a 4 second clip.
// It was not. Presentation timestamps are what govern playback and they are
// carried through untouched, which the PTS span proves and the duration field
// actively contradicts.
func TestAVFRSourceKeepsItsTimingThroughARendition(t *testing.T) {
	// One skip rather than two: this test needs both binaries and can do
	// nothing useful with either alone, so splitting them would add a second
	// skip site to the census for a condition that is really one.
	bin, errBin := exec.LookPath("ffmpeg")
	probe, errProbe := exec.LookPath("ffprobe")
	if errBin != nil || errProbe != nil {
		t.Skipf("ffmpeg and ffprobe are both required to measure frame timing (%v, %v)", errBin, errProbe)
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "vfr.mp4")

	// 30 fps nominal, four frames of every ten dropped with their original
	// timestamps kept -- irregular intervals, monotonic PTS.
	// An audio track too: RenditionArgs maps and copies audio unconditionally,
	// so a video-only fixture fails on `-map 0:a` before reaching anything this
	// test is about.
	run(t, bin, 60*time.Second,
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=30:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=4",
		"-vf", `select='not(between(mod(n\,10),3,6))'`,
		"-fps_mode", "passthrough",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-shortest", "-y", src)

	srcFrames, srcSpan := ptsSpan(t, probe, src)
	// Guard the fixture itself. If this ever stops being VFR the test still
	// passes while proving nothing, which is the failure mode that matters most
	// in a test whose whole subject is irregular timing.
	if nominal := 30.0; srcFrames == 0 || math.Abs(float64(srcFrames-1)/srcSpan-nominal) < 5 {
		t.Fatalf("the fixture is not VFR: %d frames over %.3fs is %.1f fps, too close to the %g fps nominal rate",
			srcFrames, srcSpan, float64(srcFrames-1)/srcSpan, nominal)
	}

	// The rendition's own arguments, into the container a rendition actually
	// writes. Nothing here sets -r, which is the untested path: a rendition
	// with FPS left at 0.
	out := filepath.Join(dir, "out.ts")
	args := RenditionArgs(RenditionSpec{
		VideoKbps: 1000, GOPSeconds: 2, Encoder: EncoderX264, Preset: "ultrafast",
	})
	full := append([]string{"-i", src}, encodeFlags(t, args)...)
	full = append(full, "-f", "mpegts", "-y", out)
	run(t, bin, 90*time.Second, full...)

	outFrames, outSpan := ptsSpan(t, probe, out)

	if outFrames != srcFrames {
		t.Errorf("frame count changed: %d in, %d out. A rendition must not drop or duplicate frames "+
			"to regularise an irregular source unless it was asked to.", srcFrames, outFrames)
	}
	// A tenth of a frame at the nominal rate. Anything larger is a real
	// resample, not rounding.
	const tol = 0.0033
	if math.Abs(outSpan-srcSpan) > tol {
		t.Errorf("presentation span changed: %.4fs in, %.4fs out (%.4fs of drift). "+
			"Audio is copied through untouched, so any change here desynchronises the two.",
			srcSpan, outSpan, outSpan-srcSpan)
	}
}

// ptsSpan returns the frame count and the seconds between the first and last
// presentation timestamp.
func ptsSpan(t *testing.T, probe, path string) (int, float64) {
	t.Helper()
	out := run(t, probe, 30*time.Second,
		"-loglevel", "error", "-select_streams", "v",
		"-show_entries", "frame=pts_time", "-of", "csv=p=0", path)

	var n int
	var first, last float64
	for _, line := range strings.Split(out, "\n") {
		f := strings.TrimSuffix(strings.TrimSpace(line), ",")
		if f == "" {
			continue
		}
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			continue
		}
		if n == 0 {
			first = v
		}
		last = v
		n++
	}
	return n, last - first
}

// encodeFlags returns the real encoder settings out of a rendition command:
// everything from the first -map up to the output format.
//
// A slice rather than a filter, so this uses the argv the product builds
// instead of a reconstruction of it. If RenditionArgs grows a flag that changes
// frame timing, this test picks it up without being edited -- which is the only
// reason to drive the test from the real builder at all.
func encodeFlags(t *testing.T, args []string) []string {
	t.Helper()
	start, end := -1, -1
	for i, a := range args {
		if a == "-map" && start < 0 {
			start = i
		}
		if a == "-f" && start >= 0 {
			end = i
			break
		}
	}
	if start < 0 || end < 0 {
		t.Fatalf("could not find the encode flags between -map and -f in: %s", strings.Join(args, " "))
	}
	return args[start:end]
}

func run(t *testing.T, bin string, limit time.Duration, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s\n%v\n%s", filepath.Base(bin), strings.Join(args, " "), err, out)
	}
	return string(out)
}
