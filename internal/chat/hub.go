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
	// retracted counts messages withdrawn after delivery, by a moderator here
	// or on the platform's own dashboard. Worth a counter of its own: a number
	// that climbs while nobody is using the pane's delete button is the sign
	// that moderation is happening somewhere polyemesis cannot see.
	retracted int64

	// automod holds the current moderator generation, or nil when none is
	// wired. Replaced wholesale on SetModerator so a reconfiguration cannot
	// half-apply and so a superseded generation's queued actions are abandoned
	// rather than performed. The Hub acts; it never decides -- see automod.go.
	automod    *automodState
	automodGen uint64

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

// SetRetention changes the stored-history bounds on a running Hub.
//
// Separate from WithRetention because retention is an operator setting now, and
// a setting that only takes effect on restart is one an operator changes, sees
// nothing happen, and changes again. The purge loop reads these under the lock
// on every tick, so the next sweep uses the new values.
//
// A zero or negative age means keep forever, matching RecordingSettings.
// MaxAgeHours. It is a real answer rather than a mistake to guard against: chat
// rows are small, and an operator who wants a permanent moderation record should
// be able to have one.
func (h *Hub) SetRetention(age time.Duration, keep int, purgeEvery time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.retention = age
	if keep >= 0 {
		h.retainKeep = keep
	}
	if purgeEvery > 0 {
		h.purgeEvery = purgeEvery
	}
}

// SetHistory resizes the in-memory ring on a running Hub, keeping the newest
// messages that still fit.
//
// Separate from WithHistory for the same reason SetRetention is separate from
// WithRetention: this is an operator setting now, and one that only takes
// effect on restart is one an operator changes, sees nothing happen, and
// changes again.
//
// The ring is REALLOCATED rather than resliced. Growing by reslicing a
// wrapped ring would silently reinterpret the tail as live entries, and
// shrinking it would leave histAt pointing past the end -- both produce a
// scrollback that is subtly wrong rather than an error anybody notices. Copying
// out in order and starting a fresh ring is O(n) on a bounded n, and it runs
// when an operator saves a form.
//
// Shrinking discards the OLDEST surplus, which is the direction that matches
// what the ring is for: a connecting browser wants the most recent traffic.
func (h *Hub) SetHistory(n int) {
	if n <= 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if n == len(h.history) {
		return
	}

	keep := h.histN
	if keep > n {
		keep = n
	}
	next := make([]Message, n)
	if keep > 0 {
		// The newest `keep` entries, oldest-first, so the copy lands in the
		// same order History reads.
		first := (h.histAt - keep + len(h.history)*2) % len(h.history)
		for i := 0; i < keep; i++ {
			next[i] = h.history[(first+i)%len(h.history)]
		}
	}
	h.history = next
	h.histCap = n
	h.histN = keep
	// A full ring must wrap to 0, not point one past the end.
	h.histAt = keep % n
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
	// The automod worker is not in h.wg -- it belongs to a generation that
	// outlives individual adapters -- so it is stopped explicitly. Without this
	// every closed Hub leaves one goroutine blocked on a channel forever.
	h.closeAutomod()
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

	// AFTER publishing, never before. The message is on screen by the time
	// automod looks at it; a verdict may retract it a moment later. Blocking
	// display on a check -- even a fast one -- is how chat starts feeling
	// broken, and the retraction path exists for exactly this.
	h.checkAutomod(m)
}

// retract removes one message the platform says is gone.
//
// Called on the adapter's goroutine like deliver, and cheap for the same reason.
// Idempotent by construction: a platform may report a deletion more than once,
// and a retraction for a message we never held is the normal case rather than an
// error -- it happens for anything older than the history ring.
//
// The seen-key is deliberately NOT cleared. If the platform re-sends the message
// after announcing its deletion, that is the platform contradicting itself and
// the right answer is to keep ignoring it.
func (h *Hub) retract(r *runner, messageID string) {
	if messageID == "" {
		return
	}
	h.mu.Lock()
	n := h.forgetHistory(func(m Message) bool {
		return m.ID == messageID && m.Platform == r.platform && m.Account == r.account
	})
	h.retracted += int64(n)
	h.mu.Unlock()

	h.removeStored(r.platform, r.account, messageID)
	h.publishRetraction(Retraction{
		Platform: r.platform, Account: r.account, MessageIDs: []string{messageID},
	})
}

// retractUser removes every message from one author, or -- with an empty
// authorID -- clears the whole room.
//
// The platform names a user, not a list of messages, so the message ids are
// resolved HERE from what we are holding. That is the only honest translation
// available: the pane can only remove what it has, and claiming to have removed
// a message that scrolled out of the ring an hour ago would be a lie the UI then
// has to render.
func (h *Hub) retractUser(r *runner, authorID string) {
	h.mu.Lock()
	var ids []string
	h.forgetHistory(func(m Message) bool {
		if m.Platform != r.platform || m.Account != r.account {
			return false
		}
		// An empty authorID is the platform clearing everything.
		if authorID != "" && m.Author.ID != authorID {
			return false
		}
		ids = append(ids, m.ID)
		return true
	})
	h.retracted += int64(len(ids))
	h.mu.Unlock()

	for _, id := range ids {
		h.removeStored(r.platform, r.account, id)
	}
	if len(ids) > 0 {
		h.publishRetraction(Retraction{
			Platform: r.platform, Account: r.account, MessageIDs: ids,
		})
	}
}

// forgetHistory drops every history entry matching drop, preserving order, and
// returns how many went. Caller holds h.mu.
//
// The ring is rebuilt compactly rather than tombstoned, so History stays a
// straight read with no filtering and every caller of it is unaffected.
func (h *Hub) forgetHistory(drop func(Message) bool) int {
	if h.histN == 0 {
		return 0
	}
	kept := make([]Message, 0, h.histN)
	start := (h.histAt - h.histN + len(h.history)*2) % len(h.history)
	removed := 0
	for i := 0; i < h.histN; i++ {
		m := h.history[(start+i)%len(h.history)]
		if drop(m) {
			removed++
			continue
		}
		kept = append(kept, m)
	}
	if removed == 0 {
		return 0
	}
	for i := range h.history {
		h.history[i] = Message{}
	}
	copy(h.history, kept)
	h.histN = len(kept)
	h.histAt = len(kept) % len(h.history)
	return removed
}

// removeStored deletes one message from the durable scrollback, when the store
// can do that. A store that cannot is not an error: retention will age the row
// out, and the live pane has already dropped it.
func (h *Hub) removeStored(p db.Platform, account, messageID string) {
	rm, ok := h.store.(Remover)
	if !ok || h.store == nil {
		return
	}
	if err := rm.DeleteChatMessage(p, account, messageID); err != nil {
		h.log.Debug("chat retraction not applied to the stored scrollback",
			"platform", p, "id", messageID, "err", err)
	}
}

func (h *Hub) publishRetraction(r Retraction) {
	if h.bus != nil {
		h.bus.Publish(events.TypeChatRetract, r)
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
	if err := d.Delete(ctx, messageID); err != nil {
		return err
	}
	// Same path an upstream deletion takes: drop it from the history ring and
	// the stored scrollback, and tell every subscriber.
	//
	// Before this, only the browser that pressed the button removed the message
	// — from its own copy. A second operator watching the same chat, and any
	// overlay fed from the pane, kept showing it. One moderator action should
	// not leave the room in two states.
	h.retract(r, messageID)
	return nil
}

// Hide takes a message off the platform's public feed without destroying it,
// where the platform can do that.
//
// Only Facebook can today. The refusal for everything else names the platform
// and says what it can do instead, the same way Delete's does — a sentence an
// operator can act on beats "unsupported".
func (h *Hub) Hide(ctx context.Context, p db.Platform, account, messageID string, hidden bool) error {
	h.mu.Lock()
	r := h.runners[runnerKey(p, account)]
	h.mu.Unlock()

	if r == nil {
		return fmt.Errorf("chat: %s is not connected", p)
	}
	hd, ok := r.adapter.(Hider)
	if !ok {
		return fmt.Errorf("chat: %s has no way to hide a message without deleting it. "+
			"Delete it instead, or use the %s dashboard", p, p)
	}
	ctx, cancel := context.WithTimeout(ctx, h.sendTimeout)
	defer cancel()
	if err := hd.Hide(ctx, messageID, hidden); err != nil {
		return err
	}
	// Hiding removes it from the pane; unhiding does NOT put it back. The
	// scrollback is not a mirror of the platform and never was -- a message
	// restored on Facebook will simply not reappear here, which is honest about
	// what this server actually knows rather than pretending to a sync it does
	// not have.
	if hidden {
		h.retract(r, messageID)
	}
	return nil
}

// HideLocally removes a message from THIS SERVER only, leaving the platform
// untouched.
//
// The one moderation action that works on every platform, including the ones
// with no moderation API at all, because it asks nobody's permission. It is for
// the case the others cannot serve: something is on the operator's screen — or
// on an overlay fed from it — that they do not want there, on a platform
// polyemesis cannot moderate.
//
// It is NOT moderation and callers must not present it as such. Every viewer on
// the platform still sees the message. The API says so in its response and the
// UI has to repeat it; a control that looks like a delete and is not would be
// worse than having no control at all.
func (h *Hub) HideLocally(p db.Platform, account, messageID string) error {
	if strings.TrimSpace(messageID) == "" {
		return fmt.Errorf("no message id to hide")
	}
	h.mu.Lock()
	r := h.runners[runnerKey(p, account)]
	h.mu.Unlock()

	// Deliberately works whether or not an adapter is attached. A disconnected
	// platform's messages are still on screen, and being unable to clear them
	// because the socket dropped would be the wrong answer.
	if r == nil {
		r = &runner{platform: p, account: account, hub: h}
	}
	h.retract(r, messageID)
	return nil
}

// Ban removes a person from one platform's chat. A zero duration is permanent.
//
// Per-platform and never fan-out, for the same reason Delete is: a user id only
// means something on the platform that issued it, and the same human on two
// platforms is two accounts with no link between them that polyemesis can see.
//
// The messages that person already sent are retracted from the pane too. Every
// platform here does that on its own side -- a ban or timeout clears their
// backlog -- so leaving ours behind would show the operator a room the viewers
// are no longer in.
func (h *Hub) Ban(ctx context.Context, p db.Platform, account, userID string, d time.Duration, reason string) error {
	h.mu.Lock()
	r := h.runners[runnerKey(p, account)]
	h.mu.Unlock()

	if r == nil {
		return fmt.Errorf("chat: %s is not connected", p)
	}
	b, ok := r.adapter.(Banner)
	if !ok {
		return fmt.Errorf("chat: polyemesis cannot ban on %s; use the %s dashboard", p, p)
	}
	ctx, cancel := context.WithTimeout(ctx, h.sendTimeout)
	defer cancel()
	if err := b.Ban(ctx, userID, d, reason); err != nil {
		return err
	}
	h.retractUser(r, userID)
	return nil
}

// Unban lifts a ban or an unexpired timeout.
//
// It does NOT restore the messages that were retracted. They are gone from this
// server's history and nothing re-fetches them; saying otherwise would promise a
// sync that does not exist.
func (h *Hub) Unban(ctx context.Context, p db.Platform, account, userID string) error {
	h.mu.Lock()
	r := h.runners[runnerKey(p, account)]
	h.mu.Unlock()

	if r == nil {
		return fmt.Errorf("chat: %s is not connected", p)
	}
	b, ok := r.adapter.(Banner)
	if !ok {
		return fmt.Errorf("chat: polyemesis cannot lift a ban on %s; use the %s dashboard", p, p)
	}
	ctx, cancel := context.WithTimeout(ctx, h.sendTimeout)
	defer cancel()
	return b.Unban(ctx, userID)
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
	Dropped int64 `json:"dropped"`
	// Retracted counts messages withdrawn AFTER delivery — deleted here, or
	// deleted on the platform's own dashboard and reported back. Worth its own
	// number: one that climbs while nobody is touching the pane's delete button
	// says moderation is happening somewhere polyemesis cannot see.
	Retracted int64 `json:"retracted"`
	Pending   int   `json:"pending"`
	Adapters  int   `json:"adapters"`
}

// Stats snapshots the counters.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Stats{
		Received:  h.received,
		Deduped:   h.deduped,
		Stored:    h.stored,
		Dropped:   h.dropped,
		Retracted: h.retracted,
		Pending:   len(h.pending),
		Adapters:  len(h.runners),
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
	h.mu.Lock()
	purgeEvery := h.purgeEvery
	h.mu.Unlock()
	purge := time.NewTicker(purgeEvery)
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
			// Pick up a changed sweep interval. Without this the ticker keeps
			// the interval it was born with, so SetRetention's purgeEvery would
			// be stored, reported back to the operator, and never used — the
			// worst kind of setting, because it looks applied.
			h.mu.Lock()
			next := h.purgeEvery
			h.mu.Unlock()
			if next != purgeEvery && next > 0 {
				purgeEvery = next
				purge.Reset(next)
			}
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
	// Snapshot under the lock. These used to be written once at construction
	// and read freely; SetRetention made them mutable, which turned this into a
	// data race the moment an operator saved the settings page.
	h.mu.Lock()
	retention, keep := h.retention, h.retainKeep
	store := h.store
	h.mu.Unlock()

	p, ok := store.(Purger)
	// Zero or negative retention means keep forever. Not a guard against a bad
	// value: it is the setting an operator picks when they want a permanent
	// moderation record, and the user card reads this table.
	if !ok || retention <= 0 {
		return
	}
	if _, err := p.PurgeChatMessages(h.now().Add(-retention), keep); err != nil {
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
	return r.adapter.Run(ctx, runnerSink{r: r})
}

// runnerSink is what every adapter is handed. It is a struct rather than a
// SinkFunc because it carries three capabilities now, not one: deliver, retract
// one message, and retract everything from one author.
type runnerSink struct{ r *runner }

func (s runnerSink) Deliver(m Message)           { s.r.hub.deliver(s.r, m) }
func (s runnerSink) Retract(messageID string)    { s.r.hub.retract(s.r, messageID) }
func (s runnerSink) RetractUser(authorID string) { s.r.hub.retractUser(s.r, authorID) }

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
		// Only inside the `live` branch, with Quota, and deliberately so: a
		// viewer count is a claim about right now, and carrying one out of a
		// stopped or failed adapter would put a number beside a pane that is
		// not receiving anything.
		st.Viewers = hl.Viewers
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

// UpdateChatSettings applies channel-wide chat rules on one platform.
//
// Per-platform and not a fan-out, unlike Send. "Slow mode on" means a different
// thing on each platform that has it, only Twitch publishes an API for it at
// all, and quietly applying it to one of four platforms while reporting success
// would be the kind of half-truth this package exists to avoid.
func (h *Hub) UpdateChatSettings(ctx context.Context, p db.Platform, account string, s ChatSettings) error {
	h.mu.Lock()
	r := h.runners[runnerKey(p, account)]
	h.mu.Unlock()

	if r == nil {
		return fmt.Errorf("chat: %s is not connected", p)
	}
	w, ok := r.adapter.(ChatSettingsWriter)
	if !ok {
		return fmt.Errorf("chat: %s publishes no API for slow mode or follower-only chat; "+
			"set it in the %s dashboard", p, p)
	}
	ctx, cancel := context.WithTimeout(ctx, h.sendTimeout)
	defer cancel()
	return w.UpdateChatSettings(ctx, s)
}

// quotaPacer is an adapter that meters itself against a daily API allowance the
// operator can change.
//
// AN INTERFACE RATHER THAN A *YouTubeAdapter type assertion, because YouTube is
// not going to be the only one. Every platform here polls or is pushed to, and
// the ones that poll have a budget; declaring the capability means the next one
// is wired by implementing a method rather than by remembering to add a case to
// a switch in this file. A switch is a place to forget; a method set is not.
type quotaPacer interface {
	SetQuota(units, reserve int)
}

// SetQuota pushes a changed API allowance into every attached adapter that
// paces against one, and reports how many took it.
//
// Adapters that do not implement quotaPacer are skipped and are not an error:
// an IRC connection has no daily allowance to spend, so having nothing to say
// about one is the correct answer rather than a missing case.
//
// The count is returned rather than logged here so the caller can say something
// truthful in the one place that knows whether the operator was watching -- a
// settings save that reaches zero connections is worth a different line from
// one that reaches three.
func (h *Hub) SetQuota(units, reserve int) int {
	if h == nil {
		return 0
	}
	// SNAPSHOT UNDER THE LOCK, PUSH OUTSIDE IT, the shape Send above uses: each
	// adapter takes its own mutex, and holding the Hub's across a call into one
	// would order the two locks here and the other way round anywhere an
	// adapter calls back into the Hub.
	h.mu.Lock()
	rs := make([]*runner, 0, len(h.runners))
	for _, r := range h.runners {
		rs = append(rs, r)
	}
	h.mu.Unlock()

	applied := 0
	for _, r := range rs {
		if p, ok := r.adapter.(quotaPacer); ok {
			p.SetQuota(units, reserve)
			applied++
		}
	}
	return applied
}
