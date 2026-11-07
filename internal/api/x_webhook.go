package api

// X's webhook: the one way live chat (broadcast.chat) is delivered.
//
//	GET  /api/v1/webhooks/x?crc_token=...   X's challenge-response check
//	POST /api/v1/webhooks/x                 an event delivery
//
// docs.x.com/x-api/webhooks/quickstart. Both are keyed by the app's OAuth 2.0 client secret, which is the one saved
// for X in Settings -> Platforms. The route is mounted OUTSIDE the session middleware, because X sends no session
// and no CSRF header; the signature over the raw body is the credential for POSTs, and CRC proves control of the
// address. Repeated failures from one address are throttled like the login and setup routes.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/auth"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

const (
	// xWebhookMaxBody bounds a delivery. A chat event is a few KB; this is generous, and it is checked before any
	// HMAC is computed so a large body costs a read and nothing more.
	xWebhookMaxBody = 1 << 20
	// xCRCTokenMax bounds crc_token. See xCRCTokenOK.
	xCRCTokenMax = 256

	xSignatureHeader = "X-Twitter-Webhooks-Signature-OAuth2"
)

// xWebhookPath is the path X posts to. The registration and the route are both built from routeXWebhook.
func xWebhookPath() string { return apiBase + routeXWebhook }

// xWebhookURL is the full address handed to X, or "" when there is no origin.
func xWebhookURL(origin string) string {
	if origin == "" {
		return ""
	}
	return origin + xWebhookPath()
}

// xWebhookOrigin is the https origin X can reach this server on, built from tls.hostname, or "" with the reason the
// operator can act on. X requires HTTPS, no port, and a certificate it trusts, so:
//
//   - a hostname that is a public name (not an IP, not .local/.lan/...);
//   - polyemesis serving TLS itself, with acme or a manual certificate. A self-signed certificate is refused, since
//     X cannot validate it.
//
// A reverse proxy that terminates TLS is NOT recognised: from inside the process it cannot be told apart from a
// misconfiguration. That is a follow-up, not a guess.
func xWebhookOrigin(cfg config.Config) (origin, why string) {
	host := strings.TrimSpace(cfg.TLS.Hostname)
	switch {
	case host == "":
		return "", "tls.hostname is not set, so polyemesis has no public address to give X"
	case strings.Contains(host, ":"):
		return "", fmt.Sprintf("tls.hostname %q carries a port, and X rejects a webhook URL with a port; set the bare hostname and serve on 443", host)
	case !config.IsPublicFQDN(host):
		return "", fmt.Sprintf("tls.hostname %q is not a public domain name X can resolve", host)
	}
	switch mode := cfg.ResolvedTLSMode(); mode {
	case config.ModeACME, config.ModeManual:
		return "https://" + host, ""
	case config.ModeSelfSigned:
		return "", "tls.mode is selfsigned, a certificate X does not trust; set tls.mode acme"
	default:
		return "", fmt.Sprintf("polyemesis is not serving HTTPS itself (tls.mode %s), and X only posts to HTTPS; set tls.mode acme", mode)
	}
}

// xCRCTokenOK reports whether crc_token may be signed.
//
// The CRC response and a POST signature are the same function of the same key, so signing an attacker-chosen token
// would hand out a valid signature for that token used as a BODY. A body that is an event envelope is a JSON object,
// so a token with a "{" in it is refused, and the length is capped. The token's format is not documented, so nothing
// else is restricted.
func xCRCTokenOK(tok string) bool {
	return tok != "" && len(tok) <= xCRCTokenMax && !strings.ContainsAny(tok, "{[")
}

func xHMAC(secret string, msg []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(msg)
	return "sha256=" + base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// xClientSecret is the OAuth 2.0 client secret saved for X, or "" when there is none.
func (s *Server) xClientSecret() string {
	if s.store == nil || s.box == nil {
		return ""
	}
	c, err := s.store.GetPlatformCreds(s.box, db.PlatformX)
	if err != nil || c == nil {
		return ""
	}
	return c.ClientSecret
}

// xThrottled answers 429 when this address has been failing, and reports whether it did.
func (s *Server) xThrottled(w http.ResponseWriter, ip string) bool {
	wait := s.xhooks.Try(ip)
	if wait <= 0 {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	writeError(w, http.StatusTooManyRequests, "too many rejected requests, try again later")
	return true
}

// handleXWebhookCRC answers X's challenge. A missing, empty, or unsignable crc_token is 400; no saved client secret
// is 503, because there is nothing to answer with.
func (s *Server) handleXWebhookCRC(w http.ResponseWriter, r *http.Request) {
	ip := auth.ClientIP(r, s.proxies)
	if s.xThrottled(w, ip) {
		return
	}
	tok := r.URL.Query().Get("crc_token")
	if !xCRCTokenOK(tok) {
		s.xhooks.Fail(ip)
		writeError(w, http.StatusBadRequest, "crc_token is missing or not acceptable")
		return
	}
	secret := s.xClientSecret()
	if secret == "" {
		s.xhooks.Succeed(ip)
		writeError(w, http.StatusServiceUnavailable, "no X client secret is saved, so the check cannot be answered; add it in Settings → Platforms")
		return
	}
	s.xhooks.Succeed(ip)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"response_token": xHMAC(secret, []byte(tok))})
}

// handleXWebhookEvent receives one delivery. The order matters: size, then signature over the raw bytes, and only
// then is anything parsed. A verified body is queued without blocking and answered 200 at once, since X wants an
// answer within 10 seconds and retries a non-2xx.
func (s *Server) handleXWebhookEvent(w http.ResponseWriter, r *http.Request) {
	ip := auth.ClientIP(r, s.proxies)
	if s.xThrottled(w, ip) {
		return
	}
	secret := s.xClientSecret()
	if secret == "" {
		s.xhooks.Succeed(ip)
		// 401, not 503: with no key there is nothing a delivery could be verified against, which is the same
		// refusal as a bad signature.
		writeError(w, http.StatusUnauthorized, "signature rejected: no X client secret is saved to verify it against")
		return
	}
	// One byte past the cap tells "exactly at the cap" from "over it"; a plain LimitReader would truncate silently
	// and surface as a signature failure.
	body, err := io.ReadAll(io.LimitReader(r.Body, xWebhookMaxBody+1))
	if err != nil {
		s.xhooks.Fail(ip)
		writeError(w, http.StatusBadRequest, "could not read the request body")
		return
	}
	if len(body) > xWebhookMaxBody {
		s.xhooks.Fail(ip)
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	got := r.Header.Get(xSignatureHeader)
	if got == "" || !secretEqual(got, xHMAC(secret, body)) {
		s.xhooks.Fail(ip)
		writeError(w, http.StatusUnauthorized, "signature rejected")
		return
	}
	s.xhooks.Succeed(ip)

	if s.chat == nil {
		w.WriteHeader(http.StatusOK) // 200 so X does not retry events a chat-less server will never want
		return
	}
	user := chat.XWebhookUser(body)
	if user == "" {
		writeError(w, http.StatusBadRequest, "not an X activity event")
		return
	}
	xa := s.xAdapterFor(user)
	if xa == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	switch res := xa.IngestWebhook(body); res {
	case chat.XIngestMalformed:
		writeError(w, http.StatusBadRequest, "not an X activity event")
		return
	case chat.XIngestFull:
		s.log.Warn("x webhook: chat inbox full, event dropped", "user", user)
	}
	w.WriteHeader(http.StatusOK)
}

// xAdapterFor finds the running X adapter that receives events for an X user id.
func (s *Server) xAdapterFor(userID string) *chat.XAdapter {
	for _, st := range s.chat.Statuses() {
		if st.Platform != db.PlatformX {
			continue
		}
		a, ok := s.chat.Adapter(db.PlatformX, st.Account)
		if !ok {
			continue
		}
		if xa, ok := a.(*chat.XAdapter); ok && xa.WebhookUserID() == userID {
			return xa
		}
	}
	return nil
}
