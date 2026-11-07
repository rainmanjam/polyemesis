package oauth

// Graph API failure handling shared by the token refresh, the metadata calls
// and the chat adapter (which imports these rather than keeping its own copy
// of Meta's code tables).
//
// Sources: developers.facebook.com/docs/graph-api/guides/error-handling (190
// is the token family, with subcodes 458/459/460/463/464/467; 102 is an API
// session error) and /docs/graph-api/overview/rate-limiting (4, 17, 32, 613,
// and 80000-80014 for Business Use Case limits, with the X-App-Usage and
// X-Business-Use-Case-Usage headers).

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxGraphWait bounds a server-suggested wait. Graph can ask for an hour; a
// caller that honours that for a chat poll has effectively stopped, so the
// floor is capped and the operator told it is rate-limited meanwhile.
const maxGraphWait = 30 * time.Minute

// IsGraphTokenDead reports whether a Graph error code means the token will not
// work again: 190 (expired, revoked, password changed, app removed) and 102
// (API session). Either can arrive with HTTP 400, not only 401.
func IsGraphTokenDead(code int) bool { return code == 190 || code == 102 }

// IsGraphRateLimit reports whether a Graph error code is a throttle: 4 (app),
// 17 (user), 32 (Pages API), 613 (custom) and the 80000-80014 BUC family.
func IsGraphRateLimit(code int) bool {
	switch code {
	case 4, 17, 32, 613:
		return true
	}
	return code >= 80000 && code <= 80014
}

// GraphErrorFields reads the code and subcode out of a Graph error body; ok is
// false when the body is not Meta's envelope.
func GraphErrorFields(body string) (code, subcode int, ok bool) {
	ge, ok := decodeGraphError(body)
	return ge.Code, ge.Subcode, ok
}

// GraphErrorSummary is the one-line, operator-facing form of a Graph error
// body: Meta's message and code, with no URL, fbtrace_id or raw JSON. Empty
// when the body is not Meta's envelope (an HTML error page, say).
func GraphErrorSummary(body string) string {
	ge, ok := decodeGraphError(body)
	if !ok {
		return ""
	}
	msg := metaMessage(ge)
	if msg == "" {
		return fmt.Sprintf("Graph error %d", ge.Code)
	}
	return fmt.Sprintf("%s (code %d)", msg, ge.Code)
}

// GraphWait is how long Graph asked the caller to hold off: Retry-After
// (seconds or an HTTP date), or the longest estimated_time_to_regain_access (in
// minutes) in X-Business-Use-Case-Usage. X-App-Usage carries only percentages,
// so it yields no wait. Zero means no signal; the result is capped.
func GraphWait(h http.Header) time.Duration {
	var wait time.Duration
	if ra := strings.TrimSpace(h.Get("Retry-After")); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
			wait = time.Duration(secs) * time.Second
		} else if at, err := http.ParseTime(ra); err == nil {
			wait = max(0, time.Until(at))
		}
	}
	if raw := h.Get("X-Business-Use-Case-Usage"); raw != "" {
		var buc map[string][]struct {
			Regain float64 `json:"estimated_time_to_regain_access"`
		}
		if json.Unmarshal([]byte(raw), &buc) == nil {
			for _, list := range buc {
				for _, u := range list {
					wait = max(wait, time.Duration(u.Regain*float64(time.Minute)))
				}
			}
		}
	}
	return min(wait, maxGraphWait)
}

func graphWaitAdvice(wait time.Duration) string {
	if wait > 0 {
		return fmt.Sprintf("Facebook asked for about %s before the next try.", wait.Round(time.Second))
	}
	return "Wait a few minutes and try again."
}

// fbRefreshError turns a refused fb_exchange_token into the right class of
// failure. Graph answers a dead token with HTTP 400 and error.code 190 -- no
// invalid_grant anywhere -- so the shared grantGone cannot see it, and the
// refresh loop and publish worker would treat it as retryable forever.
func fbRefreshError(err error) error {
	var te *tokenStatusError
	if !errors.As(err, &te) {
		return err
	}
	ge, ok := decodeGraphError(te.payload())
	if !ok {
		// Not Meta's envelope: a proxy or edge page. Say so without echoing it.
		return fmt.Errorf("Facebook's token endpoint returned HTTP %d", te.code)
	}
	switch {
	case IsGraphTokenDead(ge.Code):
		return fmt.Errorf("Facebook no longer accepts the saved login (it expired, was revoked, or the password "+
			"changed). Reconnect the Facebook account in Settings → Platforms. Facebook said: %s: %w",
			metaMessage(ge), ErrReconnectRequired)
	case ge.Code == 101 || strings.Contains(strings.ToLower(ge.Message), "client secret"):
		return fmt.Errorf("Facebook rejected the App ID or App Secret; check them in Settings → Platforms. "+
			"Facebook said: %s", metaMessage(ge))
	case IsGraphRateLimit(ge.Code):
		return fmt.Errorf("Facebook is rate-limiting polyemesis, so the token could not be refreshed. %s "+
			"Facebook said: %s", graphWaitAdvice(0), metaMessage(ge))
	}
	return fmt.Errorf("Facebook's token endpoint returned %d: %s", te.code, GraphErrorSummary(te.payload()))
}

// FacebookErrorText is err as one operator-facing line. A raw Graph refusal
// (*statusError) carries the request URL and up to 300 characters of JSON with
// an fbtrace_id; this renders Meta's message and code, or just the status when
// the body is not Meta's envelope (a proxy's HTML page is never echoed). Errors
// already phrased by fbAdvice and transport errors pass through unchanged.
func FacebookErrorText(err error) string {
	var se *statusError
	if !errors.As(err, &se) {
		return err.Error()
	}
	if s := GraphErrorSummary(se.payload()); s != "" {
		return fmt.Sprintf("Facebook returned %d: %s", se.Status, s)
	}
	return fmt.Sprintf("Facebook returned HTTP %d", se.Status)
}
