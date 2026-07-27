package engine

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// The manager owns the multi-source lifecycle: which engines exist, and the one
// piece of state they genuinely share. The FFmpeg path in these fixtures cannot
// exec, so a reconcile logs a failed spawn instead of binding real ports from a
// unit test -- what is under test here is the bookkeeping, not the children.

func managerFixture(t *testing.T) (*Manager, *db.DB) {
	t.Helper()
	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "polyemesis.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	cfg := config.Config{DataDir: dir}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	tools := &ffmpeg.Tools{FFmpeg: filepath.Join(dir, "no-such-ffmpeg"), FFprobe: filepath.Join(dir, "no-such-ffprobe")}
	m := NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), cfg, store, tools, events.NewBroker())
	t.Cleanup(m.Stop)
	return m, store
}

func addSource(t *testing.T, store *db.DB, name string) *db.Source {
	t.Helper()
	s := &db.Source{Name: name, Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := store.CreateSource(s); err != nil {
		t.Fatalf("CreateSource(%s): %v", name, err)
	}
	return s
}

func TestStartBringsUpAnEngineForEverySource(t *testing.T) {
	m, store := managerFixture(t)
	addSource(t, store, "Vertical")

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := len(m.Engines()); got != 2 {
		t.Fatalf("running engines = %d, want 2 (the migrated default plus Vertical)", got)
	}
}

func TestEachEngineOwnsItsOwnSource(t *testing.T) {
	m, store := managerFixture(t)
	vert := addSource(t, store, "Vertical")
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	eng := m.Engine(vert.ID)
	if eng == nil {
		t.Fatal("no engine for the second source")
	}
	if eng.SourceID() != vert.ID {
		t.Errorf("engine reports source %d, want %d", eng.SourceID(), vert.ID)
	}
}

func TestEveryEngineGetsADistinctRelayHub(t *testing.T) {
	m, store := managerFixture(t)
	addSource(t, store, "Vertical")
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// This is the failure the shared allocator exists to prevent. Each engine
	// used to mint its own PortAllocator over the same base and span, which is
	// harmless with one engine and silent corruption with two: both hand out
	// the same relay ports and the second programme's destinations bind onto
	// the first programme's traffic.
	seen := map[int]string{}
	for _, eng := range m.Engines() {
		port := eng.Hub().Port()
		if other, clash := seen[port]; clash {
			t.Fatalf("two engines share relay port %d (%s and source %d): "+
				"one programme's destinations would receive the other's video",
				port, other, eng.SourceID())
		}
		seen[port] = "source"
	}
	if len(seen) != 2 {
		t.Fatalf("collected %d hub ports, want 2", len(seen))
	}
}

func TestSyncStartsAnEngineForASourceAddedAfterStart(t *testing.T) {
	m, store := managerFixture(t)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	before := len(m.Engines())

	// Adding a source through the API and reconciling has to bring a pipeline
	// up; otherwise the row exists and nothing ever runs for it.
	vert := addSource(t, store, "Vertical")
	if err := m.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := len(m.Engines()); got != before+1 {
		t.Fatalf("engines = %d after adding a source, want %d", got, before+1)
	}
	if m.Engine(vert.ID) == nil {
		t.Error("no engine was started for the new source")
	}
}

func TestSyncStopsTheEngineOfADeletedSource(t *testing.T) {
	m, store := managerFixture(t)
	vert := addSource(t, store, "Vertical")
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := store.DeleteSource(vert.ID); err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}
	if err := m.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if m.Engine(vert.ID) != nil {
		t.Error("the deleted source still has a running engine holding its ports")
	}
	if got := len(m.Engines()); got != 1 {
		t.Errorf("engines = %d after a delete, want 1", got)
	}
}

func TestSyncIsIdempotent(t *testing.T) {
	m, store := managerFixture(t)
	addSource(t, store, "Vertical")
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Reconcile runs on every mutation, so repeating it must not accumulate
	// engines or restart the ones already up.
	first := m.Engine(1)
	for i := 0; i < 3; i++ {
		if err := m.Sync(); err != nil {
			t.Fatalf("Sync %d: %v", i, err)
		}
	}
	if got := len(m.Engines()); got != 2 {
		t.Fatalf("engines = %d after repeated syncs, want 2", got)
	}
	if m.Engine(1) != first {
		t.Error("an existing engine was replaced by a no-op sync")
	}
}

func TestDefaultIsTheFirstSourceInDisplayOrder(t *testing.T) {
	m, store := managerFixture(t)
	addSource(t, store, "Vertical")
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	want, err := store.DefaultSourceID()
	if err != nil {
		t.Fatalf("DefaultSourceID: %v", err)
	}
	// Every endpoint that predates sources routes through Default, so it has to
	// agree with the store's idea of which source that is -- otherwise an
	// unscoped API call silently acts on a different programme.
	if got := m.Default(); got == nil || got.SourceID() != want {
		t.Fatalf("Default() = %v, want the engine for source %d", got, want)
	}
}

func TestDefaultIsNilBeforeStart(t *testing.T) {
	m, _ := managerFixture(t)
	// Callers must check rather than dereference: a handler reached before the
	// pipeline exists should say so, not panic.
	if got := m.Default(); got != nil {
		t.Errorf("Default() = %v before Start, want nil", got)
	}
	if got := m.Engines(); len(got) != 0 {
		t.Errorf("Engines() = %d before Start, want none", len(got))
	}
}

func TestStopClearsEveryEngine(t *testing.T) {
	m, store := managerFixture(t)
	addSource(t, store, "Vertical")
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	m.Stop()
	if got := len(m.Engines()); got != 0 {
		t.Errorf("engines = %d after Stop, want 0", got)
	}
	// Stop is called from a signal handler and from t.Cleanup here, so it has
	// to tolerate being called twice.
	m.Stop()
}

func TestIngestLiveAndGPUBusyAggregateAcrossProgrammes(t *testing.T) {
	m, store := managerFixture(t)
	addSource(t, store, "Vertical")
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Nothing is publishing into these fixtures, so both must read false rather
	// than panicking across the engine set. The governor calls these on every
	// job-claim decision.
	if m.IngestLive() {
		t.Error("IngestLive() is true with no ingest running")
	}
	if m.GPUBusy() {
		t.Error("GPUBusy() is true with no rendition running")
	}
}

func TestSharedIngestIsOffUntilSettingsAskForIt(t *testing.T) {
	m, store := managerFixture(t)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// It replaces the ingest path every stream depends on, so it must be opted
	// into rather than inherited by an upgrade.
	if m.SharedIngestListening() {
		t.Fatal("the one-port listener bound without being enabled")
	}

	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	// Port 0 specifically. The store does not validate -- Settings.Validate
	// runs in the API handler -- and to the kernel :0 is not an error, it means
	// "any free port". Without a guard the listener binds something random,
	// reports itself listening, and tells the operator their token is enforced
	// while nothing they could publish to exists.
	st.SharedIngest.Enabled = true
	st.SharedIngest.Port = 0
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := m.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if m.SharedIngestListening() {
		t.Error("the listener bound port 0 to a random ephemeral port and called itself listening")
	}
	if got := len(m.Engines()); got == 0 {
		t.Error("a refused shared listener took the engines down with it")
	}
}
