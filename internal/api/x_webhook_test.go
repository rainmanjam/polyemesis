package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

const (
	xHookSecret = "oauth2-client-secret-for-tests"
	xHookPath   = "/api/v1/webhooks/x"
	xHookSigHdr = "X-Twitter-Webhooks-Signature-OAuth2"
	xHookUser   = "1234567890"
)

// xSign is the algorithm docs.x.com/x-api/webhooks/quickstart gives for both the CRC response token and the POST
// signature: "sha256=" + base64(HMAC-SHA256(client secret, message)). It is written out here, not borrowed from the
// code under test.
func xSign(secret string, msg []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(msg)
	return "sha256=" + base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func xHookServer(t *testing.T, withCreds bool) (*Server, http.Handler) {
	t.Helper()
	s, h, store := testServer(t, config.Config{})
	if withCreds {
		if err := store.PutPlatformCreds(s.box, db.PlatformX, "client-id", xHookSecret); err != nil {
			t.Fatalf("PutPlatformCreds: %v", err)
		}
	}
	return s, h
}

func xPost(t *testing.T, h http.Handler, body []byte, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, xHookPath, strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "203.0.113.9:5555"
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return do(t, h, r)
}

func xGet(t *testing.T, h http.Handler, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, xHookPath+"?"+rawQuery, nil)
	r.RemoteAddr = "203.0.113.9:5555"
	return do(t, h, r)
}

func xChat(uuid, user, msgID, text string) []byte {
	return []byte(fmt.Sprintf(`{"data":{"event_uuid":%q,"created_at":"2026-10-07T19:17:36.304Z","filter":{"user_id":%q},"event_type":"broadcast.chat","tag":"polyemesis","payload":{"broadcast_id":"1mGPaZaAdNqJN","message_id":%q,"message":%q,"is_subscriber":false,"is_moderator":false,"author":{"data":{"id":"777","username":"viewer","name":"Viewer"}}}}}`,
		uuid, user, msgID, text))
}

func xRemove(uuid, user, msgID string) []byte {
	return []byte(fmt.Sprintf(`{"data":{"event_uuid":%q,"filter":{"user_id":%q},"event_type":"broadcast.chat.remove","tag":"polyemesis","payload":{"broadcast_id":"1mGPaZaAdNqJN","message_id":%q}}}`,
		uuid, user, msgID))
}

// ------------------------------------------------------------------ CRC

func TestXWebhookCRCAnswersWithTheDocumentedResponseToken(t *testing.T) {
	_, h := xHookServer(t, true)
	const token = "challenge_string-8f3a"
	w := xGet(t, h, "crc_token="+token)
	if w.Code != http.StatusOK {
		t.Fatalf("CRC status %d, body %s", w.Code, w.Body.String())
	}
	var got struct {
		ResponseToken string `json:"response_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("CRC body %q is not JSON: %v", w.Body.String(), err)
	}
	if want := xSign(xHookSecret, []byte(token)); got.ResponseToken != want {
		t.Errorf("response_token = %q, want %q", got.ResponseToken, want)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestXWebhookCRCRefusesAMissingOrEmptyToken(t *testing.T) {
	_, h := xHookServer(t, true)
	for _, q := range []string{"", "crc_token=", "other=1"} {
		if w := xGet(t, h, q); w.Code != http.StatusBadRequest {
			t.Errorf("GET ?%s = %d, want 400", q, w.Code)
		}
	}
}

func TestXWebhookWithNoClientSecretCannotAnswerCRCOrVerifyPOST(t *testing.T) {
	_, h := xHookServer(t, false)
	if w := xGet(t, h, "crc_token=abc"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("CRC with no creds = %d, want 503", w.Code)
	}
	if w := xPost(t, h, xChat("u", xHookUser, "1", "x"), map[string]string{xHookSigHdr: "sha256=AAAA"}); w.Code != http.StatusUnauthorized {
		t.Errorf("POST with no creds = %d, want 401", w.Code)
	}
}

// TestXWebhookCRCIsNotASigningOracle: the CRC response and a POST signature are the same function of the same key, so
// GET ?crc_token=<body> would hand any caller a valid signature for <body>. A crc_token that could be a JSON event is
// refused, which leaves no body an attacker can get signed through this route.
func TestXWebhookCRCIsNotASigningOracle(t *testing.T) {
	s, h := xHookServer(t, true)
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	forged := xChat("forged-1", xHookUser, "2090000000000000099", "pay me")

	w := xGet(t, h, "crc_token="+url.QueryEscape(string(forged)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a JSON-shaped crc_token was signed: status %d body %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "sha256=") {
		t.Errorf("the refusal carries a signature: %s", w.Body.String())
	}
	if w := xGet(t, h, "crc_token="+strings.Repeat("a", 257)); w.Code != http.StatusBadRequest {
		t.Errorf("a 257-character crc_token = %d, want 400", w.Code)
	}
	if w := xGet(t, h, "crc_token="+strings.Repeat("a", 256)); w.Code != http.StatusOK {
		t.Errorf("a 256-character crc_token = %d, want 200", w.Code)
	}
	// Without the signature the oracle would have supplied, the forged event is refused.
	if w := xPost(t, h, forged, map[string]string{xHookSigHdr: xSign("a-guess", forged)}); w.Code != http.StatusUnauthorized {
		t.Errorf("a forged POST = %d, want 401", w.Code)
	}
}

// ------------------------------------------------------------------ POST signature

func TestXWebhookPOSTSignature(t *testing.T) {
	body := xChat("u-sig", xHookUser, "2090000000000000001", "hello")
	tampered := append([]byte(nil), body...)
	tampered[len(tampered)-5] ^= 1

	for _, tc := range []struct {
		name string
		body []byte
		hdr  map[string]string
		want int
	}{
		{"valid", body, map[string]string{xHookSigHdr: xSign(xHookSecret, body)}, http.StatusOK},
		{"no signature header", body, nil, http.StatusUnauthorized},
		{"empty signature header", body, map[string]string{xHookSigHdr: ""}, http.StatusUnauthorized},
		{"wrong key", body, map[string]string{xHookSigHdr: xSign("not-the-secret", body)}, http.StatusUnauthorized},
		{"signed body A, sent body B", tampered, map[string]string{xHookSigHdr: xSign(xHookSecret, body)}, http.StatusUnauthorized},
		{"missing sha256= prefix", body, map[string]string{xHookSigHdr: strings.TrimPrefix(xSign(xHookSecret, body), "sha256=")}, http.StatusUnauthorized},
		{"only the legacy OAuth1 header", body, map[string]string{"X-Twitter-Webhooks-Signature": xSign(xHookSecret, body)}, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, h := xHookServer(t, true)
			if w := xPost(t, h, tc.body, tc.hdr); w.Code != tc.want {
				t.Errorf("status %d, want %d (body %s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestXWebhookRefusesAnOversizedBodyBeforeCheckingIt(t *testing.T) {
	_, h := xHookServer(t, true)
	big := []byte(`{"data":{"pad":"` + strings.Repeat("a", xWebhookMaxBody) + `"}}`)
	// Signed WRONG on purpose: a 413 (and not a 401) proves the size is checked before the signature is.
	if w := xPost(t, h, big, map[string]string{xHookSigHdr: "sha256=wrong"}); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body with a wrong signature = %d, want 413", w.Code)
	}
	if w := xPost(t, h, big, map[string]string{xHookSigHdr: xSign(xHookSecret, big)}); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body with a right signature = %d, want 413", w.Code)
	}
}

func TestXWebhookTakesNoSessionAndNoCSRF(t *testing.T) {
	_, h := xHookServer(t, true)
	body := xChat("u-anon", xHookUser, "1", "x")
	r := httptest.NewRequest(http.MethodPost, xHookPath, strings.NewReader(string(body)))
	r.Header.Set(xHookSigHdr, xSign(xHookSecret, body))
	r.RemoteAddr = "198.51.100.77:1"
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Errorf("an anonymous signed POST = %d, want 200 (X sends no session and no CSRF header)", w.Code)
	}
}

// TestXWebhookAnAttackerCannotLockXOut: the review's DoS. One address alternates bad POSTs with CRC GETs. Nothing
// the attacker does may make a validly signed delivery or a CRC answer fail, from its own address or any other,
// because the limit is per address, applies to FAILED signatures only, and is decided after the HMAC.
func TestXWebhookAnAttackerCannotLockXOut(t *testing.T) {
	s, h := xHookServer(t, true)
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	liveXAdapter(t, s, xHookUser)

	at := func(ip string, r *http.Request) *httptest.ResponseRecorder {
		r.RemoteAddr = ip + ":4000"
		return do(t, h, r)
	}
	bad := xChat("atk", xHookUser, "1", "x")
	var limited bool
	for i := 0; i < 60; i++ {
		r := httptest.NewRequest(http.MethodPost, xHookPath, strings.NewReader(string(bad)))
		r.Header.Set(xHookSigHdr, "sha256=bad")
		if at("203.0.113.66", r).Code == http.StatusTooManyRequests {
			limited = true
		}
		g := httptest.NewRequest(http.MethodGet, xHookPath+"?crc_token=abc", nil)
		if w := at("203.0.113.66", g); w.Code != http.StatusOK {
			t.Fatalf("CRC GET from the attacker's address = %d at iteration %d; a CRC is never throttled", w.Code, i)
		}
	}
	if !limited {
		t.Error("60 bad signatures from one address were never limited")
	}

	// X, from another address: delivered.
	good := xChat("real-1", xHookUser, "2090000000000000040", "from X")
	r := httptest.NewRequest(http.MethodPost, xHookPath, strings.NewReader(string(good)))
	r.Header.Set(xHookSigHdr, xSign(xHookSecret, good))
	if w := at("198.51.100.20", r); w.Code != http.StatusOK {
		t.Fatalf("a valid delivery from another address = %d, want 200", w.Code)
	}
	xWait(t, "the delivery", func() bool { return len(xHistory(s)) == 1 })

	// And from the attacker's own address, a valid signature is still accepted.
	good2 := xChat("real-2", xHookUser, "2090000000000000041", "valid, same address")
	r = httptest.NewRequest(http.MethodPost, xHookPath, strings.NewReader(string(good2)))
	r.Header.Set(xHookSigHdr, xSign(xHookSecret, good2))
	if w := at("203.0.113.66", r); w.Code != http.StatusOK {
		t.Errorf("a valid delivery from the limited address = %d, want 200", w.Code)
	}
	xWait(t, "the second delivery", func() bool { return len(xHistory(s)) == 2 })
}

// TestXWebhookACRCSuccessDoesNotClearPOSTFailures: the two are separate; answering a CRC must not forgive an
// address its bad signatures.
func TestXWebhookACRCSuccessDoesNotClearPOSTFailures(t *testing.T) {
	_, h := xHookServer(t, true)
	body := xChat("u", xHookUser, "1", "x")
	post := func() int {
		r := httptest.NewRequest(http.MethodPost, xHookPath, strings.NewReader(string(body)))
		r.Header.Set(xHookSigHdr, "sha256=bad")
		r.RemoteAddr = "203.0.113.70:1"
		return do(t, h, r).Code
	}
	for i := 0; i < xFailBurst+1; i++ {
		post()
	}
	if got := post(); got != http.StatusTooManyRequests {
		t.Fatalf("setup: address not limited, got %d", got)
	}
	g := httptest.NewRequest(http.MethodGet, xHookPath+"?crc_token=abc", nil)
	g.RemoteAddr = "203.0.113.70:1"
	if w := do(t, h, g); w.Code != http.StatusOK {
		t.Fatalf("CRC = %d", w.Code)
	}
	if got := post(); got != http.StatusTooManyRequests {
		t.Errorf("after a CRC success the address's POST failures were forgiven: %d", got)
	}
}

// hangingX is a fake X whose every call blocks until the test ends, so an adapter attached to it stays "connecting".
func hangingX(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release); srv.Close() })
	return srv
}

// TestXWebhookAnswers503OnlyWhileChatIsPlausiblyComingUp: a transient not-ready adapter is 503 so X redelivers, but
// the window is bounded, after which the answer is 200 and the event is dropped with a log line.
func TestXWebhookAnswers503OnlyWhileChatIsPlausiblyComingUp(t *testing.T) {
	s, h := xHookServer(t, true)
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	x := hangingX(t)
	a, err := chat.NewX(chat.XConfig{AccountRef: xHookUser, Token: chat.StaticToken("t"), AppBearer: "b",
		APIBase: x.URL, WebhookURL: "https://polyemesis.example.com" + xHookPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.chat.Attach(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	w := signedPost(t, h, xChat("u-nl", xHookUser, "2090000000000000050", "early"))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Errorf("event for an adapter still connecting = %d (Retry-After %q), want 503 with Retry-After", w.Code, w.Header().Get("Retry-After"))
	}
	old := xNotLiveGrace
	xNotLiveGrace = 20 * time.Millisecond
	t.Cleanup(func() { xNotLiveGrace = old })
	time.Sleep(60 * time.Millisecond)
	if w := signedPost(t, h, xChat("u-nl2", xHookUser, "2090000000000000051", "late")); w.Code != http.StatusOK {
		t.Errorf("event after the grace window = %d, want 200 (X must stop retrying for chat that is not coming)", w.Code)
	}
}

// TestXWebhookAnswers200ForAPermanentlyFailedAdapter: a Fatal Run (no public address, no bearer) will not come up
// by itself, so X is told 200 and stops retrying.
func TestXWebhookAnswers200ForAPermanentlyFailedAdapter(t *testing.T) {
	s, h := xHookServer(t, true)
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	a, err := chat.NewX(chat.XConfig{AccountRef: xHookUser, Token: chat.StaticToken("t"), AppBearer: "b", WebhookProblem: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.chat.Attach(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	xWait(t, "the adapter to fail", func() bool { return a.Unavailable(time.Hour) })
	if w := signedPost(t, h, xChat("u-f", xHookUser, "2090000000000000052", "nobody home")); w.Code != http.StatusOK {
		t.Errorf("event for a permanently failed adapter = %d, want 200", w.Code)
	}
}

func TestXChatAdapterIsBuiltWithTheServerLogger(t *testing.T) {
	s, _, _ := testServerWith(t, Options{Providers: oauth.NewSet().With(db.PlatformX, oauth.NewX().WithLivestreamAccess())})
	s.env.XAppBearer = "app-bearer"
	adapter, err := s.chatAdapter(context.Background(), db.PlatformAccount{ID: 1, Platform: db.PlatformX, AccountRef: xHookUser, AccountName: "x"})
	if err != nil {
		t.Fatalf("chatAdapter: %v", err)
	}
	xa, ok := adapter.(*chat.XAdapter)
	if !ok {
		t.Fatalf("chatAdapter returned %T", adapter)
	}
	if xa.Logger() == nil || xa.Logger() != s.log {
		t.Error("the X adapter was built without the server's logger, so its warnings are discarded")
	}
}

// xSubsFake is a fake X that lists one valid webhook and the given subscriptions, and records the subscription
// DELETEs (path -> Authorization).
type xSubsFake struct {
	mu      sync.Mutex
	deletes map[string]string
	srv     *httptest.Server
}

func newXSubsFake(t *testing.T, user string) *xSubsFake {
	t.Helper()
	f := &xSubsFake{deletes: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/2/users/me":
			_, _ = io.WriteString(w, `{"data":{"id":"`+user+`"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/2/webhooks":
			_, _ = io.WriteString(w, `{"data":[{"id":"9","url":"https://polyemesis.example.com`+xHookPath+`","valid":true}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/2/activity/subscriptions":
			sub := func(id, ev string) string {
				return `{"subscription_id":"` + id + `","event_type":"` + ev + `","filter":{"user_id":"` + user + `"},"webhook_id":"9","tag":"polyemesis"}`
			}
			_, _ = io.WriteString(w, `{"data":[`+sub("71", "broadcast.chat")+`,`+sub("72", "broadcast.chat.remove")+`]}`)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/2/activity/subscriptions/"):
			f.deletes[r.URL.Path] = r.Header.Get("Authorization")
			_, _ = io.WriteString(w, `{"data":{"deleted":true}}`)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *xSubsFake) deleted() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for k, v := range f.deletes {
		out[k] = v
	}
	return out
}

// TestDisconnectingAnXAccountRemovesItsChatSubscriptions: a Run's exit leaves the subscriptions in place, so the
// explicit disconnect is what removes them from X, with the account's own token read before the row is deleted.
func TestDisconnectingAnXAccountRemovesItsChatSubscriptions(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	sign := login(t, h)
	id := connectAccount(t, store, s.box, db.PlatformX, "xuser")
	ref := "xuser-ref"
	fx := newXSubsFake(t, ref)
	a, err := chat.NewX(chat.XConfig{AccountRef: ref, Channel: "xuser", Token: s.chatToken(id), AppBearer: "app-bearer",
		APIBase: fx.srv.URL, WebhookURL: "https://polyemesis.example.com" + xHookPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.chat.Attach(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	xWait(t, "the adapter to be live", func() bool { return a.WebhookUserID() != "" })
	if n := len(fx.deleted()); n != 0 {
		t.Fatalf("setup: %d deletes before the disconnect", n)
	}
	if w := deleteAccount(t, h, sign, id, nil); w.Code != http.StatusOK {
		t.Fatalf("disconnect: %d %s", w.Code, w.Body.String())
	}
	got := fx.deleted()
	if len(got) != 2 || got["/2/activity/subscriptions/71"] != "Bearer at" || got["/2/activity/subscriptions/72"] != "Bearer at" {
		t.Errorf("subscription deletes after disconnect = %v, want both, with the account's token", got)
	}
}

func TestXWebhookWrongMethodIsRefused(t *testing.T) {
	_, h := xHookServer(t, true)
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		r := httptest.NewRequest(m, xHookPath, nil)
		if w := do(t, h, r); w.Code != http.StatusMethodNotAllowed && w.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 405 or 404", m, w.Code)
		}
	}
}

// ------------------------------------------------------------------ routing to the adapter

// liveXAdapter attaches a real X adapter, registered against a minimal fake X, and waits until it is live.
func liveXAdapter(t *testing.T, s *Server, account string) {
	t.Helper()
	var mu sync.Mutex
	var hooks []map[string]any
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/2/users/me":
			_, _ = io.WriteString(w, `{"data":{"id":"`+account+`"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/2/webhooks":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": hooks})
		case r.Method == http.MethodPost && r.URL.Path == "/2/webhooks":
			hooks = append(hooks, map[string]any{"id": "9", "url": "https://polyemesis.example.com" + xHookPath, "valid": true})
			_ = json.NewEncoder(w).Encode(map[string]any{"data": hooks[0]})
		case r.Method == http.MethodGet && r.URL.Path == "/2/activity/subscriptions":
			_, _ = io.WriteString(w, `{"data":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/2/activity/subscriptions":
			_, _ = io.WriteString(w, `{"data":{"subscription":{"subscription_id":"`+fmt.Sprint(time.Now().UnixNano())+`"}}}`)
		case r.Method == http.MethodDelete:
			_, _ = io.WriteString(w, `{"data":{"deleted":true}}`)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
		}
	}))
	t.Cleanup(fake.Close)
	a, err := chat.NewX(chat.XConfig{
		AccountRef: account, Channel: "sundaypractice", Token: chat.StaticToken("user-token"), AppBearer: "app-bearer",
		APIBase: fake.URL, WebhookURL: "https://polyemesis.example.com" + xHookPath,
	})
	if err != nil {
		t.Fatalf("NewX: %v", err)
	}
	if err := s.chat.Attach(context.Background(), a); err != nil {
		t.Fatalf("attach X: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for a.WebhookUserID() == "" {
		if time.Now().After(deadline) {
			t.Fatal("the X adapter never became live")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func xHistory(s *Server) []chat.Message {
	var out []chat.Message
	for _, m := range s.chat.History(0) {
		if m.Platform == db.PlatformX {
			out = append(out, m)
		}
	}
	return out
}

func xWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func signedPost(t *testing.T, h http.Handler, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	return xPost(t, h, body, map[string]string{xHookSigHdr: xSign(xHookSecret, body)})
}

func TestXWebhookDeliversAVerifiedChatEventToTheAccountsAdapter(t *testing.T) {
	s, h := xHookServer(t, true)
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	liveXAdapter(t, s, xHookUser)

	if w := signedPost(t, h, xChat("u-1", xHookUser, "2090000000000000004", "hi from the webhook")); w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}
	xWait(t, "the message in the hub", func() bool { return len(xHistory(s)) == 1 })
	m := xHistory(s)[0]
	if m.Text != "hi from the webhook" || m.ID != "2090000000000000004" || m.Author.Name != "Viewer" || m.Account != xHookUser {
		t.Errorf("message = %+v", m)
	}

	// X retries and replays: the same event_uuid is answered 200 and shown once.
	for i := 0; i < 3; i++ {
		if w := signedPost(t, h, xChat("u-1", xHookUser, "2090000000000000004", "hi from the webhook")); w.Code != http.StatusOK {
			t.Fatalf("replay status %d", w.Code)
		}
	}
	time.Sleep(40 * time.Millisecond)
	if n := len(xHistory(s)); n != 1 {
		t.Errorf("the hub holds %d copies after replays, want 1", n)
	}
}

func TestXWebhookRemoveEventRemovesTheMessageFromTheHub(t *testing.T) {
	s, h := xHookServer(t, true)
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	liveXAdapter(t, s, xHookUser)

	signedPost(t, h, xChat("u-a", xHookUser, "2090000000000000004", "to be removed"))
	xWait(t, "the message", func() bool { return len(xHistory(s)) == 1 })
	if w := signedPost(t, h, xRemove("u-b", xHookUser, "2090000000000000004")); w.Code != http.StatusOK {
		t.Fatalf("remove status %d", w.Code)
	}
	xWait(t, "the retraction", func() bool { return len(xHistory(s)) == 0 })
}

func TestXWebhookNeverDeliversAnotherUsersEventToThisAdapter(t *testing.T) {
	s, h := xHookServer(t, true)
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	liveXAdapter(t, s, xHookUser)

	if w := signedPost(t, h, xChat("u-other", "555", "2090000000000000010", "someone else's chat")); w.Code != http.StatusOK {
		t.Fatalf("status %d: an event nobody here wants is still answered 200 so X stops retrying", w.Code)
	}
	time.Sleep(40 * time.Millisecond)
	if n := len(xHistory(s)); n != 0 {
		t.Errorf("another account's chat reached the hub: %+v", xHistory(s))
	}
}

func TestXWebhookIs200WhenChatIsOffOrNothingIsAttached(t *testing.T) {
	s, h := xHookServer(t, true)
	body := xChat("u-off", xHookUser, "1", "x")
	if w := signedPost(t, h, body); w.Code != http.StatusOK { // s.chat is nil
		t.Errorf("chat off = %d, want 200", w.Code)
	}
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	if w := signedPost(t, h, body); w.Code != http.StatusOK {
		t.Errorf("no X adapter attached = %d, want 200", w.Code)
	}
}

func TestXWebhookAnswersAMalformedSignedBodyWith400(t *testing.T) {
	s, h := xHookServer(t, true)
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	liveXAdapter(t, s, xHookUser)
	if w := signedPost(t, h, []byte(`not json`)); w.Code != http.StatusBadRequest {
		t.Errorf("malformed signed body = %d, want 400", w.Code)
	}
}

// ------------------------------------------------------------------ the public address

func TestXWebhookOrigin(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cfg        config.Config
		wantOrigin string
		wantWhy    string
	}{
		{"acme with a public name", config.Config{TLS: config.TLS{Mode: config.ModeACME, Hostname: "stream.example.com"}}, "https://stream.example.com", ""},
		{"the staging config", config.Config{TLS: config.TLS{Mode: config.ModeACME, Hostname: "vps-607ef6d4.vps.ovh.us"}}, "https://vps-607ef6d4.vps.ovh.us", ""},
		{"manual cert with a public name", config.Config{TLS: config.TLS{Mode: config.ModeManual, Hostname: "stream.example.com"}}, "https://stream.example.com", ""},
		{"self-signed is a certificate X does not trust", config.Config{TLS: config.TLS{Mode: config.ModeSelfSigned, Hostname: "stream.example.com"}}, "", "selfsigned"},
		{"no hostname", config.Config{TLS: config.TLS{Mode: config.ModeACME}}, "", "tls.hostname is not set"},
		{"a LAN name", config.Config{TLS: config.TLS{Mode: config.ModeACME, Hostname: "polyemesis.local"}}, "", "public"},
		{"an IP address", config.Config{TLS: config.TLS{Mode: config.ModeACME, Hostname: "15.204.94.31"}}, "", "public"},
		{"plain HTTP", config.Config{TLS: config.TLS{Mode: config.ModeOff, Hostname: "stream.example.com"}}, "", "HTTPS"},
		{"a hostname carrying a port", config.Config{TLS: config.TLS{Mode: config.ModeACME, Hostname: "stream.example.com:8443"}}, "", "port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin, why := xWebhookOrigin(tc.cfg)
			if origin != tc.wantOrigin {
				t.Errorf("origin = %q, want %q", origin, tc.wantOrigin)
			}
			if tc.wantWhy == "" && why != "" || tc.wantWhy != "" && !strings.Contains(why, tc.wantWhy) {
				t.Errorf("why = %q, want it to contain %q", why, tc.wantWhy)
			}
			if origin == "" && why == "" {
				t.Error("no origin and no reason: the operator would be told nothing")
			}
		})
	}
}

func TestXWebhookURLIsTheOriginPlusTheRoute(t *testing.T) {
	if got := xWebhookURL("https://stream.example.com"); got != "https://stream.example.com/api/v1/webhooks/x" {
		t.Errorf("xWebhookURL = %q", got)
	}
	if got := xWebhookURL(""); got != "" {
		t.Errorf("xWebhookURL of nothing = %q", got)
	}
}

func TestXIngestStatus(t *testing.T) {
	for res, want := range map[chat.XIngest]int{
		chat.XIngestQueued: 200, chat.XIngestDuplicate: 200, chat.XIngestIgnored: 200, chat.XIngestWrongAccount: 200,
		chat.XIngestNotRunning: 503, chat.XIngestFull: 503, chat.XIngestMalformed: 400,
	} {
		if got := xIngestStatus(res); got != want {
			t.Errorf("xIngestStatus(%v) = %d, want %d", res, got, want)
		}
	}
}
