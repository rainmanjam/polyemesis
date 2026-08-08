package engine

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
)

// Concurrent Syncs must build exactly ONE engine per source.
//
// Sync drops m.mu in the middle on purpose -- building and starting an engine
// is far too slow to hold a lock the status endpoints need -- and that gap was
// the bug. Manager.Reconcile is reached from several HTTP handlers, so two
// passes could both observe a source with no engine, both build and Start one,
// and both write m.engines[id]. The second overwrote the first, and the loser
// stayed RUNNING with its hub, ingest child and relay ports, held by nothing
// that could ever stop it.
func TestConcurrentSyncsBuildOneEnginePerSource(t *testing.T) {
	m, store := managerFixture(t)
	for i := 0; i < 4; i++ {
		addSource(t, store, "src")
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	want := len(m.Engines())

	// A fresh source every caller will race to build.
	addSource(t, store, "contended")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Sync(); err != nil {
				t.Errorf("Sync: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := len(m.Engines()); got != want+1 {
		t.Errorf("engines = %d, want %d", got, want+1)
	}

}

// Concurrent Reconciles go through the same two functions.
func TestConcurrentManagerReconcilesAreSerialised(t *testing.T) {
	m, store := managerFixture(t)
	addSource(t, store, "one")
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	before := len(m.Engines())

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.Reconcile()
		}()
	}
	wg.Wait()

	if got := len(m.Engines()); got != before {
		t.Errorf("engines = %d, want %d: a concurrent reconcile changed the set", got, before)
	}
}

// The serialisation has to be WIRED IN, not merely available.
//
// The tests above assert convergence, and convergence is exactly what a racing
// Sync also produces most of the time -- the loser is invisible in m.engines by
// definition, so asking the map can never prove the leak is gone. The lock
// being taken is the property, so the lock being taken is what is asserted, the
// same way the readiness grace pins its own call site.
func TestSyncAndSharedIngestBothTakeSyncMu(t *testing.T) {
	b, err := os.ReadFile("manager.go")
	if err != nil {
		t.Fatalf("read manager.go: %v", err)
	}
	src := string(b)

	for _, fn := range []string{
		"func (m *Manager) Sync() error {",
		"func (m *Manager) reconcileSharedIngest() {",
	} {
		at := strings.Index(src, fn)
		if at < 0 {
			t.Errorf("cannot find %s", fn)
			continue
		}
		// Within the first few lines of the body, before anything that can
		// block: taking it later leaves exactly the window it exists to close.
		body := src[at:]
		if end := strings.Index(body, "\n}\n"); end > 0 {
			body = body[:end]
		}
		lock := strings.Index(body, "m.syncMu.Lock()")
		if lock < 0 {
			t.Errorf("%s does not take syncMu; two callers can both decide an engine "+
				"is missing and both build one, and the loser stays running with "+
				"nothing able to stop it", fn)
			continue
		}
		if !strings.Contains(body[:lock+64], "defer m.syncMu.Unlock()") {
			t.Errorf("%s takes syncMu without an immediate deferred unlock", fn)
		}
	}
}
