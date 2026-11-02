package highlights

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/childcensus"
)

// TestHighlightsHelperProcess is not a test. It is this binary re-executed as
// the child RunStreaming starts, so the runner is exercised on every platform
// without depending on an ffmpeg being installed.
func TestHighlightsHelperProcess(t *testing.T) {
	mode := ""
	for i, a := range os.Args {
		if a == "highlights-helper" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
		}
	}
	switch mode {
	case "readings":
		fmt.Println("reading 1")
		fmt.Println("reading 2")
		os.Exit(0)
	case "forever":
		// A bad reading, then output without end -- bounded only so a
		// runner that never kills it cannot leave it behind for good.
		fmt.Println("not a reading")
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			fmt.Println("reading 0.0")
		}
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "Input #0, wav")
		fmt.Fprintln(os.Stderr, "no such stream")
		os.Exit(1)
	}
}

func helperArgs(mode string) []string {
	return []string{"-test.run=^TestHighlightsHelperProcess$", "highlights-helper", mode}
}

func censusKind(kind string) (int, bool) {
	for _, c := range childcensus.Live() {
		if c.Kind == kind && c.Name == filepath.Base(os.Args[0]) {
			return c.PID, true
		}
	}
	return 0, false
}

func liveAt(pid int) bool {
	for _, c := range childcensus.Live() {
		if c.PID == pid {
			return true
		}
	}
	return false
}

// THE LOUDNESS PASS IS COUNTED. While the scan's FFmpeg runs it is in the
// process census, under its own name and kind "highlights", so a shutdown that
// leaves it behind is reported; once it is done it is gone from the census.
//
// MUTATION: drop the Enrol. Observed: FAIL, "not in the census while running".
// MUTATION: drop the Discharge. Observed: FAIL, "still in the census".
func TestTheLoudnessPassIsInTheCensusWhileItRuns(t *testing.T) {
	var pid int
	var lines []string
	err := RunStreaming(context.Background(), os.Args[0], helperArgs("readings"), func(r io.Reader) error {
		p, ok := censusKind("highlights")
		if !ok {
			t.Error("the scan's child is not in the census while running")
		}
		pid = p
		b, err := io.ReadAll(r)
		lines = strings.Fields(string(b))
		return err
	})
	if err != nil {
		t.Fatalf("RunStreaming: %v", err)
	}
	if len(lines) != 4 {
		t.Errorf("stdout was not streamed to the parser: %q", lines)
	}
	if pid != 0 && liveAt(pid) {
		t.Errorf("pid %d is still in the census after the pass ended", pid)
	}
}

// A failed pass says why in FFmpeg's own words -- the last line of stderr --
// and a parser error on a clean exit is not swallowed.
func TestAFailedLoudnessPassCarriesFFmpegsLastLine(t *testing.T) {
	err := RunStreaming(context.Background(), os.Args[0], helperArgs("fail"), func(io.Reader) error { return nil })
	if err == nil || !strings.HasSuffix(err.Error(), "no such stream") {
		t.Errorf("err = %v, want it to end with FFmpeg's last line", err)
	}
	parse := errors.New("bad reading")
	err = RunStreaming(context.Background(), os.Args[0], helperArgs("readings"), func(io.Reader) error { return parse })
	if !errors.Is(err, parse) {
		t.Errorf("err = %v, want the parser's error", err)
	}
	if err := RunStreaming(context.Background(), filepath.Join(t.TempDir(), "no-such-ffmpeg"), nil, func(io.Reader) error { return nil }); err == nil {
		t.Error("a binary that does not exist reported success")
	}
}

// A PASS THE PARSER GIVES UP ON IS STOPPED, NOT READ TO THE END. When the
// parser fails, the child is killed and reaped and leaves the census at once,
// instead of the runner draining what could be hours of audio decoded for
// nothing. The helper writes a bad line and then output for 30 s.
//
// MUTATION: drain the pipe after a parser error instead of cancelling.
// Observed: FAIL, still running after 10 s.
func TestAPassTheParserRejectsIsKilledNotDrained(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel) // reaps the helper if the runner under test does not
	parse := errors.New("not a reading")
	var pid int
	done := make(chan error, 1)
	go func() {
		done <- RunStreaming(ctx, os.Args[0], helperArgs("forever"), func(io.Reader) error {
			pid, _ = censusKind("highlights")
			return parse
		})
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the runner was still reading the child 10 s after the parser gave up")
	}
	if !errors.Is(err, parse) {
		t.Errorf("err = %v, want the parser's error", err)
	}
	if pid == 0 {
		t.Fatal("the child was never in the census")
	}
	if liveAt(pid) {
		t.Errorf("pid %d is still in the census after the runner returned", pid)
	}
	// Reaped, not merely signalled: a zombie still answers signal 0. Windows
	// has no signal 0; there a returned Wait is the evidence.
	if runtime.GOOS != "windows" {
		if p, ferr := os.FindProcess(pid); ferr == nil && p.Signal(syscall.Signal(0)) == nil {
			t.Errorf("pid %d still exists after the runner returned", pid)
		}
	}
}
