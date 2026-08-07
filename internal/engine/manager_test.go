package engine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

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

// Neither listener is optional -- each IS the ingest for its protocol -- so the
// thing worth pinning is that both come up on their own, and that a port one of
// them cannot use leaves it down rather than bound to something arbitrary.
func TestBothListenersBindWithoutBeingAskedTo(t *testing.T) {
	m, store := managerFixture(t)
	// Free ports rather than the 6000/1935 defaults: a unit test that binds a
	// well-known port fails whenever anything else on the machine holds it,
	// which makes a real regression indistinguishable from a busy laptop.
	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.Listeners.SRTPort = freeUDPPort(t)
	st.Listeners.RTMPPort = freeTCPPort(t)
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// SRT's listener IS the SRT ingest and binds unconditionally: a source can
	// be pointed at it at any moment.
	if !m.ListenerBound(db.IngestSRT) {
		t.Error("the SRT listener did not bind; every SRT source is unreachable")
	}
	// RTMP's does NOT, and the asymmetry is deliberate. Before the shared
	// listener existed, `ffmpeg -listen 1` bound 1935 only while an RTMP source
	// was configured. Binding it unconditionally would newly expose a port on
	// every install — including a fresh one that has not chosen an ingest mode —
	// for a protocol nothing there speaks. This fixture has no RTMP source.
	if m.ListenerBound(db.IngestRTMP) {
		t.Error("the RTMP listener bound with no RTMP source configured; that is a port nobody asked for")
	}
	// Pull dials out and has no listener to be gated by. Answering yes here
	// would tell an operator a token protects an ingest no publisher reaches.
	if m.ListenerBound(db.IngestPull) {
		t.Error("ListenerBound reported a listener for pull mode, which binds nothing")
	}
}

// freeUDPPort asks the kernel for a port and gives it straight back. Racy in
// principle, fine in practice, and far less racy than a hard-coded 6000.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("no free udp port: %v", err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

// freeTCPPort is the same trick for the RTMP listener, which is TCP.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("no free tcp port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestPortZeroLeavesTheListenerDownRatherThanBoundAtRandom(t *testing.T) {
	m, store := managerFixture(t)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	// Port 0 specifically. The store does not validate -- Settings.Validate
	// runs in the API handler -- and to the kernel :0 is not an error, it means
	// "any free port". Without a guard the listener binds something random,
	// reports itself listening, and tells the operator their tokens are
	// enforced at an address nobody was given.
	//
	// Both protocols, because reconcileListener is written once and
	// instantiated twice: a guard that only ran for one of them is exactly the
	// drift that made it worth writing once.
	st.Listeners.SRTPort = 0
	st.Listeners.RTMPPort = 0
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := m.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if m.ListenerBound(db.IngestSRT) {
		t.Error("the SRT listener bound port 0 to a random ephemeral port and called itself listening")
	}
	if m.ListenerBound(db.IngestRTMP) {
		t.Error("the RTMP listener bound port 0 to a random ephemeral port and called itself listening")
	}
	if got := len(m.Engines()); got == 0 {
		t.Error("a refused listener took the engines down with it")
	}
}

// A port change has to actually rebind. It used to be one listener's code path;
// it is now a shared helper serving two, and a rebind that only worked for one
// protocol would leave an operator's encoder pointed at a port nothing answers
// on while the settings page insists it moved.
func TestChangingAListenerPortRebindsIt(t *testing.T) {
	m, store := managerFixture(t)
	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.Listeners.SRTPort = freeUDPPort(t)
	st.Listeners.RTMPPort = freeTCPPort(t)
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	// The RTMP listener binds only while a source actually uses RTMP, so this
	// test has to ask for one. Without it there is no port to move and the test
	// would assert on a listener that is correctly absent.
	src, err := store.GetSource(1)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	src.Ingest.Mode = db.IngestRTMP
	if err := store.UpdateSource(src); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	moved := freeTCPPort(t)
	st.Listeners.RTMPPort = moved
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := m.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !m.ListenerBound(db.IngestRTMP) {
		t.Fatal("the RTMP listener is down after a port change")
	}
	m.mu.RLock()
	got := m.rtmpAddr
	m.mu.RUnlock()
	if want := fmt.Sprintf(":%d", moved); got != want {
		t.Errorf("rtmp listener address = %q, want %q", got, want)
	}
}

// By the time Start has brought the engines up, the RTMP port must already
// accept connections.
//
// The property, not the call order, but it is the property the call order
// exists for: an RTMP source's ingest child DIALS
// rtmp://127.0.0.1:PORT/live/<token>. Binding after the engines -- which is
// what Start used to do, with a comment about a lookup dependency that never
// existed, since both lookups resolve m.Engine(id) at connect time -- means
// every one of those children gets connection-refused and crash-loops against
// a 500ms backoff. Transient at startup, permanent if the port never binds.
func TestTheRTMPPortAcceptsSubscribersOnceStartReturns(t *testing.T) {
	m, store := managerFixture(t)
	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.Listeners.SRTPort = freeUDPPort(t)
	port := freeTCPPort(t)
	st.Listeners.RTMPPort = port
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	// The ingest children this test is about only exist for an RTMP source, and
	// the listener only binds for one. Both halves of the ordering being tested
	// depend on that, so the fixture has to configure it.
	src, err := store.GetSource(1)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	src.Ingest.Mode = db.IngestRTMP
	if err := store.UpdateSource(src); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		t.Fatalf("an ingest child dialling the listener would be refused: %v", err)
	}
	_ = c.Close()
}
