package alerts

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestFlushIsSafeBesideALiveRun pins the exported Flush against a running Run.
//
// The coalescer is single-goroutine by design (see its doc in queue.go): Run's
// for/select is the only thing allowed to touch its maps. Flush is exported --
// "the test and shutdown entry point" -- and internal/api's outbound-alert-body
// shape inspector calls it in a poll loop beside `go n.Run(ctx)`. Before the
// fix Flush walked the coalescer on the caller's goroutine, so that poll raced
// Run's tick-driven flush and its event-driven Add under -race. Flush must hand
// the pass to the run loop while Run is live, and still run it inline when Run
// is not (TestFlushDeliversThroughTheWholePathOnce reads n.send right after).
func TestFlushIsSafeBesideALiveRun(t *testing.T) {
	doer := &fakeDoer{replies: []reply{{status: 200}}}
	rule := testRule(func(r *Rule) { r.DebounceSeconds = 1 })
	n := New(quietLog(), RuleFunc(func() ([]Rule, error) { return []Rule{rule}, nil }),
		WithDoer(doer), WithFlushInterval(time.Millisecond),
		WithRetry(1, time.Millisecond, time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() { defer close(ran); n.Run(ctx) }()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			n.Publish(downEvent("1", time.Now()))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			n.Flush(time.Now().Add(time.Hour))
		}
	}()
	wg.Wait()

	cancel()
	<-ran
}

// TestFlushAfterRunStoppedIsANoOp pins the other half of "shutdown entry
// point": once Run has returned, n.send is closed, so a Flush that still walked
// the coalescer inline would panic with "send on closed channel" the moment a
// delivery came due. It must return without blocking and without sending.
func TestFlushAfterRunStoppedIsANoOp(t *testing.T) {
	rule := testRule(func(r *Rule) { r.DebounceSeconds = 1 })
	n := New(quietLog(), RuleFunc(func() ([]Rule, error) { return []Rule{rule}, nil }),
		WithDoer(&fakeDoer{replies: []reply{{status: 200}}}), WithFlushInterval(time.Hour))

	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() { defer close(ran); n.Run(ctx) }()
	cancel()
	<-ran

	// Run has returned, so the coalescer is nobody's: seeding it directly is
	// the single-goroutine contract, not a breach of it.
	n.co.Add([]Rule{rule}, downEvent("1", base), base)
	n.Flush(base.Add(time.Hour))
}

// flushDeadline bounds every no-deadlock test below. It is a failure budget,
// not a wait: a passing run returns as soon as the work is done.
const flushDeadline = 10 * time.Second

// waitOrDeadlock fails the test if ch is not closed within flushDeadline.
func waitOrDeadlock(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(flushDeadline):
		t.Fatalf("%s did not return within %s: Flush deadlocked", what, flushDeadline)
	}
}

// flushingDoer calls Flush from inside the delivery, which runs on the sender
// goroutine -- the one Run waits for during shutdown before it closes done.
type flushingDoer struct {
	n     *Notifier
	calls atomic.Int32
}

func (d *flushingDoer) Do(*http.Request) (*http.Response, error) {
	d.calls.Add(1)
	// Repeated so that at least one call lands after Run's loop has chosen
	// ctx.Done and stopped serving anything but the drain.
	for i := 0; i < 50; i++ {
		d.n.Flush(time.Now().Add(time.Hour))
	}
	return &http.Response{StatusCode: 200, Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(""))}, nil
}

// TestFlushFromADeliveryDuringShutdownCannotDeadlock: on shutdown Run closes
// send and waits for the sender to drain before it closes done. A Doer that
// calls Flush in that window must not wait on a Run that is waiting on it.
func TestFlushFromADeliveryDuringShutdownCannotDeadlock(t *testing.T) {
	rule := testRule(func(r *Rule) { r.DebounceSeconds = 1 })
	doer := &flushingDoer{}
	n := New(quietLog(), RuleFunc(func() ([]Rule, error) { return []Rule{rule}, nil }),
		WithDoer(doer), WithFlushInterval(time.Hour), WithRetry(1, time.Millisecond, time.Millisecond))
	doer.n = n

	// Before Run: put one due delivery in the send queue, inline.
	n.co.Add([]Rule{rule}, downEvent("1", base), base)
	n.Flush(base.Add(time.Hour))

	// A cancelled ctx makes the sender deliver it on the shutdown drain path.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := make(chan struct{})
	go func() { defer close(ran); n.Run(ctx) }()
	waitOrDeadlock(t, ran, "Run (draining a delivery whose Doer calls Flush)")
	if doer.calls.Load() != 1 {
		t.Fatalf("the Doer ran %d times, want 1: the drain did not deliver", doer.calls.Load())
	}
}

// TestFlushFromRunsOwnGoroutineCannotDeadlock: the rule source is read on
// Run's goroutine during every pass, so a Flush from inside it is a call from
// Run to itself and must not wait for Run to serve it.
func TestFlushFromRunsOwnGoroutineCannotDeadlock(t *testing.T) {
	rule := testRule(func(r *Rule) { r.DebounceSeconds = 1 })
	var n *Notifier
	var armed atomic.Bool
	returned := make(chan struct{})
	n = New(quietLog(), RuleFunc(func() ([]Rule, error) {
		if armed.CompareAndSwap(true, false) {
			n.Flush(time.Now().Add(time.Hour))
			close(returned)
		}
		return []Rule{rule}, nil
	}), WithDoer(&fakeDoer{replies: []reply{{status: 200}}}),
		WithFlushInterval(time.Hour), WithRulesTTL(0))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ran := make(chan struct{})
	go func() { defer close(ran); n.Run(ctx) }()

	armed.Store(true)
	n.Publish(downEvent("1", time.Now())) // Run reads the rules for it
	waitOrDeadlock(t, returned, "Flush from the rule source on Run's goroutine")
	cancel()
	waitOrDeadlock(t, ran, "Run after a re-entrant Flush")
}

// TestFlushFromTheRuleSourceBeforeRunCannotDeadlock: before Run starts Flush
// does the pass inline under runMu and reads the rule source there, so a rule
// source that calls Flush must not wait on the lock its own caller holds.
func TestFlushFromTheRuleSourceBeforeRunCannotDeadlock(t *testing.T) {
	rule := testRule(func(r *Rule) { r.DebounceSeconds = 1 })
	var n *Notifier
	var armed atomic.Bool
	n = New(quietLog(), RuleFunc(func() ([]Rule, error) {
		if armed.CompareAndSwap(true, false) {
			n.Flush(time.Now().Add(time.Hour))
		}
		return []Rule{rule}, nil
	}), WithDoer(&fakeDoer{replies: []reply{{status: 200}}}), WithRulesTTL(0))

	armed.Store(true)
	done := make(chan struct{})
	go func() { defer close(done); n.Flush(time.Now().Add(time.Hour)) }()
	waitOrDeadlock(t, done, "an inline Flush whose rule source calls Flush")
}

// TestARequestQueuedDuringAnInlinePassIsServedBeforeFlushReturns: before Run
// starts, a rule source that calls Flush with a NEWER time from inside the
// outer inline pass cannot take runMu, so it queues. Nobody else will serve
// that queue until Run starts -- possibly never -- so the outer Flush must
// drain it before returning. Here the outer time is too early for the debounce
// and only the inner, later time makes the delivery due.
func TestARequestQueuedDuringAnInlinePassIsServedBeforeFlushReturns(t *testing.T) {
	rule := testRule(func(r *Rule) { r.DebounceSeconds = 1 })
	var n *Notifier
	var armed atomic.Bool
	n = New(quietLog(), RuleFunc(func() ([]Rule, error) {
		if armed.CompareAndSwap(true, false) {
			n.Flush(base.Add(time.Hour))
		}
		return []Rule{rule}, nil
	}), WithDoer(&fakeDoer{replies: []reply{{status: 200}}}), WithRulesTTL(0))

	n.co.Add([]Rule{rule}, downEvent("1", base), base)
	armed.Store(true)
	done := make(chan struct{})
	go func() { defer close(done); n.Flush(base) }() // not yet due on its own
	waitOrDeadlock(t, done, "an inline Flush whose rule source calls Flush")

	select {
	case d := <-n.send:
		if len(d.Items) != 1 {
			t.Fatalf("delivery = %+v, want the one pending subject", d.Items)
		}
	default:
		t.Fatal("the newer Flush requested from inside the inline pass was never served")
	}
}
