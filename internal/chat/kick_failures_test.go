package chat

// Failure handling for the Kick adapter: the shapes below are the real ones
// recorded in the 2026-11 failure audit (Kick answers a bad token with a plain
// 401 and {"data":{},"message":"Unauthorized"}, from behind Cloudflare), and
// what each case pins is that the operator is told the true cause.

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const kickUnauthorized = `{"data":{},"message":"Unauthorized"}`

// signKickAt is signKick with the id and timestamp chosen by the test.
func signKickAt(t *testing.T, key *rsa.PrivateKey, req *http.Request, body []byte, id, ts string) {
	t.Helper()
	digest := sha256.Sum256([]byte(id + "." + ts + "." + string(body)))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(KickHeaderMessageID, id)
	req.Header.Set(KickHeaderTimestamp, ts)
	req.Header.Set(KickHeaderSignature, base64.StdEncoding.EncodeToString(sig))
}

func kickPEM(t *testing.T, pub *rsa.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func otherKickKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

var kickNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func newSignedReq(t *testing.T, key *rsa.PrivateKey, id string, at time.Time) (*http.Request, []byte) {
	t.Helper()
	body := []byte(`{"content":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	signKickAt(t, key, req, body, id, at.Format(time.RFC3339))
	return req, body
}

func staticKeyServer(t *testing.T, pub *rsa.PublicKey) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"data":{"public_key":%q}}`, kickPEM(t, pub))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// --- replay ------------------------------------------------------------------

func TestKickVerifierRejectsAStaleTimestamp(t *testing.T) {
	key := testKickKey(t)
	srv := staticKeyServer(t, &key.PublicKey)
	verify := kickVerifier(&KickKeyFetcher{HTTP: srv.Client(), URL: srv.URL}, func() time.Time { return kickNow })

	for name, at := range map[string]time.Time{
		"an hour old":     kickNow.Add(-time.Hour),
		"from the future": kickNow.Add(time.Hour),
	} {
		t.Run(name, func(t *testing.T) {
			req, body := newSignedReq(t, key, "01HSTALE"+name, at)
			if err := verify(req, body); !errors.Is(err, ErrKickSignature) {
				t.Fatalf("a correctly signed delivery %s returned %v, want ErrKickSignature", name, err)
			}
		})
	}

	req, body := newSignedReq(t, key, "01HFRESH", kickNow.Add(-2*time.Minute))
	if err := verify(req, body); err != nil {
		t.Fatalf("a delivery inside the clock-skew window was refused: %v", err)
	}
}

func TestKickVerifierSpotsAReplayedMessageID(t *testing.T) {
	key := testKickKey(t)
	srv := staticKeyServer(t, &key.PublicKey)
	verify := kickVerifier(&KickKeyFetcher{HTTP: srv.Client(), URL: srv.URL}, func() time.Time { return kickNow })

	req, body := newSignedReq(t, key, "01HONCE", kickNow)
	if err := verify(req, body); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	req, body = newSignedReq(t, key, "01HONCE", kickNow)
	if err := verify(req, body); !errors.Is(err, ErrKickDuplicate) {
		t.Fatalf("a replay returned %v, want ErrKickDuplicate", err)
	}
}

func TestKickForgeryCannotPoisonTheReplayMemory(t *testing.T) {
	// The id is only remembered once the signature has checked out, so a
	// stranger cannot burn a genuine message's id in advance.
	key := testKickKey(t)
	srv := staticKeyServer(t, &key.PublicKey)
	verify := kickVerifier(&KickKeyFetcher{HTTP: srv.Client(), URL: srv.URL}, func() time.Time { return kickNow })

	forged, body := newSignedReq(t, otherKickKey(t), "01HTARGET", kickNow)
	if err := verify(forged, body); !errors.Is(err, ErrKickSignature) {
		t.Fatalf("forgery returned %v", err)
	}
	real, body := newSignedReq(t, key, "01HTARGET", kickNow)
	if err := verify(real, body); err != nil {
		t.Fatalf("the genuine delivery was refused after a forgery used its id: %v", err)
	}
}

func TestKickHandlerAcknowledgesADuplicateWithoutDeliveringItTwice(t *testing.T) {
	var delivered atomic.Int32
	a := kickAdapter(t, func(c *KickConfig) {
		seen := false
		c.Verify = func(*http.Request, []byte) error {
			if seen {
				return ErrKickDuplicate
			}
			seen = true
			return nil
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Run(ctx, SinkFunc(func(Message) { delivered.Add(1) })) }()
	waitFor(t, "the sink", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.sink != nil })

	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	for i := 0; i < 2; i++ {
		resp, err := srv.Client().Post(srv.URL, "application/json", strings.NewReader(kickFullEvent))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("delivery %d returned %d, want 200 so Kick stops retrying", i, resp.StatusCode)
		}
	}
	waitFor(t, "the first delivery", func() bool { return delivered.Load() >= 1 })
	time.Sleep(50 * time.Millisecond)
	if n := delivered.Load(); n != 1 {
		t.Fatalf("%d deliveries reached the sink, want 1", n)
	}
}

// --- key rotation ------------------------------------------------------------

func TestKickVerifierPicksUpARotatedKey(t *testing.T) {
	oldKey, newKey := testKickKey(t), otherKickKey(t)
	var serving atomic.Pointer[rsa.PublicKey]
	serving.Store(&oldKey.PublicKey)
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		fmt.Fprintf(w, `{"data":{"public_key":%q}}`, kickPEM(t, serving.Load()))
	}))
	defer srv.Close()

	clock := kickNow
	f := &KickKeyFetcher{HTTP: srv.Client(), URL: srv.URL, Now: func() time.Time { return clock }}
	verify := kickVerifier(f, func() time.Time { return clock })

	req, body := newSignedReq(t, oldKey, "01HBEFORE", clock)
	if err := verify(req, body); err != nil {
		t.Fatalf("before rotation: %v", err)
	}

	serving.Store(&newKey.PublicKey) // Kick rotates
	clock = clock.Add(2 * time.Minute)

	req, body = newSignedReq(t, newKey, "01HAFTER", clock)
	if err := verify(req, body); err != nil {
		t.Fatalf("a genuine delivery signed with the rotated key was refused: %v", err)
	}
	if n := fetches.Load(); n != 2 {
		t.Fatalf("key fetched %d times, want 2 (initial + one refetch)", n)
	}
}

func TestKickKeyRefetchIsRateLimited(t *testing.T) {
	key := testKickKey(t)
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		fmt.Fprintf(w, `{"data":{"public_key":%q}}`, kickPEM(t, &key.PublicKey))
	}))
	defer srv.Close()
	clock := kickNow
	f := &KickKeyFetcher{HTTP: srv.Client(), URL: srv.URL, Now: func() time.Time { return clock }}
	verify := kickVerifier(f, func() time.Time { return clock })

	forger := otherKickKey(t)
	for i := 0; i < 5; i++ {
		req, body := newSignedReq(t, forger, fmt.Sprintf("01HF%d", i), clock)
		if err := verify(req, body); !errors.Is(err, ErrKickSignature) {
			t.Fatalf("forgery %d returned %v", i, err)
		}
	}
	// One fetch to learn the key; the first forgery may refetch once (the key
	// was fetched this instant, so it does not); the rest are within a minute.
	if n := fetches.Load(); n != 1 {
		t.Fatalf("a burst of forgeries caused %d key fetches, want 1", n)
	}
	clock = clock.Add(2 * time.Minute)
	req, body := newSignedReq(t, forger, "01HLATER", clock)
	_ = verify(req, body)
	if n := fetches.Load(); n != 2 {
		t.Fatalf("after the window a failure should refetch once: %d fetches", n)
	}
}

// --- slow / failing key endpoint ---------------------------------------------

func TestKickKeyFetchIsSharedAndDoesNotBlockOnALock(t *testing.T) {
	key := testKickKey(t)
	release := make(chan struct{})
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		<-release
		fmt.Fprintf(w, `{"data":{"public_key":%q}}`, kickPEM(t, &key.PublicKey))
	}))
	defer srv.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	f := &KickKeyFetcher{HTTP: srv.Client(), URL: srv.URL}

	// A caller whose own deadline is short gives up on time even though the
	// fetch it joined is still running. Before the fix every caller queued on
	// one mutex held across the network call.
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := f.Key(short); err == nil {
		t.Fatal("Key returned a key while the endpoint was stalled")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Key held its caller for %s past its deadline", d)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.Key(context.Background()); err != nil {
				t.Errorf("Key: %v", err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := fetches.Load(); n != 1 {
		t.Fatalf("concurrent callers caused %d fetches, want 1 shared", n)
	}
}

func TestKickHandlerAsksForARetryWhenTheKeyCannotBeFetched(t *testing.T) {
	keySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer keySrv.Close()
	a := kickAdapter(t, func(c *KickConfig) {
		c.Verify = KickVerifier(&KickKeyFetcher{HTTP: keySrv.Client(), URL: keySrv.URL})
	})
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(kickFullEvent))
	signKick(t, testKickKey(t), req, []byte(kickFullEvent))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: a 401 would count against Kick's one-day auto-disable", resp.StatusCode)
	}
}

func TestKickHealthSaysSignatureVerificationIsFailing(t *testing.T) {
	a := kickAdapter(t, func(c *KickConfig) {
		c.Verify = func(*http.Request, []byte) error { return ErrKickSignature }
	})
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	for i := 0; i < kickRejectedAfter; i++ {
		resp, err := srv.Client().Post(srv.URL, "application/json", strings.NewReader(kickFullEvent))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	a.mu.Lock()
	a.reachable = true
	a.mu.Unlock()
	a.refreshHealth()
	h := a.Health()
	if h.State != StateDegraded || !strings.Contains(h.Detail, "failing signature verification") {
		t.Fatalf("health = %+v, want degraded naming signature verification", h)
	}
	if strings.Contains(h.Detail, "no Kick chat events have arrived") {
		t.Fatalf("health blames the webhook URL for deliveries that did arrive: %q", h.Detail)
	}
}

// --- 401 vs 403 vs the edge --------------------------------------------------

func kickStatusServer(t *testing.T, status int, contentType, body string, hdr map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestKickModerationTellsABadTokenFromAMissingScope(t *testing.T) {
	calls := map[string]func(a *KickAdapter) error{
		"delete": func(a *KickAdapter) error { return a.Delete(context.Background(), "msg-1") },
		"ban":    func(a *KickAdapter) error { return a.Ban(context.Background(), "77", time.Minute, "") },
		"unban":  func(a *KickAdapter) error { return a.Unban(context.Background(), "77") },
	}
	for name, call := range calls {
		t.Run(name+" 401", func(t *testing.T) {
			srv := kickStatusServer(t, 401, "application/json", kickUnauthorized, nil)
			a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL })
			err := call(a)
			if err == nil || !IsFatal(err) {
				t.Fatalf("err = %v, want a fatal reconnect error", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "rejected the access token") || !strings.Contains(msg, "reconnect") {
				t.Fatalf("401 message %q does not say the token was rejected", msg)
			}
			if strings.Contains(msg, "never granted") || strings.Contains(msg, "moderation:") {
				t.Fatalf("401 message %q blames a missing scope", msg)
			}
			if strings.Contains(msg, srv.URL) || strings.Contains(msg, "http") {
				t.Fatalf("401 message %q carries the request URL", msg)
			}
		})
		t.Run(name+" 403", func(t *testing.T) {
			srv := kickStatusServer(t, 403, "application/json", `{"data":null,"message":"Forbidden"}`, nil)
			a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL })
			err := call(a)
			if err == nil || IsFatal(err) {
				t.Fatalf("err = %v, want a non-fatal scope/permission error", err)
			}
			if !strings.Contains(err.Error(), "moderation:") || !strings.Contains(err.Error(), "moderator") {
				t.Fatalf("403 message %q names neither the scope nor moderator status", err)
			}
			if strings.Contains(err.Error(), srv.URL) || strings.Contains(err.Error(), `{"data"`) {
				t.Fatalf("403 message %q echoes the URL or raw body", err)
			}
		})
		t.Run(name+" 404", func(t *testing.T) {
			srv := kickStatusServer(t, 404, "application/json", `{"data":{},"message":"Not Found"}`, nil)
			a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL })
			err := call(a)
			if err == nil || strings.Contains(err.Error(), srv.URL) || !strings.Contains(err.Error(), "Not Found") {
				t.Fatalf("404 message = %v, want Kick's words without the URL", err)
			}
		})
	}
}

const cloudflareBlock = `<!DOCTYPE html><html><head><title>Attention Required! | Cloudflare</title></head>` +
	`<body><h1>Sorry, you have been blocked</h1><p>Ray ID: 8a1b2c3d</p></body></html>`

func TestKickHTMLErrorPageIsTheEdgeNotAPermissionProblem(t *testing.T) {
	srv := kickStatusServer(t, 403, "text/html", cloudflareBlock, map[string]string{"Cf-Ray": "8a1b2c3d-LAX", "Server": "cloudflare"})
	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL })

	for name, call := range map[string]func() error{
		"send":   func() error { _, err := a.Send(context.Background(), "hi"); return err },
		"delete": func() error { return a.Delete(context.Background(), "m") },
		"ban":    func() error { return a.Ban(context.Background(), "7", time.Minute, "") },
	} {
		err := call()
		if err == nil {
			t.Fatalf("%s: a blocked request reported success", name)
		}
		msg := err.Error()
		if !strings.Contains(msg, "edge") || !strings.Contains(msg, "reconnecting will not help") {
			t.Fatalf("%s: %q does not say the edge blocked the request", name, msg)
		}
		if strings.Contains(msg, "<") || strings.Contains(msg, "chat:write") || IsFatal(err) {
			t.Fatalf("%s: %q leaks HTML, blames a scope, or is terminal", name, msg)
		}
	}
}

func TestKickHTMLSuccessBodyIsNotReportedAsAJSONSyntaxError(t *testing.T) {
	srv := kickStatusServer(t, 200, "text/html", cloudflareBlock, nil)
	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL })
	_, err := a.Send(context.Background(), "hi")
	if err == nil {
		t.Fatal("a web page in place of the send response reported success")
	}
	if strings.Contains(err.Error(), "invalid character") || !strings.Contains(err.Error(), "web page") {
		t.Fatalf("err = %q, want a plain-language web-page error", err)
	}
}

func TestKickRateLimitQuotesRetryAfterCapped(t *testing.T) {
	for _, tc := range []struct {
		retry, want string
	}{
		{"", "wait a moment"},
		{"30", "wait 30s"},
		{"7200", "wait 5m0s"},
	} {
		hdr := map[string]string{}
		if tc.retry != "" {
			hdr["Retry-After"] = tc.retry
		}
		srv := kickStatusServer(t, 429, "application/json", `{"data":null,"message":"Too Many Requests"}`, hdr)
		a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL })
		_, err := a.Send(context.Background(), "hi")
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Too Many Requests") {
			t.Fatalf("Retry-After %q: err = %v, want %q and Kick's message", tc.retry, err, tc.want)
		}
	}
}

// --- refresh and retry on 401 ------------------------------------------------

func TestKickRetriesOnceWithARefreshedTokenAfter401(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, kickUnauthorized)
			return
		}
		fmt.Fprint(w, `{"data":{"is_sent":true,"message_id":"m1"}}`)
	}))
	defer srv.Close()
	var reauths int
	a := kickAdapter(t, func(c *KickConfig) {
		c.APIBase = srv.URL
		c.Token = StaticToken("stale")
		c.Reauth = func(context.Context) (string, error) { reauths++; return "fresh", nil }
	})
	m, err := a.Send(context.Background(), "hi")
	if err != nil || m.ID != "m1" {
		t.Fatalf("Send = %+v, %v; want success on the retry", m, err)
	}
	if reauths != 1 || len(seen) != 2 {
		t.Fatalf("reauths=%d requests=%v, want one refresh and one retry", reauths, seen)
	}
}

func TestKickDoesNotLoopWhenTheFreshTokenIsRefusedToo(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, kickUnauthorized)
	}))
	defer srv.Close()
	a := kickAdapter(t, func(c *KickConfig) {
		c.APIBase = srv.URL
		c.Reauth = func(context.Context) (string, error) { return "fresh", nil }
	})
	_, err := a.Send(context.Background(), "hi")
	if err == nil || !IsFatal(err) {
		t.Fatalf("err = %v, want fatal", err)
	}
	if requests != 2 {
		t.Fatalf("%d requests, want exactly 2 (original + one retry)", requests)
	}
}

func TestKickRevokedGrantIsTerminalAndNotRetried(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, kickUnauthorized)
	}))
	defer srv.Close()
	a := kickAdapter(t, func(c *KickConfig) {
		c.APIBase = srv.URL
		c.Reauth = func(context.Context) (string, error) { return "", errors.New("invalid_grant") }
	})
	_, err := a.Send(context.Background(), "hi")
	if err == nil || !IsFatal(err) || !strings.Contains(err.Error(), "reconnect") {
		t.Fatalf("err = %v, want a fatal reconnect error", err)
	}
	if requests != 1 {
		t.Fatalf("%d requests after a refused refresh, want 1", requests)
	}
}

func TestKickTokenSourceSayingReconnectIsPassedThroughAsFatal(t *testing.T) {
	a := kickAdapter(t, func(c *KickConfig) {
		c.Token = func(context.Context) (string, error) {
			return "", Fatal(errors.New("the Kick grant was revoked; reconnect the Kick account in Settings → Platforms"))
		}
	})
	_, err := a.Send(context.Background(), "hi")
	if err == nil || !IsFatal(err) || !strings.Contains(err.Error(), "reconnect") {
		t.Fatalf("err = %v, want the fatal reconnect error", err)
	}
}

func TestKickVerifierTurnsAwayAnUnsignedRequestWithoutFetchingTheKey(t *testing.T) {
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	verify := KickVerifier(&KickKeyFetcher{HTTP: srv.Client(), URL: srv.URL})
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x"))
	if err := verify(req, []byte("x")); !errors.Is(err, ErrKickSignature) {
		t.Fatalf("err = %v, want ErrKickSignature (401), not a key-unavailable 503", err)
	}
	if fetches.Load() != 0 {
		t.Fatal("the key was fetched for a request with no signature headers")
	}
}

// A dead grant must end the chat, not be asked about every two minutes.
func TestKickRunEndsFatallyWhenTheGrantIsGone(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, kickUnauthorized)
	}))
	defer srv.Close()
	a := kickAdapter(t, func(c *KickConfig) {
		c.APIBase = srv.URL
		c.Tick = 10 * time.Millisecond
		c.Reauth = func(context.Context) (string, error) {
			return "", Fatal(errors.New("the Kick grant was revoked; reconnect the Kick account in Settings → Platforms"))
		}
	})
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), SinkFunc(func(Message) {})) }()
	select {
	case err := <-done:
		if !IsFatal(err) || !strings.Contains(err.Error(), "reconnect") {
			t.Fatalf("Run returned %v, want a fatal reconnect error", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run kept going against a revoked grant")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d API calls, want 1: the dead grant was asked about again", n)
	}
}

func TestKickRunEndsFatallyWhenARefreshedTokenIsStillRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, kickUnauthorized)
	}))
	defer srv.Close()
	a := kickAdapter(t, func(c *KickConfig) {
		c.APIBase = srv.URL
		c.Tick = 10 * time.Millisecond
		c.Reauth = func(context.Context) (string, error) { return "fresh", nil }
	})
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), SinkFunc(func(Message) {})) }()
	select {
	case err := <-done:
		if !IsFatal(err) {
			t.Fatalf("Run returned %v, want fatal", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run kept retrying a token Kick refuses even after a refresh")
	}
}

// Without a refresh path (no Reauth) a 401 stays the retried, degraded state it
// always was; only a refresh that did not help makes it terminal.
func TestKickRunKeepsRetryingA401WhenThereIsNoRefreshPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, kickUnauthorized)
	}))
	defer srv.Close()
	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL; c.Tick = 10 * time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, SinkFunc(func(Message) {})) }()
	time.Sleep(150 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("Run ended with %v", err)
	default:
	}
	cancel()
	<-done
}

func TestKickSubscriptionCheckSaysEdgeBlockNotWebhooksDisabled(t *testing.T) {
	srv := kickStatusServer(t, 403, "text/html", cloudflareBlock, map[string]string{"Cf-Ray": "x"})
	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = srv.URL })
	problem := a.syncSubscription(context.Background())
	if !strings.Contains(problem, "edge") || strings.Contains(problem, "webhook not enabled") || strings.Contains(problem, "<") {
		t.Fatalf("problem = %q, want the edge-block explanation", problem)
	}
}
