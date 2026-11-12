package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// appealAdapter is a moderatable Twitch connection whose channel tools answer
// only the unban-request resolution; any other tool call panics on the nil
// embedded interface, which a test would surface loudly.
type appealAdapter struct {
	*moderatableAdapter
	chat.TwitchTools
}

// Both embedded types declare these, so the ambiguity is settled by hand.
func (a *appealAdapter) Platform() db.Platform { return a.moderatableAdapter.Platform() }
func (a *appealAdapter) Account() string       { return a.moderatableAdapter.Account() }
func (a *appealAdapter) Run(ctx context.Context, sink chat.Receiver) error {
	return a.moderatableAdapter.Run(ctx, sink)
}

func (a *appealAdapter) ResolveUnbanRequest(_ context.Context, id, status, _ string) (chat.UnbanRequest, error) {
	return chat.UnbanRequest{ID: id, UserID: "u-1", Status: status}, nil
}

// Approving an appeal lifts the ban on Twitch; polyemesis's own record of it
// must clear too, or the user card keeps saying "ban".
func TestApprovingAnAppealClearsTheRecordedBan(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	hub := chat.New(chat.WithStore(store))
	t.Cleanup(hub.Close)
	s.chat = hub
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := hub.Attach(ctx, &appealAdapter{moderatableAdapter: &moderatableAdapter{platform: db.PlatformTwitch, account: "acct"}}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	sign := login(t, h)

	send(t, h, sign, http.MethodPost, "/api/v1/chat/bans?platform=twitch&account=acct&userId=u-1", nil, http.StatusOK)
	if c := readCard(t, h, sign, "platform=twitch&account=acct&authorId=u-1"); c.Sanction == nil || c.Sanction.Action != "ban" {
		t.Fatalf("setup: card carries %+v, want a ban", c.Sanction)
	}

	// A denied appeal leaves the ban in force.
	send(t, h, sign, http.MethodPatch, "/api/v1/chat/twitch/unban-requests?account=acct", map[string]string{"id": "r1", "status": "denied"}, http.StatusOK)
	if c := readCard(t, h, sign, "platform=twitch&account=acct&authorId=u-1"); c.Sanction == nil || c.Sanction.Action != "ban" {
		t.Fatalf("after a denied appeal the card carries %+v, want the ban still in force", c.Sanction)
	}

	send(t, h, sign, http.MethodPatch, "/api/v1/chat/twitch/unban-requests?account=acct", map[string]string{"id": "r1", "status": "approved"}, http.StatusOK)
	if c := readCard(t, h, sign, "platform=twitch&account=acct&authorId=u-1"); c.Sanction == nil || c.Sanction.Action != "none" {
		t.Fatalf("after an approved appeal the card carries %+v, want action \"none\"", c.Sanction)
	}
}
