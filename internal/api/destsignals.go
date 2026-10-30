package api

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// The two DestStatus signals whose facts live here rather than in the engine:
// the connected account's health (the book behind GET /platforms/accounts) and
// the platform's own verdict on the stream (a liveStreams read through the
// account's token). Both reach the engine through an interface, KeyChecker's
// pattern, because internal/api imports internal/engine and not the reverse.

// accountHealthNow is one account's health as GET /platforms/accounts reports
// it. The one derivation, so the destination dialog's chip and the Stream Deck
// key cannot disagree about the same account.
func (s *Server) accountHealthNow(a db.PlatformAccount, now time.Time) AccountHealth {
	o, failing := s.acctHealth.get(a.ID)
	// The list read carries no secrets, so whether a near-expiry account can
	// refresh is one extra read, for that account only. A failed read assumes
	// it can: "expired" is a claim this must not make on a guess.
	canRefresh := true
	if nearExpiry(a, now) {
		if full, err := s.store.GetPlatformAccount(s.box, a.ID); err == nil {
			canRefresh = full.RefreshToken != ""
		}
	}
	return accountHealthFor(a, canRefresh, o, failing, now)
}

// accountHealthTTL is how long one read of every account's health serves the
// status. Status runs per WebSocket push; a database read per destination per
// push would be the cost, and a refresh that failed a few seconds ago showing a
// few seconds late is not.
const accountHealthTTL = 15 * time.Second

// accountHealthCache is the engine.AccountHealthReader: every account's state
// word, read in one list and kept for accountHealthTTL.
type accountHealthCache struct {
	list   func() ([]db.PlatformAccount, error)
	health func(db.PlatformAccount, time.Time) AccountHealth
	now    func() time.Time

	mu     sync.Mutex
	at     time.Time
	states map[int64]string
}

// AccountHealth implements engine.AccountHealthReader. Never touches the
// network: the list and the near-expiry secrets read are both local.
func (c *accountHealthCache) AccountHealth(id int64) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.states == nil || now.Sub(c.at) >= accountHealthTTL {
		accts, err := c.list()
		if err != nil {
			// Keep the last answer rather than clearing it: a store hiccup
			// must not make a "Reconnect account" key flick back to fine.
			return c.states[id]
		}
		next := make(map[int64]string, len(accts))
		for _, a := range accts {
			next[a.ID] = c.health(a, now).State
		}
		c.states, c.at = next, now
	}
	return c.states[id]
}

// AccountHealth is what main hands the engine manager. Nil for a server with no
// store, which has no accounts.
func (s *Server) AccountHealth() engine.AccountHealthReader {
	if s.store == nil {
		return nil
	}
	return &accountHealthCache{list: s.store.ListPlatformAccounts, health: s.accountHealthNow, now: time.Now}
}

// channelHealthTTL is how long one channel's liveStreams read answers for every
// destination on it. Under the engine's five-minute cadence per destination, so
// the destinations of one channel share a read rather than each spending one.
const channelHealthTTL = 4 * time.Minute

// platformHealthCheck is the engine.PlatformHealthChecker. Its dependencies are
// functions for keyCrossCheck's reason: the test stands it up against a stub
// platform with no database.
type platformHealthCheck struct {
	dest      func(id int64) (*db.Destination, error)
	token     func(ctx context.Context, accountID int64) (*db.PlatformAccount, error)
	clientID  func(p db.Platform) (string, error)
	providers oauth.Set
	now       func() time.Time

	mu       sync.Mutex
	channels map[int64]channelHealth
}

// channelHealth is one account's streams, keyed by the SHA-256 of each stream
// key, so the cache never holds a plaintext key.
type channelHealth struct {
	at       time.Time
	verdicts map[[32]byte]oauth.StreamHealthVerdict
}

// StreamHealth implements engine.PlatformHealthChecker.
//
// READ-ONLY, through oauth.StreamHealthReader and nothing else. ok=false for a
// destination with no account, a platform with no health API (Twitch among
// them), an account on another platform, or a key the channel does not list.
func (k *platformHealthCheck) StreamHealth(ctx context.Context, destID int64) (engine.PlatformHealth, bool, error) {
	d, err := k.dest(destID)
	if err != nil {
		return engine.PlatformHealth{}, false, err
	}
	if d.AccountID == nil || d.StreamKey == "" {
		return engine.PlatformHealth{}, false, nil
	}
	reader, can := k.providers.StreamHealthFor(d.Platform)
	if !can {
		return engine.PlatformHealth{}, false, nil
	}
	verdicts, ok, err := k.channel(ctx, *d.AccountID, d.Platform, reader)
	if err != nil || !ok {
		return engine.PlatformHealth{}, false, err
	}
	v, found := verdicts[sha256.Sum256([]byte(d.StreamKey))]
	if !found {
		return engine.PlatformHealth{}, false, nil
	}
	return engine.PlatformHealth{Platform: d.Platform, Status: v.Status, Issues: v.Issues}, true, nil
}

// channel is one account's verdicts, from the cache while it is fresh.
func (k *platformHealthCheck) channel(ctx context.Context, accountID int64, p db.Platform, reader oauth.StreamHealthReader) (map[[32]byte]oauth.StreamHealthVerdict, bool, error) {
	k.mu.Lock()
	c, has := k.channels[accountID]
	k.mu.Unlock()
	if has && k.now().Sub(c.at) < channelHealthTTL {
		return c.verdicts, true, nil
	}
	acct, err := k.token(ctx, accountID)
	if err != nil {
		return nil, false, err
	}
	// The account, not the row, says which platform the token is for; see
	// keyCrossCheck.StreamKeyChanged.
	if acct.Platform != p {
		return nil, false, nil
	}
	cid, err := k.clientID(acct.Platform)
	if err != nil {
		return nil, false, err
	}
	verdicts, err := reader.StreamHealths(ctx, cid, acct.AccessToken)
	if err != nil {
		return nil, false, err
	}
	k.mu.Lock()
	if k.channels == nil {
		k.channels = map[int64]channelHealth{}
	}
	k.channels[accountID] = channelHealth{at: k.now(), verdicts: verdicts}
	k.mu.Unlock()
	return verdicts, true, nil
}

// PlatformHealth is what main hands the engine manager. Nil-safe for a server
// built without a store.
func (s *Server) PlatformHealth() engine.PlatformHealthChecker {
	if s.store == nil {
		return nil
	}
	return &platformHealthCheck{
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
		now:       time.Now,
	}
}
