package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

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
