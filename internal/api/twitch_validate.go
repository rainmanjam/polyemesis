package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// TWITCH TOKEN VALIDATION. Twitch requires every app that holds a user's OAuth
// token to call GET https://id.twitch.tv/oauth2/validate when it starts and
// every hour after, and to end every session that uses a token the answer
// says is no longer valid (dev.twitch.tv/docs/authentication/validate-tokens).
// The reason is that a user can disconnect the integration, or a token can be
// revoked, without this app calling any API that would notice: an IRC session
// sits idle and never meets the 401.
//
// What "end the session" means here: the account's chat adapter is detached
// (the IRC connection closes), the account is marked so the console says it
// needs reconnecting, and nothing else is touched. Reconnecting replaces the
// token, clears the mark and attaches a fresh adapter (attachAccountChat).

// twitchValidateEvery is Twitch's required cadence.
const twitchValidateEvery = time.Hour

// twitchRevokedReason is what the console shows.
const twitchRevokedReason = "Twitch reports this account's token is no longer valid: it was revoked, or you disconnected " +
	"polyemesis from your Twitch connections. Reconnect the Twitch account in Settings → Platforms."

// tokenValidator is a provider that can ask its platform whether a token is
// still valid. Twitch's is the one that must.
type tokenValidator interface {
	Validate(ctx context.Context, accessToken string) error
}

// StartTwitchValidation validates every connected Twitch token now and then
// hourly until ctx ends. Called by main after StartChat, under the server's
// lifetime. Nothing here fails the start.
func (s *Server) StartTwitchValidation(ctx context.Context) {
	if s.store == nil {
		return
	}
	t := time.NewTicker(twitchValidateEvery)
	go func() {
		defer t.Stop()
		s.twitchValidationLoop(ctx, t.C)
	}()
}

// twitchValidationLoop runs one pass at once and one per tick. The ticks come
// in as a channel so a test drives the cadence.
func (s *Server) twitchValidationLoop(ctx context.Context, ticks <-chan time.Time) {
	s.validateTwitchTokens(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			s.validateTwitchTokens(ctx)
		}
	}
}

// validateTwitchTokens is one pass over every connected Twitch account.
func (s *Server) validateTwitchTokens(ctx context.Context) {
	prov, err := s.providers.Get(db.PlatformTwitch)
	if err != nil {
		return
	}
	v, ok := prov.(tokenValidator)
	if !ok {
		return
	}
	accts, err := s.store.ListPlatformAccounts()
	if err != nil {
		s.log.Warn("twitch: could not list accounts to validate their tokens", "err", err)
		return
	}
	for _, a := range accts {
		if a.Platform != db.PlatformTwitch {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		s.validateTwitchAccount(ctx, v, a)
	}
}

// validateTwitchAccount judges one account. Only a definite "the grant is
// gone" ends anything: a network failure or a 5xx is "could not tell", and
// the next pass tries again.
func (s *Server) validateTwitchAccount(ctx context.Context, v tokenValidator, a db.PlatformAccount) {
	acct, err := s.tokenFor(ctx, a.ID)
	if err != nil {
		if errors.Is(err, oauth.ErrReconnectRequired) {
			s.twitchTokenRevoked(a, "its refresh was refused")
			return
		}
		s.log.Warn("twitch: could not load the account's token to validate it", "account", a.AccountName, "err", err)
		return
	}
	err = v.Validate(ctx, acct.AccessToken)
	if err == nil {
		s.acctHealth.clearRevoked(a.ID)
		return
	}
	if !oauth.IsTokenInvalid(err) {
		s.log.Warn("twitch: token validation could not be completed; will retry next hour", "account", a.AccountName, "err", err)
		return
	}

	// A 401. Before calling the grant gone, spend one refresh: a token whose
	// recorded expiry is wrong is stale, not revoked, and a reconnect prompt
	// for it would be a false alarm. A refresh Twitch refuses outright (the
	// grant is revoked, or the integration disconnected) is the real verdict.
	fresh, rerr := s.refreshNow(ctx, acct)
	switch {
	case rerr == nil:
		if err := v.Validate(ctx, fresh.AccessToken); err == nil {
			s.acctHealth.clearRevoked(a.ID)
			return
		} else if !oauth.IsTokenInvalid(err) {
			s.log.Warn("twitch: token validation could not be completed; will retry next hour", "account", a.AccountName, "err", err)
			return
		}
		s.twitchTokenRevoked(a, "a freshly refreshed token was still refused")
	case errors.Is(rerr, oauth.ErrReconnectRequired):
		s.twitchTokenRevoked(a, "its refresh was refused")
	default:
		s.log.Warn("twitch: token was refused and could not be refreshed to check; will retry next hour",
			"account", a.AccountName, "err", rerr)
	}
}

// twitchTokenRevoked ends the account's chat session and marks it as needing a
// reconnect.
func (s *Server) twitchTokenRevoked(a db.PlatformAccount, how string) {
	if s.acctHealth.markRevoked(a.ID, twitchRevokedReason) {
		s.log.Warn("twitch: the account's token is no longer valid; ending its chat session and asking for a reconnect",
			"account", a.AccountName, "detail", how)
	}
	if s.chat == nil {
		return
	}
	s.chatWire.Lock()
	defer s.chatWire.Unlock()
	s.detachChat(db.PlatformTwitch, a.AccountRef)
}

// refreshNow refreshes an account's token whether or not it looks expired, and
// stores the result the way tokenFor does (the compare-and-swap writer, so a
// reconnect that lands meanwhile is not overwritten).
func (s *Server) refreshNow(ctx context.Context, acct *db.PlatformAccount) (*db.PlatformAccount, error) {
	unlock := refreshLocks.Lock(acct.ID)
	defer unlock()
	// Re-read under the lock: a reconnect or another refresh may have replaced
	// the row since the caller read it, and its token is the one to use.
	cur, err := s.store.GetPlatformAccount(s.box, acct.ID)
	if err != nil {
		return nil, err
	}
	if cur.AccessToken != acct.AccessToken {
		return cur, nil
	}
	if cur.RefreshToken == "" {
		return nil, fmt.Errorf("the %s token was refused and there is no refresh token: %w", cur.Platform, oauth.ErrReconnectRequired)
	}
	provider, err := s.providers.Get(cur.Platform)
	if err != nil {
		return nil, err
	}
	creds, err := s.store.GetPlatformCreds(s.box, cur.Platform)
	if err != nil {
		return nil, fmt.Errorf("developer credentials for %s are missing", cur.Platform)
	}
	seen := cur.Revision()
	tok, err := provider.Refresh(ctx, creds.ClientID, creds.ClientSecret, cur.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("could not refresh the %s token: %w", cur.Platform, err)
	}
	updated, err := s.store.UpdatePlatformAccountTokens(s.box, cur.ID, seen, tok.AccessToken, tok.RefreshToken, tok.ExpiresAt)
	if errors.Is(err, db.ErrAccountRewritten) {
		// A consent landed meanwhile and knows more than this refresh does.
		return s.store.GetPlatformAccount(s.box, cur.ID)
	}
	return updated, err
}
