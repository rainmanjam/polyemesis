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
}

// refreshOutcome is one account's run of failed refreshes. Absent from the
// book means the last refresh worked, or none has been needed yet.
type refreshOutcome struct {
	failures  int
	lastError string
	at        time.Time
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
