package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
)

// Hub defaults. The numbers are chosen against chat's actual shape: bursty,
// small, and worthless once it has scrolled away.
const (
	// DefaultHistory is how many messages the in-memory ring holds for a
	// browser that connects mid-broadcast. Bigger than a screenful by an order
	// of magnitude, small enough that four platforms cost well under a
	// megabyte.
	DefaultHistory = 500
	// DefaultFlushEvery and DefaultFlushBatch pace the SQLite writes. Chat
	// arrives in bursts of dozens; one transaction per burst rather than per
	// message is the difference between a write every 20ms and a write every
	// second.
	DefaultFlushEvery = time.Second
	DefaultFlushBatch = 64
	// DefaultPendingCap bounds the unflushed queue. A database that has stopped
	// accepting writes must cost chat its persistence, never its liveness.
	DefaultPendingCap = 5000
	// DefaultDedupe is how many recent message keys are remembered. Redelivery
	// happens on a reconnect or a webhook retry, both of which repeat recent
	// messages; nothing repeats a message from ten thousand ago.
	DefaultDedupe = 4096

	// DefaultBackoff and DefaultMaxBackoff pace reconnection. Thirty seconds is
	// the ceiling because a broadcast is hours long and a chat that takes five
	// minutes to come back has missed the conversation.
	DefaultBackoff    = time.Second
	DefaultMaxBackoff = 30 * time.Second
	// DefaultHealthyFor is how long a connection must last to count as having
	// worked. Anything shorter is a flap and keeps escalating the backoff.
	DefaultHealthyFor = time.Minute

	// DefaultSendTimeout bounds one platform's send inside a fan-out. A
	// platform that has stopped answering must not hold up the reply telling
	// the operator that the other three worked.
	DefaultSendTimeout = 15 * time.Second

	// DefaultRetention and DefaultRetentionKeep bound the stored history. Two
	// hours covers a broadcast's worth of scrollback; the keep floor means a
	// quiet channel still has something to show.
	DefaultRetention     = 2 * time.Hour
	DefaultRetentionKeep = 2000
	DefaultPurgeEvery    = 5 * time.Minute
)

// Hub supervises the adapters and owns everything they must not duplicate:
// reconnection, deduplication, history, persistence and event publishing.
type Hub struct {
	mu      sync.Mutex
	runners map[string]*runner
	closed  bool

	// history is the ring shown to a browser that connects late.
	history []Message
	histAt  int
	histN   int

	// seen deduplicates by Message.Key in arrival order.
	seen     map[string]struct{}
	seenRing []string
	seenAt   int

	pending  []db.ChatMessage
	dropped  int64
	stored   int64
	deduped  int64
	received int64

	store     Store
	bus       Publisher
	log       *slog.Logger
	now       func() time.Time
	sleep     func(context.Context, time.Duration) bool
	backoffFn func(attempt int) time.Duration

	histCap     int
	dedupeCap   int
	pendingCap  int
	flushEvery  time.Duration
	flushBatch  int
	healthyFor  time.Duration
	sendTimeout time.Duration
	retention   time.Duration
	retainKeep  int
	purgeEvery  time.Duration

	stop chan struct{}
	// flushNow lets a burst of chat reach the database without waiting out the
	// tick. Buffered by one and never blocked on: a signal that cannot be
	// delivered means a flush is already pending, which is the same outcome.
	flushNow chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// Option configures a Hub.
type Option func(*Hub)

// WithStore persists the bounded history. Without one the Hub keeps only its
// in-memory ring, which is a perfectly good configuration.
func WithStore(s Store) Option { return func(h *Hub) { h.store = s } }

// WithPublisher wires the WebSocket bus.
func WithPublisher(p Publisher) Option { return func(h *Hub) { h.bus = p } }

// WithLogger replaces the logger.
func WithLogger(l *slog.Logger) Option {
	return func(h *Hub) {
		if l != nil {
			h.log = l
		}
	}
}

// WithClock replaces time.Now, for tests.
func WithClock(fn func() time.Time) Option {
	return func(h *Hub) {
		if fn != nil {
			h.now = fn
		}
	}
}

// WithSleep replaces the backoff wait. It returns false when the context ended,
// which is how a shutdown cuts a reconnect delay short.
func WithSleep(fn func(context.Context, time.Duration) bool) Option {
	return func(h *Hub) {
		if fn != nil {
			h.sleep = fn
		}
	}
}

// WithBackoff replaces the reconnect schedule.
func WithBackoff(fn func(attempt int) time.Duration) Option {
	return func(h *Hub) {
		if fn != nil {
			h.backoffFn = fn
		}
	}
}

// WithHistory sets the in-memory ring size.
func WithHistory(n int) Option {
	return func(h *Hub) {
		if n > 0 {
			h.histCap = n
		}
	}
}

// WithFlush paces persistence.
func WithFlush(every time.Duration, batch int) Option {
	return func(h *Hub) {
		if every > 0 {
			h.flushEvery = every
		}
		if batch > 0 {
			h.flushBatch = batch
		}
	}
}

// WithRetention bounds the stored history: drop messages older than age, but
// always keep the newest keep of them whatever their age.
func WithRetention(age time.Duration, keep int) Option {
	return func(h *Hub) {
		if age > 0 {
			h.retention = age
		}
		if keep >= 0 {
			h.retainKeep = keep
		}
	}
}

// WithSendTimeout bounds one platform's send inside a fan-out.
func WithSendTimeout(d time.Duration) Option {
	return func(h *Hub) {
		if d > 0 {
			h.sendTimeout = d
		}
	}
}

// New creates a Hub and starts its background flusher. Close stops it.
func New(opts ...Option) *Hub {
	h := &Hub{
		runners:     map[string]*runner{},
		seen:        map[string]struct{}{},
		log:         slog.Default(),
		now:         time.Now,
		sleep:       sleepCtx,
		histCap:     DefaultHistory,
		dedupeCap:   DefaultDedupe,
		pendingCap:  DefaultPendingCap,
		flushEvery:  DefaultFlushEvery,
		flushBatch:  DefaultFlushBatch,
		healthyFor:  DefaultHealthyFor,
		sendTimeout: DefaultSendTimeout,
		retention:   DefaultRetention,
		retainKeep:  DefaultRetentionKeep,
		purgeEvery:  DefaultPurgeEvery,
		stop:        make(chan struct{}),
		flushNow:    make(chan struct{}, 1),
	}
	h.backoffFn = h.defaultBackoff
	for _, o := range opts {
		o(h)
	}
	h.history = make([]Message, h.histCap)
	h.seenRing = make([]string, h.dedupeCap)

	h.wg.Add(1)
	go h.background()
	return h
}

// Attach starts an adapter under ctx. Detach or Close ends it early.
//
// One adapter per (platform, account): attaching a second for the same pair is
// refused rather than silently replacing the first, because the usual cause is
// a double-start and the symptom would be every message appearing twice.
func (h *Hub) Attach(ctx context.Context, a Adapter) error {
	if a == nil {
		return fmt.Errorf("chat: nil adapter")
	}
	key := runnerKey(a.Platform(), a.Account())

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return fmt.Errorf("chat: hub is closed")
	}
	if _, dup := h.runners[key]; dup {
		h.mu.Unlock()
		return fmt.Errorf("chat: %s chat is already connected for account %q", a.Platform(), a.Account())
	}
	rctx, cancel := context.WithCancel(ctx)
	r := &runner{
		hub:      h,
		adapter:  a,
		platform: a.Platform(),
		account:  a.Account(),
		cancel:   cancel,
		done:     make(chan struct{}),
		state:    StateConnecting,
		since:    h.now(),
	}
	_, r.canSend = a.(Sender)
	h.runners[key] = r
	h.mu.Unlock()

	go r.loop(rctx)
	h.publishState()
	return nil
}

// Detach stops one adapter and waits for it to finish. Detaching something
// that was never attached is not an error: the caller's intent — "this account
// is not connected" — is already satisfied.
func (h *Hub) Detach(p db.Platform, account string) {
	key := runnerKey(p, account)
	h.mu.Lock()
	r := h.runners[key]
	delete(h.runners, key)
	h.mu.Unlock()

	if r == nil {
		return
	}
	r.cancel()
	<-r.done
	h.publishState()
}

// Adapter returns the attached adapter for one account, so a caller can reach a
// capability the Adapter interface does not carry — Kick's webhook receiver and
// Facebook's live-video id are both set after attach, by whoever learns them.
//
// It exists rather than having the caller keep its own map because the Hub is
// already the thing that knows what is attached, and a second map would go stale
// the first time a runner failed fatally.
func (h *Hub) Adapter(p db.Platform, account string) (Adapter, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.runners[runnerKey(p, account)]
	if !ok {
		return nil, false
	}
	return r.adapter, true
}

// Close stops every adapter, flushes what is pending and releases the
// background goroutine. It is safe to call twice.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	rs := make([]*runner, 0, len(h.runners))
	for _, r := range h.runners {
		rs = append(rs, r)
	}
	h.runners = map[string]*runner{}
	h.mu.Unlock()

	for _, r := range rs {
		r.cancel()
	}
	for _, r := range rs {
		<-r.done
	}
	h.stopOnce.Do(func() { close(h.stop) })
	h.wg.Wait()
	// One last flush after the adapters are done: the messages from the final
	// seconds of a broadcast are the ones somebody scrolls back to.
	h.flush()
}

// ---------------------------------------------------------------- delivery

// deliver is the single path every message takes, whichever adapter produced
// it. It is called on the adapter's own goroutine and must stay cheap.
func (h *Hub) deliver(r *runner, m Message) {
	if m.Platform == "" {
		m.Platform = r.platform
	}
	if m.Account == "" {
		m.Account = r.account
	}
	m = m.Normalise(h.now)
	if m.Text == "" && len(m.Emotes) == 0 {
		// An empty message is a protocol artefact, not something a person
		// said. Dropping it here keeps every renderer from having to.
		return
	}

	h.mu.Lock()
	if h.markSeen(m.Key()) {
		h.deduped++
		h.mu.Unlock()
		return
	}
	h.received++
	h.pushHistory(m)
	h.queue(m)
	h.mu.Unlock()

	r.observe(m)
	if h.bus != nil {
		h.bus.Publish(events.TypeChat, m)
	}
}

// markSeen records a key and reports whether it was already known.
func (h *Hub) markSeen(key string) bool {
	if _, ok := h.seen[key]; ok {
		return true
	}
	if old := h.seenRing[h.seenAt]; old != "" {
		delete(h.seen, old)
	}
	h.seenRing[h.seenAt] = key
	h.seenAt = (h.seenAt + 1) % len(h.seenRing)
	h.seen[key] = struct{}{}
	return false
}

func (h *Hub) pushHistory(m Message) {
	h.history[h.histAt] = m
	h.histAt = (h.histAt + 1) % len(h.history)
	if h.histN < len(h.history) {
		h.histN++
	}
}

// queue adds a message to the unflushed batch, shedding the oldest when the
// database is not keeping up. Dropping the oldest rather than the newest is
// deliberate: the recent conversation is what a late-joining browser wants.
func (h *Hub) queue(m Message) {
	if h.store == nil {
		return
	}
	if len(h.pending) >= h.pendingCap {
		drop := len(h.pending) - h.pendingCap + 1
		h.pending = append(h.pending[:0], h.pending[drop:]...)
		h.dropped += int64(drop)
	}
	h.pending = append(h.pending, ToDB(m))
	if len(h.pending) >= h.flushBatch {
		select {
		case h.flushNow <- struct{}{}:
		default:
		}
	}
}

// History returns the newest limit messages, oldest first. A limit of zero or
// less returns everything held.
func (h *Hub) History(limit int) []Message {
	h.mu.Lock()
	defer h.mu.Unlock()

	n := h.histN
	if limit > 0 && limit < n {
		n = limit
	}
	out := make([]Message, 0, n)
	start := (h.histAt - n + len(h.history)*2) % len(h.history)
	for i := 0; i < n; i++ {
		out = append(out, h.history[(start+i)%len(h.history)])
	}
	return out
}

// ------------------------------------------------------------------ sending

// Send fans one message out to every connected platform.
//
// Every platform is attempted concurrently and reported individually: partial
// failure is the normal case, and collapsing "Twitch and Kick took it, YouTube
// did not" into a single error would either hide two successes or invite a
// retry that double-posts them. The error return is only for a request that
// was never attempted at all.
func (h *Hub) Send(ctx context.Context, text string) ([]SendResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("chat: nothing to send")
	}

	h.mu.Lock()
	rs := make([]*runner, 0, len(h.runners))
	for _, r := range h.runners {
		rs = append(rs, r)
	}
	h.mu.Unlock()

	if len(rs) == 0 {
		return nil, fmt.Errorf("chat: no platform is connected")
	}

	results := make([]SendResult, len(rs))
	var wg sync.WaitGroup
	for i, r := range rs {
		wg.Add(1)
		go func(i int, r *runner) {
			defer wg.Done()
			results[i] = r.send(ctx, text)
		}(i, r)
	}
	wg.Wait()

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Platform != results[j].Platform {
			return results[i].Platform < results[j].Platform
		}
		return results[i].Account < results[j].Account
	})
	return results, nil
}

// Delete removes a message on the platform it came from, where that platform
// supports it. It is per-platform rather than a fan-out because a message id
// only means anything on the platform that issued it.
func (h *Hub) Delete(ctx context.Context, p db.Platform, account, messageID string) error {
	h.mu.Lock()
	r := h.runners[runnerKey(p, account)]
	h.mu.Unlock()

	if r == nil {
		return fmt.Errorf("chat: %s is not connected", p)
	}
	d, ok := r.adapter.(Deleter)
	if !ok {
		return fmt.Errorf("chat: polyemesis cannot delete %s messages; use the %s dashboard", p, p)
	}
	ctx, cancel := context.WithTimeout(ctx, h.sendTimeout)
	defer cancel()
	return d.Delete(ctx, messageID)
}

// ------------------------------------------------------------------- status

// Statuses reports every attached adapter, ordered so the UI does not reshuffle
// its tabs on every refresh.
func (h *Hub) Statuses() []Status {
	h.mu.Lock()
	rs := make([]*runner, 0, len(h.runners))
	for _, r := range h.runners {
		rs = append(rs, r)
	}
	h.mu.Unlock()

	out := make([]Status, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.status())
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Platform != out[j].Platform {
			return out[i].Platform < out[j].Platform
		}
		return out[i].Account < out[j].Account
	})
	return out
}

// Stats is what the Hub will admit to, for a diagnostics panel.
type Stats struct {
	Received int64 `json:"received"`
	Deduped  int64 `json:"deduped"`
	Stored   int64 `json:"stored"`
	// Dropped counts messages shed because persistence fell behind. They were
	// still delivered live; only the scrollback lost them.
	Dropped  int64 `json:"dropped"`
	Pending  int   `json:"pending"`
	Adapters int   `json:"adapters"`
}

// Stats snapshots the counters.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Stats{
		Received: h.received,
		Deduped:  h.deduped,
		Stored:   h.stored,
		Dropped:  h.dropped,
		Pending:  len(h.pending),
		Adapters: len(h.runners),
	}
}

func (h *Hub) publishState() {
	if h.bus == nil {
		return
	}
	h.bus.Publish(events.TypeChatState, h.Statuses())
}

// --------------------------------------------------------------- background

func (h *Hub) background() {
	defer h.wg.Done()
	flush := time.NewTicker(h.flushEvery)
	defer flush.Stop()
	purge := time.NewTicker(h.purgeEvery)
	defer purge.Stop()

	for {
		select {
		case <-h.stop:
			return
		case <-flush.C:
			h.flush()
		case <-h.flushNow:
			h.flush()
		case <-purge.C:
			h.purge()
		}
	}
}

// flush writes the pending batch. A failure is logged and the batch is dropped
// rather than retried forever: chat history is the most expendable data in this
// system, and a database that is refusing writes has a problem that retrying
// chat inserts will not fix.
func (h *Hub) flush() {
	h.mu.Lock()
	if h.store == nil || len(h.pending) == 0 {
		h.mu.Unlock()
		return
	}
	batch := h.pending
	h.pending = nil
	h.mu.Unlock()

	n, err := h.store.AppendChatMessages(batch)

	h.mu.Lock()
	h.stored += int64(n)
	if err != nil {
		h.dropped += int64(len(batch) - n)
	}
	h.mu.Unlock()

	if err != nil {
		h.log.Warn("chat history not persisted", "messages", len(batch), "err", err)
	}
}

func (h *Hub) purge() {
	p, ok := h.store.(Purger)
	if !ok || h.retention <= 0 {
		return
	}
	if _, err := p.PurgeChatMessages(h.now().Add(-h.retention), h.retainKeep); err != nil {
		h.log.Warn("chat history not purged", "err", err)
	}
}

// defaultBackoff doubles up to the ceiling, with jitter.
func (h *Hub) defaultBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 5 {
		attempt = 5
	}
	d := DefaultBackoff << attempt
	if d > DefaultMaxBackoff {
		d = DefaultMaxBackoff
	}
	// Jitter only ever subtracts, so the ceiling stays a ceiling. Spreading the
	// reconnects matters because four adapters knocked off the network together
	// would otherwise retry in lockstep for the rest of the broadcast.
	return d - time.Duration(rand.Int63n(int64(d/5)+1))
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func runnerKey(p db.Platform, account string) string {
	return string(p) + "\x00" + account
}

// ---------------------------------------------------------------- the runner

// runner supervises one adapter. Every adapter gets one, and it is the reason
// YouTube's quota running out cannot stop Twitch: a runner's failures, backoff
// and state are entirely its own, and the only thing it shares with its
// siblings is the Hub's mutex, which is never held across a call into an
// adapter.
type runner struct {
	hub      *Hub
	adapter  Adapter
	platform db.Platform
	account  string
	canSend  bool
	cancel   context.CancelFunc
	done     chan struct{}

	mu        sync.Mutex
	state     State
	detail    string
	since     time.Time
	channel   string
	lastError string
	received  int64
	sent      int64
	restarts  int64
}

func (r *runner) loop(ctx context.Context) {
	defer close(r.done)

	attempt := 0
	for ctx.Err() == nil {
		r.set(StateConnecting, "")
		r.hub.publishState()

		started := r.hub.now()
		err := r.runOnce(ctx)
		lived := r.hub.now().Sub(started)

		if ctx.Err() != nil {
			break
		}
		if IsFatal(err) {
			r.fail(err.Error())
			r.hub.publishState()
			return
		}
		// A connection that lasted counts as having worked, so a channel that
		// goes offline every few hours does not end up on a thirty-second
		// reconnect delay by the end of the day.
		if lived >= r.hub.healthyFor {
			attempt = 0
		}

		wait := r.hub.backoffFn(attempt)
		attempt++
		reason := "the platform ended the chat session"
		if err != nil {
			reason = err.Error()
		}
		r.mu.Lock()
		r.restarts++
		r.mu.Unlock()
		r.set(StateFailed, fmt.Sprintf("%s — reconnecting in %s", reason, wait.Round(time.Second)))
		if err != nil {
			r.setLastError(err.Error())
		}
		r.hub.publishState()
		r.hub.log.Info("chat adapter reconnecting",
			"platform", r.platform, "in", wait.Round(time.Second), "reason", reason)

		if !r.hub.sleep(ctx, wait) {
			break
		}
	}
	r.set(StateStopped, "")
	r.hub.publishState()
}

// runOnce calls the adapter with a recover around it. A panicking adapter must
// cost its own platform a reconnect and nothing else — the alternative is one
// bad frame from one platform taking down the whole chat surface.
func (r *runner) runOnce(ctx context.Context) (err error) {
	defer func() {
		if p := recover(); p != nil {
			r.hub.log.Error("chat adapter panicked",
				"platform", r.platform, "panic", p, "stack", string(debug.Stack()))
			err = fmt.Errorf("the %s chat adapter hit an internal error (%v); it has been restarted", r.platform, p)
		}
	}()
	return r.adapter.Run(ctx, SinkFunc(func(m Message) {
		r.hub.deliver(r, m)
	}))
}

func (r *runner) send(ctx context.Context, text string) SendResult {
	res := SendResult{Platform: r.platform, Account: r.account}

	s, ok := r.adapter.(Sender)
	if !ok {
		res.Skipped = true
		res.Detail = fmt.Sprintf("polyemesis reads %s chat but cannot post to it", r.platform)
		return res
	}

	ctx, cancel := context.WithTimeout(ctx, r.hub.sendTimeout)
	defer cancel()

	echo, err := func() (m Message, err error) {
		defer func() {
			if p := recover(); p != nil {
				r.hub.log.Error("chat send panicked",
					"platform", r.platform, "panic", p, "stack", string(debug.Stack()))
				err = fmt.Errorf("the %s chat adapter hit an internal error while sending", r.platform)
			}
		}()
		return s.Send(ctx, text)
	}()
	if err != nil {
		res.Detail = err.Error()
		r.setLastError(err.Error())
		return res
	}

	res.OK = true
	r.mu.Lock()
	r.sent++
	r.mu.Unlock()

	// A non-zero echo is the platform's own copy of what we just sent, so the
	// operator sees their message in the pane immediately. Platforms that
	// deliver it back through the normal feed return the zero Message instead,
	// and the dedupe key suppresses whichever arrives second.
	if !echo.Zero() {
		echo.Echo = true
		r.hub.deliver(r, echo)
	}
	return res
}

func (r *runner) observe(m Message) {
	r.mu.Lock()
	r.received++
	if m.Channel != "" {
		r.channel = m.Channel
	}
	r.mu.Unlock()
}

func (r *runner) set(state State, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != state {
		r.since = r.hub.now()
	}
	r.state, r.detail = state, detail
}

func (r *runner) fail(detail string) {
	r.set(StateFailed, detail)
	r.setLastError(detail)
}

func (r *runner) setLastError(s string) {
	r.mu.Lock()
	r.lastError = s
	r.mu.Unlock()
}

// status composes the Hub's view with the adapter's own. The adapter wins when
// it has an opinion: only it knows that it is connected but paused on quota, or
// listening but unreachable, and both of those look identical from out here.
func (r *runner) status() Status {
	r.mu.Lock()
	st := Status{
		Platform:  r.platform,
		Account:   r.account,
		Channel:   r.channel,
		State:     r.state,
		Detail:    r.detail,
		Since:     r.since,
		Received:  r.received,
		Sent:      r.sent,
		Restarts:  r.restarts,
		LastError: r.lastError,
		CanSend:   r.canSend,
	}
	live := r.state == StateConnecting || r.state == StateLive || r.state == StateDegraded
	r.mu.Unlock()

	if hr, ok := r.adapter.(Healther); ok && live {
		hl := hr.Health()
		if hl.State != "" {
			st.State = hl.State
		}
		if hl.Detail != "" {
			st.Detail = hl.Detail
		}
		st.Quota = hl.Quota
	}
	return st
}

// ---------------------------------------------------------------- JSON glue

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 || string(b) == "null" {
		return json.RawMessage("[]")
	}
	return b
}

func decodeJSON(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}
