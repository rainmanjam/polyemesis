// Package activityhub is the one ingestion point for viewer and platform
// activity (internal/activity): follows, subs, tips, raids and the rest.
//
// WHY NOT THE CHAT HUB. chat.Hub.deliver drops a message with no text, persists
// it, keeps history, feeds analytics and runs automod on everything, and its
// runners are keyed by (platform, account), so a second transport for the same
// account (Twitch EventSub beside Twitch IRC) cannot sit beside one. An
// activity is none of those things. Every producer -- a chat adapter through
// chat.ActivityReceiver, an own-transport Source through Hub.Attach, "Send test
// alert" through the API -- therefore ends in Hub.Submit, which does the same
// five things in the same order:
//
//	normalise -> validate -> dedupe -> settings filter -> ring + fan-out
//
// FAN-OUT is three small interfaces this package declares, so it imports none
// of what implements them (the way overlay.Publisher does): the in-process bus
// (the console's /ws and, through eventstream.BridgeBus, the SSE `activity`
// type), the alerts-overlay gate, and the hooks dispatcher's opt-in `activity`
// trigger.
//
// NOTHING IS STORED. The 500-event ring answers GET /api/v1/activity for the
// console feed and a restart empties it. Durable history would be a table, and
// a table is a schema change.
package activityhub

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
)

// Publisher is the in-process event bus: *events.Broker.
type Publisher interface {
	Publish(t events.Type, data any)
}

// Display is the alerts overlay's gate: overlay.ActivityGate. Offer must not
// block; the gate decides what of the event may go on air.
type Display interface { // NOSONAR godre:S8196: the implementer is the alerts overlay gate that receives Offer; "Offerer" would name the hub that makes the offer, the reverse of the role
	Offer(ev activity.Event)
}

// Dispatcher is the webhook dispatcher: *hooks.Dispatcher. It sends the event to
// hooks that name the `activity` trigger and to no one else.
type Dispatcher interface {
	Activity(ev activity.Event)
}

// Hub bounds.
const (
	// DefaultDedupe is the bounded seen-set's capacity: a copy of the chat
	// hub's (chat.DefaultDedupe), because an at-least-once platform and a
	// reconnect overlap deliver the same event twice and four thousand is far
	// more than a stream's worth.
	DefaultDedupe = 4096
	// DefaultRing is how many events GET /api/v1/activity can return.
	DefaultRing = 500
	// settingsTTL is how long the settings are trusted. A raid of followers
	// would otherwise read and decode the settings blob once per follow.
	settingsTTL = 2 * time.Second
)

// Reasons Submit gives for not delivering an event.
const (
	ReasonInvalid   = "invalid"
	ReasonDuplicate = "duplicate"
	ReasonDisabled  = "disabled"
)

// Result is what became of a submitted event.
type Result struct {
	Accepted bool
	// Reason is empty when accepted; otherwise one of the Reason* words.
	Reason string
	// Detail is the sentence for a human: why it was invalid, or which setting
	// turned it off.
	Detail string
	// Event is the normalised event, valid when Accepted.
	Event activity.Event
}

// Stats counts what the hub did, for GET /api/v1/activity/sources.
type Stats struct {
	Received  int64 `json:"received"`
	Delivered int64 `json:"delivered"`
	Invalid   int64 `json:"invalid"`
	Deduped   int64 `json:"deduped"`
	Disabled  int64 `json:"disabled"`
}

// Option tunes a Hub.
type Option func(*Hub)

// WithBus sets the bus the console and the events stream read.
func WithBus(b Publisher) Option { return func(h *Hub) { h.bus = b } }

// WithDisplay sets the alerts overlay's gate.
func WithDisplay(d Display) Option { return func(h *Hub) { h.display = d } }

// WithHooks sets the webhook dispatcher.
func WithHooks(k Dispatcher) Option { return func(h *Hub) { h.hooks = k } }

// WithSettings sets where the activity settings come from. Read at most once
// every couple of seconds. A read that fails keeps the LAST GOOD settings rather
// than falling back to defaults: a database hiccup must not quietly turn a
// switched-off activity, or a switched-off overlay message, back on.
func WithSettings(fn func() (db.ActivitySettings, error)) Option {
	return func(h *Hub) { h.settings = fn }
}

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option { return func(h *Hub) { h.log = l } }

// WithClock replaces time.Now.
func WithClock(now func() time.Time) Option { return func(h *Hub) { h.now = now } }

// WithDedupe sets the seen-set capacity.
func WithDedupe(n int) Option { return func(h *Hub) { h.dedupeCap = n } }

// WithRing sets the ring capacity.
func WithRing(n int) Option { return func(h *Hub) { h.ringCap = n } }

// WithBackoff replaces the source supervisor's restart delay, by attempt.
// Tests use it; production takes the default.
func WithBackoff(fn func(attempt int) time.Duration) Option { return func(h *Hub) { h.backoff = fn } }

// Hub ingests, deduplicates, filters, remembers and fans out activity.
type Hub struct {
	bus      Publisher
	display  Display
	hooks    Dispatcher
	settings func() (db.ActivitySettings, error)
	log      *slog.Logger
	now      func() time.Time
	backoff  func(attempt int) time.Duration

	dedupeCap int
	ringCap   int

	received, delivered, invalid, deduped, disabled atomic.Int64

	mu      sync.Mutex
	seen    map[string]struct{}
	order   []string
	ring    []activity.Event
	cached  db.ActivitySettings
	cacheT  time.Time
	cacheOK bool
	// gen counts InvalidateSettings calls. A load that began before an
	// invalidation must not overwrite the cache with what it read: that may be
	// the document from before the save that invalidated it.
	gen uint64

	sources sources
}

// New builds a hub. With no options it filters nothing out and fans out
// nowhere, which is what a test wants.
func New(opts ...Option) *Hub {
	h := &Hub{
		log:       slog.Default(),
		now:       time.Now,
		dedupeCap: DefaultDedupe,
		ringCap:   DefaultRing,
		seen:      map[string]struct{}{},
	}
	for _, o := range opts {
		o(h)
	}
	if h.settings == nil {
		h.settings = func() (db.ActivitySettings, error) { return db.DefaultActivitySettings(), nil }
	}
	h.cached = db.DefaultActivitySettings()
	if h.backoff == nil {
		h.backoff = defaultBackoff
	}
	h.sources.init(h)
	return h
}

// Ingest implements chat.ActivityIngester: Submit for a caller that does not
// need to know what became of the event.
func (h *Hub) Ingest(ev activity.Event) { h.Submit(ev) }

// Submit runs one event through the pipeline and reports what happened. Safe
// for concurrent use, never blocks on a consumer, nil-safe.
func (h *Hub) Submit(ev activity.Event) Result {
	if h == nil {
		return Result{Reason: ReasonDisabled, Detail: "activity is not available in this build"}
	}
	h.received.Add(1)
	ev = ev.Normalise(h.now)
	if err := ev.Validate(); err != nil {
		h.invalid.Add(1)
		// The type and the error, never the event: it holds viewer text.
		h.log.Debug("activity refused", "platform", ev.Platform, "kind", ev.Kind, "err", err)
		return Result{Reason: ReasonInvalid, Detail: err.Error()}
	}

	// Dedupe BEFORE the settings filter: an event switched off now and on later
	// is not replayed, because a platform that redelivers is redelivering the
	// past. A test event bypasses it -- pressing the button twice should show
	// twice.
	if !ev.Test && h.markSeen(ev.Key()) {
		h.deduped.Add(1)
		return Result{Reason: ReasonDuplicate, Detail: "already seen", Event: ev}
	}

	cfg := h.currentSettings()
	if !cfg.Enabled {
		h.disabled.Add(1)
		return Result{Reason: ReasonDisabled, Detail: "activity is switched off in settings", Event: ev}
	}
	if !cfg.KindEnabled(ev.Platform, ev.Kind) {
		h.disabled.Add(1)
		return Result{Reason: ReasonDisabled, Detail: string(ev.Kind) + " is switched off for " + ev.Platform + " in settings", Event: ev}
	}

	h.remember(ev)
	h.delivered.Add(1)
	if h.bus != nil {
		h.bus.Publish(events.TypeActivity, ev)
	}
	if h.hooks != nil {
		h.hooks.Activity(ev)
	}
	h.offerDisplay(ev, cfg)
	return Result{Accepted: true, Event: ev}
}

// offerDisplay hands an event to the overlay unless the overlay could never
// draw it or the operator's minimum rules it out. Minimums are the OVERLAY's:
// the console and hooks have already seen every tip.
func (h *Hub) offerDisplay(ev activity.Event, cfg db.ActivitySettings) {
	if h.display == nil || !ev.OverlayVisible() {
		return
	}
	if ev.Kind == activity.KindTip && cfg.BelowMinimum(ev.Amount) {
		return
	}
	if !cfg.OverlayMessages {
		ev.Message = ""
	}
	h.display.Offer(ev)
}

// Replay re-offers a remembered event to the overlay, for the console's
// "Replay on overlay". False when the hub no longer holds it or the overlay
// could never draw it. The operator's minimum is deliberately not applied: they
// asked for this one by hand.
func (h *Hub) Replay(id string) bool {
	if h == nil || h.display == nil {
		return false
	}
	ev, ok := h.find(id)
	if !ok || !ev.OverlayVisible() {
		return false
	}
	if !h.currentSettings().OverlayMessages {
		ev.Message = ""
	}
	h.display.Offer(ev)
	return true
}

// Recent returns up to limit remembered events, newest first, optionally
// narrowed to one platform and one kind. Copies: the ring's backing array is
// never exposed.
func (h *Hub) Recent(limit int, platform string, kind activity.Kind) []activity.Event {
	if h == nil {
		return nil
	}
	if limit <= 0 || limit > h.ringCap {
		limit = h.ringCap
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]activity.Event, 0, min(limit, len(h.ring)))
	for i := len(h.ring) - 1; i >= 0 && len(out) < limit; i-- {
		e := h.ring[i]
		if (platform != "" && e.Platform != platform) || (kind != "" && e.Kind != kind) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Stats returns the counters.
func (h *Hub) Stats() Stats {
	if h == nil {
		return Stats{}
	}
	return Stats{
		Received: h.received.Load(), Delivered: h.delivered.Load(), Invalid: h.invalid.Load(),
		Deduped: h.deduped.Load(), Disabled: h.disabled.Load(),
	}
}

// Settings returns the settings the hub is currently filtering by.
func (h *Hub) Settings() db.ActivitySettings { return h.currentSettings() }

func (h *Hub) currentSettings() db.ActivitySettings {
	for attempt := 0; ; attempt++ {
		h.mu.Lock()
		if h.cacheOK && h.now().Sub(h.cacheT) < settingsTTL {
			c := h.cached
			h.mu.Unlock()
			return c
		}
		gen := h.gen
		h.mu.Unlock()
		// Outside the lock: the loader reads the database.
		c, err := h.settings()
		h.mu.Lock()
		if h.gen != gen && attempt < 2 {
			// Invalidated while we were reading: what we read may predate the
			// save. Read again rather than cache it.
			h.mu.Unlock()
			continue
		}
		if err != nil {
			// Keep the last good settings, and try again after the TTL.
			h.log.Warn("activity settings could not be read; keeping the last good ones", "err", err)
			c = h.cached
		} else {
			h.cached = c
		}
		h.cacheT, h.cacheOK = h.now(), true
		h.mu.Unlock()
		return c
	}
}

// InvalidateSettings drops the cached settings, so the next event reads the
// stored ones. Called when settings are saved.
func (h *Hub) InvalidateSettings() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.cacheOK = false
	h.gen++
	h.mu.Unlock()
}

// markSeen records a key and reports whether it was already known: a copy of
// chat.Hub.markSeen, a bounded FIFO set.
func (h *Hub) markSeen(key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, dup := h.seen[key]; dup {
		return true
	}
	h.seen[key] = struct{}{}
	h.order = append(h.order, key)
	if len(h.order) > h.dedupeCap {
		drop := len(h.order) - h.dedupeCap
		for _, k := range h.order[:drop] {
			delete(h.seen, k)
		}
		h.order = append([]string(nil), h.order[drop:]...)
	}
	return false
}

func (h *Hub) remember(ev activity.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ring = append(h.ring, ev)
	if over := len(h.ring) - h.ringCap; over > 0 {
		h.ring = append([]activity.Event(nil), h.ring[over:]...)
	}
}

func (h *Hub) find(id string) (activity.Event, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.ring) - 1; i >= 0; i-- {
		if h.ring[i].ID == id {
			return h.ring[i], true
		}
	}
	return activity.Event{}, false
}
