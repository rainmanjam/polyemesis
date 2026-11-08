package chat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// Real Graph shapes: well-formed refusals travel as HTTP 400, and are longer
// than the 300-character display cut.
const (
	fbBody190 = `{"error":{"message":"Error validating access token: Session has expired on Saturday, 03-Oct-26 09:00:00 PDT. The current time is Saturday, 07-Nov-26 10:00:00 PST. This token was issued by an app that has since been removed.","type":"OAuthException","code":190,"error_subcode":463,"fbtrace_id":"AbCdEfGhIjK"}}`
	fbBody4   = `{"error":{"message":"(#4) Application request limit reached","is_transient":true,"type":"OAuthException","code":4,"fbtrace_id":"AbCdEfGhIjK"}}`
	fbBody100 = `{"error":{"message":"Unsupported get request. Object with ID 'live-1' does not exist, cannot be loaded due to missing permissions, or does not support this operation.","type":"GraphMethodException","code":100,"error_subcode":33,"fbtrace_id":"AbCdEfGhIjK"}}`
)

func fbFailServer(t *testing.T, status int, body string, hdr map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFacebookClassifyUnderstandsGraphEnvelopesOnHTTP400(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantDone  bool
		wantFatal bool
		wantState State
	}{
		{"expired token as 400/190 is fatal", fbBody190, false, true, StateFailed},
		{"api session 102 is fatal", `{"error":{"message":"Session key invalid","type":"OAuthException","code":102}}`, false, true, StateFailed},
		{"400/100/33 is ambiguous (gone or no access) so the chat keeps polling", fbBody100, false, false, StateDegraded},
		{"a throttle is neither fatal nor terminal", fbBody4, false, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := fbFailServer(t, http.StatusBadRequest, tc.body, nil)
			a := fbAdapter(t, srv.URL, nil)
			_, err := a.poll(context.Background(), "live-1", SinkFunc(func(Message) {}))
			if err == nil {
				t.Fatal("want an error")
			}
			done, fatal := a.classify(err)
			if done != tc.wantDone || (fatal != nil) != tc.wantFatal {
				t.Fatalf("done=%v fatal=%v", done, fatal)
			}
			if fatal != nil && !IsFatal(fatal) {
				t.Fatal("fatal error is not marked fatal")
			}
			if tc.wantState != "" && a.Health().State != tc.wantState {
				t.Fatalf("state = %q, want %q", a.Health().State, tc.wantState)
			}
		})
	}
}

func TestFacebookRunStopsOnAReconnectRequiredTokenSource(t *testing.T) {
	sleeps := 0
	a := fbAdapter(t, "http://unused.invalid", func(c *FacebookConfig) {
		c.Token = func(context.Context) (string, error) {
			return "", fmt.Errorf("could not refresh the facebook token: %w", oauth.ErrReconnectRequired)
		}
		c.Sleep = func(context.Context, time.Duration) bool { sleeps++; return sleeps < 3 }
	})
	err := a.Run(context.Background(), SinkFunc(func(Message) {}))
	if !IsFatal(err) {
		t.Fatalf("a token the platform refuses for good must end the adapter, got %v", err)
	}
}

func TestFacebookRunBackoffRespectsItsCapAndJitters(t *testing.T) {
	srv := fbFailServer(t, http.StatusBadGateway, `<html>bad gateway</html>`, nil)
	for _, tc := range []struct {
		name string
		rand float64
		want []time.Duration
	}{
		{"no jitter drawn", 0, []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second,
			120 * time.Second, 120 * time.Second}},
		{"maximum jitter takes a fifth off", 0.9999, []time.Duration{8 * time.Second, 16 * time.Second, 32 * time.Second,
			64 * time.Second, 96 * time.Second, 96 * time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var slept []time.Duration
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a := fbAdapter(t, srv.URL, func(c *FacebookConfig) {
				c.Rand = func() float64 { return tc.rand }
				c.Sleep = func(_ context.Context, d time.Duration) bool {
					slept = append(slept, d)
					if len(slept) == len(tc.want) {
						cancel()
						return false
					}
					return true
				}
			})
			if err := a.Run(ctx, SinkFunc(func(Message) {})); err != nil {
				t.Fatal(err)
			}
			for i, w := range tc.want {
				// Jitter only subtracts, at most 20%, and leaves a few ms of rounding.
				if d := slept[i] - w; d > 10*time.Millisecond || d < -10*time.Millisecond {
					t.Fatalf("sleep %d = %v, want %v (all: %v)", i, slept[i], w, slept)
				}
			}
			if strings.Contains(a.Health().Detail, "<html>") {
				t.Fatalf("raw HTML leaked into health: %q", a.Health().Detail)
			}
		})
	}
}

func TestFacebookRunHonoursRateLimitSignals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		hdr    map[string]string
		want   time.Duration
		detail string
	}{
		{"Retry-After beats the backoff", 400, map[string]string{"Retry-After": "300"}, 300 * time.Second, "retrying in 5m0s"},
		{"BUC regain time in minutes", 429, map[string]string{"X-Business-Use-Case-Usage": `{"9":[{"type":"pages","estimated_time_to_regain_access":12}]}`},
			12 * time.Minute, "retrying in 12m0s"},
		{"no signal falls back to the backoff", 400, nil, 10 * time.Second, "retrying in 10s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := fbFailServer(t, tc.status, fbBody4, tc.hdr)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var slept time.Duration
			a := fbAdapter(t, srv.URL, func(c *FacebookConfig) {
				c.Rand = func() float64 { return 0 }
				c.Sleep = func(_ context.Context, d time.Duration) bool { slept = d; cancel(); return false }
			})
			if err := a.Run(ctx, SinkFunc(func(Message) {})); err != nil {
				t.Fatal(err)
			}
			if slept != tc.want {
				t.Fatalf("slept %v, want %v", slept, tc.want)
			}
			h := a.Health()
			if h.State != StateDegraded || !strings.Contains(h.Detail, "rate-limiting") || !strings.Contains(h.Detail, tc.detail) {
				t.Fatalf("health = %+v", h)
			}
			if strings.Contains(h.Detail, "fbtrace") || strings.Contains(h.Detail, "{") {
				t.Fatalf("raw Graph JSON leaked: %q", h.Detail)
			}
		})
	}
}

func TestFacebookDegradedDetailIsAOneLineSummary(t *testing.T) {
	srv := fbFailServer(t, http.StatusInternalServerError,
		`{"error":{"message":"An unexpected error has occurred. Please retry your request later.","type":"OAuthException","code":2,"is_transient":true,"fbtrace_id":"Zz"}}`, nil)
	ctx, cancel := context.WithCancel(context.Background())
	a := fbAdapter(t, srv.URL, func(c *FacebookConfig) {
		c.Sleep = func(context.Context, time.Duration) bool { cancel(); return false }
	})
	_ = a.Run(ctx, SinkFunc(func(Message) {}))
	d := a.Health().Detail
	if !strings.Contains(d, "An unexpected error has occurred") || !strings.Contains(d, "code 2") ||
		strings.Contains(d, "fbtrace") || strings.Contains(d, srv.URL) {
		t.Fatalf("detail = %q", d)
	}
}

func TestFacebookForbiddenDetailSurvivesTheRunLoop(t *testing.T) {
	srv := fbFailServer(t, http.StatusForbidden, `{"error":{"message":"nope"}}`, nil)
	ctx, cancel := context.WithCancel(context.Background())
	a := fbAdapter(t, srv.URL, func(c *FacebookConfig) {
		c.Sleep = func(context.Context, time.Duration) bool { cancel(); return false }
	})
	_ = a.Run(ctx, SinkFunc(func(Message) {}))
	if !strings.Contains(a.Health().Detail, "pages_read_engagement") {
		t.Fatalf("the permission advice was overwritten: %q", a.Health().Detail)
	}
}

// Transient failures recover: 500, 500, then a good poll goes Degraded, then
// Live, and the interval resets. Two adapters fail independently.
func TestFacebookRecoversFromTransientFailuresAndAccountsAreIndependent(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `<html>oops</html>`)
			return
		}
		fmt.Fprint(w, `{"data":[]}`)
	}))
	t.Cleanup(srv.Close)
	dead := fbFailServer(t, http.StatusBadRequest, fbBody190, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var slept []time.Duration
	var states []State
	var a *FacebookAdapter
	a = fbAdapter(t, srv.URL, func(c *FacebookConfig) {
		c.Rand = func() float64 { return 0 }
		c.Sleep = func(_ context.Context, d time.Duration) bool {
			slept = append(slept, d)
			states = append(states, a.Health().State)
			if len(slept) == 3 {
				cancel()
				return false
			}
			return true
		}
	})
	b := fbAdapter(t, dead.URL, nil)

	errc := make(chan error, 1)
	go func() { errc <- b.Run(context.Background(), SinkFunc(func(Message) {})) }()

	if err := a.Run(ctx, SinkFunc(func(Message) {})); err != nil {
		t.Fatal(err)
	}
	if want := []State{StateDegraded, StateDegraded, StateLive}; fmt.Sprint(states) != fmt.Sprint(want) {
		t.Fatalf("states = %v, want %v", states, want)
	}
	if slept[2] != 5*time.Second {
		t.Fatalf("interval did not reset after recovery: %v", slept)
	}
	if err := <-errc; !IsFatal(err) {
		t.Fatalf("the dead-token account must end on its own, got %v", err)
	}
}

func TestFacebookPollSurvivesMalformedAndTransportFailures(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"html with a 200":  func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `<html>captive portal</html>`) },
		"truncated json":   func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[{"id":"1"`) },
		"connection reset": func(w http.ResponseWriter, r *http.Request) { panic(http.ErrAbortHandler) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			a := fbAdapter(t, srv.URL, nil)
			_, err := a.poll(context.Background(), "live-1", SinkFunc(func(Message) {}))
			if err == nil {
				t.Fatal("want an error")
			}
			if done, fatal := a.classify(err); done || fatal != nil {
				t.Fatalf("a malformed answer must be transient, got done=%v fatal=%v", done, fatal)
			}
		})
	}
}

func TestFacebookModerationUnderstandsGraphEnvelopes(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantFatal  bool
		wantSubstr string
	}{
		{"400/100/33 is already gone", 400, fbBody100, false, "already be gone"},
		{"400/190 is fatal", 400, fbBody190, true, "reconnect"},
		{"a throttle says to wait", 400, fbBody4, false, "rate-limiting"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := fbFailServer(t, tc.status, tc.body, map[string]string{"Retry-After": "60"})
			a := fbAdapter(t, srv.URL, nil)
			err := a.Delete(context.Background(), "c1")
			if err == nil || IsFatal(err) != tc.wantFatal || !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("err = %v", err)
			}
			var ae *apiError
			if tc.wantSubstr == "rate-limiting" && (errors.As(err, &ae) || !strings.Contains(err.Error(), "1m0s")) {
				t.Fatalf("want a clean wait sentence, got %v", err)
			}
		})
	}
}

func TestFacebookClassifyEndsTheChatOnlyOnA404ForTheVideo(t *testing.T) {
	srv := fbFailServer(t, http.StatusNotFound, `{"error":{"message":"gone","code":803}}`, nil)
	a := fbAdapter(t, srv.URL, nil)
	_, err := a.poll(context.Background(), "live-1", SinkFunc(func(Message) {}))
	if done, fatal := a.classify(err); !done || fatal != nil || a.Health().State != StateStopped {
		t.Fatalf("done=%v fatal=%v state=%q", done, fatal, a.Health().State)
	}
}

func TestFacebookRunKeepsTheAmbiguousVideoGoneDetail(t *testing.T) {
	srv := fbFailServer(t, http.StatusBadRequest, fbBody100, nil)
	ctx, cancel := context.WithCancel(context.Background())
	a := fbAdapter(t, srv.URL, func(c *FacebookConfig) {
		c.Sleep = func(context.Context, time.Duration) bool { cancel(); return false }
	})
	_ = a.Run(ctx, SinkFunc(func(Message) {}))
	h := a.Health()
	if h.State != StateDegraded || !strings.Contains(h.Detail, "lost access") {
		t.Fatalf("health = %+v", h)
	}
}

// A Page token that alone was invalidated must not end a chat whose user token
// is fine: drop it, mint a fresh one, retry once.
func TestFacebookPollRetriesOnceWithAFreshPageTokenAfter190(t *testing.T) {
	var mints atomic.Int32
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		if strings.HasSuffix(r.Header.Get("Authorization"), "page-old") {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, fbBody190)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"c1","message":"hi","created_time":"2026-03-01T19:59:59+0000","from":{"id":"u","name":"U"}}]}`)
	}))
	t.Cleanup(srv.Close)
	a := fbAdapter(t, srv.URL, func(c *FacebookConfig) {
		c.PageAccess = func(context.Context) (string, string, error) {
			if mints.Add(1) == 1 {
				return "123", "page-old", nil
			}
			return "123", "page-new", nil
		}
	})
	var got []Message
	n, err := a.poll(context.Background(), "live-1", SinkFunc(func(m Message) { got = append(got, m) }))
	if err != nil || n != 1 || len(got) != 1 {
		t.Fatalf("n=%d err=%v got=%d", n, err, len(got))
	}
	if mints.Load() != 2 || len(auths) != 2 || !strings.HasSuffix(auths[1], "page-new") {
		t.Fatalf("mints=%d auths=%v", mints.Load(), auths)
	}

	// Through Run: the chat stays Live.
	mints.Store(0)
	auths = nil
	ctx, cancel := context.WithCancel(context.Background())
	b := fbAdapter(t, srv.URL, func(c *FacebookConfig) {
		c.PageAccess = a.cfg.PageAccess
		c.Sleep = func(context.Context, time.Duration) bool { cancel(); return false }
	})
	if err := b.Run(ctx, SinkFunc(func(Message) {})); err != nil {
		t.Fatalf("a dead Page token with a good user token ended the chat: %v", err)
	}
	if b.Health().State != StateLive {
		t.Fatalf("state = %q", b.Health().State)
	}
}

// If the retry is rejected too, the login really is gone.
func TestFacebookPollIsFatalWhenTheRetryAlso190s(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, fbBody190)
	}))
	t.Cleanup(srv.Close)
	a := fbAdapter(t, srv.URL, func(c *FacebookConfig) { c.PageAccess = fbPage("123", "page-tok") })
	_, err := a.poll(context.Background(), "live-1", SinkFunc(func(Message) {}))
	if err == nil {
		t.Fatal("want an error")
	}
	if _, fatal := a.classify(err); fatal == nil {
		t.Fatal("a second 190 must be fatal")
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want exactly one retry", calls.Load())
	}
}
