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
