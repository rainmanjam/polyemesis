package playlistmedia

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/media"
	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// rawStreamUpload writes a raw H.264 elementary stream into the uploads
// directory and returns (ffmpeg, ffprobe, stored name).
//
// Raw, meaning `-f h264`: the Annex-B bitstream with no container around it, so
// there is nowhere in the file for a duration to be recorded. That is the
// property this gate used to refuse on.
func rawStreamUpload(t *testing.T, dir, name string) (string, string, string) {
	t.Helper()
	// testenv.FFmpegBinary rather than exec.LookPath and a skip of our own: it
	// is the one place that decides whether a missing binary is an environment
	// this suite tolerates or a CI job that must fail, and two more hand-rolled
	// copies of that decision is how the ratchet stops meaning anything.
	ffmpegBin := testenv.FFmpegBinary(t, "ffmpeg",
		"ffmpeg is not installed, so no raw elementary stream can be built")
	ffprobeBin := testenv.FFmpegBinary(t, "ffprobe",
		"ffprobe is not installed, so the gate has nothing to gate with")
	if err := os.MkdirAll(filepath.Join(dir, "uploads"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "uploads", name)
	mk := exec.Command(ffmpegBin, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-t", "2", "-f", "h264", "-y", out)
	if o, err := mk.CombinedOutput(); err != nil {
		t.Skipf("this FFmpeg cannot write a raw h264 stream (%v: %s)", err, o)
	}
	return ffmpegBin, ffprobeBin, name
}

// THE SECOND GATE COUNTS TOO, which is the half of #218 that an "it was
// accepted" test at the upload handler cannot see.
//
// There are two independent gates on this path and #118's guarantee is that
// they agree about the same file. The upload handler decides whether the bytes
// are stored; THIS one decides whether they can ever be normalised, and it runs
// on three routes that never passed the first -- an item the operator
// inherited, a file that predates verdicts, a file placed in the uploads
// directory by hand (see SourceVerifier). Teaching only the upload handler to
// count would have reproduced #118's failure with the roles swapped: accepted
// at the door, refused permanently at the worker, a playlist item that can
// never go on air.
//
// Driven through the REAL verifier -- New with no WithSourceVerifier -- because
// a stubbed one would be asserting that the stub says yes.
func TestTheNormaliseGateCountsARawElementaryStreamRatherThanRefusingIt(t *testing.T) {
	dir := t.TempDir()
	ffmpegBin, ffprobe, upload := rawStreamUpload(t, dir, "dump-1a2b.h264")

	var ran bool
	p := New(nil, Config{
		FFmpeg: ffmpegBin, FFprobe: ffprobe, DataDir: dir, Uploads: mustStore(t, dir),
	},
		WithFreeSpace(func(string) (uint64, error) { return 1 << 60, nil }),
		WithExecer(func(_ context.Context, cmd media.Command, _ media.Sink) error {
			ran = true
			return os.WriteFile(cmd.Args[len(cmd.Args)-1], []byte("normalised"), 0o600)
		}))

	err := p.RunNormalise(context.Background(), normaliseJob(t, upload), &recorder{})
	if err != nil {
		t.Fatalf("the normalise gate refused a raw elementary stream: %v\n"+
			"#218: a stream with no container to declare a length gets its length "+
			"COUNTED here, exactly as it does at the upload handler. If only one of "+
			"the two gates learned that, an operator's file is accepted at the door "+
			"and dead at the worker", err)
	}
	if !ran {
		t.Error("the source passed the gate but FFmpeg was never reached")
	}
	// The specific sentence that used to end this job, named so a regression is
	// unmistakable rather than merely a non-nil error.
	if err != nil && strings.Contains(err.Error(), "could not work out how long") {
		t.Error("the worker still cannot work out how long a raw stream is")
	}
}

// AND THE DURATION IT COUNTED IS THE ONE THE DISK GUARD GETS, differentially.
//
// verifySource's return value is not decoration: it is what estimateBytes turns
// into the free-space demand AND into buildNormalise's -fs hard cap on the
// writer. A gate that accepted the file and returned zero would satisfy the
// test above -- FFmpeg would still be reached -- and would hand the encoder an
// unbounded cap, which is the 15,517x amplification that path was reshaped to
// stop.
//
// THE DIFFERENTIAL IS THE FFMPEG BINARY, because that is the thing a caller can
// forget. ffmpeg.ProbeFile takes both binaries in one struct precisely so this
// gate cannot be updated without the other, and the control below is what makes
// that arrangement observable here: with the binary, a real length; without it,
// the refusal this file used to give unconditionally.
func TestTheCountedDurationReachesTheDiskGuardAndNotZero(t *testing.T) {
	dir := t.TempDir()
	ffmpegBin, ffprobe, upload := rawStreamUpload(t, dir, "dump-3c4d.h264")
	input := filepath.Join(dir, "uploads", upload)

	counting := New(nil, Config{
		FFmpeg: ffmpegBin, FFprobe: ffprobe, DataDir: dir, Uploads: mustStore(t, dir),
	})
	secs, err := counting.verifySource(context.Background(), input)
	if err != nil {
		t.Fatalf("verifySource refused a raw elementary stream: %v", err)
	}
	if math.Abs(secs-2) > 0.25 {
		t.Fatalf("verifySource returned %v seconds, want about 2. estimateBytes turns "+
			"this into the free-space demand and into the -fs cap on the encoder; "+
			"zero makes that bound unbounded", secs)
	}
	// It has to be a real bound, not merely non-zero.
	if _, bounded := estimateBytes(int64(secs*1000), 1<<20); !bounded {
		t.Error("the counted duration did not produce a bounded disk estimate")
	}

	// THE CONTROL. Without it, a verifySource that ignored its configuration
	// and always answered would pass the assertions above. Emptying FFmpeg is
	// the one change that should turn the count back into the refusal, and if
	// it does not, this gate is not reading the field the fix threads through.
	blind := New(nil, Config{
		FFmpeg: "", FFprobe: ffprobe, DataDir: dir, Uploads: mustStore(t, dir),
	})
	if _, err := blind.verifySource(context.Background(), input); err == nil {
		t.Error("with no ffmpeg configured the gate still produced a duration, so the " +
			"count is not what produced the one above")
	} else if !jobs.IsPermanent(err) {
		t.Errorf("a file whose length cannot be established is a fact about the FILE "+
			"and must be permanent, or the queue retries it forever: %v", err)
	}
}
