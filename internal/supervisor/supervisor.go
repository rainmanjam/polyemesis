// Package supervisor owns the lifecycle of every FFmpeg child process.
//
// One goroutine per process: it spawns the child in its own process group,
// drains stderr into a ring buffer, parses -progress from stdout, and restarts
// with exponential backoff when the child dies. Nothing else in polyemesis
// calls exec.Command, so "how do processes behave" has exactly one answer.
package supervisor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// State is a process's lifecycle state. These map directly onto the UI's
// colour language: running is green, reconnecting is amber, failed is red.
type State string

const (
	StateStopped      State = "stopped"
	StateStarting     State = "starting"
	StateRunning      State = "running"
	StateReconnecting State = "reconnecting"
	StateFailed       State = "failed"
)

const (
	defaultMinBackoff = time.Second
	defaultMaxBackoff = 30 * time.Second
	// stableAfter is how long a process must survive before we treat it as
	// healthy and reset the backoff. Without this, a process that reconnects
	// every few minutes would eventually crawl to the 30s ceiling and stay
	// there for the rest of the session.
	stableAfter = 60 * time.Second
	// shutdownGrace is how long a child gets to exit after SIGTERM before it
	// is killed. FFmpeg uses this window to flush and finalise its output,
	// which is what keeps a recording playable.
	shutdownGrace = 8 * time.Second
	logRingSize   = 400
)

// LogLine is one captured stderr line.
type LogLine struct {
	Time    time.Time `json:"time"`
	Process string    `json:"process"`
	Text    string    `json:"text"`
	// Level is parsed from FFmpeg's output so the UI can colour errors.
	Level string `json:"level"`
}

// Spec describes a process to supervise.
type Spec struct {
	// Name is the stable identifier used in the API and logs,
	// e.g. "ingest", "dest:3", "recorder".
	Name string
	// Kind groups processes in the UI: ingest | destination | recorder |
	// preview | meters.
	Kind string
	Bin  string
	Args []string

	// NextArgs, when set, is called immediately before EVERY spawn and its
	// result is used instead of Args for that run.
	//
	// It exists because a respawn is not always a repeat. A destination writing
	// to a file cannot re-run the command that produced the file it is already
	// holding: FFmpeg refuses an existing output and exits, so without this the
	// first restart ends the destination permanently, and the alternative --
	// passing -y -- would silently truncate whatever had been recorded so far.
	// Choosing the path per spawn is the only option that neither destroys the
	// footage nor wedges the destination.
	NextArgs func() []string

	// MaxRestarts gives up after this many CONSECUTIVE restarts that did not
	// reach stableAfter. 0 is unlimited, which is what every process here did
	// before this existed and remains the right default for the ingest.
	//
	// It exists for destinations. A destination retrying forever looks exactly
	// like one that works: the card says "reconnecting", the supervisor is
	// busy, and nothing ever says "this endpoint has refused us forty times and
	// is not coming back". Giving up moves it to StateFailed, which the alert
	// watcher already treats as down -- so the operator is told once, loudly,
	// instead of never.
	//
	// CONSECUTIVE is the load-bearing word. A destination that reconnects
	// cleanly once an hour for a week must never accumulate its way to the
	// limit, so a run longer than stableAfter resets the count, exactly as it
	// already resets the backoff.
	MaxRestarts int

	// StartDelay holds the FIRST spawn back. Restarts are unaffected: a
	// destination that drops at 3am must come back immediately, not wait its
	// turn behind processes that are already healthy.
	//
	// It exists so that bringing several destinations up at once does not
	// spawn eight FFmpegs in the same tick. Each one opens a connection,
	// negotiates TLS and starts encoding audio simultaneously, which on a
	// small box is the moment most likely to drop frames -- and it is the exact
	// moment an operator is watching, because it is when they went live.
	StartDelay time.Duration

	// AutoRestart makes the supervisor respawn the child when it exits.
	// The preview and destinations want this; a one-shot probe does not.
	AutoRestart bool
	MinBackoff  time.Duration
	MaxBackoff  time.Duration

	// StdoutHandler consumes stdout. Defaults to the -progress parser. The
	// metering sidecar overrides it, because it writes level data there
	// instead.
	StdoutHandler func(io.Reader) error

	OnProgress func(ffmpeg.Progress)
	OnLog      func(LogLine)
	OnState    func(Status)

	// LogSink persists captured lines beyond the in-memory ring. Optional,
	// and typically one sink shared by every process so the persisted log
	// reads as a single interleaved timeline.
	LogSink LogWriter
}

// Status is the externally visible state of a process.
type Status struct {
	Name        string          `json:"name"`
	Kind        string          `json:"kind"`
	State       State           `json:"state"`
	PID         int             `json:"pid"`
	Restarts    int             `json:"restarts"`
	StartedAt   *time.Time      `json:"startedAt,omitempty"`
	UptimeSec   float64         `json:"uptimeSec"`
	LastError   string          `json:"lastError,omitempty"`
	NextRetryIn float64         `json:"nextRetryIn,omitempty"`
	Progress    ffmpeg.Progress `json:"progress"`
}

// Process is one supervised child.
type Process struct {
	spec Spec
	log  *slog.Logger

	mu        sync.RWMutex
	state     State
	pid       int
	restarts  int
	startedAt time.Time
	lastErr   string
	nextRetry time.Time
	progress  ffmpeg.Progress
	logs      *ring
	// liveArgs is the argv of the most recent spawn. Equal to spec.Args unless
	// spec.NextArgs resolved something different for this run.
	liveArgs []string

	cmdMu sync.Mutex
	cmd   *exec.Cmd

	runMu   sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
}

// New creates a supervised process. It does not start it.
func New(log *slog.Logger, spec Spec) *Process {
	if spec.MinBackoff == 0 {
		spec.MinBackoff = defaultMinBackoff
	}
	if spec.MaxBackoff == 0 {
		spec.MaxBackoff = defaultMaxBackoff
	}
	return &Process{
		spec:  spec,
		log:   log.With("process", spec.Name),
		state: StateStopped,
		logs:  newRing(logRingSize),
	}
}

// Name returns the process name.
func (p *Process) Name() string { return p.spec.Name }

// currentArgs is the argv of the running child, falling back to the configured
// one before the first spawn.
func (p *Process) currentArgs() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.liveArgs != nil {
		return p.liveArgs
	}
	return p.spec.Args
}

// Args returns the command line, for display on the monitoring page.
func (p *Process) Args() []string { return append([]string{p.spec.Bin}, p.currentArgs()...) }

// CommandString renders the full command line for the UI.
func (p *Process) CommandString() string {
	live := p.currentArgs()
	parts := make([]string, 0, len(live)+1)
	parts = append(parts, p.spec.Bin)
	for _, a := range live {
		if strings.ContainsAny(a, " \t\"'|&;<>()$`\\") {
			parts = append(parts, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
			continue
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// Start begins supervising. Calling it on an already-running process is a
// no-op, which makes reconcile loops safe to run repeatedly.
func (p *Process) Start() {
	p.runMu.Lock()
	defer p.runMu.Unlock()
	if p.running {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	p.running = true
	go p.supervise(ctx, p.done)
}

// Stop terminates the child and stops supervising. It blocks until the child
// is gone or ctx expires.
func (p *Process) Stop(ctx context.Context) {
	p.runMu.Lock()
	if !p.running {
		p.runMu.Unlock()
		return
	}
	cancel, done := p.cancel, p.done
	p.running = false
	p.runMu.Unlock()

	cancel()
	p.terminate()

	select {
	case <-done:
	case <-ctx.Done():
		p.log.Warn("timed out waiting for process to exit; killing")
		p.kill()
	}
	p.setState(StateStopped, "")
}

// Restart stops and starts the process. Used when a routing profile changes:
// only the affected destination is cycled, never the ingest.
func (p *Process) Restart(ctx context.Context) {
	p.Stop(ctx)
	p.Start()
}

func (p *Process) supervise(ctx context.Context, done chan struct{}) {
	defer close(done)

	backoff := p.spec.MinBackoff
	// Consecutive restarts that did not reach stableAfter. Reset by a healthy
	// run, not by a successful spawn: a process that starts and dies in two
	// seconds has not recovered from anything.
	consecutive := 0

	// Before the first spawn only, and interruptible: a stop that arrives
	// during the stagger window must not wait it out.
	if p.spec.StartDelay > 0 {
		select {
		case <-ctx.Done():
			p.setState(StateStopped, "")
			return
		case <-time.After(p.spec.StartDelay):
		}
	}

	for {
		if ctx.Err() != nil {
			return
		}

		p.setState(StateStarting, "")
		started := time.Now()
		err := p.runOnce(ctx)
		ranFor := time.Since(started)

		if ctx.Err() != nil {
			p.setState(StateStopped, "")
			return
		}

		msg := ""
		if err != nil {
			msg = err.Error()
			p.log.Warn("process exited", "err", err, "ranFor", ranFor.Round(time.Second))
		} else {
			p.log.Info("process exited cleanly", "ranFor", ranFor.Round(time.Second))
		}

		if !p.spec.AutoRestart {
			if err != nil {
				p.setState(StateFailed, msg)
			} else {
				p.setState(StateStopped, "")
			}
			return
		}

		// A process that stayed up is healthy; its next failure should retry
		// promptly rather than inherit an old backoff.
		if ranFor > stableAfter {
			backoff = p.spec.MinBackoff
			consecutive = 0
		}
		consecutive++

		// Out of patience. StateFailed rather than StateStopped: stopped is
		// what an operator asked for, failed is what happened to them, and the
		// alert watcher only treats one of those as an incident.
		if p.spec.MaxRestarts > 0 && consecutive > p.spec.MaxRestarts {
			give := fmt.Sprintf("gave up after %d consecutive restarts", consecutive-1)
			if msg != "" {
				give += ": " + msg
			}
			p.log.Error("giving up on process", "restarts", consecutive-1, "err", msg)
			p.appendLog(give, "error")
			p.setState(StateFailed, give)
			return
		}

		p.mu.Lock()
		p.restarts++
		p.nextRetry = time.Now().Add(backoff)
		p.mu.Unlock()
		p.setState(StateReconnecting, msg)

		p.appendLog(fmt.Sprintf("process exited after %s; retrying in %s",
			ranFor.Round(time.Second), backoff.Round(time.Second)), "warning")

		select {
		case <-ctx.Done():
			p.setState(StateStopped, "")
			return
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > p.spec.MaxBackoff {
			backoff = p.spec.MaxBackoff
		}
	}
}

// runOnce spawns the child and blocks until it exits.
func (p *Process) runOnce(ctx context.Context) error {
	argv := p.spec.Args
	if p.spec.NextArgs != nil {
		argv = p.spec.NextArgs()
	}
	// Recorded so the monitoring page and the crash-loop log line show the
	// command that is actually running, not the one this process started life
	// with. A resolved argv that only the child ever saw would make the one
	// place an operator looks disagree with reality.
	p.mu.Lock()
	p.liveArgs = argv
	p.mu.Unlock()

	cmd := exec.Command(p.spec.Bin, argv...)
	// Put the child in its own process group so we can signal the whole tree,
	// and so a Ctrl-C in the terminal reaches us first rather than killing
	// children out from under the supervisor.
	setProcessGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", p.spec.Bin, err)
	}

	p.cmdMu.Lock()
	p.cmd = cmd
	p.cmdMu.Unlock()

	p.mu.Lock()
	p.pid = cmd.Process.Pid
	p.startedAt = time.Now()
	p.progress = ffmpeg.Progress{}
	p.mu.Unlock()
	p.setState(StateRunning, "")
	p.log.Info("process started", "pid", cmd.Process.Pid)
	// Debug rather than info: this is long, and one line per respawn would
	// swamp the log. But when a process crash-loops the command line is the
	// first thing anyone wants, and reading it off the monitoring page is no
	// help when the failure is a restart loop that never settles.
	p.log.Debug("process command", "argv", p.CommandString())

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		handler := p.spec.StdoutHandler
		if handler == nil {
			handler = p.defaultStdout
		}
		if err := handler(stdout); err != nil && ctx.Err() == nil {
			p.log.Debug("stdout handler ended", "err", err)
		}
	}()

	// FFmpeg's last words before dying are on stderr, so the tail of this
	// buffer is what tells a user *why* a destination is failing.
	var lastLines []string
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64*1024), 512*1024)
		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r\n")
			if line == "" {
				continue
			}
			level := classify(line)
			p.appendLog(line, level)
			if level == "error" || level == "fatal" {
				lastLines = append(lastLines, line)
				if len(lastLines) > 3 {
					lastLines = lastLines[1:]
				}
			}
		}
	}()

	wg.Wait()
	waitErr := cmd.Wait()

	p.cmdMu.Lock()
	p.cmd = nil
	p.cmdMu.Unlock()
	p.mu.Lock()
	p.pid = 0
	p.mu.Unlock()

	if waitErr != nil && len(lastLines) > 0 {
		return fmt.Errorf("%v: %s", waitErr, strings.Join(lastLines, " | "))
	}
	return waitErr
}

func (p *Process) defaultStdout(r io.Reader) error {
	return ffmpeg.ParseProgress(r, func(pr ffmpeg.Progress) {
		p.mu.Lock()
		p.progress = pr
		p.mu.Unlock()
		if p.spec.OnProgress != nil {
			p.spec.OnProgress(pr)
		}
	})
}

// terminate asks the child's whole process group to exit, then escalates.
func (p *Process) terminate() {
	p.cmdMu.Lock()
	cmd := p.cmd
	p.cmdMu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	signalGroup(cmd)

	// Escalate if it has not gone after the grace period. FFmpeg normally
	// exits in well under a second; anything longer is wedged.
	go func() {
		time.Sleep(shutdownGrace)
		p.cmdMu.Lock()
		still := p.cmd
		p.cmdMu.Unlock()
		if still == cmd {
			p.log.Warn("process did not exit after grace period; killing group")
			killGroup(cmd)
		}
	}()
}

func (p *Process) kill() {
	p.cmdMu.Lock()
	cmd := p.cmd
	p.cmdMu.Unlock()
	if cmd != nil {
		killGroup(cmd)
	}
}

func (p *Process) setState(s State, errMsg string) {
	p.mu.Lock()
	changed := p.state != s || p.lastErr != errMsg
	p.state = s
	if errMsg != "" {
		p.lastErr = errMsg
	}
	if s == StateRunning {
		p.lastErr = ""
	}
	p.mu.Unlock()

	if changed && p.spec.OnState != nil {
		p.spec.OnState(p.Status())
	}
}

func (p *Process) appendLog(text, level string) {
	l := LogLine{Time: time.Now(), Process: p.spec.Name, Text: text, Level: level}
	p.logs.add(l)
	if p.spec.LogSink != nil {
		p.spec.LogSink.WriteLog(l)
	}
	if p.spec.OnLog != nil {
		p.spec.OnLog(l)
	}
}

// Status returns a snapshot.
func (p *Process) Status() Status {
	p.mu.RLock()
	defer p.mu.RUnlock()

	st := Status{
		Name:      p.spec.Name,
		Kind:      p.spec.Kind,
		State:     p.state,
		PID:       p.pid,
		Restarts:  p.restarts,
		LastError: p.lastErr,
		Progress:  p.progress,
	}
	if p.state == StateRunning && !p.startedAt.IsZero() {
		t := p.startedAt
		st.StartedAt = &t
		st.UptimeSec = time.Since(p.startedAt).Seconds()
	}
	if p.state == StateReconnecting && !p.nextRetry.IsZero() {
		if d := time.Until(p.nextRetry).Seconds(); d > 0 {
			st.NextRetryIn = d
		}
	}
	return st
}

// Logs returns the buffered stderr tail.
func (p *Process) Logs() []LogLine { return p.logs.snapshot() }

// classify maps an FFmpeg stderr line to a severity the UI can colour.
func classify(line string) string {
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "fatal"), strings.Contains(l, "panic"):
		return "fatal"
	case strings.Contains(l, "error"), strings.Contains(l, "failed"),
		strings.Contains(l, "invalid"), strings.Contains(l, "no such"),
		strings.Contains(l, "connection refused"), strings.Contains(l, "unable to"):
		return "error"
	case strings.Contains(l, "warning"), strings.Contains(l, "deprecated"),
		strings.Contains(l, "past duration"), strings.Contains(l, "non-monotonous"):
		return "warning"
	default:
		return "info"
	}
}

// ring is a fixed-size circular buffer of log lines. Bounded on purpose: a
// process that logs a warning every frame must not be able to exhaust memory.
type ring struct {
	mu   sync.RWMutex
	buf  []LogLine
	size int
	next int
	full bool
}

func newRing(n int) *ring { return &ring{buf: make([]LogLine, n), size: n} }

func (r *ring) add(l LogLine) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = l
	r.next = (r.next + 1) % r.size
	if r.next == 0 {
		r.full = true
	}
}

func (r *ring) snapshot() []LogLine {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.full {
		out := make([]LogLine, r.next)
		copy(out, r.buf[:r.next])
		return out
	}
	out := make([]LogLine, 0, r.size)
	out = append(out, r.buf[r.next:]...)
	out = append(out, r.buf[:r.next]...)
	return out
}
