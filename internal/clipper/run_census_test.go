package clipper

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/childcensus"
)

// TestClipperHelperProcess is not a test. It is this binary re-executed as
// the child the clipper's runners start, so they are exercised on every
// platform without depending on an ffmpeg being installed.
func TestClipperHelperProcess(t *testing.T) {
	mode := ""
	for i, a := range os.Args {
		if a == "clipper-helper" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
		}
	}
	switch mode {
	case "slow":
		time.Sleep(400 * time.Millisecond)
		fmt.Println("cut done")
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "Invalid data found when processing input")
		os.Exit(1)
	case "libass":
		// A captioned encode failing late: a screenful of libass chatter
		// naming caption code points, then FFmpeg's reason.
		for i := 0; i < 40; i++ {
			fmt.Fprintf(os.Stderr, "[Parsed_subtitles_4 @ 0x1] fontselect: failed to find any fallback with glyph 0x%X for font: (Inter, 700, 0)\n", 0x4F60+i)
		}
		fmt.Fprintln(os.Stderr, "[Parsed_subtitles_4 @ 0x1] Error opening memory font 'LICENSE-Inter.txt'")
		fmt.Fprintln(os.Stderr, "[vost#0:0 @ 0x2] Unknown encoder 'h264_nonexistent264'")
		fmt.Fprintln(os.Stderr, "Error opening output files: Encoder not found")
		os.Exit(8)
	}
}

// watchCensus records the pid of the first clipper child the census shows
// under this test binary's name, until stop is closed.
func watchCensus(stop <-chan struct{}) *atomic.Int64 {
	var seen atomic.Int64
	go func() {
		for {
			for _, c := range childcensus.Live() {
				if c.Kind == "clipper" && c.Name == filepath.Base(os.Args[0]) {
					seen.CompareAndSwap(0, int64(c.PID))
				}
			}
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	return &seen
}

// THE CUT IS COUNTED. Every child the clipper starts -- the cut or vertical
// encode, the keyframe probe -- is in the process census while it runs, and
// out of it once it is done, at normal and at lowered priority alike.
//
// MUTATION: drop the Enrol in runChild. Observed: FAIL, never in the census.
// MUTATION: drop the Discharge. Observed: FAIL, still in the census.
func TestTheClippersChildrenAreInTheCensusWhileTheyRun(t *testing.T) {
	for name, run := range map[string]func(context.Context, string, []string) ([]byte, error){
		"runCommand":     runCommand,
		"RunLowPriority": RunLowPriority,
	} {
		t.Run(name, func(t *testing.T) {
			stop := make(chan struct{})
			seen := watchCensus(stop)
			out, err := run(context.Background(), os.Args[0], []string{"-test.run=^TestClipperHelperProcess$", "clipper-helper", "slow"})
			close(stop)
			if err != nil || !strings.Contains(string(out), "cut done") {
				t.Fatalf("run = %q, %v", out, err)
			}
			pid := int(seen.Load())
			if pid == 0 {
				t.Fatal("the clipper's child was never in the census while it ran")
			}
			for _, c := range childcensus.Live() {
				if c.PID == pid {
					t.Errorf("pid %d is still in the census after it exited", pid)
				}
			}
		})
	}
}

// A failed child says why, and one that cannot start is an error, not output.
func TestAFailedClipperChildCarriesItsOutput(t *testing.T) {
	_, err := RunLowPriority(context.Background(), os.Args[0], []string{"-test.run=^TestClipperHelperProcess$", "clipper-helper", "fail"})
	if err == nil || !strings.Contains(err.Error(), "Invalid data found") {
		t.Errorf("err = %v, want FFmpeg's complaint", err)
	}
	if _, err := runCommand(context.Background(), filepath.Join(t.TempDir(), "no-such-ffmpeg"), nil); err == nil {
		t.Error("a binary that does not exist reported success")
	}
}

// A FAILED ENCODE SAYS WHY, NOT WHAT WAS CAPTIONED. The child's error keeps
// FFmpeg's real failure -- the last thing it said, however much came before --
// and none of the caption renderer's lines, which name caption code points.
//
// MUTATION: the old head-cut output. Observed: FAIL, glyph lines and no
// "Encoder not found".
func TestAFailedEncodesErrorIsItsReasonWithoutCaptionChatter(t *testing.T) {
	_, err := RunLowPriority(context.Background(), os.Args[0], []string{"-test.run=^TestClipperHelperProcess$", "clipper-helper", "libass"})
	if err == nil {
		t.Fatal("a failed encode reported success")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Encoder not found") {
		t.Errorf("the error lost FFmpeg's reason: %q", msg)
	}
	for _, leak := range []string{"glyph", "0x4F60", "Parsed_subtitles", "LICENSE-Inter"} {
		if strings.Contains(msg, leak) {
			t.Errorf("the error carries %q: %q", leak, msg)
		}
	}
	// Nothing but the renderer's lines: the error says the renderer failed,
	// and still names nothing it said.
	only := "[Parsed_subtitles_4 @ 0x1] fontselect: failed to find any fallback with glyph 0x4F60 for font: (Inter, 700, 0)"
	if got := ffmpegFailure(only); strings.Contains(got, "0x4F60") || !strings.Contains(got, "caption renderer") {
		t.Errorf("an all-renderer failure reads %q", got)
	}
	long := strings.Repeat("Last message repeated\n", 100) + "Conversion failed!"
	if got := ffmpegFailure(long); len(got) > failureLimit+3 || !strings.HasSuffix(got, "Conversion failed!") {
		t.Errorf("a long failure is not its tail: %d bytes, %q", len(got), got[max(0, len(got)-40):])
	}
}
