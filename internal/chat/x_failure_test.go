package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// The failure bodies here are the shapes X really answered (live probes of 2026-11-07 for the 401 and 403; the rest
// are the shapes docs.x.com lists for each status). Every message that reaches the operator must carry X's words, and
// never a URL, a raw body or a token.

const (
	xProbe401 = `{"title":"Unauthorized","type":"about:blank","status":401,"detail":"Unauthorized"}`
	xProbe403 = `{"title":"Unsupported Authentication","detail":"Authenticating with OAuth 2.0 Application-Only is forbidden for this endpoint.  Supported authentication types are [OAuth 1.0a User Context, OAuth 2.0 User Context].","type":"https://api.twitter.com/2/problems/unsupported-authentication","status":403}`
)

// xFaultServer answers every request with one failure, and counts them.
func xFaultServer(t *testing.T, status int, body string, header map[string]string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func xAdapterAt(t *testing.T, base string, mut func(*XConfig)) *XAdapter {
	t.Helper()
	cfg := XConfig{AccountRef: "1234567890", Channel: "sundaypractice", Token: StaticToken("user-token-SECRET"),
		AppBearer: "app-bearer-SECRET", APIBase: base, WebhookURL: xStubHookURL,
		Now: func() time.Time { return time.Unix(1761904800, 0) }}
	if mut != nil {
		mut(&cfg)
	}
	a, err := NewX(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.SetBroadcast("B1run")
	return a
}

// Every chat call, under every failure status X sends, reports X's own reason: the audit found the reason blank
// ("X refused the chat message: ") because only Google's envelope was read.
func TestXChatCallsReportXsOwnWordsForEveryFailure(t *testing.T) {
	statuses := []struct {
		name   string
		status int
		body   string
		header map[string]string
		want   string
	}{
		{"401 revoked token", 401, xProbe401, nil, "reconnect the X account"},
		{"403 unsupported authentication", 403, xProbe403, nil, "Unsupported Authentication"},
		{"403 not a moderator", 403, `{"error_type":"Forbidden","message":"You are not a moderator of this broadcast"}`, nil, "You are not a moderator of this broadcast"},
		{"404", 404, `{"errors":[{"title":"Not Found Error","detail":"Could not find the broadcast","type":"https://api.twitter.com/2/problems/resource-not-found"}]}`, nil, "Could not find the broadcast"},
		{"429", 429, `{"title":"Too Many Requests","detail":"Too Many Requests","type":"about:blank","status":429}`,
			map[string]string{"x-rate-limit-reset": strconv.FormatInt(time.Unix(1761904800, 0).Add(45*time.Second).Unix(), 10)}, "rate-limiting"},
		{"500", 500, `{"errors":[{"message":"Internal Server Error"}]}`, nil, "Internal Server Error"},
		{"503", 503, `{"error_type":"ServiceUnavailable","message":"try again later"}`, nil, "try again later"},
		{"502 html", 502, `<html>token=user-token-SECRET</html>`, nil, "HTTP 502"},
		{"400 truncated body", 400, `{`, nil, "HTTP 400"},
	}
	calls := map[string]func(a *XAdapter) error{
		"Send":   func(a *XAdapter) error { _, err := a.Send(context.Background(), "hello"); return err },
		"Delete": func(a *XAdapter) error { return a.Delete(context.Background(), "1791387165736000000") },
		"Ban":    func(a *XAdapter) error { return a.Ban(context.Background(), "777", 0, "") },
		"Unban":  func(a *XAdapter) error { return a.Unban(context.Background(), "777") },
		"List": func(a *XAdapter) error {
			a.SetBroadcast("")
			_, err := a.runningBroadcastID(context.Background())
			return err
		},
	}
	for _, sc := range statuses {
		for name, call := range calls {
			t.Run(sc.name+"/"+name, func(t *testing.T) {
				srv, _ := xFaultServer(t, sc.status, sc.body, sc.header)
				err := call(xAdapterAt(t, srv.URL, nil))
				if err == nil {
					t.Fatal("the call succeeded against a failing X")
				}
				msg := err.Error()
				if !strings.Contains(msg, sc.want) {
					t.Errorf("err = %q, want it to carry %q", msg, sc.want)
				}
				if strings.HasSuffix(strings.TrimSpace(msg), ":") {
					t.Errorf("err = %q ends with a blank reason", msg)
				}
				for _, bad := range []string{"://", "SECRET", "<html>", "{"} {
					if strings.Contains(msg, bad) {
						t.Errorf("err = %q carries %q", msg, bad)
					}
				}
			})
		}
	}
}

// A transport failure names the cause, not the request URL.
func TestXChatTransportFailureNamesNoURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()
	_, err := xAdapterAt(t, base, nil).Send(context.Background(), "hi")
	if err == nil || strings.Contains(err.Error(), "://") {
		t.Errorf("err = %v, want the cause without the request URL", err)
	}
}

// ---------------------------------------------------------------- user token

// xTokenServer accepts only Authorization: Bearer <good> on /2/broadcasts/*/chat and refuses anything else with X's
// probed 403, which is what X answers an unrecognised user bearer.
func xTokenServer(t *testing.T, good string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+good {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, xProbe403)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"success":true,"timestamp":"1791387165736000000"}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// A token the store still thinks is good but X refuses is refreshed once and the call retried once.
func TestXSendRefreshesAndRetriesOnceWhenXRefusesTheToken(t *testing.T) {
	srv, hits := xTokenServer(t, "fresh")
	var rejected []string
	a := xAdapterAt(t, srv.URL, func(c *XConfig) {
		c.Token = StaticToken("stale")
		c.RefreshToken = func(_ context.Context, bad string) (string, error) {
			rejected = append(rejected, bad)
			return "fresh", nil
		}
	})
	if _, err := a.Send(context.Background(), "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(rejected) != 1 || rejected[0] != "stale" || hits.Load() != 2 {
		t.Errorf("refreshes = %v, requests = %d, want one refresh of the stale token and two requests", rejected, hits.Load())
	}
}

func TestXSendDoesNotLoopWhenTheRefreshedTokenIsRefusedToo(t *testing.T) {
	srv, hits := xTokenServer(t, "never")
	refreshes := 0
	a := xAdapterAt(t, srv.URL, func(c *XConfig) {
		c.Token = StaticToken("stale")
		c.RefreshToken = func(context.Context, string) (string, error) { refreshes++; return "also-bad", nil }
	})
	_, err := a.Send(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "reconnect the X account") || !isCredentialError(err) {
		t.Fatalf("err = %v, want the reconnect instruction as a credential error", err)
	}
	if refreshes != 1 || hits.Load() != 2 {
		t.Errorf("refreshes = %d, requests = %d, want 1 and 2", refreshes, hits.Load())
	}
}

func TestXSendSaysReconnectWhenTheRefreshGrantIsGone(t *testing.T) {
	srv, _ := xTokenServer(t, "never")
	a := xAdapterAt(t, srv.URL, func(c *XConfig) {
		c.RefreshToken = func(context.Context, string) (string, error) {
			return "", fmt.Errorf("could not refresh the x token: %w", oauth.ErrReconnectRequired)
		}
	})
	_, err := a.Send(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "reconnect the X account") || !isCredentialError(err) {
		t.Fatalf("err = %v, want the reconnect instruction", err)
	}
}

// A refresh that failed for a reason that may pass (X down) is not a verdict on the account.
func TestXSendKeepsATransientRefreshFailureRetryable(t *testing.T) {
	srv, _ := xTokenServer(t, "never")
	a := xAdapterAt(t, srv.URL, func(c *XConfig) {
		c.RefreshToken = func(context.Context, string) (string, error) { return "", errors.New("token endpoint unreachable") }
	})
	_, err := a.Send(context.Background(), "hello")
	if err == nil || isCredentialError(err) {
		t.Fatalf("err = %v, want a retryable failure", err)
	}
}

// A revoked refresh grant ends Run for good (Fatal) with an instruction, instead of the Hub retrying it for ever.
func TestXRunIsFatalWhenTheRefreshGrantIsGone(t *testing.T) {
	st := newXChatStub(t)
	a := xAdapterAt(t, st.srv.URL, func(c *XConfig) {
		c.Token = func(context.Context) (string, error) {
			return "", fmt.Errorf("could not refresh the x token: %w", oauth.ErrReconnectRequired)
		}
	})
	err := xRunErr(t, a)
	if !IsFatal(err) || !strings.Contains(err.Error(), "reconnect the X account") {
		t.Fatalf("Run = %v, want Fatal naming the reconnect", err)
	}
	if !a.Unavailable(time.Hour) {
		t.Error("a Fatal Run left the adapter available")
	}
}

// The probed case: GET /2/users/me with a refused token answers 403 Unsupported Authentication.
func TestXRunIsFatalWhenXRefusesTheUserTokenAfterARefresh(t *testing.T) {
	srv, _ := xFaultServer(t, 403, xProbe403, nil)
	a := xAdapterAt(t, srv.URL, func(c *XConfig) {
		c.RefreshToken = func(context.Context, string) (string, error) {
			return "", fmt.Errorf("gone: %w", oauth.ErrReconnectRequired)
		}
	})
	err := xRunErr(t, a)
	if !IsFatal(err) || !strings.Contains(err.Error(), "reconnect the X account") || strings.Contains(err.Error(), "://") {
		t.Fatalf("Run = %v, want Fatal with the reconnect instruction and no URL", err)
	}
}

// A token without a scope is a reconnect too, and says which kind of reconnect.
func TestXMissingScopeSaysReconnectToGrantIt(t *testing.T) {
	srv, _ := xFaultServer(t, 403, `{"title":"Forbidden","detail":"The token lacks broadcast.write","type":"https://api.x.com/2/problems/missing-scopes","status":403}`, nil)
	_, err := xAdapterAt(t, srv.URL, nil).Send(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "lacks a permission") || !strings.Contains(err.Error(), "reconnect") {
		t.Errorf("err = %v, want the missing-permission instruction", err)
	}
}

// ---------------------------------------------------------------- app bearer

func TestXRunIsFatalAndNamesTheVariableWhenTheAppBearerIsRefused(t *testing.T) {
	st := newXChatStub(t)
	st.appRevoked.Store(true)
	err := xRunErr(t, st.adapter(t, time.Unix(1761904800, 0)))
	if !IsFatal(err) || !strings.Contains(err.Error(), "POLYEMESIS_X_APP_BEARER") {
		t.Fatalf("Run = %v, want Fatal naming POLYEMESIS_X_APP_BEARER", err)
	}
	if strings.Contains(err.Error(), "://") || strings.Contains(err.Error(), "app-bearer") {
		t.Errorf("Run = %q carries a URL or the token", err)
	}
}

// A bearer regenerated while Run is live is noticed at the next recheck, not ignored as "transient".
func TestXRunEndsFatalWhenTheAppBearerIsRevokedWhileLive(t *testing.T) {
	st := newXChatStub(t)
	a, err := NewX(XConfig{AccountRef: "1234567890", Token: StaticToken("user-token"), AppBearer: "app-bearer",
		APIBase: st.srv.URL, WebhookURL: xStubHookURL, WebhookRecheck: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), SinkFunc(func(Message) {})) }()
	xWaitFor(t, "Run to register", a.registered)
	st.appRevoked.Store(true)
	select {
	case err := <-done:
		if !IsFatal(err) || !strings.Contains(err.Error(), "POLYEMESIS_X_APP_BEARER") {
			t.Fatalf("Run = %v, want Fatal naming POLYEMESIS_X_APP_BEARER", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a revoked app bearer was never noticed while live")
	}
}

// ---------------------------------------------------------------- rate limits

// xRateServer answers /2/users/me normally and rate-limits the app-bearer webhook list.
func xRateServer(t *testing.T, header map[string]string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var webhookHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2/users/me":
			_, _ = io.WriteString(w, `{"data":{"id":"1234567890","username":"sundaypractice"}}`)
		case "/2/webhooks":
			webhookHits.Add(1)
			for k, v := range header {
				w.Header().Set(k, v)
			}
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"title":"Too Many Requests","detail":"Too Many Requests","type":"about:blank","status":429}`)
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &webhookHits
}

// A 429 makes Run wait out X's reset (plus jitter) instead of the Hub's 1 to 30 second schedule, and the hold on the
// app-bearer route is shared, so a second account does not ask X again.
func TestXRunWaitsForTheRateLimitResetAndSharesTheHoldAcrossAccounts(t *testing.T) {
	now := time.Unix(1761904800, 0)
	srv, hits := xRateServer(t, map[string]string{"x-rate-limit-reset": strconv.FormatInt(now.Add(240*time.Second).Unix(), 10)})
	var mu sync.Mutex
	var waits []time.Duration
	mk := func(account string) *XAdapter {
		return xAdapterAt(t, srv.URL, func(c *XConfig) {
			c.AccountRef = account
			c.Wait = func(_ context.Context, d time.Duration) error {
				mu.Lock()
				waits = append(waits, d)
				mu.Unlock()
				return nil
			}
		})
	}
	err := xRunErr(t, mk("1234567890"))
	if err == nil || IsFatal(err) || !strings.Contains(err.Error(), "rate-limiting") {
		t.Fatalf("Run = %v, want a retryable rate-limit error", err)
	}
	if err2 := xRunErr(t, mk("999")); err2 == nil || IsFatal(err2) {
		t.Fatalf("second account Run = %v, want a retryable rate-limit error", err2)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("GET /2/webhooks reached X %d times, want 1: the second account must ride the shared hold", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(waits) != 2 {
		t.Fatalf("waits = %v, want one per Run", waits)
	}
	for _, d := range waits {
		if d < 240*time.Second || d > 240*time.Second+xChatJitter {
			t.Errorf("waited %v, want X's 240s reset plus under %v of jitter", d, xChatJitter)
		}
	}
}

// A reset far in the future (clock skew, a bad value) is capped, not honoured for a day.
func TestXRateLimitWaitIsCapped(t *testing.T) {
	now := time.Unix(1761904800, 0)
	srv, _ := xRateServer(t, map[string]string{"x-rate-limit-reset": strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10)})
	var got time.Duration
	a := xAdapterAt(t, srv.URL, func(c *XConfig) {
		c.Wait = func(_ context.Context, d time.Duration) error { got = d; return nil }
	})
	_ = xRunErr(t, a)
	if got <= 0 || got > 16*time.Minute {
		t.Errorf("waited %v, want it capped near 15 minutes", got)
	}
}

// Retry-After is honoured when there is no reset header.
func TestXRateLimitHonoursRetryAfter(t *testing.T) {
	srv, _ := xRateServer(t, map[string]string{"Retry-After": "90"})
	var got time.Duration
	a := xAdapterAt(t, srv.URL, func(c *XConfig) {
		c.Wait = func(_ context.Context, d time.Duration) error { got = d; return nil }
	})
	_ = xRunErr(t, a)
	if got < 90*time.Second || got > 90*time.Second+xChatJitter {
		t.Errorf("waited %v, want 90s plus jitter", got)
	}
}

// ---------------------------------------------------------------- subscriptions

// A subscription that expires or is removed at X while Run is live ends the run retryably, so the Hub registers again.
func TestXRunReRegistersWhenASubscriptionDisappearsWhileLive(t *testing.T) {
	st := newXChatStub(t)
	a, err := NewX(XConfig{AccountRef: "1234567890", Token: StaticToken("user-token"), AppBearer: "app-bearer",
		APIBase: st.srv.URL, WebhookURL: xStubHookURL, WebhookRecheck: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), SinkFunc(func(Message) {})) }()
	xWaitFor(t, "Run to register", a.registered)
	if len(st.subList()) != 2 {
		t.Fatalf("setup: %d subscriptions, want 2", len(st.subList()))
	}
	st.mu.Lock()
	st.subs = st.subs[:1] // removed in console.x.com
	st.mu.Unlock()
	select {
	case err := <-done:
		if err == nil || IsFatal(err) || !strings.Contains(err.Error(), "subscription") {
			t.Fatalf("Run = %v, want a retryable error naming the missing subscription", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a vanished subscription was never noticed while live")
	}
}

func TestXRunReRegistersWhenASubscriptionExpiresWhileLive(t *testing.T) {
	st := newXChatStub(t)
	now := time.Unix(1761904800, 0)
	var mu sync.Mutex
	clock := now
	a, err := NewX(XConfig{AccountRef: "1234567890", Token: StaticToken("user-token"), AppBearer: "app-bearer",
		APIBase: st.srv.URL, WebhookURL: xStubHookURL, WebhookRecheck: 10 * time.Millisecond,
		Now: func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), SinkFunc(func(Message) {})) }()
	xWaitFor(t, "Run to register", a.registered)
	st.mu.Lock()
	for _, s := range st.subs {
		s["expires_at"] = now.Add(time.Hour).UTC().Format(time.RFC3339Nano)
	}
	st.mu.Unlock()
	mu.Lock()
	clock = now.Add(2 * time.Hour)
	mu.Unlock()
	select {
	case err := <-done:
		if err == nil || IsFatal(err) {
			t.Fatalf("Run = %v, want a retryable error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an expired subscription was never noticed while live")
	}
}
