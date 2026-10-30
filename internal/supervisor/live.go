package supervisor

import (
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"time"
)

// Live commands: changing a running child's filter level without restarting it.
//
// FFmpeg reads interactive commands from stdin once the child is started
// without -nostdin. A command is written as `c<target> -1 volume <level>\n`,
// and FFmpeg answers on stderr with `Command reply for stream N: ret:R`. ret:0
// is the only success. Anything else, or no reply, makes the caller restart.
//
// The contract, in full:
//   - One writer. Only (*liveRun).send writes to the pipe, and only the lines
//     volumeLine builds. TestOnlyTheCommandWriterTouchesTheStdinPipe enforces it.
//   - One command in flight per process. liveWriteMu serialises callers, so a
//     reply can only be matched to the command that is waiting for it.
//   - A run is poisoned by a timeout. After one, its replies can no longer be
//     trusted to belong to the next command, so the run accepts no more and the
//     caller must restart it.
//   - A new run gets a new liveRun, so a reply from a previous run cannot reach
//     the current one. The scanner delivers to its own run; only SetVolume and
//     clearLive read the Process's current run
//     (TestOnlySetVolumeAndClearLiveReadTheCurrentRun).
//   - A reply counts only as a whole, anchored line from filter graph 0. A reply
//     from another graph makes the outcome ambiguous and poisons the run.

// liveReplyTimeout bounds the wait for FFmpeg's reply. FFmpeg's main loop polls
// stdin for a key at most every 100 ms (check_keyboard_interaction), reads the
// rest of a `c` line at once, and hands the command to the filter graph's
// thread, which prints the reply when it next runs. Measured on FFmpeg 9.0.2
// the reply arrives 11 to 71 ms after the write. Two seconds is roughly twenty
// key polls and about thirty times the worst observed latency: long enough
// that a loaded machine is not mistaken for a refusal, short enough that a
// stuck command does not hold the destination's control path for long.
var liveReplyTimeout = 2 * time.Second

var (
	// errLiveUnavailable: this process has no command channel. It did not opt
	// in, it is not running, its run was poisoned, or the platform has no
	// verified path.
	errLiveUnavailable = errors.New("live commands are not available for this process")
	// errLiveTimeout: FFmpeg did not reply inside liveReplyTimeout.
	errLiveTimeout = errors.New("ffmpeg did not acknowledge the command in time")
)

// liveReplyRE matches FFmpeg's reply as the WHOLE stderr line. Anchored at both
// ends: stderr can echo text an operator or a stream controls (metadata, titles),
// and a line that merely contains the words is not an acknowledgement.
var liveReplyRE = regexp.MustCompile(`^Command reply for stream (\d+): ret:(-?\d+)(?: res:.*)?$`)

// liveGraphIndex is the filter graph a destination's volume stages live in.
// FFmpeg numbers a reply by graph, not by output stream, and a destination has
// one graph, its -filter_complex, which FFmpeg creates first: index 0. (Video
// is stream-copied, so no implicit simple graph exists for it.) A reply from
// any other index means a second graph answered the same command, and which
// answer is ours is then ambiguous.
const liveGraphIndex = 0

// liveReply is one parsed reply, as the scanner hands it to the waiter.
type liveReply struct {
	stream, ret int
}

// parseReply reads one stderr line. It reports the stream index and return code
// of a genuine reply, and ok=false for any other line.
func parseReply(line string) (stream, ret int, ok bool) {
	m := liveReplyRE.FindStringSubmatch(line)
	if m == nil {
		return 0, 0, false
	}
	stream, err1 := strconv.Atoi(m[1])
	ret, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return stream, ret, true
}

// liveInstanceRE is the only target a command may name: a volume filter
// instance. The rest of the grammar is fixed, so there is nothing else to name.
var liveInstanceRE = regexp.MustCompile(`^volume@[A-Za-z0-9_]+$`)

// maxLiveTarget is the longest target FFmpeg reads whole: it scans the target
// with %63[^ ], so a longer one would be cut and its tail read as the time.
const maxLiveTarget = 63

// maxLiveLevel bounds a volume level. The profile validator caps per-cell gain
// well below this; the bound here only refuses a value no profile could have.
const maxLiveLevel = 100

// volumeLine builds the one command line a volume change is allowed to be. It
// is the only function that produces command text, and it refuses anything that
// is not a volume instance and a finite level in range. A newline in the name
// would start a second command, and a stray `q` quits FFmpeg, so validation
// comes before any byte is written.
func volumeLine(instance string, level float64) (string, error) {
	if !liveInstanceRE.MatchString(instance) {
		return "", fmt.Errorf("live command target %q is not a volume instance", instance)
	}
	if len(instance) > maxLiveTarget {
		return "", fmt.Errorf("live command target %q is longer than FFmpeg reads (%d bytes)", instance, maxLiveTarget)
	}
	if math.IsNaN(level) || math.IsInf(level, 0) {
		return "", fmt.Errorf("live command level %v is not finite", level)
	}
	if level < 0 || level > maxLiveLevel {
		return "", fmt.Errorf("live command level %v is outside [0, %d]", level, maxLiveLevel)
	}
	return instance + " -1 volume " + strconv.FormatFloat(level, 'f', -1, 64) + "\n", nil
}

// liveRun is the command channel of one spawn of a process.
type liveRun struct {
	stdin io.WriteCloser
	// replies carries the replies the stderr scanner dispatches. Buffered and
	// filled without blocking, so a reply nobody is waiting for can never stall
	// the scanner, which is also the reader that keeps FFmpeg's stderr moving.
	replies chan liveReply
	// exited is closed when the child is reaped, so a waiter does not sit out the
	// full timeout for a process that has already gone.
	exited chan struct{}
	// poisoned is set by a timeout or a write failure. Accessed only under the
	// owning Process's liveWriteMu.
	poisoned bool
}

// send writes one command and waits for its reply. It is the ONLY method that
// writes to the pipe, and the caller must hold the process's liveWriteMu. It
// returns the ret of graph 0's reply; a reply from any other graph poisons the
// run and is an error (see liveGraphIndex).
func (r *liveRun) send(line string) (int, error) {
	if r.poisoned {
		return 0, errLiveUnavailable
	}
	// Any reply still queued belongs to a command that was already answered or
	// has timed out. Discard it so it cannot be read as this command's answer.
	for drained := false; !drained; {
		select {
		case <-r.replies:
		default:
			drained = true
		}
	}
	// The command goes out as ONE short write, so FFmpeg's key reader sees the
	// whole line at once and the write cannot be half-delivered.
	if d, ok := r.stdin.(interface{ SetWriteDeadline(time.Time) error }); ok {
		_ = d.SetWriteDeadline(time.Now().Add(liveReplyTimeout))
	}
	if _, err := io.WriteString(r.stdin, "c"+line); err != nil {
		r.poisoned = true
		return 0, fmt.Errorf("write live command: %w", err)
	}
	timer := time.NewTimer(liveReplyTimeout)
	defer timer.Stop()
	select {
	case rep := <-r.replies:
		if rep.stream != liveGraphIndex {
			r.poisoned = true
			return 0, fmt.Errorf("ffmpeg answered from filter graph %d, not %d: more than one graph took the command, so its outcome is ambiguous", rep.stream, liveGraphIndex)
		}
		return rep.ret, nil
	case <-r.exited:
		r.poisoned = true
		return 0, errLiveUnavailable
	case <-timer.C:
		r.poisoned = true
		return 0, errLiveTimeout
	}
}

// SetVolume sets the level of a named volume filter in the RUNNING child. It
// returns nil only when FFmpeg replied ret:0. Every other outcome is an error,
// and the caller is expected to restart the process so the level is applied
// by the next spawn instead. The stored profile is what a restart rebuilds
// from, so a restart never brings back a stale level.
func (p *Process) SetVolume(instance string, level float64) error {
	if p == nil {
		return errLiveUnavailable
	}
	line, err := volumeLine(instance, level)
	if err != nil {
		return err
	}
	p.liveWriteMu.Lock()
	defer p.liveWriteMu.Unlock()

	p.liveMu.Lock()
	run := p.live
	p.liveMu.Unlock()
	if run == nil {
		return errLiveUnavailable
	}
	ret, err := run.send(line)
	if err != nil {
		return err
	}
	if ret != 0 {
		return fmt.Errorf("ffmpeg rejected %s at level %v: ret %d", instance, level, ret)
	}
	return nil
}

// deliverReply routes a reply line from ONE spawn's stderr scanner to THAT
// spawn's run. The run is bound by the scanner that reads it, not looked up on
// the Process, so a reply from an earlier spawn cannot reach a later run's
// waiter. It never blocks: the scanner is the only reader of stderr.
func deliverReply(run *liveRun, line string) {
	if run == nil {
		return
	}
	stream, ret, ok := parseReply(line)
	if !ok {
		return
	}
	select {
	case run.replies <- liveReply{stream: stream, ret: ret}:
	default:
	}
}

// clearLive detaches run's command channel at its exit, but only if it is still
// the current one. Done before exited is closed, so a caller that arrives after
// the reap is refused rather than writing to a closed pipe.
func (p *Process) clearLive(run *liveRun) {
	p.liveMu.Lock()
	if p.live == run {
		p.live = nil
	}
	p.liveMu.Unlock()
}
