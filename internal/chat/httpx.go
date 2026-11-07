package chat

// Shared HTTP for the three polling/REST adapters. Twitch needs none of this;
// the other three are all "authenticated JSON over a bearer token", and one
// implementation of that is one place to get the token handling right.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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
	// Message is the top-level "message" of the {"data":..,"message":".."}
	// envelope Kick and a few others use, empty when the body is not one.
	Message string
	// RetryAfter is the platform's Retry-After, zero when it sent none.
	RetryAfter time.Duration
	// Edge is set when the answer came from a proxy or CDN in front of the
	// platform (an HTML page, or a non-JSON body with Cloudflare's headers)
	// rather than from its API. Its status says nothing about the account.
	Edge bool
}

func (e *apiError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("%s returned %d (%s): %s", e.URL, e.Status, e.Reason, e.Body)
	}
	return fmt.Sprintf("%s returned %d: %s", e.URL, e.Status, e.Body)
}

func statusOf(err error) int {
	if ae, ok := err.(*apiError); ok {
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
			Message:    errorMessage(raw),
			RetryAfter: retryAfter(resp.Header.Get("Retry-After")),
			Edge:       isEdgeAnswer(resp.Header, raw),
		}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		if isEdgeAnswer(resp.Header, raw) {
			return fmt.Errorf("%s answered %d with a web page instead of JSON; a proxy or the platform's edge may be blocking this server",
				stripQuery(endpoint), resp.StatusCode)
		}
		return err
	}
	return nil
}

// errorMessage reads the "message" string of a {"data":..,"message":".."}
// error envelope. Anything else yields empty.
func errorMessage(raw []byte) string {
	var env struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return ""
	}
	return strings.TrimSpace(env.Message)
}

// retryAfter parses a Retry-After header in either form (delta-seconds or an
// HTTP date). Zero when absent or unreadable.
func retryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil {
		return max(0, time.Until(at))
	}
	return 0
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
