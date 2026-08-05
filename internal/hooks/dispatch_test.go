package hooks

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder is a Doer that records every request and can be made arbitrarily
// slow, which is how the "never blocks" property is proved without a socket.
type recorder struct {
	mu     sync.Mutex
	bodies []string
	seqs   []string
	block  chan struct{}
	status int
}

func (r *recorder) Do(req *http.Request) (*http.Response, error) {
	if r.block != nil {
		<-r.block
	}
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.bodies = append(r.bodies, string(body))
	r.seqs = append(r.seqs, req.Header.Get(SequenceHeader))
	r.mu.Unlock()
	code := r.status
	if code == 0 {
		code = 200
	}
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader("ok")),
		Header:     http.Header{},
	}, nil
}

func (r *recorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bodies...)
}

func oneHook() []Hook {
	return []Hook{Hook{
		ID: 1, Name: "deploy", Enabled: true,
		URL: "https://example.com/h", Secret: "s3cr3t",
	}.Normalized()}
}

func runDispatcher(t *testing.T, d *Dispatcher) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return within 5s of cancellation")
		}
	})
}

// The property the whole design exists to protect. Publish is called from the
// engine's sweep goroutine; if a webhook endpoint that never answers can hold
// it up, one dead URL stalls the loop that also raises every alert.
func TestPublishNeverBlocksOnADeadEndpoint(t *testing.T) {
	rec := &recorder{block: make(chan struct{})}
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return oneHook(), nil }),
		WithDoer(rec), WithReloadInterval(10*time.Millisecond))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Far more than the queue depth, so the drop path is exercised too.
		for i := 0; i < queueDepth*4; i++ {
			d.Publish(Event{Trigger: TriggerIngestPublished, At: time.Now(), Source: SourceRef{ID: 1}})
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked while the endpoint hung; the engine sweep " +
			"would be stalled behind one dead webhook")
	}
	close(rec.block)

	waitFor(t, func() bool { return d.Stats().Dropped > 0 })
}

// Ordering is the promise a script depends on. "disconnected" arriving before
// "published" makes an automation believe the stream is down while it is live.
func TestDeliveriesToOneEndpointKeepTheirOrder(t *testing.T) {
	rec := &recorder{}
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return oneHook(), nil }),
		WithDoer(rec), WithReloadInterval(10*time.Millisecond))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })

	want := []Trigger{
		TriggerIngestPublished, TriggerDestinationUp,
		TriggerDestinationDown, TriggerIngestDisconnected,
	}
	for _, tr := range want {
		d.Publish(Event{Trigger: tr, At: time.Now(), Source: SourceRef{ID: 1}})
	}
	waitFor(t, func() bool { return len(rec.seen()) == len(want) })

	for i, tr := range want {
		if !strings.Contains(rec.seen()[i], `"trigger":"`+string(tr)+`"`) {
			t.Fatalf("delivery %d = %s, want trigger %s", i, rec.seen()[i], tr)
		}
	}
	// And the sequence numbers count from one, so a receiver can spot a gap.
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for i, s := range rec.seqs {
		if s != itoa(i+1) {
			t.Errorf("delivery %d carried sequence %q, want %d", i, s, i+1)
		}
	}
}

// A dropped delivery is admitted on the next one. A receiver that is told it
// missed two events can go and reconcile; one that is not told is quietly wrong
// and has no way to find out.
func TestADroppedDeliveryIsReportedOnTheNextOne(t *testing.T) {
	rec := &recorder{block: make(chan struct{})}
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return oneHook(), nil }),
		WithDoer(rec), WithReloadInterval(10*time.Millisecond))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })

	for i := 0; i < queueDepth*3; i++ {
		d.Publish(Event{Trigger: TriggerIngestPublished, At: time.Now(), Source: SourceRef{ID: 1}})
	}
	waitFor(t, func() bool { return d.Stats().Dropped > 0 })
	close(rec.block)

	waitFor(t, func() bool {
		for _, b := range rec.seen() {
			if strings.Contains(b, `"missed":`) {
				return true
			}
		}
		return false
	})
}

// A 4xx is the endpoint saying the request is wrong. Retrying it four times
// only delays every delivery queued behind it -- and behind it is the whole
// point, because ordering means one endpoint's retries are its own head-of-line
// blocking.
func TestA404IsNotRetried(t *testing.T) {
	rec := &recorder{status: 404}
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return oneHook(), nil }),
		WithDoer(rec), WithReloadInterval(10*time.Millisecond),
		WithSleep(func(context.Context, time.Duration) bool { return true }))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })

	d.Publish(Event{Trigger: TriggerIngestPublished, At: time.Now(), Source: SourceRef{ID: 1}})
	waitFor(t, func() bool { return d.Stats().Failed == 1 })

	if n := len(rec.seen()); n != 1 {
		t.Fatalf("attempted %d times for a 404, want 1", n)
	}
}

func TestA503IsRetried(t *testing.T) {
	rec := &recorder{status: 503}
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return oneHook(), nil }),
		WithDoer(rec), WithReloadInterval(10*time.Millisecond),
		WithSleep(func(context.Context, time.Duration) bool { return true }))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })

	d.Publish(Event{Trigger: TriggerIngestPublished, At: time.Now(), Source: SourceRef{ID: 1}})
	waitFor(t, func() bool { return d.Stats().Failed == 1 })

	if n := len(rec.seen()); n != DefaultAttempts {
		t.Fatalf("attempted %d times for a 503, want %d", n, DefaultAttempts)
	}
}

func TestASubscriptionFilterIsHonoured(t *testing.T) {
	rec := &recorder{}
	narrow := Hook{
		ID: 1, Name: "deploy", Enabled: true, URL: "https://example.com/h",
		Secret: "s", Triggers: []Trigger{TriggerDestinationDown},
	}.Normalized()
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return []Hook{narrow}, nil }),
		WithDoer(rec), WithReloadInterval(10*time.Millisecond))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })

	// Proved by a FENCE, not by a sleep. The earlier version published the
	// unsubscribed event, waited for the subscribed one, then slept 100ms and
	// counted -- so a delivery that merely arrived late left it green, and on a
	// loaded runner "late" is ordinary.
	//
	// Publish, fanOut and the worker queue are each strictly ordered, so once
	// the SECOND subscribed event has been delivered the unsubscribed one
	// between them has already been through fanOut and discarded. Nothing can
	// still be in flight, and no interval has to be guessed.
	//
	// Mutation: fanOut's `wants := w.hook.Wants(ev.Trigger)` -> `wants := true`.
	d.Publish(Event{Trigger: TriggerDestinationDown, At: time.Now(), Source: SourceRef{ID: 1}})
	d.Publish(Event{Trigger: TriggerIngestPublished, At: time.Now(), Source: SourceRef{ID: 1}})
	d.Publish(Event{Trigger: TriggerDestinationDown, At: time.Now(), Source: SourceRef{ID: 2}})
	waitFor(t, func() bool {
		for _, b := range rec.seen() {
			if strings.Contains(b, `"id":2`) {
				return true
			}
		}
		return false
	})

	seen := rec.seen()
	if len(seen) != 2 {
		t.Fatalf("delivered %d, want only the two subscribed triggers: %v", len(seen), seen)
	}
	for i, b := range seen {
		if !strings.Contains(b, `"trigger":"destination.down"`) {
			t.Fatalf("delivery %d was not subscribed to: %s", i, b)
		}
	}
}

// Every delivery is signed with the value a receiver will verify.
func TestEveryDeliveryCarriesAVerifiableSignature(t *testing.T) {
	// Guarded, because the Doer runs on the dispatcher's worker goroutine and
	// these are read from the test's. Capturing them bare is what the whole
	// design makes tempting -- delivery is asynchronous by construction -- and
	// -race catches it immediately.
	var mu sync.Mutex
	var gotSig, gotTS string
	var gotBody []byte
	verify := doerFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		sig := req.Header.Get(SignatureHeader)
		ts := req.Header.Get(TimestampHeader)
		mu.Lock()
		gotBody, gotSig, gotTS = body, sig, ts
		mu.Unlock()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return oneHook(), nil }),
		WithDoer(verify), WithReloadInterval(10*time.Millisecond))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })

	d.Publish(Event{Trigger: TriggerIngestPublished, At: time.Now(), Source: SourceRef{ID: 1}})
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gotSig != ""
	})

	mu.Lock()
	sig, tsRaw, body := gotSig, gotTS, gotBody
	mu.Unlock()

	ts := atoi64(t, tsRaw)
	if want := Sign("s3cr3t", ts, body); sig != want {
		t.Fatalf("signature = %q, want %q -- a receiver verifying this would "+
			"reject every delivery", sig, want)
	}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// waitFor polls rather than sleeping a fixed interval, so the suite is neither
// flaky on a loaded machine nor slow on an idle one.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not reached within 3s")
}

func itoa(i int) string { return strconv.Itoa(i) }

func atoi64(t *testing.T, s string) int64 {
	t.Helper()
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("timestamp header %q is not a number: %v", s, err)
	}
	return v
}
