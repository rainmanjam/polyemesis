package oauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A realistic Graph refusal is well over 300 characters (the message alone
// runs long), which is what the display-truncated copy of the body used to
// turn into invalid JSON.
const fbDeadTokenBody = `{"error":{"message":"Error validating access token: Session has expired on Saturday, 03-Oct-26 09:00:00 PDT. The current time is Saturday, 07-Nov-26 10:00:00 PST. This token was issued by an app that has since been removed or reauthorised by the user.","type":"OAuthException","code":190,"error_subcode":463,"fbtrace_id":"AbCdEfGhIjK"}}`

func TestFacebookRefreshOfADeadTokenIsReconnectRequired(t *testing.T) {
	if len(fbDeadTokenBody) <= 300 {
		t.Fatal("fixture must exceed the 300-character display cut to cover the truncation case")
	}
	for name, body := range map[string]string{
		"190 with subcode 463 over HTTP 400": fbDeadTokenBody,
		"102 API session":                    `{"error":{"message":"Session key invalid or no longer valid","type":"OAuthException","code":102,"fbtrace_id":"x"}}`,
		"190 with subcode 460 (password)":    `{"error":{"message":"The session has been invalidated because the user changed their password","type":"OAuthException","code":190,"error_subcode":460}}`,
	} {
		t.Run(name, func(t *testing.T) {
			fb, _ := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(body))
			})
			_, err := fb.Refresh(context.Background(), "cid", "secret", "old-token")
			if !errors.Is(err, ErrReconnectRequired) {
				t.Fatalf("a dead token must be reconnect-required, got %v", err)
			}
			msg := err.Error()
			for _, bad := range []string{"{", "fbtrace_id", "old-token", "secret"} {
				if strings.Contains(msg, bad) {
					t.Fatalf("message echoes %q: %s", bad, msg)
				}
			}
			if !strings.Contains(msg, "Reconnect the Facebook account") {
				t.Fatalf("message does not say how to fix it: %s", msg)
			}
		})
	}
}

func TestFacebookRefreshFailuresThatReconnectingCannotFix(t *testing.T) {
	tests := []struct {
		name, body, want string
		status           int
	}{
		{"a wrong App ID says to check the credentials", `{"error":{"message":"Invalid App ID","type":"OAuthException","code":101}}`,
			"App ID or App Secret", 400},
		{"a wrong secret says to check the credentials", `{"error":{"message":"Error validating client secret.","type":"OAuthException","code":1}}`,
			"App ID or App Secret", 400},
		{"a throttle says to wait", `{"error":{"message":"(#4) Application request limit reached","type":"OAuthException","code":4}}`,
			"rate-limiting", 400},
		{"an edge failure stays transient and opaque to the classifier", `<html>bad gateway</html>`, "502", 502},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fb, _ := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := fb.Refresh(context.Background(), "cid", "secret", "old-token")
			if err == nil || errors.Is(err, ErrReconnectRequired) {
				t.Fatalf("want a non-reconnect failure, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%q does not contain %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "<html>") || strings.Contains(err.Error(), "fbtrace") {
				t.Fatalf("raw body leaked: %q", err)
			}
		})
	}
}

// The refresh path must honour the same wait signals as the metadata calls, and
// treat code 341 (temporary application limit) as a throttle.
func TestFacebookRefreshRateLimitHonoursWaitHeaders(t *testing.T) {
	tests := []struct {
		name, body string
		headers    map[string]string
		want       string
	}{
		{"Retry-After", `{"error":{"message":"(#4) Application request limit reached","code":4}}`,
			map[string]string{"Retry-After": "120"}, "2m0s"},
		{"BUC regain time", `{"error":{"message":"(#80001) too many calls","code":80001}}`,
			map[string]string{"X-Business-Use-Case-Usage": `{"1":[{"estimated_time_to_regain_access":7}]}`}, "7m0s"},
		{"code 341 is a throttle", `{"error":{"message":"(#341) Application limit reached","code":341}}`,
			map[string]string{"Retry-After": "60"}, "1m0s"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fb, _ := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := fb.Refresh(context.Background(), "cid", "secret", "old-token")
			if err == nil || errors.Is(err, ErrReconnectRequired) {
				t.Fatalf("want a non-reconnect failure, got %v", err)
			}
			if !strings.Contains(err.Error(), "rate-limiting") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%q lacks rate-limiting / %q", err, tc.want)
			}
		})
	}
}

func TestFbAdviceHandlesRateLimitsAndMissingObjects(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		headers map[string]string
		body    string
		want    []string
		notWant string
	}{
		{"app limit with Retry-After", 400, map[string]string{"Retry-After": "120"},
			`{"error":{"message":"(#4) Application request limit reached","type":"OAuthException","code":4,"fbtrace_id":"z"}}`,
			[]string{"rate-limiting", "2m0s", "Application request limit"}, "Reconnect"},
		{"BUC page limit with estimated regain time", 429,
			map[string]string{"X-Business-Use-Case-Usage": `{"123":[{"type":"pages","call_count":100,"total_time":25,"estimated_time_to_regain_access":19}]}`},
			`{"error":{"message":"(#80001) There have been too many calls to this Page account.","type":"OAuthException","code":80001}}`,
			[]string{"rate-limiting", "19m0s"}, "Reconnect"},
		{"limit with no timing signal", 400, nil,
			`{"error":{"message":"(#32) Page request limit reached","type":"OAuthException","code":32}}`,
			[]string{"rate-limiting", "Wait a few minutes"}, ""},
		{"deleted object arrives as 400 code 100 subcode 33", 400, nil,
			`{"error":{"message":"Unsupported get request. Object with ID '1' does not exist, cannot be loaded due to missing permissions, or does not support this operation.","type":"GraphMethodException","code":100,"error_subcode":33,"fbtrace_id":"q"}}`,
			[]string{"no longer has", "Unsupported get request"}, "permission for"},
		{"token death over 400 keeps the reconnect advice", 400, nil, fbDeadTokenBody,
			[]string{"Reconnect the Facebook account"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fb, _ := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := fb.me(context.Background(), "tok")
			if err == nil {
				t.Fatal("want an error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Fatalf("%q does not contain %q", err, w)
				}
			}
			if tc.notWant != "" && strings.Contains(err.Error(), tc.notWant) {
				t.Fatalf("%q must not contain %q", err, tc.notWant)
			}
		})
	}
}

func TestGraphWaitAndCodeTables(t *testing.T) {
	h := http.Header{}
	if GraphWait(h) != 0 {
		t.Fatal("no headers means no wait")
	}
	h.Set("Retry-After", "30")
	h.Set("X-App-Usage", `{"call_count":99,"total_time":50,"total_cputime":40}`)
	if got := GraphWait(h); got != 30*time.Second {
		t.Fatalf("Retry-After = %v", got)
	}
	h.Set("X-Business-Use-Case-Usage", `{"1":[{"estimated_time_to_regain_access":2}],"2":[{"estimated_time_to_regain_access":90}]}`)
	if got := GraphWait(h); got != maxGraphWait {
		t.Fatalf("an hour-and-a-half ask must be capped, got %v", got)
	}
	for _, c := range []int{4, 17, 32, 613, 80000, 80001, 80014} {
		if !IsGraphRateLimit(c) {
			t.Errorf("%d is a throttle", c)
		}
	}
	for _, c := range []int{0, 1, 10, 100, 190, 200, 80015} {
		if IsGraphRateLimit(c) {
			t.Errorf("%d is not a throttle", c)
		}
	}
	if got := FacebookErrorText(&statusError{Status: 502, Body: "<html>"}); got != "Facebook returned HTTP 502" {
		t.Fatalf("HTML must not be echoed: %q", got)
	}
	got := FacebookErrorText(&statusError{Status: 400, URL: "https://graph/x", Body: fbDeadTokenBody[:300], full: fbDeadTokenBody})
	if strings.Contains(got, "fbtrace") || strings.Contains(got, "https://") || !strings.Contains(got, "code 190") {
		t.Fatalf("summary = %q", got)
	}
}

// Every refused refresh keeps "returned <status>" in its text: the live OAuth
// acceptance driver (scripts/acceptance_oauth_driver.go statusFrom) reads the
// HTTP status from exactly that phrase, and a friendlier message that dropped
// it turned a correct refusal into "status -1" against the real Facebook.
func TestFacebookRefreshErrorsKeepTheHTTPStatus(t *testing.T) {
	statusRe := regexp.MustCompile(`returned (\d{3})[:\s]`)
	bodies := map[string]int{
		`{"error":{"message":"Invalid App ID","type":"OAuthException","code":101}}`:                       400,
		`{"error":{"message":"(#4) Application request limit reached","type":"OAuthException","code":4}}`: 400,
		`{"error":{"message":"Session has expired","type":"OAuthException","code":190}}`:                  400,
		`<html>bad gateway</html>`: 502,
	}
	for body, status := range bodies {
		fb, _ := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		})
		_, err := fb.Refresh(context.Background(), "cid", "secret", "old-token")
		m := statusRe.FindStringSubmatch(fmt.Sprint(err))
		if m == nil || m[1] != strconv.Itoa(status) {
			t.Errorf("status %d is not readable from %q", status, err)
		}
	}
}
