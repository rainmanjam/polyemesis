package ffmpeg

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// FINDING B. A real, self-contained file in a format the allowlist does not
// name is refused with a sentence about ITSELF, not with the indirection one.
//
// ErrIndirectContainer was returned for EVERY format the allowlist did not
// name, and the handler renders it as "this file is a playlist or script naming
// other files, not media itself". Measured on files muxed for the purpose:
// AIFF, DV, y4m, IVF, CAF and GIF all got that sentence. Not one of them names
// another file. Two things were wrong with that at once -- an operator told
// something untrue about their own footage by the feature whose whole point is
// that this product stops asserting things it has not established, and a
// message that sends them looking for a script that does not exist.
//
// THE GATE IS UNCHANGED. Both sentinels are refusals; what this pins is which
// of the two a refusal gets, and that a format the allowlist has never heard of
// is still refused rather than let through for want of a name in
// indirectFormats.
func TestARealFileInAFormatWeDoNotTakeIsNotCalledAPlaylist(t *testing.T) {
	bins := needFFmpeg(t, "ffmpeg", "ffprobe")
	ffmpegBin, ffprobe := bins[0], bins[1]
	dir := t.TempDir()

	// Muxed with this repo's own FFmpeg, so each is genuinely self-contained
	// media rather than an assertion about a format name.
	cases := []struct {
		name  string
		input []string
		mux   []string
	}{
		{"sample.aiff",
			[]string{"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "1"},
			[]string{"-c:a", "pcm_s16be", "-f", "aiff"}},
		{"sample.y4m",
			[]string{"-f", "lavfi", "-i", "testsrc2=size=64x64:rate=5", "-t", "1"},
			[]string{"-f", "yuv4mpegpipe"}},
		{"sample.gif",
			[]string{"-f", "lavfi", "-i", "testsrc2=size=64x64:rate=5", "-t", "1"},
			[]string{"-f", "gif"}},
	}
	var exercised int
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(dir, tc.name)
			args := append([]string{"-hide_banner", "-loglevel", "error"}, tc.input...)
			args = append(args, tc.mux...)
			args = append(args, "-y", out)
			// NOT A SKIP, and deliberately so. A build that cannot mux this
			// format, or that puts it on the allowlist, has nothing to say
			// here -- but a t.Skip per case is a free pass three ways over, and
			// the aggregate assertion below is a stronger statement than three
			// skips could be: at least one format must have reached the branch
			// under test, or the whole test fails.
			if o, err := exec.Command(ffmpegBin, args...).CombinedOutput(); err != nil {
				t.Logf("this FFmpeg cannot mux %s (%v: %s); nothing to assert", tc.name, err, o)
				return
			}

			_, err := ProbeFile(context.Background(), ffprobe, out)
			if err == nil {
				t.Logf("%s is on the allowlist on this build; nothing to assert", tc.name)
				return
			}
			exercised++
			// Still refused: the allowlist is the gate and it still fails closed.
			if errors.Is(err, ErrIndirectContainer) {
				t.Fatalf("%s -- which names no other file -- was refused as an "+
					"indirection: %v", tc.name, err)
			}
			if !errors.Is(err, ErrUnsupportedContainer) {
				t.Fatalf("want ErrUnsupportedContainer, got %v", err)
			}
			if strings.Contains(err.Error(), "playlist") ||
				strings.Contains(err.Error(), "naming other files") {
				t.Errorf("the refusal still describes the file as a playlist: %v", err)
			}
		})
	}
	// THE ASSERTION THAT THE ASSERTIONS RAN. Every case above can skip itself,
	// and a version of this that skipped all three would print ok while
	// measuring nothing -- the dead-assertion shape this repository has had to
	// fix before.
	if exercised == 0 {
		t.Fatal("no format reached the unsupported-container branch, so this test " +
			"asserted nothing")
	}
	t.Logf("the unsupported-container branch was exercised for %d of %d formats",
		exercised, len(cases))

	// THE CONTROL. An ffconcat script still gets the indirection sentence, so
	// the split above is a split and not a blanket rename.
	buildSample(t, filepath.Join(dir, "victim.mp4"), "-t", "1")
	script := filepath.Join(dir, "innocent.mp4")
	if err := os.WriteFile(script, ffconcatScript("victim.mp4"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ProbeFile(context.Background(), ffprobe, script); !errors.Is(err, ErrIndirectContainer) {
		t.Fatalf("an ffconcat script no longer gets the indirection sentence: %v", err)
	}
}

// F6. ffprobe's stdout is bounded, and the bound is on the SIZE OF THE REPLY
// rather than on the size of the file.
//
// cmd.Output() gave stdout an uncapped bytes.Buffer and reserved its 32 KiB cap
// for stderr, which is backwards here: ffprobe's JSON scales with the STREAM
// COUNT. Measured on legitimate, allowlisted media -- a 121 KB Matroska with
// 300 audio tracks -- ffprobe printed 464 KB, 3.8x the container, and
// ProbeResult.Raw then kept a second copy of it in the same process. Raw is
// gone; this pins the cap.
func TestAProbeThatPrintsTooMuchIsRefusedRatherThanBuffered(t *testing.T) {
	if probeStdoutCap <= 0 {
		t.Fatal("probeStdoutCap is not a cap")
	}
	ffprobe := needFFmpeg(t, "ffmpeg", "ffprobe")[1]
	sample := buildSample(t, filepath.Join(t.TempDir(), "sample.mp4"), "-t", "1")

	// DRIVEN THROUGH THE REAL FUNCTION, against a real ffprobe reading real
	// media, with the cap lowered rather than the reply inflated. An earlier
	// version of this test exercised cappedBuffer alone and left ProbeFile's own
	// `if stdout.over` arm unpinned -- a mutation that deleted the check
	// survived it.
	if _, err := probeFile(context.Background(), ffprobe, sample, 32); err == nil {
		t.Fatal("a reply past the cap was parsed rather than refused")
	} else if !strings.Contains(err.Error(), "printed more than") {
		t.Fatalf("wrong refusal: %v", err)
	}
	// THE CONTROL: the same file at the real cap is read normally, so the
	// refusal above is the cap and not the file.
	if _, err := probeFile(context.Background(), ffprobe, sample, probeStdoutCap); err != nil {
		t.Fatalf("the same media at the real cap was refused: %v", err)
	}

	c := &cappedBuffer{max: 4}
	n, err := c.Write([]byte("abcdef"))
	if n != 6 || err != nil {
		t.Fatalf("Write returned (%d, %v); a short write would kill the child mid-JSON", n, err)
	}
	if got := c.buf.String(); got != "abcd" {
		t.Errorf("kept %q, want the first 4 bytes", got)
	}
	if !c.over {
		t.Error("the overflow was not recorded, so ProbeFile would parse truncated JSON")
	}
	// Under the cap nothing is flagged, or every ordinary probe would fail.
	c = &cappedBuffer{max: 16}
	c.Write([]byte("short"))
	if c.over || c.buf.String() != "short" {
		t.Errorf("a reply under the cap was altered: over=%v buf=%q", c.over, c.buf.String())
	}
}
