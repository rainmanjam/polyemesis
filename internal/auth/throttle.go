package auth

import (
	"net/netip"
	"sync"
	"time"
)

const (
	// throttleFreeAttempts is how many failures a caller gets before any delay
	// applies. A fat-fingered password twice in a row must not cost the admin
	// a wait.
	throttleFreeAttempts = 5
	// throttleBaseDelay is the penalty for the first failure past the free
	// allowance; it doubles with each subsequent one.
	throttleBaseDelay = 2 * time.Second
	// throttleMaxDelay caps the doubling. Without a cap a determined attacker
	// could push the admin's own address into an effectively permanent
	// lockout, which trades one denial of service for another.
	throttleMaxDelay = 5 * time.Minute
	// throttleIdleTTL forgets a counter that has seen no traffic. This is the
	// second guarantee that the admin recovers: walk away, come back, clean
	// slate.
	throttleIdleTTL = time.Hour
	// throttleMaxEntries bounds the map. Every distinct source address is a
	// key, so an attacker with a large address pool would otherwise be handed
	// an unbounded allocator.
	throttleMaxEntries = 4096

	// throttleGlobalBurst and throttleGlobalRefill are the budget every key
	// shares: at most this many counted attempts back to back, then one more
	// per refill interval, whoever makes them.
	//
	// WHY THERE IS ONE AT ALL. The per-key counter bounds one address. It does
	// not bound an attacker with many: each fresh address gets five free
	// attempts, and past throttleMaxEntries the LRU evicts the counters that
	// were doing the throttling, so a big enough pool guesses without limit.
	// IPv6 made that pool free -- a VPS routinely gets a /64 -- which the /64
	// keying below closes for one allocation but not for many.
	//
	// WHY IT IS THIS LOOSE. A global limit is the denial of service the
	// per-address design was chosen to avoid: while it is spent, the admin's
	// own sign-in waits too. So it is set far above anything a person does --
	// a hundred failures in a burst, then one a second, 3,600 an hour -- and
	// it only ever makes an attempt wait for the next token, never for
	// minutes. It turns "unbounded" into "bounded"; the per-address penalty
	// is still what stops a single guesser.
	throttleGlobalBurst  = 100
	throttleGlobalRefill = time.Second
)

// Throttle rate-limits failed credential checks per client address.
//
// In-process and non-persistent on purpose: polyemesis is a single process,
// and a counter that survives a restart is a counter that can strand the
// admin outside their own server.
type Throttle struct {
	mu       sync.Mutex
	attempts map[string]*attempt
	max      int
	now      func() time.Time

	// tokens is the shared budget (see throttleGlobalBurst), as of tokensAt.
	// A fractional count, refilled lazily on each call.
	tokens   float64
	tokensAt time.Time
}

type attempt struct {
	failures int
	// until is the earliest time the next attempt may be made.
	until time.Time
	// seen is the last time this key was touched, for idle expiry and for
	// choosing an eviction victim.
	seen time.Time
}

// NewThrottle creates a login throttle with the default policy.
func NewThrottle() *Throttle {
	return newThrottle(throttleMaxEntries, time.Now)
}

func newThrottle(max int, now func() time.Time) *Throttle {
	return &Throttle{attempts: make(map[string]*attempt), max: max, now: now,
		tokens: throttleGlobalBurst, tokensAt: now()}
}

// throttleKey is the bucket an address is counted in.
//
// AN IPv6 CLIENT IS ITS /64, NOT ITS ADDRESS. A /64 is the smallest
// allocation a host is normally given -- one VPS, one home connection -- and
// every address inside it belongs to the same party, who can use any of them
// at will. Keyed on the full address, each of those 2^64 addresses got its
// own five free attempts, so a single VPS could guess without limit.
//
// An IPv4-mapped IPv6 address (::ffff:192.0.2.1, which a dual-stack listener
// reports for IPv4 clients) is the IPv4 address, or one client would hold two
// counters. Anything that is not an address -- a header value from a trusted
// proxy that was not one -- is its own key, unchanged, as before.
func throttleKey(k string) string {
	a, err := netip.ParseAddr(k)
	if err != nil {
		return k
	}
	a = a.WithZone("").Unmap()
	if a.Is4() {
		return a.String()
	}
	return netip.PrefixFrom(a, 64).Masked().String()
}

// refillLocked brings the shared budget up to now.
func (t *Throttle) refillLocked(now time.Time) {
	if el := now.Sub(t.tokensAt); el > 0 {
		t.tokens += float64(el) / float64(throttleGlobalRefill)
		if t.tokens > throttleGlobalBurst {
			t.tokens = throttleGlobalBurst
		}
	}
	t.tokensAt = now
}

// globalWaitLocked is how long until the shared budget has a whole attempt
// in it again, or zero when it has one now.
func (t *Throttle) globalWaitLocked(now time.Time) time.Duration {
	t.refillLocked(now)
	if t.tokens >= 1 {
		return 0
	}
	return time.Duration((1 - t.tokens) * float64(throttleGlobalRefill))
}

// Retry reports how long key must wait before its next attempt. Zero means
// the attempt may proceed now.
//
// The longer of two waits: key's own penalty, and the shared budget's (see
// throttleGlobalBurst), so a pool of addresses cannot out-guess the limit
// that stops one.
func (t *Throttle) Retry(key string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	key = throttleKey(key)
	now := t.now()
	wait := t.globalWaitLocked(now)
	a := t.attempts[key]
	if a == nil || now.Sub(a.seen) >= throttleIdleTTL {
		return wait
	}
	if d := a.until.Sub(now); d > wait {
		return d
	}
	return wait
}

// Fail records a rejected credential check and returns the delay now imposed
// on key.
func (t *Throttle) Fail(key string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	key = throttleKey(key)
	now := t.now()
	t.refillLocked(now)
	// Floored at zero: the handlers only call Fail for an attempt Retry let
	// through, so a debt could only come from a caller that skipped Retry,
	// and it must not lock everyone out for longer than one refill.
	if t.tokens -= 1; t.tokens < 0 {
		t.tokens = 0
	}
	a := t.attempts[key]
	if a == nil || now.Sub(a.seen) >= throttleIdleTTL {
		t.evictLocked(now)
		a = &attempt{}
		t.attempts[key] = a
	}
	a.failures++
	a.seen = now
	d := penalty(a.failures)
	a.until = now.Add(d)
	return d
}

// Failures reports how many consecutive rejected credential checks key has
// accumulated. Zero for an address that has never failed, and zero once
// Succeed has run.
//
// It exists so the alert path can say something true rather than something
// alarming. A sign-in alert published on every single failure is one an
// operator mutes the first time they mistype their own password, so the
// handler has to know whether this failure is past the free allowance -- and a
// successful sign-in that follows a run of failures is a different event from
// one that follows none, which is a distinction only this counter holds and
// only until Succeed clears it.
//
// Idle expiry is honoured for the same reason Retry honours it: throttleIdleTTL
// is the promise that walking away and coming back is a clean slate, and a
// count that outlived it would attribute yesterday's guessing to today's
// sign-in.
func (t *Throttle) Failures(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()

	a := t.attempts[throttleKey(key)]
	if a == nil || t.now().Sub(a.seen) >= throttleIdleTTL {
		return 0
	}
	return a.failures
}

// Succeed clears the counter for key, so a correct password immediately
// restores full speed.
func (t *Throttle) Succeed(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.attempts, throttleKey(key))
}

// penalty is the wait after n consecutive failures.
func penalty(n int) time.Duration {
	over := n - throttleFreeAttempts
	if over <= 0 {
		return 0
	}
	// Shift past 63 overflows the int64 the duration lives in, and anything
	// past the cap is the same answer anyway.
	if over > 62 {
		return throttleMaxDelay
	}
	d := throttleBaseDelay << (over - 1)
	if d > throttleMaxDelay || d <= 0 {
		return throttleMaxDelay
	}
	return d
}

// evictLocked makes room for one new key. Idle entries go first; if none are
// idle the least recently seen is dropped, which is the entry whose attacker
// has already given up.
func (t *Throttle) evictLocked(now time.Time) {
	if len(t.attempts) < t.max {
		return
	}
	oldestKey, oldestSeen := "", time.Time{}
	for k, a := range t.attempts {
		if now.Sub(a.seen) >= throttleIdleTTL {
			delete(t.attempts, k)
			continue
		}
		if oldestKey == "" || a.seen.Before(oldestSeen) {
			oldestKey, oldestSeen = k, a.seen
		}
	}
	if len(t.attempts) >= t.max && oldestKey != "" {
		delete(t.attempts, oldestKey)
	}
}
