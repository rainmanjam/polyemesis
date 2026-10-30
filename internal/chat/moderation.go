package chat

import (
	"context"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
)

// Moderation actions, as announced on the bus (events.TypeChatModeration) and
// from there on the real-time events API as chat.moderation.
const (
	ModDelete    = "delete"
	ModHide      = "hide"
	ModUnhide    = "unhide"
	ModHideLocal = "hide_local"
	ModBan       = "ban"
	ModTimeout   = "timeout"
	ModUnban     = "unban"
)

// Who took an action.
const (
	ActorOperator = "operator"
	ActorAutomod  = "automod"
)

// Moderation is one action that SUCCEEDED. A refused or failed action is not
// announced: the caller already has the error, and an event saying a ban
// happened when the platform said no is the one lie this must not tell.
type Moderation struct {
	Action    string      `json:"action"`
	By        string      `json:"by"`
	Platform  db.Platform `json:"platform"`
	Account   string      `json:"account,omitempty"`
	MessageID string      `json:"messageId,omitempty"`
	// UserID is the platform's id for the viewer a ban, timeout or unban was
	// taken against.
	UserID string `json:"userId,omitempty"`
	// Seconds is a timeout's length; zero on a permanent ban.
	Seconds int `json:"seconds,omitempty"`
	// Reason is what the moderator gave, or automod's closed-set platform
	// reason. Never the model checker's prose (#495).
	Reason string `json:"reason,omitempty"`
}

type actorKey struct{}

// WithActor marks ctx as carrying an action taken by actor. The hub's
// moderation methods read it; anything not marked is an operator's.
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

func actorOf(ctx context.Context) string {
	if a, ok := ctx.Value(actorKey{}).(string); ok && a != "" {
		return a
	}
	return ActorOperator
}

// publishModeration announces one successful action. Non-blocking, like every
// bus publish: the broker drops for a slow subscriber rather than stall the
// moderation call.
func (h *Hub) publishModeration(ctx context.Context, m Moderation) {
	if h.bus == nil {
		return
	}
	m.By = actorOf(ctx)
	h.bus.Publish(events.TypeChatModeration, m)
}
