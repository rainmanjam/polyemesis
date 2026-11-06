package oauth

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rainmanjam/polyemesis/internal/platform"
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

type installedMeter struct {
	m YouTubeMeter
	// hosts are extra API hosts metered on every path, for a test that aims
	// the providers at a stub. Production installs none.
	hosts map[string]bool
}

var youTubeMeter atomic.Pointer[installedMeter]

// SetYouTubeMeter installs the meter. Nil removes it. hosts are extra hosts
// to meter besides Google's (tests only).
func SetYouTubeMeter(m YouTubeMeter, hosts ...string) {
	if m == nil {
		youTubeMeter.Store(nil)
		return
	}
	im := &installedMeter{m: m, hosts: map[string]bool{}}
	for _, h := range hosts {
		im.hosts[h] = true
	}
	youTubeMeter.Store(im)
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
	ytCostList  = platform.YouTubeCostList
	ytCostWrite = platform.YouTubeCostWrite
	// ytCostUploadVideo is videos.insert.
	ytCostUploadVideo = platform.YouTubeCostUploadVideo
)

// youTubeCost is the unit cost of a request, and whether it is a YouTube
// Data API request at all. Only the production host counts: a request aimed
// at a test stub is not spending anybody's quota.
//
// /upload/youtube/v3/ is the Data API too (R4): thumbnails.set is a write at
// 50, and videos.insert is 1,600. The clip publisher (internal/publish) makes
// that call itself and charges it with ChargeYouTubeQuota.
func youTubeCost(r *http.Request) (int, bool) {
	if r.URL.Host != "www.googleapis.com" {
		return 0, false
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/upload/youtube/v3/videos"):
		return ytCostUploadVideo, true
	case strings.HasPrefix(r.URL.Path, "/upload/youtube/v3/"):
		return ytCostWrite, true
	case !strings.HasPrefix(r.URL.Path, "/youtube/v3/"):
		return 0, false
	}
	return costByMethod(r.Method), true
}

func costByMethod(method string) int {
	if method == http.MethodGet {
		return ytCostList
	}
	return ytCostWrite
}

// ChargeYouTubeQuota spends units from the installed meter, for a YouTube call
// made outside this package's own HTTP client (the clip upload). A missing
// meter is a no-op.
func ChargeYouTubeQuota(units int) {
	if m := youTubeMeter.Load(); m != nil && units > 0 {
		m.m(units)
	}
}

// doRequest sends a request through the shared client, charging the meter
// for a YouTube Data API call that was not charged by its caller.
func doRequest(r *http.Request) (*http.Response, error) {
	if m := youTubeMeter.Load(); m != nil {
		if charged, _ := r.Context().Value(quotaChargedKey{}).(bool); !charged {
			if cost, ok := youTubeCost(r); ok {
				m.m(cost)
			} else if m.hosts[r.URL.Host] {
				m.m(costByMethod(r.Method))
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
	// QuotaExceeded is any of the reasons IsSpentQuotaReason names: the
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
	e.QuotaExceeded = resp.StatusCode == http.StatusForbidden && bodyNamesSpentQuota(body)
	return e
}

// spentQuotaReasons are the reasons YouTube gives for a spent daily API
// allowance. It spells the same condition more than one way, and chat, publish
// and the meter must all recognise the same set.
var spentQuotaReasons = []string{"quotaExceeded", "dailyLimitExceeded", "dailyLimitExceededUnreg"}

// IsSpentQuotaReason reports whether a YouTube error reason means the daily
// allowance is used up. rateLimitExceeded and userRateLimitExceeded are not
// that: they are short windows a retry clears.
func IsSpentQuotaReason(reason string) bool {
	return slices.Contains(spentQuotaReasons, reason)
}

func bodyNamesSpentQuota(body []byte) bool {
	for _, r := range spentQuotaReasons {
		if bytes.Contains(body, []byte(`"`+r+`"`)) {
			return true
		}
	}
	return false
}
