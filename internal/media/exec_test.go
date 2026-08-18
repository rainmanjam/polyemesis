package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// TestHelperProcess is not a test. It is this binary re-executed as the child
// Exec runs, which is how the cancellation contract gets exercised on every
// platform this product ships to without depending on a `sleep` being there.
func TestHelperProcess(t *testing.T) {
	mode := ""
	for i, a := range os.Args {
		if a == "media-helper" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
		}
	}
	if mode == "" {
		t.Skip("not the helper process")
	}

	switch mode {
	case "sleep":
		// Long enough that a test which failed to kill it would time out
		// rather than pass by luck.
		time.Sleep(60 * time.Second)
	case "fail":
		fmt.Fprintln(os.Stderr, "first line of trouble")
		fmt.Fprintln(os.Stderr, "and the real reason")
		os.Exit(3)
	case "progress":
		fmt.Println("frame=25")
		fmt.Println("out_time_ms=1000000")
		fmt.Println("progress=continue")
		fmt.Println("out_time_ms=2000000")
		fmt.Println("progress=end")
		fmt.Fprintln(os.Stderr, "a warning nobody minds")
	case "grandchild":
		// Spawns a copy of this binary that INHERITS the pipes and outlives it,
		// then blocks. That is the shape killGrace was written for: x265 and
		// SVT-AV1 fork their own workers, and killing the parent leaves those
		// workers holding the output pipes open.
		//
		// A #!/bin/sh one-liner would say the same thing in one line and cost a
		// Windows skip; skips.json records that trade being made and then paid
		// back twice, so it is not made again here.
		g := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "media-helper", "sleep")
		g.Stdout, g.Stderr = os.Stdout, os.Stderr
		if err := g.Start(); err != nil {
			os.Exit(2)
		}
		fmt.Println("grandchild started")
		time.Sleep(60 * time.Second)
	case "chatty":
		for i := 0; i < 100; i++ {
			fmt.Fprintf(os.Stderr, "line %d\n", i)
		}
		os.Exit(1)
	}
	os.Exit(0)
}

// The mode is a POSITIONAL argument, not a flag. Go's flag package stops
// parsing at the first non-flag argument, so the test binary leaves it alone
// instead of rejecting it as unknown and printing its usage.
func helper(mode string) Command {
	return Command{Name: os.Args[0], Args: []string{"-test.run=^TestHelperProcess$", "media-helper", mode}}
}

// The Worker contract's first rule: return promptly when ctx is done, and kill
// the child before doing it. A cancellation that leaks an FFmpeg is a
// cancellation that still competes with the live stream.
func TestExecReturnsPromptlyWhenTheContextEndsAndTakesTheChildWithIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Exec(ctx, helper("sleep"), Sink{}) }()

	// Give the child a moment to actually be running before killing it.
	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Exec = %v, want a context.Canceled the operator can read", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Exec did not return after its context was cancelled")
	}
}

func TestExecQuotesTheStderrTailBackWhenTheChildFails(t *testing.T) {
	err := Exec(context.Background(), helper("fail"), Sink{})
	if err == nil {
		t.Fatal("Exec hid a non-zero exit")
	}
	if !strings.Contains(err.Error(), "and the real reason") {
		t.Fatalf("error %q does not carry the child's own explanation", err)
	}
}

func TestExecBoundsHowMuchOfAChattyChildEndsUpInOneErrorMessage(t *testing.T) {
	var lines []string
	err := Exec(context.Background(), helper("chatty"), Sink{Line: func(l string) { lines = append(lines, l) }})
	if err == nil {
		t.Fatal("Exec hid a non-zero exit")
	}
	// Every line reaches the job log; only the error message is bounded.
	if len(lines) != 100 {
		t.Fatalf("the sink saw %d lines, want 100", len(lines))
	}
	if strings.Count(err.Error(), "line ") > maxStderrTail {
		t.Fatalf("the whole transcript ended up in one error: %q", err)
	}
	if !strings.Contains(err.Error(), "line 99") {
		t.Fatalf("the error kept the wrong end of the log: %q", err)
	}
}

// stdout is the machine-readable progress stream and stderr is the human log.
// Merging them would turn "speed= 654x" into a decode error, which is exactly
// what a verifier must not see.
func TestExecKeepsProgressAndTheHumanLogApart(t *testing.T) {
	var (
		snapshots []ffmpeg.Progress
		lines     []string
	)
	err := Exec(context.Background(), helper("progress"), Sink{
		Progress: func(p ffmpeg.Progress) { snapshots = append(snapshots, p) },
		Line:     func(l string) { lines = append(lines, l) },
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("got %d progress blocks, want 2", len(snapshots))
	}
	if snapshots[0].OutTimeMS != 1000 || snapshots[1].OutTimeMS != 2000 {
		t.Fatalf("progress = %+v", snapshots)
	}
	if !snapshots[1].Done {
		t.Fatal("the terminating block was not flagged done")
	}
	if len(lines) != 1 || lines[0] != "a warning nobody minds" {
		t.Fatalf("stderr lines = %v", lines)
	}
}

func TestExecIsFineWithASinkThatWantsNothing(t *testing.T) {
	if err := Exec(context.Background(), helper("progress"), Sink{}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
}

func TestExecNamesTheBinaryItCouldNotStart(t *testing.T) {
	err := Exec(context.Background(), Command{Name: "definitely-not-a-real-binary-xyz"}, Sink{})
	if err == nil {
		t.Fatal("Exec started a binary that does not exist")
	}
	if !strings.Contains(err.Error(), "definitely-not-a-real-binary-xyz") {
		t.Fatalf("error %q does not name the binary", err)
	}
}

func TestCommandStringIsReadableInAJobLog(t *testing.T) {
	got := Command{Name: "/usr/bin/ffmpeg", Args: []string{"-i", "in.mkv", "out.mp4"}}.String()
	if got != "/usr/bin/ffmpeg -i in.mkv out.mp4" {
		t.Fatalf("String = %q", got)
	}
}
