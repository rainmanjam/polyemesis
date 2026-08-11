package alerts

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Notifier delivery defaults. The queue is generous and the sender is not:
// alerts are rare and small, so the depth costs nothing, while a slow endpoint
// must not be given more concurrency to be slow with.
const (
	defaultQueueDepth   = 512
	defaultSendDepth    = 64
	defaultFlushEvery   = 500 * time.Millisecond
	defaultRulesTTL     = 5 * time.Second
	defaultBackoff      = time.Second
	defaultMaxBackoff   = 30 * time.Second
	defaultHTTPTimeout  = 10 * time.Second
	defaultMaxRetryWait = 60 * time.Second
	// shutdownDrain bounds the whole tail of queued deliveries at shutdown,
	// retries included. A shutdown that waits on a webhook is a shutdown that
	// gets killed.
	shutdownDrain = 3 * time.Second
)

// DefaultAttempts is the retry budget a Notifier built with no options uses.
//
// Exported, unlike its neighbours, because db.DefaultSettings has to seed
// AlertSettings.RetryAttempts with the same number and the two would otherwise
// drift silently: an install would run one budget until an operator opened the
// form, then jump to another the moment they saved anything at all.
//
// internal/db cannot import this package to check -- chat and alerts both
// depend on db, so the arrow only points one way -- which is why the guard
// lives in internal/api, where both are already visible.
const DefaultAttempts = 4

// Doer is the HTTP client, narrowed so a test can count attempts without a
// listening socket. *http.Client satisfies it.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// RuleSource supplies the rules. It is an interface rather than *db.DB so the
// whole package is testable without a database, and so the notifier picks up an
// edited rule without being restarted.
type RuleSource interface {
	AlertRules() ([]Rule, error)
}

// RuleFunc adapts a function to a RuleSource.
type RuleFunc func() ([]Rule, error)

func (f RuleFunc) AlertRules() ([]Rule, error) { return f() }

// Stats is what the notifier will admit to.
type Stats struct {
	// Queued is events accepted; Dropped is events refused because the queue
	// was full, which only happens if delivery has stalled for a long time.
	Queued    int64 `json:"queued"`
	Dropped   int64 `json:"dropped"`
	Coalesced int64 `json:"coalesced"`
	Pending   int   `json:"pending"`
	Sent      int64 `json:"sent"`
	Failed    int64 `json:"failed"`
	Retries   int64 `json:"retries"`
	// Deferred counts deliveries handed back because the sender was saturated.
	Deferred  int64     `json:"deferred"`
	LastSent  time.Time `json:"lastSent,omitempty"`
	LastError string    `json:"lastError,omitempty"`
}

// Option configures a Notifier.
type Option func(*Notifier)

// WithDoer replaces the HTTP client.
func WithDoer(d Doer) Option { return func(n *Notifier) { n.doer = d } }

// WithClock replaces time.Now, for tests.
func WithClock(fn func() time.Time) Option { return func(n *Notifier) { n.now = fn } }

// WithSleep replaces the retry wait. It returns false when the context ended,
// which is how a shutdown cuts a backoff short.
func WithSleep(fn func(context.Context, time.Duration) bool) Option {
	return func(n *Notifier) { n.sleep = fn }
}

// WithFlushInterval sets how often the pending set is examined.
func WithFlushInterval(d time.Duration) Option {
	return func(n *Notifier) {
		if d > 0 {
			n.flushEvery = d
		}
	}
}

// WithRetry bounds the retry budget. Bounded is the point: an endpoint that is
// down stays down, and retrying it forever would turn one dead webhook into a
// permanently busy goroutine.
func WithRetry(attempts int, base, max time.Duration) Option {
	return func(n *Notifier) {
		if attempts > 0 {
			n.attempts = attempts
		}
		if base > 0 {
			n.backoff = base
		}
		if max > 0 {
			n.maxBackoff = max
		}
	}
}

// WithRulesTTL sets how long the rule list is cached between database reads.
func WithRulesTTL(d time.Duration) Option {
	return func(n *Notifier) { n.rulesTTL = d }
}

// Notifier accepts events and delivers them.
type Notifier struct {
	log      *slog.Logger
	rules    RuleSource
	doer     Doer
	now      func() time.Time
	sleep    func(context.Context, time.Duration) bool
	timeout  time.Duration
	attempts int

	backoff    time.Duration
	maxBackoff time.Duration
	flushEvery time.Duration
	rulesTTL   time.Duration

	events chan Event
	send   chan Delivery

	// co is touched only by the run loop, so it needs no lock of its own.
	// That is a contract, not an observation -- see the doc on coalescer for
	// what it costs a test to break it.
	co *coalescer

	mu        sync.Mutex
	stats     Stats
	cache     []Rule
	cacheAt   time.Time
	closeOnce sync.Once
	done      chan struct{}
}

// New creates a Notifier. It does nothing until Run is called, which is what
// lets the engine build one before it has a context.
func New(log *slog.Logger, rules RuleSource, opts ...Option) *Notifier {
	n := &Notifier{
		log:        log,
		rules:      rules,
		doer:       &http.Client{Timeout: defaultHTTPTimeout},
		now:        time.Now,
		timeout:    defaultHTTPTimeout,
		attempts:   DefaultAttempts,
		backoff:    defaultBackoff,
		maxBackoff: defaultMaxBackoff,
		flushEvery: defaultFlushEvery,
		rulesTTL:   defaultRulesTTL,
		events:     make(chan Event, defaultQueueDepth),
		send:       make(chan Delivery, defaultSendDepth),
		co:         newCoalescer(),
		done:       make(chan struct{}),
	}
	n.sleep = sleepCtx
	for _, o := range opts {
		o(n)
	}
	return n
}

// Publish accepts an event. It never blocks and never returns an error: the
// caller is the reconcile loop or a supervisor callback, and neither may be
// held up by a webhook endpoint or slowed down by a failure it cannot act on.
func (n *Notifier) Publish(ev Event) {
	if n == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = n.now()
	}
	if ev.Key == "" {
		// Without a key every occurrence is its own subject and nothing
		// coalesces, so give it the coarsest one that is still correct.
		ev.Key = string(ev.Type)
	}
	select {
	case n.events <- ev.Redacted():
		n.bump(func(s *Stats) { s.Queued++ })
	default:
		n.bump(func(s *Stats) { s.Dropped++ })
	}
}

// Run drives the notifier until ctx ends. It owns two goroutines: this one,
// which coalesces, and one sender, which is the only thing that ever waits on
// the network.
func (n *Notifier) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for d := range n.send {
			if ctx.Err() == nil {
				n.deliver(ctx, d)
				continue
			}
			// Shutting down. The queued tail still gets one bounded pass —
			// "the server is going away" is exactly the alert somebody wants —
			// but on a deadline of its own, so a dead endpoint cannot hold the
			// process open.
			drain, cancel := context.WithTimeout(context.Background(), shutdownDrain)
			n.deliver(drain, d)
			cancel()
		}
	}()

	tick := time.NewTicker(n.flushEvery)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			n.closeOnce.Do(func() { close(n.send) })
			wg.Wait()
			close(n.done)
			return
		case ev := <-n.events:
			rules := n.currentRules()
			if took := n.co.Add(rules, ev, n.now()); took > 0 {
				n.bump(func(s *Stats) { s.Coalesced++ })
			}
		case <-tick.C:
			n.flush(n.currentRules(), n.now())
		}
	}
}

// Wait blocks until Run has drained and stopped, so a shutdown can let an
// in-flight alert finish. It returns immediately if Run was never started, and
// respects ctx so it can never become the thing that hangs a shutdown.
func (n *Notifier) Wait(ctx context.Context) {
	if n == nil {
		return
	}
	select {
	case <-n.done:
	case <-ctx.Done():
	}
}

// flush hands every ready delivery to the sender. Anything the sender cannot
// take is put back rather than dropped.
func (n *Notifier) flush(rules []Rule, now time.Time) {
	live := make(map[int64]bool, len(rules))
	for _, r := range rules {
		live[r.ID] = true
	}
	n.co.Forget(live)

	for _, d := range n.co.Due(rules, now) {
		select {
		case n.send <- d:
		default:
			n.co.Requeue(d, now)
			n.bump(func(s *Stats) { s.Deferred++ })
		}
	}
	pending := n.co.Pending()
	n.bump(func(s *Stats) { s.Pending = pending })
}

// Flush is the test and shutdown entry point into one coalescing pass.
func (n *Notifier) Flush(now time.Time) { n.flush(n.currentRules(), now) }

// Test delivers one synthetic event to a single rule immediately, skipping the
// queue, the subscription filter and the debounce.
func (n *Notifier) Test(ctx context.Context, r Rule) error {
	now := n.now()
	ev := Event{
		Type: TypeTest, Severity: SeverityInfo, Key: "test",
		Title: "polyemesis test alert",
		Text:  "If you can read this, " + r.Name + " is wired up correctly.",
		At:    now,
	}.Redacted()
	rule := r.Normalized()
	d := Delivery{Rule: rule, Items: []Item{{Event: ev, Count: 1, First: now, Last: now}}}
	err := n.post(ctx, d)
	if err == nil {
		return nil
	}
	// The same masking deliver applies, because the API handler for this route
	// writes err.Error() into a 502 body and its comment claims "errors out of
	// the notifier are already redacted". Until this was added that claim was
	// FALSE: post hands back the raw *url.Error, whose text is the full rule
	// URL, secret path and all. The route is admin-only, which lowers the
	// severity and does not make an untrue comment acceptable.
	return errors.New(n.endpointSecrets(rule).Scrub(ClientErrorText(err)))
}

// HasRules reports whether anybody is listening.
//
// It exists so a caller can skip building a snapshot nothing would read. The
// answer comes from the same briefly-cached rule list the delivery path uses,
// so asking on a ticker costs one query every rulesTTL rather than one per
// tick.
func (n *Notifier) HasRules() bool {
	if n == nil {
		return false
	}
	return len(n.currentRules()) > 0
}

// Stats reports what has happened so far.
func (n *Notifier) Stats() Stats {
	if n == nil {
		return Stats{}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.stats
}

func (n *Notifier) bump(fn func(*Stats)) {
	n.mu.Lock()
	fn(&n.stats)
	n.mu.Unlock()
}

// currentRules reads the rule list, cached briefly so a burst of events does
// not become a burst of SQLite queries.
//
// A read that fails keeps the previous list rather than falling back to "no
// rules": a database hiccup must not be the reason nobody was told the stream
// went down.
func (n *Notifier) currentRules() []Rule {
	now := n.now()
	n.mu.Lock()
	if !n.cacheAt.IsZero() && now.Sub(n.cacheAt) < n.rulesTTL {
		out := n.cache
		n.mu.Unlock()
		return out
	}
	n.mu.Unlock()

	rows, err := n.rules.AlertRules()
	if err != nil {
		n.log.Warn("cannot read alert rules; keeping the previous set", "err", err)
		n.mu.Lock()
		out := n.cache
		n.mu.Unlock()
		return out
	}
	out := make([]Rule, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Normalized())
	}
	n.mu.Lock()
	n.cache, n.cacheAt = out, now
	n.mu.Unlock()
	return out
}

// deliver sends and records the outcome.
//
// THE ERROR TEXT IS THE SENSITIVE PART OF THIS FUNCTION (#160). Stats.LastError
// is served verbatim at GET /api/v1/alerts/meta, which sits in the ordinary
// authenticated group and is therefore reachable by a READ-SCOPED token. It
// used to be `err.Error()` with no masking at all, and net/http wraps every
// transport failure in a *url.Error whose text is the FULL rule URL -- so a
// single DNS failure published a working Slack webhook secret to a credential
// whose entire promise is that it cannot write anything. Two layers now stand
// between that error and the field, in this order:
//
//  1. ClientErrorText unwraps the *url.Error and rebuilds it with the path
//     masked, keeping the host and the inner wording (x509 vs timeout vs
//     refused) an operator needs to tell the failures apart.
//  2. A SecretSet of this rule's own endpoint literals, for the OTHER shape:
//     an endpoint that quotes the path back inside its own error body, which no
//     wrapper-aware rule can see.
//
// The set is built HERE, from the Rule that was just delivered against, rather
// than cached anywhere. That is deliberate and it is the lesson of the
// destination keep-check: a compiled secret set is only safe if it cannot
// outlive the credential it was compiled from. Deriving it per delivery from
// the same value the request was built with makes staleness unrepresentable
// rather than merely unlikely. A handful of allocations on a path that fires
// when a broadcast fails is not a cost worth reasoning about.
//
// alerts.Redact is NOT the fix here and adding it would be a no-op: it does not
// mask an https path segment. See its doc, limit 2.
func (n *Notifier) deliver(ctx context.Context, d Delivery) {
	err := n.post(ctx, d)
	if err != nil {
		text := n.endpointSecrets(d.Rule).Scrub(ClientErrorText(err))
		n.log.Warn("alert delivery failed",
			"rule", d.Rule.Name, "url", d.Rule.RedactedURL(), "err", text)
		n.bump(func(s *Stats) { s.Failed++; s.LastError = text })
		return
	}
	sent := n.now()
	n.bump(func(s *Stats) { s.Sent++; s.LastSent = sent; s.LastError = "" })
}

// endpointSecrets compiles this rule's own credential literals.
//
// nil logger on purpose: a refusal here is not news. The floor and the
// FFmpeg-vocabulary denylist exist for operator-typed expert arguments, and a
// webhook path shorter than MinSecretLen is not a credential anybody minted.
func (n *Notifier) endpointSecrets(r Rule) *SecretSet {
	return NewSecretSet(nil, EndpointSecrets(r.URL)...)
}

// SetRetry changes the retry budget on a running Notifier.
//
// Separate from WithRetry because the budget is an operator setting now, and a
// setting that only takes effect on restart is one an operator changes, sees
// nothing happen, and changes again.
//
// Only the attempt count moves. The backoff curve was chosen against measured
// behaviour and no failure story argues for exposing it -- see AlertSettings in
// internal/db for why that is a decision rather than an omission.
//
// A delivery already in flight keeps the budget it started with; see post.
func (n *Notifier) SetRetry(attempts int) {
	if n == nil || attempts <= 0 {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.attempts = attempts
}

// retryBudget reads the attempt count under the lock, so one delivery can take
// a stable snapshot of a value a settings save may be changing.
func (n *Notifier) retryBudget() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.attempts
}

// post encodes and delivers with bounded retries.
//
// The retry classification is the whole point: a 404 from a deleted Slack
// webhook is permanent and retrying it four times only delays every alert
// behind it, while a 502 from a reverse proxy is exactly what a retry is for.
func (n *Notifier) post(ctx context.Context, d Delivery) error {
	body, contentType, err := Encode(d)
	if err != nil {
		return err
	}

	// Read ONCE, before the loop. SetRetry can change this from a settings save
	// while a delivery is in flight, and the budget has to be a fixed number for
	// the duration of one delivery or the loop is comparing its counter against
	// a moving target -- lowering it mid-retry would strand an attempt already
	// slept for. Taking a snapshot also keeps the -race detector honest about a
	// field the delivery goroutine would otherwise read unsynchronised.
	budget := n.retryBudget()

	var last error
	for attempt := 1; attempt <= budget; attempt++ {
		if attempt > 1 {
			n.bump(func(s *Stats) { s.Retries++ })
		}
		wait, err := n.attempt(ctx, d.Rule, body, contentType)
		if err == nil {
			return nil
		}
		last = err
		if wait < 0 || attempt == budget {
			return last
		}
		if wait == 0 {
			wait = n.backoffFor(attempt)
		}
		if !n.sleep(ctx, wait) {
			return last
		}
	}
	return last
}

// attempt performs one POST. The returned duration is how long to wait before
// the next try: zero means "use the backoff", negative means "do not retry".
func (n *Notifier) attempt(ctx context.Context, r Rule, body []byte, contentType string) (time.Duration, error) {
	reqCtx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, r.URL, bytes.NewReader(body))
	if err != nil {
		return -1, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "polyemesis")

	resp, err := n.doer.Do(req)
	if err != nil {
		// A transport error is the retryable case by definition: nothing was
		// delivered and nothing said it never would be.
		return 0, err
	}
	defer func() {
		// Drained so the connection can be reused rather than torn down after
		// every alert.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return 0, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return retryAfter(resp.Header.Get("Retry-After")), errStatus(resp.StatusCode)
	case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode >= 500:
		return 0, errStatus(resp.StatusCode)
	default:
		// Anything else is the endpoint saying the request itself is wrong.
		return -1, errStatus(resp.StatusCode)
	}
}

// backoffFor is exponential and capped. No jitter: one rule posts to one
// endpoint at a rate its own MinInterval already bounds, so there is no
// thundering herd to spread out, and a deterministic schedule is testable.
func (n *Notifier) backoffFor(attempt int) time.Duration {
	d := n.backoff
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= n.maxBackoff {
			return n.maxBackoff
		}
	}
	if d > n.maxBackoff {
		return n.maxBackoff
	}
	return d
}

// retryAfter honours the header when it is a sane number of seconds.
//
// Deliberately not clamped down to the local backoff ceiling: Discord and
// Slack use this header to say how hard they are throttling, and ignoring it is
// how a webhook gets blocked outright. It is clamped UP at a minute, so a
// hostile or mistaken value cannot park the sender for an hour.
func retryAfter(h string) time.Duration {
	secs, err := strconv.Atoi(h)
	if err != nil || secs <= 0 {
		return 0
	}
	d := time.Duration(secs) * time.Second
	if d > defaultMaxRetryWait {
		d = defaultMaxRetryWait
	}
	return d
}

type statusError int

func (e statusError) Error() string {
	return "webhook returned HTTP " + strconv.Itoa(int(e))
}

// Status exposes the code so a caller can distinguish a rejection from a
// timeout.
func (e statusError) Status() int { return int(e) }

func errStatus(code int) error { return statusError(code) }

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
