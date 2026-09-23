package supervisor

import (
	"bufio"
	"slices"
	"strings"
	"testing"
)

// The two runs are each just past the line limit on their own stream -- the
// 512 KiB the stderr drain allows a line, and the 1 MiB the -progress parser
// allows one on stdout -- plus more than any OS pipe buffer, so a reader that
// gives up leaves the child blocked. No bigger than that: every stderr byte is
// run through the secret scrubber, which under -race costs seconds per MiB.
const (
	stderrFloodBytes = 768 << 10
	stdoutFloodBytes = 1<<20 + 256<<10
)

// floodExitBound is how long a flooding child may take to be reaped. Draining
// a MiB takes milliseconds; the bound is generous for -race and a slow Windows
// spawn, and it is still far short of forever, which is what the unfixed
// drain waited.
const floodExitBound = waitTimeout

// A child whose stderr holds more than a line buffer's worth with no newline
// must still be reaped. The drain used to be a ScanLines scanner capped at
// 512 KiB: past that, Scan returned false with ErrTooLong, the goroutine
// returned, nothing read stderr again, the child blocked in write() once the
// pipe filled, and cmd.Wait() never returned -- a process reported Running
// that was doing nothing, for ever. FFmpeg's \r-terminated stats line at a
// raised -loglevel is exactly this run.
func TestStderrRunWithNoNewlineDoesNotWedgeTheChild(t *testing.T) {
	const after = "fake child: stderr is still being read"
	rec := newRecorder()
	p := testProcess(t, fakeFlood("stderr", stderrFloodBytes, after), Spec{OnState: rec.onState})
	p.Start()

	waitForBounded(t, floodExitBound, "the flooding child to be reaped", func() bool {
		return rec.saw(StateStopped)
	})

	// Reaped is half of it; the other half is that the log is still a log. The
	// line written after the run must have been captured as its own line.
	var texts []string
	for _, l := range p.Logs() {
		texts = append(texts, l.Text)
	}
	if !slices.Contains(texts, after) {
		t.Errorf("the line written after the %d-byte run was not captured on its own; "+
			"the drain stopped parsing stderr", stderrFloodBytes)
	}
	for _, s := range texts {
		if len(s) > stderrLineMax {
			t.Errorf("a captured line is %d bytes, over the %d-byte cap", len(s), stderrLineMax)
		}
	}
}

// FFmpeg ends each interactive stats update in a bare \r. Split only on \n,
// a whole session of them is one line; split on both, each update is its own.
func TestScanLogLinesSplitsOnCarriageReturnToo(t *testing.T) {
	in := "frame=1 fps=30\rframe=2 fps=30\rError opening output\r\nlast\n"
	sc := bufio.NewScanner(strings.NewReader(in))
	sc.Split(scanLogLines)
	var got []string
	for sc.Scan() {
		got = append(got, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	// "\r\n" yields an empty token between its halves; runOnce skips empties.
	want := []string{"frame=1 fps=30", "frame=2 fps=30", "Error opening output", "", "last"}
	if !slices.Equal(got, want) {
		t.Errorf("tokens = %q, want %q", got, want)
	}
}

// A run longer than the cap comes out in cap-sized pieces rather than as an
// error, and a final fragment with no terminator is still delivered at EOF.
func TestScanLogLinesCutsAnOverlongRunInsteadOfFailing(t *testing.T) {
	in := strings.Repeat("y", stderrLineMax*2+10)
	sc := bufio.NewScanner(strings.NewReader(in))
	sc.Buffer(make([]byte, 0, 64*1024), stderrLineMax)
	sc.Split(scanLogLines)
	var lens []int
	for sc.Scan() {
		lens = append(lens, len(sc.Text()))
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v (an overlong run must be cut, not refused)", err)
	}
	if want := []int{stderrLineMax, stderrLineMax, 10}; !slices.Equal(lens, want) {
		t.Errorf("piece lengths = %v, want %v", lens, want)
	}
}

// The same for stdout. The default handler is the -progress parser, which gives
// up on a line over 1 MiB; a custom handler can return for reasons of its own.
// Either way the child must not be left blocked writing into a pipe nobody reads.
func TestStdoutHandlerThatGivesUpDoesNotWedgeTheChild(t *testing.T) {
	rec := newRecorder()
	p := testProcess(t, fakeFlood("stdout", stdoutFloodBytes, "done"), Spec{OnState: rec.onState})
	p.Start()

	waitForBounded(t, floodExitBound, "the flooding child to be reaped", func() bool {
		return rec.saw(StateStopped)
	})
}
