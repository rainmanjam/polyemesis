package chat

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Failure handling for Twitch, against the error shapes Twitch really sends
// (recorded by live probes in the 2026-11 failure audit).

const twitchInvalidToken = `{"error":"Unauthorized","status":401,"message":"Invalid OAuth token"}`
const twitchMissingScope = `{"error":"Unauthorized","status":401,"message":"Missing scope: moderator:read:chat_settings"}`

func TestTwitchRevokedGrantEndsTheConnectLoop(t *testing.T) {
	dialled := false
	a, err := NewTwitch(TwitchConfig{Nick: "streamer", Token: "attach-time-token",
		Dial: func(context.Context) (net.Conn, error) { dialled = true; return nil, errors.New("unused") }})
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.PassToken = func(context.Context) (string, error) {
		return "", fmt.Errorf("%w: could not refresh: Invalid refresh token", ErrReconnectRequired)
	}
	runErr := a.Run(context.Background(), SinkFunc(func(Message) {}))
	if !IsFatal(runErr) {
		t.Fatalf("a revoked grant was retryable (%v): the Hub would re-post the dead refresh token forever", runErr)
	}
	if !strings.Contains(runErr.Error(), "reconnect") {
		t.Errorf("error %q does not tell the operator to reconnect", runErr)
	}
	if dialled {
		t.Error("dialled Twitch with no token")
	}
	if h := a.Health(); h.State != StateFailed || !strings.Contains(h.Detail, "reconnect") {
		t.Errorf("health = %+v, want failed with a reconnect instruction", h)
	}
}

func startTwitchSend(t *testing.T) (*TwitchAdapter, *ircServer, context.Context) {
	t.Helper()
	a, srv := newTwitchOverPipe(t, TwitchConfig{Nick: "streamer", Channel: "streamer", AccountRef: "42"})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = a.Run(ctx, SinkFunc(func(Message) {})) }()
	srv.readLine(t) // CAP
	srv.readLine(t) // PASS
	srv.readLine(t) // NICK
	return a, srv, ctx
}

func TestTwitchSendReportsATwitchRefusal(t *testing.T) {
	for id, want := range map[string]string{
		"msg_followersonly": "followers-only",
		"msg_banned":        "banned",
		"msg_slowmode":      "slow mode",
		"msg_duplicate":     "repeat",
		"msg_ratelimit":     "rate-limiting",
	} {
		t.Run(id, func(t *testing.T) {
			a, srv, ctx := startTwitchSend(t)
			errc := make(chan error, 1)
			go func() { _, err := a.Send(ctx, "hello chat"); errc <- err }()
			srv.readLine(t) // PRIVMSG
			srv.write(t, "@msg-id="+id+" :tmi.twitch.tv NOTICE #streamer :refused")
			select {
			case err := <-errc:
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("Send error = %v, want one naming %q: the message was dropped but reported sent", err, want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Send did not return")
			}
		})
	}
}

func TestTwitchSendSucceedsOnUserstateAndOnSilence(t *testing.T) {
	a, srv, ctx := startTwitchSend(t)
	errc := make(chan error, 1)
	go func() { _, err := a.Send(ctx, "hi"); errc <- err }()
	srv.readLine(t)
	srv.write(t, "@mod=0 :tmi.twitch.tv USERSTATE #streamer")
	if err := <-errc; err != nil {
		t.Fatalf("an accepted message failed: %v", err)
	}

	old := twitchSendAckWait
	twitchSendAckWait = 50 * time.Millisecond
	defer func() { twitchSendAckWait = old }()
	go func() { _, err := a.Send(ctx, "hi again"); errc <- err }()
	srv.readLine(t)
	if err := <-errc; err != nil {
		t.Fatalf("a message Twitch never answered was reported failed: %v", err)
	}
}

// helixRefreshRig is a fake Helix whose first request answers firstStatus/firstBody
// and whose later requests answer 200 only for the refreshed token.
func helixRefreshRig(t *testing.T, firstStatus int, firstBody string, refreshErr error) (*TwitchAdapter, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var calls, refreshes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") == "Bearer fresh-token" {
			fmt.Fprint(w, `{"data":[{"broadcaster_id":"42","slow_mode":false,"follower_mode":false,`+
				`"subscriber_mode":false,"emote_mode":false,"unique_chat_mode":false,"non_moderator_chat_delay":false}]}`)
			return
		}
		w.WriteHeader(firstStatus)
		fmt.Fprint(w, firstBody)
	}))
	t.Cleanup(srv.Close)
	a := twitchForDelete(t, srv.URL)
	a.cfg.HelixToken = StaticToken("stale-secret-token")
	a.cfg.HelixRefresh = func(context.Context, string) (string, error) {
		refreshes.Add(1)
		if refreshErr != nil {
			return "", refreshErr
		}
		return "fresh-token", nil
	}
	return a, &calls, &refreshes
}

func TestTwitchHelix401InvalidTokenRefreshesOnceAndRetries(t *testing.T) {
	a, calls, refreshes := helixRefreshRig(t, 401, twitchInvalidToken, nil)
	if _, err := a.ReadChatSettings(context.Background()); err != nil {
		t.Fatalf("a stale token was not refreshed and retried: %v", err)
	}
	if refreshes.Load() != 1 || calls.Load() != 2 {
		t.Errorf("refreshes=%d calls=%d, want 1 and 2", refreshes.Load(), calls.Load())
	}
}

func TestTwitchHelix401StillInvalidSaysTokenNotScope(t *testing.T) {
	a, calls, refreshes := helixRefreshRig(t, 401, twitchInvalidToken, fmt.Errorf("%w: gone", ErrReconnectRequired))
	_, err := a.ReadChatSettings(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if refreshes.Load() != 1 || calls.Load() != 1 {
		t.Errorf("refreshes=%d calls=%d, want 1 and 1 (no retry without a new token)", refreshes.Load(), calls.Load())
	}
	msg := err.Error()
	if strings.Contains(msg, "before moderation existed") || strings.Contains(msg, "permission was requested") {
		t.Errorf("an invalid token was blamed on a missing scope: %s", msg)
	}
	if !strings.Contains(msg, "expired or was revoked") || !strings.Contains(msg, "Invalid OAuth token") {
		t.Errorf("message does not carry the real cause: %s", msg)
	}
	if strings.Contains(msg, "stale-secret-token") {
		t.Error("the access token leaked into the error")
	}
}

func TestTwitchHelix401MissingScopeSaysScopeAndDoesNotRefresh(t *testing.T) {
	a, calls, refreshes := helixRefreshRig(t, 401, twitchMissingScope, nil)
	_, err := a.ReadChatSettings(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if refreshes.Load() != 0 || calls.Load() != 1 {
		t.Errorf("refreshes=%d calls=%d: a refresh cannot grant a scope", refreshes.Load(), calls.Load())
	}
	if !strings.Contains(err.Error(), "grant it") || !strings.Contains(err.Error(), "Missing scope") {
		t.Errorf("message does not give scope advice: %v", err)
	}
}

func TestTwitchHelix429ReadsRatelimitResetAndHoldsOffFollowUps(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Ratelimit-Limit", "800")
		w.Header().Set("Ratelimit-Remaining", "0")
		w.Header().Set("Ratelimit-Reset", strconv.FormatInt(time.Now().Add(20*time.Second).Unix(), 10))
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":"Too Many Requests","status":429,"message":"Rate limit exceeded"}`)
	}))
	defer srv.Close()
	a := twitchForDelete(t, srv.URL)

	_, err := a.ReadChatSettings(context.Background())
	var ae *apiError
	if !errors.As(err, &ae) || ae.RetryAfter < 15*time.Second || ae.RetryAfter > 21*time.Second {
		t.Fatalf("err = %v, RetryAfter not read from Ratelimit-Reset (got %+v)", err, ae)
	}
	if !strings.Contains(err.Error(), "seconds") {
		t.Errorf("message does not say how long to wait: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := a.ReadChatSettings(context.Background()); statusOf(err) != http.StatusTooManyRequests {
			t.Fatalf("follow-up %d: %v", i, err)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("Twitch was asked %d times; calls inside its stated window should be refused locally", hits.Load())
	}
}

func TestRetryAfterOfCapsAndPrefersRetryAfter(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	h := http.Header{}
	h.Set("Ratelimit-Reset", strconv.FormatInt(now.Add(10*time.Hour).Unix(), 10))
	if got := retryAfterOf(h, now); got != maxRetryAfter {
		t.Errorf("far-future reset = %v, want the %v cap", got, maxRetryAfter)
	}
	h.Set("Retry-After", "7")
	if got := retryAfterOf(h, now); got != 7*time.Second {
		t.Errorf("Retry-After = %v, want 7s", got)
	}
	h = http.Header{}
	h.Set("Ratelimit-Reset", "1")
	if got := retryAfterOf(h, now); got != 0 {
		t.Errorf("a reset in the past = %v, want 0", got)
	}
}

func TestTwitchHelixUnreadableSuccessBodyNamesTheCall(t *testing.T) {
	for name, body := range map[string]string{
		"html":      `<html><body>502 from the proxy</body></html>`,
		"truncated": `{"data":[{"broadcaster_id":"4`,
		"wrongtype": `{"data":"nope"}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv, _, _ := helixStub(t, http.StatusOK, body)
			a := twitchForDelete(t, srv.URL)
			_, err := a.ReadChatSettings(context.Background())
			if err == nil {
				t.Fatal("garbage was accepted")
			}
			if !strings.Contains(err.Error(), "/chat/settings") || !strings.Contains(err.Error(), "unreadable") {
				t.Errorf("error %q does not name the call", err)
			}
		})
	}
}

// Every Helix error path, for every status Twitch really sends: it must be
// worded, must carry Twitch's own reason, and must never carry the token or the
// query string (which holds ids).
func TestTwitchHelixErrorsAreWordedAndNeverLeak(t *testing.T) {
	cases := map[int]string{
		401: `{"error":"Unauthorized","status":401,"message":"Invalid OAuth token"}`,
		403: `{"error":"Forbidden","status":403,"message":"The user in parameter moderator_id is not one of the broadcaster's moderators"}`,
		404: `{"error":"Not Found","status":404,"message":"Message not found"}`,
		429: `{"error":"Too Many Requests","status":429,"message":"Rate limit exceeded"}`,
		500: `{"error":"Internal Server Error","status":500,"message":"Internal Server Error"}`,
	}
	for status, body := range cases {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			srv, _, _ := helixStub(t, status, body)
			a := twitchForDelete(t, srv.URL)
			a.cfg.HelixToken = StaticToken("super-secret-helix-token")
			calls := map[string]error{
				"settings": func() error { _, err := a.ReadChatSettings(context.Background()); return err }(),
				"delete":   a.Delete(context.Background(), "msg-1"),
				"announce": a.Announce(context.Background(), "hi", ""),
				"banned":   func() error { _, err := a.ListBanned(context.Background()); return err }(),
			}
			for name, err := range calls {
				if err == nil {
					t.Errorf("%s: no error for %d", name, status)
					continue
				}
				msg := err.Error()
				if strings.Contains(msg, "super-secret-helix-token") || strings.Contains(msg, "?") {
					t.Errorf("%s/%d leaks a token or query: %s", name, status, msg)
				}
				if status != 500 && !strings.Contains(msg, strings.Split(strings.Split(body, `"message":"`)[1], `"`)[0]) {
					t.Errorf("%s/%d lost Twitch's own reason: %s", name, status, msg)
				}
			}
		})
	}
}

// A shoutout cooldown is a 429 with tokens left in the bucket. It must not stop
// an unrelated Ban from reaching Twitch.
func TestTwitchEndpointCooldown429DoesNotBlockOtherCalls(t *testing.T) {
	var shoutouts, others atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "shoutouts") {
			shoutouts.Add(1)
			w.Header().Set("Ratelimit-Remaining", "799")
			w.Header().Set("Ratelimit-Reset", strconv.FormatInt(time.Now().Add(20*time.Second).Unix(), 10))
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":"Too Many Requests","status":429,"message":"shoutout cooldown"}`)
			return
		}
		others.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	a := twitchForDelete(t, srv.URL)
	if err := a.helix(context.Background(), http.MethodPost, "/chat/shoutouts", nil, nil, nil); statusOf(err) != http.StatusTooManyRequests {
		t.Fatalf("shoutout = %v, want 429", err)
	}
	if err := a.Delete(context.Background(), "msg-1"); err != nil {
		t.Fatalf("a delete after a shoutout cooldown was refused: %v", err)
	}
	if others.Load() != 1 {
		t.Errorf("the delete reached Twitch %d times, want 1", others.Load())
	}
}

// A refresh that failed for a passing reason must not be worded as a revoked grant.
func TestTwitchHelix401TransientRefreshFailureIsNotCalledRevoked(t *testing.T) {
	a, _, _ := helixRefreshRig(t, 401, twitchInvalidToken, errors.New("id.twitch.tv returned 503"))
	_, err := a.ReadChatSettings(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if strings.Contains(msg, "disconnect and reconnect") || !strings.Contains(msg, "try again") || !strings.Contains(msg, "503") {
		t.Errorf("transient refresh failure misworded: %s", msg)
	}
	if statusOf(err) != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 preserved", statusOf(err))
	}
}

// The refresh is told which token was refused.
func TestTwitchHelix401PassesTheRejectedTokenToRefresh(t *testing.T) {
	a, _, _ := helixRefreshRig(t, 401, twitchInvalidToken, nil)
	var got string
	a.cfg.HelixRefresh = func(_ context.Context, rejected string) (string, error) {
		got = rejected
		return "fresh-token", nil
	}
	if _, err := a.ReadChatSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got != "stale-secret-token" {
		t.Errorf("refresh got %q, want the rejected token", got)
	}
}
