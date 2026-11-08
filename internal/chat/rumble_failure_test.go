package chat

// Failure-handling tests for the Rumble adapter. The response shapes are the
// ones recorded by live probes of rumble.com/-livestream-api: Rumble's own JSON
// envelope for a refused key, and a full HTML page for an unknown path. The
// Cloudflare cases (HTML 403, cf-mitigated) are the shapes any site behind that
// edge returns for a WAF, bot or IP-reputation block.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// rumbleRunExhaust runs the adapter with a sleep that returns at once, until
// Run gives up and returns the error the Hub would see.
func rumbleRunExhaust(t *testing.T, a *RumbleAdapter) ([]Message, error) {
	t.Helper()
	a.cfg.Sleep = func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var got []Message
	err := a.Run(ctx, SinkFunc(func(m Message) { got = append(got, m) }))
	return got, err
}

const rumbleHTMLPage = `<!doctype html><html lang=en-US><head><title>Just a moment...</title></head>` +
	`<body><h1>Checking your browser</h1><script>var k="secret-page-script";</script></body></html>`

func TestRumbleEdgeBlockIsRetriedNotTreatedAsARevokedKey(t *testing.T) {
	tests := []struct {
		name   string
		status int
		header map[string]string
		body   string
	}{
		{"HTML 403 challenge", http.StatusForbidden, nil, rumbleHTMLPage},
		{"cf-mitigated 403", http.StatusForbidden, map[string]string{"Cf-Mitigated": "challenge"}, rumbleHTMLPage},
		{"cf-mitigated even with Rumble-shaped JSON", http.StatusForbidden,
			map[string]string{"Cf-Mitigated": "challenge"}, `{"errors":[{"code":"403","message":"Forbidden"}]}`},
		{"Cloudflare JSON with its own code", http.StatusForbidden, nil,
			`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`},
		{"empty 403", http.StatusForbidden, nil, ``},
		{"HTML 401", http.StatusUnauthorized, nil, rumbleHTMLPage},
		{"HTML 400", http.StatusBadRequest, nil, rumbleHTMLPage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newRumbleStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			a := rumbleAdapter(t, s.URL, nil)

			// Polls repeatedly instead of returning on the first one.
			if _, err := rumbleRun(t, a, 3); err != nil {
				t.Fatalf("Run returned %v; an edge block must be retried in-loop, not end the runner", err)
			}
			if n := s.calls.Load(); n != 3 {
				t.Fatalf("polled %d times, want 3", n)
			}
			h := a.Health()
			if h.State == StateFailed {
				t.Errorf("state = failed; the key is not the problem")
			}
			if strings.Contains(h.Detail, "new one") || strings.Contains(h.Detail, "revoked") {
				t.Errorf("detail tells the operator to rotate a working key: %q", h.Detail)
			}

			// After the retry budget the Hub still gets a non-fatal error.
			s2 := newRumbleStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := rumbleRunExhaust(t, rumbleAdapter(t, s2.URL, nil))
			if err == nil || IsFatal(err) {
				t.Fatalf("err = %v (fatal=%v), want a retryable error", err, err != nil && IsFatal(err))
			}
		})
	}
}

func TestRumbleOwnJSONRefusalStaysFatal(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusForbidden, `{"user":{"id":null,"logged_in":false},"errors":[{"code":"403","message":"Forbidden","type":"generic"}]}`},
		{http.StatusUnauthorized, `{"errors":[{"code":401,"message":"Unauthorized"}]}`},
		{http.StatusBadRequest, `{"errors":[{"code":"invalid_request","message":"No access token found in the request","type":"generic"}]}`},
	} {
		s := newRumbleStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		})
		a := rumbleAdapter(t, s.URL, nil)
		_, err := rumbleRun(t, a, 1)
		if !IsFatal(err) {
			t.Errorf("HTTP %d: err = %v, want fatal", tc.status, err)
		}
		if a.Health().State != StateFailed {
			t.Errorf("HTTP %d: state = %v, want failed", tc.status, a.Health().State)
		}
	}
}

func TestRumbleKeepsHTMLAndURLsOutOfOperatorText(t *testing.T) {
	const key = "rumble-live-key-do-not-log-me"
	for _, status := range []int{http.StatusNotFound, http.StatusBadGateway, http.StatusServiceUnavailable} {
		s := newRumbleStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(rumbleHTMLPage))
		})
		a := rumbleAdapter(t, s.URL, func(c *RumbleConfig) { c.Key = key })
		_, err := rumbleRunExhaust(t, a)
		if err == nil {
			t.Fatalf("HTTP %d: want an error after repeated failures", status)
		}
		for _, text := range []string{err.Error(), a.Health().Detail} {
			for _, bad := range []string{"<", "doctype", "html", "script", "Just a moment", key, "key=", s.URL, "get-data"} {
				if strings.Contains(strings.ToLower(text), strings.ToLower(bad)) {
					t.Errorf("HTTP %d: operator text %q contains %q", status, text, bad)
				}
			}
			if !strings.Contains(text, "returned ") {
				t.Errorf("HTTP %d: operator text %q lost the status", status, text)
			}
		}
	}
}

func TestRumbleCarriesTheRealReasonFromItsOwnEnvelope(t *testing.T) {
	s := newRumbleStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"code":"500","message":"Internal <b>Server</b> Error"}]}`))
	})
	a := rumbleAdapter(t, s.URL, nil)
	_, err := rumbleRunExhaust(t, a)
	if err == nil || !strings.Contains(err.Error(), "returned 500: Internal Server Error") {
		t.Errorf("err = %v, want the envelope's message with markup stripped", err)
	}
}

func TestRumbleMalformedBodiesAreRetriedWithoutLeakingThem(t *testing.T) {
	for name, body := range map[string]string{
		"HTML 200":       rumbleHTMLPage,
		"truncated JSON": `{"livestreams":[{"is_live":true,"title":"t","chat":{"recent_mess`,
		"empty 200":      ``,
	} {
		t.Run(name, func(t *testing.T) {
			s := rumbleOK(t, body)
			a := rumbleAdapter(t, s.URL, nil)
			if body == "" {
				// An empty 200 decodes to nothing: offline, not a failure.
				if _, err := rumbleRun(t, a, 2); err != nil {
					t.Fatalf("empty 200: err = %v", err)
				}
				return
			}
			_, err := rumbleRunExhaust(t, a)
			if err == nil || IsFatal(err) {
				t.Fatalf("err = %v, want a non-fatal error", err)
			}
			if strings.ContainsAny(err.Error(), "<{") || strings.Contains(err.Error(), "Just a moment") {
				t.Errorf("error carries body text: %v", err)
			}
		})
	}
}

func TestRumbleTimeoutIsRetriedWithoutTheURL(t *testing.T) {
	block := make(chan struct{})
	s := newRumbleStub(t, func(w http.ResponseWriter, r *http.Request, _ int64) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(block) })
	a := rumbleAdapter(t, s.URL, func(c *RumbleConfig) {
		c.HTTP = &http.Client{Timeout: 30 * time.Millisecond}
	})
	_, err := rumbleRunExhaust(t, a)
	if err == nil || IsFatal(err) {
		t.Fatalf("err = %v, want a non-fatal timeout", err)
	}
	if strings.Contains(err.Error(), "key=") || strings.Contains(err.Error(), s.URL) {
		t.Errorf("timeout error carries the request URL: %v", err)
	}
}

func TestRumbleHonoursRetryAfterOn429(t *testing.T) {
	s := newRumbleStub(t, func(w http.ResponseWriter, _ *http.Request, _ int64) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"errors":[{"code":"429","message":"Too Many Requests"}]}`))
	})
	a := rumbleAdapter(t, s.URL, nil)
	var waits []time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.cfg.Sleep = func(_ context.Context, d time.Duration) bool {
		waits = append(waits, d)
		if len(waits) >= 2 {
			cancel()
			return false
		}
		return true
	}
	_ = a.Run(ctx, SinkFunc(func(Message) {}))
	if len(waits) < 2 {
		t.Fatalf("waits = %v", waits)
	}
	for _, w := range waits {
		if w < 120*time.Second {
			t.Errorf("waited %v after Retry-After: 120, retrying before the limit lifts", w)
		}
	}
}

func TestRumbleCapsAHugeRetryAfterAndParsesDates(t *testing.T) {
	a := rumbleAdapter(t, "http://unused", nil)
	if d := a.retryDelay(0, 24*time.Hour); d != rumbleRetryAfterCap {
		t.Errorf("delay = %v, want capped at %v", d, rumbleRetryAfterCap)
	}
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	if d := oauth.ParseRetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now); d < 89*time.Second || d > 90*time.Second {
		t.Errorf("HTTP-date Retry-After = %v, want ~90s", d)
	}
	if d := oauth.ParseRetryAfter("garbage", now); d != 0 {
		t.Errorf("garbage Retry-After = %v, want 0", d)
	}
	// Backoff grows, stays under the ceiling, and jitter never exceeds it.
	for i := 0; i < 20; i++ {
		if d := a.retryDelay(i, 0); d > rumbleRetryCeil || d <= 0 {
			t.Errorf("retryDelay(%d) = %v outside (0, %v]", i, d, rumbleRetryCeil)
		}
	}
}

// A 503 in the middle of a live show must not blank the audience or reset the
// adapter: the viewer count survives the blip and the state returns to live.
func TestRumbleRecoversFromATransient503WithoutLosingTheAudience(t *testing.T) {
	live := rumbleBody(1, 0, `"followers":{"num_followers":1200},`)
	s := newRumbleStub(t, func(w http.ResponseWriter, _ *http.Request, n int64) {
		if n == 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(rumbleHTMLPage))
			return
		}
		_, _ = w.Write([]byte(live))
	})
	a := rumbleAdapter(t, s.URL, nil)

	var duringBlip Health
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sleeps := 0
	a.cfg.Sleep = func(context.Context, time.Duration) bool {
		sleeps++
		if sleeps == 2 { // after the failed poll
			duringBlip = a.Health()
		}
		if sleeps >= 3 {
			cancel()
			return false
		}
		return true
	}
	if err := a.Run(ctx, SinkFunc(func(Message) {})); err != nil {
		t.Fatalf("Run = %v, want the 503 absorbed", err)
	}
	if duringBlip.State != StateDegraded || duringBlip.Viewers == nil || *duringBlip.Viewers != 3 ||
		duringBlip.Followers == nil || *duringBlip.Followers != 1200 {
		t.Errorf("during the blip health = %+v, want degraded with the counts kept", duringBlip)
	}
	if h := a.Health(); h.State != StateLive || h.Viewers == nil {
		t.Errorf("after recovery health = %+v, want live", h)
	}
}
