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
	"strconv"
	"strings"
	"time"
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
	// Message is the plain-text reason from Twitch's {"error","status","message"}
	// envelope, which errorReason's nested envelopes do not cover.
	Message string
	// RetryAfter is how long the platform said to wait (Retry-After, or Twitch's
	// Ratelimit-Reset epoch), zero when it said nothing.
	RetryAfter time.Duration
}

func (e *apiError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("%s returned %d (%s): %s", e.URL, e.Status, e.Reason, e.Body)
	}
	return fmt.Sprintf("%s returned %d: %s", e.URL, e.Status, e.Body)
}

func statusOf(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

func reasonOf(err error) string {
	if ae, ok := err.(*apiError); ok {
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
			Message:    envelopeMessage(raw),
			RetryAfter: retryAfterOf(resp.Header, time.Now()),
		}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		// A 200 that is not the JSON promised (an HTML error page from a proxy,
		// a truncated body) would otherwise surface as "invalid character '<'"
		// with nothing saying which call produced it.
		return fmt.Errorf("%s returned an unreadable response: %w", stripQuery(endpoint), err)
	}
	return nil
}

// maxRetryAfter caps any wait a platform asks for: a bad header must not park
// a caller for hours.
const maxRetryAfter = 15 * time.Minute

// retryAfterOf reads how long to wait from a response: Retry-After (seconds or
// an HTTP date), else Twitch's Ratelimit-Reset, a Unix epoch of when the bucket
// refills (dev.twitch.tv/docs/api/guide, "Keeping track of your usage").
func retryAfterOf(h http.Header, now time.Time) time.Duration {
	var d time.Duration
	if ra := strings.TrimSpace(h.Get("Retry-After")); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
			d = time.Duration(secs) * time.Second
		} else if at, err := http.ParseTime(ra); err == nil {
			d = at.Sub(now)
		}
	} else if rs := strings.TrimSpace(h.Get("Ratelimit-Reset")); rs != "" {
		if epoch, err := strconv.ParseInt(rs, 10, 64); err == nil {
			d = time.Unix(epoch, 0).Sub(now)
		}
	}
	return min(max(d, 0), maxRetryAfter)
}

// envelopeMessage digs the "message" out of Twitch's flat error envelope.
func envelopeMessage(raw []byte) string {
	var env struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return ""
	}
	return env.Message
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
		return s[:300] + "..."
	}
	return s
}
