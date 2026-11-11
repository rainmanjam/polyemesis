package chat

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/platform"
)

// ChatModerator is one person who can moderate a live chat. Two platforms
// fill it differently, and each leaves the other's fields out of the JSON:
// X sets UserID, Username and Name; YouTube sets ID, ChannelID, DisplayName
// and the two URLs.
type ChatModerator struct {
	// ID is YouTube's id for the moderator-in-this-chat relationship, which is
	// what removing one addresses -- not the channel id.
	ID              string `json:"id,omitempty"`
	ChannelID       string `json:"channelId,omitempty"`
	DisplayName     string `json:"displayName,omitempty"`
	ChannelURL      string `json:"channelUrl,omitempty"`
	ProfileImageURL string `json:"profileImageUrl,omitempty"`
	// UserID, Username and Name are X's: the user id every chat message
	// carries, its handle, and its display name.
	UserID   string `json:"userId,omitempty"`
	Username string `json:"username,omitempty"`
	Name     string `json:"name,omitempty"`
}

// ChannelModeratorManager is the optional capability for managing who can moderate a
// chat by channel id (X has its own, ModeratorManager, keyed by user id). YouTube is the one chat adapter with it: liveChatModerators lists, adds
// and removes, and the platform allows only the chat's channel owner to.
type ChannelModeratorManager interface {
	Adapter
	// Moderators lists the chat's moderators (first page, 50 at most).
	Moderators(ctx context.Context) ([]ChatModerator, error)
	// AddModerator makes the channel a moderator.
	AddModerator(ctx context.Context, channelID string) (ChatModerator, error)
	// RemoveModerator removes one by the id Moderators returned.
	RemoveModerator(ctx context.Context, id string) error
}

var _ ChannelModeratorManager = (*YouTubeAdapter)(nil)

const ytModeratorsPath = "/liveChat/moderators"

type ytModeratorItem struct {
	ID      string             `json:"id"`
	Snippet ytModeratorSnippet `json:"snippet"`
}

// ytModeratorSnippet is the snippet block of a moderator resource.
type ytModeratorSnippet struct {
	ModeratorDetails ytModeratorDetails `json:"moderatorDetails"`
}

// ytModeratorDetails is the moderator's channel identity.
type ytModeratorDetails struct {
	ChannelID       string `json:"channelId"`
	ChannelURL      string `json:"channelUrl"`
	DisplayName     string `json:"displayName"`
	ProfileImageURL string `json:"profileImageUrl"`
}

func (it ytModeratorItem) moderator() ChatModerator {
	d := it.Snippet.ModeratorDetails
	return ChatModerator{ID: it.ID, ChannelID: d.ChannelID, DisplayName: d.DisplayName,
		ChannelURL: d.ChannelURL, ProfileImageURL: d.ProfileImageURL}
}

// moderatorRefusal explains YouTube's 403 on these calls. The reference says
// every one must be authorised by the owner of the live chat's channel, so a
// 403 that is not a spent quota is almost always that.
func (y *YouTubeAdapter) moderatorRefusal(err error, verb string) error {
	if statusOf(err) == http.StatusForbidden && !oauth.IsSpentQuotaReason(reasonOf(err)) {
		return fmt.Errorf("YouTube refused to %s. Only the owner of the channel that runs the "+
			"broadcast can manage its moderators; a moderator cannot: %w", verb, err)
	}
	return y.classify(err)
}

func (y *YouTubeAdapter) liveChat() (string, error) {
	y.mu.Lock()
	id := y.chatID
	y.mu.Unlock()
	if id == "" {
		return "", fmt.Errorf("YouTube has no live chat open right now")
	}
	return id, nil
}

// Moderators implements ChannelModeratorManager: liveChatModerators.list, 50 units.
func (y *YouTubeAdapter) Moderators(ctx context.Context) ([]ChatModerator, error) {
	chatID, err := y.liveChat()
	if err != nil {
		return nil, err
	}
	// allowRead, not allow: the reserve is held so a send, delete or ban stays
	// possible, and this call must not spend it.
	if !y.budget.allowRead(platform.YouTubeCostModeratorList) {
		q := y.budget.status()
		return nil, fmt.Errorf("YouTube's daily API quota is spent, or what is left is held back for chat and bans; it resets at %s", q.ResetAt.Format(ytClockLayout))
	}
	var out struct {
		Items []ytModeratorItem `json:"items"`
	}
	endpoint := y.cfg.APIBase + ytModeratorsPath + "?part=snippet&maxResults=50&liveChatId=" + url.QueryEscape(chatID)
	err = doJSON(ctx, y.cfg.HTTP, http.MethodGet, endpoint, y.cfg.Token, nil, &out)
	y.budget.spend(platform.YouTubeCostModeratorList)
	if err != nil {
		return nil, y.moderatorRefusal(err, "list the moderators")
	}
	mods := make([]ChatModerator, 0, len(out.Items))
	for _, it := range out.Items {
		mods = append(mods, it.moderator())
	}
	return mods, nil
}

// AddModerator implements ChannelModeratorManager: liveChatModerators.insert, 200 units.
func (y *YouTubeAdapter) AddModerator(ctx context.Context, channelID string) (ChatModerator, error) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return ChatModerator{}, fmt.Errorf("no channel id to make a moderator")
	}
	chatID, err := y.liveChat()
	if err != nil {
		return ChatModerator{}, err
	}
	// allowRead, not allow: the reserve is held so a send, delete or ban stays
	// possible, and this call must not spend it.
	if !y.budget.allowRead(platform.YouTubeCostModeratorEdit) {
		q := y.budget.status()
		return ChatModerator{}, fmt.Errorf("YouTube's daily API quota is spent, or what is left is held back for chat and bans; it resets at %s", q.ResetAt.Format(ytClockLayout))
	}
	payload := map[string]any{"snippet": map[string]any{
		"liveChatId":       chatID,
		"moderatorDetails": map[string]any{"channelId": channelID},
	}}
	var out ytModeratorItem
	err = doJSON(ctx, y.cfg.HTTP, http.MethodPost, y.cfg.APIBase+ytModeratorsPath+"?part=snippet",
		y.cfg.Token, payload, &out)
	y.budget.spend(platform.YouTubeCostModeratorEdit)
	if err != nil {
		return ChatModerator{}, y.moderatorRefusal(err, "add that moderator")
	}
	return out.moderator(), nil
}

// RemoveModerator implements ChannelModeratorManager: liveChatModerators.delete, 200
// units. Like Delete, never refused for want of budget: taking moderation power
// away is not something to defer because arithmetic said the quota was low.
func (y *YouTubeAdapter) RemoveModerator(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("no moderator id to remove")
	}
	err := doJSON(ctx, y.cfg.HTTP, http.MethodDelete,
		y.cfg.APIBase+ytModeratorsPath+"?id="+url.QueryEscape(id), y.cfg.Token, nil, nil)
	y.budget.spend(platform.YouTubeCostModeratorEdit)
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return fmt.Errorf("YouTube does not have that moderator any more; they may already be removed: %w", err)
		}
		return y.moderatorRefusal(err, "remove that moderator")
	}
	return nil
}

func (h *Hub) channelModeratorManager(p db.Platform, account string) (ChannelModeratorManager, error) {
	h.mu.Lock()
	r := h.runners[runnerKey(p, account)]
	h.mu.Unlock()
	if r == nil {
		return nil, refusedf("chat: %s is not connected", p)
	}
	m, ok := r.adapter.(ChannelModeratorManager)
	if !ok {
		return nil, refusedf("chat: polyemesis cannot manage moderators on %s; use the %s dashboard", p, p)
	}
	return m, nil
}

// CanManageChannelModerators says whether the connected chat of one platform
// account manages moderators by channel id (YouTube). The error is a
// *RefusedLocally: not connected, or the platform has no such API. The API
// checks this before it looks at a request's parameters.
func (h *Hub) CanManageChannelModerators(p db.Platform, account string) error {
	_, err := h.channelModeratorManager(p, account)
	return err
}

// Moderators lists one platform account's chat moderators.
func (h *Hub) Moderators(ctx context.Context, p db.Platform, account string) ([]ChatModerator, error) {
	m, err := h.channelModeratorManager(p, account)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, h.sendTimeout)
	defer cancel()
	return m.Moderators(ctx)
}

// AddChannelModerator makes a channel a moderator on one platform account's chat.
func (h *Hub) AddChannelModerator(ctx context.Context, p db.Platform, account, channelID string) (ChatModerator, error) {
	m, err := h.channelModeratorManager(p, account)
	if err != nil {
		return ChatModerator{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, h.sendTimeout)
	defer cancel()
	return m.AddModerator(ctx, channelID)
}

// RemoveChannelModerator removes a moderator from one platform account's chat.
func (h *Hub) RemoveChannelModerator(ctx context.Context, p db.Platform, account, id string) error {
	m, err := h.channelModeratorManager(p, account)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, h.sendTimeout)
	defer cancel()
	return m.RemoveModerator(ctx, id)
}
