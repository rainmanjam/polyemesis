package highlights

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/childcensus"
	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// RunStreaming runs one FFmpeg child for a scan and hands its standard output
// to stdout AS IT ARRIVES -- the loudness pass is ten readings a second for as
// long as the recording, and streaming keeps memory flat.
//
// The child is created below normal priority where the OS needs telling
// (jobs.LowerPriority; the caller's nice wrapper does it elsewhere), and is in
// the process census for as long as it runs, like every other background
// child (#717): a pass over hours of audio could otherwise outlive a shutdown
// unreported. A failure carries FFmpeg's own last line of stderr. When the
// parser fails the child is killed, not read to the end.
func RunStreaming(ctx context.Context, name string, args []string, stdout func(io.Reader) error) error {
	var stderr bytes.Buffer
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	jobs.LowerPriority(cmd)
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	childcensus.Enrol(cmd.Process.Pid, filepath.Base(name), "highlights")
	defer childcensus.Discharge(cmd.Process.Pid)
	if perr := stdout(pipe); perr != nil {
		// The parser has given up, so the rest of the pass is wasted work --
		// possibly hours of audio. Kill the child rather than read it to the
		// end; Wait reaps it (and closes the pipe), and the deferred
		// Discharge takes it out of the census. Its "killed" is not news.
		cancel()
		_ = cmd.Wait()
		return perr
	}
	// Drain whatever the parser left unread, so FFmpeg is never blocked on a
	// full pipe while Wait waits for it.
	_, _ = io.Copy(io.Discard, pipe)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%w: %s", err, lastLine(stderr.String()))
	}
	return nil
}

// lastLine is the tail of FFmpeg's stderr, which is where it says why.
func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
