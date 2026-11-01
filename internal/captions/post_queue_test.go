package captions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingPoster struct {
	mu     sync.Mutex
	seqs   []int
	bodies []string
	block  chan struct{}
	err    error
}

func (r *recordingPoster) Post(ctx context.Context, seq int, body string) error {
	if r.block != nil {
		select {
		case <-r.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seqs = append(r.seqs, seq)
	r.bodies = append(r.bodies, body)
	return r.err
}

func (r *recordingPoster) snapshot() ([]int, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.seqs...), append([]string(nil), r.bodies...)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestTheYouTubeCaptionBodyIsATimestampLineThenTheText pins the PROVISIONAL
// format in its one function.
func TestTheYouTubeCaptionBodyIsATimestampLineThenTheText(t *testing.T) {
	at := time.Date(2030, 1, 2, 3, 4, 5, 678_000_000, time.FixedZone("x", 3600))
	got := YouTubeCaptionBody(at, "hello\nworld")
	want := "2030-01-02T02:04:05.678\nhello world\n"
	if got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

// TestAPostQueueSendsEachCueOnceInOrderWithARisingSequence.
func TestAPostQueueSendsEachCueOnceInOrderWithARisingSequence(t *testing.T) {
	withPostSeqSeed(t, 0)
	p := &recordingPoster{}
	q := NewPostQueue(p, 8, time.Second, nil)
	defer q.Close()
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, s := range []string{"one", "two", "three"} {
		q.Enqueue(at, s)
	}
	waitFor(t, func() bool { s, _ := p.snapshot(); return len(s) == 3 })
	seqs, bodies := p.snapshot()
	if seqs[0] != 1 || seqs[1] != 2 || seqs[2] != 3 {
		t.Fatalf("seqs = %v", seqs)
	}
	if !strings.HasSuffix(bodies[2], "\nthree\n") {
		t.Fatalf("bodies = %q", bodies)
	}
}

// TestAFullPostQueueDropsTheOldestCueAndNeverBlocks: a stalled ingest must not
// hold up the router's timers, and the newest text is the one kept.
//
// MUTATION: Enqueue made a blocking send. Observed: FAIL, the test timed out.
func TestAFullPostQueueDropsTheOldestCueAndNeverBlocks(t *testing.T) {
	p := &recordingPoster{block: make(chan struct{})}
	q := NewPostQueue(p, 2, time.Minute, nil)
	at := time.Now()
	q.Enqueue(at, "in flight") // taken by the worker, which then blocks
	waitFor(t, func() bool { return len(q.ch) == 0 })
	done := make(chan struct{})
	go func() {
		for _, s := range []string{"a", "b", "c", "d"} {
			q.Enqueue(at, s)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Enqueue blocked on a full queue")
	}
	if q.Dropped() != 2 {
		t.Fatalf("dropped = %d, want 2", q.Dropped())
	}
	close(p.block)
	waitFor(t, func() bool { s, _ := p.snapshot(); return len(s) == 3 })
	_, bodies := p.snapshot()
	if !strings.Contains(bodies[1], "\nc\n") || !strings.Contains(bodies[2], "\nd\n") {
		t.Fatalf("kept %q, want the newest two (c, d)", bodies)
	}
	q.Close()
}

// TestClosingAPostQueueCancelsTheInFlightPostAndStopsTheWorker.
func TestClosingAPostQueueCancelsTheInFlightPostAndStopsTheWorker(t *testing.T) {
	p := &recordingPoster{block: make(chan struct{})}
	var errs []error
	var mu sync.Mutex
	q := NewPostQueue(p, 2, time.Minute, func(err error) { mu.Lock(); errs = append(errs, err); mu.Unlock() })
	q.Enqueue(time.Now(), "stuck")
	waitFor(t, func() bool { return len(q.ch) == 0 })
	finished := make(chan struct{})
	go func() { q.Close(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not stop a worker blocked in a POST")
	}
	q.Close() // twice is safe
	q.Enqueue(time.Now(), "after close")
	mu.Lock()
	defer mu.Unlock()
	if len(errs) != 0 {
		t.Fatalf("a cancel on Close was reported as a failure: %v", errs)
	}
}

// TestAFailedPostIsReportedWithoutTheURLAndTheNextCueStillGoes, end to end over
// TLS with the real poster.
func TestAFailedPostIsReportedWithoutTheURLAndTheNextCueStillGoes(t *testing.T) {
	withPostSeqSeed(t, 0)
	fake := &fakeIngest{status: http.StatusForbidden}
	srv := httptest.NewTLSServer(fake)
	defer srv.Close()
	const sig = "fixture-queue-sig"
	var mu sync.Mutex
	var errs []error
	q := NewPostQueue(fixtureHostPoster(srv.Client(), signedURL(srv.URL, sig)), 4, 2*time.Second,
		func(err error) { mu.Lock(); errs = append(errs, err); mu.Unlock() })
	defer q.Close()
	q.Enqueue(time.Now(), "first")
	q.Enqueue(time.Now(), "second")
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(errs) == 2 })
	mu.Lock()
	defer mu.Unlock()
	for _, err := range errs {
		if strings.Contains(err.Error(), sig) || strings.Contains(err.Error(), srv.URL) {
			t.Fatalf("error leaks the URL: %v", err)
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.seqs[0] != "1" || fake.seqs[1] != "2" {
		t.Fatalf("seqs = %v", fake.seqs)
	}
}

// withPostSeqSeed pins where a queue's sequence starts, for a test that reads the numbers.
func withPostSeqSeed(t *testing.T, seed int) {
	t.Helper()
	old := postSeqSeed
	postSeqSeed = func() int { return seed }
	oldHigh := postSeqHigh.Swap(0)
	t.Cleanup(func() { postSeqSeed = old; postSeqHigh.Store(oldHigh) })
}

// A queue rebuilt for the same ingestion URL mid-broadcast (a restart, a saved setting) must not send sequence
// numbers the first one already sent. The sequence starts from the clock, so the second queue starts above
// everything the first one reached.
func TestARebuiltPostQueueStartsAboveTheSequenceTheLastOneReached(t *testing.T) {
	first := &recordingPoster{}
	q := NewPostQueue(first, 8, time.Second, nil)
	for i := range 3 {
		q.Enqueue(time.Now(), "line "+string(rune('a'+i)))
	}
	waitFor(t, func() bool { s, _ := first.snapshot(); return len(s) == 3 })
	q.Close()

	second := &recordingPoster{}
	q2 := NewPostQueue(second, 8, time.Second, nil)
	defer q2.Close()
	q2.Enqueue(time.Now(), "after the restart")
	waitFor(t, func() bool { s, _ := second.snapshot(); return len(s) == 1 })
	a, _ := first.snapshot()
	b, _ := second.snapshot()
	if b[0] <= a[len(a)-1] {
		t.Fatalf("the rebuilt queue sent seq %d, not above the %d the last queue reached", b[0], a[len(a)-1])
	}
}

// After a restart nothing in the process remembers the last number, so the clock is what puts the new queue above
// it: the seed rises ten a second, faster than captions are posted.
func TestThePostSequenceSeedRisesWithTheClock(t *testing.T) {
	a := postSeqSeed()
	time.Sleep(250 * time.Millisecond)
	if b := postSeqSeed(); b < a+2 {
		t.Fatalf("seed went from %d to %d in 250 ms, want it to rise with the clock", a, b)
	}
}
