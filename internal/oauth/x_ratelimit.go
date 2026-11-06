package oauth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// X publishes no rate limits for any Livestream endpoint (docs-x section 9, "read the x-rate-limit-* headers and
// back off"). So the limits are learned from the answers: a 429, or a 2xx whose x-rate-limit-remaining is 0, holds
// every further call for that token until x-rate-limit-reset, and a call made during the hold is refused here,
// without a request. Before this, a throttled account was asked again on every fifteen-second sweep, regardless of
// the reset time, and each 429 was counted as an ordinary failure against the lifecycle's give-up budget.

// xRateHoldMax caps a hold. A reset header far in the future (a clock skew, a malformed value) must not silence an
// account for a day.
const xRateHoldMax = 15 * time.Minute

// xRateHoldDefault is the hold after a 429 that names no reset time. PROVISIONAL: X documents the header, not its
// absence.
const xRateHoldDefault = time.Minute

// xUserCacheTTL is how long the answer to GET /2/users/me is reused for a token. Every Livestream address carries the
// numeric user id, and asking for it again on every read doubled the identity calls a sweep made.
const xUserCacheTTL = 10 * time.Minute

// xUserCacheMax bounds the user cache. Every token refresh is a new fingerprint
// and the old one is never read again; expired entries are dropped on write, and
// this is the backstop for a burst of distinct tokens inside one TTL.
const xUserCacheMax = 64

// xCallState is what one X provider remembers between calls, keyed by a fingerprint of the access token so no token
// is held as a map key.
type xCallState struct {
	mu    sync.Mutex
	holds map[string]time.Time
	users map[string]xCachedUser
	// now is the clock; nil is time.Now. A test sets it.
	now func() time.Time
}

type xCachedUser struct {
	user xUser
	at   time.Time
}

func (s *xCallState) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func xTokenFingerprint(accessToken, base string) string {
	sum := sha256.Sum256([]byte(base + "\x00" + accessToken))
	return hex.EncodeToString(sum[:12])
}

// heldUntil reports the end of a hold on this token, if one is running.
func (s *xCallState) heldUntil(fp string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.holds[fp]
	if !ok {
		return time.Time{}, false
	}
	if !s.clock().Before(until) {
		delete(s.holds, fp)
		return time.Time{}, false
	}
	return until, true
}

func (s *xCallState) hold(fp string, until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.holds == nil {
		s.holds = map[string]time.Time{}
	}
	now := s.clock()
	for k, end := range s.holds {
		if !now.Before(end) {
			delete(s.holds, k)
		}
	}
	if until.After(s.holds[fp]) {
		s.holds[fp] = until
	}
}

func (s *xCallState) cachedUser(fp string) (xUser, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.users[fp]
	if !ok || s.clock().Sub(c.at) >= xUserCacheTTL {
		return xUser{}, false
	}
	return c.user, true
}

func (s *xCallState) rememberUser(fp string, u xUser) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.users == nil {
		s.users = map[string]xCachedUser{}
	}
	now := s.clock()
	for k, c := range s.users {
		if now.Sub(c.at) >= xUserCacheTTL {
			delete(s.users, k)
		}
	}
	for len(s.users) >= xUserCacheMax {
		var oldest string
		var oldestAt time.Time
		for k, c := range s.users {
			if oldest == "" || c.at.Before(oldestAt) {
				oldest, oldestAt = k, c.at
			}
		}
		delete(s.users, oldest)
	}
	s.users[fp] = xCachedUser{user: u, at: now}
}

// rateHoldFrom reads when X says the limit resets. ok is false when the answer says nothing about a limit that has
// run out: a 2xx with calls remaining, or with no headers at all.
func (s *xCallState) rateHoldFrom(resp *http.Response) (until time.Time, ok bool) {
	now := s.clock()
	limited := resp.StatusCode == http.StatusTooManyRequests
	if !limited {
		if rem := strings.TrimSpace(resp.Header.Get("x-rate-limit-remaining")); rem == "0" {
			limited = true
		}
	}
	if !limited {
		return time.Time{}, false
	}
	until = now.Add(xRateHoldDefault)
	if v, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("x-rate-limit-reset")), 10, 64); err == nil && v > 0 {
		until = time.Unix(v, 0)
	} else if secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && secs > 0 {
		until = now.Add(time.Duration(secs) * time.Second)
	}
	if until.Before(now.Add(time.Second)) {
		until = now.Add(time.Second)
	}
	if until.After(now.Add(xRateHoldMax)) {
		until = now.Add(xRateHoldMax)
	}
	return until, true
}

// xRateLimitedError is a call X refused with 429, or one refused here because an earlier 429 is still running.
type xRateLimitedError struct {
	Until time.Time
	// Held is true when no request was made.
	Held bool
	// Problems is X's own text, when it sent any.
	Problems string
}

func (e *xRateLimitedError) Error() string {
	until := e.Until.UTC().Format("15:04:05")
	if e.Held {
		return "X is rate-limiting this account; polyemesis makes no X calls for it until " + until + " UTC"
	}
	msg := "X returned 429"
	if e.Problems != "" {
		msg += ": " + e.Problems
	}
	return fmt.Sprintf("%s; polyemesis makes no X calls for this account until %s UTC", msg, until)
}

// XRateLimited reports whether err is X rate-limiting the account, and when the limit resets. A caller that counts
// failures toward a give-up budget does not count this one: it is X asking for a pause, not a refusal.
func XRateLimited(err error) (time.Time, bool) {
	var rl *xRateLimitedError
	if errors.As(err, &rl) {
		return rl.Until, true
	}
	return time.Time{}, false
}
