package alerts

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeDoer records every attempt and answers from a script.
type fakeDoer struct {
	mu       sync.Mutex
	attempts int
	bodies   []string
	// replies is consumed one per attempt; the last entry repeats.
	replies []reply
}

type reply struct {
	status int
	header http.Header
	err    error
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(req.Body)
	f.bodies = append(f.bodies, string(body))
	i := f.attempts
	f.attempts++
	if i >= len(f.replies) {
		i = len(f.replies) - 1
	}
	r := f.replies[i]
	if r.err != nil {
		return nil, r.err
	}
	h := r.header
	if h == nil {
		h = http.Header{}
	}
	return &http.Response{
		StatusCode: r.status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader("")),
	}, nil
}

func (f *fakeDoer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

func TestNotifierRetriesTransientFailuresAndStopsAtTheBudget(t *testing.T) {
	tests := []struct {
		name         string
		replies      []reply
		attempts     int
		wantAttempts int
		wantErr      bool
		wantWaits    []time.Duration
	}{
		{
			name:         "a 2xx on the first try never retries",
			replies:      []reply{{status: 204}},
			attempts:     4,
			wantAttempts: 1,
		},
		{
			name:         "a transport error is retried and can succeed",
			replies:      []reply{{err: errors.New("connection refused")}, {status: 200}},
			attempts:     4,
			wantAttempts: 2,
			wantWaits:    []time.Duration{time.Second},
		},
		{
			name:         "a 502 is retried up to the budget and then gives up",
			replies:      []reply{{status: 502}},
			attempts:     3,
			wantAttempts: 3,
			wantErr:      true,
			wantWaits:    []time.Duration{time.Second, 2 * time.Second},
		},
		{
			name:         "a 404 is permanent and is not retried",
			replies:      []reply{{status: 404}},
			attempts:     4,
			wantAttempts: 1,
			wantErr:      true,
		},
		{
			name:         "a 401 is permanent too",
			replies:      []reply{{status: 401}},
			attempts:     4,
			wantAttempts: 1,
			wantErr:      true,
		},
		{
			name:         "a 429 honours Retry-After",
			replies:      []reply{{status: 429, header: http.Header{"Retry-After": []string{"5"}}}, {status: 200}},
			attempts:     4,
			wantAttempts: 2,
			wantWaits:    []time.Duration{5 * time.Second},
		},
		{
			name:         "the backoff is capped rather than doubling forever",
			replies:      []reply{{status: 500}},
			attempts:     5,
			wantAttempts: 5,
			wantErr:      true,
			wantWaits:    []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 3 * time.Second},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doer := &fakeDoer{replies: tt.replies}
			var waits []time.Duration
			n := New(quietLog(), RuleFunc(func() ([]Rule, error) { return nil, nil }),
				WithDoer(doer),
				WithRetry(tt.attempts, time.Second, 3*time.Second),
				WithSleep(func(_ context.Context, d time.Duration) bool {
					waits = append(waits, d)
					return true
				}))

			err := n.post(context.Background(), Delivery{
				Rule:  testRule(),
				Items: []Item{{Event: downEvent("1", base), Count: 1, First: base, Last: base}},
			})
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Errorf("post error = %v, wantErr %v", err, tt.wantErr)
			}
			if doer.count() != tt.wantAttempts {
				t.Errorf("attempts = %d, want %d", doer.count(), tt.wantAttempts)
			}
			if len(waits) != len(tt.wantWaits) {
				t.Fatalf("waits = %v, want %v", waits, tt.wantWaits)
			}
			for i, w := range tt.wantWaits {
				if waits[i] != w {
					t.Errorf("wait %d = %v, want %v", i, waits[i], w)
				}
			}
		})
	}
}

func TestNotifierAbandonsRetriesWhenTheContextEnds(t *testing.T) {
	doer := &fakeDoer{replies: []reply{{status: 503}}}
	n := New(quietLog(), RuleFunc(func() ([]Rule, error) { return nil, nil }),
		WithDoer(doer),
		WithRetry(6, time.Second, time.Minute),
		// A shutdown cuts the backoff short rather than holding the process
		// open for the whole retry schedule.
		WithSleep(func(context.Context, time.Duration) bool { return false }))

	if err := n.post(context.Background(), Delivery{Rule: testRule(),
		Items: []Item{{Event: downEvent("1", base)}}}); err == nil {
		t.Fatal("post returned nil, want the last failure")
	}
	if doer.count() != 1 {
		t.Errorf("attempts = %d, want the retry loop to stop after the first", doer.count())
	}
}

// The engine must never be held up by a webhook. Publish is the only method it
// calls, and it has to return whether or not anything is draining the queue.
func TestPublishNeverBlocksEvenWithNothingDraining(t *testing.T) {
	n := New(quietLog(), RuleFunc(func() ([]Rule, error) { return []Rule{testRule()}, nil }))

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < defaultQueueDepth*3; i++ {
			n.Publish(downEvent("1", base))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked with a full queue and no consumer")
	}

	st := n.Stats()
	if st.Dropped == 0 {
		t.Error("a full queue should shed events and count them, not grow without bound")
	}
	if st.Queued == 0 {
		t.Error("nothing was queued at all")
	}
}

func TestPublishRedactsBeforeAnythingElseSeesTheEvent(t *testing.T) {
	n := New(quietLog(), RuleFunc(func() ([]Rule, error) { return []Rule{testRule()}, nil }))
	n.Publish(Event{
		Type: TypeDestinationDown, Severity: SeverityCritical, Key: "d:1",
		Title: "down", Text: "rtmp://host/live2/" + secretKey, At: base,
	})

	got := <-n.events
	if strings.Contains(got.Text, secretKey) {
		t.Errorf("the queued event still carries the stream key: %q", got.Text)
	}
}

func TestPublishGivesAnUnkeyedEventTheCoarsestUsableKey(t *testing.T) {
	n := New(quietLog(), RuleFunc(func() ([]Rule, error) { return nil, nil }))
	n.Publish(Event{Type: TypeDiskLow, Severity: SeverityCritical, Title: "disk", At: base})

	got := <-n.events
	if got.Key != string(TypeDiskLow) {
		t.Errorf("Key = %q, want %q so repeats still coalesce", got.Key, TypeDiskLow)
	}
}

func TestFlushDeliversThroughTheWholePathOnce(t *testing.T) {
	doer := &fakeDoer{replies: []reply{{status: 200}}}
	rule := testRule(func(r *Rule) { r.DebounceSeconds = 1 })
	now := base

	n := New(quietLog(), RuleFunc(func() ([]Rule, error) { return []Rule{rule}, nil }),
		WithDoer(doer), WithClock(func() time.Time { return now }))

	// Feed the coalescer directly, then flush: the run loop's goroutine is not
	// what is under test here.
	n.co.Add([]Rule{rule}, downEvent("1", now), now)
	n.co.Add([]Rule{rule}, downEvent("1", now), now)
	now = base.Add(5 * time.Second)
	n.Flush(now)

	select {
	case d := <-n.send:
		if len(d.Items) != 1 || d.Items[0].Count != 2 {
			t.Fatalf("delivery = %+v, want one subject counted twice", d.Items)
		}
	default:
		t.Fatal("nothing was handed to the sender")
	}
}

func TestTestAlertBypassesTheQueueAndFilters(t *testing.T) {
	doer := &fakeDoer{replies: []reply{{status: 200}}}
	n := New(quietLog(), RuleFunc(func() ([]Rule, error) { return nil, nil }), WithDoer(doer))

	rule := testRule(func(r *Rule) {
		r.Events = []Type{TypeDiskLow}
		r.MinSeverity = SeverityCritical
	})
	if err := n.Test(context.Background(), rule); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if doer.count() != 1 {
		t.Fatalf("attempts = %d, want exactly one POST", doer.count())
	}
	if !strings.Contains(doer.bodies[0], string(TypeTest)) {
		t.Errorf("test payload does not identify itself: %s", doer.bodies[0])
	}
}

func TestCurrentRulesKeepsThePreviousSetWhenTheDatabaseFails(t *testing.T) {
	var fail bool
	now := base
	n := New(quietLog(), RuleFunc(func() ([]Rule, error) {
		if fail {
			return nil, errors.New("database is locked")
		}
		return []Rule{testRule()}, nil
	}), WithClock(func() time.Time { return now }), WithRulesTTL(time.Second))

	if got := n.currentRules(); len(got) != 1 {
		t.Fatalf("first read = %d rules, want 1", len(got))
	}
	fail = true
	now = base.Add(time.Minute)
	if got := n.currentRules(); len(got) != 1 {
		t.Errorf("read after a failure = %d rules, want the cached 1: a database hiccup "+
			"must not be why nobody was told the stream went down", len(got))
	}
}

// The whole path, with a real socket at the end of it: Publish, coalesce,
// encode, POST.
func TestPublishReachesTheEndpointCoalescedAndRedacted(t *testing.T) {
	got := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	rule := Rule{
		ID: 1, Name: "ops", Enabled: true, Format: FormatJSON, URL: srv.URL,
		DebounceSeconds: 1, MinIntervalSeconds: 1,
	}.Normalized()

	n := New(quietLog(), RuleFunc(func() ([]Rule, error) { return []Rule{rule}, nil }),
		WithFlushInterval(10*time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go n.Run(ctx)

	for i := 0; i < 5; i++ {
		n.Publish(Event{
			Type: TypeDestinationDown, Severity: SeverityCritical, Key: "destination:1",
			Title: "Destination down: Twitch",
			Text:  "rtmps://live.twitch.tv/app/" + secretKey + ": Broken pipe",
		})
	}

	select {
	case body := <-got:
		if strings.Contains(body, secretKey) {
			t.Errorf("the delivered payload carried the stream key:\n%s", body)
		}
		if !strings.Contains(body, `"count":5`) {
			t.Errorf("payload did not coalesce the five occurrences:\n%s", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing reached the endpoint within five seconds")
	}

	select {
	case body := <-got:
		t.Errorf("a second message was delivered for the same subject:\n%s", body)
	case <-time.After(300 * time.Millisecond):
	}

	if st := n.Stats(); st.Sent != 1 {
		t.Errorf("Sent = %d, want 1", st.Sent)
	}
}

func TestHasRulesLetsACallerSkipWorkNobodyWouldRead(t *testing.T) {
	var rules []Rule
	reads := 0
	now := base
	n := New(quietLog(), RuleFunc(func() ([]Rule, error) {
		reads++
		return rules, nil
	}), WithClock(func() time.Time { return now }), WithRulesTTL(5*time.Second))

	if n.HasRules() {
		t.Error("HasRules = true with no rules configured")
	}
	// Inside the cache window, so asking repeatedly costs nothing.
	for i := 0; i < 10; i++ {
		n.HasRules()
	}
	if reads != 1 {
		t.Errorf("the rule list was read %d times inside the cache window, want 1", reads)
	}

	rules = []Rule{testRule()}
	now = base.Add(10 * time.Second)
	if !n.HasRules() {
		t.Error("HasRules = false after a rule was added")
	}

	var nilNotifier *Notifier
	if nilNotifier.HasRules() {
		t.Error("HasRules on a nil notifier must be false, not a panic")
	}
}

func TestRunStopsCleanlyWhenTheContextEnds(t *testing.T) {
	doer := &fakeDoer{replies: []reply{{status: 200}}}
	n := New(quietLog(), RuleFunc(func() ([]Rule, error) { return nil, nil }),
		WithDoer(doer), WithFlushInterval(10*time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	go n.Run(ctx)
	time.Sleep(30 * time.Millisecond)
	cancel()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	n.Wait(waitCtx)
	if waitCtx.Err() != nil {
		t.Fatal("Run did not stop within two seconds of the context ending")
	}
}
