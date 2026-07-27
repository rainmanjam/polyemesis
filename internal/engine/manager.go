package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/relay"
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

	mu      sync.RWMutex
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
		m.mu.RUnlock()
		if tw != nil {
			eng.SetTranscriber(tw, dir, nice)
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
