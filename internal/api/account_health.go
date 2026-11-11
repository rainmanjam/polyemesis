package api

import (
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// ACCOUNT HEALTH (A5): what the last token refresh of each connected account
// came to, so the destination dialog and Settings can say "this account is
// failing to refresh" before a broadcast finds out.
//
// Kept in memory, not in the database. It is a reading of the last attempt,
// and the next attempt -- the RefreshLoop tick or any on-demand tokenFor --
// replaces it; after a restart the first refresh writes it again. Storing it
// would add a column whose value is stale the moment the process that wrote it
// exits.
//
// The zero value is ready to use, so Server needs no constructor change.
type accountHealthBook struct {
	mu sync.Mutex
	m  map[int64]refreshOutcome
	// revoked is the accounts whose platform said the token is no longer valid
	// (Twitch's hourly /validate), with the sentence to show. Kept beside the
	// refresh book for the same reason it is in memory: it is a reading, and
	// the next validation pass or a reconnect replaces it.
	revoked map[string]revocation
	// reauthAt is when each account last had a forced refresh, so a token the
	// platform keeps rejecting is not refreshed once per failing request.
	reauthAt map[int64]time.Time
}

type revocation struct {
	reason string
	at     time.Time
}

// revokedKey names an account by what survives a reconnect. The row id does
// too (an upsert keeps it), but a caller that has only (platform, account_ref)
// -- the OAuth callback's attachAccountChat -- can clear by this without a
// lookup that might fail.
func revokedKey(p db.Platform, ref string) string { return string(p) + "/" + ref }

// markRevoked records that the platform no longer honours this account's
// token. Reports whether it is news (it was not already marked).
func (b *accountHealthBook) markRevoked(key, reason string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.revoked == nil {
		b.revoked = make(map[string]revocation)
	}
	_, had := b.revoked[key]
	b.revoked[key] = revocation{reason: reason, at: time.Now()}
	return !had
}

// reauthDue reports whether a forced refresh may run for the account now, and
// if so records that one is starting.
func (b *accountHealthBook) reauthDue(id int64, now time.Time, cooldown time.Duration) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reauthAt == nil {
		b.reauthAt = make(map[int64]time.Time)
	}
	if last, ok := b.reauthAt[id]; ok && now.Sub(last) < cooldown {
		return false
	}
	b.reauthAt[id] = now
	return true
}

// clearRevoked removes the mark and reports whether there was one.
func (b *accountHealthBook) clearRevoked(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, had := b.revoked[key]
	delete(b.revoked, key)
	return had
}

func (b *accountHealthBook) revokedReason(key string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.revoked[key].reason
}

func (b *accountHealthBook) revokedAt(key string) (revocation, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.revoked[key]
	return r, ok
}

// refreshOutcome is one account's run of failed refreshes. Absent from the
// book means the last refresh worked, or none has been needed yet.
type refreshOutcome struct {
	failures  int
	lastError string
	at        time.Time
	// dead is set when the refresh was refused for good (reconnect required),
	// deadExpiry the stored expiry it was refused at. A reconnect stores a new
	// expiry, so a different value means the account is worth trying again.
	dead       bool
	deadExpiry time.Time
}

// markDead records that this account's grant is gone. fail must have run first.
func (b *accountHealthBook) markDead(id int64, expiry time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if o, ok := b.m[id]; ok {
		o.dead, o.deadExpiry = true, expiry
		b.m[id] = o
	}
}

// isDead reports whether the background refresh should skip the account: its
// last refresh was refused for good and nothing has been reconnected since.
func (b *accountHealthBook) isDead(id int64, expiry time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	o, ok := b.m[id]
	return ok && o.dead && o.deadExpiry.Equal(expiry)
}

func (b *accountHealthBook) fail(id int64, err error, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.m == nil {
		b.m = make(map[int64]refreshOutcome)
	}
	o := b.m[id]
	o.failures++
	o.lastError = err.Error()
	o.at = now
	b.m[id] = o
}

func (b *accountHealthBook) ok(id int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.m, id)
}

func (b *accountHealthBook) get(id int64) (refreshOutcome, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	o, has := b.m[id]
	return o, has
}

// Account health states, in the order the UI ranks them.
const (
	AccountHealthOK             = "ok"
	AccountHealthExpiring       = "expiring"
	AccountHealthRefreshFailing = "refreshFailing"
	AccountHealthExpired        = "expired"
)

// expiringWindow is how far ahead an account with no refresh token is called
// expiring. A day is long enough to reconnect before an evening show.
const expiringWindow = 24 * time.Hour

// AccountHealth is the `health` field on GET /platforms/accounts.
type AccountHealth struct {
	State string `json:"state"`
	// Failures is the number of refreshes in a row that failed, and LastError
	// the platform's own words for the latest one. Both absent when healthy.
	Failures  int        `json:"failures,omitempty"`
	LastError string     `json:"lastError,omitempty"`
	FailedAt  *time.Time `json:"failedAt,omitempty"`
}

// accountHealthFor reads one account against the book. A failing refresh
// outranks the token's own expiry, because it names the reason. canRefresh is
// whether the account holds a refresh token; the list query does not read the
// secrets, so the handler looks that up only for a token near its expiry.
func accountHealthFor(a db.PlatformAccount, canRefresh bool, o refreshOutcome, failing bool, now time.Time) AccountHealth {
	if failing && o.failures > 0 {
		at := o.at
		return AccountHealth{State: AccountHealthRefreshFailing, Failures: o.failures, LastError: o.lastError, FailedAt: &at}
	}
	// With a refresh token, an expired access token is routine: the next use
	// refreshes it. Only an account that cannot refresh runs out.
	if !canRefresh && !a.ExpiresAt.IsZero() {
		if !a.ExpiresAt.After(now) {
			return AccountHealth{State: AccountHealthExpired}
		}
		if a.ExpiresAt.Sub(now) < expiringWindow {
			return AccountHealth{State: AccountHealthExpiring}
		}
	}
	return AccountHealth{State: AccountHealthOK}
}

// nearExpiry reports whether an account's access token runs out within the
// expiring window, which is when whether it can refresh starts to matter.
func nearExpiry(a db.PlatformAccount, now time.Time) bool {
	return !a.ExpiresAt.IsZero() && a.ExpiresAt.Sub(now) < expiringWindow
}
