package oauth

// Failure handling for the Kick provider, against the error shapes recorded in
// the 2026-11 failure audit: a bad token is a bare 401 with
// {"data":{},"message":"Unauthorized"}, from behind Cloudflare.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	kickBad401   = `{"data":{},"message":"Unauthorized"}`
	kickEdgeHTML = `<!DOCTYPE html><html><head><title>Attention Required! | Cloudflare</title></head>` +
		`<body><h1>Sorry, you have been blocked</h1></body></html>`
)

func kickStatus(status int, ctype, body string, hdr map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", ctype)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func asStatus(t *testing.T, err error) *StatusError {
	t.Helper()
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v (%T), want a *StatusError", err, err)
	}
	return se
}

func TestKickA401IsATokenProblemOnEveryPath(t *testing.T) {
	k, srv := kickStub(t, kickStatus(401, "application/json", kickBad401, nil))
	ctx := context.Background()

	paths := map[string]error{}
	_, paths["Account"] = k.Account(ctx, "cid", "tok")
	_, paths["Ingest"] = k.Ingest(ctx, "cid", "tok")
	_, paths["Stats"] = k.Stats(ctx, "cid", "tok")
	paths["UpdateChannel"] = k.UpdateChannel(ctx, "tok", KickChannelUpdate{StreamTitle: "t"})
	_, paths["PushMetadata"] = k.PushMetadata(ctx, "cid", "tok", "1", Metadata{Title: "t"})

	for name, err := range paths {
		if err == nil {
			t.Fatalf("%s: a 401 reported success", name)
		}
		se := asStatus(t, err)
		if se.Status != 401 || !IsTokenInvalid(err) {
			t.Errorf("%s: status %d, IsTokenInvalid=%v; want a 401 the caller can act on", name, se.Status, IsTokenInvalid(err))
		}
		msg := err.Error()
		if !strings.Contains(msg, "reconnect") || strings.Contains(msg, "channel:write") || strings.Contains(msg, "never granted") {
			t.Errorf("%s: %q must say reconnect and not blame a scope", name, msg)
		}
		if strings.Contains(msg, srv.URL) || strings.Contains(msg, "http") {
			t.Errorf("%s: %q carries the request URL", name, msg)
		}
	}
}

func TestKickA403CarriesKicksMessageNotTheRawBody(t *testing.T) {
	k, srv := kickStub(t, kickStatus(403, "application/json", `{"data":null,"message":"Forbidden"}`, nil))
	err := k.UpdateChannel(context.Background(), "tok", KickChannelUpdate{StreamTitle: "t"})
	msg := asStatus(t, err).Error()
	if !strings.Contains(msg, "Forbidden") || strings.Contains(msg, `{"data"`) || strings.Contains(msg, srv.URL) {
		t.Fatalf("403 message %q should quote Kick's message only", msg)
	}
	_, err = k.PushMetadata(context.Background(), "cid", "tok", "1", Metadata{Title: "t"})
	if err == nil || !strings.Contains(err.Error(), "channel:write") || !strings.Contains(err.Error(), "Forbidden") {
		t.Fatalf("PushMetadata 403 = %v, want the scope and Kick's message", err)
	}
}

func TestKickEdgeBlockIsNotAPermissionProblem(t *testing.T) {
	k, srv := kickStub(t, kickStatus(403, "text/html", kickEdgeHTML, map[string]string{"Cf-Ray": "8a1b-LAX", "Server": "cloudflare"}))
	ctx := context.Background()

	for name, err := range map[string]error{
		"UpdateChannel": k.UpdateChannel(ctx, "tok", KickChannelUpdate{StreamTitle: "t"}),
		"Stats":         func() error { _, e := k.Stats(ctx, "cid", "tok"); return e }(),
		"Account":       func() error { _, e := k.Account(ctx, "cid", "tok"); return e }(),
	} {
		if err == nil {
			t.Fatalf("%s: a blocked request reported success", name)
		}
		msg := err.Error()
		if !strings.Contains(msg, "edge") || !strings.Contains(msg, "reconnecting will not help") {
			t.Errorf("%s: %q does not say the edge blocked it", name, msg)
		}
		if strings.Contains(msg, "<") || strings.Contains(msg, "channel:write") || strings.Contains(msg, srv.URL) {
			t.Errorf("%s: %q leaks HTML, a scope, or the URL", name, msg)
		}
		if !asStatus(t, err).Edge {
			t.Errorf("%s: Edge flag not set", name)
		}
	}
	_, err := k.PushMetadata(ctx, "cid", "tok", "1", Metadata{Title: "t"})
	if err == nil || strings.Contains(err.Error(), "channel:write") || strings.Contains(err.Error(), "disconnect") {
		t.Fatalf("PushMetadata on an edge block = %v; it must not tell the operator to reconnect", err)
	}
}

func TestKickHTMLOn200IsNotAJSONSyntaxError(t *testing.T) {
	k, _ := kickStub(t, kickStatus(200, "text/html", kickEdgeHTML, nil))
	_, err := k.Account(context.Background(), "cid", "tok")
	if err == nil {
		t.Fatal("a web page in place of the channel reported success")
	}
	if strings.Contains(err.Error(), "invalid character") || !strings.Contains(err.Error(), "web page") {
		t.Fatalf("err = %q, want a plain web-page message", err)
	}
}

func TestKickRateLimitKeepsRetryAfterCappedAndJittered(t *testing.T) {
	for _, tc := range []struct {
		retry     string
		min, max  time.Duration
		wantInMsg string
	}{
		{"30", 30 * time.Second, 37 * time.Second, "wait 30s"},
		{"7200", 5 * time.Minute, 6 * time.Minute, "wait 5m0s"},
		{"", 0, 0, "wait a moment"},
	} {
		hdr := map[string]string{}
		if tc.retry != "" {
			hdr["Retry-After"] = tc.retry
		}
		k, _ := kickStub(t, kickStatus(429, "application/json", `{"data":null,"message":"Too Many Requests"}`, hdr))
		ctx := context.Background()
		for name, err := range map[string]error{
			"Stats (getJSON)":       func() error { _, e := k.Stats(ctx, "cid", "tok"); return e }(),
			"UpdateChannel (PATCH)": k.UpdateChannel(ctx, "tok", KickChannelUpdate{StreamTitle: "t"}),
		} {
			se := asStatus(t, err)
			if se.Status != 429 {
				t.Fatalf("%s: status %d", name, se.Status)
			}
			if se.RetryAfter < tc.min || se.RetryAfter > tc.max {
				t.Errorf("%s: Retry-After %q gave RetryAfter %s, want within [%s, %s]", name, tc.retry, se.RetryAfter, tc.min, tc.max)
			}
			if !strings.Contains(err.Error(), tc.wantInMsg) || !strings.Contains(err.Error(), "Too Many Requests") {
				t.Errorf("%s: %q should say %q and quote Kick", name, err, tc.wantInMsg)
			}
		}
	}
}

func TestKickStatsDoesNotDoubleTrafficAgainstADeadTokenOrALimit(t *testing.T) {
	for _, status := range []int{401, 429} {
		var calls atomic.Int32
		k, _ := kickStub(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			kickStatus(status, "application/json", kickBad401, nil)(w, r)
		})
		_, err := k.Stats(context.Background(), "cid", "tok")
		if err == nil || asStatus(t, err).Status != status {
			t.Fatalf("status %d: err = %v", status, err)
		}
		if n := calls.Load(); n != 1 {
			t.Errorf("status %d: %d calls, want 1 (no fallback to /channels)", status, n)
		}
	}
}

func TestKickStatsFallbackStillRunsOnAScopeRefusal(t *testing.T) {
	var calls atomic.Int32
	k, _ := kickStub(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.HasSuffix(r.URL.Path, "/users/livestreams") {
			kickStatus(403, "application/json", `{"message":"Forbidden"}`, nil)(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"stream":{"is_live":true,"viewer_count":5}}]}`))
	})
	st, err := k.Stats(context.Background(), "cid", "tok")
	if err != nil || !st.Live || calls.Load() != 2 {
		t.Fatalf("stats=%+v err=%v calls=%d; a 403 must still fall back to /channels", st, err, calls.Load())
	}
}

func TestKickStatsReportsUnreadableLivestreamDataInsteadOfOffline(t *testing.T) {
	// Present, but neither a list nor an object.
	k, _ := kickStub(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/users/livestreams") {
			_, _ = w.Write([]byte(`{"data":"corrupt"}`))
			return
		}
		kickStatus(500, "application/json", `{"message":"boom"}`, nil)(w, r)
	})
	_, err := k.Stats(context.Background(), "cid", "tok")
	if err == nil {
		t.Fatal("an unreadable livestream list was reported as an offline channel")
	}
	if _, derr := decodeKickData([]byte(`"corrupt"`)); derr == nil {
		t.Fatal("decodeKickData accepted a string")
	}
	if got, derr := decodeKickData([]byte(`null`)); derr != nil || len(got) != 0 {
		t.Fatalf("null should be an empty list, got %v, %v", got, derr)
	}
}

func TestKickError(t *testing.T) {
	if kickError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
	plain := errors.New("dial tcp: connection refused")
	if kickError(plain) != plain {
		t.Fatal("a transport error must pass through untouched")
	}
}
