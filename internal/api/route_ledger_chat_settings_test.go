package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// chatSettingsPlanted is the slow-mode wait the stub room reports. Distinctive
// so its appearance in a body is the planted value and nothing else.
const chatSettingsPlanted = 4093

// rulesAdapter is a connected Twitch room that answers a rules read.
type rulesAdapter struct {
	moderatableAdapter
}

func (a *rulesAdapter) ReadChatSettings(context.Context) (chat.ChatSettings, error) {
	on, wait := true, chatSettingsPlanted
	return chat.ChatSettings{SlowMode: &on, SlowModeSeconds: &wait}, nil
}

var _ chat.ChatSettingsReader = (*rulesAdapter)(nil)

// proveChatSettings is GET /chat/settings' counterpart: the admin reads the
// room's planted rules, the read token is refused them.
func proveChatSettings(t *testing.T) proofResult {
	s, h, _ := testServer(t, config.Config{})
	hub := chat.New()
	t.Cleanup(hub.Close)
	s.chat = hub
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := hub.Attach(ctx, &rulesAdapter{moderatableAdapter{platform: db.PlatformTwitch, account: "acct"}}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	sign := login(t, h)
	read := createScopedToken(t, h, sign, "ledger-chat-settings", db.ScopeRead)

	const path = "/api/v1/chat/settings?platform=twitch&account=acct"
	planted := strconv.Itoa(chatSettingsPlanted)
	code, high := rawBodyOf(t, h, sign, path)
	if code != http.StatusOK || !strings.Contains(high, planted) {
		t.Fatalf("the admin's rules read answered %d without the planted wait: %s", code, truncateForFailure(high))
	}
	code, low := rawBodyOf(t, h, bearer(read), path)
	if code != http.StatusForbidden {
		t.Errorf("the read token's rules read answered %d, want 403", code)
	}
	return proofResult{
		Pattern:   patternDriven(t, h, http.MethodGet, "/api/v1/chat/settings"),
		High:      high,
		Read:      low,
		Sentinels: []string{planted},
	}
}

// The endpoint answers with the room's real rules for the rules card.
func TestGetChatSettingsAnswersTheRoomsRules(t *testing.T) {
	res := proveChatSettings(t)
	if !strings.Contains(res.High, `"slowMode":true`) {
		t.Errorf("body %s does not carry the slow mode the room reported", res.High)
	}
}

// xModerationAdapter is a connected X chat that answers the mute list and the moderator list.
type xModerationAdapter struct {
	moderatableAdapter
}

const (
	xLedgerMuted     = "ledger-muted-4091"
	xLedgerModerator = "ledger-mod-4093"
)

func (a *xModerationAdapter) ListMutes(context.Context) ([]chat.ChatMute, bool, error) {
	return []chat.ChatMute{{UserID: "4091", Username: xLedgerMuted}}, false, nil
}

func (a *xModerationAdapter) ListModerators(context.Context) ([]chat.ChatModerator, error) {
	return []chat.ChatModerator{{UserID: "4093", Username: xLedgerModerator}}, nil
}

func (a *xModerationAdapter) AddModerator(_ context.Context, id string) ([]string, error) {
	return []string{id}, nil
}

func (a *xModerationAdapter) RemoveModerator(context.Context, string) ([]string, error) {
	return []string{}, nil
}

func (a *xModerationAdapter) BanDeleting(_ context.Context, userID string, d time.Duration, reason, _ string) error {
	return a.Ban(context.Background(), userID, d, reason)
}

var (
	_ chat.MuteLister       = (*xModerationAdapter)(nil)
	_ chat.ModeratorManager = (*xModerationAdapter)(nil)
	_ chat.MessageBanner    = (*xModerationAdapter)(nil)
)

// proveXModerationRead plants a muted viewer and a moderator behind an X chat and proves the admin reads them and a
// read token is refused.
func proveXModerationRead(t *testing.T, route, path, planted string) proofResult {
	s, h, _ := testServer(t, config.Config{})
	hub := chat.New()
	t.Cleanup(hub.Close)
	s.chat = hub
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := hub.Attach(ctx, &xModerationAdapter{moderatableAdapter{platform: db.PlatformX, account: "acct"}}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	sign := login(t, h)
	read := createScopedToken(t, h, sign, "ledger-"+route, db.ScopeRead)

	code, high := rawBodyOf(t, h, sign, path)
	if code != http.StatusOK || !strings.Contains(high, planted) {
		t.Fatalf("the admin's read of %s answered %d without the planted value: %s", path, code, truncateForFailure(high))
	}
	code, low := rawBodyOf(t, h, bearer(read), path)
	if code != http.StatusForbidden {
		t.Errorf("the read token's read of %s answered %d, want 403", path, code)
	}
	return proofResult{
		Pattern:   patternDriven(t, h, http.MethodGet, strings.SplitN(path, "?", 2)[0]),
		High:      high,
		Read:      low,
		Sentinels: []string{planted},
	}
}

// proveChatMutes is GET /chat/mutes' counterpart.
func proveChatMutes(t *testing.T) proofResult {
	return proveXModerationRead(t, "chat-mutes", "/api/v1/chat/mutes?platform=x&account=acct", xLedgerMuted)
}

// proveChatModerators is GET /chat/moderators' counterpart.
func proveChatModerators(t *testing.T) proofResult {
	return proveXModerationRead(t, "chat-moderators", "/api/v1/chat/moderators?platform=x&account=acct", xLedgerModerator)
}
