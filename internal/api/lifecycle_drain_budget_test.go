package api

import (
	"context"
	"io"
	"log/slog"
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
