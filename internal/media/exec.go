package media

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

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
	// with progressArgs. Commands without it never call this.
	Progress func(ffmpeg.Progress)
	// Line receives each stderr line as FFmpeg writes it.
	Line func(string)
}

// Execer runs one child process to completion. It is a field on Processor
// rather than a direct call so the workers can be tested without FFmpeg, which
// is the only way the argument builders and the failure paths get exercised on
// a machine that has no media on it.
type Execer func(ctx context.Context, cmd Command, sink Sink) error

// killGrace is how long a child gets between the kill signal and giving up on
// its pipes.
//
// exec.CommandContext already kills the process when ctx ends, but Wait blocks
// until the output pipes close, and a child that has spawned its own children —
// which x265 and SVT-AV1 both do — can leave those pipes open after the parent
// is gone. Without WaitDelay the worker would sit in Wait forever and the
// queue's cancellation would be a lie.
const killGrace = 5 * time.Second

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
	c := exec.CommandContext(ctx, cmd.Name, cmd.Args...)
	c.WaitDelay = killGrace

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
		_ = ffmpeg.ParseProgress(stdout, sink.Progress)
	}()
	go func() {
		defer wg.Done()
		scanLines(stderr, func(line string) {
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
	// grandchild holds the pipes open, which killGrace's own comment says x265
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
	// So: wait for the readers, but no longer than killGrace. The normal case
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
	select {
	case <-drained:
	case <-time.After(killGrace):
		// The readers are still blocked on a pipe something else is holding.
		// c.Wait below, bounded by WaitDelay, is what frees them.
	}

	err = c.Wait()

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

// output runs a command and returns its stdout, for ffprobe. Separate from Exec
// because a probe's answer IS its stdout, where an encode's stdout is a
// progress stream nobody keeps.
func output(ctx context.Context, cmd Command) ([]byte, string, error) {
	c := exec.CommandContext(ctx, cmd.Name, cmd.Args...)
	c.WaitDelay = killGrace
	var stderr strings.Builder
	c.Stderr = &stderr
	out, err := c.Output()
	return out, stderr.String(), err
}

// scanLines reads r line by line with a buffer large enough for FFmpeg's
// longest diagnostics, which run well past bufio's default 64 KiB when a filter
// graph is quoted back at you.
func scanLines(r io.Reader, fn func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		fn(sc.Text())
	}
}
