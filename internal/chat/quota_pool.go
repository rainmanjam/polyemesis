package chat

import (
	"sync"
	"time"
)

// One YouTube allowance per Google Cloud client (PR #950 review, items 1-2).
//
// YouTube's daily quota belongs to the developer PROJECT, not to a channel:
// every connected YouTube channel on one client ID, and every feature that
// calls the Data API -- the chat poll, the vertical status read, the
// audience sampler, title push, compliance, the broadcast lifecycle -- spends
// from the same 10,000 units. A budget per chat adapter counted each channel
// against its own private 10,000, and a read made with no chat attached was
// counted against nothing at all.
//
// So the budget lives here, one per client ID, and every spender draws on it:
// the chat adapters through YouTubeConfig.Quota, the vertical read and the
// sampler by charging it before each call, and the rest of the Data API calls
// through the meter internal/oauth runs on every request it sends to YouTube.

// YouTubeQuota is one Google client's daily allowance. Safe for concurrent use.
type YouTubeQuota struct {
	b *budget

	mu sync.Mutex
	// share is units spent today per tag, for spenders held to a fraction
	// of the allowance; shareReset is when it was last zeroed.
	share      map[string]int
	shareReset time.Time
}

func newYouTubeQuota(b *budget) *YouTubeQuota {
	return &YouTubeQuota{b: b, share: map[string]int{}}
}

// Spend records units already spent -- a call that was made. Never refused:
// the platform has already counted it.
func (q *YouTubeQuota) Spend(units int) {
	if q == nil {
		return
	}
	q.b.spend(units)
}

// Afford reports whether a read of this cost fits above the send reserve,
// spending nothing.
func (q *YouTubeQuota) Afford(cost int) bool {
	if q == nil {
		return false
	}
	return q.b.allowRead(cost)
}

// Charge spends a read of this cost when it fits above the send reserve, and
// reports whether it did.
func (q *YouTubeQuota) Charge(cost int) bool {
	if q == nil {
		return false
	}
	q.b.mu.Lock()
	defer q.b.mu.Unlock()
	q.b.rollLocked()
	if q.b.affordable(cost) < 1 {
		return false
	}
	q.b.used += cost
	return true
}

// rollShareLocked zeroes the shares when the allowance has reset.
func (q *YouTubeQuota) rollShareLocked() {
	reset := q.b.status().ResetAt
	if !q.shareReset.Equal(reset) {
		q.share = map[string]int{}
		q.shareReset = reset
	}
}

// shareCap is a fraction of the allowance, in units.
func (q *YouTubeQuota) shareCap(fraction float64) int {
	return int(float64(q.b.status().Limit) * fraction)
}

// ShareLeft is how many units the tag may still spend today under its share.
func (q *YouTubeQuota) ShareLeft(tag string, fraction float64) int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rollShareLocked()
	return max(0, q.shareCap(fraction)-q.share[tag])
}

// AffordShare reports whether the tag may spend cost now: inside its share,
// and inside the read budget. Spends nothing.
func (q *YouTubeQuota) AffordShare(tag string, fraction float64, cost int) bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rollShareLocked()
	return q.share[tag]+cost <= q.shareCap(fraction) && q.b.allowRead(cost)
}

// ChargeShare spends cost for the tag when it fits both its share and the
// read budget, and reports whether it did.
func (q *YouTubeQuota) ChargeShare(tag string, fraction float64, cost int) bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rollShareLocked()
	if q.share[tag]+cost > q.shareCap(fraction) {
		return false
	}
	if !q.Charge(cost) {
		return false
	}
	q.share[tag] += cost
	return true
}

// Pause marks the allowance spent until the reset: what quotaExceeded means,
// whatever our own tally said.
func (q *YouTubeQuota) Pause() {
	if q != nil {
		q.b.pause()
	}
}

// ResetIn is the time until the allowance resets.
func (q *YouTubeQuota) ResetIn() time.Duration {
	if q == nil {
		return 0
	}
	return max(0, q.b.status().ResetAt.Sub(q.b.now()))
}

// Status is the allowance as the UI shows it.
func (q *YouTubeQuota) Status() QuotaStatus {
	if q == nil {
		return QuotaStatus{}
	}
	return q.b.status()
}

// YouTubeQuotaPool holds one YouTubeQuota per Google client ID.
type YouTubeQuotaPool struct {
	mu      sync.Mutex
	units   QuotaUnits
	reserve QuotaReserve
	now     func() time.Time
	byID    map[string]*YouTubeQuota
	last    string
}

// NewYouTubeQuotaPool builds a pool whose budgets start at this allowance.
// now may be nil for time.Now.
func NewYouTubeQuotaPool(units QuotaUnits, reserve QuotaReserve, now func() time.Time) *YouTubeQuotaPool {
	return &YouTubeQuotaPool{units: units, reserve: reserve, now: now, byID: map[string]*YouTubeQuota{}}
}

// For is the budget of one Google client ID, created on first use.
func (p *YouTubeQuotaPool) For(clientID string) *YouTubeQuota {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	q, ok := p.byID[clientID]
	if !ok {
		q = newYouTubeQuota(newBudget(p.units, p.reserve, p.now))
		p.byID[clientID] = q
	}
	p.last = clientID
	return q
}

// Current is the budget of the client most recently asked for. An install
// has one set of YouTube developer credentials, so this is the one the
// request meter charges, without a database read per request.
func (p *YouTubeQuotaPool) Current() *YouTubeQuota {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	id := p.last
	p.mu.Unlock()
	return p.For(id)
}

// SetLimits replaces every client's allowance, and the one new clients start at.
func (p *YouTubeQuotaPool) SetLimits(units, reserve int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.units, p.reserve = QuotaUnits(units), QuotaReserve(reserve)
	for _, q := range p.byID {
		q.b.setLimits(p.units, p.reserve)
	}
}
