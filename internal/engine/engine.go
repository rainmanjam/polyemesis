// Package engine is the orchestrator. It owns the relay hub and every
// supervised FFmpeg process, and keeps them reconciled against the database.
//
// The central idea is reconciliation, not commands: the API mutates rows and
// calls Reconcile, and the engine works out what must start, stop or restart.
// That is what makes "changing a routing profile restarts only that
// destination" fall out naturally instead of needing a special code path.
//
// Renditions are the same idea one level up: a shared video encode is a hub of
// its own with a supervised encoder feeding it, ref-counted by the enabled
// destinations that select it, and reconciled in dependency order so no
// destination is ever left reading a relay that has gone away.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/clips"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/meters"
	"github.com/rainmanjam/polyemesis/internal/multitrack"
	"github.com/rainmanjam/polyemesis/internal/playout"
	"github.com/rainmanjam/polyemesis/internal/recording"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
	"github.com/rainmanjam/polyemesis/internal/stats"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// relayPortBase is where per-consumer loopback ports are allocated from.
const (
	relayPortBase = 21000
	relayPortSpan = 500
	// stopTimeout bounds how long we wait for one child to exit.
	//
	// It must stay above supervisor.shutdownGrace (8s), which is the only thing
	// that ends a child that does not heed the signal: a teardown that gives up
	// first turns every unheeded stop into ErrStopDeadline and abandons a child
	// the grace escalation would have reaped a moment later.
	//
	// EVERY TEARDOWN IN THIS PACKAGE WRITES `_ = proc.Stop(ctx)`, deliberately
	// and with one exception. The teardowns below are all "this tier is going
	// away": the port and the subscription are released whatever the answer, and
	// there is no second thing to try, so ErrStopDeadline changes nothing they
	// do. The exception is teardownDest, whose caller -- POST
	// /destinations/{id}/stop -- reports the outcome to a human and therefore
	// needs to know which of Stop's two arms ran (#209). The `_ =` is not a
	// shrug: internal/testenv's TestEveryDiscardedStopSaysSo requires it, so
	// that a discard nobody thought about looks different from these (#196).
	//
	// It is a package var rather than a const for one reason: a test that needs
	// Stop's DEADLINE arm cannot afford to wait 12 real seconds for it, and 12s
	// is not the property such a test is about. Nothing outside a test writes it.

	// previewIdleDefault is how long the preview encoder outlives the last
	// playlist request when settings do not say.
	previewIdleDefault = 30 * time.Second
	// previewFlowGrace is how long after the last byte the preview still counts
	// its hub as live. Wider than previewSweep so an ordinary sampling gap
	// cannot read as a dead stream, and far narrower than previewIdleDefault
	// because a stream that ended should not hold an encoder for half a minute.
	previewFlowGrace = 10 * time.Second
	// previewSweep is how often idleness is re-evaluated. It is well under the
	// idle window so the stop lands near the deadline without a timer per
	// request.
	previewSweep = 5 * time.Second
	// previewStartDebounce keeps a burst of requests against a down encoder,
	// or a start that keeps failing, from turning into a burst of spawns.
	previewStartDebounce = 2 * time.Second
	// gpuDetectTimeout bounds the DRM enumeration. A wedged driver must not
	// hold up starting an encode.
	gpuDetectTimeout = 5 * time.Second
)

// stopTimeout bounds how long we wait for one child to exit. See the comment in
// the const block above for why it is 12s and why it is a var.
var stopTimeout = 12 * time.Second

// Engine owns the whole streaming pipeline.
type Engine struct {
	// sourceID is the programme this engine owns. One engine per source: the
	// hub, the ingest, the recorder, the meters and the whole destination and
	// rendition map are all per-instance already, so running N of them is what
	// makes N programmes work rather than a rewrite of the reconciler.
	sourceID int64

	log   *slog.Logger
	cfg   config.Config
	store *db.DB
	tools *ffmpeg.Tools
	bus   *events.Broker
	hub   *relay.Hub
	alloc *relay.PortAllocator
	mon   *stats.Monitor
	// host is the process-wide resource sampler, owned and run by the manager.
	// READ ONLY from here: see New for why one engine must not sample the box
	// on behalf of the others. Nil on an engine built outside a manager.
	host   *stats.Host
	recman *recording.Manager
	// loudStore is the latest loudness report per destination. It carries its
	// own lock and is written from every analyser's stdout goroutine, so it is
	// deliberately outside e.mu.
	loudStore *meters.Store
	// play is the public HLS/DASH origin. It owns its own processes and
	// directory; the engine owns only the order it is reconciled in, which is
	// the part that matters — a variant reads a rendition hub, so it must come
	// up after that rendition and go down before it.
	play *playout.Manager

	// sink persists captured stderr beyond the in-memory ring. One sink for
	// every child on purpose: the file reads as a single interleaved timeline.
	// It is read on the stderr goroutine of every child and swapped whenever
	// the logging settings change, so it is atomic rather than under e.mu.
	sink atomic.Pointer[supervisor.FileSink]

	// meterInterval is the metering throttle, in nanoseconds.
	//
	// Atomic rather than under e.mu because it is read on the metering child's
	// stdout goroutine for every astats frame -- up to fifty a second -- and
	// taking the engine lock at that rate to answer a question that changes
	// once a month would be absurd. It exists at all because the value used to
	// be captured into the StdoutHandler closure at spawn time, which made
	// editing it a silent no-op.
	meterInterval atomic.Int64

	// feedGen numbers every source feed this engine has ever started, so the
	// seam ledger can name the outgoing and incoming feed of one switch without
	// ambiguity. Deliberately NOT a field on the selector: a tier that is torn
	// down and rebuilt would restart its own counter, and two feeds sharing a
	// number is precisely the confusion the ledger exists to remove when a run
	// is read back hours later. Atomic rather than under e.mu because it is
	// incremented inside startFeed, which already runs under selMu, and a second
	// lock ordering for a counter would be all cost.
	feedGen atomic.Uint64

	// reloadRec is the note collector for the reconcile currently in flight,
	// nil the rest of the time. Atomic because it is read from teardown paths
	// that hold e.mu.
	reloadRec  atomic.Pointer[reloadRecorder]
	lastReload atomic.Pointer[ReloadReport]
	sinkMu     sync.Mutex
	sinkCfg    db.LoggingSettings

	// stopMu guards unreaped.
	//
	// Its own lock and not e.mu: it is written from teardownDest, which already
	// runs under a reconcile, and read from Status(), which runs per WebSocket
	// push. Folding it into e.mu would put a per-push read behind a reconcile.
	stopMu sync.Mutex
	// unreaped records, per destination id, that the LAST stop of that
	// destination ended on Stop's DEADLINE arm rather than on its reap arm --
	// SIGKILL issued and not waited for, the child possibly still running.
	//
	// It exists because both arms set StateStopped, so the process state cannot
	// answer the one question a caller of POST /destinations/{id}/stop actually
	// has: may I reuse what it was holding? The supervisor already produces the
	// fact as ErrStopDeadline; before this, teardownDest threw it away one line
	// after it was computed and the API answered "stopped" either way (#209).
	//
	// Keyed by destination id and cleared when that destination next starts, so
	// it describes the current situation rather than accumulating history.
	unreaped map[int64]string
	// rolledOver records, per destination id, the path a file destination's
	// respawn was actually given when it differed from the configured one.
	//
	// Under stopMu with unreaped because it is the same KIND of state: a fact
	// about something that already happened, observed once, read back by Status
	// so an operator can be told. Neither belongs on the destination struct --
	// both outlive the process they describe.
	rolledOver map[int64]string
	// retiring holds destinations that have left e.dests but whose child has not
	// been confirmed dead, keyed by destination id and guarded by e.mu with it.
	//
	// IT EXISTS BECAUSE Status() COULD OTHERWISE REPORT A LIVE CHILD AS ABSENT.
	// stopDestinations deletes the entry, releases e.mu, and only then calls
	// teardownDest -- which does not ask the child to stop until much later. A
	// status read landing in that window found no destination and published a nil
	// Process for a destination that was delivering. Measured at 1 run in 6 on a
	// suite that reads status across a reconcile, where it reads as "the
	// destination died" (#462).
	//
	// A SEPARATE MAP RATHER THAN A FLAG ON destination, because the entry is gone
	// from e.dests by then -- that is the whole problem -- so there is nothing
	// left to carry a flag.
	retiring map[int64]*destination

	// vaapiOnce guards a single DRM-node enumeration, done lazily the first
	// time a VAAPI rendition actually starts.
	vaapiOnce sync.Once
	vaapiDev  string

	// reconcileMu serializes Reconcile end to end. It is NOT e.mu: e.mu is
	// dropped and retaken a dozen times inside one reconcile so that Status()
	// stays answerable while children are being spawned, which is exactly what
	// leaves two concurrent reconciles free to interleave.
	//
	// The hazard is not theoretical. startDestinations checks e.dests[id] ==
	// nil, unlocks, and only publishes at the end of startDest -- after
	// Allocate() and hub.Subscribe(). Two reconciles can both see nil, and
	// relay.Hub.SubscribeAddr is a bare map assignment on a deterministic name
	// (see destSubName), so the second REPLACES the first: the first FFmpeg
	// keeps running against a port the hub no longer sends to, its
	// *destination is overwritten in e.dests, and nothing will ever stop it or
	// release its port. The callers are genuinely concurrent -- five HTTP
	// handlers, the scheduler's actuator, and observeLoop.
	//
	// Taken OUTSIDE every other lock this file has, and it is the only one held
	// for the whole of a public method. Verified deadlock-free by the fact that
	// nothing holding e.mu, selMu or previewMu calls Reconcile: SwitchSource
	// holds selMu and does not, sweepPreview holds previewMu and does not, and
	// onCaptionsDegraded -- the one callback that does -- explicitly hands it
	// to a fresh goroutine rather than calling it inline.
	reconcileMu sync.Mutex

	// reconciles counts completed Reconcile calls, for tests that need to prove
	// the work was ATTEMPTED rather than skipped.
	//
	// A field rather than a package var, for the reason passwordCost gives
	// above: a package-level seam is order-dependent under -count=N and cannot
	// run in parallel with anything else. Atomic because Reconcile is called
	// from several goroutines and a test reads it from another.
	reconciles atomic.Int64

	mu       sync.RWMutex
	ingest   *supervisor.Process
	recorder *supervisor.Process
	preview  *supervisor.Process
	// previewHub is the hub the running preview subscribed to, kept so the stop
	// path unsubscribes from the one it actually joined and so a swap underneath
	// it can be noticed. Nil while no preview is running.
	previewHub *relay.Hub
	// previewRxBytes and previewRxAt track whether that hub has ADVANCED, which
	// is a different question from whether it has ever carried anything: a hub
	// that has gone quiet keeps its total, so a `RxBytes() > 0` test answers yes
	// for ever after the first byte.
	previewRxBytes uint64
	previewRxAt    time.Time
	// previewRxHub is the hub previewRxBytes was read from. A counter is only
	// comparable with itself: a DIFFERENT hub starts at zero, and comparing its
	// zero against the old hub's total reads as "the number changed", which is
	// the opposite of the truth.
	previewRxHub *relay.Hub
	meters       *supervisor.Process
	dests        map[int64]*destination
	rends        map[int64]*rendition
	// silence is the synthetic-audio tier, nil unless the ingest probed with
	// no audio at all. See silence.go.
	silence *silenceTier
	// sel is the source-selector tier, nil unless failover settings enable it.
	// It owns the one hub every downstream consumer subscribes to for its whole
	// life, which is what lets the source behind it change without restarting a
	// single destination.
	sel *selector
	// backup is the second listener, nil unless the tier is running one.
	backup *backupIngest
	// playlist is the file-playout tier, nil unless settings enable it. It is a
	// sibling of backup and not of the slate: both are a supervised process with
	// a hub of its own, running whether or not anybody is watching.
	playlist *playlistTier
	// heldSilenceSig freezes the silence tier's signature while the selector is
	// standing in for a departed primary. See holdSilence.
	heldSilenceSig string
	// loud is the per-destination EBU R128 analyser set, keyed by destination
	// id. See reconcileLoudness: these are measurement processes and nothing
	// downstream depends on one, so they are reconciled last and their failure
	// costs a number on a dashboard rather than a stream.
	loud map[int64]*loudnessMon
	// loudOff is the operator's override of the analyser tier, pending a
	// settings field of its own. See SetLoudnessMonitor.
	loudOff bool
	// clip is the rolling capture buffer, nil unless it has been switched on.
	clipCap  *clips.Capturer
	clipCfg  clips.Config
	clipOn   bool
	clipSig  string
	clipPort int
	// clipHub is which relay the buffer subscribed to, held for the same
	// reason metersHub is: an orphaned subscription forwards to a port the
	// allocator has since handed to somebody else.
	clipHub *relay.Hub

	// capt is the realtime captioner, nil unless it has been switched on.
	//
	// It is the one consumer in this file that deliberately competes with the
	// destination encoders for CPU instead of yielding to them, which is why it
	// is off by default, why it runs under the governor's nice wrapper, and why
	// it switches ITSELF off the moment it cannot keep up. See
	// internal/transcribe/live.go, and reconcileCaptions below.
	capt     *transcribe.LiveCaptioner
	captOn   bool
	captCfg  transcribe.LiveConfig
	captSig  string
	captPort int
	captHub  *relay.Hub
	captVTT  *transcribe.LiveVTT
	// captWarn is why captioning stopped, kept after the captioner is gone so
	// the operator finds out. A caption stream that vanishes silently is the
	// failure mode this whole feature is designed against.
	captWarn string

	// whisper is the detected whisper.cpp, nil on an install without it, and
	// whisperDir where its models live. Both are set once by SetTranscriber.
	whisper     *transcribe.Tools
	whisperDir  string
	whisperNice func(name string, args []string) (string, []string)

	// alerter delivers webhooks and alertWatch decides what is worth
	// delivering. Both are outside e.mu: the watcher is touched only by
	// alertLoop, and the notifier is explicitly non-blocking, which is the
	// whole reason a slow endpoint cannot reach the reconcile loop.
	alerter *alerts.Notifier
	// hooks delivers lifecycle webhooks and hookWatch derives their edges.
	//
	// The dispatcher is SHARED across every engine -- it is handed in by the
	// manager, not built here -- because a sequence number, a delivery log and
	// a retry queue all belong to the endpoint, and an endpoint is subscribed
	// to by the whole install rather than by one programme. The watcher is
	// per engine, because "has this source been publishing" is per source.
	//
	// Both may be nil on an Engine assembled field by field, which is how the
	// tests build one; every use is nil-safe.
	hooks     *hooks.Dispatcher
	hookWatch *hooks.Watcher
	// lifecycle is the THIRD consumer of the edges hookWatch derives, and it is
	// a consumer rather than a second sampler on purpose -- see observeLoop.
	// It decides when a platform's broadcast goes live and when it ends.
	//
	// An interface rather than a concrete type because the implementation lives
	// in internal/api, which imports this package. Nil on an engine assembled
	// field by field, which is how the tests build one; every use is nil-safe.
	lifecycle  LifecycleObserver
	alertWatch *alerts.Watcher
	// sched flips destinations' enabled flags on a timetable, through the same
	// path a human uses.

	// playProcs mirrors the manager's running variants so the monitoring page
	// can list them beside every other child. The manager hands out its
	// processes as an opaque Runner, so this is the only place that still knows
	// they are supervisor.Processes.
	playProcs map[string]*supervisor.Process

	// *Sig fields hash the inputs that would require restarting the
	// corresponding process. Comparing them is what keeps an unrelated
	// settings change from cycling a healthy pipeline.
	ingestSig    string
	recorderSig  string
	previewSig   string
	metersSig    string
	recorderPort int
	previewPort  int
	metersPort   int
	// metersHub is which relay the sidecar subscribed to — the ingest's, or the
	// silence tier's. Held so it is unsubscribed from the same one, because an
	// orphaned subscription forwards to a port the allocator has since handed
	// to somebody else.
	metersHub *relay.Hub

	// sourceState is the ingest layout and the flags that say what it is.
	// Embedded, so every existing e.source / e.measured / e.probed still reads
	// the same -- see sourcestate.go for the invariant that ties them together
	// and for the five bugs that came from confusing two of them.
	sourceState
	// sourceName is the operator's label for this programme, refreshed on every
	// reconcile. Cached rather than read on demand: Status() runs per WebSocket
	// push and per telemetry tick, and a database read on that path buys
	// nothing -- a rename cannot happen without a reconcile.
	sourceName string
	// sourceToken is this programme's publish secret, refreshed on the same
	// reconcile as sourceName.
	//
	// It is here because it is not just a credential any more, it is an ADDRESS:
	// the RTMP ingest child dials rtmp://127.0.0.1:PORT/live/<token> to
	// subscribe to what the encoder published, so building that command needs
	// the token in hand. db.Settings carries the ingest block but not the token,
	// and reaching for the row inside ingestSpec would put a database read in
	// the middle of command construction for no benefit -- a rotation cannot
	// happen without a reconcile either.
	sourceToken string
	// probeFailed is set while probes are failing, so the warning is logged on
	// the transition rather than on every 3s retry.
	probeFailed atomic.Bool
	// probeFails counts CONSECUTIVE probe failures, so a layout that cannot be
	// measured can be told apart from one that has merely not been measured
	// yet. See probeUnmeasurable and the hold in reconcileOutputs.
	probeFails atomic.Int64
	// destHold is why every destination is currently unplanned, or "" when they
	// are being planned normally. Written by reconcileOutputs from the holdDests
	// decision, read by Status.
	//
	// Atomic rather than under e.mu for the same reason probeFails is: the
	// decision is made in the middle of reconcileOutputs, which takes and drops
	// e.mu around the pieces it needs, and reaching for the write lock there to
	// publish one string would put this on the inside of a lock ordering it has
	// no business being part of. Nothing reads it together with another field.
	destHold atomic.Value // *HoldStatus, nil when nothing is held
	// holdSince is when the CURRENT hold began, unix nanos, 0 when nothing is
	// held. Atomic for the same reason probeFails is: read from the probe loop
	// and written from reconcile.
	holdSince atomic.Int64

	levels   ffmpeg.Levels
	levelsAt time.Time
	settings db.Settings

	// mtClient negotiates Twitch Enhanced Broadcasting. Nil is the production
	// value and means multitrack.Client's zero value, which talks to the real
	// endpoint. It is a field solely so a test can point the negotiation at an
	// httptest server through multitrack.Client.BaseURL -- the one seam that
	// package has, and one nothing at runtime writes.
	//
	// NOT UNDER e.mu and not atomic: it is written once, before the engine is
	// started, by a test in this package, and read on the reconcile goroutine.
	mtClient *multitrack.Client

	// previewMu serializes preview lifecycle changes. Unlike every other
	// child, the preview is started from an HTTP handler, so two playlist
	// requests can race to spawn it.
	previewMu sync.Mutex
	// selMu serializes every change to the selector tier, because the failover
	// sweep, a reconcile and an operator's manual switch can all reach it at
	// once. Always taken BEFORE e.mu, never the other way round.
	selMu sync.Mutex
	// selPanicMsg, selPanicAt and selPanicRelog remember and bound the last
	// selector decision panic this engine logged, so a persistent one -- e.g.
	// a map entry a future change forgot, hit on every sweep until an
	// operator or a deploy fixes it -- costs one stack walk and one full log
	// line, not two of each every second. Read and written only from
	// decideSource, which every caller (both reconcileSelector windows,
	// SwitchSource, and the sweep) reaches with selMu already held, so none
	// of the three fields needs a lock of its own.
	selPanicMsg string
	selPanicAt  time.Time
	// selPanicRelog is the window; its zero value means selPanicRelogDefault
	// (see decideSource), so a production Engine -- which never sets this
	// field -- gets exactly that window without any constructor having to
	// initialise it.
	selPanicRelog time.Duration
	// decideFn is a test seam that substitutes decideSource's call to
	// chooseSource. Nil -- always true in production -- means "use the real
	// chooseSource", so leaving it unset is what keeps decideSource's
	// behaviour unchanged: this field only ever matters to a test that sets
	// it.
	//
	// decideFn and selPanicRelog are fields on Engine, not package-level
	// vars, because a package-level var is shared by every Engine that
	// exists in the test binary, not just the one under test: a leaked
	// selectorLoop goroutine from an unrelated engine built with New() (see
	// manager_test.go, which starts one on every engine whether or not
	// failover is enabled) would read a package-level var concurrently with
	// a test in this package writing it -- a real `go test -race` failure,
	// and one that would only start firing once a production panic became
	// reachable, which is exactly what the planned fourth candidate kind
	// risks.
	//
	// What actually makes setting these two fields safe in this package's
	// tests is narrower than "before the engine is used": it is that those
	// tests build their engine directly as &Engine{...} (see failoverEngine
	// and TestSlateSpecFollowsTheProbedIngestRatherThanAForm) rather than
	// through New(), so no selectorLoop -- or any other goroutine of this
	// particular engine's -- ever exists to read either field concurrently.
	// Setting decideFn only after reconcileSelector has already built the
	// tier, as TestSwitchSourceRollsBackPinOnARecoveredPanic does, is still
	// safe on that same basis: reconcileSelector runs synchronously on the
	// test's own goroutine and starts no goroutine that reads decideFn.
	decideFn func(sourceChoice) (sourceKind, string)
	// previewSeen is the last playlist request and previewAt the last start
	// attempt; together they drive on-demand start and idle stop.
	previewSeen time.Time
	previewAt   time.Time
	// stopped closes the door on a playlist request that arrives while the
	// engine is shutting down, which would otherwise leave an orphan encoder.
	stopped bool
	// afterPublish, when set, runs in the window between a destination being
	// published into e.dests and its process being started -- the gap e.stopped
	// cannot close, because a Stop that lands inside it passes the guard and
	// then tears down a process that has not started yet.
	//
	// A seam rather than a sleep: the window is a few instructions wide and no
	// timing test could sit in it reliably. Set before anything concurrent
	// exists and never written again, on the same basis decideFn above is safe.
	// Nil in production, and the two reads are one nil check per destination
	// start.
	afterPublish func()

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// destination is one running output.
type destination struct {
	row      *db.Destination
	proc     *supervisor.Process
	port     int
	subName  string
	compiled routing.Result
	// hub is where this destination reads from: the ingest hub for a
	// passthrough destination, its rendition's own hub otherwise. Held so
	// teardown unsubscribes from the same hub it subscribed to.
	hub *relay.Hub
	// watch says whether media has ever moved through this child. Read by the
	// #674 re-probe: a destination that has NEVER published is the one that may
	// have characterised its input before the ingest carried audio. One that
	// has published is riding a switch and must not be disturbed.
	watch *destWatch
	// spec is a hash of everything that would require a restart. Comparing it
	// is what keeps an unrelated edit from cycling a healthy stream.
	spec string
	err  string

	// The redundant output, when this destination has one. A SEPARATE
	// supervised process on a separate subscription and port, which is what
	// makes "the backup cannot take the primary down" true rather than hopeful
	// -- the supervisor restarts each independently and neither stops the
	// other.
	backup     *supervisor.Process
	backupPort int
	backupSub  string
	// backupSpec is the backup's OWN restart hash, deliberately not derived
	// from spec. The two must cycle independently: a rotated backup key has to
	// restart only the backup, and nothing about the backup may ever restart
	// the primary.
	backupSpec string
	// backupErr explains why there is no backup when one was asked for --
	// no endpoint offered, or no relay port left. Reported rather than logged
	// alone, because an operator who enabled redundancy and silently did not
	// get it is worse off than one who never tried.
	backupErr string

	// multitrack is what the Enhanced Broadcasting negotiation for THIS start
	// decided. Zero for every destination that did not ask, which is nearly
	// all of them.
	//
	// HELD RATHER THAN ONLY LOGGED, because the operator's question is "what is
	// this destination doing right now" and a line that scrolled past at
	// go-live cannot answer it. It is written once, on the start that produced
	// it, and never mutated -- Status hands these pointers out and reads them
	// after dropping the lock, which is only safe while a published destination
	// never changes again.
	multitrack mtDecision
	// vodDropped explains why this destination's second audio mix is not on the
	// wire, empty when there is nothing to explain. Separate from multitrack
	// because the two have different causes: the pair can be refused with no
	// negotiation attempted at all.
	vodDropped string
}

// rendition is one running shared video encode.
//
// It reads the ingest hub, re-encodes VIDEO ONLY — every audio track is copied
// through untouched — and publishes to a hub of its own, which its destinations
// subscribe to instead of the ingest's. That is what lets N destinations share
// one encode while each still applies its own audio routing graph.
type rendition struct {
	row  *db.Rendition
	proc *supervisor.Process
	// hub is this rendition's own relay, the one its destinations read.
	hub *relay.Hub
	// in is the relay this encode READS: the ingest's, or the silence tier's
	// when one is synthesising a track for a video-only ingest. Held so
	// teardown unsubscribes from the same hub it subscribed to.
	in *relay.Hub
	// port and subName are its subscription on `in`, i.e. its input.
	port    int
	subName string
	// spec plays the same role as a destination's: a hash of everything the
	// encode's command line depends on, so editing an unrelated rendition or
	// renaming this one never cycles a live encode.
	spec string
	err  string
}

// New creates the engine and binds the relay hub.
// New builds the engine for one source.
//
// alloc is shared across every engine on purpose. Each one used to mint its own
// PortAllocator over the same base and span, which is harmless with a single
// engine and a silent disaster with two: both would hand out the same relay
// ports and the second programme's destinations would bind onto the first
// programme's traffic. Pass one allocator, or pass nil for a private one when
// there genuinely is only one engine (tests).
//
// host is shared for the same kind of reason and is read, never run, here: it
// describes the BOX, so N engines sampling it produced N identical readings a
// second and an unscoped API read reported whichever engine it reached. The
// manager owns the one sampler and runs it. Nil is allowed and means this
// engine publishes an empty host block on its telemetry frame, which is what a
// test engine built outside a manager gets.
func New(log *slog.Logger, cfg config.Config, store *db.DB, tools *ffmpeg.Tools, bus *events.Broker, sourceID int64, alloc *relay.PortAllocator, host *stats.Host) (*Engine, error) {
	hub, err := relay.New(log, 0)
	if err != nil {
		return nil, err
	}
	if alloc == nil {
		alloc = relay.NewPortAllocator(relayPortBase, relayPortSpan)
	}

	e := &Engine{
		sourceID:   sourceID,
		log:        log,
		cfg:        cfg,
		store:      store,
		tools:      tools,
		bus:        bus,
		hub:        hub,
		alloc:      alloc,
		host:       host,
		dests:      map[int64]*destination{},
		rends:      map[int64]*rendition{},
		loud:       map[int64]*loudnessMon{},
		rolledOver: map[int64]string{},
		retiring:   map[int64]*destination{},
		loudStore:  meters.NewStore(),
		playProcs:  map[string]*supervisor.Process{},
		// Promoted fields cannot be set in a composite literal; the placeholder
		// goes in just below.
		sourceState: sourceState{source: routing.DefaultSource()},
		// The clip buffer is described in full but switched OFF, so an upgrade
		// changes nothing at all about how much memory this process holds.
		// SetClipBuffer is what turns it on. See reconcileClips.
		clipCfg: clips.Config{
			Dir:           filepath.Join(cfg.RecordingsDir(), clips.Subdir),
			WindowSeconds: clips.DefaultWindowSeconds,
		}.Normalized(),
	}
	e.mon = stats.NewMonitor(hub.RxBytes)
	// THE ENGINE'S OWN RECORDING MANAGER, AND IT IS NOT THE MANAGER'S SHARED
	// ONE. Do not merge the pair.
	//
	// Both point at the same directory — there is one recordings directory per
	// install, not one per programme — so it is tempting to keep only the
	// shared instance. The two options below are what makes that wrong:
	//
	//	WithFFprobe measures finished segments, which only the engine that
	//	WROTE them has any business doing.
	//
	//	WithStorageGuard is ENGINE-SCOPED and is the whole reason this one
	//	exists: e.onStorage halts and resumes THIS engine's recorder child. A
	//	shared manager would have to halt every programme on the box because
	//	one volume filled, or halt none.
	//
	// Manager.Recordings() is the read-only counterpart the API asks for
	// usage, deletes and path confinement, so that those answers do not depend
	// on which engine is running, or on one running at all.
	e.recman = recording.New(log, store, cfg.RecordingsDir(), func() {
		bus.Publish(events.TypeRecordings, nil)
	},
		recording.WithFFprobe(tools.FFprobe),
		// The programme, so every row this manager indexes carries it. Nothing
		// else ever knows: the filename does not encode it and a later reader
		// cannot work it out, which is why source_id was NULL on every
		// recording ever written and the clip editor labelled every clip with
		// the default programme's track names.
		recording.WithSourceID(sourceID),
		recording.WithStorageGuard(e.onStorage),
	)
	e.play = playout.New(playout.Deps{
		Log:   log,
		Dir:   cfg.PlayoutDir(),
		Ports: e.alloc,
		// The manager never imports internal/supervisor; it asks for a child
		// and the engine decides what a child is. That is what keeps a playout
		// muxer on the same log sink, state callbacks and restart policy as
		// every other process in the pipeline.
		Spawn: func(name string, args []string) playout.Runner {
			proc := supervisor.New(e.log, supervisor.Spec{
				Name: name, Kind: "playout", Bin: e.tools.FFmpeg, Args: args,
				AutoRestart: true, OnLog: e.onLog, OnState: e.onState, LogSink: logSink{e},
			})
			return &playoutProc{e: e, name: name, Process: proc}
		},
	})
	// Both read their configuration from the database on every pass, so a rule
	// or a schedule added later takes effect without a restart, and a server
	// with neither configured does no work beyond one cheap sweep.
	e.alerter = alerts.New(log, store)
	e.alertWatch = alerts.NewWatcher(alerts.WatchConfig{})
	// Named from the source row on the first sweep; SourceRef starts with the
	// id alone so an event raised before that still identifies its programme.
	e.hookWatch = hooks.NewWatcher(hooks.SourceRef{ID: sourceID}, hooks.WatchConfig{})
	return e, nil
}

// Playout exposes the public origin so the API can mount its media handler and
// render its status.
func (e *Engine) Playout() *playout.Manager { return e.play }

// playoutProc is a supervised child the playout manager owns, registered with
// the engine for the lifetime the manager runs it.
//
// The registration is what puts a playout muxer on the monitoring page beside
// the ingest and the destinations. It hangs off Start/Stop rather than off
// Reconcile because the manager's teardown is the only event that reliably
// means "this child is gone" — polling states would leave a stopped variant
// listed as a ghost until something else happened to look.
type playoutProc struct {
	*supervisor.Process
	e    *Engine
	name string
}

func (p *playoutProc) Start() {
	p.e.mu.Lock()
	p.e.playProcs[p.name] = p.Process
	p.e.mu.Unlock()
	p.Process.Start()
}

func (p *playoutProc) Stop(ctx context.Context) error {
	p.e.mu.Lock()
	// Only if it is still ours: a restarted variant registers its replacement
	// under the same name before the old one is torn down.
	if p.e.playProcs[p.name] == p.Process {
		delete(p.e.playProcs, p.name)
	}
	p.e.mu.Unlock()
	return p.Process.Stop(ctx)
}

// Hub exposes the relay for the monitoring endpoint.
func (e *Engine) Hub() *relay.Hub { return e.hub }

// BackupHub is the failover backup's relay, or nil when no backup is running.
//
// Exposed for the same reason Hub is: the one-port SRT listener lives on the
// manager and delivers into whichever hub the presented token belongs to. A
// backup encoder addresses `<token>.backup`, so the manager needs somewhere to
// put those datagrams.
func (e *Engine) BackupHub() *relay.Hub {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.backup == nil {
		return nil
	}
	return e.backup.hub
}

// Monitor exposes host/bitrate stats.
func (e *Engine) Monitor() *stats.Monitor { return e.mon }

// ingestLiveGrace is how long after the last non-zero bitrate sample the ingest
// is still called live. The monitor samples once a second, so this is two
// missed samples — short enough to release the queue promptly when a broadcast
// ends, long enough not to flap on one late tick.
const ingestLiveGrace = 3 * time.Second

// IngestLive reports whether a stream is arriving right now.
//
// Read from the bitrate series rather than from process state, for the reason
// probeLoop spells out: an SRT or RTMP listener sits in "running" for as long
// as it waits for a publisher, which is a different question from "is the
// source arriving". This is the predicate the background-job governor gates
// every heavy task on, so it has to be a bytes-are-flowing answer or the
// governor would refuse to run anything for as long as the server was up.
//
// The monitor already samples the hub at 1 Hz for the bitrate graph; nothing
// here adds a second sampler.
func (e *Engine) IngestLive() bool { return ingestLive(e.mon.Bitrate(), time.Now()) }

// ingestLive is the decision on its own, so every boundary of it is a table
// test rather than a two-second sleep against a real monitor.
func ingestLive(samples []stats.Sample, now time.Time) bool {
	if len(samples) == 0 {
		return false
	}
	last := samples[len(samples)-1]
	return last.Kbps > 0 && now.Sub(last.Time) < ingestLiveGrace
}

// GPUBusy reports whether the live path is currently occupying a hardware
// encoder, which is the signal that stops a transcode from queueing behind the
// broadcast for the same silicon.
//
// Conservative in the permissive direction on purpose: an encoder name this
// build did not probe successfully counts as software, so an unrecognised
// encoder leaves background work RUNNING rather than blocking it forever on a
// GPU nobody can prove is busy.
func (e *Engine) GPUBusy() bool { return gpuBusy(e.Status().Renditions) }

func gpuBusy(rends []RenditionStatus) bool {
	for _, r := range rends {
		if r.Process == nil || r.Process.State != supervisor.StateRunning {
			continue
		}
		if r.Encoder != db.EncoderX264 && r.Encoder != db.EncoderX265 {
			return true
		}
	}
	return false
}

// Recordings exposes the recording manager.
func (e *Engine) Recordings() *recording.Manager { return e.recman }

// Tools exposes the detected FFmpeg.
//
// The API reads Manager.Tools() instead: the detection is install-wide and
// every engine was handed the same pointer, so asking an engine for it meant a
// build without one could not answer a question that has nothing to do with a
// programme.
func (e *Engine) Tools() *ffmpeg.Tools { return e.tools }

// hostSystem is the box's resource snapshot, taken from the sampler the
// manager owns. An engine built outside a manager has none and publishes an
// empty block rather than starting a second sampler; see New.
func (e *Engine) hostSystem() stats.System {
	if e.host == nil {
		return stats.System{}
	}
	return e.host.System()
}

// Start brings the pipeline up and begins the background loops.
func (e *Engine) Start(ctx context.Context) error {
	e.ctx, e.cancel = context.WithCancel(ctx)

	settings, err := e.effectiveSettings()
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.settings = settings
	e.mu.Unlock()

	e.wg.Add(3)
	go func() { defer e.wg.Done(); e.mon.Run(e.ctx) }()
	go func() { defer e.wg.Done(); e.recman.Run(e.ctx, e.currentRecordingSettings) }()
	go func() { defer e.wg.Done(); e.probeLoop(e.ctx) }()

	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.statsLoop(e.ctx) }()

	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.previewLoop(e.ctx) }()

	// The failover detector. It runs whether or not the tier is enabled and
	// returns immediately when it is not, so switching failover on takes effect
	// without a restart.
	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.selectorLoop(e.ctx) }()

	// The playout sweeper: the muxers prune their own windows, but a restart
	// orphans the previous run's segments and nothing else would collect them.
	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.play.Run(e.ctx) }()

	// Alerting is two goroutines that never touch each other's state: the
	// notifier owns the network and the sweep owns the transitions. The
	// scheduler is a third, and writes nothing but the enabled flag.
	if e.alerter != nil {
		e.wg.Add(2)
		go func() { defer e.wg.Done(); e.alerter.Run(e.ctx) }()
		go func() { defer e.wg.Done(); e.observeLoop(e.ctx) }()
	}

	return e.Reconcile()
}

// Stop tears every child down in dependency order and closes the relay.
// Stop tears the engine down within ShutdownBudget.
//
// Kept for callers that have no deadline of their own -- tests, and the
// single-engine paths in Manager. Anything shutting the PROCESS down must use
// StopWithin and share one context, or the per-engine budgets add up past
// what systemd allows. See shutdown_budget.go. #645.
func (e *Engine) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), ShutdownBudget)
	defer cancel()
	e.StopWithin(ctx)
}

// StopWithin tears the engine down inside the caller's deadline.
//
// The context is the WHOLE process's remaining shutdown time, not this
// engine's share of it. Engines are stopped concurrently precisely so that
// sharing one deadline does not mean dividing it.
func (e *Engine) StopWithin(ctx context.Context) {
	if e.cancel != nil {
		e.cancel()
	}

	// Consumers first, then the renditions they read, then the ingest. Stopping
	// an upstream first would make everything below it log a spurious "input
	// ended" as it dies.
	//
	// previewMu is taken ahead of e.mu, matching the order every other preview
	// path uses, so an in-flight playlist request cannot spawn an encoder
	// between here and the teardown below. selMu is taken for the same reason
	// and in the same order: the failover sweep must not start a feed into a
	// hub this is about to close.
	e.previewMu.Lock()
	e.selMu.Lock()
	e.mu.Lock()
	e.stopped = true
	dests := make([]*destination, 0, len(e.dests))
	for _, d := range e.dests {
		dests = append(dests, d)
	}
	rends := make([]*rendition, 0, len(e.rends))
	for _, r := range e.rends {
		rends = append(rends, r)
	}
	// The measurement tiers come down with the consumers below, not after
	// them: an analyser reads a destination's hub and the clip buffer reads the
	// selector's, so both would spend the shutdown on a relay that had closed.
	monitors := make([]*loudnessMon, 0, len(e.loud))
	for _, m := range e.loud {
		monitors = append(monitors, m)
	}
	clipCap, clipPort, clipHub := e.clipCap, e.clipPort, e.clipHub
	// The captioner is a consumer of the same hub, so it comes down in the same
	// phase. Left running it would spend the shutdown transcribing a dead relay
	// with a whisper child still holding CPU.
	capt, captPort, captHub, captVTT := e.capt, e.captPort, e.captHub, e.captVTT
	e.capt, e.captPort, e.captHub, e.captVTT, e.captSig = nil, 0, nil, nil, ""
	e.loud = map[int64]*loudnessMon{}
	e.clipCap, e.clipPort, e.clipHub, e.clipSig = nil, 0, nil, ""
	silence := e.silence
	sel, backup, playlist := e.sel, e.backup, e.playlist
	recorder, preview, meters, ingest := e.recorder, e.preview, e.meters, e.ingest
	e.dests = map[int64]*destination{}
	e.rends = map[int64]*rendition{}
	e.silence = nil
	e.sel, e.backup, e.playlist = nil, nil, nil
	e.recorder, e.preview, e.meters, e.ingest = nil, nil, nil, nil
	e.mu.Unlock()
	e.selMu.Unlock()
	e.previewMu.Unlock()

	var wg sync.WaitGroup
	stop := func(p *supervisor.Process) {
		if p == nil {
			return
		}
		wg.Add(1)
		go func() { defer wg.Done(); _ = p.Stop(ctx) }()
	}
	// Through teardownDest rather than a second stop path, so there is exactly
	// one definition of "take a destination down" -- and so the REDUNDANT
	// output goes down with it.
	//
	// Stopping d.proc alone was worse than a leak. The backup is built with
	// supervisor.Spec{AutoRestart: true}, so the orphan did not exit: it went on
	// reconnecting to the platform's backup ingest for ever, from a process in
	// no map and on no monitoring page, and its relay port -- one of the 500
	// shared across every source engine -- was never released. Deleting a
	// SOURCE reaches this same code (Manager.Sync deletes the engine and calls
	// Stop) while the daemon keeps running, so an operator who deleted a source
	// was still publishing to Facebook from something nothing could see.
	for _, d := range dests {
		dest := d
		wg.Add(1)
		go func() { defer wg.Done(); e.teardownDest(dest) }()
	}
	for _, m := range monitors {
		mon := m
		wg.Add(1)
		go func() { defer wg.Done(); e.teardownLoudness(mon) }()
	}
	e.teardownClips(clipCap, clipPort, clipHub)
	// In parallel with the destination stops: the captioner's whisper child can
	// take a couple of seconds to reap, and there is nothing to be gained by
	// waiting for it before starting everything else.
	wg.Add(1)
	go func() { defer wg.Done(); e.teardownCaptions(capt, captPort, captHub, captVTT) }()
	stop(recorder)
	stop(preview)
	stop(meters)
	// A playout variant is a rendition consumer exactly as a destination is, so
	// it belongs in this phase and not the next one: torn down after the
	// rendition tier, it would spend the shutdown reading a closed hub.
	wg.Add(1)
	go func() { defer wg.Done(); e.play.Stop() }()
	wg.Wait()

	// Only now that no destination is reading them: a rendition whose hub
	// closed under a live destination would leave it spinning on a dead relay
	// for as long as the shutdown takes.
	for _, r := range rends {
		stop(r.proc)
	}
	wg.Wait()
	for _, r := range rends {
		if r.hub != nil {
			_ = r.hub.Close()
		}
	}

	// One more level up. The order here is the same dependency chain the
	// reconcile uses, read from the bottom: the renditions above were reading
	// the selector's hub, the selector's feed was reading the silence tier's or
	// the backup's, and each can only go once the thing that reads it has.
	if sel != nil {
		e.teardownFeed(sel.feed)
		if sel.hub != nil {
			_ = sel.hub.Close()
		}
	}
	e.teardownSilence(silence)
	e.teardownBackup(backup)
	// The same level as the backup and torn down with it: both are hubs the
	// selector's feed reads, and the feed above has already gone. A tier left
	// here would hold a UDP socket and an FFmpeg child past shutdown.
	e.teardownPlaylist(playlist)

	if ingest != nil {
		_ = ingest.Stop(ctx)
	}

	e.wg.Wait()
	_ = e.hub.Close()
	// After every child is gone, so the queued tail of their stderr is flushed
	// rather than dropped.
	if sink := e.sink.Swap(nil); sink != nil {
		_ = sink.Close()
	}
	e.log.Info("engine stopped")
}

func (e *Engine) currentRecordingSettings() db.RecordingSettings {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.settings.Recording
}

// Settings returns the live settings snapshot.
func (e *Engine) Settings() db.Settings {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.settings
}

// logSink is the LogWriter handed to every child. It indirects through the
// engine rather than naming a *FileSink directly, so turning persistence on
// starts filling the file for children that are already running.
type logSink struct{ e *Engine }

func (s logSink) WriteLog(l supervisor.LogLine) {
	if f := s.e.sink.Load(); f != nil {
		f.WriteLog(l)
	}
}

// applyLogging opens, closes or re-opens the persistent process log to match
// settings. A rotation limit change re-opens rather than adjusts, so the file
// on disk always obeys the limits it is labelled with.
func (e *Engine) applyLogging(s db.LoggingSettings) {
	e.sinkMu.Lock()
	defer e.sinkMu.Unlock()

	if s == e.sinkCfg {
		return
	}
	e.sinkCfg = s

	if old := e.sink.Swap(nil); old != nil {
		_ = old.Close()
	}
	if !s.PersistProcessLogs {
		return
	}

	f, err := supervisor.NewFileSink(e.log, filepath.Join(e.cfg.DataDir, "logs"), s.MaxFileMB, s.MaxFiles)
	if err != nil {
		// Not fatal: the in-memory ring still has the last few hundred lines,
		// which is what the UI reads.
		e.log.Error("cannot open process log; logs stay in memory only", "err", err)
		return
	}
	e.sink.Store(f)
	e.log.Info("persisting process logs", "path", f.Path())
}

// onStorage reacts to the recording free-space guard. Halting stops the
// recorder outright rather than letting it write until the volume is full and
// takes the database down with it.
func (e *Engine) onStorage(st recording.StorageState) {
	if st.Halted {
		e.log.Warn("stopping recorder", "reason", st.Reason)
	} else {
		e.log.Info("free space recovered; restarting recorder")
	}
	// UNDER reconcileMu, like every other production caller.
	//
	// This was the last one that skipped it, and free-space recovery is exactly
	// when a reconcile is likely to be in flight: the sweeper that frees the
	// space also fires onChange. Two passes then reached reconcileRecorder at
	// once, both read e.recorderSig unlocked, and both concluded the recorder
	// needed starting -- two FFmpegs writing the same segment pattern, one of
	// them an orphan holding a relay port that Stop cannot reach because only
	// the second is in e.recorder.
	//
	// Safe to take here: the guard fires from the recording manager's own
	// 30-second sweep goroutine, and nothing on the Reconcile path calls into
	// it, so this can never be re-entered from inside a reconcile.
	e.reconcileMu.Lock()
	e.reconcileRecorder(e.Settings())
	e.reconcileMu.Unlock()
	e.publishStatus()
}

// ---------------------------------------------------------------- reconcile

// effectiveSettings returns the global settings with this engine's own ingest
// overlaid on top.
//
// Everything except the ingest block stays genuinely global -- recording paths,
// logging, meter intervals, post-production policy are properties of the
// install, not of one programme. The ingest is the one part that differs per
// source, so overlaying it here means every existing reader of
// settings.Ingest keeps working unchanged and no caller has to learn that
// sources exist.
//
// A source row that has gone missing falls back to the settings ingest rather
// than failing the reconcile. That is the fail-open direction: an engine that
// keeps running on its last known configuration is recoverable, and one that
// refuses to reconcile takes the stream down over a database read.
func (e *Engine) effectiveSettings() (db.Settings, error) {
	settings, err := e.store.GetSettings()
	if err != nil {
		return settings, err
	}
	src, err := e.store.GetSource(e.sourceID)
	if err != nil {
		e.log.Warn("source row unavailable; keeping the settings ingest",
			"source", e.sourceID, "err", err)
		return settings, nil
	}
	settings.Ingest = src.Ingest

	// Cached here rather than read on demand because Status() is assembled on
	// every WebSocket push and on every telemetry tick, and the name changes
	// only when the operator renames the source -- which goes through a
	// reconcile.
	e.mu.Lock()
	e.sourceName = src.Name
	e.sourceToken = src.Token
	e.mu.Unlock()

	return settings, nil
}

// ingestToken is the address this programme is reached at on the shared
// listeners. Empty until the first reconcile has read the row.
func (e *Engine) ingestToken() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.sourceToken
}

// SourceID reports which programme this engine owns, and 0 when there is no
// engine to ask. See Engine.Status for why the nil receiver is answered.
//
// 0 is not a programme and no row ever carries it, so a caller that feeds this
// to the store gets "no such source" rather than a phantom one. That is the
// only reading of it that is safe, and it is why nothing here treats a returned
// id as a licence to write.
func (e *Engine) SourceID() int64 {
	if e == nil {
		return 0
	}
	return e.sourceID
}

// SourceName is the operator's label for this programme, empty until the first
// reconcile has read the row.
//
// It exists because every external consumer that groups by source needs a
// stable human-readable handle: an id is meaningless in an MQTT topic or on a
// Home Assistant entity, and the id is the only thing Status carried before.
func (e *Engine) SourceName() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.sourceName
}

// Reconcile makes the running processes match the database. It is safe to call
// repeatedly and from any handler.
//
// One reconcile at a time, for the whole of it. See Engine.reconcileMu: the
// per-tier locks are all dropped and retaken inside, so without this two
// callers can both observe a destination as missing and both start it, and the
// first of the two becomes an FFmpeg nothing can find or stop.
func (e *Engine) Reconcile() error {
	e.reconcileMu.Lock()
	defer e.reconcileMu.Unlock()
	e.reconciles.Add(1)

	settings, err := e.effectiveSettings()
	if err != nil {
		return err
	}
	// Installed before the first sub-reconcile and cleared after the last, so a
	// note raised by previewLoop or by the storage guard -- neither of which is
	// a consequence of anything an operator just saved -- is dropped rather
	// than reported as one.
	rec := newReloadRecorder()
	e.reloadRec.Store(rec)
	defer func() {
		e.reloadRec.Store(nil)
		rep := ReloadReport{SourceID: e.sourceID, SourceName: e.SourceName(), Notes: rec.snapshot()}
		e.lastReload.Store(&rep)
		// Nil-checked unlike the publishers around it. Those all run on paths
		// that only exist after New; this one runs on every Reconcile, and a
		// partially-built Engine in a test is a pattern this package already
		// uses -- it panicked exactly that way once during this work.
		if len(rep.Notes) > 0 && e.bus != nil {
			e.bus.Publish(eventReload, rep)
		}
	}()

	e.mu.Lock()
	prev := e.settings
	e.settings = settings
	e.mu.Unlock()

	e.applyLogging(settings.Logging)
	e.reconcileIngest(settings, prev)
	e.reconcileRecorder(settings)
	e.reconcileMeters(settings)

	if err := e.reconcileOutputs(); err != nil {
		return err
	}
	// After the outputs, because these read the hub that the silence and
	// selector tiers decide, and reconcileOutputs is where that is settled.
	//
	// THE PREVIEW JOINED THIS LIST when it stopped reading the raw ingest hub.
	// Run before the outputs it compared its running encoder against the OLD
	// upstream, decided nothing had changed, and then the hub was swapped
	// underneath it -- leaving the preview subscribed to a tier that was no
	// longer on air.
	// Neither can fail the reconcile: a measurement that will not start and a
	// capture buffer that will not bind are both worth a log line and nothing
	// more, and a destination must never be held back by either.
	e.reconcilePreview(settings)
	e.reconcileClips()
	e.reconcileCaptions()
	e.reconcileLoudness(settings)
	e.publishStatus()
	return nil
}

func (e *Engine) reconcileIngest(s, prev db.Settings) {
	// SRT has NO ingest process. The one-port listener is a Go server owned by
	// the manager: it accepts the publisher, matches the streamid against every
	// source's token, and delivers the datagrams straight into this engine's
	// hub. Spawning FFmpeg here as well would put two things on one socket --
	// and since the Go server binds first, the FFmpeg one would fail to bind
	// and crash-loop forever behind a listener that was working fine.
	//
	// That is not hypothetical: it is exactly what this did until the port was
	// made install-wide, and on a host whose FFmpeg lacks libsrt it hid behind
	// "Protocol not found" rather than showing itself as a conflict.
	//
	// A LAYOUT BELONGS TO THE TRANSPORT THAT DELIVERED IT.
	//
	// Checked here, above every early return below, because three of them exit
	// without reaching the reset that starting an ingest performs. Switching an
	// RTMP source to SRT took the first of them and left the RTMP stream's
	// layout in e.source, still flagged measured -- so reconcileOutputs planned
	// destinations against the previous transport's track list until the new
	// probe landed. A guard that refuses the placeholder and accepts a stale
	// real layout is worth very little.
	// TWO SEPARATE CONCERNS, and tangling them left a door open.
	//
	// Invalidating the layout only makes sense if there IS one, so that half is
	// rightly gated on measured. Bumping the generation is not: it exists to
	// discard a probe that is ALREADY IN FLIGHT, and a probe in flight is
	// precisely the state where measured is still false. Gating the bump on
	// measured meant a mode switch made before the first probe committed bumped
	// nothing -- so that probe passed its generation check and committed the
	// OLD transport's layout stamped with the NEW mode. measured=true over the
	// wrong track list, and the guard permanently satisfied: the exact failure
	// the comment above describes, reached through the one door it did not
	// close.
	e.mu.Lock()
	if e.measuredMode != s.Ingest.Mode {
		if e.measured {
			// invalidate does both halves, generation included.
			e.invalidate()
		} else {
			// Nothing measured to put back, but a probe may still be in flight
			// and it has to be told its result is stale.
			e.sourceGen++
		}
		// The failure history belongs to the OLD transport. Without this, five
		// failed RTMP probes followed by a switch to SRT started destinations
		// provisionally on the very next reconcile, before a single SRT probe
		// had been attempted -- and the SRT path returns early without
		// replacing the ingest child, so the reset at ingest start never runs.
		e.probeFails.Store(0)
	}
	e.mu.Unlock()

	// RTMP IS NOT THE SAME CASE, even though it now has a one-port Go listener
	// too. srtserver hands datagrams straight to a Sink, so an SRT source needs
	// no child at all; rtmpserver has no Sink and re-publishes to subscribers,
	// so an RTMP source still needs a child -- one that DIALS the listener on
	// loopback instead of binding 1935. Extending this early return to RTMP
	// would leave the programme with a publisher and nobody reading it.
	if s.Ingest.Mode == db.IngestSRT {
		e.stopIngestProcess()
		return
	}

	// Nothing chosen yet. A fresh install does not pick an ingest on the
	// operator's behalf (see db.IngestUnset), so there is nothing to listen
	// with until they do — and spawning FFmpeg with an empty Kind would build a
	// command with no input and crash-loop against it.
	if s.Ingest.Mode == db.IngestUnset {
		e.stopIngestProcess()
		return
	}

	// An RTMP source with no token has no address, and the child must not be
	// spawned without one. `rtmp://127.0.0.1:1935/live/` resolves — StreamKey
	// falls back to the whole path when there is no second segment — so the
	// subscriber would silently attach to the stream key "live" and receive
	// whatever any publisher happened to send there. Reachable only through
	// effectiveSettings' fail-open path, where the source row went missing
	// mid-reconcile, which is exactly when quietly crossing two programmes
	// would be hardest to notice.
	if s.Ingest.Mode == db.IngestRTMP && e.ingestToken() == "" {
		e.log.Error("rtmp ingest not started: the source has no publish token, so it has no address",
			"source", e.sourceID)
		e.stopIngestProcess()
		return
	}

	// #255: the INHERITED pull source, re-examined here because the save-time
	// gate cannot see one configured before it existed. Only a REFUSAL stops an
	// ingest; an upload merely stored unchecked keeps streaming and is reported
	// on the card. pullUploadRefusal carries the argument for the split, and it
	// is the file to read before folding the two states back together.
	//
	// Above the signature comparison on purpose: a source already running on a
	// refused upload has an unchanged signature, so a check placed below would
	// take the `same` early return and never fire for the one case this exists
	// for -- the one nothing re-examines.
	if s.Ingest.Mode == db.IngestPull {
		if refusal := e.pullUploadRefusal("this source", s.Ingest.Pull.URL); refusal != "" {
			e.log.Error("pull ingest not started: "+refusal, "source", e.sourceID)
			e.noteReload("ingest", "ingest", reloadStop, refusal)
			e.stopIngestProcess()
			return
		}
	}

	spec := e.ingestSpec(s)
	sig := hashStrings(append([]string{string(s.Ingest.Mode)}, spec...))

	e.mu.Lock()
	cur := e.ingest
	same := cur != nil && e.ingestSig == sig
	e.mu.Unlock()
	if same {
		return
	}

	if cur != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		_ = cur.Stop(ctx)
		cancel()
	}

	proc := supervisor.New(e.log, supervisor.Spec{
		Name:        "ingest",
		Kind:        "ingest",
		Bin:         e.tools.FFmpeg,
		Args:        spec,
		Secrets:     ingestSecrets(s.Ingest.SRT, s.Ingest.RTMP, s.Ingest.Pull, e.ingestToken()),
		AutoRestart: true,
		// The ingest listener is expected to exit whenever the streamer stops,
		// so it must come back fast rather than backing off toward 30s and
		// leaving the next session waiting.
		MinBackoff: 500 * time.Millisecond,
		MaxBackoff: 5 * time.Second,
		// THE INGEST'S OWN OUTPUT RATE. #674
		//
		// The relay hub receives ~6 packets/second for 81 seconds and then
		// ~135, with consumers subscribed throughout, so it is INPUT-starved
		// rather than failing to deliver. The ingest is the only thing that
		// feeds it and it execs once for the whole run -- it is alive and
		// producing almost nothing. This is its own account of how much it has
		// written, which nothing has ever recorded.
		OnProgress: e.ingestProgressLogger(),
		OnLog:      e.onLog,
		OnState:    e.onState,
		LogSink:    logSink{e},
	})

	e.mu.Lock()
	e.ingest = proc
	e.ingestSig = sig
	// A new ingest means the previous layout is stale. One of two places the
	// placeholder goes back into e.source (the other invalidates on an
	// ingest-mode change, above); both bump sourceGen and both clear measured.
	// probeLoop's idle branch does NEITHER on purpose -- it drops probed but
	// keeps the layout, because a layout that was measured stays measured.
	e.invalidate()
	// A new ingest gets a fresh hold. The failure count is about THIS stream,
	// so carrying it across a restart would start the next one provisionally
	// before anything had been tried.
	e.probeFails.Store(0)
	e.mu.Unlock()

	proc.Start()
	e.log.Info("ingest started", "mode", s.Ingest.Mode, "url", e.ingestURLForLog(s))
}

// stopIngestProcess tears down an FFmpeg ingest if one is running, leaving the
// engine with no ingest process at all. Used when the mode changes to SRT,
// where the listener is the manager's Go server rather than a child.
func (e *Engine) stopIngestProcess() {
	e.mu.Lock()
	cur := e.ingest
	e.ingest, e.ingestSig = nil, ""
	e.mu.Unlock()
	if cur == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	_ = cur.Stop(ctx)
	cancel()
}

func (e *Engine) ingestSpec(s db.Settings) []string {
	spec := ffmpeg.IngestSpec{
		Kind:          ffmpeg.IngestKind(s.Ingest.Mode),
		SRTPort:       s.Listeners.SRTPort,
		SRTPassphrase: s.Ingest.SRT.Passphrase,
		SRTLatencyMS:  s.Ingest.SRT.LatencyMS,
		RTMPPort:      s.Listeners.RTMPPort,
		RTMPApp:       s.Ingest.RTMP.App,
		// The token, not s.Ingest.RTMP.StreamKey. This child SUBSCRIBES to the
		// shared listener, and the path it dials has to be the same address the
		// manager registered for this source -- which is the token, because that
		// is the only per-source value that is unique, rotatable and never
		// logged. See db.RTMPSettings.StreamKey for what the old field became.
		RTMPAddress: e.ingestToken(),
		// DataDir is the confinement root for a file:// source. Without it a
		// relative path resolves against the process working directory, which
		// fails open — FFmpeg says "no such file" — rather than fails unsafe.
		PullURL:               s.Ingest.Pull.URL,
		PullDataDir:           e.cfg.DataDir,
		PullReconnectDelayMax: s.Ingest.Pull.ReconnectDelayMaxSeconds,
		PullRTSPTransport:     s.Ingest.Pull.RTSPTransport,
		RelayURL:              e.hub.InputURL(),
	}
	return ffmpeg.IngestArgs(spec)
}

// ingestURLForLog renders this install's ingest address for a LOG LINE.
//
// Named for what it is. It was ingestPublicURL, which read as "the URL to show
// people" -- and its single caller has always been an Info log line, so the
// name invited exactly the mistake that was in it: handing a rendering that
// carries the SRT passphrase and the whole pull URL to something that writes to
// journalctl. The API's own public rendering is ffmpeg.PublicIngestURL, reached
// from internal/api, and that one is still whole on purpose.
func (e *Engine) ingestURLForLog(s db.Settings) string {
	spec := ffmpeg.IngestSpec{
		Kind:          ffmpeg.IngestKind(s.Ingest.Mode),
		SRTPort:       s.Listeners.SRTPort,
		SRTPassphrase: s.Ingest.SRT.Passphrase,
		SRTLatencyMS:  s.Ingest.SRT.LatencyMS,
		RTMPPort:      s.Listeners.RTMPPort,
		RTMPApp:       s.Ingest.RTMP.App,
		// No RTMPAddress: the rendering deliberately shows the server half only,
		// and the token goes in OBS's separate stream-key box.
		PullURL: s.Ingest.Pull.URL,
	}
	// THE COMMENT ABOVE USED TO CLAIM MORE THAN THE CODE DID. It said "this is a
	// log line and a dashboard string, so the token stays out of it" -- true of
	// the RTMP token, which is simply not in the spec, and false of the other
	// two credentials this function hands to PublicIngestURL: the SRT passphrase
	// goes into the query, and the pull URL is returned whole. Both then reached
	// `ingest started` at Info, on every boot, in the clear.
	//
	// IngestURLForLog keeps the host and port -- which is the question a failed
	// ingest actually asks -- and drops the rest.
	return spec.IngestURLForLog("<server>")
}

// stemPlanSig folds a stem plan into the recorder's restart signature. Names
// and codecs both matter: either one changing changes a filename on disk.
func stemPlanSig(plan []recording.Stem) string {
	if len(plan) == 0 {
		return ""
	}
	parts := make([]string, 0, len(plan))
	for _, st := range plan {
		parts = append(parts, strconv.Itoa(st.Track)+":"+st.Name+":"+string(st.Codec))
	}
	return strings.Join(parts, ",")
}

// stemPlanFor decides which stems the recorder writes.
//
// The stem plan is derived from the probed track set and its roles, so all
// three belong in the signature: renaming track 2 from "track2" to "mic"
// has to move the file the next segment is written to.
//
// PROBED is the load-bearing word, and it was missing. Until the ingest has
// been probed, e.source is routing.DefaultSource() -- six placeholder tracks
// that exist so the routing editor has something to render, not a claim
// about what is arriving. Planning stems from it asks FFmpeg to
// `-map 0:a:3` on a three-track ingest, and FFmpeg treats a map that
// matches no stream as fatal:
//
//	Stream map '0:a:3' matches no streams.
//	Error parsing options for output file .../rec-%Y%m%d-%H%M%S-track4.flac
//
// The recorder then crash-loops until the probe lands and a later reconcile
// replaces the plan. On a fast machine that is over in a second and leaves
// nothing but a stray set of track-named stems; on a slow one it is a
// restart storm whose outcome depends on what finishes first. That is the
// acceptance-audio flake.
//
// The main recording is deliberately NOT gated on this. It maps `0:v` and
// `0:a` wholesale, which is correct whatever arrives, and an operator who
// switched recording on wants the programme captured from the first frame
// -- waiting for a probe would lose the opening seconds for nothing.
// stemPlanFor derives the per-track stem outputs from the ingest layout.
//
// known means e.source is a measurement rather than the placeholder, which is
// the only thing that makes PlanStems' output real. It used to be `probed`,
// which asks whether a stream is arriving right now — so an encoder going quiet
// for a few seconds emptied the plan, changed the recorder signature, and
// restarted the recorder WITHOUT stems mid-outage, then restarted it a second
// time when the probe came back. Two segment splits and a hole in the stems for
// every blip, from a layout that never actually changed.
func stemPlanFor(rec db.RecordingSettings, src routing.Source, known bool) []recording.Stem {
	if !rec.Stems || !known {
		return nil
	}
	return recording.PlanStems(src, rec.StemCodec)
}

// probeGiveUp is how many consecutive probe failures mean the layout is not
// going to be measured.
//
// probeLoop retries every 3 seconds while bytes are flowing, so this is about
// fifteen seconds of trying. Long enough that a transient failure -- a relay
// port not yet bound, a stream whose first packets are not yet decodable -- rides
// through it and destinations are never planned on a guess they did not need.
// Short enough that a genuinely broken ffprobe does not take a broadcast off air
// for the length of an event.
const probeGiveUp = 5

// The probe loop's two cadences and the bound on one attempt. Named rather than
// written inline because holdCeiling below is only correct in relation to them,
// and a relationship spelled out in three separate literals is one edit away
// from being silently false. TestTheHoldCeilingCannotPreemptTheCounter pins it.
const (
	probeFastCadence    = 3 * time.Second
	probeSlowCadence    = 30 * time.Second
	probeAttemptTimeout = 10 * time.Second
)

// holdCeiling bounds the hold in WALL-CLOCK TIME, which probeGiveUp does not.
//
// probeGiveUp counts CONSECUTIVE FAILURES, and probeLoop only probes while the
// relay is carrying data (see the `flowing` branch). A relay that alternates
// quiet and flowing -- routine in an encoder's first minute, because the
// selector leaves the primary for the slate and back -- advances the counter
// only during the flowing stretches and freezes it, unreset, during the quiet
// ones. So "after enough consecutive failures" has no bound on the clock an
// operator or a test actually measures: 5 failures need 65s of FLOW, and the
// wall-clock cost is that plus every quiet spell in between, without limit.
// That is #473: a hold outliving the acceptance suite's 90s ceiling with no
// single component being slow.
//
// WHY THIS IS NOT THE TIMEOUT THAT WAS REJECTED. reconcileOutputs argues,
// correctly, that a timeout "reintroduces the original bug on a schedule,
// because it fires just as readily while a probe is merely slow". That argument
// is about a timeout REPLACING the counter. This one is strictly weaker: it sits
// ABOVE the counter's own worst case on a flowing stream
//
//	probeGiveUp * (probeAttemptTimeout + probeFastCadence) = 5 * 13s = 65s
//
// so on any stream where probes actually run back to back the counter reaches
// five first and this never fires. It bites only where the counter is blind --
// when probes are not being attempted at all -- which is exactly the case a
// consecutive-failure count cannot see.
//
// Below the suite's 90s ceiling on purpose, so the exit is observable rather
// than racing the thing measuring it.
const holdCeiling = 75 * time.Second

// probeUnmeasurable reports that probing has failed enough times in a row to
// stop waiting for it.
func (e *Engine) probeUnmeasurable() bool {
	return e.probeFails.Load() >= probeGiveUp
}

// holdBegan starts the hold's clock, and leaves it alone if it is already
// running. Restamping on every reconcile would reset the ceiling on each pass
// and it could never be reached -- the same shape as the give-up counter reset
// that made probeGiveUp unreachable (#469).
func (e *Engine) holdBegan(now time.Time) {
	e.holdSince.CompareAndSwap(0, now.UnixNano())
}

// holdExpired reports that the current hold has outlasted holdCeiling. False
// when nothing is held, so a fresh engine never starts destinations on a guess.
func (e *Engine) holdExpired(now time.Time) bool {
	since := e.holdSince.Load()
	if since == 0 {
		return false
	}
	return now.Sub(time.Unix(0, since)) >= holdCeiling
}

func (e *Engine) reconcileRecorder(s db.Settings) {
	e.mu.RLock()
	cur := e.recorder
	// Read here rather than through e.Source(): that takes the same RLock, and
	// this function holds it again further down.
	src := e.source
	// The recorder reads e.hub — the INGEST relay, not the downstream one — so
	// the layout that matters is the ingest's own, and the silence tier never
	// stands in for it here. measured is what says e.source holds one.
	measured := e.measured
	e.mu.RUnlock()
	src = e.annotate(src)

	// The free-space guard has the last word: recording into a volume that is
	// about to fill takes the database and the preview down with it.
	if !s.Recording.Enabled || !e.recman.RecordingAllowed() {
		if cur != nil {
			e.stopAux(&e.recorder, "recorder")
		}
		return
	}
	plan := stemPlanFor(s.Recording, src, measured)
	sig := strconv.Itoa(s.Recording.SegmentSeconds) + "|" +
		strconv.FormatBool(s.Recording.Stems) + "|" + string(s.Recording.StemCodec) + "|" +
		stemPlanSig(plan)
	if cur != nil && e.recorderSig == sig {
		return
	}
	if cur != nil {
		e.stopAux(&e.recorder, "recorder")
	}

	if err := os.MkdirAll(e.cfg.RecordingsDir(), 0o755); err != nil {
		e.log.Error("cannot create recordings directory", "err", err)
		return
	}
	port, err := e.alloc.Allocate()
	if err != nil {
		e.log.Error("recorder: no relay port", "err", err)
		return
	}
	url := e.hub.Subscribe("recorder", port)

	pattern := filepath.Join(e.cfg.RecordingsDir(), "rec-%Y%m%d-%H%M%S.mkv")
	rs := ffmpeg.RecorderSpec{
		RelayURL:       url,
		OutputPattern:  pattern,
		SegmentSeconds: s.Recording.SegmentSeconds,
	}
	args := ffmpeg.RecorderArgs(rs)
	if len(plan) > 0 {
		// Fail open: a stems directory that cannot be made costs the stems, not
		// the archive. Losing the master because a subdirectory was unwritable
		// would be the worse trade by a wide margin.
		if err := recording.EnsureStemsDir(e.cfg.RecordingsDir()); err != nil {
			e.log.Error("stems: cannot create directory; recording master only", "err", err)
		} else {
			args = ffmpeg.StemRecorderArgs(ffmpeg.StemRecorderSpec{
				RecorderSpec: rs,
				Codec:        s.Recording.StemCodec,
				Stems:        recording.StemSpecs(e.cfg.RecordingsDir(), pattern, plan),
			})
		}
	}

	proc := supervisor.New(e.log, supervisor.Spec{
		Name: "recorder", Kind: "recorder", Bin: e.tools.FFmpeg, Args: args,
		AutoRestart: true, OnLog: e.onLog, OnState: e.onState, LogSink: logSink{e},
	})

	e.mu.Lock()
	// Shutdown may have run since the checks above: the free-space guard calls
	// this from the recording loop, which Stop waits on rather than precedes.
	// Publishing the process under the same lock Stop uses to collect them is
	// what keeps a late start from becoming an orphan.
	if e.stopped {
		e.mu.Unlock()
		e.hub.Unsubscribe("recorder")
		e.alloc.Release(port)
		return
	}
	e.recorder = proc
	e.recorderPort = port
	e.recorderSig = sig
	e.mu.Unlock()
	proc.Start()
}

// reconcilePreview applies settings changes to the preview encoder, but never
// conjures one out of nothing: the preview is demand-driven from
// PreviewRequested. Reconcile only stops an encoder that settings have
// disabled, and restarts one that is being watched with stale arguments.
func (e *Engine) reconcilePreview(s db.Settings) {
	e.previewMu.Lock()
	defer e.previewMu.Unlock()

	e.mu.RLock()
	cur, sig, seen := e.preview, e.previewSig, e.previewSeen
	e.mu.RUnlock()

	if !s.Preview.Enabled {
		if cur != nil {
			e.stopPreviewLocked()
		}
		return
	}

	want := previewSig(s)
	// The hub is compared by IDENTITY, not by a label for it. A selector rebuilt
	// with an equivalent spec produces the same label and a different object, and
	// a preview left subscribed to the old one receives nothing while reporting
	// itself healthy.
	e.mu.RLock()
	joined := e.previewHub
	e.mu.RUnlock()
	sameHub := cur == nil || joined == e.downstreamHub()
	if cur != nil && sig == want && sameHub {
		return
	}
	if cur != nil {
		e.stopPreviewLocked()
	}
	// Restart only for a viewer who is still there. With nobody watching, the
	// next playlist request picks the new arguments up anyway.
	if !previewIdle(s, seen, time.Now()) {
		e.startPreviewLocked(s)
	}
}

// PreviewRequested records a client asking for the HLS playlist, and starts the
// encoder if it is down.
//
// The preview is the only video re-encode polyemesis performs, so it runs only
// while someone is watching instead of for the whole session. A player polls
// the playlist every segment, and that poll is the liveness signal previewLoop
// uses to decide when to shut the encoder down again.
//
// The first request after an idle stop is answered 404, because ffmpeg has not
// written the playlist yet. That is expected: hls.js retries a failed manifest
// load, so the player recovers on its own once the first segment lands, a
// second or two later.
func (e *Engine) PreviewRequested() {
	now := time.Now()

	e.mu.Lock()
	s, stopped := e.settings, e.stopped
	running := e.preview != nil
	last := e.previewAt
	e.previewSeen = now
	e.mu.Unlock()

	if stopped || running || !s.Preview.Enabled {
		return
	}
	if !last.IsZero() && now.Sub(last) < previewStartDebounce {
		return
	}

	e.previewMu.Lock()
	e.mu.RLock()
	running = e.preview != nil
	e.mu.RUnlock()
	if !running {
		e.startPreviewLocked(s)
	}
	e.previewMu.Unlock()

	if !running {
		e.publishStatus()
	}
}

// previewLoop stops the encoder once the playlist has gone unrequested for the
// configured idle period.
func (e *Engine) previewLoop(ctx context.Context) {
	tick := time.NewTicker(previewSweep)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			e.sweepPreview(time.Now())
		}
	}
}

func (e *Engine) sweepPreview(now time.Time) {
	e.mu.RLock()
	s, seen := e.settings, e.previewSeen
	running := e.preview != nil
	e.mu.RUnlock()

	// A DRY HUB STOPS IT WITHOUT WAITING OUT THE IDLE WINDOW. Idle means "nobody
	// is watching"; dry means "there is nothing to watch". Waiting the full idle
	// window for the second one keeps an encoder alive on a stream that ended,
	// which is most of what made the panel flap.
	dry := !e.previewFlowing(now)
	if !running || (!dry && !previewIdle(s, seen, now)) {
		return
	}

	e.previewMu.Lock()
	// A request may have landed while we were taking the lock, and that client
	// is now watching an encoder we were about to kill.
	e.mu.RLock()
	seen = e.previewSeen
	running = e.preview != nil
	e.mu.RUnlock()
	// RE-READ UNDER THE LOCK. The first sample was taken before previewMu, and a
	// selector swap in between can retire the hub it looked at while the one now
	// downstream is carrying a stream -- stopping on the stale answer would kill
	// an encoder somebody is watching.
	dry = !e.previewFlowing(now)
	stop := running && (dry || previewIdle(s, seen, now))
	if stop {
		e.stopPreviewLocked()
	}
	e.previewMu.Unlock()

	if stop {
		if dry {
			e.log.Info("preview stopped; nothing on air to encode")
		} else {
			e.log.Info("preview idle; encoder stopped", "after", previewIdleWindow(s))
		}
		e.publishStatus()
	}
}

// OutputLive reports whether anything is reaching this programme's destinations
// -- the operator's encoder, a backup, the slate, or a playlist.
//
// DIFFERENT FROM IngestLive, and the difference is the whole point. IngestLive
// asks whether the operator's own encoder is arriving; this asks whether
// ANYTHING is going out. During a failover they disagree, and that disagreement
// is what lets a preview show the slate while saying the input is gone, rather
// than blanking the picture of the thing currently being broadcast.
// REFUSES A NIL RECEIVER, exactly as IngestLive does. A nil engine is an install
// with no source, and there is no pipeline to report on: answering false would
// invent a fact about a programme that does not exist. Every caller reaches this
// through Manager.Engines(), which yields real engines.
func (e *Engine) OutputLive() bool { return e.previewFlowing(time.Now()) }

// previewFlowing reports whether the hub the preview would read has carried
// anything RECENTLY.
//
// A byte DELTA, not a total. relay.Hub.RxBytes() only ever grows, so a hub that
// has gone silent still answers a `> 0` test for the rest of the process's life
// -- which is the shape of gate that looks right and does nothing.
//
// It is the same predicate probeLoop, the alert sweep and the selector's
// liveness sweep already use, and for the reason probeLoop states: an SRT or
// RTMP listener sits in "running" for as long as it waits for a publisher, so
// process state answers the wrong question.
//
// Sampled on call rather than by a ticker of its own. Both callers are frequent
// -- a watching player polls the playlist every few seconds and sweepPreview
// runs every previewSweep -- so the series is dense enough without another
// goroutine.
func (e *Engine) previewFlowing(now time.Time) bool {
	h := e.downstreamHub()
	if h == nil {
		return false
	}
	rx := h.RxBytes()
	e.mu.Lock()
	switch {
	case h != e.previewRxHub:
		// A NEW HUB IS NOT DELIVERY. A selector coming up or going down replaces
		// the hub, and the replacement starts at zero -- which differs from the
		// old one's total and would otherwise stamp "flowing" and start an
		// encoder against silence for the whole grace period. Adopt the new
		// baseline and wait to SEE it advance.
		e.previewRxHub, e.previewRxBytes, e.previewRxAt = h, rx, time.Time{}
	case rx != e.previewRxBytes:
		e.previewRxBytes, e.previewRxAt = rx, now
	}
	at := e.previewRxAt
	e.mu.Unlock()
	// Zero until the first advance is observed, which is the honest answer on an
	// install where nothing has ever published.
	return !at.IsZero() && now.Sub(at) < previewFlowGrace
}

// startPreviewLocked spawns the encoder. The caller must hold previewMu.
func (e *Engine) startPreviewLocked(s db.Settings) {
	e.mu.Lock()
	stopped := e.stopped
	// Recorded even when the start below fails, so a failing start backs off
	// rather than being retried on every poll.
	e.previewAt = time.Now()
	e.mu.Unlock()
	if stopped {
		return
	}

	// NOTHING ON AIR, NOTHING TO PREVIEW.
	//
	// The encoder used to start on any playlist poll. With the relay quiet its
	// ffmpeg does not fail and does not exit -- it BLOCKS in avformat_open_input
	// on a UDP socket that has no EOF, the supervisor has no stall watchdog, so
	// it sits "running" for ever, writes no playlist, and every poll 404s while
	// a libx264 process holds a port. That is the flapping an operator sees on
	// the pipeline panel, and the wasted encoder behind it.
	//
	// Placed HERE rather than in PreviewRequested so both callers inherit it --
	// reconcilePreview restarts through this path too. A refused start costs
	// nothing: PreviewRequested still records the request, so the moment bytes
	// appear the next poll starts the encoder.
	if !e.previewFlowing(time.Now()) {
		return
	}

	dir := e.cfg.HLSDirFor(e.sourceID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.log.Error("cannot create hls directory", "err", err)
		return
	}
	// Old segments from a previous session would otherwise be served to the
	// player before the new ones appear.
	clearDir(dir)

	port, err := e.alloc.Allocate()
	if err != nil {
		e.log.Error("preview: no relay port", "err", err)
		return
	}
	// THE HUB THAT IS ACTUALLY ON AIR, not the raw primary ingest.
	//
	// Every other consumer -- meters, clips, captions, destinations -- reads
	// downstreamHub(); the preview read e.hub directly, so it saw only the
	// primary encoder. During a failover the destinations rode the slate or the
	// backup and the OPERATOR'S PREVIEW SHOWED NOTHING, which is the moment a
	// preview is worth most.
	//
	// The hub itself is remembered rather than a label for it: a label is not
	// identity, and a selector rebuilt with an equivalent spec would compare
	// equal while being a different object to unsubscribe from.
	hub := e.downstreamHub()
	if hub == nil {
		return
	}
	e.mu.Lock()
	e.previewHub = hub
	e.mu.Unlock()
	url := hub.Subscribe("preview", port)

	args := ffmpeg.PreviewArgs(ffmpeg.PreviewSpec{
		RelayURL:       url,
		OutputDir:      dir,
		SegmentSeconds: s.Preview.SegmentSeconds,
		Height:         s.Preview.VideoHeight,
		VideoKbps:      s.Preview.VideoKbps,
	})

	proc := supervisor.New(e.log, supervisor.Spec{
		Name: "preview", Kind: "preview", Bin: e.tools.FFmpeg, Args: args,
		AutoRestart: true, OnLog: e.onLog, OnState: e.onState, LogSink: logSink{e},
	})

	e.mu.Lock()
	e.preview = proc
	e.previewPort = port
	e.previewSig = previewSig(s)
	e.mu.Unlock()
	proc.Start()
	e.log.Info("preview started on demand", "height", s.Preview.VideoHeight)
}

// stopPreviewLocked tears the encoder down. The caller must hold previewMu.
func (e *Engine) stopPreviewLocked() {
	e.stopAux(&e.preview, "preview")
	// The playlist left behind would be served to the next viewer, pointing at
	// segments the next start is about to delete. Scoped to THIS source: while
	// it cleared the shared directory, an engine tearing down took the live
	// playlist of whichever engine had since become the default with it.
	clearDir(e.cfg.HLSDirFor(e.sourceID))
}

// previewSig hashes the arguments a running encoder was built with. The idle
// timeout is deliberately absent: changing it must not cycle a live preview.
func previewSig(s db.Settings) string {
	return fmt.Sprintf("%d/%d/%d", s.Preview.SegmentSeconds, s.Preview.VideoHeight, s.Preview.VideoKbps)
}

func previewIdleWindow(s db.Settings) time.Duration {
	if s.Preview.IdleTimeoutSeconds <= 0 {
		return previewIdleDefault
	}
	return time.Duration(s.Preview.IdleTimeoutSeconds) * time.Second
}

// previewIdle reports whether the last playlist request is old enough that the
// encoder should not be running. The window is what stops a page reload, which
// pauses polling for a moment, from cycling ffmpeg.
func previewIdle(s db.Settings, seen, now time.Time) bool {
	return now.Sub(seen) >= previewIdleWindow(s)
}

// reconcileMeters restarts the metering sidecar whenever the ingest's track
// layout changes, because the merged channel numbering it parses depends on
// the exact per-track channel counts.
// applyMeterInterval publishes a changed metering throttle to the sidecar that
// is already running.
//
// Called before every early return in reconcileMeters, deliberately: a change
// made while the meters are down (no probe yet, or metering switched off) has
// to survive until they come back, or the operator has to make it twice.
func (e *Engine) applyMeterInterval(m db.MeterSettings) {
	e.meterInterval.Store(int64(time.Duration(m.IntervalMS) * time.Millisecond))
	e.noteReload("meters", "meters", reloadLive, "metering interval applied without a respawn")
}

// meterThrottle rate-limits metering frames on their way to the WebSocket.
//
// It holds the ENGINE rather than a Duration. That is the whole point: astats
// prints far faster than any UI can draw, so the frames have to be shed, but
// the rate at which they are shed is an operator setting that must not require
// respawning the sidecar to change. A captured Duration is what made
// meters.intervalMs a setting that stored and did nothing.
type meterThrottle struct {
	e    *Engine
	last time.Time
}

func (t *meterThrottle) allow(now time.Time) bool {
	if now.Sub(t.last) < time.Duration(t.e.meterInterval.Load()) {
		return false
	}
	t.last = now
	return true
}

func (e *Engine) reconcileMeters(s db.Settings) {
	// Before every early return below. See applyMeterInterval.
	e.applyMeterInterval(s.Meters)

	e.mu.RLock()
	cur := e.meters
	e.mu.RUnlock()
	// The layout the meters will actually see, which is the synthetic one when
	// the silence tier is running — a meter built from a zero-track probe would
	// parse nothing while the destinations downstream carry a track.
	//
	// `known` is what stops a meter being built from the placeholder layout.
	// The zero-track check below cannot catch that: DefaultSource() has SIX
	// tracks, so an unprobed engine sails past it and compiles
	// `[0:a:0]...[0:a:5]amerge=inputs=6` against a three-track ingest. FFmpeg
	// reports "Stream specifier ':a:3' ... matches no streams" and exits, and
	// the meters crash-loop until the probe lands -- burning CPU on the
	// smallest machines, which are the ones least able to spare it.
	src, known := e.effectiveSourceKnown()

	if !s.Meters.Enabled || len(src.Tracks) == 0 || !known {
		if cur != nil {
			e.stopAux(&e.meters, "meters")
		}
		return
	}

	channels := make([]int, len(src.Tracks))
	for i, t := range src.Tracks {
		channels[i] = t.Channels
	}
	// The hub identity rides in the signature, not just the channel counts. A
	// one-stereo-track ingest and a synthesised silent track are both "[2]", so
	// without this the meters would keep a subscription on a silence hub that
	// has closed the moment the ingest gained a real track.
	sig := hashStrings([]string{fmt.Sprint(channels), e.sourceLabel()})
	if cur != nil && e.metersSig == sig {
		return
	}
	if cur != nil {
		e.stopAux(&e.meters, "meters")
	}

	port, err := e.alloc.Allocate()
	if err != nil {
		e.log.Error("meters: no relay port", "err", err)
		return
	}
	meterHub := e.downstreamHub()
	url := meterHub.Subscribe("meters", port)

	args := ffmpeg.MetersArgs(ffmpeg.MetersSpec{RelayURL: url, TrackChannels: channels})

	proc := supervisor.New(e.log, supervisor.Spec{
		Name: "meters", Kind: "meters", Bin: e.tools.FFmpeg, Args: args,
		AutoRestart: true,
		// astats prints far faster than any UI can draw; throttle here rather
		// than flooding every WebSocket client with 50 frames a second. The
		// rate is read per frame from the engine, so lowering it applies to
		// this sidecar without respawning it.
		StdoutHandler: func(r io.Reader) error {
			th := &meterThrottle{e: e}
			return ffmpeg.ParseLevels(r, channels, func(l ffmpeg.Levels) {
				now := time.Now()
				if !th.allow(now) {
					return
				}
				e.mu.Lock()
				e.levels = l
				e.levelsAt = now
				e.mu.Unlock()
				e.bus.Publish(events.TypeLevels, l)
			})
		},
		OnLog:   e.onLog,
		OnState: e.onState,
		LogSink: logSink{e},
	})

	e.mu.Lock()
	e.meters = proc
	e.metersPort = port
	e.metersSig = sig
	e.metersHub = meterHub
	e.mu.Unlock()
	proc.Start()
}

// reconcileOutputs brings the shared encodes and the destinations that consume
// them into line with the database.
//
// The order is the load-bearing part, and it is why this is one function rather
// than two: a destination whose rendition has not started yet, or whose hub has
// just been closed, spins on a dead relay. So destinations come down before the
// renditions they read and go back up after them.
func (e *Engine) reconcileOutputs() error {
	destRows, err := e.store.ListDestinationsBySource(e.sourceID)
	if err != nil {
		return err
	}
	rendRows, err := e.store.ListRenditionsBySource(e.sourceID)
	if err != nil {
		return err
	}
	// The ref count, straight from the database: a rendition nothing enabled
	// selects is absent, and absent means "must not be burning CPU".
	//
	// SCOPED, like the listing on the line above it. Counting every programme's
	// destinations meant a destination on ANOTHER programme kept this engine's
	// encode alive -- a tier nothing here consumes, burning a core for the life
	// of the broadcast, while the status card (correctly scoped) reported zero
	// consumers on a process that was plainly running. Two screens disagreeing
	// about the same rendition, and the one telling the truth looked wrong.
	counts, err := e.store.CountEnabledDestinationsByRenditionForSource(e.sourceID)
	if err != nil {
		return err
	}
	// Playout variants are rendition consumers too, and they are the only ones
	// the database cannot count: they live in settings, not in a table with a
	// rendition_id. Folded in BEFORE wantedRenditions, so a tier that only
	// playout reads still earns its encode instead of being torn down under a
	// variant that is serving viewers off it.
	settings := e.Settings()
	for id, n := range playout.RenditionRefs(settings.Playout) {
		counts[id] += n
	}

	e.mu.RLock()
	src := e.source
	measured := e.measured
	fps := probedFPS(e.videoInfo)
	e.mu.RUnlock()

	// Decided before anything is planned, because between them they settle both
	// the layout every routing graph is compiled against and the hub every
	// consumer reads.
	selSig := wantSelector(settings)
	silenceSig := e.holdSilence(e.wantSilence(settings))
	if silenceSig != "" {
		src = synthTrack()
	}
	// Roles are attached last, after the layout is settled, so that whichever
	// tier is standing in for the ingest the graphs are compiled against the
	// same annotated source the routing editor is showing.
	if anns := settings.Ingest.Annotations; len(anns) > 0 {
		src = src.WithAnnotations(anns)
	}
	// What every consumer folds into its restart hash. With the selector
	// running this is CONSTANT, which is the entire point: the source behind it
	// can change all night without moving a single destination's signature.
	srcSig := upstreamSig(selSig, silenceSig)

	wantRends := wantedRenditions(rendRows, counts, func(r *db.Rendition) string {
		return renditionSig(r, fps, srcSig, e.cfg.DataDir)
	})
	e.mu.RLock()
	haveRends := make(map[int64]string, len(e.rends))
	for id, r := range e.rends {
		// A rendition that failed to start carries an empty spec, so it never
		// matches a wanted one and is retried on the next reconcile.
		haveRends[id] = r.spec
	}
	e.mu.RUnlock()
	startRends, stopRends := diffRenditions(wantRends, haveRends)

	// DO NOT COMPILE A ROUTING GRAPH AGAINST A LAYOUT NOBODY HAS MEASURED.
	//
	// Until the probe lands, e.source is routing.DefaultSource(): six stereo
	// tracks that exist so the routing editor has something to draw, not a claim
	// about what is arriving. reconcileMeters and stemPlanFor both refuse on
	// exactly this — see the comment at reconcileMeters, which spells out that
	// the zero-track check cannot catch it because the placeholder HAS tracks.
	// Destinations were the one process-building consumer that read e.source raw,
	// and they are the ones that matter most.
	//
	// Two ways it goes wrong, and the second is the reason this is a guard rather
	// than a warning. A profile naming a track the stream does not have emits
	// `[0:a:5]` and FFmpeg refuses to start, so the destination crash-loops —
	// loud, and diagnosable. But the placeholder also claims Channels: 2 on every
	// track, so a real 5.1 track compiles to `pan=stereo|c0=c0|c1=c1`, which is
	// VALID FFmpeg: the destination starts, stays up, and publishes front L/R
	// only. Centre — where dialogue lives — is discarded, with no error anywhere.
	//
	// A silence tier substitutes synthTrack() above, which IS a measured layout,
	// so that case is known and proceeds normally.
	//
	// TWO THINGS THIS GUARD GOT WRONG ON THE WAY IN, both found by the failover
	// suite, and both worth stating because the wrong versions read as obviously
	// correct.
	//
	// It is DESTINATIONS ONLY. The first version returned from reconcileOutputs
	// outright, which also skipped the selector, the silence tier, the
	// renditions and playout. Nothing below compiles a routing graph against
	// e.source, so nothing below is exposed to the placeholder; holding them
	// took the slate off air during the outage the selector exists to cover, and
	// the late reconcile put a backwards DTS step in the output — the
	// discontinuity a receiving platform drops the connection on.
	//
	// And it asks MEASURED, not probed. probed means "a layout is arriving right
	// now", and probeLoop clears it after three idle rounds so the UI stops
	// claiming tracks nobody is sending — but it deliberately leaves e.source
	// alone. Only ingest start resets e.source to the placeholder. So an encoder
	// that stopped a moment ago still has a real, measured layout on file, and
	// holding on !probed refused to plan against it: a destination added during
	// a failover could not start until the primary came back. The literal read
	// of the probe is the same mistake holdSilence exists to prevent one tier
	// down — see the comment there.
	//
	// THE HOLD HAS AN EXIT, and needing one is not hypothetical. wantSilence
	// itself requires `measured`, so a probe that can NEVER succeed -- a missing
	// or broken ffprobe, an unidentifiable stream -- left silenceSig empty and
	// measured false forever. Every destination stayed down permanently, with
	// the one tier that could have lifted the hold structurally unable to.
	//
	// So after enough consecutive failures the layout is declared unmeasurable
	// and destinations are planned PROVISIONALLY: the guessed pan matrices are
	// replaced by a runtime downmix, so a wrong layout folds audibly instead of
	// discarding dialogue in silence. That is the property the hold existed to
	// protect, and once it holds by construction the hold is no longer earning
	// its cost. See routing.CompileProvisional.
	//
	// A timeout was the other candidate and is worse: it reintroduces the
	// original bug on a schedule, because it fires just as readily while a probe
	// is merely slow. This fires only when probing has actually failed, and it
	// reverts the instant one succeeds.
	//
	// AND A CEILING ON THE CLOCK, because the count above is blind to time: it
	// only advances while the relay is flowing, so a stream that goes quiet and
	// back can sit held indefinitely without five failures ever accruing. See
	// holdCeiling, which is set above this path's own worst case so it cannot
	// preempt it on a stream that is genuinely being probed.
	now := time.Now()
	unmeasurable := !measured && (e.probeUnmeasurable() || e.holdExpired(now))
	holdDests := !measured && silenceSig == "" && !unmeasurable
	// Stamped from the decision, so the clock starts with the hold and cannot
	// keep running once it is over.
	if holdDests {
		e.holdBegan(now)
	} else {
		e.holdSince.Store(0)
	}
	// Published from the decision itself, not recomputed anywhere else, so
	// /status cannot disagree with the reconcile about why nothing is running.
	// Cleared on every pass that does not hold, so a stale reason cannot outlive
	// the condition that produced it.
	// Worded for an operator watching a dashboard, not for the person who wrote
	// the hold. "A routing graph compiled against the placeholder would map tracks
	// that may not exist" is the true reason and belongs in reconcileOutputs'
	// comment, where it already is; on a card it reads as a fault report about
	// track mapping, which is not what is happening. What the operator needs is
	// that this is a normal, transient, pre-stream state and nothing is wrong.
	var hold *HoldStatus
	if holdDests {
		hold = &HoldStatus{
			Code: "awaiting-ingest-probe",
			Reason: "Waiting for the first look at the incoming stream. " +
				"Destinations start once its audio and video tracks are known.",
		}
	}
	e.destHold.Store(hold)
	switch {
	case holdDests:
		e.noteReload("destinations", "all", reloadRestart,
			"held: the ingest layout has not been probed yet, and a routing graph "+
				"compiled against the placeholder would map tracks that may not exist")
	case unmeasurable:
		e.noteReload("destinations", "all", reloadRestart,
			"the ingest layout cannot be measured, so each track is downmixed by "+
				"FFmpeg at runtime rather than by its profile's matrix")
	}

	// PLANNED AND STOPPED ALWAYS. ONLY THE START IS HELD.
	//
	// The obvious shape -- skip all three while held -- is wrong, and quietly
	// so. Everything below still tears down and replaces hubs: renditions,
	// the silence tier, the selector. Skipping stopDestinations leaves a
	// destination subscribed to a hub that is then closed under it, and closing
	// a hub only stops UDP delivery; it does not end the process. So FFmpeg sits
	// there "started", never restarting because nothing asked it to, receiving
	// nothing. It cost a file destination its entire 76-second run: zero bytes,
	// no error, and a suite that stayed green because the only check that would
	// have seen it was a note rather than an assertion.
	//
	// That ordering -- consumers stopped before their upstream is replaced -- is
	// the invariant the comment above reconcileSilence already states. The hold
	// has no business suspending it. Planning against the placeholder is
	// harmless in itself: the specs it produces are only ever read by
	// startDestinations, which is the one call that must not happen.
	// While held, plans is EMPTY rather than placeholder-derived, and that is a
	// second bug fixed in the same line. stopDestinations keeps a destination
	// whose running spec equals its planned one -- so a destination compiled
	// against a real stereo layout SURVIVED an unmeasured window, because the
	// placeholder is also stereo and produced an identical spec. The next stream
	// being 5.1 changed nothing: it kept running the old graph and published
	// front L/R, silently discarding centre. Exactly the failure the hold exists
	// to prevent, reached through the hold itself.
	//
	// Empty, not nil, and still passed: an unmeasured layout means no
	// destination's graph can be vouched for, so none may keep running. They
	// come back on the next reconcile once the probe lands.
	plans := map[int64]destPlan{}
	if !holdDests {
		plans = e.planDestinations(destRows, wantRends, src, srcSig, unmeasurable)
	}
	e.stopDestinations(plans)
	for _, id := range stopRends {
		e.mu.Lock()
		r := e.rends[id]
		delete(e.rends, id)
		e.mu.Unlock()
		e.teardownRendition(r)
	}

	// One level above the renditions, in the window where nothing at all is
	// reading it. Both directions matter: appearing, it must be up before the
	// renditions that will read it; disappearing, its hub must not close under
	// one. Every consumer below reads e.downstreamHub(), which these decide.
	//
	// The selector goes second because its primary feed READS the silence
	// tier's hub: reconciled the other way round, a feed would be left holding
	// a subscription on a relay that had just closed.
	e.detachFeedForSilence(silenceSig)
	e.reconcileSilence(silenceSig)
	e.reconcileSelector(settings, selSig, silenceSig)

	byID := make(map[int64]*db.Rendition, len(rendRows))
	for _, r := range rendRows {
		byID[r.ID] = r
	}
	for _, id := range startRends {
		e.startRendition(byID[id], wantRends[id], fps, counts[id])
	}

	// After the renditions are up, so a variant never resolves an upstream that
	// has not started, and before the destinations, so the two consumer tiers
	// come up in a deterministic order.
	//
	// One asymmetry against stopDestinations, stated rather than hidden: a
	// variant whose rendition was removed in this same pass is torn down just
	// after that rendition's hub closed rather than just before. What it sees is
	// a UDP socket that stops delivering — the identical pause FFmpeg already
	// rides out on every rendition restart — and it lasts until the line below.
	if err := e.play.Reconcile(settings.Playout, e.playoutUpstream); err != nil {
		// Never fatal to the reconcile: playout is an output, and an output
		// that cannot be packaged must not stop the destinations from sending.
		e.log.Error("playout reconcile", "err", err)
	}

	if !holdDests {
		e.startDestinations(plans)
	}
	return nil
}

// playoutUpstream resolves which relay a playout variant reads: the ingest hub
// for a passthrough rung, the named rendition's own hub otherwise.
//
// It lives on the engine rather than in internal/playout because rendition.hub
// is unexported and in-package, which is the same reason upstreamHub does.
func (e *Engine) playoutUpstream(id *int64) (playout.Upstream, error) {
	if id == nil {
		e.mu.RLock()
		v := e.videoInfo
		e.mu.RUnlock()
		// Not e.hub: with a video-only ingest the source rung has to package the
		// silence tier's output, or it publishes a stream with no audio track
		// and every player that finds one refuses it.
		up := playout.Upstream{Hub: e.downstreamHub(), Label: "source:" + e.sourceLabel()}
		// From the probe, so the master playlist advertises the real ingest
		// rather than a guess. Absent before the first probe, which the
		// packager reads as "unknown" and omits.
		if v != nil {
			up.Width, up.Height = v.Width, v.Height
		}
		return up, nil
	}
	e.mu.RLock()
	r := e.rends[*id]
	e.mu.RUnlock()
	if r == nil || r.hub == nil {
		// Packaging it anyway would publish a rung that resolves to a playlist
		// nothing ever writes a segment into.
		reason := "is not running"
		if r != nil && r.err != "" {
			reason = "failed to start: " + r.err
		}
		return playout.Upstream{}, fmt.Errorf("rendition %d %s", *id, reason)
	}
	return playout.Upstream{
		Hub: r.hub,
		// The id and the encode signature, never the name: the label rides in
		// the variant's restart hash, so labelling by name would cycle a live
		// muxer every time an operator renamed a tier. The id makes moving
		// between two identically configured tiers restart the variant onto the
		// hub it now has to read, which a signature alone would miss.
		Label:     "rendition:" + strconv.FormatInt(*id, 10) + ":" + r.spec,
		Width:     r.row.Width,
		Height:    r.row.Height,
		VideoKbps: r.row.VideoBitrate,
	}, nil
}

func renditionLabel(row *db.Destination) string {
	if row.RenditionID == nil {
		return "passthrough"
	}
	return strconv.FormatInt(*row.RenditionID, 10)
}

// --------------------------------------------------------------- renditions

// wantedRenditions is the ref count made concrete: a shared encode earns a
// process from its first enabled destination and loses it with its last, so a
// tier nobody selects never appears here and never costs a core.
//
// counts comes from the database and omits renditions with no enabled
// destinations entirely; passthrough destinations are not counted because there
// is no process to ref-count.
func wantedRenditions(rows []*db.Rendition, counts map[int64]int, sig func(*db.Rendition) string) map[int64]string {
	want := map[int64]string{}
	for _, r := range rows {
		if counts[r.ID] > 0 {
			want[r.ID] = sig(r)
		}
	}
	return want
}

// diffRenditions splits the wanted encodes against the running ones. A changed
// signature appears in both lists, because a shared encode is replaced rather
// than adjusted: FFmpeg cannot change its output resolution mid-run, and the
// destinations copying its video have to be restarted onto the new stream
// anyway.
func diffRenditions(want, running map[int64]string) (start, stop []int64) {
	for id, sig := range running {
		if w, ok := want[id]; !ok || w != sig {
			stop = append(stop, id)
		}
	}
	for id, sig := range want {
		if r, ok := running[id]; !ok || r != sig {
			start = append(start, id)
		}
	}
	// Sorted so a reconcile is deterministic and reads the same in the log
	// twice running.
	slices.Sort(start)
	slices.Sort(stop)
	return start, stop
}

// renditionSig hashes everything the encode's command line depends on.
//
// Name and note are deliberately absent: renaming a tier must not interrupt the
// destinations riding it. The source frame rate is folded in only when the
// rendition inherits it, since that is the only case where the keyframe
// interval — counted in frames — depends on what the ingest is doing.
func renditionSig(r *db.Rendition, sourceFPS float64, silenceSig, dataDir string) string {
	parts := []string{
		strconv.Itoa(r.Width), strconv.Itoa(r.Height), strconv.Itoa(r.FPS),
		strconv.Itoa(r.VideoBitrate), string(r.Encoder), r.Preset,
		strconv.FormatFloat(r.GOPSeconds, 'g', -1, 64),
		// The rest of the rate-control triple. Both reach the command line --
		// rendition.go writes `-maxrate` and `-bufsize` from them -- so a
		// ceiling or buffer edit is a different encode. They were missing, which
		// meant the UI accepted the change, the row stored it, and the running
		// encoder kept the old ceiling until something else happened to restart
		// it. Exactly the Deinterlace defect above, in a different field.
		strconv.Itoa(r.MaxrateKbps), strconv.Itoa(r.BufsizeKbps),
		// Aspect conversion changes the filter chain without changing any
		// dimension, so it has to be named here or picking a mode would be
		// saved and never encoded.
		r.AspectMode, r.PadColor,
		// Deinterlace, for exactly the same reason -- and it was missing until
		// the overlay work went looking. Changing a rendition from progressive
		// to `all` was stored, shown in the UI, and never reached the running
		// encoder, because nothing in this list changed and the supervisor had
		// no reason to restart it.
		r.Deinterlace,
		// The overlay's SHAPE. Every field, because each one changes the filter
		// graph: a different anchor, width or opacity is a different encode.
		overlaySig(r.Overlay, dataDir),
		textSig(r.Text, dataDir),
		// Which relay it reads. RenditionArgs copies audio with -map 0:a, so a
		// tier started against the raw ingest of a video-only stream produces a
		// video-only hub; it has to be restarted onto the silence tier when one
		// appears, and back off it when the ingest gains real tracks.
		silenceSig,
	}
	if r.FPS == 0 {
		parts = append(parts, strconv.FormatFloat(sourceFPS, 'g', -1, 64))
	}
	return hashStrings(parts)
}

// overlaySig hashes the overlay's shape, plus the image file's size and
// modification time.
//
// The file stat is here on purpose. Everything else in the signature is a
// database field, but an operator who replaces logo.png with a new one has
// changed the encode without changing a single row -- and without the stat the
// running encoder would keep compositing the old image until something
// unrelated restarted it. The file's CONTENTS are not read: a stat per
// reconcile is cheap, and hashing the bytes on every sweep is not.
func overlaySig(o db.RenditionOverlay, dataDir string) string {
	if !o.Active() {
		return ""
	}
	parts := []string{
		o.Image, o.Anchor,
		strconv.FormatFloat(o.WidthPct, 'g', -1, 64),
		strconv.FormatFloat(o.MarginXPct, 'g', -1, 64),
		strconv.FormatFloat(o.MarginYPct, 'g', -1, 64),
		strconv.FormatFloat(o.Opacity, 'g', -1, 64),
	}
	if fi, err := os.Stat(filepath.Join(dataDir, filepath.FromSlash(o.Image))); err == nil {
		parts = append(parts,
			strconv.FormatInt(fi.Size(), 10),
			strconv.FormatInt(fi.ModTime().UnixNano(), 10))
	} else {
		// A missing file is itself a state worth restarting on: once the
		// operator puts it there, the encode has to pick it up. Naming the
		// absence -- rather than silently omitting the stat -- means the
		// signature changes the moment the file appears.
		parts = append(parts, "missing")
	}
	return strings.Join(parts, "\x00")
}

// textSig is overlaySig for burned-in text.
//
// The FONT FILE is stat'ed as well as named, for exactly the reason the overlay
// image is: an operator who replaces MyStation.ttf in the fonts directory with
// a corrected version has changed what the stream looks like, and a signature
// built only from the stored name would not notice. Naming a missing font
// rather than omitting it means the encode restarts the moment the file
// appears.
func textSig(t db.RenditionText, dataDir string) string {
	if !t.Active() {
		return ""
	}
	parts := []string{
		t.Content, t.Font, t.Anchor, t.Color, t.BoxColor,
		strconv.FormatFloat(t.SizePct, 'g', -1, 64),
		strconv.FormatFloat(t.MarginXPct, 'g', -1, 64),
		strconv.FormatFloat(t.MarginYPct, 'g', -1, 64),
		strconv.FormatBool(t.Box),
		strconv.FormatFloat(t.BoxOpacity, 'g', -1, 64),
	}
	if p, err := textFontPath(t.Font, dataDir); err == nil {
		if fi, err := os.Stat(p); err == nil {
			parts = append(parts,
				strconv.FormatInt(fi.Size(), 10),
				strconv.FormatInt(fi.ModTime().UnixNano(), 10))
		} else {
			parts = append(parts, "missing")
		}
	} else {
		parts = append(parts, "unresolved")
	}
	return strings.Join(parts, "\x00")
}

// textFontPath resolves a stored font NAME to an absolute path.
//
// Empty means the built-in default, which is how a rendition that asks for text
// and names no font still draws: the shipping image has no system fonts, so
// leaving fontfile unset would fall through to fontconfig and find nothing.
func textFontPath(name, dataDir string) (string, error) {
	if strings.TrimSpace(name) == "" {
		name = ffmpeg.DefaultFont
	}
	return ffmpeg.FontPath(filepath.Join(dataDir, ffmpeg.FontsDirName), name)
}

// textSpecOf maps the stored text onto the filter's spec.
//
// A font that will not resolve yields NO TEXT rather than an error or a
// fallback. The alternatives are both worse: erroring takes the whole rendition
// off air over a caption, and silently substituting a different font ships a
// frame the operator did not design. Validation refuses a bad name at save
// time, so reaching here means the file was removed from under a running
// install -- and textSig has already made that a restart trigger, so the text
// returns by itself when the font does.
func textSpecOf(t db.RenditionText, dataDir string) *ffmpeg.TextSpec {
	if !t.Active() {
		return nil
	}
	font, err := textFontPath(t.Font, dataDir)
	if err != nil {
		// No logger here on purpose: this is called from renditionSpecOf, which
		// is a pure mapping the tests drive directly. The one production caller
		// notices the Active-but-nil case and logs it, so the silence is local
		// rather than total.
		return nil
	}
	return &ffmpeg.TextSpec{
		Text:       t.Content,
		FontFile:   font,
		Anchor:     ffmpeg.OverlayAnchor(t.Anchor),
		SizePct:    t.SizePct,
		Color:      t.Color,
		MarginXPct: t.MarginXPct,
		MarginYPct: t.MarginYPct,
		Box:        t.Box,
		BoxColor:   t.BoxColor,
		BoxOpacity: t.BoxOpacity,
	}
}

// overlaySpecOf resolves the stored relative path against the data directory.
//
// Joined to an absolute path only here, at process-build time, exactly as the
// slate image is. The stored value stays relative so it cannot be an absolute
// read primitive for whoever reaches the renditions API; db validation refuses
// traversal, and this is the one place the two halves meet.
func overlaySpecOf(o db.RenditionOverlay, dataDir string) *ffmpeg.OverlaySpec {
	if !o.Active() {
		return nil
	}
	opacity := o.Opacity
	if opacity <= 0 {
		// A row stored before opacity existed, or one saved as 0. Treated as
		// fully opaque rather than fully transparent: an invisible watermark is
		// indistinguishable from a broken one, and the operator asked for a
		// watermark.
		opacity = 1
	}
	return &ffmpeg.OverlaySpec{
		ImagePath:  filepath.Join(dataDir, filepath.FromSlash(o.Image)),
		Anchor:     ffmpeg.OverlayAnchor(o.Anchor),
		WidthPct:   o.WidthPct,
		MarginXPct: o.MarginXPct,
		MarginYPct: o.MarginYPct,
		Opacity:    opacity,
	}
}

// renditionSpecOf maps a stored rendition onto the encode's command line.
//
// There is no audio field to map, and there must never be one: RenditionArgs
// copies every audio track through with -c:a copy, which is what leaves the
// per-destination routing graphs downstream with the full multitrack ingest to
// work from.
func renditionSpecOf(r *db.Rendition, in, out string, sourceFPS float64, vaapiDevice, dataDir string) ffmpeg.RenditionSpec {
	return ffmpeg.RenditionSpec{
		Overlay:     overlaySpecOf(r.Overlay, dataDir),
		Text:        textSpecOf(r.Text, dataDir),
		Deinterlace: ffmpeg.DeinterlaceMode(r.Deinterlace),
		// Only meaningful for the VAAPI encoders, and empty everywhere else.
		// Until this was threaded through, every VAAPI rendition fell back to
		// FFmpeg's built-in default node, so a machine with a second GPU could
		// not be told to use it -- the detection had always chosen a device,
		// and nothing ever asked for the answer.
		VAAPIDevice: vaapiDevice,
		InRelayURL:  in,
		OutRelayURL: out,
		Width:       r.Width,
		Height:      r.Height,
		FPS:         float64(r.FPS),
		SourceFPS:   sourceFPS,
		VideoKbps:   r.VideoBitrate,
		// Both 0 unless somebody set them, and RenditionArgs reads 0 as "derive
		// the CBR relationship" -- so an install that never touches these emits
		// the command line it always did. Until this mapping existed the fields
		// were on the spec, used by the argv builder, and reachable from
		// nowhere. #341.
		MaxrateKbps: r.MaxrateKbps,
		BufsizeKbps: r.BufsizeKbps,
		Encoder:     string(r.Encoder),
		Preset:      r.Preset,
		GOPSeconds:  r.GOPSeconds,
		Aspect:      ffmpeg.AspectMode(r.AspectMode),
		PadColor:    r.PadColor,
	}
}

// renditionEncoderProblem reports why this encoder cannot run here, or nil when
// nothing is known against it.
//
// Two different questions, asked in that order because they have two different
// answers. `ffmpeg -encoders` says what the BUILD registers; the test encode
// says what the MACHINE can do, and a stock Linux build lists nvenc, qsv, vaapi
// and amf on a box with no GPU in it at all. A saved rendition goes stale for
// the same reasons — the card was swapped, the driver was upgraded, the
// container lost its --device passthrough — and the failure has to name the
// cause here rather than leave FFmpeg crash-looping on a driver error nobody is
// reading.
//
// Both checks are silent when detection could not run: an empty encoder list
// and an unprobed encoder both mean "we do not know", and detection that could
// not answer must never be the thing that stops a stream.
func renditionEncoderProblem(tools *ffmpeg.Tools, encoder db.VideoEncoder) error {
	if tools == nil {
		return nil
	}
	if len(tools.VideoEncoders) > 0 && !tools.HasEncoder(string(encoder)) {
		return fmt.Errorf("this FFmpeg build has no %s encoder", encoder)
	}
	works, reason := tools.EncoderWorks(string(encoder))
	if works || notProbed(reason) {
		return nil
	}
	if reason == "" {
		reason = "the test encode failed without saying why"
	}
	return fmt.Errorf("%s did not pass its test encode on this machine: %s. "+
		"Choose a different encoder, or fix the driver and re-detect hardware from the rendition editor",
		encoder, reason)
}

// notProbed distinguishes "the encoder failed" from "we never got to ask it".
//
// Detection marks a probe it could not run — a cancelled scan, an expired
// budget — as not working, with a reason that says so. Read literally, one
// cancelled detection would refuse every rendition on the box. That is the
// exact shape of the SRT check that used to stop the server from starting, and
// the lesson from it was the same one: a capability check must fail open.
func notProbed(reason string) bool {
	return strings.HasPrefix(reason, "not probed:")
}

// startRendition brings up one shared encode: a hub of its own, a subscription
// on the ingest hub for its input, and a supervised FFmpeg between them.
//
// consumers is the ref count that earned it a process, carried through for the
// log line only — the decision itself was made from the database.
func (e *Engine) startRendition(row *db.Rendition, spec string, sourceFPS float64, consumers int) {
	if row == nil {
		return
	}
	fail := func(err error) {
		// Recorded rather than returned: the destinations downstream have to be
		// told why they are not starting, and the next reconcile retries.
		e.mu.Lock()
		e.rends[row.ID] = &rendition{row: row, err: err.Error()}
		e.mu.Unlock()
		e.log.Error("start rendition", "rendition", row.Name, "err", err)
	}

	if err := renditionEncoderProblem(e.tools, row.Encoder); err != nil {
		fail(err)
		return
	}

	port, err := e.alloc.Allocate()
	if err != nil {
		fail(err)
		return
	}
	// Its own hub, so its destinations read the encoded stream instead of the
	// ingest. Port 0 lets the kernel pick, well clear of the allocator's range.
	hub, err := relay.New(e.log, 0)
	if err != nil {
		e.alloc.Release(port)
		fail(err)
		return
	}

	// The selector's hub when the tier is running, the silence tier's when it
	// is not. RenditionArgs copies audio through untouched, so reading the raw
	// ingest of a video-only stream here would produce a rendition hub with no
	// audio track at all and break every destination on this tier.
	upstream := e.selectorHub()
	if upstream == nil {
		if err := e.selectorProblem(); err != nil {
			e.alloc.Release(port)
			_ = hub.Close()
			fail(err)
			return
		}
		if err := e.silenceProblem(); err != nil {
			e.alloc.Release(port)
			_ = hub.Close()
			fail(err)
			return
		}
		upstream = e.sourceHub()
	}

	subName := fmt.Sprintf("rendition:%d", row.ID)
	in := upstream.Subscribe(subName, port)
	rspec := renditionSpecOf(row, in, hub.InputURL(), sourceFPS, e.vaapiDevice(row), e.cfg.DataDir)
	// An FFmpeg with no drawtext filter must not be handed one.
	//
	// This is not a cosmetic guard. Found by running the renditions acceptance
	// suite against a Homebrew FFmpeg built without libfreetype: the graph is
	// rejected with "No such filter: 'drawtext'", the encode dies, the
	// supervisor restarts it, and it dies again. The whole rendition is off
	// air -- along with every destination feeding from it -- because somebody
	// asked for a caption.
	//
	// Dropping the text keeps the picture up. The same choice the unresolvable
	// font makes below, and for the same reason: a missing caption is a
	// disappointment, a missing stream is an outage.
	if rspec.Text != nil && !e.tools.HasFilter("drawtext") {
		rspec.Text = nil
		e.log.Warn("this FFmpeg has no drawtext filter, so no text is drawn; "+
			"the rendition runs without it rather than failing to start",
			"rendition", row.ID)
	}
	// Configured text that produced no spec means the font could not be
	// resolved. Validation refuses a bad name at save time, so this is a font
	// removed from under a running install. Said out loud, because the operator
	// otherwise sees a stream that starts fine and simply has no caption --
	// which is the failure mode this whole feature keeps trying to avoid.
	if row.Text.Active() && rspec.Text == nil && e.tools.HasFilter("drawtext") {
		e.log.Warn("rendition text has no usable font, so no text is drawn",
			"rendition", row.ID, "font", row.Text.Font)
	}
	args := ffmpeg.RenditionArgs(rspec)

	proc := supervisor.New(e.log, supervisor.Spec{
		Name: subName, Kind: "rendition", Bin: e.tools.FFmpeg, Args: args,
		AutoRestart: true, OnLog: e.onLog, OnState: e.onState, LogSink: logSink{e},
	})

	e.mu.Lock()
	// Shutdown may have run since this reconcile started; publishing under the
	// same lock Stop collects processes with is what keeps a late start from
	// becoming an orphaned encoder holding a UDP socket.
	if e.stopped {
		e.mu.Unlock()
		upstream.Unsubscribe(subName)
		e.alloc.Release(port)
		_ = hub.Close()
		return
	}
	e.rends[row.ID] = &rendition{
		row: row, proc: proc, hub: hub, in: upstream,
		port: port, subName: subName, spec: spec,
	}
	e.mu.Unlock()

	proc.Start()
	e.log.Info("rendition started", "rendition", row.Name, "encoder", row.Encoder,
		"bitrate", row.VideoBitrate, "consumers", consumers, "relayPort", hub.Port())
	e.noteReload("rendition", row.Name, reloadRestart, "started")
}

func (e *Engine) teardownRendition(r *rendition) {
	if r == nil {
		return
	}
	e.noteReload("rendition", r.row.Name, reloadRestart,
		"its encode signature changed, or nothing selects it any more")
	if r.proc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		_ = r.proc.Stop(ctx)
		cancel()
	}
	if r.subName != "" {
		// Its own input hub, which is not always the ingest's.
		in := r.in
		if in == nil {
			in = e.hub
		}
		in.Unsubscribe(r.subName)
	}
	if r.port != 0 {
		e.alloc.Release(r.port)
	}
	// After the process, so the encode is never writing into a closed socket.
	if r.hub != nil {
		_ = r.hub.Close()
	}
}

func (e *Engine) stopAux(slot **supervisor.Process, name string) {
	e.mu.Lock()
	proc := *slot
	*slot = nil
	var port int
	// The ingest hub unless this consumer says otherwise. The meters sidecar and
	// the preview both read whichever tier is on air, so both unsubscribe from
	// the hub they actually JOINED -- unsubscribing from e.hub instead leaves a
	// live subscription on a selector hub that is about to close.
	hub := e.hub
	switch name {
	case "recorder":
		port, e.recorderPort = e.recorderPort, 0
		e.recorderSig = ""
	case "preview":
		port, e.previewPort = e.previewPort, 0
		e.previewSig = ""
		if e.previewHub != nil {
			hub, e.previewHub = e.previewHub, nil
		}
	case "meters":
		port, e.metersPort = e.metersPort, 0
		e.metersSig = ""
		if e.metersHub != nil {
			hub, e.metersHub = e.metersHub, nil
		}
	}
	e.mu.Unlock()

	if proc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		_ = proc.Stop(ctx)
		cancel()
	}
	hub.Unsubscribe(name)
	if port != 0 {
		e.alloc.Release(port)
	}
}

// ------------------------------------------------------------------ probing

// probeLoop keeps the ingest's track layout up to date.
//
// Probing costs a short-lived ffprobe against its own relay subscription. It
// runs often while the layout is unknown and slowly once it is settled, so a
// streamer who changes their OBS track count mid-session is picked up without
// a restart.
func (e *Engine) probeLoop(ctx context.Context) {
	fast := probeFastCadence
	slow := probeSlowCadence
	timer := time.NewTimer(fast)
	defer timer.Stop()

	// Probing is gated on the relay actually carrying data, not on the ingest
	// process being up. An SRT listener sits in "running" for as long as it
	// waits for a publisher, and conversely data can still be in flight while
	// the ingest is momentarily restarting. Bytes on the relay is the only
	// signal that means "there is a stream to probe".
	lastRx := e.hub.RxBytes()
	idleRounds := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		rx := e.hub.RxBytes()
		flowing := rx > lastRx
		lastRx = rx

		next := fast
		if flowing {
			idleRounds = 0
			// The hold's exit is a STATE TRANSITION, not a layout change, and
			// reconciling only on `changed` left the exit inert.
			//
			// probeOnce returns false on every failure, so the fifth one -- the
			// one that declares the layout unmeasurable -- looked identical to
			// the four before it. The log line went out, and nothing re-planned:
			// destinations stayed held until some unrelated HTTP request
			// happened to call Reconcile, which on an unattended box is never.
			// The fix for a permanent outage that was itself permanently inert.
			wasUnmeasurable := e.probeUnmeasurable()
			changed := e.probeOnce(ctx)
			if changed || e.probeUnmeasurable() != wasUnmeasurable {
				// Layout changed: the meters process and every destination
				// graph were built against the old one, and a rendition that
				// inherits the source frame rate has a keyframe interval
				// derived from it. The recorder is in this list only when it
				// is writing stems, which are planned one per probed track —
				// its signature is unchanged otherwise, so this costs nothing.
				//
				// UNDER reconcileMu, which this used to skip. Reconcile holds it
				// end to end (see the field comment); this path is the only other
				// production caller of reconcileOutputs, and unserialised the two
				// now DISAGREE where they used to agree. `measured` flips inside
				// the probeOnce just above, so a Reconcile that snapshotted
				// measured=false is still mid-pass holding an empty plan set
				// while this pass sees measured=true and starts every
				// destination -- then that pass reaches stopDestinations({}) and
				// tears down everything this one just started. Nothing restarts
				// them: the layout is stable so no later probe reports `changed`,
				// and Reconcile is event-driven with no ticker.
				//
				// Taken HERE, not inside reconcileOutputs: Reconcile already
				// holds it when it calls that, and the mutex is not reentrant.
				// AND THE CONSUMERS OF WHAT reconcileOutputs JUST STARTED.
				//
				// This used to run three of Reconcile's steps and stop, which is
				// the hazard the paragraph above describes arriving from the
				// other direction: a hand-copied subset of a sequence drifts the
				// moment the sequence grows. It had already drifted. The
				// analyser tier is reconciled ONLY by reconcileLoudness, this is
				// the path that starts destinations on every fresh install --
				// they are held until a probe lands -- and Reconcile is
				// event-driven with no ticker, so nothing ran it again.
				//
				// The result was #612: an install reporting the loudness monitor
				// enabled with no analyser ever having run. GET /loudness said
				// so, the Meters page drew the switch on, and nothing measured.
				// Only toggling the switch off and on recovered it, because that
				// was one of the few things that reached a real Reconcile.
				//
				// Preview, clips and captions are here for the same reason and
				// were wrong in the same way: each reads a hub that
				// reconcileOutputs may have just replaced. Their own comment in
				// Reconcile says they must run "after the outputs, because these
				// read the hub that the silence and selector tiers decide" --
				// and this path settles exactly that and then skipped them.
				settings := e.Settings()
				e.reconcileMu.Lock()
				e.reconcileMeters(settings)
				e.reconcileRecorder(settings)
				_ = e.reconcileOutputs()
				e.reconcilePreview(settings)
				e.reconcileClips()
				e.reconcileCaptions()
				e.reconcileLoudness(settings)
				e.reconcileMu.Unlock()
				e.publishStatus()
			}
			e.mu.RLock()
			probed := e.probed
			e.mu.RUnlock()
			if probed {
				next = slow
			}
		} else {
			idleRounds++
			// The stream has stopped. Forget the layout so the UI stops
			// claiming tracks that are no longer arriving, and so the next
			// stream is probed promptly rather than on the slow cadence.
			if idleRounds >= 3 {
				e.mu.Lock()
				wasProbed := e.probed
				e.clearProbed()
				e.mu.Unlock()
				if wasProbed {
					e.log.Info("ingest stopped; track layout cleared")
					e.bus.Publish(events.TypeSource, e.SourceInfo())
				}
			}
		}
		timer.Reset(next)
	}
}

// probeFailedNow counts a probe that measured nothing and says so at the level
// the situation deserves, which depends on whether a layout is ALREADY MEASURED.
//
// The messages used to be unconditional, and after a layout had been measured
// both of them were false. "destinations are held until a layout is measured"
// describes a hold that is not happening; "starting destinations with a runtime
// downmix instead of their routing matrices" describes a switch that is not
// happening either. Both branches in reconcileOutputs are guarded by !measured
// -- verified, so nothing was ever actually held or downmixed on this path --
// but a log line that asserts a state transition the engine did not make is a
// bug in its own right.
//
// It became routine rather than rare with the probe's read timeout: a probe now
// FAILS on a relay that has gone quiet instead of blocking until something kills
// it, and a source selector produces quiet relays several times a minute around
// a slate. Alarming lines printed on an ordinary slate transition are how an
// operator learns to ignore the line that matters -- and worse, how a genuine
// downmix gets dismissed as "it always says that".
//
// So after a measurement stands, the honest report is a probe that failed with
// no consequence, at INFO, and the real risk if it keeps failing is STALENESS --
// the engine is trusting a cached layout, not falling back to a guessed one.
func (e *Engine) probeFailedNow(err error) {
	e.mu.RLock()
	measured := e.measured
	e.mu.RUnlock()

	n := e.probeFails.Add(1)
	first := !e.probeFailed.Swap(true)

	if measured {
		if first {
			e.log.Info("ingest probe failed; keeping the layout already measured",
				"err", err, "source", e.sourceID)
		} else if n == probeGiveUp {
			e.log.Warn("ingest probes keep failing; the layout in use is the last one measured and may no longer match the stream",
				"failures", n, "err", err, "source", e.sourceID)
		}
		return
	}

	if first {
		e.log.Warn("ingest probe failed; destinations are held until a layout is measured",
			"err", err, "source", e.sourceID)
	} else if n == probeGiveUp {
		// The transition out of the hold, said once and plainly. An operator
		// whose destinations just came up carrying an approximate mix needs
		// to be able to find out why from the log alone.
		e.log.Warn("ingest layout cannot be measured; starting destinations with a runtime downmix instead of their routing matrices",
			"failures", n, "err", err, "source", e.sourceID)
	}
}

func (e *Engine) probeOnce(ctx context.Context) bool {
	port, err := e.alloc.Allocate()
	if err != nil {
		// THE THIRD WAY TO MEASURE NOTHING, and it used to be the only one that
		// said nothing and counted for nothing. Allocate walks the whole range
		// binding each candidate, so it fails under exactly the conditions that
		// make everything else here fragile: a box with many children and not
		// enough free UDP ports.
		//
		// Returning silently made that a PERMANENT, INVISIBLE hold. Destinations
		// are held until a layout is measured; the hold's exit needs
		// probeGiveUp consecutive failures; and a probe that never ran recorded
		// no failure. So the one condition where the box is too loaded to probe
		// was the one condition the exit could not reach.
		//
		// Counted and logged like an ffprobe failure, because to the hold they
		// are the same event: no layout was measured, and the reason it was not
		// is not something waiting longer will change.
		e.probeFailedNow(fmt.Errorf("no free relay port to probe the ingest: %w", err))
		return false
	}
	defer e.alloc.Release(port)

	name := "probe"
	url := e.hub.Subscribe(name, port)
	defer e.hub.Unsubscribe(name)

	// Captured BEFORE the read, checked before the write. Everything between is
	// up to ten seconds during which the ingest can be restarted or switched to
	// another transport; see sourceGen.
	e.mu.RLock()
	gen := e.sourceGen
	e.mu.RUnlock()

	pctx, cancel := context.WithTimeout(ctx, probeAttemptTimeout)
	defer cancel()

	res, err := ffmpeg.Probe(pctx, e.tools.FFprobe, url, 3)
	if err != nil {
		// SAY SO. This used to return silently, and silence here is expensive:
		// destinations are held until a probe lands, so a probe that can never
		// land (missing ffprobe, an unusable stream) leaves every destination
		// down with nothing anywhere explaining why.
		//
		// Logged once per run of failures rather than every time. probeLoop
		// retries on a 3s cadence while bytes are flowing, and an unconditional
		// line here would bury the rest of the log within minutes.
		e.probeFailedNow(err)
		return false
	}
	if e.probeFailed.Swap(false) {
		e.log.Info("ingest probe recovered", "source", e.sourceID)
	}
	// A probe that succeeded and found no audio is a RESULT, not a failure, and
	// it used to be thrown away here. It is the only evidence that an ingest is
	// video-only, which is what the silence tier is started from — and without
	// it a destination on such a stream was compiled against six assumed tracks
	// and then crash-looped mapping audio that was never there.
	//
	// It still has to be a probe that ran: `err != nil` above returns early, so
	// "we could not ask" never reaches this and never synthesises anything.
	if res.Video == nil && len(res.Audio) == 0 {
		// Neither video nor audio is not a video-only stream, it is a probe that
		// read a few packets of something it could not identify yet.
		//
		// COUNTS TOWARD GIVING UP. A probe that ran and identified nothing is
		// exactly as unmeasurable as one that could not run, and this is the
		// "unidentifiable stream" case the hold's exit was written for. The
		// reset used to sit above this return, so this branch cleared the
		// counter on every attempt and it could never reach probeGiveUp --
		// which left the one shape the exit most needed to cover wedged in the
		// hold forever. Both reviewers found it independently.
		e.probeFails.Add(1)
		return false
	}
	// Reset only once there is a real result to commit. Anything that returns
	// before this point failed to measure the layout, whatever the reason.
	//
	// AND THAT INCLUDED A RETURN BELOW THIS LINE, which is why the reset moved.
	// The stale-generation discard commits nothing -- the stream it measured is
	// no longer arriving -- but it used to run AFTER the counter had already been
	// cleared, so a probe that measured nothing reported the same thing to the
	// hold's exit as one that measured everything. Same shape as the
	// identified-nothing branch above, whose comment records it being found by
	// two reviewers; the fix was applied to that branch and not to this one.
	//
	// It matters because the two conditions compound. A probe takes up to ten
	// seconds, and the window where an ingest restarts is exactly the window
	// where the encoder is least stable -- so on a loaded box every probe can be
	// discarded as stale, each one resetting the counter that is supposed to end
	// the wait, and destinations stay held with nothing in the log to say why.
	src := routing.Source{}
	for _, a := range res.Audio {
		src.Tracks = append(src.Tracks, routing.Track{
			Index: a.Index, Channels: a.Channels, Codec: a.Codec,
			Layout: a.Layout, Language: a.Language, Title: a.Title,
		})
	}

	e.mu.Lock()
	// The frame rate counts as a change because a rendition that inherits it
	// converts its keyframe interval from seconds into frames; a rendition
	// started before the first probe is running on the assumed rate.
	changed := !sameSource(e.source, src) || !e.probed ||
		probedFPS(e.videoInfo) != probedFPS(res.Video)
	if e.sourceGen != gen {
		// The ingest was restarted or switched while this probe was reading, so
		// what it measured belongs to a stream that is no longer arriving.
		// Committing it would mark a dead transport's layout `measured` under
		// the new mode and satisfy the guard permanently.
		//
		// The counter is deliberately LEFT ALONE rather than cleared or
		// incremented. Clearing it is the bug above. Incrementing it would be
		// wrong too: a restart is not evidence that the layout cannot be read,
		// and reconcileIngest already resets the count for each new stream on
		// purpose -- the failures are about THIS stream.
		e.mu.Unlock()
		return false
	}
	// Committing, so the layout was measured: this is the real result the reset
	// was always meant to be paired with.
	e.probeFails.Store(0)
	e.commitProbe(src, res.Video, e.settings.Ingest.Mode)
	e.mu.Unlock()

	if changed {
		e.log.Info("ingest layout probed", "audioTracks", len(src.Tracks))
		e.bus.Publish(events.TypeSource, e.SourceInfo())
		// #674: any destination started before this moment characterised an
		// input that did not yet carry audio, and FFmpeg never re-probes. This
		// is the earliest instant a fresh probe would succeed.
		if len(src.Tracks) > 0 {
			e.reprobeDestinationsThatNeverPublished("ingest layout probed")
		}
		// #627: the video codec is the ENCODER's choice and is only knowable
		// here, so this is the first moment an operator can be told that an
		// HEVC or AV1 ingest will be rejected by a named destination -- rather
		// than learning it from the platform.
		if res.Video != nil {
			if rows, derr := e.store.ListDestinationsBySource(e.sourceID); derr == nil {
				e.warnVideoCodec(res.Video.Codec, rows)
			}
		}
	}
	return changed
}

// probedFPS is the ingest's frame rate, or 0 while it is unknown — which the
// rendition builder reads as "assume a conservative rate".
func probedFPS(v *ffmpeg.VideoStream) float64 {
	if v == nil {
		return 0
	}
	return v.FrameRate
}

func sameSource(a, b routing.Source) bool {
	if len(a.Tracks) != len(b.Tracks) {
		return false
	}
	for i := range a.Tracks {
		if a.Tracks[i].Index != b.Tracks[i].Index ||
			a.Tracks[i].Channels != b.Tracks[i].Channels ||
			a.Tracks[i].Codec != b.Tracks[i].Codec {
			return false
		}
	}
	return true
}

// annotate attaches the operator's stored track roles to a probed layout.
//
// It is deliberately the ONLY place annotations meet a Source. The probe
// overwrites e.source wholesale from ffprobe on every reconnect, so anything
// stored on that struct would vanish the first time the encoder blinked; going
// through here means the roles survive a reconnect without sameSource() having
// to know they exist.
func (e *Engine) annotate(src routing.Source) routing.Source {
	anns := e.Settings().Ingest.Annotations
	if len(anns) == 0 {
		return src
	}
	return src.WithAnnotations(anns)
}

// Source returns the track layout a routing profile is compiled against.
//
// That is the ingest's own layout, except while the silence tier is standing in
// for a video-only ingest — then it is the tier's single synthetic track,
// because that is what every destination below it actually receives. Callers
// that compile a graph must see the same layout the engine compiles against, or
// the routing editor and the running destination disagree.
func (e *Engine) Source() routing.Source {
	return e.effectiveSource()
}

// SourceKnown is Source plus whether that layout is a MEASUREMENT or the
// placeholder, so a caller can say which it is showing.
//
// Source() alone discards the bit, and every API path that compiles a routing
// preview used it. The result was that an unprobed engine handed the operator a
// filterComplex containing [0:a:5] and 2-channel pans, labelled as their
// destination's routing, while reconcileOutputs was refusing to run that very
// graph -- the screen and the process disagreeing, in the direction that makes
// the placeholder look authoritative.
//
// Deliberately NOT a refusal. Configuring destinations before any stream has
// connected is the normal order (see the refuseIfSilent reasoning in the API),
// so the preview stays; it just has to admit what it is compiled from.
//
// With no engine at all the answer is the placeholder and false — the same
// shape an engine that has not probed yet gives, so the routing editor has a
// layout to draw and is still told not to believe it. The guard is on the
// exported method and NOT on effectiveSourceKnown, deliberately: Source()
// discards the "known" bit, and every caller of that compiles a command line
// from what it gets back. Those refuse at the API boundary instead of being
// handed six tracks that do not exist. See Engine.Status.
func (e *Engine) SourceKnown() (routing.Source, bool) {
	if e == nil {
		return routing.DefaultSource(), false
	}
	return e.effectiveSourceKnown()
}

// SourceInfo is the ingest layout as the API reports it.
type SourceInfo struct {
	// ID and Name identify the programme this snapshot belongs to. They are
	// here because a Status handed to anything outside the WebSocket -- MQTT
	// telemetry, Home Assistant discovery -- has to say which source it
	// describes, and until now nothing in the payload did.
	ID     int64               `json:"id"`
	Name   string              `json:"name"`
	Probed bool                `json:"probed"`
	Tracks []routing.Track     `json:"tracks"`
	Video  *ffmpeg.VideoStream `json:"video,omitempty"`
	// Synthetic reports that Tracks describes the silence tier's output rather
	// than the ingest's. The routing editor has to be shown the layout the
	// graphs are actually compiled against, or an operator on a video-only
	// ingest would be offered nothing to route.
	Synthetic bool `json:"synthetic,omitempty"`
	// Annotations is what the operator has said each track is. omitempty so an
	// install that has never opened the roles editor sends the payload it
	// always did.
	Annotations []routing.TrackAnnotation `json:"annotations,omitempty"`
	// MetersDropped is how many TRAILING tracks the metering process could not
	// cover, because amerge refuses past 64 channels.
	//
	// ffmpeg.MetersDropped has computed this since the limit was introduced,
	// and its comment says why: so a wide ingest "degrades visibly instead of
	// silently metering a prefix and letting an operator believe a track is
	// silent when it is merely unmeasured". Nothing carried it out of that
	// package, so the degradation was not visible anywhere and the meters page
	// drew flat bars indistinguishable from real silence -- on the one page
	// whose entire job is telling those two apart.
	//
	// Zero for every ingest anyone is likely to send: 32 stereo tracks fit. It
	// is omitempty for exactly that reason, so the common payload is unchanged.
	MetersDropped int `json:"metersDropped,omitempty"`
}

// SourceInfo returns the layout downstream graphs are compiled against, which
// is the probe's unless the silence tier is standing in for it.
//
// With no engine the zero value says exactly what is true: id 0, unprobed, no
// tracks. Unlike Status this needs no empty-slice fixing up — types.ts already
// declares tracks nullable, because an unprobed engine has always answered that
// way. See Engine.Status.
func (e *Engine) SourceInfo() SourceInfo {
	if e == nil {
		return SourceInfo{}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.sourceInfoLocked()
}

// sourceInfoLocked is SourceInfo for a caller that already holds e.mu.
//
// It exists so Status can read the process states and the source layout under
// ONE acquisition instead of two. Between two acquisitions a reconcile can land,
// and the snapshot then pairs the ingest's state with a layout from a different
// instant -- "running" beside a track list that has just been invalidated, which
// reads to an operator as a fault that is not there.
//
// It does not make the WHOLE snapshot atomic, and that is deliberate rather than
// forgotten: Renditions, Loudness, ClipBuffer and Failover go to the database or
// to subsystems with their own mutexes, and holding e.mu across a database query
// to tidy a display inconsistency would be a bad trade. See status.go.
func (e *Engine) sourceInfoLocked() SourceInfo {
	src, video := e.source, e.videoInfo
	synthetic := e.silence != nil && e.silence.hub != nil
	if synthetic {
		src = synthTrack()
	}
	// Computed from the SAME track list this snapshot reports, not from what
	// the running meters process happens to have been started with. Those can
	// differ for one reconcile, and the number an operator reads must describe
	// the layout beside it rather than a previous one.
	chans := make([]int, 0, len(src.Tracks))
	for _, t := range src.Tracks {
		chans = append(chans, t.Channels)
	}
	return SourceInfo{
		ID: e.sourceID, Name: e.sourceName,
		Probed: e.probed, Tracks: src.Tracks, Video: video, Synthetic: synthetic,
		Annotations:   e.settings.Ingest.Annotations,
		MetersDropped: ffmpeg.MetersDropped(chans),
	}
}

// ------------------------------------------------------- loudness compliance

// The loudness tier: one EBU R128 analyser per running destination, reading
// the same relay that destination reads and applying the same compiled routing
// graph, so what it measures is what the platform receives.
//
// It is the LAST thing reconciled and the first thing that may fail. Nothing
// downstream reads its hub, no destination waits on it, and an analyser that
// cannot start leaves a report saying so rather than taking anything off air.
// That is the whole reason it is a separate process — see meters.Args for the
// CPU tradeoff that buys.
const (
	// loudnessPublishInterval throttles the WebSocket. ebur128 prints at 10 Hz
	// and integrated loudness moves at the speed of a programme, so a push per
	// second is already faster than the number can change meaningfully.
	loudnessPublishInterval = time.Second
	loudnessSubPrefix       = "loudness:"
)

// loudnessMon is one destination's running analyser.
type loudnessMon struct {
	proc    *supervisor.Process
	hub     *relay.Hub
	port    int
	subName string
	// sig hashes everything the analyser's command line and its verdict depend
	// on, so editing an unrelated destination never cycles a healthy meter.
	sig string
}

// loudnessPlan is what one destination's analyser should look like.
type loudnessPlan struct {
	id       int64
	name     string
	hub      *relay.Hub
	compiled routing.Result
	target   meters.Target
	sig      string
}

// loudnessWanted is the analyser set this reconcile should end with.
//
// Only destinations that are actually RUNNING earn one. A destination that is
// disabled, broken or waiting on a rendition is sending nothing, and metering
// nothing would produce a confident -70 LUFS that reads like a mixing fault.
func (e *Engine) loudnessWanted(s db.Settings) map[int64]loudnessPlan {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Gated on the existing meters switch, which is the one an operator
	// already reaches for when they want the measurement processes to stop.
	if !e.loudnessTierWantedLocked(s) {
		return nil
	}
	out := make(map[int64]loudnessPlan, len(e.dests))
	for id, d := range e.dests {
		if p, ok := loudnessPlanFor(id, d); ok {
			out[id] = p
		}
	}
	return out
}

// loudnessTierWantedLocked is the gate above, factored out so that the answer
// the Meters page's switch reads back and the answer reconcile acts on cannot
// drift apart. The page used to seed that switch from a hardcoded `true`, which
// is why this needs an accessor at all.
//
// Caller holds e.mu (read or write).
func (e *Engine) loudnessTierWantedLocked(s db.Settings) bool {
	return !e.stopped && !e.loudOff && s.Meters.Enabled
}

// LoudnessMonitorEnabled reports whether the analyser tier is currently wanted:
// the operator's SetLoudnessMonitor override AND the meters switch it follows.
//
// A CONTROL rather than a warning: the Meters page cannot assert a state it has
// not been told, so it asks. Nil receiver for the same reason Loudness has one
// -- an install with no source has no engine and still renders the page.
func (e *Engine) LoudnessMonitorEnabled() bool {
	if e == nil {
		return false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.loudnessTierWantedLocked(e.settings)
}

// loudnessPlanFor derives one destination's analyser plan, or reports that this
// destination earns none.
//
// SEPARATED OUT SO THE PREDICATE CAN BE ASKED TWICE. It is evaluated once when a
// reconcile decides what it wants, and again in startLoudness under the lock that
// publishes the monitor -- because those are two different moments and the
// destination can be deleted in between. Inlining it in loudnessWanted, which is
// where it used to live, made the second check impossible to write without
// duplicating the first and letting the two drift.
//
// Pure in d, and takes no lock: both callers already hold one.
func loudnessPlanFor(id int64, d *destination) (loudnessPlan, bool) {
	if d == nil || d.proc == nil || d.hub == nil || d.err != "" || d.compiled.FilterComplex == "" {
		return loudnessPlan{}, false
	}
	t := meters.TargetFor(d.row.Profile.Loudness,
		routing.PlatformFor(string(d.row.Platform), string(d.row.Kind)))
	return loudnessPlan{
		id: id, name: d.row.Name, hub: d.hub, compiled: d.compiled, target: t,
		// d.spec already hashes the graph and the upstream, so this adds
		// only what the analyser cares about that the destination does not.
		sig: hashStrings([]string{d.spec, t.Sig()}),
	}, true
}

// reconcileLoudness starts, stops and cycles the analysers to match.
func (e *Engine) reconcileLoudness(s db.Settings) {
	// An Engine assembled field by field rather than through New has neither,
	// and there is nothing here worth panicking a reconcile over.
	if e.loudStore == nil || e.loud == nil {
		return
	}
	want := e.loudnessWanted(s)

	e.mu.Lock()
	var stop []*loudnessMon
	for id, m := range e.loud {
		if p, ok := want[id]; ok && p.sig == m.sig {
			continue
		}
		stop = append(stop, m)
		delete(e.loud, id)
	}
	e.mu.Unlock()

	for _, m := range stop {
		e.teardownLoudness(m)
	}

	ids := make([]int64, 0, len(want))
	keep := make(map[int64]bool, len(want))
	for id := range want {
		ids = append(ids, id)
		keep[id] = true
	}
	slices.Sort(ids)
	for _, id := range ids {
		e.mu.RLock()
		running := e.loud[id] != nil
		e.mu.RUnlock()
		if running {
			continue
		}
		e.startLoudness(want[id])
	}
	// Reports outlive their analyser by exactly one reconcile, which is what
	// stops a deleted destination being pushed to browsers forever.
	e.loudStore.Keep(keep)
}

func (e *Engine) startLoudness(p loudnessPlan) {
	now := time.Now()
	fail := func(err error) {
		e.loudStore.Put(meters.Failed(p.id, p.name, p.target, err.Error(), now))
		e.log.Warn("loudness monitor cannot run", "dest", p.name, "err", err)
	}

	port, err := e.alloc.Allocate()
	if err != nil {
		fail(err)
		return
	}
	subName := loudnessSubPrefix + strconv.FormatInt(p.id, 10)
	url := p.hub.Subscribe(subName, port)

	args := meters.Args(meters.Spec{
		RelayURL:      url,
		FilterComplex: p.compiled.FilterComplex,
		OutLabel:      p.compiled.OutLabel,
	})

	id, name, target := p.id, p.name, p.target
	proc := supervisor.New(e.log, supervisor.Spec{
		Name: subName, Kind: "loudness", Bin: e.tools.FFmpeg, Args: args,
		AutoRestart: true,
		StdoutHandler: func(r io.Reader) error {
			var last time.Time
			return meters.Parse(r, func(f meters.Frame) {
				rep := meters.Observe(id, name, target, f, time.Now())
				// Stored on every frame, published on a throttle: a browser
				// that connects between pushes still gets the current number.
				e.loudStore.Put(rep)
				if time.Since(last) < loudnessPublishInterval {
					return
				}
				last = time.Now()
				e.bus.Publish(events.TypeLoudness, rep)
			})
		},
		OnLog: e.onLog, OnState: e.onState, LogSink: logSink{e},
	})

	e.mu.Lock()
	// Shutdown may have run since this reconcile started; publishing under the
	// same lock Stop collects processes with is what keeps a late start from
	// becoming an orphan holding a UDP port.
	//
	// AND THE DESTINATION MAY HAVE GONE, which is the same hazard by a different
	// route and was not covered. This guard read `if e.stopped` alone, so a
	// reconcile that computed its wanted-set before a destination was deleted and
	// arrived here after it published a monitor for a destination that no longer
	// existed -- holding a relay port, subscribed to a hub about to be closed
	// under it, and receiving nothing forever. Measured: adding then deleting one
	// destination reliably left exactly one of these behind (#453), and the
	// comment above already described the failure without covering this path to
	// it.
	//
	// Re-asking loudnessPlanFor rather than testing `e.dests[p.id] != nil` is
	// deliberate: a destination that was deleted and re-created, or respecced,
	// is present under the same id but is not the thing this plan was made for.
	// The signature is what distinguishes them.
	cur, want := loudnessPlanFor(p.id, e.dests[p.id])
	if e.stopped || !want || cur.sig != p.sig {
		e.mu.Unlock()
		p.hub.Unsubscribe(subName)
		e.alloc.Release(port)
		return
	}
	e.loud[p.id] = &loudnessMon{proc: proc, hub: p.hub, port: port, subName: subName, sig: p.sig}
	e.mu.Unlock()

	e.loudStore.Put(meters.Starting(p.id, p.name, p.target, now))
	proc.Start()
	e.log.Info("loudness monitor started", "dest", p.name,
		"target", p.target.Source, "lufs", p.target.LUFS)
}

func (e *Engine) teardownLoudness(m *loudnessMon) {
	if m == nil {
		return
	}
	if m.proc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		_ = m.proc.Stop(ctx)
		cancel()
	}
	if m.subName != "" {
		hub := m.hub
		if hub == nil {
			hub = e.hub
		}
		hub.Unsubscribe(m.subName)
	}
	if m.port != 0 {
		e.alloc.Release(m.port)
	}
}

// Loudness returns the latest compliance report for every monitored
// destination, for the REST snapshot a browser needs before the first push.
//
// The reports come back AGED: a verdict nothing has re-measured for
// meters.StaleAfter is returned as unknown with a reason saying so, rather than
// as the pass it was when the analyser last spoke. This is the only read path
// a stale report can reach -- the WebSocket publishes each frame as it is
// parsed, so what it carries is fresh by construction -- and it is shared by
// GET /loudness and Engine.Status, so aging it here covers the UI, the API and
// anything else that ever asks. The clock comparison stays on the server on
// purpose: Report.At is a server timestamp, and a browser judging it against
// its own clock would call every reading stale on any machine whose time is a
// minute out. See meters.StaleAfter for what #609 cost.
//
// The nil guard is for an Engine assembled field by field rather than through
// New — which is how the tests build one, and how a status snapshot could
// otherwise panic on a code path that has nothing to do with loudness.
//
// The RECEIVER guard below is a different question from that one and both are
// needed: the store check reads a field, so it dereferences before it can
// decide anything. An install with no source has no analyser to report and no
// engine to hold one. See Engine.Status.
func (e *Engine) Loudness() []meters.Report {
	if e == nil {
		return []meters.Report{}
	}
	if e.loudStore == nil {
		return []meters.Report{}
	}
	return e.loudStore.All()
}

// SetLoudnessMonitor turns the analyser tier off or back on without touching
// the ingest meter.
//
// A stopgap until settings carry a switch of their own: the tier follows
// Meters.Enabled today, and an operator who wants per-channel ingest levels but
// not one analyser per destination has nowhere else to say so.
// SetLoudnessMonitor turns the analyser tier on or off and RECONCILES EITHER
// WAY, including when the flag already holds the value being set.
//
// It used to return early on a no-op change, which reads as an obvious
// optimisation and was the whole of #612. `loudOff` is the zero value, so the
// tier is "wanted" from the moment an engine is built -- but analysers are only
// started by reconcileLoudness, and on a fresh install the one pass that runs
// it happens while destinations are still HELD, waiting for the ingest probe.
// It correctly finds nothing to measure. Nothing reconciles the tier again once
// they start, and `SetLoudnessMonitor(true)` short-circuited because the flag
// already said on.
//
// The result was an install reporting `enabled: true` with no analyser ever
// having run: GET /loudness said so, the Meters page drew the switch on, and
// nothing measured. Only toggling OFF and then ON fixed it, because the off
// transition was the only one that reached a reconcile.
//
// So the early return goes. Setting a switch to the position it already claims
// to be in must still make reality match the claim -- the optimisation assumed
// the flag fully determines the state, and it does not. A reconcile that has
// nothing to do is cheap; an operator with no loudness measurement and a
// switch insisting otherwise is not.
func (e *Engine) SetLoudnessMonitor(enabled bool) error {
	e.mu.Lock()
	e.loudOff = !enabled
	e.mu.Unlock()
	return e.Reconcile()
}

// ------------------------------------------------------------- clip capture

// clipSubName is fixed: there is at most one capture buffer.
const clipSubName = "clips"

// reconcileClips brings the rolling capture buffer into line.
//
// It reads e.downstreamHub(), the same relay every destination reads, so a clip
// is what went to air — including whichever source the failover tier had
// selected at the time. The hub identity therefore rides in the signature: a
// buffer left subscribed to a silence tier that has closed would quietly stop
// receiving and hand out an empty clip an hour later.
func (e *Engine) reconcileClips() {
	e.mu.RLock()
	on, cfg, cur, sig, stopped := e.clipOn, e.clipCfg, e.clipCap, e.clipSig, e.stopped
	e.mu.RUnlock()

	want := ""
	if on && !stopped {
		want = hashStrings([]string{
			strconv.Itoa(cfg.WindowSeconds),
			strconv.FormatInt(cfg.MaxRingBytes, 10),
			e.sourceLabel(),
		})
	}
	if (cur != nil) == (want != "") && sig == want {
		return
	}

	if cur != nil {
		e.mu.Lock()
		old, port, hub := e.clipCap, e.clipPort, e.clipHub
		e.clipCap, e.clipPort, e.clipHub, e.clipSig = nil, 0, nil, ""
		e.mu.Unlock()
		e.teardownClips(old, port, hub)
	}
	if want == "" {
		return
	}

	port, err := e.alloc.Allocate()
	if err != nil {
		e.log.Error("clip buffer: no relay port", "err", err)
		return
	}
	hub := e.downstreamHub()
	url := hub.Subscribe(clipSubName, port)

	capt, err := clips.Open(e.log, cfg, url, func() {
		e.bus.Publish(events.TypeClips, nil)
	})
	if err != nil {
		hub.Unsubscribe(clipSubName)
		e.alloc.Release(port)
		e.log.Error("clip buffer", "err", err)
		return
	}

	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		_ = capt.Close()
		hub.Unsubscribe(clipSubName)
		e.alloc.Release(port)
		return
	}
	e.clipCap, e.clipPort, e.clipHub, e.clipSig = capt, port, hub, want
	e.mu.Unlock()

	e.log.Info("clip buffer started",
		"windowSeconds", cfg.WindowSeconds, "maxBytes", cfg.MaxRingBytes, "dir", cfg.Dir)
}

func (e *Engine) teardownClips(c *clips.Capturer, port int, hub *relay.Hub) {
	if c == nil {
		return
	}
	// The socket first: the hub must stop being told about a consumer before
	// the port goes back in the pool.
	if hub == nil {
		hub = e.hub
	}
	hub.Unsubscribe(clipSubName)
	_ = c.Close()
	if port != 0 {
		e.alloc.Release(port)
	}
	e.log.Info("clip buffer stopped")
}

// SetClipBuffer turns the rolling capture buffer on or off and sizes its
// window.
//
// Off is the default, and deliberately so: the buffer is the one feature here
// that costs memory whether or not anybody uses it, and an upgrade must not
// silently start holding a hundred megabytes of somebody's 4K feed. Passing
// zero seconds keeps the current window.
func (e *Engine) SetClipBuffer(enabled bool, windowSeconds int) error {
	e.mu.Lock()
	cfg := e.clipCfg
	if windowSeconds > 0 {
		if windowSeconds < clips.MinWindowSeconds || windowSeconds > clips.MaxWindowSeconds {
			e.mu.Unlock()
			return fmt.Errorf("clip window %ds out of range (%d-%d)",
				windowSeconds, clips.MinWindowSeconds, clips.MaxWindowSeconds)
		}
		cfg.WindowSeconds = windowSeconds
	}
	e.clipCfg = cfg.Normalized()
	e.clipOn = enabled
	e.mu.Unlock()
	return e.Reconcile()
}

// clipCapturer is the running buffer, or an error explaining why there is none.
func (e *Engine) clipCapturer() (*clips.Capturer, error) {
	e.mu.RLock()
	c, on := e.clipCap, e.clipOn
	e.mu.RUnlock()
	if c != nil {
		return c, nil
	}
	if !on {
		return nil, fmt.Errorf("the clip buffer is switched off")
	}
	return nil, fmt.Errorf("the clip buffer is not running")
}

// Clip captures the last seconds of the stream to a file.
func (e *Engine) Clip(seconds int) (clips.Clip, error) {
	c, err := e.clipCapturer()
	if err != nil {
		return clips.Clip{}, err
	}
	if seconds <= 0 {
		seconds = c.Config().WindowSeconds
	}
	return c.Capture(time.Duration(seconds) * time.Second)
}

// Clips lists the captured clips, newest first.
//
// It reads the directory rather than the running buffer, so clips survive the
// buffer being switched off — the recordings they are stored beside do.
func (e *Engine) Clips() ([]clips.Clip, error) { return clips.List(e.clipDir()) }

// ClipUsage reports what the clips directory holds against its retention.
func (e *Engine) ClipUsage() (clips.Usage, error) {
	if c, err := e.clipCapturer(); err == nil {
		return c.Usage()
	}
	e.mu.RLock()
	cfg := e.clipCfg
	e.mu.RUnlock()
	return clips.UsageOf(cfg)
}

// ClipPath resolves a clip name to a path a download handler can open,
// refusing anything that escapes the clips directory.
func (e *Engine) ClipPath(name string) (string, error) {
	return clips.Resolve(e.clipDir(), name)
}

// DeleteClip removes one captured clip.
func (e *Engine) DeleteClip(name string) error {
	if c, err := e.clipCapturer(); err == nil {
		return c.Delete(name)
	}
	path, err := clips.Resolve(e.clipDir(), name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	e.bus.Publish(events.TypeClips, nil)
	return nil
}

func (e *Engine) clipDir() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.clipCfg.Dir
}

// ClipStatus is the buffer as the dashboard renders it: whether there is any
// history to clip, and how much.
type ClipStatus struct {
	Enabled bool `json:"enabled"`
	Running bool `json:"running"`
	// Buffer is absent when nothing is running, so the card shows "off" rather
	// than a row of zeroes that look like a stalled stream.
	Buffer *clips.Stats `json:"buffer,omitempty"`
	Dir    string       `json:"dir"`
}

// ClipBuffer reports the capture buffer's state.
//
// No engine means no capturer and no configured directory, which renders as the
// "off" card rather than a row of zeroes. See Engine.Status.
func (e *Engine) ClipBuffer() ClipStatus {
	if e == nil {
		return ClipStatus{}
	}
	e.mu.RLock()
	c, on, dir := e.clipCap, e.clipOn, e.clipCfg.Dir
	e.mu.RUnlock()

	st := ClipStatus{Enabled: on, Running: c != nil, Dir: dir}
	if c != nil {
		s := c.Stats()
		st.Buffer = &s
	}
	return st
}

// ------------------------------------------------------- realtime captions

// captSubName is fixed: there is at most one captioner.
const captSubName = "captions"

// CaptionStatus is what the dashboard shows about realtime captioning.
type CaptionStatus struct {
	// Enabled is the operator's switch, Running whether a captioner is
	// actually up. They differ while the ingest has no audio, and after the
	// health guard has switched the feature back off.
	Enabled bool `json:"enabled"`
	Running bool `json:"running"`
	// Available reports whether it could be offered at all, and Unavailable
	// says why not.
	Available   bool   `json:"available"`
	Unavailable string `json:"unavailable,omitempty"`
	// Cost is the plain-language warning the UI must show next to the switch.
	// It lives in the transcription package so the promise the interface makes
	// cannot drift away from what the code actually does.
	Cost string `json:"cost"`
	// Warning is why captioning stopped, and survives the captioner.
	Warning string                `json:"warning,omitempty"`
	Track   int                   `json:"track"`
	Speaker string                `json:"speaker,omitempty"`
	Model   string                `json:"model,omitempty"`
	VTTPath string                `json:"vttPath,omitempty"`
	Stats   transcribe.LiveStats  `json:"stats"`
	Config  transcribe.LiveConfig `json:"config"`
}

// CaptionEvent is the payload of events.TypeCaption.
//
// Exactly one field is set per event: a caption line, or a status change. They
// share a type because they share a stream — a caption bar that goes quiet has
// to be able to say why, and a subscriber that took the lines but not the
// warning would sit on a frozen last sentence forever.
type CaptionEvent struct {
	Line   *transcribe.LiveCaption `json:"line,omitempty"`
	Status *CaptionStatus          `json:"status,omitempty"`
}

// SetTranscriber hands the engine the detected whisper.cpp, the directory its
// models live in, and the governor's nice wrapper.
//
// All three are optional. A nil *transcribe.Tools is the normal state of an
// install without whisper.cpp and must never be an error: captioning is simply
// not offered, and everything else is unaffected. A nil nice wrapper works too,
// but it is the difference between speech recognition yielding to the encoders
// and competing with them at equal priority, so the captioner logs it.
func (e *Engine) SetTranscriber(w *transcribe.Tools, modelsDir string, nice func(name string, args []string) (string, []string)) {
	if modelsDir == "" {
		modelsDir = transcribe.ModelsDir(e.cfg.DataDir)
	}
	e.mu.Lock()
	e.whisper, e.whisperDir, e.whisperNice = w, modelsDir, nice
	e.mu.Unlock()
}

// SetLiveCaptions turns realtime captioning on or off.
//
// Off is the default and an upgrade never turns it on: this is the one job that
// takes CPU away from a live broadcast, and nobody should discover that by
// noticing dropped frames.
func (e *Engine) SetLiveCaptions(cfg transcribe.LiveConfig) error {
	cfg = cfg.Normalized()
	if err := cfg.Validate(); err != nil {
		return err
	}

	e.mu.RLock()
	w := e.whisper
	e.mu.RUnlock()
	if cfg.Enabled && !w.Available() {
		return fmt.Errorf("%s", w.Unavailable())
	}

	e.mu.Lock()
	e.captCfg, e.captOn = cfg, cfg.Enabled
	if cfg.Enabled {
		// A fresh opt-in clears the last refusal. The operator has seen it and
		// chosen to try again, possibly with a smaller model.
		e.captWarn = ""
	}
	e.mu.Unlock()
	return e.Reconcile()
}

// LiveCaptions reports the captioner's state for the dashboard and the API.
func (e *Engine) LiveCaptions() CaptionStatus {
	e.mu.RLock()
	capt, on, cfg, warn, w := e.capt, e.captOn, e.captCfg, e.captWarn, e.whisper
	vtt := e.captVTT
	e.mu.RUnlock()

	st := CaptionStatus{
		Enabled:     on,
		Running:     capt != nil && capt.Running(),
		Available:   w.Available(),
		Unavailable: w.Unavailable(),
		Cost:        transcribe.LiveCost,
		Warning:     warn,
		Track:       cfg.Track,
		Speaker:     cfg.Speaker,
		Model:       cfg.Model,
		VTTPath:     vtt.Path(),
		Config:      cfg,
	}
	if capt != nil {
		st.Stats = capt.Stats()
		if st.Stats.Warning != "" {
			st.Warning = st.Stats.Warning
		}
	}
	return st
}

// reconcileCaptions brings the captioner into line with the switch.
//
// Like the clip buffer it reads e.downstreamHub(), so the captions describe
// what actually went to air, and the hub identity rides in the signature: a
// captioner left subscribed to a silence tier that has closed would quietly
// caption nothing forever.
//
// Nothing here can fail a reconcile. A missing model, a port that will not
// allocate and a tap that will not start are all worth a warning the operator
// can read and nothing more — a destination must never be held back because
// speech recognition could not start.
func (e *Engine) reconcileCaptions() {
	e.mu.RLock()
	on, cfg, cur, sig, stopped := e.captOn, e.captCfg, e.capt, e.captSig, e.stopped
	whisper, modelsDir, nice := e.whisper, e.whisperDir, e.whisperNice
	e.mu.RUnlock()

	choice, haveTrack := transcribe.LiveTrack(e.effectiveSource(), cfg.Track)

	want := ""
	if on && !stopped && whisper.Available() && haveTrack {
		cfg.Track, cfg.Speaker, cfg.Denoise = choice.Track, choice.Speaker, choice.Denoise
		if cfg.Language == "" {
			cfg.Language = choice.Language
		}
		want = hashStrings([]string{
			strconv.Itoa(cfg.Track), cfg.Model, string(cfg.Backend), cfg.Language,
			cfg.Window.String(), cfg.Step.String(), cfg.VTTPath, e.sourceLabel(),
		})
	}
	if (cur != nil) == (want != "") && sig == want {
		return
	}

	if cur != nil {
		e.mu.Lock()
		old, port, hub, vtt := e.capt, e.captPort, e.captHub, e.captVTT
		e.capt, e.captPort, e.captHub, e.captVTT, e.captSig = nil, 0, nil, nil, ""
		e.mu.Unlock()
		e.teardownCaptions(old, port, hub, vtt)
	}
	if want == "" {
		return
	}

	hint := transcribe.HintFromTools(e.tools)
	if cfg.Model == "" {
		cfg.Model = transcribe.LiveDefaultModel(hint).Name
	}
	if cfg.Threads == 0 {
		cfg.Threads = transcribe.LiveThreads(hint.CPUCores)
	}
	modelPath, err := transcribe.ResolveModel(modelsDir, cfg.Model)
	if err != nil {
		e.captionsFailed(fmt.Sprintf("live captions need the %s model: %v", cfg.Model, err))
		return
	}

	port, err := e.alloc.Allocate()
	if err != nil {
		e.captionsFailed(fmt.Sprintf("live captions could not get a relay port: %v", err))
		return
	}
	hub := e.downstreamHub()
	url := hub.Subscribe(captSubName, port)

	// The sidecar lands in the playout directory by default so it sits beside
	// the HLS window it describes. It is a growing WebVTT file, not a
	// conformant HLS subtitle rendition — see transcribe.LiveVTT — and the
	// playout handler serves a closed list of extensions that does not yet
	// include .vtt, so today this is for anything that reads the directory
	// directly.
	vttPath := cfg.VTTPath
	if vttPath == "" {
		vttPath = filepath.Join(e.play.Dir(), transcribe.LiveVTTName)
	}
	vtt, err := transcribe.OpenLiveVTT(vttPath)
	if err != nil {
		// Not fatal. Captions on the WebSocket are the feature; the file is a
		// convenience, and losing it must not cost the dashboard its captions.
		e.log.Warn("live captions: no sidecar file", "path", vttPath, "err", err)
		vtt = nil
	}

	capt, err := transcribe.NewLiveCaptioner(e.log, e.tools, whisper, cfg,
		transcribe.WithLiveNice(nice),
		transcribe.WithLiveEmit(func(c transcribe.LiveCaption) {
			if err := vtt.Append(c); err != nil {
				e.log.Warn("live captions: sidecar write failed", "err", err)
			}
			e.bus.Publish(events.TypeCaption, CaptionEvent{Line: &c})
		}),
		transcribe.WithLiveDegrade(e.onCaptionsDegraded),
	)
	if err != nil {
		hub.Unsubscribe(captSubName)
		e.alloc.Release(port)
		_ = vtt.Close()
		e.captionsFailed(err.Error())
		return
	}

	ctx := e.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := capt.Start(ctx, url, modelPath, transcribe.LiveWorkDir(e.cfg.DataDir)); err != nil {
		hub.Unsubscribe(captSubName)
		e.alloc.Release(port)
		_ = vtt.Close()
		e.captionsFailed(err.Error())
		return
	}

	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		e.teardownCaptions(capt, port, hub, vtt)
		return
	}
	e.capt, e.captPort, e.captHub, e.captVTT, e.captSig = capt, port, hub, vtt, want
	e.captCfg = capt.Config()
	e.mu.Unlock()

	// Warn, not Info. This is the one thing in the pipeline that takes CPU away
	// from the encoders, and an operator reading the log after a stutter should
	// find it without looking.
	e.log.Warn("live captions started: speech recognition now competes with the encoders for CPU",
		"track", cfg.Track, "speaker", cfg.Speaker, "model", cfg.Model,
		"threads", cfg.Threads, "niced", nice != nil)
	e.publishCaptionStatus()
}

// captionsFailed records a reason captioning could not start, switches the
// operator's flag back off and says so.
//
// Off rather than "keep retrying every reconcile": the failures that get here —
// no model, no port, no tap — do not fix themselves, and a captioner that
// re-attempts a doomed whisper start on every settings change is a background
// CPU cost with nothing to show for it.
func (e *Engine) captionsFailed(reason string) {
	e.mu.Lock()
	e.captOn, e.captCfg.Enabled, e.captWarn = false, false, reason
	e.mu.Unlock()
	e.log.Error("live captions could not start", "reason", reason)
	e.publishCaptionStatus()
}

// onCaptionsDegraded is the health guard firing: the machine could not caption
// and stream at the same time.
//
// The switch goes back OFF rather than the captioner being left to limp. The
// operator has to opt in again, which is the correct shape for a feature whose
// failure mode is stealing CPU from a live broadcast — and it makes the state
// machine honest, because "enabled but not running" then only ever means "no
// audio yet".
func (e *Engine) onCaptionsDegraded(reason string) {
	e.mu.Lock()
	e.captOn, e.captCfg.Enabled, e.captWarn = false, false, reason
	e.mu.Unlock()
	e.log.Error("live captions stopped to protect the stream", "reason", reason)
	e.publishCaptionStatus()
	// From a goroutine: this fires on the captioner's own goroutine and the
	// teardown reconcile waits for that goroutine to finish.
	go func() {
		if err := e.Reconcile(); err != nil {
			e.log.Error("live captions: reconcile after degrade", "err", err)
		}
	}()
}

func (e *Engine) publishCaptionStatus() {
	st := e.LiveCaptions()
	e.bus.Publish(events.TypeCaption, CaptionEvent{Status: &st})
}

func (e *Engine) teardownCaptions(c *transcribe.LiveCaptioner, port int, hub *relay.Hub, vtt *transcribe.LiveVTT) {
	if c == nil {
		_ = vtt.Close()
		return
	}
	// The subscription first, so the hub stops forwarding before the port goes
	// back in the pool and the allocator hands it to somebody else.
	if hub == nil {
		hub = e.hub
	}
	hub.Unsubscribe(captSubName)
	c.Stop()
	_ = vtt.Close()
	if port != 0 {
		e.alloc.Release(port)
	}
	e.log.Info("live captions stopped")
}

// Levels returns the most recent metering frame, or an empty one and the zero
// time when there is no engine to have measured anything. See Engine.Status.
func (e *Engine) Levels() (ffmpeg.Levels, time.Time) {
	if e == nil {
		return ffmpeg.Levels{}, time.Time{}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.levels, e.levelsAt
}

// ------------------------------------------------------- alerts & schedules

const (
	// alertSweep is how often the pipeline is judged. Close to the stats loop's
	// cadence on purpose: the thresholds are measured in tens of seconds, so
	// anything faster only costs snapshots.
	alertSweep = 2 * time.Second
	// alertDiskEvery throttles the free-space reading, which is the only part
	// of a snapshot that touches the database and the filesystem.
	alertDiskEvery = 30 * time.Second
	// alertLevelsFresh is how old a metering frame may be before its peaks stop
	// counting. A stale frame would keep reporting the last loud moment before
	// the ingest went away.
	alertLevelsFresh = 5 * time.Second
	// eventSchedule announces that a schedule acted, so a browser can refresh
	// rather than wonder why a destination it did not touch just came up.
	eventSchedule events.Type = "schedule"
)

// Alerts exposes the notifier so the API can report its counters and send a
// test message. Nil on an Engine assembled field by field, which is how the
// tests build one, and nil when there is no engine at all.
//
// Those two nils were never the same statement — the doc line above describes
// the FIELD, and reading it as a nil-receiver guarantee is how this method got
// called on a nil engine in the first place. See Engine.Status.
//
// The callers already handle a nil notifier and must keep doing so: Stats and
// Publish are nil-receiver safe, so the meta page reports empty counters, while
// the test-send route refuses with a 503 rather than reporting "sent" for a
// webhook nobody sent.
func (e *Engine) Alerts() *alerts.Notifier {
	if e == nil {
		return nil
	}
	return e.alerter
}

// Hooks exposes the dispatcher so the API can report its counters, list recent
// deliveries and send a test. Nil when no dispatcher was wired.
func (e *Engine) Hooks() *hooks.Dispatcher { return e.hooks }

// SetHooks attaches the shared dispatcher.
//
// A setter rather than a New parameter, matching SetTranscriber: engines are
// created whenever a source is added, long after main built the dispatcher, and
// a programme whose hooks silently never fire is a bug nobody reports.
func (e *Engine) SetHooks(d *hooks.Dispatcher) {
	e.mu.Lock()
	e.hooks = d
	e.mu.Unlock()
}

// LifecycleObserver is told about the same UP/DOWN edges the webhook dispatcher
// receives, so something outside this package can decide when a PLATFORM's
// broadcast goes live and when it ends.
//
// WHY AN OBSERVER AND NOT A CALL IN startDest/teardownDest, because that is the
// first question anybody reading this will have and both answers are facts
// about other people's code rather than preferences:
//
//   - startDest cannot work. YouTube refuses transition(status=live) with
//     errorStreamInactive until data is arriving at the bound ingest, and
//     proc.Start() is the LAST statement of startDest -- not one byte has left
//     the box. Succeeding would need a wait-for-ingest loop sitting between an
//     operator pressing a button and anything reaching a viewer, which is
//     exactly what multitrackDeadline's comment forbids.
//   - teardownDest cannot be trusted. It fires on a COMMAND-LINE CHANGE, not
//     only on a stop -- its own noteReload says "its command line changed, or
//     it was disabled or removed". Ending a broadcast there would mean that
//     nudging a bitrate ends the show and creates a new watch URL mid-stream,
//     which is what every destination edit does.
//
// The edge, by contrast, already distinguishes them: a torn-down-and-restarted
// destination crosses no edge at all, because the DOWN direction has a 10s
// dwell (hooks.DefaultDestinationDownAfter) and one reconcile completes well
// inside it.
type LifecycleObserver interface {
	// Observe receives one edge the observeLoop derived.
	//
	// IT MUST NOT BLOCK. This is called from observeLoop, which is the same
	// goroutine that raises every alert and publishes every webhook for this
	// programme, on a 2s tick. An implementation enqueues and returns: no HTTP,
	// no database write, no engine lock, no sleep. A full queue must DROP the
	// event rather than wait -- which is safe precisely because the event is
	// only a WAKEUP, and the durable answer is re-derived from the destination
	// row by a sweep that runs anyway.
	Observe(ev hooks.Event)
	// Wanted reports whether any destination currently needs these edges, and
	// must be cheap enough to call on every 2s tick -- a cached atomic, not a
	// query.
	//
	// It exists to keep a promise observeLoop makes in as many words: an install
	// with no alert rule and no webhook pays for two cached lookups and nothing
	// else, no status snapshot and no disk read. Wiring a coordinator that
	// always answered true would quietly repeal that for every install on
	// earth, including the ones with no lifecycle platform configured at all.
	Wanted() bool
}

// SetLifecycle attaches the broadcast-lifecycle coordinator.
//
// A setter rather than a New parameter, for SetHooks' reason: engines are
// created whenever a source is added, long after main built the coordinator,
// and a programme whose broadcasts silently never go live is a bug nobody
// reports -- they report "YouTube says starting soon", days later, about a show
// that has already been and gone.
func (e *Engine) SetLifecycle(o LifecycleObserver) {
	e.mu.Lock()
	e.lifecycle = o
	e.mu.Unlock()
}

// lifecycleWanted is the nil-safe read of the observer's own gate, in the shape
// hooks.Dispatcher.HasHooks uses: the nil check lives here rather than at the
// call site, because a guard written at the call site is the one that diverges
// from its sibling the next time somebody adds a consumer.
func (e *Engine) lifecycleWanted() bool {
	o := e.lifecycleObserver()
	return o != nil && o.Wanted()
}

// lifecycleObserver reads the field under the lock, so observeLoop never touches
// it directly. SetLifecycle can land at any moment -- the manager pushes it into
// every engine when the API server is built, and into engines created later by
// Sync -- and an unsynchronised read of an interface field is a data race the
// detector finds only when the timing happens to line up.
func (e *Engine) lifecycleObserver() LifecycleObserver {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.lifecycle
}

// scheduleActuator is how the scheduler reaches the enable/disable path.
//
// Deliberately hair-thin: a schedule writes exactly the intent a human writes
// and then asks for a reconcile, so a scheduled start and a clicked one are the
// same code and cannot drift apart.
type scheduleActuator struct{ m *Manager }

func (a scheduleActuator) SetDestinationEnabled(id int64, enabled bool) error {
	return a.m.store.SetDestinationEnabled(id, enabled)
}

// SetPlaylistEnabled flips the playlist's stored intent, exactly as the settings
// endpoint does. Read-modify-write rather than a targeted UPDATE because the
// settings are one JSON document, so there is no such thing as writing a single
// field of them.
//
// IT VALIDATES BEFORE IT WRITES, and that is the whole of this function's
// safety. The validation now lives in db.UpdateSettings rather than here, but
// the reason it has to happen is unchanged: PutSettings does not validate — it
// marshals and inserts — while handlePutSettings calls Settings.Validate first.
// So a scheduled write is the one path that could store a document the API
// layer would have refused, and on a DEFAULT INSTALL it would: the playlist
// ships disabled with no items, and "enabled with no items" is a state Validate
// rejects by name.
//
// Left unvalidated, an overnight playlist.start would store that document and
// every later PUT /settings would answer 400 for a reason the operator did not
// cause and cannot see — locking them out of every unrelated setting until
// somebody edited the database. That is the same shape as the settings lockout
// the previous sub-project had to fix, arriving by a different door.
//
// Going through db.UpdateSettings is also what stops a scheduled flip landing
// on top of a concurrent PUT /settings and discarding it wholesale: this runs
// in the engine and cannot reach the API server's own settings mutex, so the
// store is the only place the two can be serialised.
//
// The error is returned rather than swallowed so the runner leaves the
// occurrence unhandled and the run log carries the reason.
func (a scheduleActuator) SetPlaylistEnabled(enabled bool) error {
	_, err := a.m.store.UpdateSettings(func(s *db.Settings) error {
		if s.Failover.Playlist.Enabled == enabled {
			// Already there, so there is nothing to write and nothing to
			// validate. An overlapping schedule, or a restart inside a window,
			// arrives here every occurrence, and this is what makes that free
			// rather than a re-validate and a re-write of the whole document.
			//
			// It is also what keeps it from becoming an ERROR: without this
			// branch, a stored document that is invalid for some unrelated
			// reason would fail Validate on the way through, and a schedule
			// asking for a state the install is already in would report a
			// failure, stay unhandled and retry until its grace window ran out.
			// See db.ErrSettingsUnchanged.
			return db.ErrSettingsUnchanged
		}
		s.Failover.Playlist.Enabled = enabled
		return nil
	})
	var invalid db.InvalidSettingsError
	if errors.As(err, &invalid) {
		return fmt.Errorf("a scheduled playlist change would leave the settings "+
			"invalid, so nothing was written: %w", err)
	}
	return err
}

func (a scheduleActuator) ListDestinationIDs() ([]int64, error) {
	rows, err := a.m.store.ListDestinations()
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids, nil
}

func (a scheduleActuator) Reconcile() error { return a.m.Reconcile() }

// onSchedule publishes the fact that a timetable moved something. A dashboard
// that shows a destination coming up with no explanation is how an operator
// concludes the server has a mind of its own.
func (m *Manager) onSchedule(r scheduler.Result) {
	m.bus.Publish(eventSchedule, r)
}

// observeWanted reports whether a sweep is worth building.
//
// Extracted so the gate is testable without an engine. The failure it guards is
// silent: this loop used to skip everything when no ALERT rules existed, and a
// hook is a second consumer of the same snapshot, so an install with hooks and
// no alert rules would have observed nothing at all.
//
// THE THIRD BOOL IS THE SAME BUG A THIRD TIME, and it costs more than the first
// two did. The broadcast-lifecycle coordinator consumes these edges; leave it
// out of this gate and an install with no alert rules and no webhooks -- which
// is a default install -- takes the `continue` below on every sweep. No
// snapshot is built, so hookWatch never observes, so no edgeState ever
// advances, so no UP or DOWN edge is ever crossed, so Observe is never called.
// The visible result is not an error anywhere: it is every YouTube broadcast on
// that box sitting in "testing" for ever while the watch page says "starting
// soon" and the stream itself is perfectly healthy.
func observeWanted(alertRules, hookRules, lifecycleWanted bool) bool {
	return alertRules || hookRules || lifecycleWanted
}

// observeLoop samples the pipeline and hands each snapshot to BOTH watchers.
//
// They answer different questions -- "is this an incident worth waking
// somebody" against "did this transition happen" -- and so cross different
// edges at different times: a destination failing raises a hook at 10s and an
// alert at 20s. Deriving the second set from a second sampler would mean two
// Status() calls at two cadences that could disagree about what they saw.
//
// One sweep raises every alert rather than a Publish call scattered through the
// reconcile, because everything worth alerting on is a TRANSITION — "has been
// down for twenty seconds", "is out of tolerance again" — and a transition
// needs somewhere to remember the previous state. Sweeping also guarantees an
// alert is never raised while e.mu is held by the thing it is about.
func (e *Engine) observeLoop(ctx context.Context) {
	// THIS GUARD GATES ALL THREE CONSUMERS, including the two that have nothing
	// to do with alerting. Production always builds both watchers together (see
	// New), so on a real install it only ever means "this engine was never
	// started". An Engine assembled field by field -- which is how the tests
	// build one -- with a lifecycle observer and no alerter observes NOTHING,
	// and that is the existing contract rather than a new bug: a test that
	// wires a coordinator to a hand-built engine and waits for an edge will
	// wait for ever.
	if e.alerter == nil || e.alertWatch == nil {
		return
	}
	tick := time.NewTicker(alertSweep)
	defer tick.Stop()

	var (
		lastRx   uint64
		firstRx  = true
		disk     alerts.DiskState
		diskAt   time.Time
		haveDisk bool
	)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			// Liveness from bytes on the hub, not from process state: an SRT or
			// RTMP listener sits in "running" for as long as it waits for a
			// publisher, which is a different question from "is the source
			// arriving".
			rx := e.hub.RxBytes()
			live := !firstRx && rx > lastRx
			lastRx, firstRx = rx, false

			// A server with neither an alert rule nor a hook pays for two
			// cached lookups and nothing else — no status snapshot, no
			// queries, no disk read. Adding the first one starts the timers
			// from that moment, which is the only honest thing it could do.
			//
			// e.hooks may be nil; HasHooks is nil-safe, the same discipline
			// alerts.Notifier and transcribe.Tools use. Guarding at the call
			// site instead is what makes the two diverge.
			if !observeWanted(e.alerter.HasRules(), e.hooks.HasHooks(), e.lifecycleWanted()) {
				haveDisk = false
				continue
			}

			if !haveDisk || now.Sub(diskAt) >= alertDiskEvery {
				disk, haveDisk, diskAt = e.diskState(), true, now
			}
			snap := e.alertSnapshot(now, live)
			snap.Disk = disk
			for _, ev := range e.alertWatch.Observe(snap) {
				e.alerter.Publish(ev)
			}
			if e.hookWatch != nil {
				// Re-stamped every sweep: the source row is named after the
				// engine is built, and an event carrying only an id tells a
				// script which programme but not which one an operator would
				// recognise.
				e.hookWatch.SetSource(hooks.SourceRef{ID: e.sourceID, Name: e.SourceName()})
				// ONE DERIVATION, THREE CONSUMERS. The lifecycle coordinator
				// sees exactly the events the webhook dispatcher sees, from the
				// same Observe call on the same watcher over the same snapshot.
				// A second sampler would mean two Status() calls at two
				// cadences that could disagree about what they saw -- and the
				// disagreement that matters is "was this destination disabled
				// or did it crash", which is the difference between ending a
				// broadcast and leaving it alone.
				lc := e.lifecycleObserver()
				for _, ev := range e.hookWatch.Observe(snap) {
					e.hooks.Publish(ev)
					if lc != nil {
						// Contractually non-blocking; see LifecycleObserver.
						lc.Observe(ev)
					}
				}
			}
		}
	}
}

// alertSnapshot flattens the status snapshot into the shape the watcher judges.
// Nothing secret crosses this boundary: a destination contributes its name,
// platform and error, never its URL or stream key.
func (e *Engine) alertSnapshot(now time.Time, ingestLive bool) alerts.Snapshot {
	st := e.Status()
	snap := alerts.Snapshot{At: now, IngestLive: ingestLive}
	if st.Ingest != nil {
		snap.IngestConfigured = true
		snap.IngestError = st.Ingest.LastError
	}
	for _, d := range st.Destinations {
		ds := alerts.DestState{
			ID: d.ID, Name: d.Name, Enabled: d.Enabled,
			// A destination whose graph would not compile has no process at
			// all, and that is as down as a failed one.
			Running:  d.Error == "" && d.Process != nil && d.Process.State == supervisor.StateRunning,
			Platform: string(d.Platform),
			Error:    d.Error,
		}
		// FFmpeg has been reporting all three of these once a second per
		// destination since -progress was wired up, and nothing has ever read
		// them. Left at their zero values the watcher treats the speed as
		// unknown and says nothing, which is what happens for a destination
		// with no process.
		if d.Process != nil {
			ds.Speed = d.Process.Progress.Speed
			ds.DropFrames = d.Process.Progress.DropFrames
			ds.DupFrames = d.Process.Progress.DupFrames
		}
		snap.Destinations = append(snap.Destinations, ds)
	}
	if st.Failover != nil {
		snap.Failover = &alerts.FailoverState{
			Active:   string(st.Failover.Active),
			Reason:   st.Failover.Reason,
			Switches: st.Failover.Switches,
		}
	}
	for _, r := range st.Loudness {
		// Only a fail, and only from an analyser that is working: a broken
		// meter is a measurement problem, and reporting it as a loudness
		// failure would send somebody to remix a stream that is fine.
		if r.Error != "" {
			continue
		}
		snap.Loudness = append(snap.Loudness, alerts.LoudnessState{
			ID: r.DestinationID, Name: r.Destination,
			Failed: r.Verdict == meters.VerdictFail,
			Reason: r.Reason, LUFS: r.IntegratedLUFS, Target: r.Target.LUFS,
		})
	}
	if levels, at := e.Levels(); !at.IsZero() && now.Sub(at) < alertLevelsFresh {
		for t, chans := range levels.Peak {
			for c, peak := range chans {
				// One-based, matching how tracks and channels are numbered
				// everywhere the operator sees them.
				snap.Peaks = append(snap.Peaks, alerts.PeakState{
					Track: t + 1, Channel: c + 1, PeakDB: peak,
				})
			}
		}
	}
	return snap
}

// diskState reads the recordings volume. Kept separate because it is the one
// part of a snapshot that costs a query and a syscall.
func (e *Engine) diskState() alerts.DiskState {
	if e.recman == nil {
		return alerts.DiskState{}
	}
	u, err := e.recman.Usage()
	if err != nil {
		// An unreadable volume is not a full one. Reporting zero bytes free
		// would fire a critical alert every thirty seconds for a database that
		// is merely busy.
		return alerts.DiskState{}
	}
	return alerts.DiskState{
		FreeBytes: u.FreeBytes, TotalBytes: u.TotalBytes,
		Halted: u.Storage.Halted, Reason: u.Storage.Reason,
	}
}

// RestartRendition cycles one shared encode without touching anything else.
//
// Its destinations ride the gap out rather than being restarted with it: their
// input is a UDP socket on the rendition's hub, which the restart never closes,
// so FFmpeg simply sees a pause in the datagrams — the same thing it already
// survives every time the ingest reconnects.
func (e *Engine) RestartRendition(id int64) error {
	e.mu.RLock()
	r := e.rends[id]
	e.mu.RUnlock()
	if r == nil || r.proc == nil {
		return e.Reconcile()
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	r.proc.Restart(ctx)
	e.publishStatus()
	return nil
}

// RestartDestination cycles one destination without touching anything else.
func (e *Engine) RestartDestination(id int64) error {
	e.mu.RLock()
	d := e.dests[id]
	e.mu.RUnlock()
	if d == nil || d.proc == nil {
		return e.Reconcile()
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	d.proc.Restart(ctx)
	e.publishStatus()
	return nil
}

func (e *Engine) onLog(l supervisor.LogLine) {
	e.bus.Publish(events.TypeLog, l)
}

func (e *Engine) onState(s supervisor.Status) {
	e.publishStatus()
}

// publishStatus asks for a status snapshot to go out. COALESCING: a burst of
// callers produces one immediate push and at most one more per window.
//
// Status() costs three database queries plus a routing.Compile for every
// destination that is not running, and onState fires it per process transition
// -- so a reconcile starting N destinations rebuilt the whole snapshot N times,
// each one describing state that the next was about to replace.
//
// Leading edge, deliberately. The first request publishes immediately, so a
// single event still reaches the UI with no added latency; only the ones piling
// up behind it are collapsed. A trailing-edge debounce would have made every
// isolated change feel slow, which is the trade this was NOT worth making.
//
// Falls back to publishing inline when the loop is not running -- before Start,
// and in the unit tests that drive an Engine directly -- so this is never a
// reason a status goes missing.
// publishStatus sends a status snapshot to the event bus.
//
// SYNCHRONOUS, and it stays that way. A coalescing version of this lived here
// briefly: Status() costs three database queries plus a routing.Compile per
// idle destination, and onState fires it per process transition, so a reconcile
// starting N destinations rebuilt the whole snapshot N times. Collapsing a burst
// into one immediate push plus at most one more per 150ms window is a real
// saving and it was measured as one.
//
// It also caused a REGRESSION, measured with scripts/../flake-rate on ten runs a
// side: with coalescing the failover suite handed a destination a backwards
// decode timestamp at a switch in 3 runs out of 10, and with this synchronous
// version it does so in 0. A platform drops the connection on a backwards DTS,
// so that is the failover tier failing at the one thing it exists to do.
//
// Whether the delay CREATED that or merely exposed a latent race in the switch
// path is not settled -- see issue #126, which stays open for it. What is
// settled is that this cost three in ten broadcasts to save some database reads
// on a path nobody had complained about, which is not a trade worth making.
func (e *Engine) publishStatus() {
	e.bus.Publish(events.TypeStatus, e.Status())
}

// statsLoop pushes host and bitrate stats on a fixed cadence, and refreshes
// the status snapshot so uptimes tick in the UI without polling.
func (e *Engine) statsLoop(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			e.bus.Publish(events.TypeStats, map[string]any{
				"system":  e.hostSystem(),
				"bitrate": e.mon.Bitrate(),
			})
			e.publishStatus()
		}
	}
}

func hashStrings(parts []string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func clearDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// vaapiDevice resolves the render node a VAAPI rendition should use.
//
// Detection is deferred and done once: enumerating DRM nodes touches sysfs and
// opens devices, which is not work to repeat on every rendition restart, and is
// pointless on the overwhelming majority of installs that never run a VAAPI
// encoder at all. Anything other than a VAAPI encoder returns empty
// immediately, without detecting.
//
// An empty result is the safe answer, not a failure: FFmpeg then uses its own
// default node, which is exactly the behaviour every install had before this
// existed. Being wrong in the restrictive direction here -- refusing to start a
// rendition because a device string could not be resolved -- would be worse
// than the software fallback it is meant to avoid.
func (e *Engine) vaapiDevice(r *db.Rendition) string {
	if !strings.HasSuffix(string(r.Encoder), "_vaapi") {
		return ""
	}
	e.vaapiOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), gpuDetectTimeout)
		defer cancel()
		info := ffmpeg.DetectGPUs(ctx)
		e.vaapiDev = info.VAAPIDevice
		if e.vaapiDev == "" {
			e.log.Info("no VAAPI render node chosen; FFmpeg will use its default")
		} else {
			e.log.Info("VAAPI render node selected", "device", e.vaapiDev)
		}
	})
	return e.vaapiDev
}

// reconcileCount is how many times Reconcile has been entered. Test-only; see
// the field's comment for why it is a field.
func (e *Engine) reconcileCount() int64 { return e.reconciles.Load() }
