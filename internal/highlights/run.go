package highlights

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
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
	pr, pw := io.Pipe()
	cmd.Stdout = pw

	parsed := make(chan error, 1)
	go func() {
		perr := stdout(pr)
		if perr != nil {
			// The parser has given up, so the rest of the pass is wasted work --
			// possibly hours of audio. Kill the child rather than read it to the
			// end. Its "killed" is not news.
			cancel()
		}
		// Drain whatever the parser left unread, so FFmpeg is never blocked on a
		// full pipe while Wait waits for it.
		_, _ = io.Copy(io.Discard, pr)
		parsed <- perr
	}()
	// ffmpeg.RunEnrolled owns the spawn: census, and WaitDelay so a cancelled
	// pass returns even when something else holds the pipe.
	runErr := ffmpeg.RunEnrolled(cmd, "highlights")
	_ = pw.Close()
	if perr := <-parsed; perr != nil {
		return perr
	}
	if runErr != nil {
		if why := lastLine(stderr.String()); why != "" {
			return fmt.Errorf("%w: %s", runErr, why)
		}
		return runErr
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
