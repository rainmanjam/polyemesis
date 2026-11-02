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
// unreported. A failure carries FFmpeg's own last line of stderr.
func RunStreaming(ctx context.Context, name string, args []string, stdout func(io.Reader) error) error {
	var stderr bytes.Buffer
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
	perr := stdout(pipe)
	// Drain whatever the parser left, so FFmpeg is never blocked on a full
	// pipe while Wait waits for it.
	_, _ = io.Copy(io.Discard, pipe)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%w: %s", err, lastLine(stderr.String()))
	}
	return perr
}

// lastLine is the tail of FFmpeg's stderr, which is where it says why.
func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
