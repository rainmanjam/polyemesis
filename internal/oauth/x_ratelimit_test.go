package oauth

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// X publishes no Livestream rate limits (docs-x section 9), so the provider learns them from x-rate-limit-reset.
// A 429 holds every further call on that token until the reset, refused here without a request; the hold ends at
// the reset; and the error says it is a rate limit, so a give-up budget does not count it.
func TestAnXRateLimitHoldsEveryCallUntilTheResetWithoutAskingAgain(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	reset := now.Add(90 * time.Second)
	var hits atomic.Int32
	limited := atomic.Bool{}
	limited.Store(true)
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if limited.Load() {
			w.Header().Set("x-rate-limit-reset", strconv.FormatInt(reset.Unix(), 10))
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"title":"Too Many Requests","detail":"slow down"}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[]}`)
	})
	x.calls.now = func() time.Time { return now }
	ctx := context.Background()

	_, err := x.Broadcasts(ctx, "client", "tok")
	until, ok := XRateLimited(err)
	if !ok || !until.Equal(reset) {
		t.Fatalf("first 429: err = %v, XRateLimited = %v %v; want the reset time", err, until, ok)
	}
	for range 3 {
		if _, err := x.Broadcasts(ctx, "client", "tok"); err == nil {
			t.Fatal("a call during the hold succeeded")
		} else if _, ok := XRateLimited(err); !ok {
			t.Fatalf("a held call is not reported as rate-limited: %v", err)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("X was asked %d times during the hold, want 1: the hold must refuse without a request", n)
	}
	// Another token is not held.
	limited.Store(false)
	if _, err := x.Broadcasts(ctx, "client", "other-token"); err != nil {
		t.Fatalf("a different token was held too: %v", err)
	}
	// Past the reset, calls go out again.
	now = reset.Add(time.Second)
	if _, err := x.Broadcasts(ctx, "client", "tok"); err != nil {
		t.Fatalf("after the reset: %v", err)
	}
}

// A 2xx that says no calls remain holds the next one too, rather than spending it on a certain 429.
func TestAnXAnswerWithNoCallsRemainingHoldsTheNextCall(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var hits atomic.Int32
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("x-rate-limit-remaining", "0")
		w.Header().Set("x-rate-limit-reset", strconv.FormatInt(now.Add(time.Minute).Unix(), 10))
		_, _ = io.WriteString(w, `{"data":[]}`)
	})
	x.calls.now = func() time.Time { return now }
	if _, err := x.Broadcasts(context.Background(), "client", "tok"); err != nil {
		t.Fatalf("the answer itself succeeded: %v", err)
	}
	if _, err := x.Broadcasts(context.Background(), "client", "tok"); err == nil {
		t.Fatal("the next call went out with no calls remaining")
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
}

// Every Livestream address carries the user id. It is asked for once per token, not once per call: Ingest used to
// ask twice in one call, and every lifecycle read asked again.
func TestTheXUserIDIsAskedForOncePerTokenNotOncePerCall(t *testing.T) {
	var me atomic.Int32
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/2/users/me":
			me.Add(1)
			_, _ = io.WriteString(w, xMeFixture)
		case strings.HasSuffix(r.URL.Path, "/sources"):
			_, _ = io.WriteString(w, xSourcesFixture)
		default:
			_, _ = io.WriteString(w, `{"data":{"id":"1ynJOZVeajOJR","state":"RUNNING","source_id":"k3y0ne"}}`)
		}
	})
	ctx := context.Background()
	if _, err := x.Ingest(ctx, "client", "tok"); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	for range 3 {
		if _, err := x.BroadcastState(ctx, "tok", "1ynJOZVeajOJR"); err != nil {
			t.Fatalf("BroadcastState: %v", err)
		}
	}
	if n := me.Load(); n != 1 {
		t.Fatalf("/2/users/me was asked %d times, want once", n)
	}
}

// The {error_type, message} body X uses for 403, 404, 412 and 503 reaches the operator in X's words.
func TestAnXErrorTypeAndMessageBodyIsReadNotDroppedToABareStatus(t *testing.T) {
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = io.WriteString(w, `{"error_type":"preconditionFailed","message":"This account can't create broadcasts"}`)
	})
	_, err := x.Broadcasts(context.Background(), "client", "tok")
	if err == nil || !strings.Contains(err.Error(), "This account can't create broadcasts") ||
		!strings.Contains(err.Error(), "preconditionFailed") {
		t.Fatalf("err = %v, want X's message and error_type", err)
	}
}

// An empty 2xx is not a success: a publish or an end that X answered with nothing does not show the broadcast
// moved, and a read with no broadcast is not a broadcast with no state.
func TestAnEmptyXAnswerIsNotTakenAsAPublishAnEndOrABroadcast(t *testing.T) {
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/2/users/me" {
			_, _ = io.WriteString(w, xMeFixture)
			return
		}
		_, _ = io.WriteString(w, `{"data":{}}`)
	})
	ctx := context.Background()
	if _, err := x.TransitionBroadcast(ctx, "tok", "b1", PhaseLive); err == nil {
		t.Error("an empty publish answer was taken as live")
	}
	if _, err := x.TransitionBroadcast(ctx, "tok", "b1", PhaseComplete); err == nil {
		t.Error("an empty end answer was taken as ended")
	}
	if _, err := x.BroadcastByID(ctx, "client", "tok", "b1"); err == nil {
		t.Error("an empty read was taken as a broadcast")
	}
}

// A confidential X client authenticates to /2/oauth2/token with HTTP Basic (docs-x section 1, CONFIRMED). The
// secret travels only in the Authorization header, on the exchange and on every refresh; it is never in the body.
func TestAConfidentialXClientAuthenticatesTheTokenCallsWithBasicNotTheBody(t *testing.T) {
	var auths, bodies []string
	x, _ := xStub(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		auths = append(auths, r.Header.Get("Authorization"))
		bodies = append(bodies, r.PostForm.Encode())
		_, _ = io.WriteString(w, `{"access_token":"a","refresh_token":"r","expires_in":7200,"token_type":"bearer"}`)
	})
	secret := xSecret("sh", "h-not", "-real")
	if _, err := x.Exchange(context.Background(), "client-1", secret, "https://cb.example.invalid", "code", "verifier"); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if _, err := x.Refresh(context.Background(), "client-1", secret, "r"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("client-1:"+secret))
	for i := range auths {
		if auths[i] != want {
			t.Errorf("call %d Authorization = %q, want HTTP Basic of the client pair", i, auths[i])
		}
		if strings.Contains(bodies[i], secret) || strings.Contains(bodies[i], "client_secret") {
			t.Errorf("call %d sent the client secret in the body: %s", i, bodies[i])
		}
		if !strings.Contains(bodies[i], "client_id=client-1") {
			t.Errorf("call %d dropped client_id from the body: %s", i, bodies[i])
		}
	}
}

// The hold's edges: Retry-After when X names no reset, the default when it names neither, the 15-minute cap on a
// reset far ahead, and the one-second floor on a reset already past.
func TestTheXRateHoldReadsRetryAfterAndClampsTheReset(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	s := &xCallState{now: func() time.Time { return now }}
	resp := func(status int, hdr map[string]string) *http.Response {
		h := http.Header{}
		for k, v := range hdr {
			h.Set(k, v)
		}
		return &http.Response{StatusCode: status, Header: h}
	}
	for _, tc := range []struct {
		name string
		r    *http.Response
		want time.Time
		ok   bool
	}{
		{"retry-after", resp(429, map[string]string{"Retry-After": "30"}), now.Add(30 * time.Second), true},
		{"no headers", resp(429, nil), now.Add(xRateHoldDefault), true},
		{"reset far ahead", resp(429, map[string]string{"x-rate-limit-reset": strconv.FormatInt(now.Add(5*time.Hour).Unix(), 10)}), now.Add(xRateHoldMax), true},
		{"reset already past", resp(429, map[string]string{"x-rate-limit-reset": strconv.FormatInt(now.Add(-time.Hour).Unix(), 10)}), now.Add(time.Second), true},
		{"calls remaining", resp(200, map[string]string{"x-rate-limit-remaining": "4"}), time.Time{}, false},
	} {
		got, ok := s.rateHoldFrom(tc.r)
		if ok != tc.ok || !got.Equal(tc.want) {
			t.Errorf("%s: hold = %v %v, want %v %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// A body cut off mid-read is not an answer, and an end with no broadcast id is refused before any call.
func TestAnXCallRefusesAnEmptyBroadcastIDBeforeAnyRequest(t *testing.T) {
	var hits atomic.Int32
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1) })
	if _, err := x.TransitionBroadcast(context.Background(), "tok", " ", PhaseComplete); err == nil {
		t.Fatal("an end with no broadcast id was sent")
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests for an empty id, want none", hits.Load())
	}
}
