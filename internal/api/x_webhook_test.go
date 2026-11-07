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
	// Signed correctly: the refusal is about size, not signature.
	if w := xPost(t, h, big, map[string]string{xHookSigHdr: xSign(xHookSecret, big)}); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body = %d, want 413", w.Code)
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

func TestXWebhookRateLimitsRepeatedBadSignatures(t *testing.T) {
	_, h := xHookServer(t, true)
	body := xChat("u", xHookUser, "1", "x")
	var last int
	for i := 0; i < 12; i++ {
		last = xPost(t, h, body, map[string]string{xHookSigHdr: "sha256=bad"}).Code
		if last == http.StatusTooManyRequests {
			break
		}
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("12 bad signatures from one address never produced a 429; last status %d", last)
	}
	// A different address is not affected.
	r := httptest.NewRequest(http.MethodPost, xHookPath, strings.NewReader(string(body)))
	r.Header.Set(xHookSigHdr, xSign(xHookSecret, body))
	r.RemoteAddr = "198.51.100.50:9"
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Errorf("a good delivery from another address = %d, want 200", w.Code)
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
