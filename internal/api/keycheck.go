package api

import (
	"context"
	"slices"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// keyCrossCheck is the engine.KeyChecker: when a destination's publish was
// refused, it asks the destination's connected account whether the key being
// sent is still one of the account's keys. "No" is the one refusal with a
// one-click fix -- Refresh stream key -- and the card offers exactly that.
//
// Its dependencies are functions rather than a *Server so the test can stand it
// up against a stub platform without a database, the shape lifecycleCoordinator
// already uses.
type keyCrossCheck struct {
	dest      func(id int64) (*db.Destination, error)
	token     func(ctx context.Context, accountID int64) (*db.PlatformAccount, error)
	clientID  func(p db.Platform) (string, error)
	providers oauth.Set
}

// KeyChecker is what main hands the engine manager. Nil-safe for a server
// built without a store, which has nothing to look up.
func (s *Server) KeyChecker() engine.KeyChecker {
	if s.store == nil {
		return nil
	}
	return &keyCrossCheck{
		dest:  s.store.GetDestination,
		token: s.tokenFor,
		clientID: func(p db.Platform) (string, error) {
			creds, err := s.store.GetPlatformCreds(s.box, p)
			if err != nil {
				return "", err
			}
			return creds.ClientID, nil
		},
		providers: s.providers,
	}
}

// StreamKeyChanged implements engine.KeyChecker.
//
// READ-ONLY, through oauth.StreamKeyReader and nothing else: never Ingest or
// IngestFor, which can create a liveStream or a Facebook live video. A platform
// with no read-only key endpoint answers ok=false and the card simply does not
// offer the shortcut.
//
// The comparison is byte for byte against the stored key, with no trimming and
// no case folding (#306).
func (k *keyCrossCheck) StreamKeyChanged(ctx context.Context, destID int64) (changed, ok bool, err error) {
	d, err := k.dest(destID)
	if err != nil {
		return false, false, err
	}
	if d.AccountID == nil || d.StreamKey == "" {
		return false, false, nil
	}
	reader, can := k.providers.StreamKeysFor(d.Platform)
	if !can {
		return false, false, nil
	}
	acct, err := k.token(ctx, *d.AccountID)
	if err != nil {
		return false, false, err
	}
	// The account, not the destination row, says which platform the token is
	// for. A mismatch is a row dropUnsendableSettings has not repaired yet;
	// asking YouTube about a Twitch token can only produce a false "changed".
	if acct.Platform != d.Platform {
		return false, false, nil
	}
	cid, err := k.clientID(acct.Platform)
	if err != nil {
		return false, false, err
	}
	keys, err := reader.StreamKeys(ctx, cid, acct.AccessToken)
	if err != nil {
		return false, false, err
	}
	// An account with no keys at all answers nothing about this one: a channel
	// whose streams were all deleted is "changed" in a sense, but the fix is
	// not one click, and Refresh stream key would create a stream.
	if len(keys) == 0 {
		return false, false, nil
	}
	return !slices.Contains(keys, d.StreamKey), true, nil
}
