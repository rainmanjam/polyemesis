package oauth

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// The YouTube quota meter (PR #950 review, item 2).
//
// Every feature that calls the YouTube Data API spends from ONE daily
// allowance -- the Google Cloud project's -- and most of those calls are made
// here: title and category push, compliance, the broadcast lifecycle,
// rollover, scheduling. None of them used to tell the pacer, so the chat poll
// paced itself against a budget that the rest of polyemesis was quietly
// spending. The meter charges every Data API request this package sends, at
// the published unit costs, to whatever the server installs with
// SetYouTubeMeter (internal/chat's shared YouTubeQuota). A caller that
// charged its read up front -- the audience sampler, the vertical status read
// -- marks its context with WithQuotaCharged so the call is not counted twice.

// YouTubeMeter is told the unit cost of each YouTube Data API call made.
type YouTubeMeter func(cost int)

var youTubeMeter atomic.Pointer[YouTubeMeter]

// SetYouTubeMeter installs the meter. Nil removes it.
func SetYouTubeMeter(m YouTubeMeter) {
	if m == nil {
		youTubeMeter.Store(nil)
		return
	}
	youTubeMeter.Store(&m)
}

type quotaChargedKey struct{}

// WithQuotaCharged marks requests made under ctx as already charged.
func WithQuotaCharged(ctx context.Context) context.Context {
	return context.WithValue(ctx, quotaChargedKey{}, true)
}

// Unit costs from the YouTube Data API quota table: every list is 1, and
// every write this package makes -- insert, update, delete, bind,
// transition, thumbnails.set, playlistItems.insert -- is 50.
const (
	ytCostList  = 1
	ytCostWrite = 50
)

// youTubeCost is the unit cost of a request, and whether it is a YouTube
// Data API request at all. Only the production host counts: a request aimed
// at a test stub is not spending anybody's quota.
func youTubeCost(r *http.Request) (int, bool) {
	if r.URL.Host != "www.googleapis.com" || !strings.HasPrefix(r.URL.Path, "/youtube/v3/") {
		return 0, false
	}
	if r.Method == http.MethodGet {
		return ytCostList, true
	}
	return ytCostWrite, true
}

// doRequest sends a request through the shared client, charging the meter
// for a YouTube Data API call that was not charged by its caller.
func doRequest(r *http.Request) (*http.Response, error) {
	if m := youTubeMeter.Load(); m != nil {
		if charged, _ := r.Context().Value(quotaChargedKey{}).(bool); !charged {
			if cost, ok := youTubeCost(r); ok {
				(*m)(cost)
			}
		}
	}
	return httpClient.Do(r)
}

// StatusError is a non-2xx answer from a platform API, typed so a caller can
// tell a spent quota and a rate limit from any other refusal (item 5).
// Error() is the wording the untyped error used to carry, unchanged.
type StatusError struct {
	Status int
	// RetryAfter is the platform's Retry-After, zero when it sent none.
	RetryAfter time.Duration
	// QuotaExceeded is YouTube's quotaExceeded (or dailyLimitExceeded): the
	// project's allowance is spent until the reset.
	QuotaExceeded bool
	msg           string
}

func (e *StatusError) Error() string { return e.msg }

func newStatusError(resp *http.Response, body []byte, msg string) *StatusError {
	e := &StatusError{Status: resp.StatusCode, msg: msg}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(ra)); err == nil && secs > 0 {
			e.RetryAfter = time.Duration(secs) * time.Second
		} else if at, err := http.ParseTime(ra); err == nil {
			e.RetryAfter = max(0, time.Until(at))
		}
	}
	e.QuotaExceeded = resp.StatusCode == http.StatusForbidden &&
		(bytes.Contains(body, []byte(`"quotaExceeded"`)) || bytes.Contains(body, []byte(`"dailyLimitExceeded"`)))
	return e
}
