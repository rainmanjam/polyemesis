package api

import (
	"errors"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/publish"
)

// The publish worker ends a publish as needs_reconnect only on the typed
// sentinel, so the API's token source must produce it for a grant that cannot
// be refreshed: here an expired token with no refresh token.
func TestPublishTokensTypeAGrantThatCannotBeRefreshed(t *testing.T) {
	s, _, store := testServer(t, config.Config{})
	acct, err := store.UpsertPlatformAccount(s.box, &db.PlatformAccount{
		Platform: db.PlatformYouTube, AccountName: "chan", AccountRef: "chan-ref",
		AccessToken: "at", ExpiresAt: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	_, err = s.PublishTokens()(t.Context(), acct.ID)
	if !errors.Is(err, publish.ErrReconnectRequired) {
		t.Fatalf("PublishTokens = %v, want publish.ErrReconnectRequired", err)
	}
}
