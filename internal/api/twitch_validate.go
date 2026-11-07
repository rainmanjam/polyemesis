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
//
// Every verdict is about ONE token, identified by the row revision it was read
// at, and is applied only if the row still has that revision (see
// applyTwitchVerdict): an operator who reconnects while a pass is in flight
// must not have the new token judged on the old one's failure.
func (s *Server) validateTwitchAccount(ctx context.Context, v tokenValidator, a db.PlatformAccount) {
	acct, err := s.tokenFor(ctx, a.ID)
	if err != nil {
		if errors.Is(err, oauth.ErrReconnectRequired) {
			s.applyTwitchVerdict(a, nil, true, "its refresh was refused")
			return
		}
		s.log.Warn("twitch: could not load the account's token to validate it", "account", a.AccountName, "err", err)
		return
	}
	rev := acct.Revision()
	err = v.Validate(ctx, acct.AccessToken)
	if err == nil {
		s.applyTwitchVerdict(a, &rev, false, "")
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
		freshRev := fresh.Revision()
		if err := v.Validate(ctx, fresh.AccessToken); err == nil {
			s.applyTwitchVerdict(a, &freshRev, false, "")
			return
		} else if !oauth.IsTokenInvalid(err) {
			s.log.Warn("twitch: token validation could not be completed; will retry next hour", "account", a.AccountName, "err", err)
			return
		}
		s.applyTwitchVerdict(a, &freshRev, true, "a freshly refreshed token was still refused")
	case errors.Is(rerr, oauth.ErrReconnectRequired):
		// The refusal was of the token read before the refresh: judged at
		// that revision. If a reconnect replaced it meanwhile, it is stale.
		s.applyTwitchVerdict(a, &rev, true, "its refresh was refused")
	default:
		s.log.Warn("twitch: token was refused and could not be refreshed to check; will retry next hour",
			"account", a.AccountName, "err", rerr)
	}
}

// applyTwitchVerdict records what a validation found, under chatWire so it
// cannot interleave with a connect or disconnect, and only if the account is
// still the one that was judged. rev is the revision of the token the verdict
// is about; nil means the row could not be read to get one, and the verdict is
// applied only to a row that is unchanged by the weaker test of still existing.
func (s *Server) applyTwitchVerdict(a db.PlatformAccount, rev *db.AccountRevision, revoked bool, how string) {
	key := revokedKey(a.Platform, a.AccountRef)
	if s.chat != nil {
		s.chatWire.Lock()
		defer s.chatWire.Unlock()
	}
	cur, err := s.store.GetPlatformAccount(s.box, a.ID)
	if err != nil {
		// Deleted (or unreadable): there is nothing to mark, and a disconnect
		// ends its chat on its own.
		return
	}
	if rev != nil && cur.Revision() != *rev {
		s.log.Info("twitch: the account was reconnected while its token was being validated; discarding the verdict",
			"account", a.AccountName)
		return
	}

	if revoked {
		if s.acctHealth.markRevoked(key, twitchRevokedReason) {
			s.log.Warn("twitch: the account's token is no longer valid; ending its chat session and asking for a reconnect",
				"account", a.AccountName, "detail", how)
		}
		if s.chat != nil {
			s.detachChat(db.PlatformTwitch, a.AccountRef)
		}
		return
	}

	// Valid. If it had been marked revoked, its IRC session was ended, and the
	// mark clearing is not the end of the story: put the chat back.
	if !s.acctHealth.clearRevoked(key) {
		return
	}
	s.log.Info("twitch: the account's token validates again; reattaching its chat", "account", a.AccountName)
	if s.chat == nil || s.chatLife == nil {
		return
	}
	if _, attached := s.chat.Adapter(db.PlatformTwitch, a.AccountRef); attached {
		return
	}
	withCreds, err := s.platformsWithCreds()
	if err != nil {
		s.log.Warn("twitch: could not reattach chat after validation; it attaches on the next connect or restart", "err", err)
		return
	}
	s.connectChat(s.chatLife, a, withCreds)
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
		// As tokenFor does: the account's health says "failing to refresh"
		// with the platform's words.
		s.acctHealth.fail(cur.ID, err, time.Now())
		return nil, fmt.Errorf("could not refresh the %s token: %w", cur.Platform, err)
	}
	updated, err := s.store.UpdatePlatformAccountTokens(s.box, cur.ID, seen, tok.AccessToken, tok.RefreshToken, tok.ExpiresAt)
	if errors.Is(err, db.ErrAccountRewritten) {
		// A consent landed meanwhile and knows more than this refresh does.
		return s.store.GetPlatformAccount(s.box, cur.ID)
	}
	if err == nil {
		s.acctHealth.ok(cur.ID)
	}
	return updated, err
}
