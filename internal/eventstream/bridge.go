package eventstream

import (
	"context"

	"github.com/rainmanjam/polyemesis/internal/events"
)

// busTypes maps the in-process bus types the stream carries onto stream types.
// Only chat and activity come this way: it is install-wide, so the bus message needs no
// programme stamped on it, and the chat hub already publishes every message,
// retraction and moderation action there exactly once.
var busTypes = map[events.Type]Type{
	events.TypeChat:           TypeChatMessage,
	events.TypeChatRetract:    TypeChatRetract,
	events.TypeChatModeration: TypeChatModeration,
	// Activity rides the bus like chat does, and for the same reason: it is
	// install-wide, and the activity hub is the one place it is published.
	events.TypeActivity: TypeActivity,
}

// BridgeBus forwards the chat family from the in-process bus until ctx ends.
//
// One subscription for the whole install rather than one per connection, so
// the bus's own drop-on-slow rule only ever applies to this goroutine, which
// does nothing but hand events to Hub.Publish -- itself non-blocking.
func BridgeBus(ctx context.Context, bus *events.Broker, hub *Hub) {
	if bus == nil || hub == nil {
		return
	}
	types := make([]events.Type, 0, len(busTypes))
	for t := range busTypes {
		types = append(types, t)
	}
	sub := bus.Subscribe(types...)
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.C:
			if !ok {
				return
			}
			if t, ok := busTypes[ev.Type]; ok {
				hub.Publish(Envelope{Type: t, At: ev.Time, Data: ev.Data})
			}
		}
	}
}

// BusReadAccess is what a read-scoped token gets of an in-process bus type this
// stream carries, from the same catalogue row /events applies. ok is false for
// a bus type the stream does not carry. /ws asks it for those types so the two
// surfaces cannot classify one event two ways.
func BusReadAccess(t events.Type) (Access, bool) {
	st, ok := busTypes[t]
	if !ok {
		return Withheld, false
	}
	spec, found := Lookup(st)
	if !found {
		return Withheld, true
	}
	return spec.ReadScope, true
}
