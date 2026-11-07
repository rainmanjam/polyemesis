package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// xModAdapter is a connected chat that can list mutes, mute-and-delete and manage moderators.
type xModAdapter struct {
	*fakeAdapter
	banned  []string
	removed []string
	failAdd error
}

func (a *xModAdapter) ListMutes(context.Context) ([]ChatMute, bool, error) {
	return []ChatMute{{UserID: "1"}}, true, nil
}
func (a *xModAdapter) BanDeleting(_ context.Context, userID string, _ time.Duration, _, messageID string) error {
	a.banned = append(a.banned, userID+"/"+messageID)
	return nil
}
func (a *xModAdapter) ListModerators(context.Context) ([]ChatModerator, error) {
	return []ChatModerator{{UserID: "2"}}, nil
}
func (a *xModAdapter) AddModerator(_ context.Context, id string) ([]string, error) {
	return []string{id}, a.failAdd
}
func (a *xModAdapter) RemoveModerator(_ context.Context, id string) ([]string, error) {
	a.removed = append(a.removed, id)
	return nil, nil
}

func TestHubXModerationRoutesToTheAdapter(t *testing.T) {
	h := testHub(t)
	a := &xModAdapter{fakeAdapter: &fakeAdapter{platform: db.PlatformX, account: "acct"}}
	if err := h.Attach(context.Background(), a); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	ctx := context.Background()
	if m, trunc, err := h.ListMutes(ctx, db.PlatformX, "acct"); err != nil || len(m) != 1 || !trunc {
		t.Errorf("ListMutes = %v, %v, %v; want one mute, truncated passed through", m, trunc, err)
	}
	if err := h.BanDeleting(ctx, db.PlatformX, "acct", "555", time.Minute, "spam", "99"); err != nil || len(a.banned) != 1 || a.banned[0] != "555/99" {
		t.Errorf("BanDeleting = %v, banned %v", err, a.banned)
	}
	if err := h.BanDeleting(ctx, db.PlatformX, "acct", "556", 0, "", "98"); err != nil {
		t.Errorf("permanent BanDeleting: %v", err)
	}
	if m, err := h.ListModerators(ctx, db.PlatformX, "acct"); err != nil || len(m) != 1 {
		t.Errorf("ListModerators = %v, %v", m, err)
	}
	if ids, err := h.AddModerator(ctx, db.PlatformX, "acct", "3"); err != nil || len(ids) != 1 {
		t.Errorf("AddModerator = %v, %v", ids, err)
	}
	if _, err := h.RemoveModerator(ctx, db.PlatformX, "acct", "3"); err != nil || len(a.removed) != 1 {
		t.Errorf("RemoveModerator = %v, removed %v", err, a.removed)
	}
	a.failAdd = errors.New("limit")
	if _, err := h.AddModerator(ctx, db.PlatformX, "acct", "4"); err == nil {
		t.Error("an adapter failure was swallowed")
	}
}

func TestHubXModerationRefusesWhatThePlatformCannotDo(t *testing.T) {
	h := testHub(t)
	plain := &fakeAdapter{platform: db.PlatformTwitch, account: "acct"}
	if err := h.Attach(context.Background(), plain); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	ctx := context.Background()
	calls := map[string]func() error{
		"mutes":     func() error { _, _, err := h.ListMutes(ctx, db.PlatformTwitch, "acct"); return err },
		"banDelete": func() error { return h.BanDeleting(ctx, db.PlatformTwitch, "acct", "1", 0, "", "2") },
		"listMods":  func() error { _, err := h.ListModerators(ctx, db.PlatformTwitch, "acct"); return err },
		"addMod":    func() error { _, err := h.AddModerator(ctx, db.PlatformTwitch, "acct", "1"); return err },
		"removeMod": func() error { _, err := h.RemoveModerator(ctx, db.PlatformTwitch, "acct", "1"); return err },
	}
	for name, call := range calls {
		if err := call(); err == nil || !strings.Contains(err.Error(), "twitch") {
			t.Errorf("%s on a Twitch chat = %v, want a refusal naming the platform", name, err)
		}
	}
	// Not connected at all.
	if _, _, err := h.ListMutes(ctx, db.PlatformX, "none"); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Errorf("unconnected ListMutes = %v", err)
	}
	if err := h.BanDeleting(ctx, db.PlatformX, "none", "1", 0, "", "2"); err == nil {
		t.Error("unconnected BanDeleting succeeded")
	}
}
