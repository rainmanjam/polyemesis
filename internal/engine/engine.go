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

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/recording"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/stats"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
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
)

// Engine owns the whole streaming pipeline.
type Engine struct {
	log    *slog.Logger
	cfg    config.Config
	store  *db.DB
	tools  *ffmpeg.Tools
	bus    *events.Broker
	hub    *relay.Hub
	alloc  *relay.PortAllocator
	mon    *stats.Monitor
	recman *recording.Manager

	// sink persists captured stderr beyond the in-memory ring. One sink for
	// every child on purpose: the file reads as a single interleaved timeline.
	// It is read on the stderr goroutine of every child and swapped whenever
	// the logging settings change, so it is atomic rather than under e.mu.
	sink    atomic.Pointer[supervisor.FileSink]
	sinkMu  sync.Mutex
	sinkCfg db.LoggingSettings

	mu       sync.RWMutex
	ingest   *supervisor.Process
	recorder *supervisor.Process
	preview  *supervisor.Process
	meters   *supervisor.Process
	dests    map[int64]*destination
	rends    map[int64]*rendition

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

	// source is the probed ingest layout. Until the ingest carries a stream,
	// this is DefaultSource() so the routing editor still has something to
	// render.
	source    routing.Source
	probed    bool
	videoInfo *ffmpeg.VideoStream
	levels    ffmpeg.Levels
	levelsAt  time.Time
	settings  db.Settings

	// previewMu serializes preview lifecycle changes. Unlike every other
	// child, the preview is started from an HTTP handler, so two playlist
	// requests can race to spawn it.
	previewMu sync.Mutex
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
	// port and subName are its subscription on the INGEST hub, i.e. its input.
	port    int
	subName string
	// spec plays the same role as a destination's: a hash of everything the
	// encode's command line depends on, so editing an unrelated rendition or
	// renaming this one never cycles a live encode.
	spec string
	err  string
}

// New creates the engine and binds the relay hub.
func New(log *slog.Logger, cfg config.Config, store *db.DB, tools *ffmpeg.Tools, bus *events.Broker) (*Engine, error) {
	hub, err := relay.New(log, 0)
	if err != nil {
		return nil, err
	}

	e := &Engine{
		log:    log,
		cfg:    cfg,
		store:  store,
		tools:  tools,
		bus:    bus,
		hub:    hub,
		alloc:  relay.NewPortAllocator(relayPortBase, relayPortSpan),
		dests:  map[int64]*destination{},
		rends:  map[int64]*rendition{},
		source: routing.DefaultSource(),
	}
	e.mon = stats.NewMonitor(hub.RxBytes)
	e.recman = recording.New(log, store, cfg.RecordingsDir(), func() {
		bus.Publish(events.TypeRecordings, nil)
	},
		recording.WithFFprobe(tools.FFprobe),
		recording.WithStorageGuard(e.onStorage),
	)
	return e, nil
}

// Hub exposes the relay for the monitoring endpoint.
func (e *Engine) Hub() *relay.Hub { return e.hub }

// Monitor exposes host/bitrate stats.
func (e *Engine) Monitor() *stats.Monitor { return e.mon }

// Recordings exposes the recording manager.
func (e *Engine) Recordings() *recording.Manager { return e.recman }

// Tools exposes the detected FFmpeg.
func (e *Engine) Tools() *ffmpeg.Tools { return e.tools }

// Start brings the pipeline up and begins the background loops.
func (e *Engine) Start(ctx context.Context) error {
	e.ctx, e.cancel = context.WithCancel(ctx)

	settings, err := e.store.GetSettings()
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
	// between here and the teardown below.
	e.previewMu.Lock()
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
	recorder, preview, meters, ingest := e.recorder, e.preview, e.meters, e.ingest
	e.dests = map[int64]*destination{}
	e.rends = map[int64]*rendition{}
	e.recorder, e.preview, e.meters, e.ingest = nil, nil, nil, nil
	e.mu.Unlock()
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
	stop(recorder)
	stop(preview)
	stop(meters)
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

// Reconcile makes the running processes match the database. It is safe to call
// repeatedly and from any handler.
func (e *Engine) Reconcile() error {
	settings, err := e.store.GetSettings()
	if err != nil {
		return err
	}
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
	e.publishStatus()
	return nil
}

func (e *Engine) reconcileIngest(s, prev db.Settings) {
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

func (e *Engine) ingestSpec(s db.Settings) []string {
	spec := ffmpeg.IngestSpec{
		Kind:          ffmpeg.IngestKind(s.Ingest.Mode),
		SRTPort:       s.Ingest.SRT.Port,
		SRTPassphrase: s.Ingest.SRT.Passphrase,
		SRTLatencyMS:  s.Ingest.SRT.LatencyMS,
		RTMPPort:      s.Ingest.RTMP.Port,
		RTMPApp:       s.Ingest.RTMP.App,
		RTMPStreamKey: s.Ingest.RTMP.StreamKey,
		RelayURL:      e.hub.InputURL(),
	}
	return ffmpeg.IngestArgs(spec)
}

func (e *Engine) ingestPublicURL(s db.Settings) string {
	spec := ffmpeg.IngestSpec{
		Kind:          ffmpeg.IngestKind(s.Ingest.Mode),
		SRTPort:       s.Ingest.SRT.Port,
		SRTPassphrase: s.Ingest.SRT.Passphrase,
		SRTLatencyMS:  s.Ingest.SRT.LatencyMS,
		RTMPPort:      s.Ingest.RTMP.Port,
		RTMPApp:       s.Ingest.RTMP.App,
	}
	return spec.PublicIngestURL("<server>")
}

func (e *Engine) reconcileRecorder(s db.Settings) {
	e.mu.RLock()
	cur := e.recorder
	e.mu.RUnlock()

	// The free-space guard has the last word: recording into a volume that is
	// about to fill takes the database and the preview down with it.
	if !s.Recording.Enabled || !e.recman.RecordingAllowed() {
		if cur != nil {
			e.stopAux(&e.recorder, "recorder")
		}
		return
	}
	sig := strconv.Itoa(s.Recording.SegmentSeconds)
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

	args := ffmpeg.RecorderArgs(ffmpeg.RecorderSpec{
		RelayURL:       url,
		OutputPattern:  filepath.Join(e.cfg.RecordingsDir(), "rec-%Y%m%d-%H%M%S.mkv"),
		SegmentSeconds: s.Recording.SegmentSeconds,
	})

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
func (e *Engine) reconcileMeters(s db.Settings) {
	e.mu.RLock()
	cur := e.meters
	src := e.source
	e.mu.RUnlock()

	if !s.Meters.Enabled || len(src.Tracks) == 0 {
		if cur != nil {
			e.stopAux(&e.meters, "meters")
		}
		return
	}

	channels := make([]int, len(src.Tracks))
	for i, t := range src.Tracks {
		channels[i] = t.Channels
	}
	sig := hashStrings([]string{fmt.Sprint(channels)})
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
	url := e.hub.Subscribe("meters", port)

	args := ffmpeg.MetersArgs(ffmpeg.MetersSpec{RelayURL: url, TrackChannels: channels})
	interval := time.Duration(s.Meters.IntervalMS) * time.Millisecond

	proc := supervisor.New(e.log, supervisor.Spec{
		Name: "meters", Kind: "meters", Bin: e.tools.FFmpeg, Args: args,
		AutoRestart: true,
		// astats prints far faster than any UI can draw; throttle here rather
		// than flooding every WebSocket client with 50 frames a second.
		StdoutHandler: func(r io.Reader) error {
			var last time.Time
			return ffmpeg.ParseLevels(r, channels, func(l ffmpeg.Levels) {
				if time.Since(last) < interval {
					return
				}
				last = time.Now()
				e.mu.Lock()
				e.levels = l
				e.levelsAt = last
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
	destRows, err := e.store.ListDestinations()
	if err != nil {
		return err
	}
	rendRows, err := e.store.ListRenditions()
	if err != nil {
		return err
	}
	// The ref count, straight from the database: a rendition nothing enabled
	// selects is absent, and absent means "must not be burning CPU".
	counts, err := e.store.CountEnabledDestinationsByRendition()
	if err != nil {
		return err
	}

	e.mu.RLock()
	src := e.source
	fps := probedFPS(e.videoInfo)
	e.mu.RUnlock()

	wantRends := wantedRenditions(rendRows, counts, func(r *db.Rendition) string {
		return renditionSig(r, fps)
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

	plans := e.planDestinations(destRows, wantRends, src)

	e.stopDestinations(plans)
	for _, id := range stopRends {
		e.mu.Lock()
		r := e.rends[id]
		delete(e.rends, id)
		e.mu.Unlock()
		e.teardownRendition(r)
	}

	byID := make(map[int64]*db.Rendition, len(rendRows))
	for _, r := range rendRows {
		byID[r.ID] = r
	}
	for _, id := range startRends {
		e.startRendition(byID[id], wantRends[id], fps, counts[id])
	}

	e.startDestinations(plans)
	return nil
}

// planDestinations works out the desired state of every enabled destination,
// including which upstream it reads and whether it can run at all.
func (e *Engine) planDestinations(rows []*db.Destination, wantRends map[int64]string, src routing.Source) map[int64]destPlan {
	plans := map[int64]destPlan{}
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		p := destPlan{row: row}

		// The upstream's own signature rides in the destination's, so editing a
		// rendition restarts exactly the destinations downstream of it and
		// nothing else.
		upstream := ""
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

		if err := e.startDest(p.row, p.compiled, p.spec, hub); err != nil {
			e.log.Error("start destination", "dest", p.row.Name, "err", err)
			e.mu.Lock()
			e.dests[id] = &destination{row: p.row, compiled: p.compiled, err: err.Error()}
			e.mu.Unlock()
		}
	}
}

// upstreamHub is the relay a destination reads: the ingest's when it is on
// passthrough, its rendition's own otherwise.
func (e *Engine) upstreamHub(row *db.Destination) (*relay.Hub, error) {
	if row.RenditionID == nil {
		return e.hub, nil
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
	})
}

func (e *Engine) startDest(row *db.Destination, compiled routing.Result, spec string, hub *relay.Hub) error {
	port, err := e.alloc.Allocate()
	if err != nil {
		return err
	}
	subName := fmt.Sprintf("dest:%d", row.ID)
	url := hub.Subscribe(subName, port)

	target := row.Target()
	if row.Kind == db.DestFile {
		// File destinations are confined to the recordings directory; the
		// path never comes straight from user input.
		resolved, err := e.recman.Resolve(row.URL)
		if err != nil {
			e.hub.Unsubscribe(subName)
			e.alloc.Release(port)
			return err
		}
		target = resolved
	}

	args := ffmpeg.DestinationArgs(ffmpeg.DestSpec{
		Kind:          ffmpeg.DestKind(row.Kind),
		Target:        target,
		RelayURL:      url,
		FilterComplex: compiled.FilterComplex,
		AudioOutLabel: compiled.OutLabel,
		AudioBitrate:  row.AudioBitrate,
		SampleRate:    row.Profile.SampleRate,
		CopyVideo:     true,
	})

	proc := supervisor.New(e.log, supervisor.Spec{
		Name:        subName,
		Kind:        "destination",
		Bin:         e.tools.FFmpeg,
		Args:        args,
		AutoRestart: true,
		OnLog:       e.onLog,
		OnState:     e.onState,
		LogSink:     logSink{e},
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
	return nil
}

func (e *Engine) teardownDest(d *destination) {
	if d == nil {
		return
	}
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
func renditionSig(r *db.Rendition, sourceFPS float64) string {
	parts := []string{
		strconv.Itoa(r.Width), strconv.Itoa(r.Height), strconv.Itoa(r.FPS),
		strconv.Itoa(r.VideoBitrate), string(r.Encoder), r.Preset,
		strconv.FormatFloat(r.GOPSeconds, 'g', -1, 64),
	}
	if r.FPS == 0 {
		parts = append(parts, strconv.FormatFloat(sourceFPS, 'g', -1, 64))
	}
	return hashStrings(parts)
}

// renditionSpecOf maps a stored rendition onto the encode's command line.
//
// There is no audio field to map, and there must never be one: RenditionArgs
// copies every audio track through with -c:a copy, which is what leaves the
// per-destination routing graphs downstream with the full multitrack ingest to
// work from.
func renditionSpecOf(r *db.Rendition, in, out string, sourceFPS float64) ffmpeg.RenditionSpec {
	return ffmpeg.RenditionSpec{
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

	subName := fmt.Sprintf("rendition:%d", row.ID)
	in := e.hub.Subscribe(subName, port)
	args := ffmpeg.RenditionArgs(renditionSpecOf(row, in, hub.InputURL(), sourceFPS))

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
		e.hub.Unsubscribe(subName)
		e.alloc.Release(port)
		_ = hub.Close()
		return
	}
	e.rends[row.ID] = &rendition{
		row: row, proc: proc, hub: hub, port: port, subName: subName, spec: spec,
	}
	e.mu.Unlock()

	proc.Start()
	e.log.Info("rendition started", "rendition", row.Name, "encoder", row.Encoder,
		"bitrate", row.VideoBitrate, "consumers", consumers, "relayPort", hub.Port())
}

func (e *Engine) teardownRendition(r *rendition) {
	if r == nil {
		return
	}
	if r.proc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		r.proc.Stop(ctx)
		cancel()
	}
	if r.subName != "" {
		e.hub.Unsubscribe(r.subName)
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
	}
	e.mu.Unlock()

	if proc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		proc.Stop(ctx)
		cancel()
	}
	e.hub.Unsubscribe(name)
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
				// derived from it.
				e.reconcileMeters(e.Settings())
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
	if err != nil || len(res.Audio) == 0 {
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

// Source returns the current ingest track layout.
func (e *Engine) Source() routing.Source {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.source
}

// SourceInfo is the ingest layout as the API reports it.
type SourceInfo struct {
	Probed bool                `json:"probed"`
	Tracks []routing.Track     `json:"tracks"`
	Video  *ffmpeg.VideoStream `json:"video,omitempty"`
}

// SourceInfo returns the probed layout.
func (e *Engine) SourceInfo() SourceInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return SourceInfo{Probed: e.probed, Tracks: e.source.Tracks, Video: e.videoInfo}
}

// Levels returns the most recent metering frame.
func (e *Engine) Levels() (ffmpeg.Levels, time.Time) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.levels, e.levelsAt
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
	Ingest       *supervisor.Status `json:"ingest,omitempty"`
	Recorder     *supervisor.Status `json:"recorder,omitempty"`
	Preview      *supervisor.Status `json:"preview,omitempty"`
	Meters       *supervisor.Status `json:"meters,omitempty"`
	Renditions   []RenditionStatus  `json:"renditions"`
	Destinations []DestStatus       `json:"destinations"`
	Source       SourceInfo         `json:"source"`
	Relay        relay.Stats        `json:"relay"`
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
	}
	st.Ingest = procStatus(ingest)
	st.Recorder = procStatus(recorder)
	st.Preview = procStatus(preview)
	st.Meters = procStatus(meters)

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
	for _, p := range []*supervisor.Process{e.ingest, e.recorder, e.preview, e.meters} {
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
