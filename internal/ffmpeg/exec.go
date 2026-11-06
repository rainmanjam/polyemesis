package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/childcensus"
)

// Everything that runs a short-lived FFmpeg or ffprobe child to completion --
// the derived-media jobs, the playlist transcodes, the upload and recording
// probes -- goes through Exec or Output, and so owns the same four decisions:
// the child dies with its context, WaitDelay bounds a grandchild holding the
// pipes, the child is enrolled in the process census for as long as it runs,
// and it stays in this process's group (not a supervisor's own group), so an
// abrupt death of polyemesis takes it too. The argument preamble comes from
// CommonArgs and ProgressArgs.

// Command is one child process to run.
type Command struct {
	Name string
	Args []string
}

// String renders the command for a log line. Not shell-quoted, because nothing
// here ever reaches a shell — this is for a human reading a job log.
func (c Command) String() string {
	return c.Name + " " + strings.Join(c.Args, " ")
}

// Sink is where a running child's output goes. Both fields may be nil.
type Sink struct {
	// Progress receives one snapshot per -progress block, for commands built
	// with ProgressArgs. Commands without it never call this.
	Progress func(Progress)
	// Line receives each stderr line as FFmpeg writes it.
	Line func(string)
}

// Execer runs one child process to completion. It is a field on Processor
// rather than a direct call so the workers can be tested without FFmpeg, which
// is the only way the argument builders and the failure paths get exercised on
// a machine that has no media on it.
type Execer func(ctx context.Context, cmd Command, sink Sink) error

// KillGrace is how long a child gets between the kill signal and giving up on
// its pipes.
//
// exec.CommandContext already kills the process when ctx ends, but Wait blocks
// until the output pipes close, and a child that has spawned its own children —
// which x265 and SVT-AV1 both do — can leave those pipes open after the parent
// is gone. Without WaitDelay the worker would sit in Wait forever and the
// queue's cancellation would be a lie.
const KillGrace = 5 * time.Second

// maxStderrTail is how much of a failed command's output is quoted back in the
// error. Enough for the reason, bounded so a chatty encoder cannot make one
// error message the whole job log.
const maxStderrTail = 20

// Exec is the real Execer.
//
// It satisfies the Worker contract's first rule: when ctx is done the child is
// killed, and this returns rather than waiting on a process that is not going
// to exit. A cancellation that leaks an FFmpeg is a cancellation that still
// competes with the live stream.
func Exec(ctx context.Context, cmd Command, sink Sink) error {
	return execWithGrace(ctx, cmd, sink, KillGrace)
}

// execWithGrace is Exec with the grace passed in rather than read from a
// package variable, so the test that pins the timeout path can use a short one
// without a global another Exec could observe mid-change.
func execWithGrace(ctx context.Context, cmd Command, sink Sink, grace time.Duration) error {
	c := exec.CommandContext(ctx, cmd.Name, cmd.Args...)
	c.WaitDelay = grace

	stdout, err := c.StdoutPipe()
	if err != nil {
		return fmt.Errorf("%s: stdout pipe: %w", cmd.Name, err)
	}
	stderr, err := c.StderrPipe()
	if err != nil {
		return fmt.Errorf("%s: stderr pipe: %w", cmd.Name, err)
	}
	if err := c.Start(); err != nil {
		return fmt.Errorf("start %s: %w", cmd.Name, err)
	}
	// ENROLLED FOR AS LONG AS IT RUNS. #717. Before this, the census covered
	// supervisor children only, and a transcode that outlived the shutdown
	// produced exactly the silence #631 produced -- while the shutdown log
	// reported that nothing was wrong.
	//
	// Discharged where Wait returns, which is the only moment the pid is
	// genuinely gone; a child that ignores the kill is still very much a child.
	childcensus.Enrol(c.Process.Pid, cmd.Name, mediaKind)
	defer childcensus.Discharge(c.Process.Pid)

	var (
		mu   sync.Mutex
		tail []string
		wg   sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		if sink.Progress == nil {
			_, _ = io.Copy(io.Discard, stdout)
			return
		}
		_ = ParseProgress(stdout, sink.Progress)
	}()
	go func() {
		defer wg.Done()
		_ = scanLines(stderr, func(line string) {
			line = strings.TrimSpace(line)
			if line == "" {
				return
			}
			mu.Lock()
			tail = append(tail, line)
			if len(tail) > maxStderrTail {
				tail = tail[len(tail)-maxStderrTail:]
			}
			mu.Unlock()
			if sink.Line != nil {
				sink.Line(line)
			}
		})
	}()
	// DRAIN FIRST, BUT ON A LEASH. Both orderings of these two lines have now
	// been wrong, in opposite directions, and the bound is what reconciles them.
	//
	// wg.Wait() unconditionally first -- the original -- hangs forever when a
	// grandchild holds the pipes open, which KillGrace's own comment says x265
	// and SVT-AV1 both do: the readers never see EOF, so c.Wait is never reached
	// and WaitDelay, which only has effect INSIDE Wait, never applies. The
	// worker sits there and the queue's cancellation is a lie.
	//
	// c.Wait() first -- the fix for that -- LOSES OUTPUT, and Go says so
	// outright: "it is thus incorrect to call Wait before all reads from the
	// pipe have completed" (os/exec, StderrPipe). Wait closes the pipes, and
	// whatever the scanner had not read yet goes with them. The comment here
	// used to claim the opposite was safe. CI disproved it: a child that writes
	// 100 lines and exits was seen delivering 83, and the assertion that every
	// line reaches the job log failed on the 17 that did not.
	//
	// So: wait for the readers, but no longer than KillGrace. The normal case
	// drains completely, because the child has exited and closed its ends. The
	// grandchild case gives up on the tail after the same grace the kill gets,
	// falls through to Wait, and WaitDelay closes the pipes from under the
	// readers -- which is the outcome that ordering was reaching for, now
	// reached without paying for it on every ordinary run.
	drained := make(chan struct{})
	go func() {
		wg.Wait()
		close(drained)
	}()
	// If this times out the readers are still blocked on a pipe something else
	// is holding; c.Wait below, bounded by WaitDelay, is what frees them.
	_ = awaitReaders(drained, grace)

	err = c.Wait()

	// JOIN THE READERS BEFORE RETURNING. On the timeout path above they were
	// left running: Wait closes the pipes, which wakes them, but "wakes" is not
	// "has returned", so Exec came back with up to three goroutines (two
	// readers and the one waiting on them) still live, still holding the sink
	// callbacks, and on a long-lived worker that is one leaked trio per
	// timed-out command. Joining here also means the tail read below is
	// complete: a line the reader was mid-way through when the pipes closed
	// used to miss the error message.
	//
	// Bounded by the same grace. Wait has closed the pipes, so the only thing
	// that can still hold a reader is a Sink callback that does not return, and
	// a worker that hangs forever behind a stuck callback is worse than the
	// goroutine it would be waiting for.
	_ = awaitReaders(drained, grace)

	if err != nil {
		mu.Lock()
		reason := strings.Join(tail, "; ")
		mu.Unlock()
		// The context's own error wins the explanation: "signal: killed" tells
		// an operator nothing, while "cancelled" tells them they pressed the
		// button.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%s: %w", cmd.Name, ctxErr)
		}
		if reason != "" {
			return fmt.Errorf("%s failed: %w: %s", cmd.Name, err, reason)
		}
		return fmt.Errorf("%s failed: %w", cmd.Name, err)
	}
	return nil
}

// awaitReaders reports whether the reader goroutines finished within grace.
func awaitReaders(drained <-chan struct{}, grace time.Duration) bool {
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-drained:
		return true
	case <-t.C:
		return false
	}
}

// Publish renames a finished .partial file into place.
//
// Nothing that runs through Exec writes to a final path directly, so a
// half-written derivative can never be served, measured by a verifier or read
// as a finished one. That is also why CommonArgs(..., true) passes -y: the
// partial a killed process left behind must not make the retry hang on FFmpeg's
// overwrite prompt. Same convention as clips.Capture.
func Publish(partial, final string) error {
	if err := os.Rename(partial, final); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("publish %s: %w", filepath.Base(final), err)
	}
	return nil
}

// Output runs a command to completion and returns its stdout and its stderr,
// for ffprobe: a probe's answer IS its stdout, where an encode's stdout is a
// progress stream nobody keeps. Enrolled in the census like Exec's children, and
// bounded by KillGrace the same way, so a probe that spawned something holding
// its pipes cannot make a cancelled caller wait for ever.
func Output(ctx context.Context, cmd Command) ([]byte, string, error) {
	c := exec.CommandContext(ctx, cmd.Name, cmd.Args...)
	c.WaitDelay = KillGrace
	var stdout bytes.Buffer
	stderr := &tailBuffer{max: probeStderrCap}
	c.Stdout, c.Stderr = &stdout, stderr
	err := RunEnrolled(c, probeKind)
	return stdout.Bytes(), stderr.String(), err
}

// probeKind is the census kind of an ffprobe run, mediaKind of an Exec child.
const (
	probeKind = "probe"
	mediaKind = "media"
)

// RunEnrolled starts c, enrols it in the process census for as long as it runs
// and waits for it. For the callers that need their own pipes (capped buffers,
// say) and so cannot use Output, which is every other way a probe is run.
//
// Discharged where Wait returns, which is the only moment the pid is genuinely
// gone; a child that ignores the kill is still very much a child.
func RunEnrolled(c *exec.Cmd, kind string) error {
	if err := c.Start(); err != nil {
		return err
	}
	childcensus.Enrol(c.Process.Pid, c.Args[0], kind)
	defer childcensus.Discharge(c.Process.Pid)
	return c.Wait()
}

// tailBuffer keeps the last max bytes written to it. A probe's stderr is quoted
// back in an error, and the reason a probe failed is at the end of it, so it is
// the tail that is kept; it never errors, so the child is never killed by a
// short write.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }
