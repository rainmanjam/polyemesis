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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
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
	//
	// It is contracted from OUTSIDE this package: acceptance-recording-stop.sh
	// measures elapsed wall-clock across a stop and asserts it is under 8s. The
	// number may not be changed without changing that assertion.
	shutdownGrace = 8 * time.Second
	// drainGrace is how long the stdout/stderr drain gets to finish AFTER the
	// child has been reaped, before the supervisor closes the read ends out from
	// under it.
	//
	// It exists because a pipe reaches EOF when the LAST write end closes, and a
	// child's descendants inherit those write ends. FFmpeg spawning a helper that
	// outlives it -- or any grandchild that escapes the process group -- keeps
	// stdout and stderr open after the child is gone, and a drain that waits for
	// EOF therefore waits on a process the supervisor never started and cannot
	// name. Before this bound existed, that made runOnce, and so `done`, and so
	// Stop, unbounded.
	//
	// 2s and not less because the drain's remaining job at that point is to scan
	// what is already sitting in the pipe buffer, which the kernel caps at tens
	// of kilobytes -- three orders of magnitude below what 2s of bufio scanning
	// costs, so the tail of stderr that becomes LastError survives. 2s and not
	// more because it is additive to a stop that has already spent its own grace,
	// and only ever paid at all when a descendant is holding the pipe.
	drainGrace  = 2 * time.Second
	logRingSize = 400
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

	// Secrets are the EXACT credential literals that appear anywhere in this
	// process's argv, its stderr, or an error string it produces.
	//
	// Everything the supervisor renders for a human -- CommandString, the log
	// ring, the on-disk process.log, the WebSocket log frames and
	// Status.LastError -- removes these by exact substring match before anything
	// else looks at the text. See (*Process).scrub.
	//
	// It exists because alerts.Redact, which was the only masking on those
	// egresses, is a GRAMMAR over an open namespace. FFmpeg takes arbitrary
	// `-flag value` pairs, so `-rtmp_conn S:<key>` is invisible to it and
	// `Authorization:Bearer\ <key>` splits into argv entries where it masks the
	// word "Bearer" and hands back the key. Both shapes shipped, and both
	// reached a READ-scoped API token through GET /processes, GET
	// /processes/{name}/logs and the /ws log stream. No regex closes that,
	// because the set of flag spellings is not enumerable -- but the set of
	// credentials THIS process was built with is, and it is right here.
	//
	// Populate it at every construction site whose argv can carry an operator's
	// credential. internal/engine has an AST guard that fails the build when a
	// site neither sets this nor appears in its declared exemption list with a
	// reason, because a forgotten site reproduces exactly the bug this closes.
	Secrets []string

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
	// secrets is spec.Secrets compiled once at construction. Immutable after
	// New, so scrub needs no lock and can be called from the stderr scan
	// goroutine and an HTTP handler at the same time.
	secrets *alerts.SecretSet

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
	// exited is closed by runOnce the instant cmd.Wait() returns for THIS cmd --
	// that is, the instant the child is reaped, not when its pipes close. It is
	// allocated with cmd and cleared with it, so it is a per-run token: a stale
	// reference cannot be satisfied by a successor's exit, and a successor's
	// escalator cannot be cancelled by a predecessor's.
	//
	// It replaces the pointer-identity guard terminate() used to carry. That
	// guard was correct -- runOnce nils p.cmd before a respawn allocates a new
	// exec.Cmd, so a stale closure could not match -- but it was a subtle
	// invariant defended by nothing, and it could only be consulted after the
	// escalator had already slept the whole grace period.
	exited chan struct{}
	// signalled records that THIS cmd has already been sent its SIGTERM, so a
	// second terminate() for the same spawn is a no-op. Cleared where cmd is
	// published, which makes it per-run exactly as `exited` is.
	//
	// It exists because terminate() acquired two callers the moment runOnce
	// learned to honour a cancelled context: stop(), and the spawn itself. Both
	// can reach it for one cmd, and a SECOND SIGTERM IS NOT FREE -- FFmpeg
	// answers a repeated SIGTERM by exiting immediately instead of flushing,
	// which turns the finalised recording the grace period exists to protect
	// into a truncated one.
	signalled bool

	// escalators tracks terminate()'s grace goroutines. Each one is a live
	// commitment to kill a process group, so something must be able to observe
	// that they have finished; without this the only proof was wall-clock.
	escalators sync.WaitGroup

	// grace and drain are shutdownGrace and drainGrace as this Process will
	// actually use them. Fields rather than the constants directly so a test can
	// separate "the escalation fired" from "the escalation fired at 8 seconds"
	// without the suite paying 8 seconds per case, and without mutating package
	// state that a concurrent test would see.
	grace time.Duration
	drain time.Duration

	runMu   sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
	// retired is the terminal "do not start" latch. Stop sets it whether or not
	// the process was running, and Start honours it for ever after.
	//
	// Without it, Stop on a process that has not been started yet is a silent
	// no-op, and every caller that builds a process, publishes it somewhere a
	// shutdown can find it, and only THEN calls Start has a window: the shutdown
	// takes the published entry, calls Stop on a process that is not running,
	// releases the port and the hub subscription it was given, and then the
	// original caller starts a child that nothing holds a reference to -- still
	// publishing, on a relay port the allocator has already handed to someone
	// else. internal/engine's destinations, its backup ingest, its renditions and
	// internal/playout's variants all have that shape, so the latch belongs here
	// rather than being re-derived at each of them.
	//
	// Restart deliberately does NOT set it: cycling a process is not retiring it.
	// A real Stop that lands between Restart's stop and its start does set it,
	// and the restart correctly turns into a no-op.
	retired bool

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
	pol := Policy{
		MinBackoff:  spec.MinBackoff,
		MaxBackoff:  spec.MaxBackoff,
		MaxRestarts: spec.MaxRestarts,
	}.Normalised()
	spec.MinBackoff, spec.MaxBackoff = pol.MinBackoff, pol.MaxBackoff
	return &Process{
		spec:    spec,
		pol:     pol,
		retune:  make(chan struct{}, 1),
		log:     log.With("process", spec.Name),
		state:   StateStopped,
		logs:    newRing(logRingSize),
		secrets: alerts.NewSecretSet(log, spec.Secrets...),
		grace:   shutdownGrace,
		drain:   drainGrace,
	}
}

// scrub is THE chokepoint. Every string this process renders for a reader --
// human or machine, authenticated or not -- goes through here and through
// nothing else.
//
// Two passes, in this order and for different reasons:
//
//  1. The exact literals from Spec.Secrets. This is the boundary. It cannot be
//     defeated by how the credential was spelled onto the command line, because
//     it does not read the spelling at all.
//  2. alerts.Redact, as a RESIDUAL best-effort pass, for credentials this
//     process was never told about: a token an endpoint echoed back in an error
//     string, a URL FFmpeg synthesised from parts. It is not the boundary and
//     the four callers below must not be read as though it were.
//
// UNCONDITIONAL -- there is no principal argument and there must not be one.
// Two of the sinks fed from here have no principal and never will: process.log
// is a file that ends up in support tarballs, and cmd/polyemesis/mqtt.go
// publishes Status.LastError to a RETAINED topic that outlives the process and
// is readable by every subscriber on the broker. An admin who genuinely wants
// the raw argv has GET /destinations/{id}/expert, which is admin-only and reads
// Args() rather than anything on this path.
func (p *Process) scrub(s string) string {
	return alerts.Redact(p.secrets.Scrub(s))
}

// Normalised fills a Policy's zero fields with the defaults a running process
// would have been given, and clamps a ceiling that sits below its floor.
//
// It exists because a zero Policy and the Policy it becomes are the same
// intent expressed two ways, and CALLERS COMPARE THEM. internal/engine builds
// its wanted policy straight from the database row, where "unconfigured" is 0,
// and compares it against Policy() -- which New has already filled in. Without
// one definition of "the same policy" those two never match, so every reconcile
// reported an unconfigured destination as retuned: a log line and a reload note
// each time, on a destination nobody had touched.
//
// A ceiling below the floor would make backoff *= 2 clamp downwards for ever,
// pinning the retry curve at the floor. The API validates the pair, so that
// only catches a caller that built a Policy by hand.
func (p Policy) Normalised() Policy {
	if p.MinBackoff <= 0 {
		p.MinBackoff = defaultMinBackoff
	}
	if p.MaxBackoff <= 0 {
		p.MaxBackoff = defaultMaxBackoff
	}
	if p.MaxBackoff < p.MinBackoff {
		p.MaxBackoff = p.MinBackoff
	}
	return p
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
	pol = pol.Normalised()

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
// TWO passes over TWO different units of text, and the split is the fix rather
// than a tidy-up.
//
// The exact literals from Spec.Secrets are removed PER ELEMENT, because that is
// the unit the credential arrives in and a substring match does not care what
// is glued to it.
//
// alerts.Redact then runs ONCE over the JOINED string, because applying it per
// argument is itself a leaking shape. `-headers Authorization: Bearer <key>` is
// fully masked as one string -- the bearerToken rule spans the space -- and
// leaks the key when each argv element is redacted alone, since "Bearer" and
// the key are separate elements and neither matches anything by itself. This
// method did exactly that, so the CANONICAL, correctly-spelled header form was
// the one it failed on. Reconstituting the line before the residual pass is
// what closes it.
//
// Redaction of the literals runs before quoting so the shell quoting describes
// the string that is actually shown rather than the one being hidden.
func (p *Process) CommandString() string {
	live := p.currentArgs()
	parts := make([]string, 0, len(live)+1)
	parts = append(parts, p.spec.Bin)
	for _, a := range live {
		a = p.secrets.Scrub(a)
		if strings.ContainsAny(a, " \t\"'|&;<>()$`\\") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		parts = append(parts, a)
	}
	return alerts.Redact(strings.Join(parts, " "))
}

// Start begins supervising. Calling it on an already-running process is a
// no-op, which makes reconcile loops safe to run repeatedly. Calling it on a
// process that has already been Stopped is also a no-op, for ever: see retired.
func (p *Process) Start() {
	p.runMu.Lock()
	defer p.runMu.Unlock()
	// Mutation that proves the retired half: delete the `p.retired ||` clause
	// below. TestStopBeforeStartRetiresTheProcessForEver fails, and so does the
	// engine's TestAReconcileThatPublishesIntoAShutdownStartsNothing.
	//
	// The clause, NOT the line. Deleting the whole `if` leaves a dangling
	// `return` and does not compile, and a mutation that does not build proves
	// nothing at all.
	if p.retired || p.running {
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
//
// RETURNS AN ERROR WHEN THE CHILD HAD TO BE KILLED ON THE DEADLINE, because
// "stopped" and "sent SIGKILL and stopped waiting" are different facts and a
// caller cannot tell them apart from the state alone. Both end at StateStopped.
// The one caller that most needs the difference is the selector: it starts a
// replacement feed into the same hub the moment this returns, so a child that
// is still alive and still writing is two publishers on one input.
//
// Ignoring the error is fine and most callers do -- a shutdown path has nothing
// better to do with it -- which is why this returns rather than blocking longer
// or panicking.
//
// Stop is TERMINAL. It retires the process even when there is nothing running
// to terminate, so a Start that was already on its way -- built, published,
// about to be called -- cannot bring a child up behind the shutdown that just
// released its port and its subscription.
func (p *Process) Stop(ctx context.Context) error { return p.stop(ctx, true) }

// Restart stops and starts the process. Used when a routing profile changes:
// only the affected destination is cycled, never the ingest.
//
// Non-terminal, unlike Stop: this is a cycle, not a retirement. If a real Stop
// lands in the gap, its latch makes the Start below a no-op, which is the
// outcome the caller of Stop asked for.
func (p *Process) Restart(ctx context.Context) {
	// Deliberately discarded: a restart that had to kill the old child still
	// wants the new one, and the caller of Restart has no different action to
	// take. The selector, which does, calls Stop directly.
	_ = p.stop(ctx, false)
	p.Start()
}

// ErrStopDeadline reports that Stop gave up waiting and killed the child. Its
// own error because the caller's question is "can I reuse what it was holding
// yet", and that has a different answer from any other stop failure.
var ErrStopDeadline = errors.New("process did not exit before the stop deadline")

func (p *Process) stop(ctx context.Context, retire bool) error {
	p.runMu.Lock()
	if retire {
		p.retired = true
	}
	if !p.running {
		p.runMu.Unlock()
		return nil
	}
	cancel, done := p.cancel, p.done
	p.running = false
	p.runMu.Unlock()

	cancel()
	p.terminate()

	var err error
	select {
	case <-done:
	case <-ctx.Done():
		// SIGKILL is issued and this returns; it does NOT wait for the child to
		// die. It cannot: the deadline is already spent, and blocking past it
		// would hold whatever the caller is holding for an unbounded time. So
		// the honest thing is to say so rather than to report a clean stop.
		p.log.Warn("timed out waiting for process to exit; killing")
		p.kill()
		err = fmt.Errorf("%w after %s: the child was sent SIGKILL and may still be running",
			ErrStopDeadline, p.Name())
	}
	p.setState(StateStopped, "")
	return err
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
			// SCRUBBED, like every other rendering of this text. The error carries
			// the child's own stderr, and for a destination that stderr is FFmpeg
			// reporting the output URL it could not open -- stream key included.
			//
			// Found on a live run against a real platform: a refused publish put
			// the key in server.log six times as the supervisor retried, while
			// process.log was clean because that path already scrubbed. One sink
			// was covered and its sibling was not.
			//
			// A refusal is exactly when this fires, and a refusal is exactly when
			// an operator copies logs to somebody who can help. See #306.
			p.log.Warn("process exited", "err", p.scrub(msg), "ranFor", ranFor.Round(time.Second))
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
			// SCRUBBED, for the reason the "process exited" line above is, and
			// this line is the one that was missed when that one was fixed.
			// #311 scrubbed the retry line and left this one carrying the same
			// `msg` -- runOnce's error, which holds FFmpeg's stderr and with it
			// the publish URL and its key.
			//
			// The give-up path is the WORSE of the two to leak on. It fires
			// only after MaxRestarts consecutive failures, which is to say
			// after a destination has been refused over and over -- exactly the
			// state an operator is looking at when they copy server.log into an
			// issue. The retry line got the attention because it fires often;
			// this one fires when someone is about to ask for help.
			p.log.Error("giving up on process", "restarts", consecutive-1, "err", p.scrub(msg))
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

	// os.Pipe rather than cmd.StdoutPipe/StderrPipe, and the reason is the whole
	// of this function's shape.
	//
	// exec's own pipes are owned by exec: Wait closes them after the process
	// exits, and its documentation therefore forbids calling Wait before every
	// read has completed. That forces "drain to EOF, then Wait" -- and EOF on a
	// pipe means the last WRITE end closed, which is a fact about the child's
	// descendants rather than about the child. A grandchild that outlives its
	// parent, or escapes the process group the escalation signals, holds that
	// write end open, and the drain waits for a process the supervisor never
	// started. runOnce then never returns, supervise never closes `done`, and
	// Stop takes its deadline arm at best and blocks for ever at worst.
	//
	// Pipes this package creates are owned by this package. Nothing in exec
	// touches them, so cmd.Wait() may be called the moment the child is reaped --
	// bounding the reap on the child alone -- and the read ends can be closed
	// from here, which unblocks a drain that is waiting on an inherited writer.
	stdout, stdoutW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdout.Close()
		_ = stdoutW.Close()
		return fmt.Errorf("stderr pipe: %w", err)
	}
	cmd.Stdout, cmd.Stderr = stdoutW, stderrW

	startErr := cmd.Start()
	// The parent's copies of the write ends, closed unconditionally: the child
	// has inherited its own, and while the parent holds one the pipe cannot reach
	// EOF even when every descendant has gone.
	_ = stdoutW.Close()
	_ = stderrW.Close()
	if startErr != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return fmt.Errorf("start %s: %w", p.spec.Bin, startErr)
	}

	exited := make(chan struct{})
	p.cmdMu.Lock()
	p.cmd = cmd
	p.exited = exited
	p.signalled = false
	p.cmdMu.Unlock()

	// A STOP THAT LANDED WHILE THIS SPAWN WAS IN FLIGHT SIGNALLED NOTHING, and
	// until this line nothing ever went back for it.
	//
	// Start() sets p.running under runMu and returns; p.cmd is not published
	// until the two lines above. Between those two points stop() takes its
	// `p.running` arm -- so it does NOT return early -- cancels this ctx, and
	// calls terminate(), which finds p.cmd nil and returns having sent no
	// signal and armed no escalator. `p.cmd == nil` means two different things
	// there, "no child yet" and "the child is already reaped", and terminate()
	// could not tell them apart. The child was then spawned BEHIND a stop that
	// had already given up on it: exec.Command is not CommandContext, so the
	// cancelled ctx kills nothing, cmd.Wait() below blocks on a child nobody
	// has asked to leave, and stop() waits out its whole deadline before
	// SIGKILLing on the way past. Measured window: a Stop landing 50µs to ~2ms
	// after Start reproduced it 25 times out of 25.
	//
	// For the selector that is a twelve-second teardown -- issue #126's seam 6,
	// whose predecessor's own 7.3s teardown pushed the next sweep to within a
	// millisecond of the new feed's Start -- during which the outgoing feed
	// keeps publishing into the same hub the replacement is about to join, with
	// its timestamps advancing past the offset the replacement was already
	// given. For a destination it is worse: SIGKILL without the SIGTERM that
	// precedes it is FFmpeg's output never finalised, which is a recording that
	// holds a header and nothing else.
	//
	// Checked AFTER the publish, and that ordering is the proof rather than a
	// preference. stop() cancels before it calls terminate(), so a terminate()
	// that saw nil ran after the cancel and before this publish -- which puts
	// the cancel before this check, and this check therefore sees it. A
	// terminate() that saw a non-nil cmd ran after the publish, signalled, and
	// left p.signalled set, so the call below is the no-op it should be. There
	// is no interleaving in which both fire and none in which neither does.
	if ctx.Err() != nil {
		p.terminate()
	}

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
	drained := make(chan struct{})
	go func() { wg.Wait(); close(drained) }()

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

	// THE REAP FIRST, and on its own. cmd.Wait() here waits for the child and for
	// nothing else: the pipes above belong to this package, so exec has no
	// copying goroutine to join and no descriptor of ours to close. Whatever is
	// still holding stdout open cannot delay this.
	waitErr := cmd.Wait()
	// Announce the reap before the drain, because that is what terminate()'s
	// escalation is asking about: a child that has been reaped needs no SIGKILL,
	// whether or not its grandchild is still writing.
	close(exited)

	// THEN THE DRAIN, BOUNDED. In the ordinary case -- no surviving descendant --
	// the write ends closed when the child died, the drain has already seen EOF,
	// and this select takes its first arm immediately: stderr is fully captured
	// and LastError is exactly what it was before.
	//
	// When a descendant IS holding the pipe, closing the read ends unblocks the
	// drain goroutines with "file already closed" instead of leaving them parked
	// on a writer nobody can account for. The wait after the close is not
	// optional: lastLines is read below, and abandoning the goroutines that write
	// it would be a data race and would truncate LastError non-deterministically.
	select {
	case <-drained:
	case <-time.After(p.drain):
		p.log.Warn("stdout/stderr still open after the child was reaped; "+
			"closing them (a descendant inherited the pipe)", "after", p.drain)
		_ = stdout.Close()
		_ = stderr.Close()
		<-drained
	}
	_ = stdout.Close()
	_ = stderr.Close()

	p.cmdMu.Lock()
	p.cmd = nil
	p.exited = nil
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
	cmd, exited := p.cmd, p.exited
	// IDEMPOTENT PER SPAWN. Two callers reach this for one cmd -- stop(), and
	// runOnce when it finds the ctx already cancelled -- and only one of them
	// may signal, because FFmpeg answers a repeated SIGTERM by exiting without
	// flushing.
	already := p.signalled
	if cmd != nil && cmd.Process != nil {
		p.signalled = true
	}
	p.cmdMu.Unlock()
	if cmd == nil || cmd.Process == nil || already {
		return
	}
	signalGroup(cmd)

	// Escalate if it has not gone after the grace period. FFmpeg normally
	// exits in well under a second; anything longer is wedged.
	//
	// A SELECT, NOT A SLEEP. The sleep this replaces ran to completion on every
	// stop, including the overwhelming majority that are over in milliseconds: a
	// supervisor stopping forty destinations paid forty goroutines each parked
	// for the full 8 seconds, and the only thing standing between a stale one and
	// an innocent successor was pointer identity on p.cmd. `exited` is closed by
	// runOnce when THIS cmd is reaped, so the ordinary case costs one timer and
	// returns at once, and the escalating case cannot be about anybody else's
	// child -- the channel was allocated with this cmd and is never reused.
	p.escalators.Add(1)
	go func() {
		defer p.escalators.Done()
		t := time.NewTimer(p.grace)
		defer t.Stop()
		select {
		case <-exited:
			// Reaped inside the grace period. There is nothing to escalate to,
			// and killGroup on a reaped pid is a signal to whoever holds that
			// number now.
		case <-t.C:
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

// appendLog records one stderr line and fans it out.
//
// MASKED HERE, at the single point every copy is made from, rather than in
// Logs() where it used to be. Masking on the way out covered the reader that
// prompted it and missed the two that leave the process entirely:
//
//   - LogSink is FileSink in production, so an unmasked line became a
//     PERMANENT one in process.log -- and a log file is the artifact people
//     attach to bug reports and ship in support tarballs, which the database
//     beside it never is.
//   - OnLog publishes to the event bus, which the console's live log panel
//     reads over the WebSocket. Authenticated, so no boundary is crossed, but
//     it is precisely the panel an operator screenshots.
//
// Safe to do at construction because classify() has already run on the raw
// line in the scan loop above: nothing downstream of here needs the
// unmasked text, and p.logs.add is the only writer of the ring.
//
// The cost is one short string scan per stderr line. That is the right trade
// against a key written to disk once and kept for ever.
func (p *Process) appendLog(text, level string) {
	l := LogLine{
		Time:    time.Now(),
		Process: p.spec.Name,
		Text:    p.scrub(text),
		Level:   level,
	}
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
		LastError: p.scrub(p.lastErr),
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
// The ring is ALREADY masked -- see appendLog, which is the single point every
// copy is made from. Masking only here, as this first did, covered this reader
// and missed LogSink writing the raw line permanently into process.log and
// OnLog publishing it to the console's live log panel.
//
// This pass stays anyway, and is not dead code by accident. It is the guarantee
// the method's own name makes: no line leaves through Logs() unmasked, whatever
// a future writer to the ring forgets. Redact is idempotent, so running it over
// already-clean text costs a scan and changes nothing.
//
// What "masked" means here is scrub, NOT alerts.Redact. The process's exact
// declared credential literals are removed first and Redact runs afterwards only
// as a residual pass over whatever it can recognise. The distinction is the
// whole of #150's argv finding: Redact alone left `-rtmp_conn S:<key>` and the
// backslash-escaped form of an Authorization header in the clear, and both
// reached a read-scoped API token through this method.
//
// The diagnostic value an operator came for survives either way: the scheme, the
// host, "Connection refused" and the frame counters are none of them
// credentials and none of them declared, so nothing removes them.
func (p *Process) Logs() []LogLine {
	lines := p.logs.snapshot()
	for i := range lines {
		lines[i].Text = p.scrub(lines[i].Text)
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
