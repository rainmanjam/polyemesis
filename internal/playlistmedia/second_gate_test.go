package playlistmedia

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/media"
)

// THE SECOND GATE.
//
// internal/ffmpeg.ProbeFile's format allowlist had EXACTLY ONE production call
// site -- the upload handler -- and that handler's gate is triggerable by the
// client, because the probe runs under the request's context. A caller who
// sends a complete body and drops the connection gets the file stored with no
// inspection at all.
//
// The upload path now records that and the settings validator refuses an item
// naming such a file, but neither reaches three routes into this worker: an
// item the operator INHERITED (which the validator skips by design), a file
// stored before verdicts existed, and a file put in the uploads directory by
// hand. What arrived here was `ffmpeg -i <path>` with no -f and no
// -protocol_whitelist, and a 3 KB ffconcat naming one clip two hundred times
// was measured producing 50 MB in 8 seconds at 1143% CPU -- 15,517x, past a
// disk guard that had been told to expect 3 KB.
//
// So the worker runs the same allowlist again, at the moment of use.

// realMediaOrSkip puts one second of real 160x90 h264+aac under dir/uploads and
// returns the ffprobe to gate with and the stored name.
//
// ONE SKIP SITE for the whole file, and it names every reason at once. Three
// separate ones -- no ffmpeg, no ffprobe, an ffmpeg that cannot mux h264/aac --
// would be three free passes for the same condition, which is the shape the
// skip ratchet exists to stop. None of them is "the thing under test changed";
// all of them are "this environment cannot run the test at all".
func realMediaOrSkip(t *testing.T, dir, name string) (ffprobe, stored string) {
	t.Helper()
	ffmpegBin, ffmpegErr := exec.LookPath("ffmpeg")
	ffprobeBin, ffprobeErr := exec.LookPath("ffprobe")
	var muxErr error
	if ffmpegErr == nil && ffprobeErr == nil {
		out := filepath.Join(dir, "uploads", name)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			t.Fatal(err)
		}
		mk := exec.Command(ffmpegBin, "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=15",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
			"-map", "0:v", "-map", "1:a",
			"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-t", "1", "-y", out)
		if o, err := mk.CombinedOutput(); err != nil {
			muxErr = fmt.Errorf("%v: %s", err, o)
		}
	}
	if ffmpegErr != nil || ffprobeErr != nil || muxErr != nil {
		t.Skipf("this environment cannot build real media to gate with: "+
			"ffmpeg=%v ffprobe=%v mux=%v", ffmpegErr, ffprobeErr, muxErr)
	}
	return ffprobeBin, name
}

// A source that names other files is refused BEFORE any FFmpeg is built, and
// permanently, so the queue does not spend every attempt on it.
//
// Driven through the REAL verifier -- New with no WithSourceVerifier -- because
// a stubbed one would be asserting that the stub says no.
func TestANormaliseSourceThatNamesOtherFilesIsRefusedPermanently(t *testing.T) {
	dir := t.TempDir()
	ffprobe, victim := realMediaOrSkip(t, dir, "victim-1a2b.mp4")

	script := "ffconcat version 1.0\n" + strings.Repeat("file "+victim+"\n", 200)
	attack := "attack-1a2b.mp4"
	if err := os.WriteFile(filepath.Join(dir, "uploads", attack), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	p := New(nil, Config{
		FFmpeg: "ffmpeg", FFprobe: ffprobe, DataDir: dir, Uploads: mustStore(t, dir),
	},
		WithFreeSpace(func(string) (uint64, error) { return 1 << 60, nil }),
		WithExecer(func(context.Context, media.Command, media.Sink) error {
			t.Error("FFmpeg was started on a file that names other files")
			return nil
		}))

	err := p.RunNormalise(context.Background(), normaliseJob(t, attack), &recorder{})
	if err == nil {
		t.Fatal("an ffconcat script was accepted as a playlist source")
	}
	if !jobs.IsPermanent(err) {
		t.Errorf("the refusal is retryable, so the queue will burn every attempt on it: %v", err)
	}
	if !strings.Contains(err.Error(), "naming other files") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
	if _, statErr := os.Stat(DerivativePath(dir, attack)); statErr == nil {
		t.Error("a derivative was published for a file that names other files")
	}

	// THE CONTROL. Without it this test is satisfied by a verifier that refuses
	// everything, which is a different bug and would strand every playlist.
	var ran bool
	q := New(nil, Config{
		FFmpeg: "ffmpeg", FFprobe: ffprobe, DataDir: dir, Uploads: mustStore(t, dir),
	},
		WithFreeSpace(func(string) (uint64, error) { return 1 << 60, nil }),
		WithStreamProber(streams(true, true)),
		WithDurationProber(func(context.Context, string) (float64, float64, error) { return 1, 1, nil }),
		WithExecer(func(_ context.Context, cmd media.Command, _ media.Sink) error {
			ran = true
			return os.WriteFile(cmd.Args[len(cmd.Args)-1], []byte("normalised"), 0o600)
		}))
	if err := q.RunNormalise(context.Background(), normaliseJob(t, victim), &recorder{}); err != nil {
		t.Fatalf("real media was refused by the same gate, so the assertion above "+
			"proves nothing: %v", err)
	}
	if !ran {
		t.Error("the control never reached FFmpeg")
	}
}

// A source this server cannot inspect is RETRYABLE, not permanent. The
// classification mirrors the upload handler's, and getting it backwards means
// the queue gives up permanently on a file that is fine because ffprobe was
// briefly missing.
func TestASourceThisServerCannotInspectIsRetryable(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "clip-1a2b.mp4", "source bytes")
	p := New(nil, Config{
		FFmpeg:  "ffmpeg",
		FFprobe: filepath.Join(t.TempDir(), "no-such-ffprobe"),
		DataDir: dir,
		Uploads: mustStore(t, dir),
	},
		WithFreeSpace(func(string) (uint64, error) { return 1 << 60, nil }),
		WithExecer(func(context.Context, media.Command, media.Sink) error {
			t.Error("FFmpeg was started although the source could not be inspected")
			return nil
		}))
	err := p.RunNormalise(context.Background(), normaliseJob(t, "clip-1a2b.mp4"), &recorder{})
	if err == nil {
		t.Fatal("a source that could not be inspected was normalised anyway")
	}
	if jobs.IsPermanent(err) {
		t.Errorf("a probe this server could not run was treated as a verdict about "+
			"the file: %v", err)
	}
}

// F3. The encode is bounded by -fs, and the bound is the same figure the disk
// guard demanded room for.
//
// Nothing in the argv bounded the output's LENGTH: everything else bounds its
// SHAPE. checkSpace demanded 2 GiB + the SOURCE's size and FFmpeg then wrote
// whatever the input turned out to be.
func TestTheNormaliseArgvBoundsTheOutputSize(t *testing.T) {
	const cap = 12345678
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"operator media", normaliseArgs("in.mp4", "out.ts", 3, 3, cap)},
		{"a silent source", normaliseSilentArgs("in.mp4", "out.ts", cap)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, n := pairValue(tc.args, "-fs")
			if n != 1 {
				t.Fatalf("-fs appears %d times in %v", n, tc.args)
			}
			if got != strconv.Itoa(cap) {
				t.Errorf("-fs = %q, want %d", got, cap)
			}
			// BEFORE the output path, or FFmpeg reads it as an input option and
			// it bounds nothing.
			fs, out := indexOf(tc.args, "-fs"), len(tc.args)-1
			if fs > out {
				t.Errorf("-fs is at %d, past the output at %d", fs, out)
			}
		})
	}
	// Zero means no cap, for a caller with no estimate. Every production path
	// has one; this is what keeps the flag out of the argv rather than emitting
	// "-fs 0", which FFmpeg reads as "stop immediately".
	if _, n := pairValue(normaliseArgs("in.mp4", "out.ts", 3, 3, 0), "-fs"); n != 0 {
		t.Error("-fs 0 was emitted, which would produce an empty derivative")
	}
}

// A run that HIT the cap is not published. -fs stops FFmpeg cleanly and exits
// zero, so without this the safety valve would quietly put a derivative on air
// that stops in the middle -- the exact "the playlist played half a file and
// stopped" report PartialSuffix exists to prevent, arriving by a new route.
func TestADerivativeThatReachedTheOutputCapIsNotPublished(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "clip-1a2b.mp4", "source bytes")
	p := newTestProcessor(t, dir,
		WithDurationProber(func(context.Context, string) (float64, float64, error) { return 2, 2, nil }),
		WithExecer(func(_ context.Context, cmd media.Command, _ media.Sink) error {
			// Write exactly as much as -fs allowed, which is what a run that hit
			// the limit looks like on disk.
			capStr, n := pairValue(cmd.Args, "-fs")
			if n != 1 {
				t.Fatalf("no -fs in the argv: %v", cmd.Args)
			}
			size, err := strconv.ParseInt(capStr, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return os.WriteFile(cmd.Args[len(cmd.Args)-1], make([]byte, size), 0o600)
		}))

	err := p.RunNormalise(context.Background(), normaliseJob(t, "clip-1a2b.mp4"), &recorder{})
	if err == nil {
		t.Fatal("a derivative that reached the output cap was published")
	}
	if !jobs.IsPermanent(err) {
		t.Errorf("want a permanent refusal, got %v", err)
	}
	if _, statErr := os.Stat(DerivativePath(dir, "clip-1a2b.mp4")); statErr == nil {
		t.Error("the over-cap derivative is on disk under its final name")
	}
	if _, statErr := os.Stat(DerivativePath(dir, "clip-1a2b.mp4") + PartialSuffix); statErr == nil {
		t.Error("the over-cap .partial was left behind for the next attempt to inherit")
	}
}

// A source whose duration cannot be established is refused rather than guessed
// at. The old fallback -- estimate the derivative from the SOURCE'S size -- is
// not a weaker bound, it is a measurement of the wrong thing, and it is the
// number the 15,517x amplification walked past.
func TestASourceWithNoKnowableDurationIsRefused(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "clip-1a2b.mp4", "source bytes")
	p := newTestProcessor(t, dir,
		WithSourceVerifier(func(context.Context, string) (float64, error) { return 0, nil }),
		WithExecer(func(context.Context, media.Command, media.Sink) error {
			t.Error("FFmpeg was started for a source of unknown length")
			return nil
		}))
	err := p.RunNormalise(context.Background(), normaliseJob(t, "clip-1a2b.mp4"), &recorder{})
	if err == nil {
		t.Fatal("a source of unknown length was normalised")
	}
	if !jobs.IsPermanent(err) {
		t.Errorf("want a permanent refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "how long") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
}

// The disk guard now works from the PROBED duration, so MaxDurationMS is no
// longer inert. Nothing populated a duration before -- the only production
// submitter deliberately leaves it at zero -- so the twenty-four hour bound
// could never be reached by anything.
func TestAnItemLongerThanTheMaximumIsRefused(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "clip-1a2b.mp4", "source bytes")
	p := newTestProcessor(t, dir,
		WithSourceVerifier(func(context.Context, string) (float64, error) {
			return float64(MaxDurationMS)/1000 + 1, nil
		}),
		WithExecer(func(context.Context, media.Command, media.Sink) error {
			t.Error("FFmpeg was started for an item past the duration limit")
			return nil
		}))
	err := p.RunNormalise(context.Background(), normaliseJob(t, "clip-1a2b.mp4"), &recorder{})
	if err == nil || !jobs.IsPermanent(err) {
		t.Fatalf("want a permanent refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "limit for a playlist item") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
}

// indexOf is the position of the first occurrence of s, or -1.
func indexOf(args []string, s string) int {
	for i, a := range args {
		if a == s {
			return i
		}
	}
	return -1
}
