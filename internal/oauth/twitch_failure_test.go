package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Twitch's 429 carries Ratelimit-Reset (a Unix epoch), never Retry-After.
func TestTwitch429ReadsRatelimitReset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Ratelimit-Limit", "800")
		w.Header().Set("Ratelimit-Remaining", "0")
		w.Header().Set("Ratelimit-Reset", strconv.FormatInt(time.Now().Add(30*time.Second).Unix(), 10))
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"Too Many Requests","status":429,"message":"Rate limit exceeded"}`))
	}))
	defer srv.Close()

	_, err := NewTwitch(WithBaseURL(srv.URL)).Account(context.Background(), "cid", "tok")
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want a 429 StatusError", err)
	}
	if se.RetryAfter < 25*time.Second || se.RetryAfter > 31*time.Second {
		t.Errorf("RetryAfter = %v, want about 30s from Ratelimit-Reset", se.RetryAfter)
	}
}

func TestTwitchRatelimitResetIsCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Ratelimit-Reset", strconv.FormatInt(time.Now().Add(48*time.Hour).Unix(), 10))
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	_, err := NewTwitch(WithBaseURL(srv.URL)).Account(context.Background(), "cid", "tok")
	var se *StatusError
	if !errors.As(err, &se) || se.RetryAfter != maxRetryAfter {
		t.Fatalf("err = %v, RetryAfter %v, want the %v cap", err, se, maxRetryAfter)
	}
}

// A 200 that is not JSON must say which call it came from, without the query.
func TestTwitchUnreadableSuccessBodyNamesTheCall(t *testing.T) {
	for name, body := range map[string]string{
		"html":      `<html>502 from a proxy</html>`,
		"truncated": `{"data":[{"id":"4`,
		"wrongtype": `{"data":"nope"}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()
			_, err := NewTwitch(WithBaseURL(srv.URL)).Account(context.Background(), "cid", "tok")
			if err == nil {
				t.Fatal("garbage accepted")
			}
			if !strings.Contains(err.Error(), "unreadable response") || !strings.Contains(err.Error(), "/users") {
				t.Errorf("error %q does not name the call", err)
			}
		})
	}
}

// The PATCH path (requestJSON, which PushCompliance uses) reads the wait the
// same way the GET path does: one helper, so a Twitch 429 carries a RetryAfter
// from Ratelimit-Reset on both, capped.
func TestTwitchPatchRateLimitCarriesRetryAfter(t *testing.T) {
	for name, tc := range map[string]struct {
		header, value string
		lo, hi        time.Duration
	}{
		"ratelimit-reset": {"Ratelimit-Reset", strconv.FormatInt(time.Now().Add(30*time.Second).Unix(), 10), 25 * time.Second, 31 * time.Second},
		"retry-after":     {"Retry-After", "12", 12 * time.Second, 12 * time.Second},
		"capped":          {"Ratelimit-Reset", strconv.FormatInt(time.Now().Add(48*time.Hour).Unix(), 10), maxRetryAfter, maxRetryAfter},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(tc.header, tc.value)
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer srv.Close()
			tw := NewTwitch(WithBaseURL(srv.URL))
			err := tw.requestJSON(context.Background(), http.MethodPatch, srv.URL+"/channels?broadcaster_id=1", "tok", map[string]any{}, nil, nil)
			var se *statusError
			if !errors.As(err, &se) || se.Status != http.StatusTooManyRequests {
				t.Fatalf("err = %v, want a 429 statusError", err)
			}
			if se.RetryAfter == 0 || se.RetryAfter < tc.lo || se.RetryAfter > tc.hi {
				t.Errorf("RetryAfter = %v, want within [%v, %v]", se.RetryAfter, tc.lo, tc.hi)
			}
		})
	}
}
