package chat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// The real shapes, recorded against googleapis.com with a fake token.
const (
	ytAuth401  = `{"error":{"code":401,"message":"Request had invalid authentication credentials. Expected OAuth 2 access token, login cookie or other valid authentication credential. See https://developers.google.com/identity/sign-in/web/devconsole-project?token=SECRET","errors":[{"message":"Invalid Credentials","domain":"global","reason":"authError","location":"Authorization","locationType":"header"}],"status":"UNAUTHENTICATED"}}`
	ytScope403 = `{"error":{"code":403,"message":"Request had insufficient authentication scopes.","errors":[{"message":"Insufficient Permission","domain":"global","reason":"insufficientPermissions"}],"status":"PERMISSION_DENIED"}}`
	ytNoLive   = `{"error":{"code":403,"message":"The user is not enabled for live streaming.","errors":[{"message":"x","domain":"youtube.liveBroadcast","reason":"liveStreamingNotEnabled"}]}}`
	ytChat404  = `{"error":{"code":404,"message":"The live chat cannot be found.","errors":[{"message":"x","domain":"youtube.liveChat","reason":"liveChatNotFound"}]}}`
	ytRate403  = `{"error":{"code":403,"message":"Rate limit exceeded.","errors":[{"message":"x","domain":"youtube.liveChat","reason":"rateLimitExceeded"}]}}`
)

// pathLog records "METHOD /path" for every request and answers 404 for any
// route not in routes, as the real API does for a path that does not exist.
func pathLog(t *testing.T, routes map[string]string) (*ytStub, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	s := newYTStub(t, func(w http.ResponseWriter, r *http.Request, _ int64) {
		key := r.Method + " " + r.URL.Path
		mu.Lock()
		seen = append(seen, key)
		mu.Unlock()
		body, ok := routes[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, body)
	})
	return s, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), seen...) }
}

// TestYouTubeCallsTheDocumentedCollectionPaths pins every chat path. The old
// stub answered any path, so liveChatMessages (a route that does not exist,
// 404 live) passed every test while the real adapter could never poll.
func TestYouTubeCallsTheDocumentedCollectionPaths(t *testing.T) {
	stub, seen := pathLog(t, map[string]string{
		"GET /liveChat/messages":    `{"items":[],"pollingIntervalMillis":5000,"offlineAt":"2026-03-01T00:00:00Z"}`,
		"POST /liveChat/messages":   `{"id":"m1","snippet":{}}`,
		"DELETE /liveChat/messages": ``,
		"POST /liveChat/bans":       `{"id":"b1"}`,
	})
	a := ytAdapter(t, stub.URL, func(c *YouTubeConfig) {
		c.LiveChatID = "chat-1"
		c.Sleep = func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }
	})
	ctx := context.Background()
	if err := a.pump(ctx, "chat-1", SinkFunc(func(Message) {})); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Send(ctx, "hi"); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Ban(ctx, "UC-troll", 0, ""); err != nil {
		t.Fatal(err)
	}
	want := []string{"GET /liveChat/messages", "POST /liveChat/messages", "DELETE /liveChat/messages", "POST /liveChat/bans"}
	got := seen()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("paths = %v, want %v", got, want)
	}
}

// A 404 on the poll is only "the chat closed" when YouTube says so with
// liveChatNotFound. An empty 404 is a wrong route and must surface as an
// error, not as a chat that quietly stopped.
func TestYouTubeAnEmptyPoll404IsAnErrorNotAClosedChat(t *testing.T) {
	stub := newYTStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) { w.WriteHeader(http.StatusNotFound) })
	a := ytAdapter(t, stub.URL, nil)
	err := a.pump(context.Background(), "chat-1", SinkFunc(func(Message) {}))
	if err == nil || statusOf(err) != http.StatusNotFound {
		t.Fatalf("pump = %v, want the 404 as an error", err)
	}
	if IsFatal(err) {
		t.Errorf("a route 404 must be retried by the Hub, not fatal")
	}

	stub = newYTStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, ytChat404)
	})
	a = ytAdapter(t, stub.URL, nil)
	if err := a.pump(context.Background(), "chat-1", SinkFunc(func(Message) {})); err != nil {
		t.Fatalf("liveChatNotFound should end the chat cleanly, got %v", err)
	}
	if a.Health().State != StateStopped {
		t.Errorf("state = %v, want stopped", a.Health().State)
	}
}

// A refresh grant that is gone must end chat with a reconnect instruction;
// it used to read as a plain "no usable access token" the Hub retried for ever.
func TestYouTubeARevokedRefreshGrantIsFatalAndSaysReconnect(t *testing.T) {
	stub := newYTStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) { t.Error("YouTube was called without a token") })
	revoked := fmt.Errorf("refresh failed: %w", &tokenGone{})
	a := ytAdapter(t, stub.URL, func(c *YouTubeConfig) {
		c.Token = func(context.Context) (string, error) { return "", revoked }
	})
	err := a.Run(context.Background(), SinkFunc(func(Message) {}))
	if !IsFatal(err) || !errors.Is(err, oauth.ErrReconnectRequired) {
		t.Fatalf("Run = %v, want fatal reconnect-required", err)
	}
	if !strings.Contains(err.Error(), "reconnect") || strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("message = %q, want a reconnect instruction without the token-endpoint JSON", err)
	}
}

type tokenGone struct{}

func (*tokenGone) Error() string {
	return `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`
}
func (*tokenGone) Is(target error) bool { return target == oauth.ErrReconnectRequired }

// A 401 is retried once with a fresh token (the TokenFunc is asked again);
// a second 401 is fatal, and never echoes the body's link.
func TestYouTubeA401IsRetriedOnceThenFatal(t *testing.T) {
	var calls int
	stub := newYTStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, ytAuth401)
	})
	a := ytAdapter(t, stub.URL, func(c *YouTubeConfig) {
		c.Token = func(context.Context) (string, error) { calls++; return fmt.Sprintf("tok-%d", calls), nil }
	})
	err := a.pump(context.Background(), "chat-1", SinkFunc(func(Message) {}))
	if !IsFatal(err) || calls != 2 {
		t.Fatalf("pump = %v after %d token fetches, want fatal after exactly 2", err, calls)
	}
	if !strings.Contains(err.Error(), "reconnect") || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("message = %q", err)
	}

	// A 401 that a fresh token cures does not end chat.
	var n int
	stub = newYTStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
		n++
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, ytAuth401)
			return
		}
		fmt.Fprint(w, `{"items":[],"offlineAt":"2026-03-01T00:00:00Z"}`)
	})
	a = ytAdapter(t, stub.URL, nil)
	if err := a.pump(context.Background(), "chat-1", SinkFunc(func(Message) {})); err != nil {
		t.Fatalf("pump after a cured 401 = %v", err)
	}
}

// insufficientPermissions and liveStreamingNotEnabled can only be fixed by
// the operator; both are fatal with an instruction rather than a retry loop.
func TestYouTubeScopeAndLiveStreamingFailuresAreFatalWithAnAction(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"scope", ytScope403, "approve every permission"},
		{"not enabled", ytNoLive, "enable it in YouTube Studio"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := newYTStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, tc.body)
			})
			a := ytAdapter(t, stub.URL, nil)
			for name, err := range map[string]error{
				"poll":     a.pump(context.Background(), "chat-1", SinkFunc(func(Message) {})),
				"discover": func() error { _, e := a.liveChatID(context.Background()); return e }(),
			} {
				if !IsFatal(err) || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("%s: %v, want fatal containing %q", name, err, tc.want)
				}
			}
		})
	}
}

// Retry-After is honoured, capped, and jittered; repeated failures escalate
// and then hand over to the Hub instead of retrying for ever.
func TestYouTubeRateLimitHonoursRetryAfterWithCapAndHandsOver(t *testing.T) {
	var waits []time.Duration
	stub := newYTStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, ytRate403)
	})
	a := ytAdapter(t, stub.URL, func(c *YouTubeConfig) {
		c.Sleep = func(_ context.Context, d time.Duration) bool { waits = append(waits, d); return true }
	})
	err := a.pump(context.Background(), "chat-1", SinkFunc(func(Message) {}))
	if err == nil || IsFatal(err) || reasonOf(err) != "rateLimitExceeded" {
		t.Fatalf("pump = %v, want a plain rate-limit error for the Hub", err)
	}
	if len(waits) != ytMaxInPlaceRetries {
		t.Fatalf("waited %d times, want %d", len(waits), ytMaxInPlaceRetries)
	}
	for _, d := range waits {
		if d < 60*time.Second || d > ytRateWaitCap+ytRateWaitCap/4 {
			t.Errorf("wait %v outside [Retry-After, cap+jitter]", d)
		}
	}
	// Longer than the cap is cut to the cap; without a header it escalates.
	if d := ytRateWait(1, time.Hour); d < ytRateWaitCap || d > ytRateWaitCap+ytRateWaitCap/4 {
		t.Errorf("capped wait = %v", d)
	}
	if ytRateWait(1, 0) >= ytRateWait(4, 0) {
		t.Errorf("wait does not escalate: %v vs %v", ytRateWait(1, 0), ytRateWait(4, 0))
	}
}

// Operator-visible text carries Google's sentence and reason, never the raw
// body, an HTML error page, or a URL's query string.
func TestYouTubeErrorTextNeverEchoesTheBody(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       string
		notWant    []string
	}{
		{"envelope", ytAuth401, "authError", []string{"SECRET", "UNAUTHENTICATED", "token="}},
		{"html", `<html><body><h1>502 Bad Gateway</h1> internal host 10.0.0.5</body></html>`, "returned 502", []string{"<html>", "10.0.0.5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code := http.StatusUnauthorized
			if tc.name == "html" {
				code = http.StatusBadGateway
			}
			stub := newYTStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
				w.WriteHeader(code)
				fmt.Fprint(w, tc.body)
			})
			a := ytAdapter(t, stub.URL, nil)
			err := doJSON(context.Background(), a.cfg.HTTP, http.MethodGet, stub.URL+"/liveChat/messages?liveChatId=x&access=SECRET", a.cfg.Token, nil, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "?") || strings.Contains(err.Error(), "SECRET") {
				t.Errorf("err leaks a query: %v", err)
			}
			for _, bad := range tc.notWant {
				if strings.Contains(err.Error(), bad) {
					t.Errorf("err %q contains %q", err, bad)
				}
			}
			if code == http.StatusUnauthorized && !strings.Contains(err.Error(), "Invalid Credentials") && !strings.Contains(err.Error(), "invalid authentication") {
				t.Errorf("err %q lost Google's own message", err)
			}
		})
	}
}

// Failure-shape table for the poll: network error, malformed 200, 5xx, 410.
func TestYouTubePollFailureShapes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		handler   func(w http.ResponseWriter, r *http.Request)
		wantErr   bool
		wantFatal bool
		state     State
	}{
		{"5xx", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }, true, false, ""},
		{"malformed 200", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"items":`) }, true, false, ""},
		{"410 gone", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(410) }, false, false, StateStopped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, _ int64) { tc.handler(w, r) })
			a := ytAdapter(t, stub.URL, nil)
			err := a.pump(context.Background(), "chat-1", SinkFunc(func(Message) {}))
			if (err != nil) != tc.wantErr || IsFatal(err) != tc.wantFatal {
				t.Fatalf("pump = %v", err)
			}
			if tc.state != "" && a.Health().State != tc.state {
				t.Errorf("state = %v, want %v", a.Health().State, tc.state)
			}
		})
	}
	// A dead server: connection refused is an error the Hub retries.
	stub := newYTStub(t, func(http.ResponseWriter, *http.Request, int64) {})
	base := stub.URL
	stub.Close()
	a := ytAdapter(t, base, nil)
	if err := a.pump(context.Background(), "chat-1", SinkFunc(func(Message) {})); err == nil || IsFatal(err) {
		t.Errorf("network error = %v, want a plain error", err)
	}
}
