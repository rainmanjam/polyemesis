// Package engine is the orchestrator. It owns the relay hub and every
// supervised FFmpeg process, and keeps them reconciled against the database.
//
// The central idea is reconciliation, not commands: the API mutates rows and
// calls Reconcile, and the engine works out what must start, stop or restart.
// That is what makes "changing a routing profile restarts only that
// destination" fall out naturally instead of needing a special code path.
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
	"strconv"
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
	// spec is a hash of everything that would require a restart. Comparing it
	// is what keeps an unrelated edit from cycling a healthy stream.
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

	// Consumers first, then the ingest. Stopping the ingest first would make
	// every consumer log a spurious "input ended" as it dies.
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
	recorder, preview, meters, ingest := e.recorder, e.preview, e.meters, e.ingest
	e.dests = map[int64]*destination{}
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

	if err := e.reconcileDestinations(); err != nil {
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

func (e *Engine) reconcileDestinations() error {
	rows, err := e.store.ListDestinations()
	if err != nil {
		return err
	}

	e.mu.RLock()
	src := e.source
	e.mu.RUnlock()

	want := map[int64]*db.Destination{}
	for _, r := range rows {
		if r.Enabled {
			want[r.ID] = r
		}
	}

	// Stop destinations that are gone or newly disabled.
	e.mu.Lock()
	var toStop []*destination
	for id, d := range e.dests {
		if _, ok := want[id]; !ok {
			toStop = append(toStop, d)
			delete(e.dests, id)
		}
	}
	e.mu.Unlock()
	for _, d := range toStop {
		e.teardownDest(d)
	}

	for id, row := range want {
		compiled, cerr := routing.Compile(row.Profile, src)
		spec := ""
		if cerr == nil {
			spec = hashStrings([]string{
				row.Target(), string(row.Kind), compiled.FilterComplex,
				strconv.Itoa(row.AudioBitrate), strconv.Itoa(row.Profile.SampleRate),
			})
		}

		e.mu.RLock()
		cur := e.dests[id]
		e.mu.RUnlock()

		if cerr != nil {
			// A destination whose routing cannot compile must not run with a
			// stale graph: stop it and surface the reason.
			if cur != nil {
				e.teardownDest(cur)
				e.mu.Lock()
				delete(e.dests, id)
				e.mu.Unlock()
			}
			e.mu.Lock()
			e.dests[id] = &destination{row: row, err: cerr.Error()}
			e.mu.Unlock()
			e.log.Warn("destination routing invalid", "dest", row.Name, "err", cerr)
			continue
		}

		// Nothing that matters changed: leave the stream alone. This is the
		// guarantee that renaming a destination, or editing a different one,
		// never interrupts a live output.
		if cur != nil && cur.proc != nil && cur.spec == spec {
			cur.row = row
			continue
		}
		if cur != nil {
			e.teardownDest(cur)
		}
		if err := e.startDest(row, compiled, spec); err != nil {
			e.log.Error("start destination", "dest", row.Name, "err", err)
			e.mu.Lock()
			e.dests[id] = &destination{row: row, compiled: compiled, err: err.Error()}
			e.mu.Unlock()
		}
	}
	return nil
}

func (e *Engine) startDest(row *db.Destination, compiled routing.Result, spec string) error {
	port, err := e.alloc.Allocate()
	if err != nil {
		return err
	}
	subName := fmt.Sprintf("dest:%d", row.ID)
	url := e.hub.Subscribe(subName, port)

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
		compiled: compiled, spec: spec,
	}
	e.mu.Unlock()

	proc.Start()
	e.log.Info("destination started", "dest", row.Name, "kind", row.Kind,
		"tracks", compiled.Summary)
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
		e.hub.Unsubscribe(d.subName)
	}
	if d.port != 0 {
		e.alloc.Release(d.port)
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
				// graph were built against the old one.
				e.reconcileMeters(e.Settings())
				_ = e.reconcileDestinations()
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
	changed := !sameSource(e.source, src) || !e.probed
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
}

// Status is the whole-system snapshot pushed over the WebSocket.
type Status struct {
	Ingest       *supervisor.Status `json:"ingest,omitempty"`
	Recorder     *supervisor.Status `json:"recorder,omitempty"`
	Preview      *supervisor.Status `json:"preview,omitempty"`
	Meters       *supervisor.Status `json:"meters,omitempty"`
	Destinations []DestStatus       `json:"destinations"`
	Source       SourceInfo         `json:"source"`
	Relay        relay.Stats        `json:"relay"`
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
		Destinations: []DestStatus{},
	}
	statusOf := func(p *supervisor.Process) *supervisor.Status {
		if p == nil {
			return nil
		}
		s := p.Status()
		return &s
	}
	st.Ingest = statusOf(ingest)
	st.Recorder = statusOf(recorder)
	st.Preview = statusOf(preview)
	st.Meters = statusOf(meters)

	// Every destination row appears, running or not, so the dashboard shows a
	// disabled destination rather than silently omitting it.
	rows, err := e.store.ListDestinations()
	if err == nil {
		for _, row := range rows {
			ds := DestStatus{
				ID: row.ID, Name: row.Name, Kind: row.Kind,
				Platform: row.Platform, Enabled: row.Enabled,
			}
			if live := e.destByID(dests, row.ID); live != nil {
				ds.Summary = live.compiled.Summary
				ds.Tracks = live.compiled.Tracks
				ds.FilterComplex = live.compiled.FilterComplex
				ds.Normalization = live.compiled.Normalization
				ds.Warnings = live.compiled.Warnings
				ds.Error = live.err
				ds.Process = statusOf(live.proc)
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
	for _, d := range e.dests {
		if d.proc != nil {
			out = append(out, d.proc)
		}
	}
	return out
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
