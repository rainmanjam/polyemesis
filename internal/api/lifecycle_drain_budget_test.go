package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// drainCoordinator is a coordinator whose store ANSWERS. contractCoordinator's
// store refuses every read on purpose -- it exists to prove Observe never
// reads -- and drain does read, so borrowing it would fail for the right
// reason about the wrong function.
func drainCoordinator(t *testing.T) *lifecycleCoordinator {
	t.Helper()
	return newLifecycleCoordinator(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		&fakeLifecycleStore{rows: map[int64]*db.Destination{}},
		oauth.NewSet(),
		func(context.Context, int64) (*db.PlatformAccount, error) { return nil, nil },
		func(lifecycleFault) {},
	)
}

/* THE DRAIN HAS TO STAY INSIDE THE SHUTDOWN'S BUDGET, NOT ITS OWN.
 *
 * Shutdown used to be four budgets that nothing added together, and their sum
 * passed the TimeoutStopSec systemd waits for -- see
 * internal/engine/shutdown_budget.go and #645. The drain was one of the four.
 * It now takes the caller's deadline, and the property that matters is that it
 * honours whichever expires FIRST: a drain that outlived the process budget
 * would eat the engines' share, and the engines are the ones holding an open
 * recording.
 *
 * A nil lifecycle is the other half. DrainLifecycleWithin runs on every
 * shutdown, including a server built without the coordinator, and a panic
 * there would replace a clean stop with a crash at the worst moment.
 */

func TestDrainLifecycleWithinHonoursTheCallersDeadline(t *testing.T) {
	s := &Server{lifecycle: drainCoordinator(t)}

	// Already expired: the drain must return rather than spend its own budget.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.DrainLifecycleWithin(ctx)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("DrainLifecycleWithin ignored an expired parent context. " +
			"On shutdown that means spending the engines' share of the budget, " +
			"and the engines are the ones holding an open recording. See #645.")
	}
}

func TestDrainLifecycleWithinToleratesNoCoordinator(t *testing.T) {
	// Runs on every shutdown, including a server built without a lifecycle. A
	// panic here turns a clean stop into a crash at the worst possible moment.
	s := &Server{}
	s.DrainLifecycleWithin(context.Background())
}

func TestDrainLifecycleUsesItsOwnBudgetWhenNobodyElseHasOne(t *testing.T) {
	// The convenience wrapper kept for callers with no deadline. It must still
	// terminate -- it is what a test or an ad-hoc caller reaches for.
	s := &Server{lifecycle: drainCoordinator(t)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.DrainLifecycle()
	}()
	select {
	case <-done:
	case <-time.After(lifecycleDrainBudget + 2*time.Second):
		t.Fatal("DrainLifecycle did not return inside its own budget")
	}
}

/* A DRAIN THAT ENDED NOTHING MUST SAY SO.
 *
 * Returning fast on an expired parent is correct and #645 is why, so the
 * behaviour above is not the thing to change. What was wrong is that it was
 * SILENT: shutdown holds the cancelled app context and the shutdown budget in
 * scope at once, they are the same type, and the correct one is not the one
 * named `ctx`. Passing the wrong one is a single-token edit, and the only
 * evidence would be broadcasts still live on the platform after a clean stop --
 * discovered on the platform's own dashboard, hours later, by someone who has
 * no reason to connect it to a shutdown that looked fine.
 */
func TestAnExpiredDrainContextIsReported(t *testing.T) {
	var buf bytes.Buffer
	s := &Server{
		lifecycle: drainCoordinator(t),
		log:       slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.DrainLifecycleWithin(ctx)

	if !strings.Contains(buf.String(), "ended nothing") {
		t.Errorf("an expired drain context produced no warning; log was %q", buf.String())
	}
}

/* POSITIVE CONTROL. A warning that fires on every drain is not a signal, and
 * would train the operator to scroll past the one that matters. */
func TestAHealthyDrainReportsNothing(t *testing.T) {
	var buf bytes.Buffer
	s := &Server{
		lifecycle: drainCoordinator(t),
		log:       slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}

	s.DrainLifecycleWithin(context.Background())

	if strings.Contains(buf.String(), "ended nothing") {
		t.Errorf("a drain with a live context warned anyway; log was %q", buf.String())
	}
}
