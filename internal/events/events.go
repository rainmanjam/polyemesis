// Package events is the in-process pub/sub that feeds the WebSocket.
//
// Publishers (the supervisor, the metering parser, the stats monitor) must
// never block on a slow browser, so every subscriber gets a buffered channel
// and a subscriber that falls behind loses messages rather than applying
// backpressure to the streaming path.
package events

import (
	"sync"
	"time"
)

// Type identifies an event's payload shape.
type Type string

const (
	// TypeStatus carries the full process/destination status snapshot.
	TypeStatus Type = "status"
	// TypeLevels carries an audio metering frame.
	TypeLevels Type = "levels"
	// TypeLog carries one FFmpeg stderr line.
	TypeLog Type = "log"
	// TypeStats carries host CPU/RAM and the ingest bitrate point.
	TypeStats Type = "stats"
	// TypeSource carries the probed ingest track layout.
	TypeSource Type = "source"
	// TypeRecordings signals the recordings list has changed.
	TypeRecordings Type = "recordings"
	// TypeLoudness carries one destination's EBU R128 compliance report,
	// measured downstream of that destination's own routing graph. One
	// destination per event rather than the whole set: the reports arrive
	// independently and a browser that only cares about one card should not be
	// re-rendering all of them.
	TypeLoudness Type = "loudness"
	// TypeClips signals the captured-clip list has changed.
	TypeClips Type = "clips"
	// TypeCaption carries one live caption line, or the news that live
	// captioning has stopped because this machine could not keep up.
	//
	// One type for both because the two belong to the same stream: a caption
	// bar that goes quiet has to be able to say why, and a subscriber that took
	// the lines but not the warning would show a frozen last sentence forever.
	// Captions are also the most droppable payload here — a line nobody
	// received is a line nobody needed, which is exactly what this broker's
	// non-blocking publish is for.
	TypeCaption Type = "caption"
	// TypeChat carries one normalised chat message from any platform. One
	// message per event rather than a batch: chat is read as it arrives, and a
	// browser that joined mid-broadcast gets its scrollback from the REST
	// history endpoint instead of from a replayed batch nobody else needs.
	//
	// Like every other payload here it is droppable. A subscriber so far behind
	// that it is shedding chat has lost the conversation either way, and the
	// adapters must never be slowed down by a browser: an IRC socket that stops
	// being read gets dropped by Twitch in minutes.
	TypeChat Type = "chat"
	// TypeChatState carries the per-platform connection state for the whole
	// chat surface — connecting, live, degraded, failed — with the reason in
	// the operator's words.
	//
	// It is a separate type from TypeChat because it is the answer to "why has
	// chat gone quiet", and that question is asked precisely when no messages
	// are flowing. A UI that inferred health from message arrival could not
	// tell an idle channel from a dead adapter, and cross-platform chat where
	// one platform silently stopped is worse than one where it visibly did.
	TypeChatState Type = "chatState"
)

// Event is one message.
type Event struct {
	Type Type      `json:"type"`
	Time time.Time `json:"time"`
	Data any       `json:"data"`
}

// subBuffer is how many events a subscriber may fall behind before dropping.
// Levels arrive at ~10 Hz and logs can burst; 256 absorbs a browser stalling
// on a repaint without letting a dead tab accumulate unbounded memory.
const subBuffer = 256

// Broker fans events out to subscribers.
type Broker struct {
	mu   sync.RWMutex
	subs map[int]*Subscription
	next int
	// dropped counts events shed by slow subscribers, surfaced for debugging
	// a UI that appears to lag.
	dropped int64
}

// Subscription is one consumer's channel.
type Subscription struct {
	id     int
	C      chan Event
	broker *Broker
	// types, when non-empty, filters which events this subscriber receives.
	types map[Type]bool
}

// NewBroker creates a broker.
func NewBroker() *Broker {
	return &Broker{subs: map[int]*Subscription{}}
}

// Subscribe registers a consumer. Passing no types subscribes to everything.
func (b *Broker) Subscribe(types ...Type) *Subscription {
	b.mu.Lock()
	defer b.mu.Unlock()

	s := &Subscription{
		id:     b.next,
		C:      make(chan Event, subBuffer),
		broker: b,
	}
	if len(types) > 0 {
		s.types = map[Type]bool{}
		for _, t := range types {
			s.types[t] = true
		}
	}
	b.subs[b.next] = s
	b.next++
	return s
}

// Close unsubscribes and releases the channel.
func (s *Subscription) Close() {
	s.broker.mu.Lock()
	defer s.broker.mu.Unlock()
	if _, ok := s.broker.subs[s.id]; ok {
		delete(s.broker.subs, s.id)
		close(s.C)
	}
}

// Publish delivers an event to every interested subscriber.
//
// It never blocks: a full subscriber channel means that consumer is too slow,
// and dropping a metering frame is always better than stalling the pipeline
// that produced it.
func (b *Broker) Publish(t Type, data any) {
	ev := Event{Type: t, Time: time.Now(), Data: data}

	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, s := range b.subs {
		if s.types != nil && !s.types[t] {
			continue
		}
		select {
		case s.C <- ev:
		default:
			b.dropped++
		}
	}
}

// Stats reports subscriber count and dropped events.
func (b *Broker) Stats() (subscribers int, dropped int64) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs), b.dropped
}
