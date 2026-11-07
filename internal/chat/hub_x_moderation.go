package chat

import (
	"context"
	"fmt"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// RefusedLocally is an error the hub raised itself, before any call to a platform: the chat is not connected, or the
// platform has no such capability. A caller tells it from a failure of the platform with errors.As.
type RefusedLocally struct{ msg string }

func (e *RefusedLocally) Error() string { return e.msg }

func refusedf(format string, args ...any) error {
	return &RefusedLocally{msg: fmt.Sprintf(format, args...)}
}

// runnerFor finds the connected runner for one account, or says the platform is not connected.
func (h *Hub) runnerFor(p db.Platform, account string) (*runner, error) {
	h.mu.Lock()
	r := h.runners[runnerKey(p, account)]
	h.mu.Unlock()
	if r == nil {
		return nil, refusedf("chat: %s is not connected", p)
	}
	return r, nil
}

// ListMutes reads who the account has muted, where the platform publishes that. truncated is true when the platform
// had more than the list holds.
func (h *Hub) ListMutes(ctx context.Context, p db.Platform, account string) (mutes []ChatMute, truncated bool, err error) {
	r, err := h.runnerFor(p, account)
	if err != nil {
		return nil, false, err
	}
	l, ok := r.adapter.(MuteLister)
	if !ok {
		return nil, false, refusedf("chat: %s publishes no list of muted viewers; use the %s dashboard", p, p)
	}
	ctx, cancel := context.WithTimeout(ctx, h.sendTimeout)
	defer cancel()
	return l.ListMutes(ctx)
}

// BanDeleting mutes a viewer and removes one of their messages in a single platform call, where the platform has
// one. A platform without it is refused rather than done as two calls: the caller can ask for Ban and Delete.
func (h *Hub) BanDeleting(ctx context.Context, p db.Platform, account, userID string, d time.Duration, reason, messageID string) error {
	r, err := h.runnerFor(p, account)
	if err != nil {
		return err
	}
	b, ok := r.adapter.(MessageBanner)
	if !ok {
		return fmt.Errorf("chat: %s has no single call that bans and removes a message; ban and delete separately", p)
	}
	defer h.lockViewer(r, userID)()
	ctx, cancel := context.WithTimeout(ctx, h.sendTimeout)
	defer cancel()
	if err := b.BanDeleting(ctx, userID, d, reason, messageID); err != nil {
		return err
	}
	h.retractUser(r, userID)
	m := Moderation{Action: ModBan, Platform: p, Account: account, UserID: userID, Reason: reason}
	if d > 0 {
		m.Action, m.Seconds = ModTimeout, int(d/time.Second)
	}
	h.recordSanction(ctx, r, userID, m.Action, d, reason)
	h.publishModeration(ctx, m)
	return nil
}

func (h *Hub) moderatorManager(p db.Platform, account string) (ModeratorManager, error) {
	r, err := h.runnerFor(p, account)
	if err != nil {
		return nil, err
	}
	m, ok := r.adapter.(ModeratorManager)
	if !ok {
		return nil, refusedf("chat: %s publishes no chat moderator API; use the %s dashboard", p, p)
	}
	return m, nil
}

// ListModerators reads the account's chat moderators.
func (h *Hub) ListModerators(ctx context.Context, p db.Platform, account string) ([]ChatModerator, error) {
	m, err := h.moderatorManager(p, account)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, h.sendTimeout)
	defer cancel()
	return m.ListModerators(ctx)
}

// AddModerator appoints a chat moderator and returns the ids of every moderator afterwards.
func (h *Hub) AddModerator(ctx context.Context, p db.Platform, account, userID string) ([]string, error) {
	m, err := h.moderatorManager(p, account)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, h.sendTimeout)
	defer cancel()
	return m.AddModerator(ctx, userID)
}

// RemoveModerator removes a chat moderator and returns the ids of those that remain.
func (h *Hub) RemoveModerator(ctx context.Context, p db.Platform, account, userID string) ([]string, error) {
	m, err := h.moderatorManager(p, account)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, h.sendTimeout)
	defer cancel()
	return m.RemoveModerator(ctx, userID)
}
