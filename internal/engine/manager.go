package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/srtserver"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// Manager runs one Engine per source.
//
// The alternative was to make a single Engine internally multi-source, which
// would have meant reworking the hub, the reconciler, the destination and
// rendition maps, the silence tier and the failover selector all at once --
// every one of which is already correct for exactly one programme. Running N
// engines reuses all of that as-is, and the only genuinely shared state is the
// relay port allocator.
//
// That allocator is the reason this type has to exist rather than main.go
// keeping a slice: two engines minting their own allocators over the same base
// and span would hand out identical relay ports, and the second programme's
// destinations would quietly bind onto the first programme's traffic. One
// allocator, handed to every engine.
type Manager struct {
	log   *slog.Logger
	cfg   config.Config
	store *db.DB
	tools *ffmpeg.Tools
	bus   *events.Broker
	alloc *relay.PortAllocator

	mu sync.RWMutex
	// srt is the one-port listener, shared by every source. Nil when the
	// feature is off or the port could not be bound -- in which case the
	// per-source ports still work, which is why a bind failure is logged
	// rather than fatal.
	srt     *srtserver.Server
	srtAddr string
	engines map[int64]*Engine
	order   []int64 // source ids in display order, so Default is deterministic
	ctx     context.Context
	started bool

	// transcriber is remembered rather than applied once, because engines are
	// created after Start whenever a source is added, and a programme whose
	// recordings silently never transcribe is a bug nobody reports.
	tw        *transcribe.Tools
	modelsDir string
	nice      func(name string, args []string) (string, []string)

	// alertAttempts is remembered for the same reason and against the same
	// failure: a source added after the setting was saved would otherwise chase
	// a dead webhook for a different number of tries than every other source,
	// with nothing to show an operator that the two disagree. Zero means
	// "never set", and leaves the alerts package default in place.
	alertAttempts int
}

// NewManager builds the manager. No engines exist until Start.
func NewManager(log *slog.Logger, cfg config.Config, store *db.DB, tools *ffmpeg.Tools, bus *events.Broker) *Manager {
	return &Manager{
		log:     log,
		cfg:     cfg,
		store:   store,
		tools:   tools,
		bus:     bus,
		alloc:   relay.NewPortAllocator(relayPortBase, relayPortSpan),
		engines: map[int64]*Engine{},
	}
}

// Start brings up an engine for every source.
//
// A source that fails to start does not stop the others. With several
// programmes on one install, one misconfigured ingest -- a port already taken,
// say -- must not take the rest off the air; that would make adding a source a
// risk to everything already running.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	m.ctx = ctx
	m.started = true
	m.mu.Unlock()

	if err := m.Sync(); err != nil {
		return err
	}
	m.reconcileSharedIngest()
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.engines) == 0 {
		return fmt.Errorf("no sources to start")
	}
	return nil
}

// Sync makes the set of running engines match the sources table, starting
// engines for new sources and stopping those whose source has gone.
func (m *Manager) Sync() error {
	rows, err := m.store.ListSources()
	if err != nil {
		return err
	}

	m.mu.Lock()
	ctx, started := m.ctx, m.started
	want := make(map[int64]bool, len(rows))
	order := make([]int64, 0, len(rows))
	for _, s := range rows {
		want[s.ID] = true
		order = append(order, s.ID)
	}
	m.order = order

	// Stop first, so a source that was deleted releases its ports and its
	// listener before a new one possibly claims the same numbers.
	var stopping []*Engine
	for id, eng := range m.engines {
		if !want[id] {
			stopping = append(stopping, eng)
			delete(m.engines, id)
		}
	}
	var missing []int64
	for _, s := range rows {
		if _, ok := m.engines[s.ID]; !ok {
			missing = append(missing, s.ID)
		}
	}
	m.mu.Unlock()

	for _, eng := range stopping {
		eng.Stop()
	}
	if !started {
		return nil
	}

	for _, id := range missing {
		eng, err := New(m.log, m.cfg, m.store, m.tools, m.bus, id, m.alloc)
		if err != nil {
			m.log.Error("cannot build engine for source", "source", id, "err", err)
			continue
		}
		m.mu.RLock()
		tw, dir, nice := m.tw, m.modelsDir, m.nice
		attempts := m.alertAttempts
		m.mu.RUnlock()
		if tw != nil {
			eng.SetTranscriber(tw, dir, nice)
		}
		// Zero means the operator never set one, which leaves the alerts
		// package default rather than clamping this engine to something no
		// other engine is using. SetRetry tolerates a nil Notifier.
		if attempts > 0 {
			eng.Alerts().SetRetry(attempts)
		}
		if err := eng.Start(ctx); err != nil {
			m.log.Error("cannot start engine for source", "source", id, "err", err)
			eng.Stop()
			continue
		}
		m.mu.Lock()
		m.engines[id] = eng
		m.mu.Unlock()
	}
	return nil
}

// reconcileSharedIngest brings the one-port SRT listener up or down to match
// the settings, and rebinds it when the port changes.
//
// It lives on the manager because it is ONE listener for every source: an
// engine could not own it without owning the other engines' traffic.
func (m *Manager) reconcileSharedIngest() {
	st, err := m.store.GetSettings()
	if err != nil {
		m.log.Warn("cannot read shared-ingest settings", "err", err)
		return
	}
	port := st.Listeners.SRTPort
	// The store does not validate -- Settings.Validate runs in the API handler
	// -- so the manager is the last place between a stored value and a bound
	// socket, and it has to check. Port 0 is the one that matters: it is not
	// an error to the kernel, it means "any free port", so a zero here would
	// bind something random, report itself as listening, and tell the operator
	// their token is enforced while nothing they could publish to exists.
	//
	// There is no longer an "off" to fall back to: this listener IS the SRT
	// ingest. An out-of-range port is therefore an error that leaves nothing
	// bound, not a quiet downgrade to per-source ports.
	ok := port >= 1 && port <= 65535
	if !ok {
		m.log.Error("srt ingest not started: listener port out of range", "port", port)
	}
	addr := fmt.Sprintf(":%d", port)

	m.mu.Lock()
	cur, curAddr := m.srt, m.srtAddr
	m.mu.Unlock()

	if cur != nil && (!ok || curAddr != addr) {
		cur.Stop()
		m.mu.Lock()
		m.srt, m.srtAddr = nil, ""
		m.mu.Unlock()
		m.log.Info("one-port srt ingest stopped")
		cur = nil
	}
	if !ok || cur != nil {
		return
	}

	srv := srtserver.New(m.log, addr, m.lookupToken)
	if err := srv.Start(); err != nil {
		// A listener that cannot bind must not take the engines down with it:
		// every source still has its own port, which is the whole reason both
		// addressing modes are kept.
		m.log.Error("one-port srt ingest could not start; per-source ports still work",
			"addr", addr, "err", err)
		return
	}
	m.mu.Lock()
	m.srt, m.srtAddr = srv, addr
	m.mu.Unlock()
}

// lookupToken resolves a publish token to the source that owns it, in constant
// time across every candidate.
//
// The scan covers both the live token and a rotated-out one inside its grace
// window, which is what lets a rotation happen without cutting off an encoder
// already publishing.
func (m *Manager) lookupToken(token string) (srtserver.Target, bool) {
	rows, err := m.store.ListSources()
	if err != nil {
		m.log.Warn("cannot read sources for an srt publish attempt", "err", err)
		return srtserver.Target{}, false
	}
	now := time.Now()
	type key struct {
		id     int64
		backup bool
	}
	targets := make([]srtserver.Target, 0, len(rows))
	tokens := make(map[key][]string, len(rows))
	for _, s := range rows {
		var sink srtserver.Sink
		// A nil *relay.Hub in an interface is not a nil interface, so the engine
		// lookup has to be spelled out rather than assigned straight through --
		// otherwise a source with no engine would present a non-nil Sink and the
		// listener would accept a stream into nothing.
		if eng := m.Engine(s.ID); eng != nil {
			sink = eng.Hub()
		}
		targets = append(targets, srtserver.Target{
			SourceID:   s.ID,
			Name:       s.Name,
			Enabled:    s.Enabled,
			Passphrase: s.Ingest.SRT.Passphrase,
			Sink:       sink,
		})
		valid := s.ValidTokens(now)
		tokens[key{s.ID, false}] = valid

		// The failover standby, addressed by "<token>.backup" on this same
		// listener. Derived rather than stored: one secret per source is one
		// thing to rotate, one thing to leak, and one thing to explain -- and
		// rotating the source's token moves the backup's address with it.
		if eng := m.Engine(s.ID); eng != nil {
			if bh := eng.BackupHub(); bh != nil {
				suffixed := make([]string, 0, len(valid))
				for _, t := range valid {
					suffixed = append(suffixed, t+backupTokenSuffix)
				}
				targets = append(targets, srtserver.Target{
					SourceID:   s.ID,
					Name:       s.Name + " (backup)",
					Enabled:    s.Enabled,
					Passphrase: s.Ingest.SRT.Passphrase,
					Sink:       bh,
					Backup:     true,
				})
				tokens[key{s.ID, true}] = suffixed
			}
		}
	}
	return srtserver.ConstantTimeLookup(
		func() []srtserver.Target { return targets },
		func(t srtserver.Target) []string { return tokens[key{t.SourceID, t.Backup}] },
	)(token)
}

// backupTokenSuffix turns a source's publish token into its standby's address.
// A suffix rather than a second secret: see lookupToken.
const backupTokenSuffix = ".backup"

// SRTLinks reports uplink health for every publisher on the shared listener.
func (m *Manager) SRTLinks() []srtserver.LinkStats {
	m.mu.RLock()
	srv := m.srt
	m.mu.RUnlock()
	if srv == nil {
		return nil
	}
	return srv.Stats()
}

// SharedIngestPublishing reports whether one source has a live publisher on the
// shared listener.
func (m *Manager) SharedIngestPublishing(sourceID int64) bool {
	m.mu.RLock()
	srv := m.srt
	m.mu.RUnlock()
	return srv != nil && srv.Publishing(sourceID)
}

// Reconcile syncs the engine set, then reconciles each engine.
//
// Every engine is reconciled even when one fails, and the first error is
// returned afterwards: stopping at the first failure would leave the
// programmes after it in the map un-reconciled for reasons that have nothing
// to do with them.
func (m *Manager) Reconcile() error {
	if err := m.Sync(); err != nil {
		return err
	}
	// After Sync, so the listener's token lookup can already see an engine for
	// a source that was added in the same reconcile.
	m.reconcileSharedIngest()
	var firstErr error
	for _, eng := range m.Engines() {
		if err := eng.Reconcile(); err != nil {
			m.log.Error("reconcile failed", "source", eng.SourceID(), "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Stop shuts every engine down.
func (m *Manager) Stop() {
	m.mu.Lock()
	if m.srt != nil {
		m.srt.Stop()
		m.srt, m.srtAddr = nil, ""
	}
	engines := make([]*Engine, 0, len(m.engines))
	for _, eng := range m.engines {
		engines = append(engines, eng)
	}
	m.engines = map[int64]*Engine{}
	m.started = false
	m.mu.Unlock()

	for _, eng := range engines {
		eng.Stop()
	}
}

// Engine returns the engine for one source, or nil.
func (m *Manager) Engine(id int64) *Engine {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.engines[id]
}

// Engines returns every running engine in source display order.
func (m *Manager) Engines() []*Engine {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Engine, 0, len(m.engines))
	for _, id := range m.order {
		if eng, ok := m.engines[id]; ok {
			out = append(out, eng)
		}
	}
	// Anything started but not yet in order (a source added between a Sync and
	// a list) still has to appear, or it is invisible until the next reconcile.
	for id, eng := range m.engines {
		found := false
		for _, known := range m.order {
			if known == id {
				found = true
				break
			}
		}
		if !found {
			out = append(out, eng)
		}
	}
	return out
}

// Default is the engine an unscoped API call operates on: the first source in
// display order.
//
// Every endpoint that predates sources routes here, which is what lets the
// existing API and UI keep working untouched while multi-source runs
// underneath. It returns nil when nothing is running, and callers must say so
// rather than dereference it.
func (m *Manager) Default() *Engine {
	if engines := m.Engines(); len(engines) > 0 {
		return engines[0]
	}
	return nil
}

// SetTranscriber applies speech transcription to every engine, now and to any
// engine created later.
func (m *Manager) SetTranscriber(w *transcribe.Tools, modelsDir string, nice func(name string, args []string) (string, []string)) {
	m.mu.Lock()
	m.tw, m.modelsDir, m.nice = w, modelsDir, nice
	m.mu.Unlock()
	for _, eng := range m.Engines() {
		eng.SetTranscriber(w, modelsDir, nice)
	}
}

// LastReload is what each engine's most recent reconcile did, in display order.
//
// One report per engine rather than a merged list: a settings save is
// install-wide, and an operator with three programmes needs to know which one
// lost a destination.
func (m *Manager) LastReload() []ReloadReport {
	engines := m.Engines()
	out := make([]ReloadReport, 0, len(engines))
	for _, eng := range engines {
		out = append(out, eng.LastReload())
	}
	return out
}

// SetAlertRetry applies the alert delivery budget to every engine, now and to
// any engine created later.
//
// Remembered rather than applied once, for the same reason SetTranscriber is:
// engines are created and destroyed as sources come and go, so a value pushed
// only into the engines running at save time is silently lost the moment an
// operator adds a source. That failure is invisible -- the new source's alerts
// simply chase a dead endpoint for a different length of time than every other
// source's -- which makes it exactly the kind worth designing out.
func (m *Manager) SetAlertRetry(attempts int) {
	if attempts <= 0 {
		return
	}
	m.mu.Lock()
	m.alertAttempts = attempts
	m.mu.Unlock()
	for _, eng := range m.Engines() {
		eng.Alerts().SetRetry(attempts)
	}
}

// IngestLive reports whether ANY programme is receiving.
//
// Any rather than all, because this gates the post-production governor's
// yield-to-stream behaviour: one live programme is reason enough to keep heavy
// background work off the CPU, and waiting for every source to go live would
// mean an install with two sources never yields at all.
func (m *Manager) IngestLive() bool {
	for _, eng := range m.Engines() {
		if eng.IngestLive() {
			return true
		}
	}
	return false
}

// GPUBusy reports whether any programme is using the GPU.
func (m *Manager) GPUBusy() bool {
	for _, eng := range m.Engines() {
		if eng.GPUBusy() {
			return true
		}
	}
	return false
}

// SharedIngestListening reports whether the one-port listener is actually bound.
//
// Distinct from the setting: a listener whose port was already taken leaves the
// setting on while enforcing nothing, and the UI has to be able to tell those
// apart before it tells anyone their token protects an ingest.
func (m *Manager) SharedIngestListening() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.srt != nil
}
