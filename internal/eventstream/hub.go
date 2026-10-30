package eventstream

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

// Hub bounds.
const (
	// DefaultReplayWindow is how far back a reconnecting client can resume
	// from. Five minutes covers a laptop lid, a proxy restart and a Wi-Fi
	// handover; anything longer is an outage, and the answer to an outage is
	// to reconcile over REST, which stream.gap says.
	DefaultReplayWindow = 5 * time.Minute
	// DefaultReplayMax caps the ring by count as well as by age. A busy chat is
	// the only producer that can approach it, and an age bound alone would let
	// a raid hold an unbounded five minutes of messages in memory.
	DefaultReplayMax = 4096
	// DefaultClientBuffer is how many events one connection may fall behind
	// before it is disconnected. Not dropped-and-continued like events.Broker:
	// a consumer that silently skipped an event would believe it had seen
	// every one, and the whole point of numbered events is that it can tell.
	// Disconnected, it reconnects with Last-Event-ID and replays the gap from
	// the ring, which is the recovery path that already exists.
	DefaultClientBuffer = 256
)

// Option tunes a Hub. Tests use them; production takes the defaults.
type Option func(*Hub)

// WithReplayWindow sets the replay age bound.
func WithReplayWindow(d time.Duration) Option { return func(h *Hub) { h.window = d } }

// WithReplayMax sets the replay count bound.
func WithReplayMax(n int) Option { return func(h *Hub) { h.max = n } }

// WithClientBuffer sets the per-connection buffer.
func WithClientBuffer(n int) Option { return func(h *Hub) { h.buffer = n } }

// WithClock replaces time.Now.
func WithClock(now func() time.Time) Option { return func(h *Hub) { h.now = now } }

// Hub numbers, redacts, remembers and fans out events.
//
// PUBLISH NEVER BLOCKS. Its callers are the engine's sweep goroutine, a
// supervisor callback holding the engine lock, the chat bridge and the
// lifecycle coordinator -- the same callers hooks.Dispatcher.Publish and
// alerts.Notifier.Publish refuse to stall, for the same reason. The hub takes
// its own mutex and nothing else, and never calls out while holding it, so it
// can sit at the bottom of any lock order.
type Hub struct {
	now    func() time.Time
	window time.Duration
	max    int
	buffer int
	// boot distinguishes this process's ids from the last one's. A client
	// resuming with an id from before a restart would otherwise be replayed
	// from whatever happens to carry the same number now.
	boot string

	mu   sync.Mutex
	seq  uint64
	ring []Envelope
	subs map[*Subscription]struct{}

	listeners atomic.Int64
	kicked    atomic.Int64
}

// NewHub builds a hub.
func NewHub(opts ...Option) *Hub {
	h := &Hub{
		now:    time.Now,
		window: DefaultReplayWindow,
		max:    DefaultReplayMax,
		buffer: DefaultClientBuffer,
		subs:   map[*Subscription]struct{}{},
	}
	for _, o := range opts {
		o(h)
	}
	h.boot = strconv.FormatInt(h.now().UnixNano(), 36)
	return h
}

// Subscription is one connection's view of the hub.
type Subscription struct {
	// C carries events in order. It is CLOSED when the hub disconnects a
	// client that fell DefaultClientBuffer behind; the handler then ends the
	// response and the client resumes with Last-Event-ID.
	C       <-chan Envelope
	c       chan Envelope
	topics  map[Topic]bool
	read    bool
	overlay *overlayPolicy
	hub     *Hub
	closed  bool
	// Overflowed reports, after C is closed, whether the hub closed it.
	overflowed atomic.Bool
}

// Overflowed reports whether the hub disconnected this subscription for falling
// behind.
func (s *Subscription) Overflowed() bool { return s.overflowed.Load() }

// Filter is what one connection asked for.
type Filter struct {
	// Topics, when non-empty, limits the stream to these topics.
	Topics []Topic
	// ReadScoped withholds every type whose Spec says Withheld.
	ReadScoped bool
	// Overlay, when set, makes this an overlay key's subscription: it is sent
	// the OverlayVisible types for its platforms and NOTHING ELSE. It
	// overrides Topics and ReadScoped rather than combining with them, so no
	// combination of query parameters can widen it.
	Overlay *OverlayView
}

// OverlayView is what one overlay may see.
type OverlayView struct {
	// Platforms limits the overlay to these platforms; empty means all.
	Platforms []string
}

// overlayPolicy is an OverlayView compiled for the fan-out.
type overlayPolicy struct{ platforms map[string]bool }

func (f Filter) overlayPolicy() *overlayPolicy {
	if f.Overlay == nil {
		return nil
	}
	p := &overlayPolicy{}
	if len(f.Overlay.Platforms) > 0 {
		p.platforms = map[string]bool{}
		for _, pl := range f.Overlay.Platforms {
			p.platforms[pl] = true
		}
	}
	return p
}

// displayPlatform reads the platform off a display payload, or "" when the
// payload is not one -- which the overlay policy treats as "withhold".
func displayPlatform(env Envelope) string {
	switch d := env.Data.(type) {
	case DisplayLine:
		return d.Platform
	case DisplayRemoval:
		return d.Platform
	}
	return ""
}

// admits is the overlay policy, and it fails closed twice: a type outside the
// closed set is withheld, and so is a payload whose platform cannot be read.
func (p *overlayPolicy) admits(env Envelope) bool {
	if !OverlayVisible(env.Type) {
		return false
	}
	// The one platform-neutral payload, admitted on its own row only and to
	// every overlay: it names no platform, so a platform filter has nothing
	// to filter (TestAPollIsShownOnEveryOverlayWhateverItsPlatformFilter).
	if _, ok := env.Data.(PollDisplay); ok {
		return env.Type == TypePollDisplay
	}
	// A line retracted since it was published is never REPLAYED to an
	// overlay. The live send happened before the retraction existed, and the
	// chat.undisplay that followed it is what removed it there.
	if env.retracted {
		return false
	}
	pl := displayPlatform(env)
	if pl == "" {
		return false
	}
	return p.platforms == nil || p.platforms[pl]
}

func (f Filter) topicSet() map[Topic]bool {
	if len(f.Topics) == 0 {
		return nil
	}
	m := make(map[Topic]bool, len(f.Topics))
	for _, t := range f.Topics {
		m[t] = true
	}
	return m
}

// wants is the per-connection policy, and it FAILS CLOSED: a type with no
// catalogue row is sent to nobody, read-scoped or not. Publish refuses such a
// type before it reaches here, so this is the net under that guard.
func wants(topics map[Topic]bool, readScoped bool, overlay *overlayPolicy, env Envelope) bool {
	spec, ok := Lookup(env.Type)
	if !ok {
		return false
	}
	if overlay != nil {
		return overlay.admits(env)
	}
	if readScoped {
		switch spec.ReadScope {
		case Granted:
		case Partly:
			if spec.ReadIf == nil || !spec.ReadIf(env) {
				return false
			}
		default:
			return false
		}
	}
	return topics == nil || topics[spec.Topic]
}

// HasSubscribers reports whether any connection is open. The engine asks this
// to decide whether its sweep has anyone to derive edges for. Nil-safe.
func (h *Hub) HasSubscribers() bool { return h != nil && h.listeners.Load() > 0 }

// Kicked is how many connections have been disconnected for falling behind.
func (h *Hub) Kicked() int64 {
	if h == nil {
		return 0
	}
	return h.kicked.Load()
}

// Publish numbers, redacts, records and fans out one event. It never blocks
// and never returns an error. Nil-safe, so a producer wired without a hub --
// every hand-built engine in a test -- needs no guard at its call site.
//
// An uncatalogued type is DROPPED here. Fail-closed at the door means a
// producer that invents a type nobody classified cannot reach any client, and
// TestEveryCatalogued… makes adding one to the catalogue the way in.
func (h *Hub) Publish(env Envelope) {
	if h == nil {
		return
	}
	if _, ok := Lookup(env.Type); !ok || env.Type == TypeStreamGap {
		return
	}
	now := h.now()
	if env.At.IsZero() {
		env.At = now
	}
	env.SpecVersion = SpecVersion
	// REDACTED BEFORE IT IS NUMBERED, RECORDED OR SENT, so the ring -- which
	// replays to principals who were not connected when it was published --
	// never holds an unredacted copy.
	env = env.redacted()

	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	env.ID = h.boot + "-" + strconv.FormatUint(h.seq, 10)
	h.ring = append(h.ring, env)
	h.scrubRetractedLocked(env)
	h.pruneLocked(now)
	for s := range h.subs {
		if !wants(s.topics, s.read, s.overlay, env) {
			continue
		}
		select {
		case s.c <- env:
		default:
			h.dropLocked(s, true)
		}
	}
}

// scrubRetractedLocked marks every held chat.display a chat.undisplay names, so
// overlay replay skips it (see overlayPolicy.admits). Done when the retraction
// is PUBLISHED rather than worked out at replay time, so it holds for any
// resume point -- before the line, between the line and its retraction, or a
// gap that replays everything held. Admin replay is unaffected: it is sent the
// line and then the retraction, which is what happened.
func (h *Hub) scrubRetractedLocked(env Envelope) {
	rm, ok := env.Data.(DisplayRemoval)
	if !ok || env.Type != TypeChatUndisplay || len(rm.IDs) == 0 {
		return
	}
	gone := make(map[string]bool, len(rm.IDs))
	for _, id := range rm.IDs {
		gone[id] = true
	}
	for i := range h.ring {
		if h.ring[i].Type != TypeChatDisplay {
			continue
		}
		if d, ok := h.ring[i].Data.(DisplayLine); ok && d.Platform == rm.Platform && gone[d.ID] {
			h.ring[i].retracted = true
		}
	}
}

// pruneLocked drops ring entries past either bound.
func (h *Hub) pruneLocked(now time.Time) {
	cut := 0
	for cut < len(h.ring) && now.Sub(h.ring[cut].At) > h.window {
		cut++
	}
	if over := len(h.ring) - cut - h.max; over > 0 {
		cut += over
	}
	if cut > 0 {
		// Re-sliced, not copied: a copy here made every publish past the cap
		// O(ring), which TestPublishNeverBlocksAPublisher measured at seconds
		// for a burst. The dropped head stays reachable only until the next
		// append outgrows the backing array, which then reallocates holding
		// the live tail alone, so memory stays within a small multiple of max.
		h.ring = h.ring[cut:]
	}
}

func (h *Hub) dropLocked(s *Subscription, overflow bool) {
	if s.closed {
		return
	}
	s.closed = true
	delete(h.subs, s)
	h.listeners.Add(-1)
	if overflow {
		s.overflowed.Store(true)
		h.kicked.Add(1)
	}
	close(s.c)
}

// Close releases a subscription. Safe to call after the hub has closed it.
func (s *Subscription) Close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.hub.dropLocked(s, false)
}

// Subscribe registers a connection and returns, atomically with the
// registration, every remembered event after lastID that the filter admits.
// Atomic so that an event published between the replay and the registration is
// neither lost nor sent twice.
//
// gap is true when lastID was given and the ring cannot prove it holds
// everything after it: an id from another process, an id older than the ring,
// or one that does not parse. The caller sends stream.gap.
func (h *Hub) Subscribe(f Filter, lastID string) (sub *Subscription, replay []Envelope, gap bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	topics := f.topicSet()
	overlay := f.overlayPolicy()
	if lastID != "" {
		from, ok := h.resumeIndexLocked(lastID)
		if !ok {
			gap = true
		}
		for _, env := range h.ring[from:] {
			if wants(topics, f.ReadScoped, overlay, env) {
				replay = append(replay, env)
			}
		}
	}
	c := make(chan Envelope, h.buffer)
	sub = &Subscription{C: c, c: c, topics: topics, read: f.ReadScoped, overlay: overlay, hub: h}
	h.subs[sub] = struct{}{}
	h.listeners.Add(1)
	return sub, replay, gap
}

// resumeIndexLocked finds where in the ring to resume after lastID. ok is false
// when the answer cannot be trusted, in which case everything held is replayed
// -- more than was asked for, never less.
func (h *Hub) resumeIndexLocked(lastID string) (int, bool) {
	boot, seqText, found := strings.Cut(lastID, "-")
	if !found || boot != h.boot {
		return 0, false
	}
	seq, err := strconv.ParseUint(seqText, 10, 64)
	if err != nil || seq > h.seq {
		return 0, false
	}
	if seq == h.seq {
		return len(h.ring), true
	}
	if len(h.ring) == 0 {
		// Something was published after lastID and has already aged out.
		return 0, false
	}
	first := h.ringSeqLocked(0)
	if seq+1 < first {
		// The next event the client needs has already aged out.
		return 0, false
	}
	return int(seq + 1 - first), true
}

// ringSeqLocked is the sequence number of ring entry i. Ids in the ring are
// contiguous, because Publish numbers and appends under one lock and prune only
// removes from the front.
func (h *Hub) ringSeqLocked(i int) uint64 {
	_, s, _ := strings.Cut(h.ring[i].ID, "-")
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}

// Gap builds the stream.gap event for one connection.
func (h *Hub) Gap(lastID string) Envelope {
	return Envelope{
		SpecVersion: SpecVersion, Type: TypeStreamGap, At: h.now(),
		Reason: "events after " + strconv.Quote(lastID) + " are no longer held, or are from " +
			"before a restart; everything still held has been replayed, so reconcile " +
			"anything older over REST",
	}
}

// redacted applies the webhook rules -- hooks.Event.redacted's fields, by the
// same function -- and then the residual pass over the free-form payload that
// the read-scoped WebSocket applies to text somebody else authored.
func (e Envelope) redacted() Envelope {
	e.Reason = alerts.Redact(e.Reason)
	e.Error = alerts.Redact(e.Error)
	if e.Source != nil {
		s := *e.Source
		s.Name = alerts.Redact(s.Name)
		e.Source = &s
	}
	if e.Destination != nil {
		d := *e.Destination
		d.Name = alerts.Redact(d.Name)
		e.Destination = &d
	}
	if e.Failure != nil {
		f := *e.Failure
		f.Line = alerts.Redact(f.Line)
		e.Failure = &f
	}
	// An alert is redacted by ITS OWN rules and NOT by the tree pass. Its
	// fields are name/value PAIRS, and a pair whose name says credential is
	// masked by alerts.Event.Redacted, which the generic pass cannot do -- it
	// sees a key called "value". And the generic pass would do the opposite
	// harm to the alert's own `key`, which is the SUBJECT ("destination:3"),
	// not a credential: a field named "key" is masked outright by name, and an
	// alert whose subject reads "[redacted]" cannot be correlated with its
	// resolution. The key is run through the text redactor instead.
	//
	// The notifier tap already hands over a redacted copy; doing it again
	// here means a producer that publishes an alert some other way is held to
	// the same rule rather than trusted to have remembered it.
	if a, ok := e.Data.(alerts.Event); ok {
		a = a.Redacted()
		a.Key = alerts.Redact(a.Key)
		e.Data = a
		return e
	}
	// The display projection keeps its TYPE, because the overlay policy reads
	// the platform off it and a map would be withheld from every overlay. Its
	// free text gets the same text redactor the tree pass would apply; it has
	// no field named after a secret for the tree pass to mask.
	switch d := e.Data.(type) {
	case DisplayLine:
		d.Name = alerts.Redact(d.Name)
		d.Text = alerts.Redact(d.Text)
		e.Data = d
		return e
	case DisplayRemoval:
		return e
	case PollDisplay:
		d.Question = alerts.Redact(d.Question)
		opts := make([]PollDisplayOption, len(d.Options))
		for i, o := range d.Options {
			opts[i] = PollDisplayOption{Label: alerts.Redact(o.Label), Votes: o.Votes}
		}
		d.Options = opts
		e.Data = d
		return e
	}
	if e.Data != nil {
		e.Data = alerts.RedactTree(e.Data)
	}
	return e
}
