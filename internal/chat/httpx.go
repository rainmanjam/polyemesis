package chat

// Shared HTTP for the three polling/REST adapters. Twitch needs none of this;
// the other three are all "authenticated JSON over a bearer token", and one
// implementation of that is one place to get the token handling right.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// defaultHTTPTimeout bounds one API call. Chat polling runs on a schedule
// tighter than most of this codebase, and a platform that has stopped
// answering must not hold the loop past its next tick.
const defaultHTTPTimeout = 20 * time.Second

// TokenFunc supplies a fresh access token for each call.
//
// A function rather than a string because tokens expire mid-broadcast and
// internal/oauth already refreshes them: handing the adapter a closure over
// that machinery means chat inherits the refresh instead of dying an hour in
// with a 401 nobody is watching for.
type TokenFunc func(ctx context.Context) (string, error)

// StaticToken adapts a fixed token, for callers that manage refresh
// themselves and for tests.
func StaticToken(tok string) TokenFunc {
	return func(context.Context) (string, error) { return tok, nil }
}

// apiError carries the status alongside the platform's own words, so a caller
// can tell "your token expired" from "you are out of quota" without matching
// on prose.
type apiError struct {
	Status int
	URL    string
	Body   string
	// Reason is the platform's machine-readable code where it publishes one
	// (Google's errors[].reason, Facebook's error.code).
	Reason string
	// full is the untouched body, for parsing: Body is cut at 300 characters
	// for display, and a Graph envelope can be longer than that.
	full string
	// Wait is how long the platform asked callers to hold off (Retry-After, or
	// Graph's estimated_time_to_regain_access); zero when it said nothing.
	Wait time.Duration
	// Raw is the start of the response body, untruncated by shorten, for
	// adapters that must tell the platform's own JSON envelope from an edge
	// (WAF/CDN) HTML page. It is never part of Error().
	Raw []byte
	// Header is the response header, for edge signals such as cf-mitigated.
	Header http.Header
	// RetryAfter is the wait the response asked for: Retry-After, else Twitch's
	// Ratelimit-Reset epoch, capped at 15 minutes (oauth.RetryAfterFromHeaders).
	// Zero when absent or invalid. Wait above is the separate Graph signal
	// (estimated_time_to_regain_access); Rumble, Kick, YouTube and Twitch read
	// RetryAfter, Facebook reads Wait.
	RetryAfter time.Duration
	// Problem is the platform's own words from an RFC 7807 or {errors:[...]} body, with no URLs and nothing else
	// from the body (see oauth.XProblemText). Types is the problem type URIs, for matching, never for display.
	// None of Problem, Types or Header is part of Error().
	Problem string
	Types   string
	// Message is the platform's own sentence from the error envelope: the
	// top-level "message" (Kick, Twitch), else "error.message" (Google); see
	// errorMessage. A string-valued "error" is tolerated but not read. Empty
	// when the body is neither.
	Message string
	// Edge is set when the answer came from a proxy or CDN in front of the
	// platform (an HTML page, or a non-JSON body with Cloudflare's headers)
	// rather than from its API. Its status says nothing about the account.
	Edge bool
	// RateRemaining is Twitch's Ratelimit-Remaining as sent, empty when absent.
	// A 429 with tokens left in the bucket is an endpoint's own cooldown, not an
	// empty bucket.
	RateRemaining string
}

// payload is the body to parse: whole when captured, truncated otherwise.
func (e *apiError) payload() string {
	if e.full != "" {
		return e.full
	}
	return e.Body
}

// Error never carries the raw body. Body stays on the struct for the callers
// that match on it (Kick's duplicate detection) and for debugging; what an
// operator reads is the status, the reason code and the envelope's message.
func (e *apiError) Error() string {
	switch {
	case e.Reason != "" && e.Message != "":
		return fmt.Sprintf("%s returned %d (%s): %s", e.URL, e.Status, e.Reason, e.Message)
	case e.Reason != "":
		return fmt.Sprintf("%s returned %d (%s)", e.URL, e.Status, e.Reason)
	case e.Message != "":
		return fmt.Sprintf("%s returned %d: %s", e.URL, e.Status, e.Message)
	}
	return fmt.Sprintf("%s returned %d", e.URL, e.Status)
}

func retryAfterFrom(err error) time.Duration {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.RetryAfter
	}
	return 0
}

func statusOf(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

func reasonOf(err error) string {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Reason
	}
	return ""
}

// doJSON performs one authenticated request.
//
// The token goes in the Authorization header and never into the URL, so that
// no error, log line or status message built from a URL anywhere in this
// package can leak it — which is exactly how tokens escape in projects that
// pass ?access_token=.
func doJSON(ctx context.Context, hc *http.Client, method, endpoint string, token TokenFunc, payload, out any) error {
	return doJSONHeaders(ctx, hc, method, endpoint, token, nil, payload, out)
}

// doJSONHeaders is doJSON with extra request headers.
//
// It exists for Twitch's Helix API, which rejects a request carrying a valid
// bearer token but no Client-Id. Kept as a separate entry point rather than
// widening doJSON's signature at thirty call sites, and headers are for
// IDENTIFIERS only -- the Authorization header is still built here from the
// TokenFunc, so no caller is able to route a credential through this map.
func doJSONHeaders(ctx context.Context, hc *http.Client, method, endpoint string, token TokenFunc, headers map[string]string, payload, out any) error {
	if hc == nil {
		hc = &http.Client{Timeout: defaultHTTPTimeout}
	}
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	if token != nil {
		tok, err := token(ctx)
		if err != nil {
			// A refresh the platform refused because the grant is gone
			// (revoked, expired for good) cannot be fixed by trying again.
			// Left as a plain error, the Hub would redial the token endpoint
			// every few seconds for ever, showing raw token-endpoint JSON.
			if errors.Is(err, oauth.ErrReconnectRequired) {
				return Fatal(fmt.Errorf("the account's authorization was revoked or has expired; reconnect it in Settings → Platforms: %w", oauth.ErrReconnectRequired))
			}
			return fmt.Errorf("no usable access token: %w", err)
		}
		if tok == "" {
			return fmt.Errorf("no access token; reconnect the account in Settings → Platforms")
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// After Authorization, never before: a caller cannot overwrite the bearer
	// token by putting "Authorization" in this map.
	for k, v := range headers {
		if strings.EqualFold(k, "Authorization") {
			continue
		}
		req.Header.Set(k, v)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &apiError{
			Status:     resp.StatusCode,
			URL:        stripQuery(endpoint),
			Body:       shorten(raw),
			Reason:     errorReason(raw),
			full:       string(raw),
			Wait:       oauth.GraphWait(resp.Header),
			Raw:        raw[:min(len(raw), 4096)],
			Header:     resp.Header,
			RetryAfter: retryAfterOf(resp.Header, time.Now()),
			// Parsed from raw, not Body: Body is cut at 300 characters and a cut envelope does not parse.
			Problem:       oauth.XProblemText(raw),
			Types:         oauth.XProblemTypes(raw),
			Message:       errorMessage(raw),
			Edge:          isEdgeAnswer(resp.Header, raw),
			RateRemaining: strings.TrimSpace(resp.Header.Get("Ratelimit-Remaining")),
		}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		if isEdgeAnswer(resp.Header, raw) {
			return fmt.Errorf("%s returned an unreadable response: it answered %d with a web page instead of JSON; a proxy or the platform's edge may be blocking this server",
				stripQuery(endpoint), resp.StatusCode)
		}
		// A 200 that is not the JSON promised (a truncated body, say) would
		// otherwise surface as "invalid character" with nothing saying which
		// call produced it.
		return fmt.Errorf("%s returned an unreadable response: %w", stripQuery(endpoint), err)
	}
	return nil
}

// isEdgeAnswer reports whether a response is a proxy's page rather than the
// API's JSON: the body is not JSON and either looks like HTML or carries a
// CDN's headers. A real API error, including Kick's, is JSON even when it
// travelled through Cloudflare, so the JSON check comes first.
func isEdgeAnswer(h http.Header, raw []byte) bool {
	if len(bytes.TrimSpace(raw)) == 0 || json.Valid(raw) {
		return false
	}
	lead := strings.ToLower(strings.TrimSpace(string(raw[:min(len(raw), 64)])))
	if strings.HasPrefix(lead, "<") {
		return true
	}
	return h.Get("Cf-Ray") != "" || strings.EqualFold(h.Get("Server"), "cloudflare")
}

// maxRetryAfter caps any wait a platform asks for.
const maxRetryAfter = oauth.MaxRetryAfter

// retryAfterOf reads how long to wait from a response: Retry-After (seconds or
// an HTTP date), else Twitch's Ratelimit-Reset, capped at maxRetryAfter. The
// parsing lives in oauth so the two packages cannot drift.
func retryAfterOf(h http.Header, now time.Time) time.Duration {
	return oauth.RetryAfterFromHeaders(h, now)
}

// errorReason digs the machine-readable code out of the two error envelopes
// these platforms use. An envelope we do not recognise yields empty, and every
// caller treats empty as "decide from the status code".
func errorReason(raw []byte) string {
	var env struct {
		Error struct {
			Status string `json:"status"`
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
			Type string `json:"type"`
			Code int    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return ""
	}
	if len(env.Error.Errors) > 0 && env.Error.Errors[0].Reason != "" {
		return env.Error.Errors[0].Reason
	}
	if env.Error.Status != "" {
		return env.Error.Status
	}
	if env.Error.Type != "" {
		return env.Error.Type
	}
	return ""
}

// errorMessage is the human sentence inside an error envelope, tried in this
// order: the top-level "message" (Kick's {"data":..,"message":".."}, Twitch's
// {"error":"Forbidden","status":403,"message":".."}), then "error.message"
// (Google's nested object). A string-valued "error" is tolerated but not read.
// Shortened, with any URL scrubbed so a link carrying a query string never
// reaches a health line. Empty when the body is none of these.
func errorMessage(raw []byte) string {
	// "error" is an object for Google and a plain string for Twitch
	// ({"error":"Forbidden","status":403,"message":".."}), so it is decoded
	// separately and a mismatch there never costs the top-level message.
	var env struct {
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return ""
	}
	said := strings.TrimSpace(env.Message)
	if said == "" {
		var nested struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(env.Error, &nested) == nil {
			said = nested.Message
		}
	}
	return shorten([]byte(scrubURLs(said)))
}

// scrubURLs drops the query string from any http(s) URL in s.
func scrubURLs(s string) string {
	fields := strings.Fields(s)
	for i, f := range fields {
		if strings.HasPrefix(f, "http://") || strings.HasPrefix(f, "https://") {
			fields[i] = stripQuery(f)
		}
	}
	return strings.Join(fields, " ")
}

// stripQuery keeps a query string out of an error message. Nothing here puts a
// secret in one, and this is the belt to that braces: an endpoint that grows a
// parameter later cannot make an old error message leak it.
func stripQuery(endpoint string) string {
	if i := strings.IndexByte(endpoint, '?'); i >= 0 {
		return endpoint[:i]
	}
	return endpoint
}

func shorten(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		// On a rune boundary: a cut through a multi-byte character is invalid
		// UTF-8 in a message the UI renders.
		cut := 300
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		return s[:cut] + "..."
	}
	return s
}
