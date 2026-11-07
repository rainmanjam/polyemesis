package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// KICK TOKEN FAILURES.
//
// Kick publishes no token-validation endpoint and no revocation webhook, and it
// answers every unacceptable token the same way: a bare 401 with
// {"data":{},"message":"Unauthorized"} (recorded live). So the only evidence a
// Kick token has gone bad is a 401 on a real call, and the only way to tell "the
// access token alone is bad" from "the whole grant is gone" is to spend one
// refresh on it. Twitch's hourly /validate pass has no Kick equivalent; this is
// the Kick version of the same verdict, reached lazily.

// kickRevokedReason is what the console shows for a Kick account whose refresh
// grant is gone.
const kickRevokedReason = "Kick no longer accepts this account's token and cannot renew it: it was revoked, " +
	"or polyemesis was removed in Kick's connected apps. Reconnect the Kick account in Settings → Platforms."

// kickReauthCooldown is how soon after a forced refresh another may run for the
// same account. A token Kick keeps rejecting must cost one id.kick.com call a
// half-minute, not one per failing request.
const kickReauthCooldown = 30 * time.Second

// markKickRevoked records that a Kick account's grant is gone, unless the row
// was rewritten since seen: a reconnect that landed while the failing refresh
// was in flight has already replaced the grant, and marking it would tell the
// operator to reconnect an account they just reconnected.
func (s *Server) markKickRevoked(acct *db.PlatformAccount, seen db.AccountRevision) {
	cur, err := s.store.GetPlatformAccount(s.box, acct.ID)
	if err != nil || cur.Revision() != seen {
		return
	}
	if s.acctHealth.markRevoked(revokedKey(acct.Platform, acct.AccountRef), kickRevokedReason) {
		s.log.Warn("kick: the account's grant is gone; chat sends stop and the account asks for a reconnect",
			"account", acct.AccountName)
	}
}

// kickRevoked reports whether a Kick account has been found unrecoverable.
func (s *Server) kickRevoked(acct *db.PlatformAccount) bool {
	return acct.Platform == db.PlatformKick &&
		s.acctHealth.revokedReason(revokedKey(acct.Platform, acct.AccountRef)) != ""
}

// errKickReconnect is what every Kick caller sees once the grant is known gone.
func errKickReconnect() error {
	return fmt.Errorf("the Kick grant was revoked and cannot be renewed; reconnect the Kick account in Settings → Platforms: %w",
		oauth.ErrReconnectRequired)
}

// kickReauth is the forced refresh after Kick rejected acct's access token. It
// returns the account with its new token, or why that is not possible. A refresh
// Kick refuses outright marks the account revoked.
func (s *Server) kickReauth(ctx context.Context, acct *db.PlatformAccount) (*db.PlatformAccount, error) {
	if s.kickRevoked(acct) {
		return nil, errKickReconnect()
	}
	if !s.acctHealth.reauthDue(acct.ID, time.Now(), kickReauthCooldown) {
		return nil, errors.New("a token refresh was just tried for this Kick account")
	}
	seen := acct.Revision()
	fresh, err := s.refreshNow(ctx, acct)
	if err != nil {
		if errors.Is(err, oauth.ErrReconnectRequired) {
			s.markKickRevoked(acct, seen)
		}
		return nil, err
	}
	return fresh, nil
}

// kickRetry makes a Kick call with acct's token and, if Kick answers 401, makes
// it once more with a freshly refreshed one. Every other platform's call, and
// every other outcome, passes through untouched.
func (s *Server) kickRetry(ctx context.Context, acct *db.PlatformAccount, call func(token string) error) error {
	err := call(acct.AccessToken)
	if acct.Platform != db.PlatformKick || !oauth.IsTokenInvalid(err) {
		return err
	}
	fresh, rerr := s.kickReauth(ctx, acct)
	if rerr != nil {
		if errors.Is(rerr, oauth.ErrReconnectRequired) {
			return fmt.Errorf("%w; %s", err, kickRevokedReason)
		}
		return err
	}
	return call(fresh.AccessToken)
}

// kickChatReauth is the forced-refresh TokenFunc handed to the Kick chat
// adapter. A grant that is gone is fatal for chat: the adapter stops retrying
// and says to reconnect, instead of asking Kick again every thirty minutes.
func (s *Server) kickChatReauth(id int64) chat.TokenFunc {
	return func(ctx context.Context) (string, error) {
		acct, err := s.store.GetPlatformAccount(s.box, id)
		if err != nil {
			return "", err
		}
		fresh, err := s.kickReauth(ctx, acct)
		if err != nil {
			if errors.Is(err, oauth.ErrReconnectRequired) {
				return "", chat.Fatal(err)
			}
			return "", err
		}
		return fresh.AccessToken, nil
	}
}
