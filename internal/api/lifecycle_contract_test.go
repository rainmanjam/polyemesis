package api

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

/* THE TWO PROMISES LifecycleObserver MAKES TO THE ENGINE, NEITHER OF WHICH WAS
 * TESTED ON EITHER SIDE.
 *
 * engine.go states them at the interface:
 *
 *   Observe "MUST NOT BLOCK. This is called from observeLoop, which is the same
 *   goroutine that raises every alert and publishes every webhook for this
 *   programme, on a 2s tick. An implementation enqueues and returns: no HTTP, no
 *   database write, no engine lock, no sleep. A full queue must DROP the event
 *   rather than wait."
 *
 *   Wanted "must be cheap enough to call on every 2s tick -- a cached atomic,
 *   not a query."
 *
 * The implementation obeys both. Nothing checked either, and the existing
 * lifecycle tests call Observe and Wanted only as SETUP for something else --
 * they would all still pass against an implementation that blocked, queried the
 * database, or slept.
 *
 * WHY THESE ARE WORTH A FILE. observeLoop is one goroutine per programme, and
 * everything time-sensitive in it is downstream of this call: alert rules,
 * webhook delivery, the DOWN dwell. An Observe that blocks does not fail --
 * it stalls the loop, and what an operator sees is alerts arriving late, or a
 * webhook for a destination that went down a minute ago, on an install where
 * the lifecycle coordinator is the one component they never touched. Nothing
 * points at the cause.
 *
 * The failure is also arbitrarily bad rather than bounded: a blocking send on a
 * full channel waits for a READER, and the reader is the sweep, which is off
 * doing HTTP to YouTube. So the 2s tick would be held up by somebody else's
 * server -- which is the exact coupling preannounce.go was written to forbid.
 */

// refusingStore fails the test if the coordinator touches the database.
//
// This is the whole mechanism for the "no database write" half of the contract:
// an assertion about what a function does NOT do needs something that objects,
// and lifecycleStore being an interface is what makes that possible.
type refusingStore struct{ t *testing.T }

func (r refusingStore) ListDestinations() ([]*db.Destination, error) {
	r.t.Error("Observe called ListDestinations: the contract is enqueue-and-return, " +
		"and this runs on the goroutine that raises every alert on a 2s tick")
	return nil, nil
}

func (r refusingStore) UpdateLifecycle(int64, func(*db.Destination) bool) (*db.Destination, error) {
	r.t.Error("Observe wrote to the database; the durable answer is the sweep's job")
	return nil, nil
}

func (r refusingStore) GetDestination(int64) (*db.Destination, error) {
	r.t.Error("Observe read a destination row; the event is a WAKEUP, not a query")
	return nil, nil
}

// contractCoordinator builds a coordinator whose store objects to being used
// and whose escalate would fail the test if the edge path called it.
func contractCoordinator(t *testing.T) *lifecycleCoordinator {
	t.Helper()
	return newLifecycleCoordinator(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		refusingStore{t: t},
		oauth.NewSet(),
		func(context.Context, int64) (*db.PlatformAccount, error) {
			t.Error("Observe resolved a token: that is an HTTP-shaped call on the tick goroutine")
			return nil, nil
		},
		func(lifecycleFault) {
			t.Error("Observe escalated a fault; edges only enqueue")
		},
	)
}

func downEvent(id int64, reason string) hooks.Event {
	return hooks.Event{
		Trigger:     hooks.TriggerDestinationDown,
		Reason:      reason,
		Destination: &hooks.DestinationRef{ID: id},
	}
}

func upEvent(id int64) hooks.Event {
	return hooks.Event{
		Trigger:     hooks.TriggerDestinationUp,
		Destination: &hooks.DestinationRef{ID: id},
	}
}

// THE CONTRACT'S FIRST HALF, AND THE ONE WITH TEETH.
//
// The queue is filled to capacity first, so the send in Observe has nowhere to
// go. An implementation that dropped the `default:` branch -- a one-line edit
// that looks like a simplification -- blocks here for as long as the sweep takes
// to drain, which is however long YouTube takes to answer.
func TestObserveDoesNotBlockWhenTheWakeQueueIsFull(t *testing.T) {
	c := contractCoordinator(t)

	// Fill it. Nothing is draining: lifecycleLoop is not running in this test,
	// which is exactly the state a sweep busy on HTTP puts the queue in.
	for i := 0; i < lifecycleWakeQueue; i++ {
		c.wake <- int64(i)
	}
	if len(c.wake) != lifecycleWakeQueue {
		t.Fatalf("fixture: queue holds %d, want it full at %d", len(c.wake), lifecycleWakeQueue)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Observe(downEvent(9999, lifecycleReasonDisabled))
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		// Deliberately generous. This is not a latency measurement -- the
		// distinction being drawn is "returns" versus "waits for a reader", and
		// a blocked send never returns at all, so a loose bound costs nothing
		// and cannot flake on a busy machine.
		t.Fatal("Observe did not return with a full queue: it is waiting for a reader.\n" +
			"That reader is the sweep, which is off doing HTTP to a platform — so the " +
			"2s observeLoop tick, and every alert and webhook downstream of it, is now " +
			"held up by somebody else's server. A full queue must DROP.")
	}

	if got := len(c.wake); got != lifecycleWakeQueue {
		t.Errorf("queue length %d after a drop, want it unchanged at %d",
			got, lifecycleWakeQueue)
	}
}

// The same guarantee under repetition, because a drop that works once and then
// wedges is the shape a flapping destination would actually produce.
func TestObserveKeepsDroppingRatherThanWedging(t *testing.T) {
	c := contractCoordinator(t)
	for i := 0; i < lifecycleWakeQueue; i++ {
		c.wake <- int64(i)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			c.Observe(downEvent(int64(i), lifecycleReasonRemoved))
			c.Observe(upEvent(int64(i)))
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a thousand edges against a full queue did not complete: the drop " +
			"is not holding under repetition, which is what a flapping destination " +
			"produces")
	}
}

// THE OTHER HALF OF "no HTTP, no database write, no engine lock, no sleep".
//
// Every event shape the coordinator recognises, plus the two it ignores, driven
// against a store and a token resolver that fail the test if touched.
func TestObserveTouchesNeitherTheDatabaseNorAToken(t *testing.T) {
	c := contractCoordinator(t)

	for _, ev := range []hooks.Event{
		upEvent(1),
		downEvent(2, lifecycleReasonDisabled),
		downEvent(3, lifecycleReasonRemoved),
		// A CRASH. The most important one to drive here: its handling is a
		// deliberate silence, and a future edit that "helpfully" looked the
		// destination up to decide would put a query on the tick goroutine.
		downEvent(4, "stopped"),
		// A reason no release has invented yet.
		downEvent(5, "something-new"),
		// A trigger that is not an edge at all.
		{Trigger: hooks.TriggerDestinationUp},
	} {
		c.Observe(ev)
	}
}

// An edge with no destination must not panic. observeLoop derives these from a
// snapshot, and a nil here would take down the goroutine that raises every
// alert for the programme.
func TestObserveIgnoresAnEdgeWithNoDestination(t *testing.T) {
	c := contractCoordinator(t)
	c.Observe(hooks.Event{Trigger: hooks.TriggerDestinationDown, Reason: lifecycleReasonDisabled})
	if len(c.wake) != 0 {
		t.Errorf("an edge naming no destination enqueued %d wakeups", len(c.wake))
	}
}

// WANTED IS A CACHED ATOMIC, MEASURED RATHER THAN ASSERTED.
//
// Its comment promises an install with no lifecycle destination "pays for two
// cached lookups and nothing else, no status snapshot and no disk read". A
// coordinator that answered by counting rows would satisfy every other test in
// this package and quietly repeal that for every install on earth.
//
// AllocsPerRun is the mechanical form of "cheap": a query allocates, an atomic
// load does not. Paired with the refusing store, which catches the version that
// queries without allocating.
func TestWantedIsACachedAtomicRatherThanAQuery(t *testing.T) {
	c := contractCoordinator(t)

	if allocs := testing.AllocsPerRun(200, func() { _ = c.Wanted() }); allocs != 0 {
		t.Errorf("Wanted allocates %.0f times per call; it is called on every 2s tick "+
			"for every programme and is promised to be a cached atomic, not a query",
			allocs)
	}

	// And it still answers correctly either side of the tracking edge, so
	// "cheap" was not bought by making it constant.
	if c.Wanted() {
		t.Error("a coordinator tracking nothing reports it wants the engine's edges, " +
			"which repeals observeLoop's no-rules fast path for every install")
	}
	c.mu.Lock()
	c.tracked[1] = lifecycleTarget{}
	c.wanted.Store(len(c.tracked) > 0)
	c.mu.Unlock()
	if !c.Wanted() {
		t.Error("a coordinator with a tracked destination reports it wants nothing, " +
			"so the engine would never deliver the edges that drive it")
	}
}
