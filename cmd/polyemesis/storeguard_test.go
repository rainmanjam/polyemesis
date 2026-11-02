package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// guardRig is a storeGuard with every collaborator recorded: the order things
// happened in, whether the store was closed, and what was logged.
type guardRig struct {
	g *storeGuard

	mu     sync.Mutex
	events []string
	logBuf bytes.Buffer
}

func (r *guardRig) note(e string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *guardRig) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func (r *guardRig) logged() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.logBuf.String()
}

type lockedWriter struct{ r *guardRig }

func (w lockedWriter) Write(p []byte) (int, error) {
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	return w.r.logBuf.Write(p)
}

func newGuardRig(t *testing.T) *guardRig {
	t.Helper()
	r := &guardRig{}
	log := slog.New(slog.NewTextHandler(lockedWriter{r}, nil))
	r.g = newStoreGuard(log, func() error { r.note("close"); return nil })
	return r
}

// attachTier wires a job tier whose wait answers with the given function.
func (r *guardRig) attachTier(wait func(context.Context) bool) {
	r.g.attach(func() { r.note("cancel") }, func(ctx context.Context) bool {
		r.note("wait")
		ok := wait(ctx)
		if ok {
			r.note("stopped")
		}
		return ok
	})
}

// stops answers at once: the tier was already down.
func stops(context.Context) bool { return true }

// neverStops answers only when the budget runs out: a worker that will not
// come down in time.
func neverStops(ctx context.Context) bool { <-ctx.Done(); return false }

func wantEvents(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

// THE ORDERLY STOP: cancel, wait, then -- and only then -- close, once, with
// nothing to warn about. The deferred close afterwards does not stop the tier
// a second time.
func TestStoreGuardOrderlyStopCancelsWaitsThenCloses(t *testing.T) {
	r := newGuardRig(t)
	r.attachTier(stops)

	if !r.g.stopTier(context.Background()) {
		t.Fatal("stopTier reported an overrun for a tier that stopped")
	}
	r.g.close(time.Hour)

	wantEvents(t, r.seen(), "cancel", "wait", "stopped", "close")
	if l := r.logged(); l != "" {
		t.Errorf("an orderly stop logged: %s", l)
	}
}

// THE BUDGET RUNS OUT: the store is NOT closed under a worker that is still
// running, and the operator is told why.
func TestStoreGuardBudgetExhaustedKeepsTheStoreOpenAndSaysSo(t *testing.T) {
	r := newGuardRig(t)
	r.attachTier(neverStops)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the shared shutdown budget, already spent by the engines
	if r.g.stopTier(ctx) {
		t.Fatal("stopTier reported a clean stop with the budget spent and the tier still running")
	}
	// A short budget for the deferred close: it must not wait again at all, and
	// if it does, the second "cancel","wait" below fails this test rather than
	// hanging it on a tier that never stops.
	r.g.close(time.Millisecond)

	wantEvents(t, r.seen(), "cancel", "wait")
	if !strings.Contains(r.logged(), shutdownBudgetSpentMsg) {
		t.Errorf("the overrun was not logged; got %q", r.logged())
	}
}

// THE ERROR PATH: run returns early -- a listener that would not bind -- and
// only the deferred close runs. It must stop the tier itself, in order, before
// closing.
func TestStoreGuardErrorPathStopsTheTierBeforeClosing(t *testing.T) {
	r := newGuardRig(t)
	r.attachTier(stops)

	r.g.close(time.Hour)

	wantEvents(t, r.seen(), "cancel", "wait", "stopped", "close")
}

// THE ERROR PATH, OUT OF TIME: the same warning as the orderly path, and the
// store left open.
func TestStoreGuardErrorPathOverrunLogsTheSameWarning(t *testing.T) {
	r := newGuardRig(t)
	r.attachTier(neverStops)

	r.g.close(time.Millisecond)

	wantEvents(t, r.seen(), "cancel", "wait")
	if !strings.Contains(r.logged(), shutdownBudgetSpentMsg) {
		t.Errorf("the deferred path's overrun was not logged; got %q", r.logged())
	}
}

// THE STORE IS CLOSED ONLY AFTER THE WAIT: while the tier is still coming down,
// close blocks, and the store is untouched until the tier says it is down.
func TestStoreGuardClosesTheStoreOnlyAfterTheTierIsDown(t *testing.T) {
	r := newGuardRig(t)
	release := make(chan struct{})
	waiting := make(chan struct{})
	r.attachTier(func(ctx context.Context) bool {
		close(waiting)
		select {
		case <-release:
			return true
		case <-ctx.Done():
			return false
		}
	})

	closed := make(chan struct{})
	go func() { r.g.close(time.Hour); close(closed) }()
	<-waiting
	for _, e := range r.seen() {
		if e == "close" {
			t.Fatal("the store was closed while the tier was still coming down")
		}
	}
	close(release)
	<-closed
	wantEvents(t, r.seen(), "cancel", "wait", "stopped", "close")
}

// NO TIER: run failed before startPostProd. Nothing to cancel or wait for; the
// store simply closes.
func TestStoreGuardWithNoTierJustCloses(t *testing.T) {
	r := newGuardRig(t)
	r.g.close(time.Hour)
	wantEvents(t, r.seen(), "close")
}
