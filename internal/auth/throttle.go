package auth

import (
	"net"
	"net/http"
	"strings"
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
	return &Throttle{attempts: make(map[string]*attempt), max: max, now: now}
}

// Retry reports how long key must wait before its next attempt. Zero means
// the attempt may proceed now.
func (t *Throttle) Retry(key string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	a := t.attempts[key]
	if a == nil || now.Sub(a.seen) >= throttleIdleTTL {
		return 0
	}
	if d := a.until.Sub(now); d > 0 {
		return d
	}
	return 0
}

// Fail records a rejected credential check and returns the delay now imposed
// on key.
func (t *Throttle) Fail(key string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
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

	a := t.attempts[key]
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
	delete(t.attempts, key)
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

// ClientIP derives the address a request came from, for rate-limiting keys.
//
// X-Forwarded-For is honoured only when the operator has declared a proxy in
// front of us; otherwise any client could mint a fresh key per request and
// walk straight past the throttle.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// Leftmost is the original client. Trusting it is exactly what
			// trustProxyHeaders asserts: that the proxy rewrites the header
			// rather than appending to whatever the client sent.
			if first, _, ok := strings.Cut(xff, ","); ok {
				return strings.TrimSpace(first)
			}
			return strings.TrimSpace(xff)
		}
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
