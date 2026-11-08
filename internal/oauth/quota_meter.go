package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
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
// the published unit costs, to the meter the provider set was built
// with (WithYouTubeMeter; the server passes internal/chat's shared YouTubeQuota). A caller that
// charged its read up front -- the audience sampler, the vertical status read
// -- marks its context with WithQuotaCharged so the call is not counted twice.

// YouTubeMeter is told the unit cost of each YouTube Data API call made.
type YouTubeMeter func(cost int)

// WithYouTubeMeter hands a provider set the meter every YouTube Data API call
// is charged to. It is a construction option rather than a package global, so
// a provider can only be built metered or deliberately unmetered; there is no
// moment after construction at which a call could go uncounted.
func WithYouTubeMeter(m YouTubeMeter) ProviderOption {
	return func(e *endpoints) { e.meter = m }
}

// WithMeteredHosts meters every request to the given extra hosts, on every
// path, besides Google's. For a test that aims the providers at a stub.
func WithMeteredHosts(hosts ...string) ProviderOption {
	return func(e *endpoints) {
		e.meterHosts = map[string]bool{}
		for _, h := range hosts {
			e.meterHosts[h] = true
		}
	}
}

type quotaChargedKey struct{}

// WithQuotaCharged marks requests made under ctx as already charged.
func WithQuotaCharged(ctx context.Context) context.Context {
	return context.WithValue(ctx, quotaChargedKey{}, true)
}

// Unit costs from the YouTube Data API quota table
// (https://developers.google.com/youtube/v3/determine_quota_cost): every list
// is 1, a liveBroadcasts or liveStreams write is 1, and every other write this
// package makes -- thumbnails.set, playlistItems.insert, videos.update -- is 50.
// videos.insert has its own bucket and is not counted here at all.
const (
	ytCostList      = platform.YouTubeCostList
	ytCostWrite     = platform.YouTubeCostWrite
	ytCostLiveWrite = platform.YouTubeCostLiveWrite
)

// youTubeCost is the unit cost of a request, and whether it is a YouTube
// Data API request at all. Only the production host counts: a request aimed
// at a test stub is not spending anybody's quota.
//
// /upload/youtube/v3/ is the Data API too (R4): thumbnails.set is a write at
// 50. videos.insert is 1 unit in a bucket of its own (100 calls a day), so it
// does not spend the shared allowance and is not metered.
func youTubeCost(r *http.Request) (int, bool) {
	if r.URL.Host != "www.googleapis.com" {
		return 0, false
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/upload/youtube/v3/videos"):
		return 0, false
	case strings.HasPrefix(r.URL.Path, "/upload/youtube/v3/"):
		return ytCostWrite, true
	case !strings.HasPrefix(r.URL.Path, "/youtube/v3/"):
		return 0, false
	}
	if r.Method != http.MethodGet && ytLiveResource(r.URL.Path) {
		return ytCostLiveWrite, true
	}
	return costByMethod(r.Method), true
}

// ytLiveResource reports whether a /youtube/v3/ path is liveBroadcasts or
// liveStreams, whose every method the table prices at 1.
func ytLiveResource(path string) bool {
	rest := strings.TrimPrefix(path, "/youtube/v3/")
	return strings.HasPrefix(rest, "liveBroadcasts") || strings.HasPrefix(rest, "liveStreams")
}

func costByMethod(method string) int {
	if method == http.MethodGet {
		return ytCostList
	}
	return ytCostWrite
}

// doRequest sends a request through the shared client, charging the meter
// for a YouTube Data API call that was not charged by its caller.
func (e endpoints) doRequest(r *http.Request) (*http.Response, error) {
	if m := e.meter; m != nil {
		if charged, _ := r.Context().Value(quotaChargedKey{}).(bool); !charged {
			if cost, ok := youTubeCost(r); ok {
				m(cost)
			} else if e.meterHosts[r.URL.Host] {
				m(costByMethod(r.Method))
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
	// Edge is set when what answered was a proxy or CDN in front of the
	// platform (an HTML page, or a non-JSON body with Cloudflare's headers)
	// rather than its API, so the status says nothing about the account.
	Edge bool
	// body is the response body, bounded, kept for a provider that wants the
	// platform's own message back out of it.
	body string
	msg  string
}

func (e *StatusError) Error() string { return e.msg }

func newStatusError(resp *http.Response, body []byte, msg string) *StatusError {
	e := &StatusError{Status: resp.StatusCode, msg: msg, body: string(body[:min(len(body), 4<<10)])}
	e.Edge = isEdgeAnswer(resp.Header, body)
	e.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
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

// parseRetryAfter reads a Retry-After header in either form, delta-seconds or an
// HTTP date. Zero when absent or unreadable.
func parseRetryAfter(ra string) time.Duration {
	ra = strings.TrimSpace(ra)
	if ra == "" {
		return 0
	}
	if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(ra); err == nil {
		return max(0, time.Until(at))
	}
	return 0
}

// isEdgeAnswer reports whether a response is a proxy's page rather than the
// API's JSON: the body is not JSON and either looks like HTML or carries a
// CDN's headers. A real API error is JSON even when it travelled through
// Cloudflare, so the JSON check comes first.
func isEdgeAnswer(h http.Header, body []byte) bool {
	if len(bytes.TrimSpace(body)) == 0 || json.Valid(body) {
		return false
	}
	lead := strings.ToLower(strings.TrimSpace(string(body[:min(len(body), 64)])))
	if strings.HasPrefix(lead, "<") {
		return true
	}
	return h.Get("Cf-Ray") != "" || strings.EqualFold(h.Get("Server"), "cloudflare")
}
