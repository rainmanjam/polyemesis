package engine

import (
	"io"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/procname"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// The relay subscriptions that have no row behind them. A name is both a
// consumer's subscription on its hub and the key its wake is sent to, so each is
// written once, here, and used for Subscribe, Unsubscribe and relayConsumer
// alike: Hub.Wake on a name nothing subscribed under returns false silently.
const (
	recorderSubName = "recorder"
	previewSubName  = "preview"
	metersSubName   = "meters"
)

// relayConsumer is one supervised child that reads a relay hub: what it is
// called, what it is, which hub it subscribed to and under which name, and the
// command it runs.
type relayConsumer struct {
	// name is the supervised process's name, as the API and the logs know it.
	name string
	// sub is the subscription the child holds on hub. Usually the same string as
	// name; the selector's copy hops are the exception.
	sub  string
	kind procname.Kind
	// hub is the hub the child ACTUALLY reads -- the ingest's, a rendition's, the
	// selector's -- not necessarily e.hub. A wake sent to the wrong hub is a wake
	// nothing receives.
	hub  *relay.Hub
	args []string
	// secrets are the EXACT credential literals that appear anywhere in this
	// child's argv, its stderr or an error it produces; the supervisor removes
	// those and only those from everything it shows a reader. Every site states
	// them, or says in secretsExempt why none exists (see
	// TestEverySupervisorSpecDeclaresItsSecrets): the leak that guard exists for
	// is one construction site that did not say what was on its command line.
	secrets []string
}

// relayConsumerSpec is the supervisor.Spec every relay consumer starts from, and
// the one place the things all of them need are set:
//
//   - OnLog, OnState and LogSink, so its output reaches the log panel, the
//     persisted log and the status stream like every other child's;
//   - AutoRestart, which the callers that rebuild their own command on a respawn
//     (the selector's copy hops, which need a current timestamp offset) switch
//     off;
//   - WakeOnStop, which is what lets a child stopped on a quiet feed exit. A
//     consumer blocks in a timeout-less read on purpose (it has to ride through
//     a quiet patch), so after its publisher leaves FFmpeg ignores the single
//     SIGTERM and is SIGKILLed at the end of the grace -- no trailer on a file,
//     no flush on a session. It was set on three of eleven consumers and
//     forgotten on the rest: the preview, renditions, playout variants, meters
//     and selector feeds sat out the whole grace. See supervisor.Spec.WakeOnStop.
//
// A caller adds what is its own (Secrets, NextArgs, a progress handler, restart
// policy) to the returned Spec before building the process.
func (e *Engine) relayConsumerSpec(c relayConsumer) supervisor.Spec {
	return supervisor.Spec{
		Name:        c.name,
		Kind:        c.kind,
		Bin:         e.tools.FFmpeg,
		Args:        c.args,
		AutoRestart: true,
		OnLog:       e.onLog,
		OnState:     e.onState,
		LogSink:     logSink{e},
		WakeOnStop:  relayWaker(c.hub, c.sub),
		Secrets:     c.secrets,
	}
}

// newRelayConsumer builds the process for a consumer that needs nothing beyond
// the shared Spec.
func (e *Engine) newRelayConsumer(c relayConsumer) *supervisor.Process {
	return supervisor.New(e.log, e.relayConsumerSpec(c))
}

// newMeterConsumer builds the process for a consumer whose stdout carries
// measurements instead of -progress: the ingest meter and the loudness monitors.
//
// Overriding StdoutHandler takes the progress parser away, and with it the only
// thing that tells the supervisor media is flowing, so the uptime clock never
// starts and the card reads --:--:-- for ever. parse is handed noteMedia to call
// for every reading, which is the convention two sites used to copy from each
// other.
func (e *Engine) newMeterConsumer(c relayConsumer, parse func(r io.Reader, noteMedia func()) error) *supervisor.Process {
	spec := e.relayConsumerSpec(c)
	var proc *supervisor.Process
	spec.StdoutHandler = func(r io.Reader) error {
		return parse(r, func() { proc.NoteMedia() })
	}
	proc = supervisor.New(e.log, spec)
	return proc
}

// destConsumer is a destination's FFmpeg: the relay consumer plus what makes it
// a destination. The primary and the backup are the same thing writing to two
// endpoints, so they are built here and not by two hand-written Specs that can
// drift -- and had: the backup lacked the publishing signal and the #674
// re-probe handler, so a backup that characterised its audio before any existed
// published nothing for ever, where the primary restarted to probe again.
type destConsumer struct {
	relayConsumer
	row *db.Destination
	// label names this output in the log: the destination's name, with
	// " (backup)" for the redundant one.
	label string
	// nextArgs rebuilds the command for a respawn. A file destination needs a
	// fresh output path each time; the rest read the graph holder so a live
	// level survives a reconnect.
	nextArgs func() []string
	// startDelay spaces out the FIRST spawn only; a reconnect is never delayed.
	startDelay time.Duration
}

// newDestinationProcess builds a destination's process and the signal that says
// whether it has ever published.
//
//   - OnProgress is the only signal that says the output is PUBLISHING rather
//     than merely spawned (see firstMediaLogger and #675), and the watch it
//     returns is what the #674 re-probe reads to tell a stuck destination from
//     one riding a switch.
//   - OnLog carries the #674 handler: a destination that probed before its audio
//     existed can only be restarted. The trigger is the demuxer's own "could not
//     find codec parameters" rather than an absence of output, because absence
//     of output is also what a mismatched publisher and a still-probing child
//     look like.
//   - LiveCommands makes stdin the channel for gain and mute.
//   - The reconnect policy is the row's, and the same three values are
//     re-applied without a restart by applyDestPolicy when they change; a key
//     or codec the platform keeps refusing stops being retried
//     (destHoldAfterQuickRejections), and Retry lifts it.
func (e *Engine) newDestinationProcess(c destConsumer) (*supervisor.Process, *destWatch) {
	onProgress, watch := e.firstMediaLoggerWatched(c.label, c.row.Kind)
	pol := destPolicy(c.row)

	spec := e.relayConsumerSpec(c.relayConsumer)
	spec.NextArgs = c.nextArgs
	spec.LiveCommands = true
	spec.OnProgress = onProgress
	spec.MinBackoff, spec.MaxBackoff, spec.MaxRestarts = pol.MinBackoff, pol.MaxBackoff, pol.MaxRestarts
	spec.HoldAfterQuickRejections = destHoldAfterQuickRejections
	spec.StartDelay = c.startDelay

	// Declared first because the re-probe handler closes over the process it is
	// installed on, and so can only resolve it lazily.
	var proc *supervisor.Process
	spec.OnLog = e.reprobeOnUncharacterisedAudio(c.label, c.row.Kind,
		func() *supervisor.Process { return proc }, e.onLog)
	proc = supervisor.New(e.log, spec)
	return proc, watch
}

// relayWaker is the Spec.WakeOnStop for a consumer subscribed to hub as name.
//
// Safe after the fact by construction: Wake does nothing for a name the hub no
// longer holds, and every teardown here stops the process BEFORE it
// unsubscribes, so the subscription is still there for as long as the wake can
// be needed. The one overlap: a Stop whose context expires before its child is
// reaped returns while the escalator is still running, and a successor that
// subscribes under the same name in that window can be sent a wake too. That
// costs it nothing it would notice -- a wake only ever reaches a feed that is
// already silent, and the empty PES it opens carries no payload.
func relayWaker(hub *relay.Hub, name string) func() {
	if hub == nil {
		return nil
	}
	return func() { hub.Wake(name) }
}
