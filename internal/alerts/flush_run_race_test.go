package alerts

import (
	"context"
	"sync"
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
