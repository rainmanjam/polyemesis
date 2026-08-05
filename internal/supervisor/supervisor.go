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

	"github.com/rainmanjam/polyemesis/internal/alerts"
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
	//
	// MaxRestarts, MinBackoff and MaxBackoff seed the process's Policy. After
	// Start they are read only through Policy()/SetPolicy(); reading spec here
	// would mean a retune applied to a running destination had no effect.
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

// Policy is the part of a Spec that can change while the process is running.
//
// It is separated out because everything else in a Spec ends up in an argv, and
// FFmpeg has no channel for changing an argv in flight. These three never reach
// the child at all -- they describe what the SUPERVISOR does once it has exited
// -- so retuning them is a memory write rather than a restart.
//
// Before this existed the only way to apply "be more patient with this
// destination" was to drop its connection: the three values rode in destSpec,
// so editing them tore the destination down and built it again. An operator
// raising a give-up threshold because a platform was flapping got a guaranteed
// outage as the price of asking for fewer of them.
type Policy struct {
	MinBackoff  time.Duration
	MaxBackoff  time.Duration
	MaxRestarts int
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

	// policyMu guards pol. Deliberately NOT p.mu: setState takes p.mu and then
	// calls OnState, which fans out to the WebSocket, and a reconcile applying
	// a policy must never end up waiting behind a browser.
	policyMu sync.Mutex
	pol      Policy
	// retune wakes a supervisor that is sleeping out a backoff, so a lowered
	// ceiling takes effect now rather than after the wait it was already in.
	// Buffered by one and written non-blocking: applying a policy must never
	// block on a supervisor that is mid-spawn.
	retune chan struct{}
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
		spec:   spec,
		pol:    Policy{MinBackoff: spec.MinBackoff, MaxBackoff: spec.MaxBackoff, MaxRestarts: spec.MaxRestarts},
		retune: make(chan struct{}, 1),
		log:    log.With("process", spec.Name),
		state:  StateStopped,
		logs:   newRing(logRingSize),
	}
}

// Policy returns the live restart policy.
func (p *Process) Policy() Policy {
	p.policyMu.Lock()
	defer p.policyMu.Unlock()
	return p.pol
}

// SetPolicy retunes a process that is already running.
//
// It deliberately does NOT reset the restart counter or the consecutive-failure
// count. A settings save touches every destination, so resetting them would
// mean that saving a log level silently granted every destination that had been
// failing all night a fresh set of lives -- and one that should have given up
// would retry for ever, which is the condition GiveUpAfter exists to end.
//
// A lowered MaxRestarts applies from the NEXT exit and never retroactively: a
// process is not failed for exits it made under the old rules.
func (p *Process) SetPolicy(pol Policy) {
	if pol.MinBackoff <= 0 {
		pol.MinBackoff = defaultMinBackoff
	}
	if pol.MaxBackoff <= 0 {
		pol.MaxBackoff = defaultMaxBackoff
	}
	// A ceiling below the floor would make backoff *= 2 clamp downwards for
	// ever, pinning the retry curve at the floor. The API validates the pair,
	// so this only catches a caller that constructed a Policy by hand.
	if pol.MaxBackoff < pol.MinBackoff {
		pol.MaxBackoff = pol.MinBackoff
	}

	p.policyMu.Lock()
	changed := p.pol != pol
	p.pol = pol
	p.policyMu.Unlock()
	if !changed {
		return
	}

	select {
	case p.retune <- struct{}{}:
	default:
	}
}

// waitBackoff sleeps out a retry delay, returning false when the process was
// stopped during it.
//
// A policy change during the wait shortens it to the new ceiling and never
// lengthens it. Both halves are deliberate: an operator who lowers MaxBackoff
// while a destination is crawling expects it back sooner, and one who raises it
// does not expect the destination they are already watching to go quiet for
// longer than it had already promised.
func (p *Process) waitBackoff(ctx context.Context, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		wait := time.Until(deadline)
		if wait <= 0 {
			return true
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
			return true
		case <-p.retune:
			timer.Stop()
			if max := p.Policy().MaxBackoff; time.Until(deadline) > max {
				deadline = time.Now().Add(max)
				// Status.NextRetryIn is what the dashboard counts down, so it
				// has to move with the deadline or the card lies for the rest
				// of the wait.
				p.mu.Lock()
				p.nextRetry = deadline
				p.mu.Unlock()
			}
		}
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

// Args returns the argv exactly as it was handed to the kernel, credentials
// and all.
//
// This is the machine-readable form, for a caller that has to reason about the
// arguments themselves -- expert mode strips its own additions back off this to
// show an operator their edit rather than their edit stacked on the last one.
// Anything destined for a screen wants CommandString instead, which is the same
// argv with the credentials masked.
func (p *Process) Args() []string { return append([]string{p.spec.Bin}, p.currentArgs()...) }

// CommandString renders the full command line for a human, with every
// credential masked.
//
// The masking lives here rather than at the API boundary because there is no
// caller of this method that wants the unmasked text. It exists to be shown --
// on the monitoring page, and in the spawn-time debug line below -- and both of
// those are places a stream key must not appear. A destination's argv contains
// rtmps://<host>/rtmp/<key>, and with backup ingest on it contains the backup
// key too. A caller that genuinely needs the real argv has Args().
//
// alerts.Redact is deliberately the same function the alerts payloads, the
// lifecycle hooks and the MQTT broker logs already run on this byte stream.
// Having one redactor is the only thing that stops this path drifting from
// those, which is exactly how it came to be the one egress that skipped the
// policy.
//
// Redaction runs before quoting so the shell quoting describes the string that
// is actually shown rather than the one being hidden.
func (p *Process) CommandString() string {
	live := p.currentArgs()
	parts := make([]string, 0, len(live)+1)
	parts = append(parts, p.spec.Bin)
	for _, a := range live {
		a = alerts.Redact(a)
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

	// From the policy, not the spec: a retune that landed between New and
	// Start must be the curve this run starts on.
	backoff := p.Policy().MinBackoff
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
			backoff = p.Policy().MinBackoff
			consecutive = 0
		}
		consecutive++

		// Out of patience. StateFailed rather than StateStopped: stopped is
		// what an operator asked for, failed is what happened to them, and the
		// alert watcher only treats one of those as an incident.
		// Read here rather than at the top of supervise: the limit an exit is
		// judged against is the one in force when it exited, which is what
		// makes a lowered limit apply from the next exit rather than to the
		// history that preceded it.
		if pol := p.Policy(); pol.MaxRestarts > 0 && consecutive > pol.MaxRestarts {
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

		if !p.waitBackoff(ctx, backoff) {
			p.setState(StateStopped, "")
			return
		}

		backoff *= 2
		if max := p.Policy().MaxBackoff; backoff > max {
			backoff = max
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

	// LastError is masked for the same reason Logs is, and it is the more
	// dangerous of the two. runOnce builds it from the last three stderr lines
	// classified as error or fatal, and "Error opening output rtmps://host/app/
	// <key>: Connection refused" is exactly that shape -- so the field an
	// operator reads to find out why a destination is down is also the field
	// most likely to hold the key.
	//
	// It travels further than the process page: engine copies it onto the
	// dashboard snapshot, and cmd/polyemesis/mqtt.go copies it into
	// SourceState.IngestError, which is published RETAINED. A retained topic
	// outlives the process that wrote it and is readable by every subscriber on
	// the broker, with no session behind it.
	st := Status{
		Name:      p.spec.Name,
		Kind:      p.spec.Kind,
		State:     p.state,
		PID:       p.pid,
		Restarts:  p.restarts,
		LastError: alerts.Redact(p.lastErr),
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

// Logs returns the buffered stderr tail, with every credential masked.
//
// FFmpeg prints the full publish URL when a connect fails, so the tail of a
// failing destination is precisely where a stream key surfaces -- and a failing
// destination is the only reason anyone opens this. hooks.payload already
// scrubs the same bytes on its way out for the same reason.
//
// Masking on the way out rather than on the way in is deliberate. classify()
// and the on-disk sink have already seen the raw line, the ring keeps it, and
// anything in-process that later needs to match on a URL still can. This method
// is the boundary where the line stops being ours, so this is where it is
// cleaned.
//
// alerts.Redact touches URLs and bare key=value credentials only. An FFmpeg
// progress or error line has neither, so the diagnostic value an operator came
// for -- the scheme, the host, "Connection refused", the frame counters --
// survives intact.
func (p *Process) Logs() []LogLine {
	lines := p.logs.snapshot()
	for i := range lines {
		lines[i].Text = alerts.Redact(lines[i].Text)
	}
	return lines
}

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
