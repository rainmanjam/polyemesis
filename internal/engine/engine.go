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
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"runtime/debug"
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
	"github.com/rainmanjam/polyemesis/internal/playlistmedia"
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
	stopTimeout = 12 * time.Second

	// previewIdleDefault is how long the preview encoder outlives the last
	// playlist request when settings do not say.
	previewIdleDefault = 30 * time.Second
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

// Engine owns the whole streaming pipeline.
type Engine struct {
	// sourceID is the programme this engine owns. One engine per source: the
	// hub, the ingest, the recorder, the meters and the whole destination and
	// rendition map are all per-instance already, so running N of them is what
	// makes N programmes work rather than a rewrite of the reconciler.
	sourceID int64

	log    *slog.Logger
	cfg    config.Config
	store  *db.DB
	tools  *ffmpeg.Tools
	bus    *events.Broker
	hub    *relay.Hub
	alloc  *relay.PortAllocator
	mon    *stats.Monitor
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

	// reloadRec is the note collector for the reconcile currently in flight,
	// nil the rest of the time. Atomic because it is read from teardown paths
	// that hold e.mu.
	reloadRec  atomic.Pointer[reloadRecorder]
	lastReload atomic.Pointer[ReloadReport]
	sinkMu     sync.Mutex
	sinkCfg    db.LoggingSettings

	// vaapiOnce guards a single DRM-node enumeration, done lazily the first
	// time a VAAPI rendition actually starts.
	vaapiOnce sync.Once
	vaapiDev  string

	mu       sync.RWMutex
	ingest   *supervisor.Process
	recorder *supervisor.Process
	preview  *supervisor.Process
	meters   *supervisor.Process
	dests    map[int64]*destination
	rends    map[int64]*rendition
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
	hooks      *hooks.Dispatcher
	hookWatch  *hooks.Watcher
	alertWatch *alerts.Watcher
	// sched flips destinations' enabled flags on a timetable, through the same
	// path a human uses.
	sched *scheduler.Runner

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

	// source is the probed ingest layout. Until the ingest carries a stream,
	// this is DefaultSource() so the routing editor still has something to
	// render.
	source routing.Source
	// sourceName is the operator's label for this programme, refreshed on every
	// reconcile. Cached rather than read on demand: Status() runs per WebSocket
	// push and per telemetry tick, and a database read on that path buys
	// nothing -- a rename cannot happen without a reconcile.
	sourceName string
	probed     bool
	videoInfo  *ffmpeg.VideoStream
	levels     ffmpeg.Levels
	levelsAt   time.Time
	settings   db.Settings

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
	// spec is a hash of everything that would require a restart. Comparing it
	// is what keeps an unrelated edit from cycling a healthy stream.
	spec string
	err  string
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
func New(log *slog.Logger, cfg config.Config, store *db.DB, tools *ffmpeg.Tools, bus *events.Broker, sourceID int64, alloc *relay.PortAllocator) (*Engine, error) {
	hub, err := relay.New(log, 0)
	if err != nil {
		return nil, err
	}
	if alloc == nil {
		alloc = relay.NewPortAllocator(relayPortBase, relayPortSpan)
	}

	e := &Engine{
		sourceID:  sourceID,
		log:       log,
		cfg:       cfg,
		store:     store,
		tools:     tools,
		bus:       bus,
		hub:       hub,
		alloc:     alloc,
		dests:     map[int64]*destination{},
		rends:     map[int64]*rendition{},
		loud:      map[int64]*loudnessMon{},
		loudStore: meters.NewStore(),
		playProcs: map[string]*supervisor.Process{},
		source:    routing.DefaultSource(),
		// The clip buffer is described in full but switched OFF, so an upgrade
		// changes nothing at all about how much memory this process holds.
		// SetClipBuffer is what turns it on. See reconcileClips.
		clipCfg: clips.Config{
			Dir:           filepath.Join(cfg.RecordingsDir(), clips.Subdir),
			WindowSeconds: clips.DefaultWindowSeconds,
		}.Normalized(),
	}
	e.mon = stats.NewMonitor(hub.RxBytes)
	e.recman = recording.New(log, store, cfg.RecordingsDir(), func() {
		bus.Publish(events.TypeRecordings, nil)
	},
		recording.WithFFprobe(tools.FFprobe),
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
	e.sched = scheduler.New(log, store, scheduleActuator{e}, scheduler.WithOnResult(e.onSchedule))
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

func (p *playoutProc) Stop(ctx context.Context) {
	p.e.mu.Lock()
	// Only if it is still ours: a restarted variant registers its replacement
	// under the same name before the old one is torn down.
	if p.e.playProcs[p.name] == p.Process {
		delete(p.e.playProcs, p.name)
	}
	p.e.mu.Unlock()
	p.Process.Stop(ctx)
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
func (e *Engine) Tools() *ffmpeg.Tools { return e.tools }

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
	if e.sched != nil {
		e.wg.Add(1)
		go func() { defer e.wg.Done(); e.sched.Run(e.ctx) }()
	}

	return e.Reconcile()
}

// Stop tears every child down in dependency order and closes the relay.
func (e *Engine) Stop() {
	if e.cancel != nil {
		e.cancel()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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
		go func() { defer wg.Done(); p.Stop(ctx) }()
	}
	for _, d := range dests {
		stop(d.proc)
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
		ingest.Stop(ctx)
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
	e.reconcileRecorder(e.Settings())
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
	e.mu.Unlock()

	return settings, nil
}

// SourceID reports which programme this engine owns.
func (e *Engine) SourceID() int64 { return e.sourceID }

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
func (e *Engine) Reconcile() error {
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
	e.reconcilePreview(settings)
	e.reconcileMeters(settings)

	if err := e.reconcileOutputs(); err != nil {
		return err
	}
	// After the outputs, because both of these read the hub that the silence
	// and selector tiers decide, and reconcileOutputs is where that is settled.
	// Neither can fail the reconcile: a measurement that will not start and a
	// capture buffer that will not bind are both worth a log line and nothing
	// more, and a destination must never be held back by either.
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
	if s.Ingest.Mode == db.IngestSRT {
		e.stopIngestProcess()
		return
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
		cur.Stop(ctx)
		cancel()
	}

	proc := supervisor.New(e.log, supervisor.Spec{
		Name:        "ingest",
		Kind:        "ingest",
		Bin:         e.tools.FFmpeg,
		Args:        spec,
		AutoRestart: true,
		// The ingest listener is expected to exit whenever the streamer stops,
		// so it must come back fast rather than backing off toward 30s and
		// leaving the next session waiting.
		MinBackoff: 500 * time.Millisecond,
		MaxBackoff: 5 * time.Second,
		OnLog:      e.onLog,
		OnState:    e.onState,
		LogSink:    logSink{e},
	})

	e.mu.Lock()
	e.ingest = proc
	e.ingestSig = sig
	// A new ingest means the previous layout is stale.
	e.probed = false
	e.source = routing.DefaultSource()
	e.mu.Unlock()

	proc.Start()
	e.log.Info("ingest started", "mode", s.Ingest.Mode, "url", e.ingestPublicURL(s))
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
	cur.Stop(ctx)
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
		RTMPStreamKey: s.Ingest.RTMP.StreamKey,
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

func (e *Engine) ingestPublicURL(s db.Settings) string {
	spec := ffmpeg.IngestSpec{
		Kind:          ffmpeg.IngestKind(s.Ingest.Mode),
		SRTPort:       s.Listeners.SRTPort,
		SRTPassphrase: s.Ingest.SRT.Passphrase,
		SRTLatencyMS:  s.Ingest.SRT.LatencyMS,
		RTMPPort:      s.Listeners.RTMPPort,
		RTMPApp:       s.Ingest.RTMP.App,
		// In pull mode nobody points an encoder anywhere, so the address the
		// operator needs to see is the one we dial.
		PullURL: s.Ingest.Pull.URL,
	}
	return spec.PublicIngestURL("<server>")
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
func stemPlanFor(rec db.RecordingSettings, src routing.Source, probed bool) []recording.Stem {
	if !rec.Stems || !probed {
		return nil
	}
	return recording.PlanStems(src, rec.StemCodec)
}

func (e *Engine) reconcileRecorder(s db.Settings) {
	e.mu.RLock()
	cur := e.recorder
	// Read here rather than through e.Source(): that takes the same RLock, and
	// this function holds it again further down.
	src := e.source
	probed := e.probed
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
	plan := stemPlanFor(s.Recording, src, probed)
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
	if cur != nil && sig == want {
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

	if !running || !previewIdle(s, seen, now) {
		return
	}

	e.previewMu.Lock()
	// A request may have landed while we were taking the lock, and that client
	// is now watching an encoder we were about to kill.
	e.mu.RLock()
	seen = e.previewSeen
	running = e.preview != nil
	e.mu.RUnlock()
	stop := running && previewIdle(s, seen, now)
	if stop {
		e.stopPreviewLocked()
	}
	e.previewMu.Unlock()

	if stop {
		e.log.Info("preview idle; encoder stopped", "after", previewIdleWindow(s))
		e.publishStatus()
	}
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

	dir := e.cfg.HLSDir()
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
	url := e.hub.Subscribe("preview", port)

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
	// segments the next start is about to delete.
	clearDir(e.cfg.HLSDir())
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

// destPlan is what one destination should look like after this reconcile.
type destPlan struct {
	row      *db.Destination
	compiled routing.Result
	spec     string
	// err is a reason not to run at all — a routing graph that will not
	// compile, or an upstream rendition that is not there. Either way the
	// destination is shown as broken rather than started against nothing.
	err string
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
	counts, err := e.store.CountEnabledDestinationsByRendition()
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

	plans := e.planDestinations(destRows, wantRends, src, srcSig)

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

	e.startDestinations(plans)
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

// planDestinations works out the desired state of every enabled destination,
// including which upstream it reads and whether it can run at all.
func (e *Engine) planDestinations(rows []*db.Destination, wantRends map[int64]string, src routing.Source, silenceSig string) map[int64]destPlan {
	plans := map[int64]destPlan{}
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		p := destPlan{row: row}

		// The upstream's own signature rides in the destination's, so editing a
		// rendition restarts exactly the destinations downstream of it and
		// nothing else. A passthrough destination's upstream is the silence
		// tier when there is one, which is why silenceSig is the seed rather
		// than the empty string: switching synthesis on or off has to restart
		// every destination, not just the ones on a rendition.
		upstream := silenceSig
		if row.RenditionID != nil {
			sig, ok := wantRends[*row.RenditionID]
			if !ok {
				// The rendition was deleted between the two queries. Deleting
				// one drops its destinations back to passthrough, so the
				// reconcile that follows the delete sees a nil id and this
				// destination comes straight back.
				p.err = fmt.Sprintf("rendition %d is no longer available", *row.RenditionID)
			}
			upstream = sig
		}

		compiled, cerr := routing.Compile(row.Profile, src)
		if cerr != nil {
			p.err = cerr.Error()
		} else {
			p.compiled = compiled
			p.spec = destSpec(row, compiled, upstream)
		}
		plans[row.ID] = p
	}
	return plans
}

// stopDestinations tears down every destination that is gone, newly disabled,
// newly broken, or running with arguments that no longer match. Everything else
// is left strictly alone — that is the guarantee that renaming a destination,
// or editing a different one, never interrupts a live output.
func (e *Engine) stopDestinations(plans map[int64]destPlan) {
	e.mu.Lock()
	var toStop []*destination
	for id, d := range e.dests {
		p, wanted := plans[id]
		keep := wanted && d.proc != nil && p.err == "" && d.spec == p.spec
		if !keep {
			toStop = append(toStop, d)
			delete(e.dests, id)
		}
	}
	e.mu.Unlock()

	for _, d := range toStop {
		e.teardownDest(d)
	}
}

// startDestinations starts everything the plan wants that is not already
// running, once its rendition is up.
func (e *Engine) startDestinations(plans map[int64]destPlan) {
	ids := make([]int64, 0, len(plans))
	for id := range plans {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	// Spacing for destinations started in THIS sweep. Counted per actually-
	// started process, not per id, so a reconcile that leaves seven running and
	// starts one does not make that one wait seven slots for nothing.
	stagger := time.Duration(e.Settings().Destinations.StaggerMS) * time.Millisecond
	started := 0

	for _, id := range ids {
		p := plans[id]

		e.mu.Lock()
		if cur := e.dests[id]; cur != nil {
			// Survived the stop phase, so it is running with the right
			// arguments; refresh the row for cosmetic fields like the name.
			//
			// Replaced wholesale rather than mutated in place: Status hands out
			// these pointers and then reads their fields after dropping the
			// lock, which is only safe while a published destination never
			// changes again.
			next := *cur
			next.row = p.row
			e.dests[id] = &next
			e.mu.Unlock()
			// AFTER the unlock. SetPolicy itself is a memory write, but the
			// revival path calls Restart, which blocks for up to stopTimeout.
			// Holding e.mu across that would stall every Status() the dashboard
			// asks for and every other tier's reconcile behind it.
			e.applyDestPolicy(&next, p.row)
			continue
		}
		e.mu.Unlock()

		if p.err != "" {
			e.mu.Lock()
			e.dests[id] = &destination{row: p.row, compiled: p.compiled, err: p.err}
			e.mu.Unlock()
			e.log.Warn("destination cannot run", "dest", p.row.Name, "err", p.err)
			continue
		}

		hub, herr := e.upstreamHub(p.row)
		if herr != nil {
			e.mu.Lock()
			e.dests[id] = &destination{row: p.row, compiled: p.compiled, err: herr.Error()}
			e.mu.Unlock()
			e.log.Warn("destination has no upstream", "dest", p.row.Name, "err", herr)
			continue
		}

		if err := e.startDest(p.row, p.compiled, p.spec, hub, stagger*time.Duration(started)); err != nil {
			e.log.Error("start destination", "dest", p.row.Name, "err", err)
			e.mu.Lock()
			e.dests[id] = &destination{row: p.row, compiled: p.compiled, err: err.Error()}
			e.mu.Unlock()
			continue
		}
		started++
	}
}

// upstreamHub is the relay a destination reads: the ingest's when it is on
// passthrough, its rendition's own otherwise.
func (e *Engine) upstreamHub(row *db.Destination) (*relay.Hub, error) {
	if row.RenditionID == nil {
		if h := e.selectorHub(); h != nil {
			// The silence tier is no longer this destination's problem: the
			// selector's feed is what reads it, and a silence tier that is
			// broken leaves the destination on a quiet hub rather than off the
			// air. Holding the platform connection while nothing is arriving is
			// the whole reason this tier exists.
			return h, nil
		}
		if err := e.selectorProblem(); err != nil {
			return nil, err
		}
		if err := e.silenceProblem(); err != nil {
			return nil, err
		}
		return e.sourceHub(), nil
	}
	e.mu.RLock()
	r := e.rends[*row.RenditionID]
	e.mu.RUnlock()
	if r == nil || r.hub == nil {
		// Starting it anyway would give the user a destination that looks
		// healthy and sends nothing.
		reason := "is not running"
		if r != nil && r.err != "" {
			reason = "failed to start: " + r.err
		}
		return nil, fmt.Errorf("rendition %d %s", *row.RenditionID, reason)
	}
	return r.hub, nil
}

// destSpec hashes everything about a destination that requires a restart.
//
// upstream is its rendition's signature, empty for passthrough. Folding both it
// and the rendition id in is what makes moving a destination between tiers, or
// editing the tier it sits on, restart that destination and no other.
func destSpec(row *db.Destination, compiled routing.Result, upstream string) string {
	source := "passthrough"
	if row.RenditionID != nil {
		source = "rendition:" + strconv.FormatInt(*row.RenditionID, 10)
	}
	return hashStrings([]string{
		row.Target(), string(row.Kind), compiled.FilterComplex,
		strconv.Itoa(row.AudioBitrate), strconv.Itoa(row.Profile.SampleRate),
		source, upstream,
		// A negative delay leaves no trace in the filter string — it is carried
		// on the video side instead — so without this, changing one would be
		// saved and never applied.
		strconv.Itoa(compiled.VideoDelayMS),
		// Expert mode. Without these an edit would be saved and then do nothing
		// until some unrelated reconcile happened to restart the destination,
		// which is the worst of both worlds: the operator is told it applied
		// and the process is still running the old command line.
		row.ExtraInputArgs, row.ExtraOutputArgs,
		// Transport tuning, for exactly the same reason. Every one of these
		// changes the command line, and a setting that is stored and never
		// reaches the running process is the failure this repo keeps paying
		// for -- most recently r.Deinterlace on renditions.
		strconv.FormatBool(row.Transport.NoDurationFilesize),
		strconv.Itoa(row.Transport.MuxQueuePackets),
		strconv.Itoa(row.Transport.MuxQueueBytes),
		strconv.Itoa(row.Transport.RWTimeoutSeconds),
		// Resilience is deliberately ABSENT. It is a property of the
		// supervisor, not of the command line, and supervisor.SetPolicy now
		// carries it into a process that is already running -- see
		// applyDestPolicy. The reasoning that first put it here was right about
		// the danger (a setting stored and never reaching the process it
		// governs) and wrong about the remedy: the remedy was to deliver it,
		// not to drop the operator's connection in order to deliver it.
		// Audio encoding: both change the command line.
		row.Audio.Codec, strconv.FormatBool(row.Audio.Mono),
	})
}

// audioCodecOf maps the stored codec name onto the FFmpeg encoder name. An
// unrecognised value falls back to AAC rather than reaching the command line:
// a destination row written by a newer build must still stream, and AAC is the
// one codec every platform takes.
func audioCodecOf(stored string) string {
	if stored == db.DestAudioOpus {
		return ffmpeg.AudioCodecOpus
	}
	return ffmpeg.AudioCodecAAC
}

// secondsOr converts a settings value in seconds to a Duration, returning the
// fallback when the operator has not set one. Zero means "the supervisor's
// default", never "no delay at all" -- a zero backoff would be a spin loop
// against a platform that is refusing us.
func secondsOr(v int, fallback time.Duration) time.Duration {
	if v <= 0 {
		return fallback
	}
	return time.Duration(v) * time.Second
}

// destPolicy is the reconnect policy for one destination row.
//
// The zero value must map to the zero Policy, not to an explicit 1s/30s: that
// is what leaves supervisor.New's own defaults in place, which is what every
// destination ran on before the policy was configurable.
func destPolicy(row *db.Destination) supervisor.Policy {
	return supervisor.Policy{
		MinBackoff:  secondsOr(row.Resilience.MinBackoffSeconds, 0),
		MaxBackoff:  secondsOr(row.Resilience.MaxBackoffSeconds, 0),
		MaxRestarts: row.Resilience.GiveUpAfter,
	}
}

// applyDestPolicy carries a changed reconnect policy into a destination that is
// already running, and revives one that had given up under a stricter rule.
//
// The revival is the one place this work chooses a restart over a live apply,
// and it is chosen deliberately. Raising GiveUpAfter on a destination that has
// already exhausted the old limit and would otherwise sit in StateFailed for
// ever is exactly the "stored, reported as applied, and does nothing" failure
// this file is littered with warnings about. Lowering it is NOT retroactive: a
// destination is not executed for exits it made under the old rules.
//
// Start() cannot do the revival -- supervise returns down the give-up path
// without clearing p.running, so Start takes its idempotence early return.
// Restart() is the only door, and its Stop returns immediately because the
// supervise goroutine has already closed done.
func (e *Engine) applyDestPolicy(d *destination, row *db.Destination) {
	if d == nil || d.proc == nil {
		return
	}
	before := d.proc.Policy()
	want := destPolicy(row)
	if before == want {
		return
	}
	d.proc.SetPolicy(want)
	e.log.Info("destination reconnect policy retuned without a restart",
		"dest", row.Name, "minBackoff", want.MinBackoff, "maxBackoff", want.MaxBackoff,
		"giveUpAfter", want.MaxRestarts)
	e.noteReload("destination", row.Name, reloadLive,
		fmt.Sprintf("reconnect policy retuned to %s..%s, giving up after %d",
			want.MinBackoff, want.MaxBackoff, want.MaxRestarts))

	if d.proc.Status().State != supervisor.StateFailed || !moreForgiving(before, want) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	d.proc.Restart(ctx)
	e.log.Info("destination revived: it had given up under the previous limit",
		"dest", row.Name, "giveUpAfter", want.MaxRestarts)
	e.noteReload("destination", row.Name, reloadRestart,
		"it had given up under the previous limit and the new one is more forgiving")
}

// moreForgiving reports whether want allows more attempts than before.
//
// 0 means unlimited, so it is the MOST forgiving value rather than the least.
// Compared as a plain number it sorts exactly the wrong way round, which would
// revive a destination the operator had just told to give up sooner.
func moreForgiving(before, want supervisor.Policy) bool {
	if want.MaxRestarts == 0 {
		return before.MaxRestarts != 0
	}
	if before.MaxRestarts == 0 {
		return false
	}
	return want.MaxRestarts > before.MaxRestarts
}

// expertArgv parses a destination's hand-written arguments into an argv.
//
// A parse failure yields nothing rather than an error, and that is deliberate:
// the API validates on the way in, so anything unparseable here got in before
// the rules were what they are now. Dropping it starts the destination on its
// generated command — the stream keeps running and the editor still shows the
// operator the stored text with the reason it will not apply. Refusing to start
// the destination would be the restrictive-direction failure this repo has
// already paid for three times.
func expertArgv(log *slog.Logger, row *db.Destination, raw, field string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	argv, err := ffmpeg.SplitArgs(raw)
	if err != nil {
		log.Warn("ignoring unparseable expert arguments",
			"dest", row.Name, "field", field, "err", err)
		return nil
	}
	return argv
}

// destWritesAFile reports whether a destination's target is a path on this
// machine rather than a network endpoint, and therefore has to be confined to
// the recordings directory before FFmpeg is handed it.
//
// An audio-only destination is either an Icecast mount or a bare filename; the
// scheme is the only thing that tells them apart. Without this an audio file
// target would be written relative to the process working directory, outside
// the confinement every other file destination has.
func destWritesAFile(row *db.Destination) bool {
	switch row.Kind {
	case db.DestFile:
		return true
	case db.DestAudio:
		return !strings.Contains(row.URL, "://")
	default:
		return false
	}
}

func (e *Engine) startDest(row *db.Destination, compiled routing.Result, spec string, hub *relay.Hub, startDelay time.Duration) error {
	port, err := e.alloc.Allocate()
	if err != nil {
		return err
	}
	subName := fmt.Sprintf("dest:%d", row.ID)
	url := hub.Subscribe(subName, port)

	target := row.Target()
	writesAFile := destWritesAFile(row)
	if writesAFile {
		// File destinations are confined to the recordings directory; the
		// path never comes straight from user input.
		resolved, err := e.recman.ResolveForWrite(row.URL)
		if err != nil {
			e.hub.Unsubscribe(subName)
			e.alloc.Release(port)
			return err
		}
		target = resolved
	}

	buildArgs := func(out string) []string {
		return ffmpeg.DestinationArgs(ffmpeg.DestSpec{
			Kind:          ffmpeg.DestKind(row.Kind),
			Target:        out,
			RelayURL:      url,
			FilterComplex: compiled.FilterComplex,
			AudioOutLabel: compiled.OutLabel,
			AudioBitrate:  row.AudioBitrate,
			SampleRate:    row.Profile.SampleRate,
			CopyVideo:     true,
			// A negative routing delay pulls audio ahead of picture, which no
			// audio filter can do, so the compiler hands the amount over here
			// and the video is held back instead.
			VideoDelayMS: compiled.VideoDelayMS,
			// Expert mode. Spliced by DestinationArgs into the two positions
			// FFmpeg binds options from, which are the same two the operator
			// was shown in the confirm dialog.
			ExtraInputArgs:  expertArgv(e.log, row, row.ExtraInputArgs, "input"),
			ExtraOutputArgs: expertArgv(e.log, row, row.ExtraOutputArgs, "output"),
			// Muxer and socket tuning. Its zero value emits nothing, so a
			// destination that has not opted in produces exactly the command
			// it always did.
			// Output audio encoding. Zero value is AAC stereo.
			Audio: ffmpeg.AudioSpec{
				Codec: audioCodecOf(row.Audio.Codec),
				Mono:  row.Audio.Mono,
			},
			Transport: ffmpeg.TransportSpec{
				NoDurationFilesize: row.Transport.NoDurationFilesize,
				MuxQueuePackets:    row.Transport.MuxQueuePackets,
				MuxQueueBytes:      row.Transport.MuxQueueBytes,
				RWTimeoutSeconds:   row.Transport.RWTimeoutSeconds,
			},
		})
	}

	// Only a file destination needs a fresh argv per spawn, and only because
	// its output path cannot be reused. An RTMP or SRT target is reconnected
	// to, not recreated, so rebuilding its command line every respawn would be
	// churn with no benefit — and it would make the argv shown on the
	// monitoring page differ from the one that has been running all along.
	var nextArgs func() []string
	if writesAFile {
		nextArgs = func() []string {
			out, err := e.recman.ResolveForWrite(row.URL)
			if err != nil {
				// Keep the last known-good path rather than refusing to start:
				// the resolver only fails when the directory itself is
				// unusable, and that is already reported by the recorder's own
				// storage guard.
				e.log.Error("destination: cannot pick an output filename",
					"dest", row.Name, "err", err)
				return buildArgs(target)
			}
			return buildArgs(out)
		}
	}

	proc := supervisor.New(e.log, supervisor.Spec{
		Name:        subName,
		Kind:        "destination",
		Bin:         e.tools.FFmpeg,
		Args:        buildArgs(target),
		NextArgs:    nextArgs,
		AutoRestart: true,
		// Per-destination reconnect policy. Zero values leave the supervisor's
		// own defaults in place, which is what every destination ran on before
		// this was configurable. The same three values are re-applied without a
		// restart by applyDestPolicy when they change.
		MinBackoff:  destPolicy(row).MinBackoff,
		MaxBackoff:  destPolicy(row).MaxBackoff,
		MaxRestarts: destPolicy(row).MaxRestarts,
		// Spaced out so going live does not spawn every destination in the
		// same tick. First spawn only -- a reconnect is never delayed.
		StartDelay: startDelay,
		OnLog:      e.onLog,
		OnState:    e.onState,
		LogSink:    logSink{e},
	})

	e.mu.Lock()
	e.dests[row.ID] = &destination{
		row: row, proc: proc, port: port, subName: subName,
		compiled: compiled, hub: hub, spec: spec,
	}
	e.mu.Unlock()

	proc.Start()
	e.log.Info("destination started", "dest", row.Name, "kind", row.Kind,
		"tracks", compiled.Summary, "rendition", renditionLabel(row))
	e.noteReload("destination", row.Name, reloadRestart, "started")
	return nil
}

func (e *Engine) teardownDest(d *destination) {
	if d == nil {
		return
	}
	e.noteReload("destination", d.row.Name, reloadRestart,
		"its command line changed, or it was disabled or removed")
	if d.proc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		d.proc.Stop(ctx)
		cancel()
	}
	if d.subName != "" {
		// Its own hub, which is not always the ingest's.
		hub := d.hub
		if hub == nil {
			hub = e.hub
		}
		hub.Unsubscribe(d.subName)
	}
	if d.port != 0 {
		e.alloc.Release(d.port)
	}
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
		r.proc.Stop(ctx)
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
	// The ingest hub unless this consumer says otherwise; only the meters
	// sidecar ever reads anything else.
	hub := e.hub
	switch name {
	case "recorder":
		port, e.recorderPort = e.recorderPort, 0
		e.recorderSig = ""
	case "preview":
		port, e.previewPort = e.previewPort, 0
		e.previewSig = ""
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
		proc.Stop(ctx)
		cancel()
	}
	hub.Unsubscribe(name)
	if port != 0 {
		e.alloc.Release(port)
	}
}

// ----------------------------------------------------------------- selector
//
// The source-selector tier is a permanent relay between the ingest and
// everything downstream. Destinations, renditions, playout and the meters
// subscribe to it for their whole life; a separate FEED process decides what
// flows into it — the primary ingest, the backup ingest, or a synthesised
// slate — and a switch replaces only that feed.
//
//	ingest  -> [silence] -\
//	backup ingest --------+-> [selector] -> [rendition] -> destination
//	slate ----------------/
//
// That indirection is the whole feature. Switching a destination's own
// subscription would restart its process and drop the platform connection,
// which is the exact failure both failover and the slate exist to prevent. So
// the hub a destination reads never changes; only the bytes arriving on it do.
//
// It is OFF by default and costs nothing when off: sourceHub() answers exactly
// as it did before, and no feed process runs. Turning it on adds one `-c copy`
// remux hop, which is a few percent of a core and a few milliseconds — real,
// but not something an upgrade should spend on your behalf.
//
// TWO THINGS THAT DECIDE WHETHER THIS WORKS AT ALL:
//
// PTS CONTINUITY. Every feed normalises its own input to a timeline starting at
// zero, so without help each switch would hand the destinations a timestamp
// that jumps BACKWARDS by however long the previous feed had been running —
// and a platform answers a backwards jump by dropping the connection. Every
// feed is therefore started with -output_ts_offset set to the tier's own
// elapsed wall-clock time (SlateSpec.TimestampOffsetSeconds is the slate's
// spelling of the same flag). Because each feed publishes in real time, the
// published timeline stays within a switch's dead time of wall clock, forwards
// and monotonic across any number of switches. This is the first thing to look
// at if destinations stall on a switch rather than riding it.
//
// It is also why a feed is NOT AutoRestart: the supervisor would respawn it
// with the offset it was born with, and the second life of a feed that had been
// running an hour would publish an hour in the past. Respawn is owned by the
// sweep below, which computes a fresh offset every time.
//
// CODEC AND LAYOUT MATCH. A destination copies video (`-c:v copy`), so the
// slate has to look enough like the departed ingest that the platform's decoder
// re-initialises instead of giving up. The slate is therefore built at the
// PROBED width, height and frame rate, and the mpegts muxer repeats SPS/PPS
// in-band at every keyframe so a decoder has what it needs to re-init. What it
// cannot match is the encoder itself: the ingest's stream came from OBS, the
// slate's comes from libx264, and a platform that refuses that change will show
// a glitch or a reconnect at the switch. When the ingest was never probed there
// is no geometry to copy and the slate falls back to 1280x720 at 30, which a
// copying destination will pass through as a visible resolution change. Both of
// those are the deliberate choice: degrade visibly rather than corrupt quietly.
//
// The slate publishes ONE stereo track. A destination whose routing profile
// selects track 1 or above finds nothing on those inputs while the slate is up
// and stops producing output — no worse than the departed ingest, which
// delivered nothing on every track, but no better either. Closing that gap
// needs a track count on ffmpeg.SlateSpec, which is a change to a file this
// work does not own.

const (
	// selectorSweep is how often liveness is re-evaluated. Well under any
	// sensible grace period, so a switch lands near its deadline rather than up
	// to a whole period late.
	selectorSweep = 500 * time.Millisecond
	// feedRespawn bounds how fast a feed that will not start is retried, so a
	// slate with a broken encoder logs once a couple of seconds instead of
	// spawning in a tight loop.
	feedRespawn = 2 * time.Second
	// selectorSubName is the selector's subscription on whichever hub is
	// feeding it. Fixed: there is at most one feed.
	selectorSubName = "selector"
	// eventFailover announces a source switch. Declared here rather than in
	// internal/events because the constant is only meaningful to a system that
	// has a selector tier; the broker takes any type.
	eventFailover events.Type = "failover"
)

// sourceKind names what is feeding the selector.
type sourceKind string

const (
	sourceNone    sourceKind = ""
	sourcePrimary sourceKind = "primary"
	sourceBackup  sourceKind = "backup"
	// sourcePlaylist is a scheduled playlist feed: a real programme, but not a
	// live one. It sits between the ingests and the slate -- see candidatesFor
	// for why that is the ordering and not the other one.
	sourcePlaylist sourceKind = "playlist"
	sourceSlate    sourceKind = "slate"
)

// selector is the running tier.
type selector struct {
	// hub is the relay every downstream consumer reads, for its whole life.
	hub *relay.Hub
	// spec is deliberately constant while the tier is enabled. Anything that
	// changed it would close this hub and restart every destination on it,
	// which is precisely what the tier exists to avoid.
	spec string
	// startedAt is the tier's own clock and the origin of every feed's
	// timestamp offset.
	startedAt time.Time

	feed   *sourceFeed
	active sourceKind
	// feedAt is the last START ATTEMPT, recorded whether or not it worked, so a
	// feed that cannot start backs off instead of being retried every sweep.
	feedAt time.Time
	reason string
	// pinned is an operator's manual choice. It is honoured only while that
	// source is delivering: a pin that outlived its source would strand the
	// broadcast on a dead input, which is the opposite of what somebody
	// reaching for a manual override wants.
	pinned     sourceKind
	switchedAt time.Time
	switches   int
	err        string

	// live is per-source liveness, sampled from each hub's byte counter.
	live map[sourceKind]*liveness
}

// sourceFeed is the one process publishing into the selector's hub.
type sourceFeed struct {
	kind sourceKind
	proc *supervisor.Process
	// in is the hub this feed READS, nil for the slate, which reads nothing.
	in      *relay.Hub
	port    int
	subName string
	// upstream hashes what the feed's command line depends on, so a settings
	// change respawns the feed without disturbing anything downstream of it.
	upstream  string
	offset    float64
	startedAt time.Time
}

// backupIngest is the second listener, with a hub of its own so it can be
// receiving from its encoder long before anybody asks it to go on air.
type backupIngest struct {
	proc *supervisor.Process
	hub  *relay.Hub
	sig  string
}

// playlistTier is the file on loop, and it is shaped exactly like backupIngest
// because it answers the same question the backup does: what else could be on
// air, and is it ready before anybody asks for it.
//
// The hub of its own is the entire point of the tier. A file played into the
// PRIMARY's hub carries bytes into it, so the primary reads live, and a live
// primary is the one thing failover never switches away from -- putting a file
// on air would have silently disabled the whole feature. With its own relay the
// primary goes quiet when the encoder goes quiet, which is the truth the
// selector needs.
type playlistTier struct {
	proc *supervisor.Process
	hub  *relay.Hub
	sig  string
	// listPath is the concat list this tier's FFmpeg is reading, and the tier
	// owns exactly that file: it wrote it before spawning and removes that same
	// path, and no other, once its process has stopped.
	//
	// Held as a field rather than recomputed at teardown because a recomputed
	// name is only as good as the inputs still being what they were. The
	// signature moves whenever the list does, so a teardown that rebuilt the
	// name from the CURRENT settings would sweep the wrong file, or none, at
	// precisely the moment an operator edits a playlist that is on air.
	listPath string
}

// liveness is one candidate source's delivery record, derived from bytes on its
// hub rather than from its process state. An SRT listener sits in "running"
// for as long as it waits for a publisher, so process state answers a different
// question than the one failover has to ask.
type liveness struct {
	rx uint64
	// at is when rx last increased, zero for a source that has never delivered.
	at time.Time
	// since is when the current unbroken run of delivery began, which is what an
	// automatic return measures its stability window against.
	since time.Time
}

func (l liveness) alive(now time.Time, grace time.Duration) bool {
	return !l.at.IsZero() && now.Sub(l.at) < grace
}

func (l liveness) stableFor(now time.Time) time.Duration {
	if l.since.IsZero() {
		return 0
	}
	return now.Sub(l.since)
}

func (l *liveness) sample(rx uint64, now time.Time, grace time.Duration) {
	if rx <= l.rx {
		return
	}
	if !l.alive(now, grace) {
		l.since = now
	}
	l.rx = rx
	l.at = now
}

// wantSelector reports the tier's signature, empty when it must not run.
//
// The signature is a constant rather than a hash of the settings, and that is
// load-bearing: every consumer folds it into its own restart hash, so a
// signature that moved with the backup's port or the slate's colour would
// restart every destination on an edit that changes neither.
func wantSelector(s db.Settings) string {
	if !s.Failover.Enabled {
		return ""
	}
	return "on"
}

// upstreamSig is what a consumer of "the source" folds into its restart hash:
// the selector tier's signature when it is running, the silence tier's
// otherwise.
//
// With the selector running the silence signature drops out entirely, because a
// destination no longer reads the silence hub — the feed does. That is what
// makes an ingest gaining or losing audio a restart of one remux process rather
// than of every destination.
func upstreamSig(selSig, silenceSig string) string {
	if selSig != "" {
		return "selector:" + selSig
	}
	return silenceSig
}

// selectorHub is the tier's relay, or nil when the tier is not running.
func (e *Engine) selectorHub() *relay.Hub {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.sel == nil {
		return nil
	}
	return e.sel.hub
}

// downstreamHub is what sourceHub() would answer if it could see the selector.
//
// Every consumer inside this file names this instead of sourceHub(), so there
// is still exactly ONE decision about where "the source" is — it is spelled
// across two files only because the silence tier and the selector tier are
// stacked, and sourceHub() remains the answer for everything below the selector
// as well as for the whole pipeline when the tier is off.
func (e *Engine) downstreamHub() *relay.Hub {
	if h := e.selectorHub(); h != nil {
		return h
	}
	return e.sourceHub()
}

// sourceLabel distinguishes the hubs a consumer might be reading, for the
// restart hashes that have to notice a consumer moving between them.
func (e *Engine) sourceLabel() string {
	e.mu.RLock()
	sel := e.sel
	e.mu.RUnlock()
	if sel != nil && sel.hub != nil {
		return "selector:" + sel.spec
	}
	return e.silenceLabel()
}

// backupHub is the second listener's relay, or nil when there is none.
func (e *Engine) backupHub() *relay.Hub {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.backup == nil {
		return nil
	}
	return e.backup.hub
}

// playlistHub is the playlist tier's relay, or nil when there is none.
//
// Nil rather than a panic for the same reason backupHub answers nil: every
// caller asks BEFORE knowing whether the tier is running, and "no hub" is a
// normal answer for a feature that is off by default.
func (e *Engine) playlistHub() *relay.Hub {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.playlist == nil {
		return nil
	}
	return e.playlist.hub
}

// holdSilence freezes the silence tier's signature while the selector is
// standing in for a departed primary.
//
// wantSilence answers from the PROBE, and the probe goes blank a few seconds
// after the primary stops delivering. Read literally that would tear the
// silence tier down in the middle of a failover, change the signature every
// consumer was started with, and restart every destination — the exact failure
// the tier exists to prevent. So the last answer taken while the primary was
// the live source is held until it is the live source again.
func (e *Engine) holdSilence(fresh string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sel != nil && e.sel.hub != nil && e.sel.active != sourcePrimary {
		return e.heldSilenceSig
	}
	e.heldSilenceSig = fresh
	return fresh
}

// heldSilence is the signature the running primary feed was built against.
func (e *Engine) heldSilence() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.heldSilenceSig
}

// sourceChoice is everything the decision needs, gathered under the lock so the
// decision itself stays a pure function of a snapshot.
type sourceChoice struct {
	now     time.Time
	cur     sourceKind
	pinned  sourceKind
	primary liveness
	backup  liveness

	backupEnabled bool
	slateEnabled  bool
	// playlistRunning is whether the playlist is DELIVERING, which is the whole
	// of "would the playlist carry bytes if we switched to it".
	//
	// It is not "the tier is up". PlaylistFileProblem checks path confinement,
	// not existence, so a path safely inside the data directory that names no
	// file starts a supervised process which never opens an input -- running,
	// and permanently empty. Offering that as a candidate would rank a source
	// that cannot deliver above the slate, and the slate is the thing that
	// exists so an operator never sees nothing. So it is collapsed from the
	// playlist hub's own liveness, sampled from that hub's byte counter beside
	// the two ingests' -- see sampleSources, and the comment on liveness for why
	// process state answers a different question than failover has to ask.
	//
	// One bit, not a liveness: the decision asks nothing else of a playlist.
	// There is no automatic return TO a file and no stability window to measure,
	// so alive/not is everything, and it is collapsed by the caller under the
	// lock rather than looked up from inside the decision -- chooseSource has to
	// stay pure and cheap, because the golden table's only claim to being
	// exhaustive is that every input it branches on can be enumerated.
	playlistRunning bool
	grace           time.Duration
	autoReturn      bool
	returnStable    time.Duration
}

// candidate is one source the selector may choose, and whether it can be
// chosen right now.
//
// available is not "is the process running" — it is "would this deliver bytes
// if we switched to it". A slate is always available when enabled because it
// synthesises its own picture; an ingest is available only when it is actually
// delivering, which is the distinction the liveness type already draws and the
// reason the selector switches on delivery rather than on process state.
type candidate struct {
	kind      sourceKind
	available bool
	// rank orders the list. Lower is preferred. It is a field rather than the
	// slice position so a future caller can build the list in any order and
	// still get a stable decision.
	rank int
}

// candidatesFor turns a snapshot into the ordered list the ladder implies:
// primary, then backup, then the playlist, then slate.
//
// rank is assigned from position rather than written out, so this literal is
// the ONE place the preference order lives. Two spellings of the same ordering
// is how a reordering ends up half-applied — the list reads one way, the
// decision goes the other, and nothing fails until an outage.
func candidatesFor(c sourceChoice) []candidate {
	out := []candidate{
		{kind: sourcePrimary, available: c.primary.alive(c.now, c.grace)},
		// A backup that is delivering into a hub nobody enabled is still not a
		// place to send viewers, so the setting gates availability rather than
		// membership: the candidate stays in the list and stays unavailable.
		{kind: sourceBackup, available: c.backupEnabled && c.backup.alive(c.now, c.grace)},
		// THE PLAYLIST RANKS BELOW BOTH INGESTS, AND THAT IS A DECISION.
		//
		// A scheduled broadcast is a fallback for "nobody is streaming", not a
		// pre-emption of somebody who is. Put it above the primary and the
		// failure it buys is the one nobody forgives: a presenter live on air
		// is cut off mid-sentence because a playlist entry came due. An
		// operator who genuinely wants the playlist to win says so by pinning
		// it, and the pin path outranks this whole ladder already — so the
		// wanted behaviour costs nothing and the unwanted one cannot happen by
		// accident.
		//
		// It ranks ABOVE the slate for the mirror-image reason: both are
		// holding patterns, but one of them is programming somebody chose and
		// the other is a card saying the picture is missing.
		//
		// There is deliberately no `&& c.playlistEnabled` here to mirror the
		// backup's line above, and the asymmetry is the design rather than an
		// omission. The backup's hub is the listener's and outlives every
		// publisher, so its counter cannot tell enabled from disabled; the
		// playlist's hub belongs to the tier and dies with it, so
		// reconcilePlaylist zeroes this liveness as it tears the tier down --
		// see the comment there. That covers the untick a setting gate would
		// cover AND the three it would not: failover switched off entirely, a
		// file path that fails confinement, and a path edit that swaps one hub
		// for another. The invariant the two ends maintain between them: no
		// hub, no liveness, no candidate.
		{kind: sourcePlaylist, available: c.playlistRunning},
		// The slate has no liveness to check. It synthesises its own picture, so
		// "enabled" is the whole of "would this deliver bytes".
		{kind: sourceSlate, available: c.slateEnabled},
	}
	for i := range out {
		out[i].rank = i
	}
	return out
}

// chooseFrom is the ladder, expressed over a candidate list.
//
// Every branch below picks the best AVAILABLE candidate and then names why,
// which is the shape the ladder always had; the reasons are keyed by the kind
// that won because they are a contract. They reach an operator through
// Failover.Reason, so rewording one is a user-visible change.
//
// PRECONDITION: cands and c must describe the SAME INSTANT. This function has
// two sources of truth and reads both. Preference order and "can this deliver
// bytes right now" come from cands; everything else -- c.cur, c.pinned,
// c.autoReturn, c.returnStable, and the liveness histories behind
// c.primary/c.backup -- comes from c. Two rules read one of each in the same
// breath: the pin below asks available(c.pinned), and the flapping guard asks
// available(sourcePrimary) alongside c.primary.stableFor(c.now). Build cands
// from a snapshot fresher (or staler) than c and those rules straddle two
// different moments: the `>= returnStable` boundary would be evaluated against
// a primary the list never judged, so the selector could return to a primary
// the list calls dead, or refuse to return to one it calls alive. chooseSource
// keeps the two consistent by construction -- it derives cands from the same c
// it passes -- and that is exactly why the golden table cannot see a violation
// here. A future caller that assembles candidates itself owns this precondition.
//
// It also assumes the list is well formed: one candidate per kind, and no
// available sourceNone. best() panics on the ways that matter rather than
// deciding from a list that cannot mean what it says.
func chooseFrom(cands []candidate, c sourceChoice) (sourceKind, string) {
	ranked := slices.Clone(cands)
	slices.SortStableFunc(ranked, func(a, b candidate) int { return cmp.Compare(a.rank, b.rank) })

	// available answers about one named source, for the rules that are about a
	// particular source rather than about preference order.
	available := func(k sourceKind) bool {
		for _, cand := range ranked {
			if cand.kind == k {
				return cand.available
			}
		}
		return false
	}

	// best is the ladder itself: the first available candidate wins, and says
	// the sentence that goes with having won. fallbackKind is where the
	// selector parks when nothing at all is available — never sourceNone,
	// because handing the pipeline nothing is worse than every alternative.
	//
	// reasons is looked up with comma-ok rather than a plain index, and a miss
	// panics instead of returning "". A plain index is what this looked like
	// until a review of the candidate-list cutover: available(k) matches the
	// FIRST candidate of a kind in rank order, while best here matches the
	// FIRST AVAILABLE one, and those two agree only when the list has one
	// candidate per kind. candidatesFor always builds it that way today, but
	// nothing in the type checks that a future caller -- or a map literal
	// missing the entry for a kind Task 4 adds -- keeps it true. Get it wrong
	// and a plain index hands the operator a blank Failover.Reason on a real
	// switch, which reads as "nothing happened" when something did. A panic
	// naming the kind is a test failure at the point the list is built wrong,
	// which is the whole point of a candidate winning a reason nobody wrote.
	//
	// An available sourceNone is rejected the same way, and BEFORE the reason
	// lookup, because it is malformed for a different reason: sourceNone is the
	// absence of a source, not a source that can be on air, so a list offering
	// it as available says something that cannot be true. Returning it would be
	// worse than the blank reason above -- applySourceChoice's want ==
	// sourceNone guard would drop the decision, and a dropped decision is a
	// failover that silently did not happen. Today a "" kind happens to trip
	// the missing-reason panic below, because no branch registers a reason
	// under the empty key -- but only by accident, and a map literal that ever
	// gained a sourceNone entry would turn that accident into a silent
	// discard. This check does not read the reasons map, so no map can undo it.
	best := func(reasons map[sourceKind]string, fallbackKind sourceKind, fallbackReason string) (sourceKind, string) {
		for _, cand := range ranked {
			if cand.available {
				if cand.kind == sourceNone {
					panic("chooseFrom: the candidate list offers sourceNone as available, but sourceNone is the absence of a source and can never be put on air -- fix the list that was built")
				}
				reason, ok := reasons[cand.kind]
				if !ok {
					panic(fmt.Sprintf("chooseFrom: candidate %q won but this branch has no reason registered for it -- add one to the map", cand.kind))
				}
				return cand.kind, reason
			}
		}
		return fallbackKind, fallbackReason
	}

	// An operator's pin outranks the ladder, but only while the pinned source
	// is available: a pin that outlived its source would strand the broadcast
	// on a dead input.
	if reason, ok := pinReason(c.pinned); ok && available(c.pinned) {
		return c.pinned, reason
	}

	switch c.cur {
	case sourceBackup:
		if available(sourceBackup) {
			// The flapping guard. Manual is the default because an encoder that
			// dropped once usually drops again, and each automatic return is a
			// visible cut for every viewer.
			if available(sourcePrimary) && c.autoReturn && c.primary.stableFor(c.now) >= c.returnStable {
				return sourcePrimary, "the primary ingest has been delivering steadily again"
			}
			return sourceBackup, ""
		}
		// Manual return means "do not flap", not "never recover": with the
		// backup gone there is nothing to flap between. The backup is known
		// unavailable here, so it cannot win its own branch.
		return best(map[sourceKind]string{
			sourcePrimary:  "the backup ingest stopped delivering and the primary is back",
			sourcePlaylist: "neither ingest is delivering, so the playlist is on air",
			sourceSlate:    "neither ingest is delivering",
		}, sourcePrimary, "the backup ingest stopped delivering")

	case sourceSlate:
		// A slate is a holding pattern, never a destination. The return to a
		// real source is immediate and is NOT subject to the return mode: the
		// flap risk is already bounded by the grace period on the way out, and
		// sitting on a standby card while the show is back on air is the worse
		// failure by a wide margin. Staying put is silent; a slate that has been
		// switched off underneath us falls through to the primary.
		return best(map[sourceKind]string{
			sourcePrimary:  "the primary ingest is delivering again",
			sourceBackup:   "the backup ingest is delivering",
			sourcePlaylist: "the playlist is running",
			sourceSlate:    "",
		}, sourcePrimary, "the slate was switched off")

	case sourcePlaylist:
		// The playlist is a holding pattern too, so it leaves the same way the
		// slate does: the moment a real ingest is back, and without consulting
		// the return mode. The flap risk the return mode exists to bound is a
		// risk between two LIVE encoders; there is none here, because the
		// playlist never stops delivering and so can never hand the primary a
		// window to flap in.
		//
		// Staying put is silent, exactly as the slate branch is. Without that
		// empty string a selector already on the playlist would republish the
		// same reason on every 500ms sweep for the whole scheduled programme,
		// and an operator reading the log would find a failover storm where
		// nothing at all had moved. That is the trap a fourth kind walks into
		// by being added only to the maps of branches it can arrive in, and
		// never to a branch of its own.
		return best(map[sourceKind]string{
			sourcePrimary:  "the primary ingest is delivering again",
			sourceBackup:   "the backup ingest is delivering",
			sourcePlaylist: "",
			sourceSlate:    "the playlist stopped running",
		}, sourcePrimary, "the playlist stopped running")

	default:
		// Already on the primary means the primary winning is not news, so the
		// reason stays empty and nothing is logged or published.
		onPrimary := ""
		if c.cur != sourcePrimary {
			onPrimary = "the primary ingest is delivering"
		}
		// Nothing better exists, so stay parked on the primary rather than
		// switching to nothing: a feed that is merely waiting still holds its
		// place, and it starts carrying the stream the moment an encoder
		// arrives.
		noOther := ""
		if c.cur != sourcePrimary {
			noOther = "there is no other source to run"
		}
		return best(map[sourceKind]string{
			sourcePrimary:  onPrimary,
			sourceBackup:   "the primary ingest stopped delivering",
			sourcePlaylist: "the primary ingest stopped delivering and the playlist is running",
			sourceSlate:    "the primary ingest stopped delivering and no backup is on air",
		}, sourcePrimary, noOther)
	}
}

// pinReason is the sentence for an honoured manual override, and whether the
// kind is one an operator can pin at all. sourceNone is not: it is the absence
// of a pin, not a pin on nothing.
func pinReason(k sourceKind) (string, bool) {
	switch k {
	case sourcePrimary:
		return "an operator selected the primary ingest", true
	case sourceBackup:
		return "an operator selected the backup ingest", true
	// The pin is how an operator overrides the ranking decision candidatesFor
	// explains: the playlist loses to a live encoder on the ladder, and this is
	// the sentence that says somebody wanted it to win anyway. Still honoured
	// only while the playlist is actually running, like every other pin.
	case sourcePlaylist:
		return "an operator selected the playlist", true
	case sourceSlate:
		return "an operator selected the slate", true
	}
	return "", false
}

// chooseSource decides what should be feeding the selector, and says why.
//
// An empty reason means "no change"; a non-empty one is written to the log and
// published, because a failover nobody notices is how an operator discovers at
// the end of a broadcast that they streamed the backup all night.
func chooseSource(c sourceChoice) (sourceKind, string) {
	return chooseFrom(candidatesFor(c), c)
}

// reconcileSelector brings the tier, the backup listener and the feed into line
// with settings.
//
// Called from reconcileOutputs in the window where nothing downstream is
// reading anything: the hub it may create is the one every consumer below will
// subscribe to, and the hub it may close must not close under one.
func (e *Engine) reconcileSelector(s db.Settings, want, silenceSig string) {
	e.selMu.Lock()
	defer e.selMu.Unlock()

	e.mu.Lock()
	cur := e.sel
	e.mu.Unlock()

	// A tier whose hub failed to bind carries an empty signature, so it never
	// matches "on" and is retried on the next reconcile — and it is cleared
	// unconditionally when the feature is switched off, because a leftover
	// broken tier would go on refusing destinations that no longer need it.
	if cur != nil && cur.spec == want && want != "" {
		e.reconcileBackupIngest(s)
		e.reconcilePlaylist(s)
		// Ignored on purpose: a reconcile has no caller to fail to, and a
		// decision that could not be made has already logged itself. Holding
		// the current source is what this path wants anyway.
		_ = e.applySourceChoice(s, silenceSig, time.Now())
		return
	}

	if cur != nil {
		e.mu.Lock()
		e.sel = nil
		e.mu.Unlock()
		e.teardownFeed(cur.feed)
		if cur.hub != nil {
			_ = cur.hub.Close()
			e.log.Info("source selector stopped; destinations read the ingest directly again")
		}
	}
	if want == "" {
		e.reconcileBackupIngest(s)
		// Reached with the whole feature switched off, which is when the tier
		// most needs stopping: playlistSig is empty without Failover.Enabled, so
		// this is the call that takes the file off air with the selector.
		e.reconcilePlaylist(s)
		return
	}

	hub, err := relay.New(e.log, 0)
	if err != nil {
		// Recorded rather than returned, exactly as a rendition does: the
		// destinations downstream have to be told why they are not starting.
		e.mu.Lock()
		e.sel = &selector{spec: "", err: err.Error()}
		e.mu.Unlock()
		e.log.Error("start source selector", "err", err)
		return
	}

	now := time.Now()
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		_ = hub.Close()
		return
	}
	e.sel = &selector{
		hub: hub, spec: want, startedAt: now, switchedAt: now,
		live: map[sourceKind]*liveness{
			sourcePrimary: {}, sourceBackup: {}, sourcePlaylist: {},
		},
	}
	e.mu.Unlock()

	e.log.Info("source selector started",
		"reason", "failover is enabled, so destinations subscribe to one stable relay for their whole life",
		"relayPort", hub.Port())

	e.reconcileBackupIngest(s)
	e.reconcilePlaylist(s)
	// Ignored for the same reason as the "already running" window above.
	_ = e.applySourceChoice(s, silenceSig, now)
}

// selectorProblem is the reason a destination cannot run that the selector is
// responsible for, or nil when it is not in the way.
//
// A feed that is down is deliberately NOT a reason: a destination started
// against a silent hub holds its platform connection and starts sending the
// moment the feed comes back, whereas one that refused to start has already
// lost the broadcast.
func (e *Engine) selectorProblem() error {
	e.mu.RLock()
	sel := e.sel
	e.mu.RUnlock()
	if sel == nil || sel.hub != nil {
		return nil
	}
	if sel.err != "" {
		return fmt.Errorf("the source selector failed to start: %s", sel.err)
	}
	return fmt.Errorf("the source selector is not running")
}

// selectorLoop is the failover detector: it samples each source's byte counter
// and switches the feed when the answer changes.
func (e *Engine) selectorLoop(ctx context.Context) {
	tick := time.NewTicker(selectorSweep)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			e.sweepSelector(time.Now())
		}
	}
}

func (e *Engine) sweepSelector(now time.Time) {
	s := e.Settings()

	e.selMu.Lock()
	defer e.selMu.Unlock()
	if e.selectorHub() == nil {
		return
	}
	e.sampleSources(s, now)
	// Ignored on purpose: the ticker has no caller to fail to. A decision that
	// could not be made holds the current source and logs, and the next tick
	// tries again 500ms later.
	_ = e.applySourceChoice(s, e.heldSilence(), now)
}

// sampleSources folds each candidate hub's byte counter into its liveness.
func (e *Engine) sampleSources(s db.Settings, now time.Time) {
	// The PRIMARY's own hub, never the selector's or the silence tier's: the
	// question is whether the operator's encoder is delivering, and the selector
	// hub carries bytes whichever source is on air.
	primaryRx := e.hub.RxBytes()
	var backupRx uint64
	if h := e.backupHub(); h != nil {
		backupRx = h.RxBytes()
	}
	// The playlist is sampled from bytes for the same reason the two ingests
	// are, and the failure it guards against is specific: PlaylistFileProblem
	// checks that the operator's path is CONFINED to the data directory, not
	// that it names a file. A path that is safely inside it and names nothing
	// passes validation, playlistSig is non-empty, the tier starts, and FFmpeg
	// then fails to open the input and backs off toward five seconds. "The tier
	// is running" is true throughout, and it is the wrong question -- exactly as
	// it is for an SRT listener that sits in "running" while it waits for a
	// publisher. Ranking is only worth anything if a candidate offered is a
	// candidate that would deliver, so what is sampled is delivery.
	var playlistRx uint64
	if h := e.playlistHub(); h != nil {
		playlistRx = h.RxBytes()
	}
	grace := failoverGrace(s)

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sel == nil {
		return
	}
	e.sel.live[sourcePrimary].sample(primaryRx, now, grace)
	e.sel.live[sourceBackup].sample(backupRx, now, grace)

	// The playlist's counter is the one that goes BACKWARDS, and it has to be
	// handled rather than fed to sample(), which takes a counter that only ever
	// rises and ignores anything at or below what it has seen. Two ordinary
	// things reset it: an operator editing the file path, which makes
	// reconcilePlaylist close this hub and build a fresh one counting from zero,
	// and disabling the tier, which leaves no hub to read at all. Fed in as-is,
	// the first would leave the liveness pinned at the OLD hub's total -- a
	// playlist that has been re-pointed at a working file reported dead until it
	// out-counts a file it no longer plays -- and the second would keep offering
	// a tier that has been torn down for a whole grace window, which startFeed
	// can only answer with "the playlist source has no relay to read". Zeroing
	// first makes both read as what they are: this is a different playlist, and
	// it has delivered nothing yet.
	//
	// reconcilePlaylist now zeroes the same liveness as it tears a tier down,
	// which is what makes the correction immediate rather than one sweep late,
	// so in practice this branch finds pl.rx already zero. It stays as the
	// second line of defence for the invariant, not as its enforcement: this is
	// the only place that reads the counter, and a counter read without a
	// rollback guard is how the first version of this went wrong.
	pl := e.sel.live[sourcePlaylist]
	if playlistRx < pl.rx {
		*pl = liveness{}
	}
	pl.sample(playlistRx, now, grace)
}

func failoverGrace(s db.Settings) time.Duration {
	if s.Failover.GraceSeconds <= 0 {
		return 5 * time.Second
	}
	return time.Duration(s.Failover.GraceSeconds) * time.Second
}

// errSelectorUndecided says the selector produced no source to put on air, so
// nothing was switched. It is what a recovered decision panic looks like from
// the outside: the three background callers ignore it and hold what they have,
// and SwitchSource turns it into a failure for the operator rather than
// reporting a switch that did not happen.
var errSelectorUndecided = errors.New("the source selector could not decide which source to put on air")

// applySourceChoice decides which source should be on air and makes it so. The
// caller must hold selMu.
//
// It returns errSelectorUndecided when the decision failed and the current
// source was held instead. Nothing was switched in that case, so a caller that
// asked for a specific source must not report success. The background callers
// -- the sweep ticker and both reconcile windows -- have nobody to report to
// and deliberately ignore it: holding is already the behaviour they want, and
// making them start failing would turn a decision bug into a reconcile outage.
func (e *Engine) applySourceChoice(s db.Settings, silenceSig string, now time.Time) error {
	e.mu.Lock()
	sel := e.sel
	if sel == nil || sel.hub == nil {
		e.mu.Unlock()
		return nil
	}
	c := sourceChoice{
		now:           now,
		cur:           sel.active,
		pinned:        sel.pinned,
		primary:       *sel.live[sourcePrimary],
		backup:        *sel.live[sourceBackup],
		backupEnabled: s.Failover.Backup.Enabled,
		slateEnabled:  s.Failover.Slate.Enabled,
		// Collapsed from the playlist hub's liveness, NOT from "e.playlist !=
		// nil". A tier can be up and its hub empty forever -- see the field's
		// comment on sourceChoice -- and a candidate offered on process state
		// would be a candidate that cannot deliver. Read here under the same
		// lock as the primary's, from the sample sweepSelector took a moment
		// ago, so the decision still sees one consistent snapshot.
		playlistRunning: sel.live[sourcePlaylist].alive(now, failoverGrace(s)),
		grace:           failoverGrace(s),
		autoReturn:      s.Failover.Return == db.FailoverReturnAuto,
		returnStable:    time.Duration(s.Failover.ReturnStableSeconds) * time.Second,
	}
	e.mu.Unlock()

	want, reason, decided := e.decideSource(c)
	// want == sourceNone can ONLY happen here on a recovered panic with
	// nothing to hold: chooseFrom's own best() never returns sourceNone on a
	// real decision (its fallback is always sourcePrimary -- see the comment
	// on candidatesFor), so decideSource's recover is the only path that can
	// hand this back, and only when c.cur was itself sourceNone -- the
	// selector's first decision, before any feed has ever run, or the first
	// one after the tier restarts. There is no current source to hold, so
	// there is nothing to do, and this returns rather than calling
	// ensureFeed(sourceNone).
	//
	// ensureFeed would now refuse that kind itself -- errNoFeedShape lists the
	// kinds positively and sourceNone is not among them -- but this guard is
	// still the one that belongs here, and it is not redundant: refusing inside
	// ensureFeed would record an error on the tier describing a missing feed
	// shape, when the true story is that the decision itself failed and there
	// was nothing to hold. The two are logged apart on purpose below. Before
	// either guard existed this started a PRIMARY-shaped feed while
	// e.sel.active stayed recorded as none, because every function that builds
	// a feed treated an unrecognised kind as the primary.
	//
	// It used to return here in silence, which was the one thing worse than the
	// blank reason this whole file exists to prevent: a decision that reached
	// no feed and left no trace. The two ways of getting here are logged apart
	// on purpose, and neither repeats the panic itself -- decideSource has
	// already named the cause, and once a minute its stack -- so this line adds
	// only what that one cannot know, which is that nothing is on air.
	if want == sourceNone {
		if decided {
			// Unreachable today and a bug if it ever happens: a real decision
			// never yields sourceNone. best() panics on an available one and
			// every fallback is sourcePrimary, which is why all 3200 rows of
			// the frozen table decide primary, backup, playlist or slate and
			// none ever decides none.
			e.log.Error("selector decided on no source at all; no feed started",
				"source", e.sourceID, "cause", "the candidate list produced sourceNone from a decision that did not panic")
		} else {
			e.log.Error("selector has no current source to hold; no feed started",
				"source", e.sourceID, "cause", "the decision panicked before any feed had ever run, so there was nothing to hold")
		}
		return errSelectorUndecided
	}
	e.ensureFeed(s, silenceSig, want, reason, now)
	if !decided {
		return errSelectorUndecided
	}
	return nil
}

// selPanicRelogDefault bounds how often a PERSISTENT decision panic re-logs
// its full stack, for every Engine that leaves its own selPanicRelog field at
// the zero value -- which is every production Engine, since only a test ever
// sets that field (see its comment on the Engine struct for why the window
// lives there, per-instance, rather than in a package-level var). The
// underlying cause (a map entry missing a reason) does not change between
// sweeps, so the tenth identical stack trace in five seconds tells an
// operator nothing the first one didn't -- it only spends CPU walking the
// stack and log volume repeating it, on the same 500ms ticker that is also
// the thing hammering the panic.
//
// It is a window since the last STACK WAS LOGGED, not since the last panic --
// see decideSource. Measured the other way it would never elapse, and "bounds
// how often" would quietly mean "logs it once".
const selPanicRelogDefault = time.Minute

// decideSource is chooseSource with a recover around it, and it is the ONE
// place that recover needs to live: chooseSource has exactly one caller
// (here), and this is in turn called from every production path that can
// reach the selector's decision -- both windows of reconcileSelector, the
// operator's SwitchSource, and the sweep's ticker. Recovering here, rather
// than separately in each of those four, is what keeps a fix from covering
// three of them and leaving the fourth fatal.
//
// It is placed at the DECISION, not around applySourceChoice's ensureFeed
// call that follows it. applySourceChoice is the function that performs a
// switch -- teardownFeed, then startFeed, then only after both succeed does
// it update e.sel.feed/active -- so recovering across that whole sequence
// could catch a panic mid-switch and leave e.sel's bookkeeping pointing at a
// feed that teardownFeed already stopped: a half-switched state that is worse
// than the panic it was hiding. chooseSource runs entirely before any of that
// teardown or start begins, so a panic here can only ever mean "the decision
// itself is broken", never "the switch got halfway done". Recovering at
// exactly this boundary makes surviving it safe BY CONSTRUCTION rather than
// by care taken in ensureFeed.
//
// On a recovered panic this holds the current source with no reason AND
// reports decided=false, which is the least action available: not "switch to
// whatever best() almost
// picked" (that candidate had no explanation, and this file exists precisely
// so an unexplained switch is never shown to an operator as one), and not
// "crash" either. The invariant chooseFrom's best() leans on -- one candidate
// per kind -- is enforced by candidatesFor's convention, not by the compiler,
// so a change that breaks it (Task 4 adds a fourth candidate kind) reaches
// this on every reconcile a settings change triggers, not only on a tick.
//
// "Hold the current source" only means something when there is one. On the
// selector's first decision -- c.cur is sourceNone, no feed has ever run --
// there is nothing to hold, and the caller must not treat sourceNone as a
// destination: this was considered, not missed, and applySourceChoice is
// where it is handled, by skipping ensureFeed entirely rather than starting
// a feed the bookkeeping cannot describe. See the comment there.
//
// decided is how the recovery leaves the building. Holding the current source
// is the right BEHAVIOUR for the three callers that have nobody to report to
// -- the ticker and both reconcile windows just carry on -- but it is a lie to
// the one that does: SwitchSource would otherwise return nil to an operator
// whose source was never put on air, with the log saying it was and the pin
// LATCHED, so every later sweep re-panics on the same input forever. A
// substituted value cannot be told apart from a real one; a separate flag can.
//
// Deliberately NOT inside chooseFrom, chooseSource or best: those are called
// directly by selector_candidates_test.go and selector_golden_test.go, and a
// recover placed there would swallow the panic those tests exist to observe.
func (e *Engine) decideSource(c sourceChoice) (kind sourceKind, reason string, decided bool) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		msg := fmt.Sprint(r)
		window := e.selPanicRelog
		if window == 0 {
			window = selPanicRelogDefault
		}
		// The window is measured from the last STACK, not from the last panic.
		// Refreshing selPanicAt on every panic would restart the clock twice a
		// second, time.Since would never reach the window, and the full
		// stack would be logged once EVER rather than once a minute -- so an
		// operator opening the logs an hour into an incident would find only
		// stackless lines and a first stack that had long since rotated out.
		fresh := msg != e.selPanicMsg || time.Since(e.selPanicAt) > window
		if fresh {
			e.selPanicMsg, e.selPanicAt = msg, time.Now()
			e.log.Error("selector decision panicked; holding the current source",
				"source", e.sourceID, "panic", msg, "stack", string(debug.Stack()))
		} else {
			e.log.Error("selector decision panicked again; holding the current source",
				"source", e.sourceID, "panic", msg)
		}
		kind, reason, decided = c.cur, "", false
	}()
	// choose is chooseSource unless a test has substituted decideFn -- see
	// its comment on the Engine struct. Indirecting through a local rather
	// than branching inline keeps this identical to calling chooseSource
	// directly when decideFn is nil, which is every production call.
	choose := chooseSource
	if e.decideFn != nil {
		choose = e.decideFn
	}
	kind, reason = choose(c)
	return kind, reason, true
}

// holdForUnfeedableKind records a decision the feed layer cannot carry out and
// leaves everything else exactly as it was.
//
// It logs once per distinct cause rather than on every 500ms sweep. The cause
// cannot change between sweeps -- it is a missing case in the source, not a
// condition that clears -- so the hundredth identical line tells an operator
// nothing the first did, while the sweep producing it is the same one already
// hammering the fault. sel.err is the dedupe key AND the operator-visible
// record, which is what keeps the quiet sweeps from being silent ones: the
// tier goes on reporting the fault through the API for as long as it lasts.
func (e *Engine) holdForUnfeedableKind(want sourceKind, err error) {
	e.mu.Lock()
	repeat := e.sel != nil && e.sel.err == err.Error()
	if e.sel != nil {
		e.sel.err = err.Error()
	}
	e.mu.Unlock()
	if repeat {
		return
	}
	e.log.Error("the selector chose a source no feed can run; holding the current feed",
		"source", e.sourceID, "want", string(want), "err", err)
}

// ensureFeed starts, replaces or leaves the feed alone. The caller must hold
// selMu.
func (e *Engine) ensureFeed(s db.Settings, silenceSig string, want sourceKind, reason string, now time.Time) {
	// The boundary for the three functions below, and the reason none of their
	// loud failures reaches a production crash: a kind the feed layer cannot
	// build is refused HERE, before anything is torn down. The running feed
	// keeps running, active and reason are left alone, and the tier records why
	// -- which is the same "hold what you have and say so" the recovered
	// decision panic settles on, for the same reason. Starting nothing is
	// better than starting the primary under another name.
	if err := errNoFeedShape(want); err != nil {
		e.holdForUnfeedableKind(want, err)
		return
	}
	upstream := e.feedUpstreamSig(s, want, silenceSig)

	e.mu.Lock()
	sel := e.sel
	if sel == nil || sel.hub == nil {
		e.mu.Unlock()
		return
	}
	cur, active, lastAt := sel.feed, sel.active, sel.feedAt
	e.mu.Unlock()

	switch {
	case cur != nil && cur.kind == want && cur.upstream == upstream:
		if feedRunning(cur) {
			return
		}
		if now.Sub(lastAt) < feedRespawn {
			return
		}
	case cur == nil && !lastAt.IsZero() && now.Sub(lastAt) < feedRespawn:
		// A start that failed backs off rather than being retried on every
		// sweep, which is what keeps a slate with an unopenable encoder from
		// spawning twice a second forever.
		return
	}
	respawn := active == want

	e.teardownFeed(cur)
	feed := e.startFeed(s, want, upstream, silenceSig, now)

	e.mu.Lock()
	if e.sel != nil {
		e.sel.feed = feed
		e.sel.active = want
		e.sel.feedAt = now
		if feed != nil {
			e.sel.err = ""
		}
		if !respawn {
			e.sel.reason = reason
			e.sel.switchedAt = now
			e.sel.switches++
		}
	}
	e.mu.Unlock()

	if respawn {
		// Not a switch, but never silent either: a feed that keeps dying is the
		// difference between a broadcast that is on air and one that only looks
		// like it.
		e.log.Warn("source feed restarted", "source", string(want))
		e.publishStatus()
		return
	}
	if reason == "" {
		return
	}
	// Three ways to notice, because a silent failover is the failure this
	// feature is judged on: a log line, the status snapshot, and an event.
	if want == sourcePrimary {
		e.log.Info("source switched", "to", string(want), "reason", reason)
	} else {
		e.log.Warn("source switched", "to", string(want), "reason", reason)
	}
	e.bus.Publish(eventFailover, e.Failover())
	e.publishStatus()
}

// feedRunning reports whether the feed's process is still up. A feed is not
// AutoRestart, so a process that has exited stays exited until the sweep
// rebuilds it with a current timestamp offset.
func feedRunning(f *sourceFeed) bool {
	if f == nil || f.proc == nil {
		return false
	}
	switch f.proc.Status().State {
	case supervisor.StateStopped, supervisor.StateFailed:
		return false
	}
	return true
}

// errNoFeedShape names a source kind that the feed layer does not know how to
// run, and it exists because "does not know how to run it" used to be spelled
// as silence.
//
// THREE functions decide what a feed actually is -- feedUpstreamSig hashes what
// its command line depends on, startFeed builds that command line, and
// downstreamFeedInput picks the hub it reads -- and until this commit ALL THREE
// treated an unrecognised kind as the primary. A kind added to the ladder and
// missed in any one of them therefore produced a running process reading the
// PRIMARY's hub while sel.active recorded the new kind and Failover.Reason told
// the operator that new source was on air. Bookkeeping and process disagreeing,
// with no error, no panic and no test failure -- and the selector's panic
// recovery never fires, because nothing panicked.
//
// This is the same class of defect best() already refuses in chooseFrom (a
// winning candidate with no reason registered), caught the same way and for the
// same reason: a kind that reaches a place nobody taught about it must fail
// where it is noticed, not produce a plausible-looking wrong answer.
//
// The kinds are listed positively. A future kind is then a case that is
// VISIBLY absent here rather than one silently absorbed by a default.
func errNoFeedShape(kind sourceKind) error {
	switch kind {
	case sourcePrimary, sourceBackup, sourceSlate, sourcePlaylist:
		return nil
	}
	// The three sites are named in the message rather than in a comment, because
	// the reader of this line is somebody who has just added a FIFTH kind and is
	// looking at a failure they did not expect. Teaching two of the three and
	// missing the last is the mistake this whole guard exists for, and it is the
	// one a message that merely said "unknown source" would let them repeat.
	return fmt.Errorf("no feed knows how to run source %q: feedUpstreamSig, startFeed and "+
		"downstreamFeedInput each need a case for it before the selector is ever allowed "+
		"to offer it as a candidate", kind)
}

// feedUpstreamSig hashes what one feed's command line depends on, so a settings
// change respawns the feed and disturbs nothing downstream of it.
//
// It panics on a kind with no feed shape rather than hashing one. It cannot
// return an error -- its result is a hash folded into a respawn decision, and
// there is no value it could return that means "refuse" -- so the loud failure
// is the only honest one available here. ensureFeed rejects such a kind before
// this is ever reached, which is what keeps the panic off every production
// path; see the guard there.
func (e *Engine) feedUpstreamSig(s db.Settings, kind sourceKind, silenceSig string) string {
	if err := errNoFeedShape(kind); err != nil {
		panic("feedUpstreamSig: " + err.Error())
	}
	switch kind {
	case sourceBackup:
		return hashStrings([]string{"backup", backupIngestSig(s)})
	case sourcePlaylist:
		// playlistSig, exactly as the backup folds in backupIngestSig: the feed
		// here is only a copy hop out of the playlist's hub, but that hub is
		// CLOSED and rebuilt whenever reconcilePlaylist sees the signature move
		// -- an operator editing the file path is the ordinary case -- and a
		// copy hop left pointing at the old relay would spin on a port nothing
		// publishes to. Folding the same signature in is what makes the feed
		// respawn onto the new hub in the same sweep.
		return hashStrings([]string{"playlist", playlistSig(s)})
	case sourceSlate:
		e.mu.RLock()
		v := e.videoInfo
		e.mu.RUnlock()
		sl := s.Failover.Slate
		parts := []string{
			"slate", sl.ImagePath, sl.Color, strconv.Itoa(sl.VideoKbps),
			string(sl.Encoder), sl.Preset,
		}
		if v != nil {
			parts = append(parts, strconv.Itoa(v.Width), strconv.Itoa(v.Height),
				strconv.FormatFloat(v.FrameRate, 'g', -1, 64))
		}
		return hashStrings(parts)
	default:
		// sourcePrimary, and nothing else: errNoFeedShape above has already
		// turned every other kind away. Left as a default rather than written
		// as `case sourcePrimary` so the compiler still sees a total function,
		// but the set it stands for is now one kind wide instead of open.
		return primaryFeedSig(silenceSig)
	}
}

// primaryFeedSig is the primary feed's upstream signature. The silence tier is
// between the ingest and this feed, so the feed has to be rebuilt onto the
// tier's hub when one appears and back off it when it goes.
func primaryFeedSig(silenceSig string) string {
	return hashStrings([]string{"primary", silenceSig})
}

// detachFeedForSilence stops the primary feed when the silence tier under it is
// about to be replaced.
//
// reconcileSilence closes that tier's hub, and the feed is the only thing still
// subscribed to it. Stopped here, it is rebuilt onto the new hub by
// reconcileSelector a few lines later, and the destinations ride out the same
// pause in datagrams they already survive on every rendition restart.
func (e *Engine) detachFeedForSilence(silenceSig string) {
	e.selMu.Lock()
	defer e.selMu.Unlock()

	want := primaryFeedSig(silenceSig)
	e.mu.Lock()
	var feed *sourceFeed
	if e.sel != nil && e.sel.feed != nil &&
		e.sel.feed.kind == sourcePrimary && e.sel.feed.upstream != want {
		feed, e.sel.feed = e.sel.feed, nil
		// Cleared, or the failed-start backoff would read this deliberate
		// teardown as a feed that cannot start and leave the tier unfed for a
		// couple of seconds.
		e.sel.feedAt = time.Time{}
	}
	e.mu.Unlock()
	e.teardownFeed(feed)
}

// startFeed spawns the process that publishes one source into the selector's
// hub. The caller must hold selMu.
func (e *Engine) startFeed(s db.Settings, kind sourceKind, upstream, silenceSig string, now time.Time) *sourceFeed {
	fail := func(err error) *sourceFeed {
		e.mu.Lock()
		if e.sel != nil {
			e.sel.err = err.Error()
		}
		e.mu.Unlock()
		e.log.Error("start source feed", "source", string(kind), "err", err)
		return nil
	}

	e.mu.RLock()
	sel := e.sel
	e.mu.RUnlock()
	if sel == nil || sel.hub == nil {
		return nil
	}
	out := sel.hub.InputURL()
	// The tier's own elapsed time. See the PTS note at the top of this section:
	// this single number is what keeps the published timeline monotonic across
	// every switch and every respawn.
	offset := now.Sub(sel.startedAt).Seconds()
	if offset < 0 {
		offset = 0
	}

	feed := &sourceFeed{kind: kind, upstream: upstream, offset: offset, startedAt: now}
	var args []string

	// Switched on the kind exhaustively rather than "slate, or else the primary
	// shape". The else was the mechanism: an unrecognised kind fell into the
	// relay branch, downstreamFeedInput handed it the primary's hub, and a
	// process started that read the primary while calling itself something
	// else. This function can report a failure -- fail() records it on the tier
	// and logs it -- so an unfeedable kind is returned as an error rather than
	// as a panic.
	switch kind {
	case sourceSlate:
		spec, encFallback := e.slateSpec(s, out, offset)
		if encFallback != "" {
			e.log.Warn("slate encoder unusable; falling back to software",
				"encoder", string(s.Failover.Slate.Encoder), "reason", encFallback)
		}
		args = ffmpeg.SlateArgs(spec)
	case sourcePrimary, sourceBackup, sourcePlaylist:
		// The playlist joins the two ingests here rather than getting a branch
		// of its own, and that is the whole shape of the tier: it already runs
		// its own FFmpeg against the file and publishes into its own hub, so
		// what the selector needs is the identical copy hop the backup gets --
		// subscribe to a relay, republish into the selector's hub with the
		// tier's timestamp offset. Re-reading the file here instead would put a
		// second decoder on the same input and reset its timeline on every
		// switch, which is the thing the offset exists to prevent.
		in := e.downstreamFeedInput(kind)
		if in == nil {
			return fail(fmt.Errorf("the %s source has no relay to read", kind))
		}
		port, err := e.alloc.Allocate()
		if err != nil {
			return fail(err)
		}
		feed.in, feed.port, feed.subName = in, port, selectorSubName
		args = relayFeedArgs(in.Subscribe(selectorSubName, port), out, offset)
	default:
		// A fifth kind lands here, and lands here VISIBLY: nothing is started
		// and nothing is recorded as active, because a feed that cannot be built
		// must not leave a process behind that pretends it was.
		return fail(errNoFeedShape(kind))
	}

	feed.proc = supervisor.New(e.log, supervisor.Spec{
		Name: "source:" + string(kind), Kind: "source", Bin: e.tools.FFmpeg, Args: args,
		// Deliberately not AutoRestart: a respawn has to be rebuilt with a
		// current timestamp offset, so the sweep owns it.
		AutoRestart: false, OnLog: e.onLog, OnState: e.onState, LogSink: logSink{e},
	})

	e.mu.Lock()
	// Shutdown may have run since this started; publishing under the same lock
	// Stop collects processes with is what keeps a late start from becoming an
	// orphan holding a UDP socket.
	if e.stopped {
		e.mu.Unlock()
		e.teardownFeed(feed)
		return nil
	}
	e.mu.Unlock()

	feed.proc.Start()
	return feed
}

// downstreamFeedInput is the hub one copy-hop feed reads.
//
// This is the function that actually made a mislabelled feed primary-shaped: it
// was `if kind == sourceBackup` and everything else fell through to
// sourceHub(). A kind nobody taught it about got the primary's bytes and no
// complaint. It is now a closed switch, and startFeed calls it only for the two
// kinds that HAVE a hub, so the panic below is a "this cannot happen" marker
// rather than a path -- the same division of labour as chooseFrom's best(),
// which panics while decideSource holds the recovery.
func (e *Engine) downstreamFeedInput(kind sourceKind) *relay.Hub {
	switch kind {
	case sourceBackup:
		return e.backupHub()
	case sourcePlaylist:
		// The playlist's OWN hub, and naming it here is the point of the tier
		// having one. Handed the primary's hub instead -- which is what the old
		// fall-through did -- a file on air would have carried bytes onto the
		// primary's relay, the primary would have read live, and failover never
		// switches away from a live primary. The feature would have disabled
		// itself the first time it was used, silently.
		return e.playlistHub()
	case sourcePrimary:
		// sourceHub(), not e.hub: with a video-only primary the silence tier is
		// between the two, and feeding the selector from the raw ingest would
		// publish a stream with no audio track at all.
		return e.sourceHub()
	}
	panic(fmt.Sprintf("downstreamFeedInput: source %q has no hub to read -- only the primary, "+
		"the backup and the playlist do, and startFeed must not ask about any other kind", kind))
}

func (e *Engine) teardownFeed(f *sourceFeed) {
	if f == nil {
		return
	}
	if f.proc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		f.proc.Stop(ctx)
		cancel()
	}
	if f.subName != "" && f.in != nil {
		f.in.Unsubscribe(f.subName)
	}
	if f.port != 0 {
		e.alloc.Release(f.port)
	}
}

// relayFeedArgs builds the copy hop that carries one ingest into the selector.
//
// `-map 0 -c copy`, exactly like the ingest itself: the selector must never
// become a second place video is degraded or a track is quietly dropped. The
// one thing it adds is -output_ts_offset, which is what makes a switch a
// forward step on a shared timeline instead of a jump into the past.
//
// This is the only FFmpeg command line built outside internal/ffmpeg. It
// belongs there beside IngestArgs, where it would be table-tested with the
// rest; it is written out here because nothing in that package builds a
// relay-to-relay copy yet.
func relayFeedArgs(inURL, outURL string, offsetSeconds float64) []string {
	return []string{
		"-hide_banner", "-nostdin", "-loglevel", "warning",
		"-nostats", "-progress", "pipe:1",
		"-fflags", "+genpts",
		"-thread_queue_size", "1024",
		"-i", ffmpeg.RelayInputURL(inURL),
		"-map", "0",
		"-c", "copy",
		"-output_ts_offset", strconv.FormatFloat(offsetSeconds, 'f', 3, 64),
		"-f", "mpegts",
		"-flush_packets", "1",
		ffmpeg.RelayOutputURL(outURL),
	}
}

// slateSpec builds the standby source, and reports why a configured encoder was
// not used when it was not.
func (e *Engine) slateSpec(s db.Settings, out string, offset float64) (ffmpeg.SlateSpec, string) {
	sl := s.Failover.Slate

	e.mu.RLock()
	v := e.videoInfo
	e.mu.RUnlock()

	spec := ffmpeg.SlateSpec{
		OutRelayURL:            out,
		Color:                  sl.Color,
		VideoKbps:              sl.VideoKbps,
		Preset:                 sl.Preset,
		TimestampOffsetSeconds: offset,
	}
	// Geometry from the probe, never from a form: matching the departed ingest
	// is what gives a `-c:v copy` destination a chance of riding the change, and
	// a hand-typed 1080p over a 720p camera would be exactly the silent
	// corruption this must not cause.
	if v != nil {
		spec.Width, spec.Height, spec.FPS = v.Width, v.Height, v.FrameRate
	}

	var fallback string
	if sl.Encoder != "" {
		if err := renditionEncoderProblem(e.tools, sl.Encoder); err != nil {
			// Fails OPEN, and this is the one place in the pipeline where that
			// matters most: a standby source exists to start when everything
			// else has already failed, so an encoder we cannot vouch for costs
			// a fallback to software, never a refusal to build a command.
			fallback = err.Error()
		} else {
			// The device is left to SlateArgs' own default, exactly as a
			// rendition leaves it: one place decides which render node VAAPI
			// opens, and it is not this one.
			spec.Encoder = string(sl.Encoder)
		}
	}

	if p := strings.TrimSpace(sl.ImagePath); p != "" {
		if err := sl.SlateImageProblem(); err != nil {
			e.log.Warn("ignoring slate image; painting a flat colour instead",
				"path", p, "err", err)
		} else {
			// Confined to the data directory, resolved here rather than stored
			// absolute, exactly as a file:// pull source is.
			spec.ImagePath = filepath.Join(e.cfg.DataDir, filepath.FromSlash(p))
		}
	}
	return spec, fallback
}

// ----------------------------------------------------------- backup listener

// backupIngestSig hashes everything the second listener's command depends on.
func backupIngestSig(s db.Settings) string {
	b := s.Failover.Backup
	if !s.Failover.Enabled || !b.Enabled {
		return ""
	}
	return hashStrings([]string{
		string(b.Mode),
		b.SRT.Passphrase, strconv.Itoa(b.SRT.LatencyMS),
		b.RTMP.App, b.RTMP.StreamKey,
		// The listener ports are install-wide now, but they still belong in
		// this hash: changing one changes the command the backup runs.
		strconv.Itoa(s.Listeners.SRTPort), strconv.Itoa(s.Listeners.RTMPPort),
		b.Pull.URL, strconv.Itoa(b.Pull.ReconnectDelayMaxSeconds), b.Pull.RTSPTransport,
	})
}

// reconcileBackupIngest starts, stops or restarts the second listener. The
// caller must hold selMu.
func (e *Engine) reconcileBackupIngest(s db.Settings) {
	want := backupIngestSig(s)

	e.mu.Lock()
	cur := e.backup
	e.mu.Unlock()
	if cur != nil && cur.sig == want {
		return
	}

	if cur != nil {
		// The feed reads this hub, so it goes first — a feed left running
		// across the teardown would spin on a relay that has gone away.
		e.mu.Lock()
		var feed *sourceFeed
		if e.sel != nil && e.sel.feed != nil && e.sel.feed.kind == sourceBackup {
			feed, e.sel.feed = e.sel.feed, nil
			// See detachFeedForSilence: a deliberate teardown must not be
			// mistaken for a start that failed.
			e.sel.feedAt = time.Time{}
		}
		e.backup = nil
		e.mu.Unlock()
		e.teardownFeed(feed)
		e.teardownBackup(cur)
	}
	if want == "" {
		return
	}

	hub, err := relay.New(e.log, 0)
	if err != nil {
		e.log.Error("backup ingest: no relay", "err", err)
		return
	}
	b := s.Failover.Backup
	if b.Mode == db.IngestSRT {
		// Same reasoning as the primary: the Go listener already holds the
		// socket, and the backup is reached on it by publishing to
		// `<token>.backup`. All this tier needs is the hub to receive into,
		// which was created above.
		e.mu.Lock()
		e.backup = &backupIngest{hub: hub, sig: want}
		e.mu.Unlock()
		e.log.Info("backup ingest ready", "addressedBy", "<token>.backup",
			"relayPort", hub.Port())
		return
	}
	spec := ffmpeg.IngestSpec{
		Kind:                  ffmpeg.IngestKind(b.Mode),
		SRTPort:               s.Listeners.SRTPort,
		SRTPassphrase:         b.SRT.Passphrase,
		SRTLatencyMS:          b.SRT.LatencyMS,
		RTMPPort:              s.Listeners.RTMPPort,
		RTMPApp:               b.RTMP.App,
		RTMPStreamKey:         b.RTMP.StreamKey,
		PullURL:               b.Pull.URL,
		PullDataDir:           e.cfg.DataDir,
		PullReconnectDelayMax: b.Pull.ReconnectDelayMaxSeconds,
		PullRTSPTransport:     b.Pull.RTSPTransport,
		RelayURL:              hub.InputURL(),
	}

	proc := supervisor.New(e.log, supervisor.Spec{
		Name: "backup-ingest", Kind: "ingest", Bin: e.tools.FFmpeg,
		Args:        ffmpeg.IngestArgs(spec),
		AutoRestart: true,
		// Same reasoning as the primary listener: it exits whenever its
		// streamer stops, and the backup of all things must be waiting again
		// immediately rather than backing off toward half a minute.
		MinBackoff: 500 * time.Millisecond,
		MaxBackoff: 5 * time.Second,
		OnLog:      e.onLog, OnState: e.onState, LogSink: logSink{e},
	})

	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		_ = hub.Close()
		return
	}
	e.backup = &backupIngest{proc: proc, hub: hub, sig: want}
	e.mu.Unlock()

	proc.Start()
	e.log.Info("backup ingest started", "mode", b.Mode, "url", spec.PublicIngestURL("<server>"))
}

func (e *Engine) teardownBackup(b *backupIngest) {
	if b == nil {
		return
	}
	if b.proc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		b.proc.Stop(ctx)
		cancel()
	}
	if b.hub != nil {
		_ = b.hub.Close()
	}
}

// ------------------------------------------------------------ playlist tier

// playlistItemUpload is item i's Upload, trimmed -- the ONE place engine.go
// reads it, so playlistSig's hash and reconcilePlaylist's resolution cannot
// disagree about what an item names.
//
// THE TRIM ITSELF IS db.PlaylistUploadName'S, not a copy of it. This function
// exists for the bounds check and for being the single engine-side accessor;
// the whitespace rule lives with the type, where internal/api can reach it too.
// It used to carry its own strings.TrimSpace, and a second one appeared in
// internal/api the moment the settings handler started reading items -- which
// is the same three-way drift, starting again, that this helper was created to
// end. See db.PlaylistUploadName for the failure.
func playlistItemUpload(items []db.PlaylistItem, i int) string {
	if i < 0 || i >= len(items) {
		return ""
	}
	return db.PlaylistUploadName(items[i].Upload)
}

// playlistItemsReady reports whether every item could actually go on air:
// every one has a derivative this profile produced.
//
// NORMALISATION IS ASYNCHRONOUS, so "an item names this upload" and "this item
// can be played" are different states, and there is nothing else on disk that
// tells them apart. An operator adds an item the moment their file finishes
// uploading; the transcode that makes it playable is a queued job that runs
// when the governor lets it, which on a machine that is busy carrying a live
// stream is not immediately.
//
// THE FAILURE THIS PREVENTS: the playlist ranks ABOVE the slate. Start the tier
// for a list whose first item is still transcoding and the selector is offered
// a candidate that cannot play, in preference to the slate -- and the slate is
// the one thing that exists so that an operator never sees nothing. Holding the
// slate for another few seconds is the cheap outcome; handing the broadcast to
// a file that is not there yet is not.
//
// IT ASKS ONE QUESTION: is there a non-empty derivative this profile produced?
// It no longer asks whether the upload survives.
//
// B1 required the upload too, because the argv NAMED it: a deleted upload was a
// tier respawn-looping on a missing file with the process reported healthy the
// whole time. B2's argv names the concat list, every entry of which is a
// derivative, so the original is never opened and that reason expired with it.
// Keeping the stat would take a PLAYING programme off air because a source file
// was tidied away -- punishing the operator for something the broadcast does not
// depend on. A missing upload is a configuration problem the readiness endpoint
// reports; it is not a reason to go to black. Validation governs what may be
// SAVED, readiness governs what may go to AIR, and this is where they stop
// swapping jobs.
//
// Confinement is not lost with the upload stat, it moves to where the path is
// actually built: playlistmedia.DerivativePath reduces the name to its base
// before joining, so no item name can name a file outside the derivative
// directory -- which is the property -c copy's `-safe 0` list depends on, and
// stronger than the uploads-directory check it replaces. An item that escapes
// is refused earlier anyway: playlistSig hashes EMPTY when PlaylistFileProblem
// fails, so an unusable list never reaches this function at all.
//
// This is NOT a second notion of availability. Sub-project A settled that a
// candidate is available when its hub is delivering bytes, and chooseSource
// sees one plain boolean for that; a parallel "ready" input would give the
// selector two ways to be unavailable and make the golden table's claim to
// exhaustiveness false. This decides whether the hub gets FED at all: an
// unready playlist starts no tier, so playlistRunning stays false through the
// existing byte-counter path and the slate wins by the ranking already there.
//
// An empty list is vacuously ready and never reaches here anyway: playlistSig
// is empty for an enabled playlist with no items, so reconcilePlaylist has
// already returned.
//
// It takes the items rather than reading e.settings because reconcilePlaylist
// must gate the playlist it is ABOUT TO START -- the settings it was handed --
// and not whatever the engine last stored. In production those are the same
// value; Reconcile assigns e.settings before reconcileOutputs runs. Anywhere
// else they are not, and a gate that consulted the wrong one would refuse or
// admit a playlist other than the one being started.
func (e *Engine) playlistItemsReady(items []db.PlaylistItem) bool {
	for i := range items {
		// playlistItemUpload is the ONE trim point shared with playlistSig, so
		// readiness cannot disagree with the hash about what an item names.
		upload := playlistItemUpload(items, i)
		// The normalised copy, which is what says the transcode has finished AND
		// is the exact file reconcilePlaylist puts in the concat list below.
		// os.Stat, never a Resolve: Resolve is a shape check that never touches
		// the disk, and the question here is existence.
		//
		// NON-EMPTY, not merely present. A zero-length file under the finished
		// name is a transcode that died between create and first write, and
		// nothing on disk distinguishes it from a finished one except its size.
		// Admit it and the concat list names an empty file, FFmpeg exits at once
		// and the tier respawn-loops while the process reports healthy -- the
		// same shape as B1's deleted-upload loop, and it would do it in
		// preference to the slate.
		//
		// The other three readers of this exact path already treat zero-length
		// as absent: api.playlistItemStatus, api.enqueuePlaylistNormalisation
		// and RunNormalise's already-normalised skip. This one decides what goes
		// to AIR, so a disagreement here is the one that costs a broadcast --
		// and it would show as an amber UI over a black output, with nothing
		// reconciling the two.
		fi, err := os.Stat(playlistmedia.DerivativePath(e.cfg.DataDir, upload))
		if err != nil || fi.Size() == 0 {
			return false
		}
	}
	return true
}

// playlistSig hashes everything the playlist's command depends on, and is empty
// when the tier must not run.
//
// An unusable list hashes empty rather than being started and left to fail:
// PlaylistFileProblem is the same confinement a file:// pull source and the
// slate's still are held to, and an item that fails it is operator input
// trying to name something other than a stored upload, not a file to hand
// FFmpeg anyway.
func playlistSig(s db.Settings) string {
	p := s.Failover.Playlist
	if !s.Failover.Enabled || !p.Enabled {
		return ""
	}
	if p.PlaylistFileProblem() != nil {
		return ""
	}
	// Every item's name is part of the hash, and in order, so re-sequencing
	// the list (once sequencing exists) respawns exactly as editing one entry
	// does now.
	parts := make([]string, 0, len(p.Items)+1)
	parts = append(parts, "playlist")
	for i := range p.Items {
		parts = append(parts, playlistItemUpload(p.Items, i))
	}
	return hashStrings(parts)
}

// playlistFeedArgs builds the loop that publishes the WHOLE list into the
// playlist's own hub. It is the backup's command with a concat list where the
// socket was: `-map 0 -c copy`, so a programme that was encoded once is not
// re-encoded here.
//
// -f concat over every item's derivative, looped as a whole rather than per
// item, so the list plays in order and then starts again from the top.
//
// THE LIST NAMES DERIVATIVES, which is the point of B1: every entry shares
// codec, timebase, geometry and channel layout by construction, so `-c copy`
// across a seam is a copy and not a codec change.
//
// -safe 0 because the list holds absolute paths. They are paths this process
// built through playlistmedia.DerivativePath, from a name uploads.SafeName had
// already sanitised and DerivativePath then reduces to its base -- never
// operator text reaching FFmpeg as written, which is the whole reason items
// reference uploads rather than paths.
//
// ALWAYS CONCAT, EVEN FOR ONE ITEM. A single-file special case would mean two
// argv shapes, two sets of seam behaviour, and a branch that is wrong in a way
// nobody notices until the one-item playlist is the one on air.
//
// NO `duration` DIRECTIVES ARE EMITTED, and that is a measured result rather
// than a preference: three real derivatives were concatenated, looped past two
// full wraps and probed over 1068 packets with and without the per-entry
// directives. See internal/playlistmedia/concat_behaviour_test.go.
//
// The packet streams are identical ONLY WHEN THE DIRECTIVE IS EXACT. An earlier
// version of this comment said "byte-identical either way" without that clause,
// and the measurement does not support it: at three decimals the directive is
// 333 microseconds short of the file and every packet after the first item
// shifts. ffmpeg.ConcatList renders `%.3f` because ConcatEntry.DurationMS is
// MILLISECONDS, so the only directives this product could emit are the
// inaccurate kind -- which is an argument for leaving the field zero, not
// against it. The field survives for a profile that drifts far enough to need
// it, and whoever turns it on needs sub-millisecond precision first.
//
// The two input flags are the whole difference from every other feed, and
// neither is optional. -stream_loop -1 is what makes a file that ends look like
// a source that does not, so the tier is still delivering an hour later instead
// of exiting once and leaving the selector with a candidate that vanished.
// Without -re FFmpeg reads a file as fast as the disk allows: an hour of
// programme arrives at the relay in seconds, the hub's consumers are handed a
// timeline racing away from wall clock, and what an operator sees is a playlist
// that "played" and disappeared. The same pair, for the same reason, is what
// pullFile emits for a file:// ingest.
//
// The "file:" prefix pins the LIST to the file protocol, as B1 pinned the
// upload path and as pullFile and pullSource pin theirs.
//
// WHAT IT ACTUALLY PROTECTS IS NARROWER THAN IT LOOKS, and the boundary was
// measured against FFmpeg 8.1.2 rather than reasoned about. FFmpeg infers a
// protocol from the characters before the first ":" ONLY while no "/" has
// appeared, so:
//
//   - "2026:01/data/list.txt" -- a RELATIVE data directory whose first segment
//     carries a colon -- fails with "Protocol not found" unprefixed, and opens
//     with the prefix. This is the case the prefix buys.
//   - "/mnt/2026:01/data/list.txt" is fine either way. An ABSOLUTE path can
//     never be misread, because the leading "/" ends protocol detection before
//     the colon is reached.
//   - "data/a:b/list.txt" is fine either way, for the same reason.
//
// So the widely-repeated worry -- an operator's data directory containing a
// colon -- is NOT a failure for any absolute DataDir, which is the ordinary
// case. The prefix is kept because it makes the guarantee unconditional instead
// of resting on that argument, it costs one string concatenation, and it keeps
// every file input in this package spelled the same way. It is not load bearing
// for a deployment that configures an absolute data directory.
//
// THE FILE'S CODEC PARAMETERS MUST MATCH THE INGEST'S, AND NOTHING CHECKS.
// `-c copy` here and a copy hop in startFeed mean the file's codec, resolution,
// frame rate and pixel format reach every destination exactly as they were
// encoded. A destination that is also copying passes them straight to the
// platform, so switching to a file that differs is a mid-stream codec change --
// and platforms answer that by dropping the connection, which is the one thing
// this whole tier exists to prevent. The slate re-encodes to the ingest's
// PROBED geometry for precisely this reason; the playlist cannot, because
// re-encoding a programme that was already encoded once is a cost an operator
// did not ask for. scripts/acceptance-failover.sh builds its filler clip to
// match the publisher deliberately, and says so.
//
// It is not validated here because validation would mean probing the operator's
// file at settings-save time and comparing it against an ingest that may not be
// connected yet -- a feature with its own failure modes (an unprobeable file, a
// geometry that changes when the encoder reconnects), not a check this function
// can make from an argv. Until that exists the constraint is documented where
// an operator meets it, in docs/SCHEDULED-BROADCAST.md.
func playlistFeedArgs(listPath, outURL string) []string {
	return []string{
		"-hide_banner", "-nostdin", "-loglevel", "warning",
		"-nostats", "-progress", "pipe:1",
		"-stream_loop", "-1",
		"-re",
		// Each entry's own timestamps restart at every seam and at every loop
		// boundary; genpts gives the relay a monotonic base without touching the
		// payload.
		"-fflags", "+genpts",
		"-f", "concat", "-safe", "0", "-i", "file:" + listPath,
		"-map", "0",
		"-c", "copy",
		"-f", "mpegts",
		"-flush_packets", "1",
		ffmpeg.RelayOutputURL(outURL),
	}
}

// reconcilePlaylist starts, stops or restarts the playlist tier. The caller
// must hold selMu.
//
// Signature-compared rather than restarted unconditionally, exactly as the
// backup listener is: a respawn is visible downstream -- the hub goes quiet
// while FFmpeg reopens the file and the loop restarts from the top -- so a
// settings save that touches the recorder or a destination must not cost the
// playlist a gap. The no-op is the common case, not the optimisation.
func (e *Engine) reconcilePlaylist(s db.Settings) {
	want := playlistSig(s)

	e.mu.Lock()
	cur := e.playlist
	e.mu.Unlock()
	if cur != nil && cur.sig == want {
		return
	}

	// READINESS IS EVALUATED BEFORE ANYTHING IS TORN DOWN, and the order is the
	// whole of this check. It used to sit after the teardown block below, so an
	// operator who appended one item to a playlist that was ON AIR moved the
	// signature, lost the running tier to the teardown, and then had the gate
	// refuse to bring it back because the new item had not been normalised yet.
	// That is dead air, bought for an item B1 would not have played anyway --
	// it still plays item 0 only -- and it lasted until some unrelated event
	// happened to reconcile again. Refusing HERE leaves the running tier
	// exactly as it was, still recorded under the OLD signature, so the next
	// reconcile after the transcode lands still sees a mismatch and respawns
	// onto the new list. Nothing is latched.
	//
	// Only when want is non-empty. An empty signature means the playlist must
	// STOP -- the operator disabled it, or the items no longer pass
	// PlaylistFileProblem -- and a stop must never be held up by a readiness
	// question about a list that is not going on air.
	if want != "" && !e.playlistItemsReady(s.Failover.Playlist.Items) {
		e.log.Info("playlist not started; not every item has been normalised yet",
			"items", s.Failover.Playlist.Items,
			"alreadyRunning", cur != nil,
			"reason", "the playlist ranks above the slate, so a tier started for an item "+
				"that cannot play would displace the source that exists so an operator "+
				"never sees nothing; a finished normalisation job reconciles again")
		return
	}

	if cur != nil {
		// The feed reads this hub, so it goes first -- a feed left running
		// across the teardown would spin on a relay that has gone away. This
		// is now a live hazard rather than a precaution: startFeed builds a
		// playlist copy hop out of exactly the hub being closed two lines below.
		e.mu.Lock()
		var feed *sourceFeed
		if e.sel != nil && e.sel.feed != nil && e.sel.feed.kind == sourcePlaylist {
			feed, e.sel.feed = e.sel.feed, nil
			// See detachFeedForSilence: a deliberate teardown must not be
			// mistaken for a start that failed.
			e.sel.feedAt = time.Time{}
		}
		// A TIER THAT HAS BEEN TORN DOWN MUST NOT READ AS DELIVERING, and this
		// is the line that makes that true at the instant it stops being a
		// place to send viewers rather than up to a sweep later.
		//
		// The liveness this zeroes is the ONLY thing candidatesFor consults
		// about the playlist. Leave it standing and the very next decision --
		// applySourceChoice, which reconcileSelector calls immediately after
		// this function with no sample in between -- reads a counter describing
		// a hub that has just been closed, holds the playlist, and asks
		// startFeed for a relay that no longer exists. What an operator does to
		// reach that is not exotic: they untick the playlist while it is on
		// air. The result was roughly two seconds of dead air on every
		// destination, because the sweep that would have corrected it arrives
		// after ensureFeed's failed-start backoff has already begun.
		//
		// Zeroed HERE rather than gated in candidatesFor on
		// Failover.Playlist.Enabled, which was the other candidate fix and is
		// the shape the backup's own line has. That gate would answer the
		// untick and nothing else: playlistSig is also empty when the whole
		// failover feature goes off and when the file path fails confinement,
		// and it changes for a path EDIT, which closes this hub and builds a
		// fresh one counting from zero. In every one of those the setting still
		// reads enabled while the hub is gone or new, so a gate on the setting
		// would leave the same stale-liveness hole open under a different
		// cause. One assignment at the single place the tier can go away covers
		// all four, and it keeps chooseSource's inputs describing DELIVERY
		// rather than mixing in a process fact -- which is what the comment on
		// sourceChoice.playlistRunning promises a reader.
		if e.sel != nil {
			if pl := e.sel.live[sourcePlaylist]; pl != nil {
				*pl = liveness{}
			}
		}
		e.playlist = nil
		e.mu.Unlock()
		e.teardownFeed(feed)
		e.teardownPlaylist(cur)
	}
	if want == "" {
		if s.Failover.Enabled && s.Failover.Playlist.Enabled {
			// Only reachable through settings that never passed validation --
			// db.Settings.Validate rejects an item that fails PlaylistFileProblem
			// -- so it is said out loud rather than left as a tier that quietly
			// never starts.
			e.log.Warn("playlist not started; its items are unusable",
				"items", s.Failover.Playlist.Items,
				"err", s.Failover.Playlist.PlaylistFileProblem())
		}
		return
	}

	// READINESS GATED THE START ABOVE, before the teardown, and that is the
	// whole of it -- there is no second availability input anywhere below. A
	// playlist that is not ready starts no tier, so it has no hub, so
	// playlistRunning stays false through the byte counter sampleSources
	// already reads, and the slate wins on the ranking that is already there.
	//
	// Re-read on every reconcile rather than watched: an unready reconcile
	// records no tier, so the next one re-evaluates for free. What FIRES that
	// next reconcile when the last normalisation job finishes is
	// cmd/polyemesis/postprod.go's queue change hook, which calls
	// Manager.Reconcile for a completed playlistmedia.KindNormalise. Without
	// it nothing would: reconcilePlaylist is reached only from Reconcile, and
	// Reconcile is only called by settings saves, the API, the manager and the
	// scheduler -- none of which a finished job is.
	hub, err := relay.New(e.log, 0)
	if err != nil {
		e.log.Error("playlist: no relay", "err", err)
		return
	}

	// EVERY ITEM, IN ORDER, AND IT PLAYS THE DERIVATIVES. The gate above has
	// already required a derivative for every item; these are the very files it
	// stat'd, and the operator's originals are not opened at all. That is what
	// makes the fixed normalised profile load bearing rather than bookkeeping:
	// the concat demuxer requires every file in the list to share codecs,
	// timebase, resolution and channel layout, and `-c copy` across a seam is
	// only a copy because B1 guaranteed it.
	//
	// Built through playlistmedia.DerivativePath, never a join of our own, so
	// the profile version in the filename cannot drift from the one the
	// normaliser writes -- and so the name is reduced to its base on the way,
	// which is the confinement standing behind `-safe 0`.
	//
	// No per-entry `duration` directives: DurationMS is left zero deliberately.
	// Three real derivatives, looped past two full wraps, 1068 packets probed
	// with and without them -- identical ONLY when the directive is exact, and
	// ConcatList renders milliseconds. See the fuller note above playlistFeedArgs
	// and internal/playlistmedia/concat_behaviour_test.go.
	// ABSOLUTE, and that is a bug fix rather than tidiness.
	//
	// The concat demuxer resolves a relative entry against THE LIST FILE'S OWN
	// DIRECTORY -- and the list lives in <dataDir>/playlist-media, which is
	// exactly the prefix a relative DerivativePath already carries. So
	// `--data data` produced entries like `data/playlist-media/x.ts.v2.ts` in a
	// list at `data/playlist-media/`, the demuxer looked for
	// `data/playlist-media/data/playlist-media/x.ts.v2.ts`, and the tier
	// respawn-looped on a file that was sitting right there. Readiness had
	// already stat'd it and passed, because readiness resolves paths from the
	// process's working directory the way every other caller does.
	//
	// Nothing shipped hits it -- deployments pass an absolute --data and the
	// engine's own tests build one with t.TempDir() -- which is precisely why it
	// survived: every existing caller happened to be absolute, so the one that
	// is not had nothing watching it. Found by the acceptance suite.
	items := s.Failover.Playlist.Items
	entries := make([]ffmpeg.ConcatEntry, 0, len(items))
	for i := range items {
		p := playlistmedia.DerivativePath(e.cfg.DataDir, playlistItemUpload(items, i))
		abs, err := filepath.Abs(p)
		if err != nil {
			// Only when the working directory cannot be read, which is a great
			// deal more wrong than one playlist. Said out loud rather than
			// silently feeding the demuxer a path it will double.
			e.log.Error("playlist: cannot make a derivative path absolute",
				"path", p, "err", err)
			return
		}
		entries = append(entries, ffmpeg.ConcatEntry{Path: abs})
	}

	// THE FILENAME CARRIES THE SIGNATURE AND THIS ENGINE'S SOURCE ID, and it
	// needs both.
	//
	// The signature alone fixes the different-list overwrite but not the
	// identical-list case. internal/engine/manager.go runs ONE ENGINE PER
	// SOURCE over a shared data directory, so two sources configured with the
	// same playlist hash the same: one tier stopping would delete a file the
	// other's FFmpeg is still re-reading at its next wrap, and the second source
	// would drop off air for a reason nothing in its own configuration changed.
	// A tier deletes only the path it holds, and only once its process is gone.
	listPath := filepath.Join(playlistmedia.DerivativeDir(e.cfg.DataDir),
		fmt.Sprintf("playlist-%d-%s.txt", e.sourceID, want))
	if err := os.MkdirAll(filepath.Dir(listPath), 0o755); err != nil {
		e.log.Error("playlist: no derivative directory for the list", "err", err)
		_ = hub.Close()
		return
	}
	if err := os.WriteFile(listPath, []byte(ffmpeg.ConcatList(entries)), 0o600); err != nil {
		// Written BEFORE the process is spawned, so a list that cannot be
		// written is a tier that never starts rather than one that respawn-loops
		// on a file it will never find.
		e.log.Error("playlist: cannot write the concat list", "path", listPath, "err", err)
		_ = hub.Close()
		return
	}

	proc := supervisor.New(e.log, supervisor.Spec{
		Name: "playlist", Kind: "source", Bin: e.tools.FFmpeg,
		Args: playlistFeedArgs(listPath, hub.InputURL()),
		// AutoRestart, unlike a selector feed: this process publishes into its
		// OWN hub and carries no timestamp offset, so there is nothing for a
		// sweep to rebuild and no reason to make an operator wait for one. A
		// file that FFmpeg cannot open backs off toward five seconds and says so
		// every time, which is what the supervisor's backoff is for.
		AutoRestart: true,
		MinBackoff:  500 * time.Millisecond,
		MaxBackoff:  5 * time.Second,
		OnLog:       e.onLog, OnState: e.onState, LogSink: logSink{e},
	})

	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		_ = hub.Close()
		// No tier is recorded, so nothing else will ever own this file. It is
		// removed here or not at all.
		_ = os.Remove(listPath)
		return
	}
	e.playlist = &playlistTier{proc: proc, hub: hub, sig: want, listPath: listPath}
	e.mu.Unlock()

	proc.Start()
	e.log.Info("playlist started", "list", listPath, "items", len(entries),
		"relayPort", hub.Port(),
		"reason", "a playlist publishes into a hub of its own, so a file on air "+
			"never makes the primary ingest read live")
}

func (e *Engine) teardownPlaylist(p *playlistTier) {
	if p == nil {
		return
	}
	if p.proc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		p.proc.Stop(ctx)
		cancel()
	}
	if p.hub != nil {
		_ = p.hub.Close()
	}
	// AFTER Stop returns, and only the path this tier wrote.
	//
	// After, because the concat demuxer re-reads the list at every wrap: remove
	// it while the process is still running and a tier that was asked to stop
	// spends its last seconds failing to reopen its own input, logging as though
	// something were wrong. Only this path, because another source may hold an
	// identically-hashed list of its own -- which is why the filename carries a
	// source id at all.
	//
	// The error is dropped deliberately. A list that is already gone is the
	// ordinary outcome of a data directory cleaned up underneath us, and there
	// is nothing a stopping tier could usefully do about it.
	if p.listPath != "" {
		_ = os.Remove(p.listPath)
	}
}

// ------------------------------------------------------- failover: operator

// SwitchSource puts one source on air by hand.
//
// "auto" hands the decision back to the detector. Anything else is honoured
// only while that source is delivering, which is what makes a manual return
// safe: a pin cannot strand the broadcast on an input that has since died.
func (e *Engine) SwitchSource(kind string) error {
	var want sourceKind
	switch sourceKind(strings.ToLower(strings.TrimSpace(kind))) {
	case sourcePrimary:
		want = sourcePrimary
	case sourceBackup:
		want = sourceBackup
	case sourceSlate:
		want = sourceSlate
	case sourcePlaylist:
		// Accepted only now that a playlist feed can actually be built. The pin
		// itself has been honoured by the decision since the candidate was
		// added; this list was the one thing keeping an operator from setting
		// it, and it stayed shut on purpose while a decision of "playlist"
		// would have reached a feed layer that could not run one.
		want = sourcePlaylist
	case sourceNone, "auto":
		want = sourceNone
	default:
		return fmt.Errorf("unknown source %q (primary, backup, slate, playlist, auto)", kind)
	}

	e.selMu.Lock()
	defer e.selMu.Unlock()

	e.mu.Lock()
	if e.sel == nil || e.sel.hub == nil {
		e.mu.Unlock()
		return fmt.Errorf("failover is not enabled, so there is nothing to switch between")
	}
	prev := e.sel.pinned
	e.sel.pinned = want
	e.mu.Unlock()

	// The pin has to be committed before the decision, because the decision
	// reads it. That makes rolling it back the only way to keep the pin and
	// what actually happened in step: a failed decision switched nothing, and a
	// pin that survived one would be LATCHED -- re-read by every 500ms sweep,
	// re-panicking on the same input forever, with the dashboard showing a
	// pinned source that is not the active one and the operator holding an
	// HTTP 200 that said it worked. Rolled back under e.mu exactly as it was
	// set, and still inside the selMu the whole method holds, so no sweep can
	// observe the pin between the failure and the rollback. Nothing is
	// published: the state is what it was before the call.
	if err := e.applySourceChoice(e.Settings(), e.heldSilence(), time.Now()); err != nil {
		e.mu.Lock()
		if e.sel != nil {
			e.sel.pinned = prev
		}
		e.mu.Unlock()
		e.log.Error("source selection was not applied; the pin was rolled back",
			"source", e.sourceID, "requested", kind, "err", err)
		return fmt.Errorf("could not select source %q: %w", kind, err)
	}

	// Logged only once the switch has actually been applied. Announcing it
	// first is how the log came to say a source was selected on sweeps where
	// nothing was.
	if want == sourceNone {
		e.log.Info("source selection returned to automatic")
	} else {
		e.log.Info("source selected by operator", "source", string(want))
	}
	e.publishStatus()
	return nil
}

// FailoverStatus is the tier as the dashboard reports it. Absent entirely when
// the tier is not running, which is the default.
type FailoverStatus struct {
	Active sourceKind `json:"active"`
	Reason string     `json:"reason,omitempty"`
	// Pinned is the operator's manual choice, empty when the detector is in
	// charge.
	Pinned     sourceKind `json:"pinned,omitempty"`
	SwitchedAt time.Time  `json:"switchedAt"`
	Switches   int        `json:"switches"`
	Error      string     `json:"error,omitempty"`
	RelayPort  int        `json:"relayPort,omitempty"`

	PrimaryLive   bool `json:"primaryLive"`
	BackupLive    bool `json:"backupLive"`
	BackupEnabled bool `json:"backupEnabled"`
	SlateEnabled  bool `json:"slateEnabled"`

	Feed   *supervisor.Status `json:"feed,omitempty"`
	Backup *supervisor.Status `json:"backup,omitempty"`
}

// Failover returns the tier's live state, or nil when there is none.
func (e *Engine) Failover() *FailoverStatus {
	s := e.Settings()
	now := time.Now()
	grace := failoverGrace(s)

	// Everything the tier owns is copied out under the lock — the mutable
	// fields as VALUES, not through the selector pointer, because the failover
	// sweep is writing them from another goroutine. Only the two process
	// handles leave the lock, and those are read the way every other status
	// reads them: after it is released, so a state callback cannot deadlock
	// against a snapshot being taken.
	e.mu.RLock()
	sel := e.sel
	if sel == nil {
		e.mu.RUnlock()
		return nil
	}
	st := &FailoverStatus{
		Active:        sel.active,
		Reason:        sel.reason,
		Pinned:        sel.pinned,
		SwitchedAt:    sel.switchedAt,
		Switches:      sel.switches,
		Error:         sel.err,
		BackupEnabled: s.Failover.Backup.Enabled,
		SlateEnabled:  s.Failover.Slate.Enabled,
	}
	if sel.hub != nil {
		st.RelayPort = sel.hub.Port()
	}
	if sel.live != nil {
		st.PrimaryLive = sel.live[sourcePrimary].alive(now, grace)
		st.BackupLive = st.BackupEnabled && sel.live[sourceBackup].alive(now, grace)
	}
	var feedProc, backupProc *supervisor.Process
	if sel.feed != nil {
		feedProc = sel.feed.proc
	}
	if e.backup != nil {
		backupProc = e.backup.proc
	}
	e.mu.RUnlock()

	st.Feed = procStatus(feedProc)
	st.Backup = procStatus(backupProc)
	return st
}

// ------------------------------------------------------------------ probing

// probeLoop keeps the ingest's track layout up to date.
//
// Probing costs a short-lived ffprobe against its own relay subscription. It
// runs often while the layout is unknown and slowly once it is settled, so a
// streamer who changes their OBS track count mid-session is picked up without
// a restart.
func (e *Engine) probeLoop(ctx context.Context) {
	fast := 3 * time.Second
	slow := 30 * time.Second
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
			if changed := e.probeOnce(ctx); changed {
				// Layout changed: the meters process and every destination
				// graph were built against the old one, and a rendition that
				// inherits the source frame rate has a keyframe interval
				// derived from it. The recorder is in this list only when it
				// is writing stems, which are planned one per probed track —
				// its signature is unchanged otherwise, so this costs nothing.
				e.reconcileMeters(e.Settings())
				e.reconcileRecorder(e.Settings())
				_ = e.reconcileOutputs()
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
				e.probed = false
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

func (e *Engine) probeOnce(ctx context.Context) bool {
	port, err := e.alloc.Allocate()
	if err != nil {
		return false
	}
	defer e.alloc.Release(port)

	name := "probe"
	url := e.hub.Subscribe(name, port)
	defer e.hub.Unsubscribe(name)

	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	res, err := ffmpeg.Probe(pctx, e.tools.FFprobe, url, 3)
	if err != nil {
		return false
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
		return false
	}

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
	e.source = src
	e.probed = true
	e.videoInfo = res.Video
	e.mu.Unlock()

	if changed {
		e.log.Info("ingest layout probed", "audioTracks", len(src.Tracks))
		e.bus.Publish(events.TypeSource, e.SourceInfo())
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
}

// SourceInfo returns the layout downstream graphs are compiled against, which
// is the probe's unless the silence tier is standing in for it.
func (e *Engine) SourceInfo() SourceInfo {
	e.mu.RLock()
	probed, src, video := e.probed, e.source, e.videoInfo
	name := e.sourceName
	synthetic := e.silence != nil && e.silence.hub != nil
	e.mu.RUnlock()

	if synthetic {
		src = synthTrack()
	}
	return SourceInfo{
		ID: e.sourceID, Name: name,
		Probed: probed, Tracks: src.Tracks, Video: video, Synthetic: synthetic,
		Annotations: e.Settings().Ingest.Annotations,
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
	if e.stopped || e.loudOff || !s.Meters.Enabled {
		return nil
	}
	out := make(map[int64]loudnessPlan, len(e.dests))
	for id, d := range e.dests {
		if d.proc == nil || d.hub == nil || d.err != "" || d.compiled.FilterComplex == "" {
			continue
		}
		t := meters.TargetFor(d.row.Profile.Loudness,
			routing.PlatformFor(string(d.row.Platform), string(d.row.Kind)))
		out[id] = loudnessPlan{
			id: id, name: d.row.Name, hub: d.hub, compiled: d.compiled, target: t,
			// d.spec already hashes the graph and the upstream, so this adds
			// only what the analyser cares about that the destination does not.
			sig: hashStrings([]string{d.spec, t.Sig()}),
		}
	}
	return out
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
	if e.stopped {
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
		m.proc.Stop(ctx)
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
// The nil guard is for an Engine assembled field by field rather than through
// New — which is how the tests build one, and how a status snapshot could
// otherwise panic on a code path that has nothing to do with loudness.
func (e *Engine) Loudness() []meters.Report {
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
func (e *Engine) SetLoudnessMonitor(enabled bool) error {
	e.mu.Lock()
	if e.loudOff == !enabled {
		e.mu.Unlock()
		return nil
	}
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
	list, err := clips.List(cfg.Dir)
	if err != nil {
		return clips.Usage{}, err
	}
	u := clips.Usage{Count: len(list), MaxBytes: int64(cfg.MaxDiskMB) << 20, MaxClips: cfg.MaxClips}
	for _, cl := range list {
		u.UsedBytes += cl.Bytes
	}
	return u, nil
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
func (e *Engine) ClipBuffer() ClipStatus {
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

// Levels returns the most recent metering frame.
func (e *Engine) Levels() (ffmpeg.Levels, time.Time) {
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
// tests build one.
func (e *Engine) Alerts() *alerts.Notifier { return e.alerter }

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

// Scheduler exposes the schedule runner for the same reason.
func (e *Engine) Scheduler() *scheduler.Runner { return e.sched }

// scheduleActuator is how the scheduler reaches the enable/disable path.
//
// Deliberately hair-thin: a schedule writes exactly the intent a human writes
// and then asks for a reconcile, so a scheduled start and a clicked one are the
// same code and cannot drift apart.
type scheduleActuator struct{ e *Engine }

func (a scheduleActuator) SetDestinationEnabled(id int64, enabled bool) error {
	return a.e.store.SetDestinationEnabled(id, enabled)
}

func (a scheduleActuator) ListDestinationIDs() ([]int64, error) {
	rows, err := a.e.store.ListDestinations()
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids, nil
}

func (a scheduleActuator) Reconcile() error { return a.e.Reconcile() }

// onSchedule publishes the fact that a timetable moved something. A dashboard
// that shows a destination coming up with no explanation is how an operator
// concludes the server has a mind of its own.
func (e *Engine) onSchedule(r scheduler.Result) {
	e.bus.Publish(eventSchedule, r)
}

// observeWanted reports whether a sweep is worth building.
//
// Extracted so the gate is testable without an engine. The failure it guards is
// silent: this loop used to skip everything when no ALERT rules existed, and a
// hook is a second consumer of the same snapshot, so an install with hooks and
// no alert rules would have observed nothing at all.
func observeWanted(alertRules, hookRules bool) bool { return alertRules || hookRules }

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
			if !observeWanted(e.alerter.HasRules(), e.hooks.HasHooks()) {
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
				for _, ev := range e.hookWatch.Observe(snap) {
					e.hooks.Publish(ev)
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
		snap.Destinations = append(snap.Destinations, alerts.DestState{
			ID: d.ID, Name: d.Name, Enabled: d.Enabled,
			// A destination whose graph would not compile has no process at
			// all, and that is as down as a failed one.
			Running:  d.Error == "" && d.Process != nil && d.Process.State == supervisor.StateRunning,
			Platform: string(d.Platform),
			Error:    d.Error,
		})
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

// ------------------------------------------------------------------- status

// DestStatus is one destination's live state, as the dashboard renders it.
type DestStatus struct {
	ID            int64              `json:"id"`
	Name          string             `json:"name"`
	Kind          db.DestKind        `json:"kind"`
	Platform      db.Platform        `json:"platform"`
	Enabled       bool               `json:"enabled"`
	Summary       string             `json:"summary"`
	Tracks        []int              `json:"tracks"`
	FilterComplex string             `json:"filterComplex"`
	Normalization routing.NormMode   `json:"normalization"`
	Warnings      []string           `json:"warnings"`
	Error         string             `json:"error,omitempty"`
	Process       *supervisor.Status `json:"process,omitempty"`
	// RenditionID is the shared encode this destination reads, nil for
	// passthrough. RenditionName is its label, empty for passthrough, so the
	// dashboard can group destinations under the encode they share.
	RenditionID   *int64 `json:"renditionId,omitempty"`
	RenditionName string `json:"renditionName,omitempty"`
}

// RenditionStatus is one shared video encode's live state.
//
// Consumers is the ref count the engine acted on: a rendition with none has no
// process, by design, and the dashboard should say so rather than show it as
// failed.
type RenditionStatus struct {
	ID           int64              `json:"id"`
	Name         string             `json:"name"`
	Width        int                `json:"width"`
	Height       int                `json:"height"`
	FPS          int                `json:"fps"`
	VideoBitrate int                `json:"videoBitrate"`
	Encoder      db.VideoEncoder    `json:"encoder"`
	Codec        string             `json:"codec"`
	Consumers    int                `json:"consumers"`
	RelayPort    int                `json:"relayPort,omitempty"`
	Error        string             `json:"error,omitempty"`
	Process      *supervisor.Status `json:"process,omitempty"`
}

// Status is the whole-system snapshot pushed over the WebSocket.
type Status struct {
	Ingest   *supervisor.Status `json:"ingest,omitempty"`
	Recorder *supervisor.Status `json:"recorder,omitempty"`
	Preview  *supervisor.Status `json:"preview,omitempty"`
	Meters   *supervisor.Status `json:"meters,omitempty"`
	// Silence is the synthetic-audio tier, absent unless it is running. Nothing
	// in the stream can say why a video-only ingest suddenly has audio — the
	// MPEG-TS muxer discards a track title — so this is the only place it can
	// be explained.
	Silence *SilenceStatus `json:"silence,omitempty"`
	// Failover is the source-selector tier, absent unless it is running. Which
	// source is on air has to be visible somewhere: a failover nobody notices is
	// how an operator discovers at the end of a broadcast that they streamed the
	// backup all night.
	Failover     *FailoverStatus   `json:"failover,omitempty"`
	Renditions   []RenditionStatus `json:"renditions"`
	Destinations []DestStatus      `json:"destinations"`
	Source       SourceInfo        `json:"source"`
	Relay        relay.Stats       `json:"relay"`
	// Loudness is the post-routing EBU R128 report for each monitored
	// destination — what the platform on the other end actually receives, which
	// is the only loudness figure it will judge the stream on.
	Loudness []meters.Report `json:"loudness"`
	// Clips is the rolling capture buffer's state.
	Clips ClipStatus `json:"clips"`
}

// procStatus is nil for a process that is not running, which the JSON omits.
func procStatus(p *supervisor.Process) *supervisor.Status {
	if p == nil {
		return nil
	}
	s := p.Status()
	return &s
}

// Renditions returns the live state of every shared encode.
//
// Every rendition row appears, running or not: one with no enabled destination
// is idle on purpose and must not read as broken.
func (e *Engine) Renditions() []RenditionStatus {
	rows, err := e.store.ListRenditions()
	if err != nil {
		return []RenditionStatus{}
	}
	counts, cerr := e.store.CountEnabledDestinationsByRendition()
	if cerr != nil {
		counts = map[int64]int{}
	}
	// The same fold reconcileOutputs does. Without it a tier kept alive purely
	// by a playout variant reads as "0 consumers" on a card that is showing a
	// running process, which is the dashboard calling its own decision a bug.
	for id, n := range playout.RenditionRefs(e.Settings().Playout) {
		counts[id] += n
	}

	e.mu.RLock()
	live := make(map[int64]*rendition, len(e.rends))
	for id, r := range e.rends {
		live[id] = r
	}
	e.mu.RUnlock()

	out := make([]RenditionStatus, 0, len(rows))
	for _, row := range rows {
		rs := RenditionStatus{
			ID: row.ID, Name: row.Name, Width: row.Width, Height: row.Height,
			FPS: row.FPS, VideoBitrate: row.VideoBitrate, Encoder: row.Encoder,
			Codec: row.Codec(), Consumers: counts[row.ID],
		}
		if r := live[row.ID]; r != nil {
			rs.Error = r.err
			rs.Process = procStatus(r.proc)
			if r.hub != nil {
				rs.RelayPort = r.hub.Port()
			}
		}
		out = append(out, rs)
	}
	return out
}

// Status assembles the current snapshot.
func (e *Engine) Status() Status {
	e.mu.RLock()
	ingest, recorder, preview, meters := e.ingest, e.recorder, e.preview, e.meters
	dests := make([]*destination, 0, len(e.dests))
	for _, d := range e.dests {
		dests = append(dests, d)
	}
	e.mu.RUnlock()

	st := Status{
		Source:       e.SourceInfo(),
		Relay:        e.hub.Stats(),
		Renditions:   e.Renditions(),
		Destinations: []DestStatus{},
		Loudness:     e.Loudness(),
		Clips:        e.ClipBuffer(),
	}
	st.Ingest = procStatus(ingest)
	st.Recorder = procStatus(recorder)
	st.Preview = procStatus(preview)
	st.Meters = procStatus(meters)
	st.Silence = e.Silence()
	st.Failover = e.Failover()

	names := make(map[int64]string, len(st.Renditions))
	for _, r := range st.Renditions {
		names[r.ID] = r.Name
	}

	// Every destination row appears, running or not, so the dashboard shows a
	// disabled destination rather than silently omitting it.
	rows, err := e.store.ListDestinations()
	if err == nil {
		for _, row := range rows {
			ds := DestStatus{
				ID: row.ID, Name: row.Name, Kind: row.Kind,
				Platform: row.Platform, Enabled: row.Enabled,
				RenditionID: row.RenditionID,
			}
			if row.RenditionID != nil {
				ds.RenditionName = names[*row.RenditionID]
			}
			if live := e.destByID(dests, row.ID); live != nil {
				ds.Summary = live.compiled.Summary
				ds.Tracks = live.compiled.Tracks
				ds.FilterComplex = live.compiled.FilterComplex
				ds.Normalization = live.compiled.Normalization
				ds.Warnings = live.compiled.Warnings
				ds.Error = live.err
				ds.Process = procStatus(live.proc)
			} else if c, cerr := routing.Compile(row.Profile, e.Source()); cerr == nil {
				// Not running: still show what it *would* send, so the card is
				// informative before the stream is ever started.
				ds.Summary = c.Summary
				ds.Tracks = c.Tracks
				ds.FilterComplex = c.FilterComplex
				ds.Normalization = c.Normalization
				ds.Warnings = c.Warnings
			} else {
				ds.Error = cerr.Error()
			}
			st.Destinations = append(st.Destinations, ds)
		}
	}
	return st
}

func (e *Engine) destByID(list []*destination, id int64) *destination {
	for _, d := range list {
		if d.row != nil && d.row.ID == id {
			return d
		}
	}
	return nil
}

// Processes returns every supervised process, for the monitoring page.
func (e *Engine) Processes() []*supervisor.Process {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var out []*supervisor.Process
	procs := []*supervisor.Process{e.ingest, e.recorder, e.preview, e.meters}
	if e.silence != nil {
		procs = append(procs, e.silence.proc)
	}
	if e.backup != nil {
		procs = append(procs, e.backup.proc)
	}
	if e.playlist != nil {
		procs = append(procs, e.playlist.proc)
	}
	if e.sel != nil && e.sel.feed != nil {
		procs = append(procs, e.sel.feed.proc)
	}
	for _, p := range procs {
		if p != nil {
			out = append(out, p)
		}
	}
	for _, r := range e.rends {
		if r.proc != nil {
			out = append(out, r.proc)
		}
	}
	for _, d := range e.dests {
		if d.proc != nil {
			out = append(out, d.proc)
		}
	}
	// Sorted, because a map of analysers would otherwise reshuffle the
	// monitoring page on every poll.
	for _, id := range slices.Sorted(maps.Keys(e.loud)) {
		if m := e.loud[id]; m != nil && m.proc != nil {
			out = append(out, m.proc)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(e.playProcs)) {
		out = append(out, e.playProcs[name])
	}
	return out
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
				"system":  e.mon.System(),
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
