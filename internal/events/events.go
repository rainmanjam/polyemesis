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
