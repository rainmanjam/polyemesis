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
