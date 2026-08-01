package hooks

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

const (
	// queueDepth is how many deliveries one endpoint may fall behind before
	// they start being dropped. Deep enough to absorb a slow endpoint through a
	// go-live burst (one publish plus one up per destination), shallow enough
	// that a dead endpoint cannot hold megabytes of history.
	queueDepth = 64
	// intakeDepth buffers the hand-off from Publish to the fan-out goroutine.
	intakeDepth = 256
	// reloadEvery is how often the hook list is re-read, so a hook added or
	// edited takes effect without a restart. Same idea as the notifier's rule
	// cache; the interval is longer because nothing here is on a hot path.
	reloadEvery = 5 * time.Second
	// backoffBase and backoffMax bound a retry. Deliberately small: a retry
	// blocks the endpoint's own queue, because ordering is the promise.
	backoffBase = time.Second
	backoffMax  = 8 * time.Second
	// logRing is how many recent deliveries per hook the operator can inspect.
	// This is the answer to "did my hook fire" without a packet capture.
	logRing = 50
	// bodySnippet bounds what a response body contributes to the delivery log.
	bodySnippet = 512
)

// Doer is the HTTP client, narrowed so a test can count attempts without a
// listening socket. Same shape as alerts.Doer, and deliberately a second
// declaration rather than an import: internal/hooks must not depend on
// internal/alerts for anything but redaction and the Snapshot type.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Source supplies the enabled hooks, with their secrets decrypted. An interface
// rather than *db.DB so the package is testable without a database, and so an
// edited hook is picked up without a restart.
type Source interface {
	Hooks() ([]Hook, error)
}

// SourceFunc adapts a function to a Source.
type SourceFunc func() ([]Hook, error)

func (f SourceFunc) Hooks() ([]Hook, error) { return f() }

// Stats is what the dispatcher will admit to.
type Stats struct {
	Queued  int64 `json:"queued"`
	Dropped int64 `json:"dropped"`
	Sent    int64 `json:"sent"`
	Failed  int64 `json:"failed"`
	Retries int64 `json:"retries"`
	// Endpoints is how many workers are running, i.e. enabled hooks.
	Endpoints int       `json:"endpoints"`
	LastSent  time.Time `json:"lastSent,omitempty"`
	// LastError has already been through alerts.Redact.
	LastError string `json:"lastError,omitempty"`
}

// DeliveryRecord is one attempt's outcome, kept in memory so an operator can
// answer "did my hook fire, and what did the endpoint say" without a packet
// capture. Not persisted: this is a debugging aid, and a table of every
// delivery on a busy install is a database that grows without an operator ever
// choosing it.
type DeliveryRecord struct {
	HookID     int64     `json:"hookId"`
	Trigger    Trigger   `json:"trigger"`
	Sequence   uint64    `json:"sequence"`
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	Attempts   int       `json:"attempts"`
	Status     int       `json:"status,omitempty"`
	DurationMS int64     `json:"durationMs"`
	// Error and Response are both redacted; an endpoint that echoes the request
	// back would otherwise put the payload's own free text into the log twice.
	Error    string `json:"error,omitempty"`
	Response string `json:"response,omitempty"`
}

// TestResult is what the test button reports. It is deliberately richer than
// the alert equivalent's "sent": the operator is testing a machine contract, so
// the status code and the body are the answer, not a green tick.
type TestResult struct {
	Status     int    `json:"status"`
	DurationMS int64  `json:"durationMs"`
	Response   string `json:"response,omitempty"`
	Body       string `json:"body"`
	Signature  string `json:"signature"`
}

// Option configures a Dispatcher.
type Option func(*Dispatcher)

// WithDoer replaces the HTTP client.
func WithDoer(d Doer) Option { return func(x *Dispatcher) { x.doer = d } }

// WithClock replaces time.Now, for tests.
func WithClock(fn func() time.Time) Option { return func(x *Dispatcher) { x.now = fn } }

// WithSleep replaces the retry wait. Returning false means the context ended.
func WithSleep(fn func(context.Context, time.Duration) bool) Option {
	return func(x *Dispatcher) { x.sleep = fn }
}

// WithReloadInterval sets how often the hook list is re-read.
func WithReloadInterval(d time.Duration) Option {
	return func(x *Dispatcher) {
		if d > 0 {
			x.reloadEvery = d
		}
	}
}

// worker owns one endpoint: its queue, its goroutine and its sequence number.
//
// One goroutine per hook rather than a shared pool, because ORDER is the
// promise. A pool would deliver "ingest.disconnected" before "ingest.published"
// whenever the first attempt at the earlier one was slower, and an automation
// told the stream is down while it is live is worse than no automation.
type worker struct {
	ch     chan Event
	stop   func()
	done   chan struct{}
	seq    atomic.Uint64
	missed atomic.Uint64

	mu   sync.Mutex
	hook Hook
	log  []DeliveryRecord
}

// Dispatcher delivers events to every subscribed endpoint.
//
// One per process, not one per engine: sequence numbers, the delivery log and
// the worker set all belong to the endpoint, and an endpoint is shared by every
// source. Compare relay.PortAllocator, which is shared for the same class of
// reason.
type Dispatcher struct {
	log         *slog.Logger
	src         Source
	doer        Doer
	now         func() time.Time
	sleep       func(context.Context, time.Duration) bool
	reloadEvery time.Duration

	intake chan Event

	mu      sync.Mutex
	workers map[int64]*worker
	stats   Stats
}

// NewDispatcher builds a dispatcher. It does nothing until Run is called, which
// is what lets main wire it before it has a context.
func NewDispatcher(log *slog.Logger, src Source, opts ...Option) *Dispatcher {
	d := &Dispatcher{
		log:         log,
		src:         src,
		doer:        &http.Client{},
		now:         time.Now,
		sleep:       sleepCtx,
		reloadEvery: reloadEvery,
		intake:      make(chan Event, intakeDepth),
		workers:     map[int64]*worker{},
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Publish accepts a transition. It never blocks and never returns an error.
//
// The caller is the engine's sweep goroutine, which also raises every alert. A
// webhook endpoint that never answers must not be able to stall it -- that is
// the same argument alerts.Notifier.Publish makes, and it is the reason both
// are a non-blocking send onto a bounded queue rather than an HTTP call.
func (d *Dispatcher) Publish(ev Event) {
	if d == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = d.now()
	}
	select {
	case d.intake <- ev.redacted():
		d.bump(func(s *Stats) { s.Queued++ })
	default:
		d.bump(func(s *Stats) { s.Dropped++ })
	}
}

// HasHooks reports whether anybody is listening, so the engine can skip work
// nothing would read.
func (d *Dispatcher) HasHooks() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.workers) > 0
}

// Stats reports what has happened so far.
func (d *Dispatcher) Stats() Stats {
	if d == nil {
		return Stats{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.stats
	out.Endpoints = len(d.workers)
	return out
}

// Deliveries returns one hook's recent attempts, newest last.
func (d *Dispatcher) Deliveries(hookID int64) []DeliveryRecord {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	w := d.workers[hookID]
	d.mu.Unlock()
	if w == nil {
		return []DeliveryRecord{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]DeliveryRecord(nil), w.log...)
}

// Run drives the dispatcher until ctx ends.
func (d *Dispatcher) Run(ctx context.Context) {
	d.reload()
	tick := time.NewTicker(d.reloadEvery)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			d.stopAll()
			return
		case <-tick.C:
			d.reload()
		case ev := <-d.intake:
			d.fanOut(ev)
		}
	}
}

// fanOut hands one event to every subscribed endpoint's queue. Non-blocking per
// endpoint: a full queue means that endpoint is too slow, and the count rides
// out on its next successful delivery as "missed".
func (d *Dispatcher) fanOut(ev Event) {
	d.mu.Lock()
	targets := make([]*worker, 0, len(d.workers))
	for _, w := range d.workers {
		w.mu.Lock()
		wants := w.hook.Wants(ev.Trigger)
		w.mu.Unlock()
		if wants {
			targets = append(targets, w)
		}
	}
	d.mu.Unlock()

	for _, w := range targets {
		select {
		case w.ch <- ev:
		default:
			w.missed.Add(1)
			d.bump(func(s *Stats) { s.Dropped++ })
		}
	}
}

// reload diffs the stored hooks against the running workers.
//
// An EDITED hook keeps its worker, its queue and its sequence number: only the
// value is swapped. Restarting the worker would throw away whatever is queued
// and reset the sequence, so renaming a hook would look to the receiver exactly
// like polyemesis restarting.
func (d *Dispatcher) reload() {
	rows, err := d.src.Hooks()
	if err != nil {
		// Keep the current set. A database hiccup must not be the reason a
		// go-live went unannounced.
		d.log.Warn("cannot read hooks; keeping the running set", "err", err)
		return
	}
	want := make(map[int64]Hook, len(rows))
	for _, h := range rows {
		n := h.Normalized()
		if n.Enabled && n.Validate() == nil {
			want[n.ID] = n
		}
	}

	d.mu.Lock()
	var stopping []*worker
	for id, w := range d.workers {
		if _, keep := want[id]; !keep {
			stopping = append(stopping, w)
			delete(d.workers, id)
		}
	}
	for id, h := range want {
		if w := d.workers[id]; w != nil {
			w.mu.Lock()
			w.hook = h
			w.mu.Unlock()
			continue
		}
		d.workers[id] = d.startWorker(h)
	}
	d.mu.Unlock()

	for _, w := range stopping {
		w.stop()
	}
}

// startWorker spawns one endpoint's goroutine. Called with d.mu held.
func (d *Dispatcher) startWorker(h Hook) *worker {
	ctx, cancel := context.WithCancel(context.Background())
	w := &worker{
		ch:   make(chan Event, queueDepth),
		stop: cancel,
		done: make(chan struct{}),
		hook: h,
	}
	go func() {
		defer close(w.done)
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-w.ch:
				d.deliver(ctx, w, ev)
			}
		}
	}()
	return w
}

func (d *Dispatcher) stopAll() {
	d.mu.Lock()
	all := make([]*worker, 0, len(d.workers))
	for id, w := range d.workers {
		all = append(all, w)
		delete(d.workers, id)
	}
	d.mu.Unlock()
	for _, w := range all {
		w.stop()
	}
}

// deliver builds, signs and posts one envelope, with bounded retries.
func (d *Dispatcher) deliver(ctx context.Context, w *worker, ev Event) {
	w.mu.Lock()
	hook := w.hook
	w.mu.Unlock()

	id, err := deliveryID()
	if err != nil {
		d.log.Warn("cannot mint a delivery id", "err", err)
		return
	}
	env := Envelope{
		SpecVersion: SpecVersion,
		ID:          id,
		Trigger:     ev.Trigger,
		Sequence:    w.seq.Add(1),
		At:          ev.At.UTC(),
		Missed:      w.missed.Swap(0),
		Source:      ev.Source,
		Destination: ev.Destination,
		Reason:      ev.Reason,
		Error:       ev.Error,
	}
	body, err := Encode(env)
	if err != nil {
		d.log.Warn("cannot encode a hook payload", "trigger", ev.Trigger, "err", err)
		return
	}

	started := d.now()
	rec := DeliveryRecord{
		HookID: hook.ID, Trigger: env.Trigger, Sequence: env.Sequence,
		ID: env.ID, At: started,
	}
	for attempt := 1; attempt <= hook.MaxAttempts; attempt++ {
		if attempt > 1 {
			d.bump(func(s *Stats) { s.Retries++ })
		}
		rec.Attempts = attempt
		status, snippet, retry, err := d.attempt(ctx, hook, body, env)
		rec.Status, rec.Response = status, snippet
		if err == nil {
			rec.DurationMS = d.now().Sub(started).Milliseconds()
			sent := d.now()
			d.bump(func(s *Stats) { s.Sent++; s.LastSent = sent; s.LastError = "" })
			w.record(rec)
			return
		}
		rec.Error = alerts.Redact(err.Error())
		if !retry || attempt == hook.MaxAttempts {
			break
		}
		if !d.sleep(ctx, backoffFor(attempt)) {
			break
		}
	}
	rec.DurationMS = d.now().Sub(started).Milliseconds()
	lastErr := rec.Error
	d.bump(func(s *Stats) { s.Failed++; s.LastError = lastErr })
	d.log.Warn("hook delivery failed",
		"hook", hook.Name, "url", hook.RedactedURL(),
		"trigger", env.Trigger, "err", lastErr)
	w.record(rec)
}

// attempt performs one POST. retry reports whether another try could help.
//
// The classification is the same one alerts/notify.go:423 makes, and for the
// same reason: a 404 from a deleted endpoint is permanent, and retrying it only
// delays everything behind it -- which here means everything queued for THIS
// endpoint, because ordering is preserved by never overtaking.
func (d *Dispatcher) attempt(ctx context.Context, h Hook, body []byte, env Envelope) (status int, snippet string, retry bool, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(h.TimeoutSeconds)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		return 0, "", false, err
	}
	ts := d.now().Unix()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "polyemesis")
	req.Header.Set(TimestampHeader, strconv.FormatInt(ts, 10))
	req.Header.Set(TriggerHeader, string(env.Trigger))
	req.Header.Set(DeliveryHeader, env.ID)
	req.Header.Set(SequenceHeader, strconv.FormatUint(env.Sequence, 10))
	if h.Secret != "" {
		req.Header.Set(SignatureHeader, Sign(h.Secret, ts, body))
	}

	resp, err := d.doer.Do(req)
	if err != nil {
		// Nothing was delivered and nothing said it never would be.
		return 0, "", true, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, bodySnippet))
	snippet = alerts.Redact(string(raw))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return resp.StatusCode, snippet, false, nil
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusRequestTimeout,
		resp.StatusCode >= 500:
		return resp.StatusCode, snippet, true, statusError(resp.StatusCode)
	default:
		return resp.StatusCode, snippet, false, statusError(resp.StatusCode)
	}
}

// Test delivers one synthetic envelope to a single endpoint immediately,
// skipping the queue and the subscription filter, and reports what the endpoint
// said.
//
// It returns the body and the signature it sent, not just a verdict. The
// operator is testing a machine contract: "sent" tells them nothing about
// whether their verification code agrees, and this is the whole answer to
// "how do I test a hook without going live".
func (d *Dispatcher) Test(ctx context.Context, h Hook, tr Trigger) (TestResult, error) {
	hook := h.Normalized()
	id, err := deliveryID()
	if err != nil {
		return TestResult{}, err
	}
	if !KnownTrigger(tr) {
		tr = TriggerIngestPublished
	}
	env := Envelope{
		SpecVersion: SpecVersion, ID: id, Trigger: tr, Sequence: 0,
		At: d.now().UTC(), Test: true,
		Source: SourceRef{ID: 0, Name: "test"},
		Reason: "test delivery from polyemesis",
	}
	if tr == TriggerDestinationUp || tr == TriggerDestinationDown {
		env.Destination = &DestinationRef{ID: 0, Name: "Example destination", Platform: "custom"}
	}
	body, err := Encode(env)
	if err != nil {
		return TestResult{}, err
	}
	started := d.now()
	status, snippet, _, err := d.attempt(ctx, hook, body, env)
	res := TestResult{
		Status:     status,
		DurationMS: d.now().Sub(started).Milliseconds(),
		Response:   snippet,
		Body:       string(body),
	}
	if hook.Secret != "" {
		res.Signature = Sign(hook.Secret, d.now().Unix(), body)
	}
	return res, err
}

func (d *Dispatcher) bump(fn func(*Stats)) {
	d.mu.Lock()
	fn(&d.stats)
	d.mu.Unlock()
}

func (w *worker) record(r DeliveryRecord) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.log = append(w.log, r)
	if len(w.log) > logRing {
		w.log = append(w.log[:0:0], w.log[len(w.log)-logRing:]...)
	}
}

// backoffFor is exponential and capped. No jitter: one worker posts to one
// endpoint, so there is no herd to spread out and a deterministic schedule is
// testable.
func backoffFor(attempt int) time.Duration {
	d := backoffBase
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= backoffMax {
			return backoffMax
		}
	}
	return d
}

func deliveryID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

type statusError int

func (e statusError) Error() string { return "endpoint returned HTTP " + strconv.Itoa(int(e)) }

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// sortedIDs exists so the meta endpoint and the tests can list endpoints in a
// stable order.
func sortedIDs(m map[int64]*worker) []int64 {
	out := make([]int64, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
