package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// chatModeratorsPlanted is a moderator name only this proof plants.
const chatModeratorsPlanted = "planted-moderator-4099"

// moderatorsAdapter is a connected YouTube chat whose owner may manage moderators.
type moderatorsAdapter struct {
	moderatableAdapter
	added, removed string
	failWith       error
}

func (a *moderatorsAdapter) Moderators(context.Context) ([]chat.ChatModerator, error) {
	if a.failWith != nil {
		return nil, a.failWith
	}
	return []chat.ChatModerator{{ID: "m1", ChannelID: "UC-m", DisplayName: chatModeratorsPlanted}}, nil
}

func (a *moderatorsAdapter) AddModerator(_ context.Context, channelID string) (chat.ChatModerator, error) {
	a.added = channelID
	return chat.ChatModerator{ID: "m2", ChannelID: channelID, DisplayName: "new"}, nil
}

func (a *moderatorsAdapter) RemoveModerator(_ context.Context, id string) error {
	a.removed = id
	return nil
}

var _ chat.ChannelModeratorManager = (*moderatorsAdapter)(nil)

func moderatorsFixture(t *testing.T) (http.Handler, *moderatorsAdapter) {
	t.Helper()
	s, h, _ := testServer(t, config.Config{})
	hub := chat.New()
	t.Cleanup(hub.Close)
	s.chat = hub
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ad := &moderatorsAdapter{moderatableAdapter: moderatableAdapter{platform: db.PlatformYouTube, account: "acct"}}
	if err := hub.Attach(ctx, ad); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return h, ad
}

// proveYouTubeChatModerators is the YouTube half of GET /chat/moderators'
// counterpart: the admin reads the planted roster, the read token is refused it.
// The registered "chatModerators" proof drives X's half (proveChatModerators, in
// route_ledger_chat_settings_test.go); the route is one pattern, so this half
// runs as its own test below rather than as a second ledger entry.
func proveYouTubeChatModerators(t *testing.T) proofResult {
	h, _ := moderatorsFixture(t)
	sign := login(t, h)
	read := createScopedToken(t, h, sign, "ledger-chat-moderators", db.ScopeRead)

	const path = "/api/v1/chat/moderators?platform=youtube&account=acct"
	code, high := rawBodyOf(t, h, sign, path)
	if code != http.StatusOK || !strings.Contains(high, chatModeratorsPlanted) {
		t.Fatalf("the admin's moderator read answered %d without the planted name: %s", code, truncateForFailure(high))
	}
	code, low := rawBodyOf(t, h, bearer(read), path)
	if code != http.StatusForbidden {
		t.Errorf("the read token's moderator read answered %d, want 403", code)
	}
	return proofResult{
		Pattern:   patternDriven(t, h, http.MethodGet, "/api/v1/chat/moderators"),
		High:      high,
		Read:      low,
		Sentinels: []string{chatModeratorsPlanted},
	}
}

func TestYouTubeChatModeratorsReadIsAdminOnly(t *testing.T) {
	got := proveYouTubeChatModerators(t)
	if got.Pattern != "/api/v1/chat/moderators" || strings.Contains(got.Read, chatModeratorsPlanted) {
		t.Fatalf("pattern %q, read body %q", got.Pattern, truncateForFailure(got.Read))
	}
}

func TestChatModeratorRoutesListAddRemoveAndRefuse(t *testing.T) {
	h, ad := moderatorsFixture(t)
	sign := login(t, h)

	var list struct {
		Moderators []chat.ChatModerator `json:"moderators"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/chat/moderators?platform=youtube&account=acct", nil, http.StatusOK), &list)
	if len(list.Moderators) != 1 || list.Moderators[0].ID != "m1" {
		t.Fatalf("list = %+v", list)
	}

	var added chat.ChatModerator
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/chat/moderators?platform=youtube&account=acct&channelId=UC-new", nil, http.StatusOK), &added)
	if added.ChannelID != "UC-new" || ad.added != "UC-new" {
		t.Fatalf("added %+v (%q)", added, ad.added)
	}

	send(t, h, sign, http.MethodDelete, "/api/v1/chat/moderators?platform=youtube&account=acct&id=m1", nil, http.StatusOK)
	if ad.removed != "m1" {
		t.Errorf("removed %q", ad.removed)
	}

	// Missing parameters, a platform that is not connected, and one that cannot.
	send(t, h, sign, http.MethodGet, "/api/v1/chat/moderators", nil, http.StatusBadRequest)
	send(t, h, sign, http.MethodPost, "/api/v1/chat/moderators?platform=youtube", nil, http.StatusBadRequest)
	send(t, h, sign, http.MethodDelete, "/api/v1/chat/moderators?platform=youtube", nil, http.StatusBadRequest)
	send(t, h, sign, http.MethodGet, "/api/v1/chat/moderators?platform=twitch&account=x", nil, http.StatusBadRequest)
	send(t, h, sign, http.MethodPost, "/api/v1/chat/moderators?platform=twitch&channelId=u", nil, http.StatusBadRequest)
	send(t, h, sign, http.MethodDelete, "/api/v1/chat/moderators?platform=twitch&id=u", nil, http.StatusBadRequest)
}

// YouTube broadcast controls travel with the broadcast half, which reports its
// own result, so complianceFields names nothing for them.
func TestComplianceFieldsNamesTheYouTubeControls(t *testing.T) {
	dvr := true
	got := complianceFields(db.Compliance{YouTube: &db.YouTubeBroadcastControls{EnableDvr: &dvr}})
	if len(got) != 0 {
		t.Errorf("fields = %v", got)
	}
	if got := complianceFields(db.Compliance{YouTube: &db.YouTubeBroadcastControls{}}); len(got) != 0 {
		t.Errorf("empty controls named %v", got)
	}
}

// The shared route checks the platform's capability BEFORE its parameters: a
// connected Twitch or Kick chat is told polyemesis cannot manage its moderators
// (400, a local refusal), even when the request is also missing channelId or
// id. A platform that is not connected at all gets "not connected", also 400.
// A failure of the platform itself is 502, as X's is; bad input and local
// refusals stay 400.
func TestChatModeratorRoutesAnswer502WhenYouTubeFails(t *testing.T) {
	h, ad := moderatorsFixture(t)
	sign := login(t, h)
	ad.failWith = errors.New("YouTube said no")
	send(t, h, sign, http.MethodGet, "/api/v1/chat/moderators?platform=youtube&account=acct", nil, http.StatusBadGateway)
}

func TestChatModeratorRoutesRefuseAPlatformThatCannotManageThem(t *testing.T) {
	s2, h2, _ := testServer(t, config.Config{})
	hub := chat.New()
	t.Cleanup(hub.Close)
	s2.chat = hub
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for _, p := range []db.Platform{db.PlatformTwitch, db.PlatformKick} {
		if err := hub.Attach(ctx, &moderatableAdapter{platform: p, account: "acct"}); err != nil {
			t.Fatalf("Attach %s: %v", p, err)
		}
	}
	sign2 := login(t, h2)
	for _, p := range []string{"twitch", "kick"} {
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			body := send(t, h2, sign2, m, "/api/v1/chat/moderators?platform="+p+"&account=acct", nil, http.StatusBadRequest)
			if want := "cannot manage moderators on " + p; !strings.Contains(string(body), want) {
				t.Errorf("%s %s answered %s, want it to say %q", m, p, body, want)
			}
		}
	}
	body := send(t, h2, sign2, http.MethodGet, "/api/v1/chat/moderators?platform=youtube&account=acct", nil, http.StatusBadRequest)
	if !strings.Contains(string(body), "not connected") {
		t.Errorf("an unconnected YouTube answered %s", body)
	}
}
