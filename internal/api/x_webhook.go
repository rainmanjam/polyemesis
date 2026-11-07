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
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

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

// xAdapterFor finds the X adapter for an X user id, or nil when this install has none. It matches the account the
// adapter is keyed on (the X user id), live or not, so an event for a known account whose adapter is still starting
// is told apart from one for an account this install does not have.
func (s *Server) xAdapterFor(userID string) *chat.XAdapter {
	for _, st := range s.chat.Statuses() {
		if st.Platform != db.PlatformX {
			continue
		}
		a, ok := s.chat.Adapter(db.PlatformX, st.Account)
		if !ok {
			continue
		}
		if xa, ok := a.(*chat.XAdapter); ok && xa.Account() == userID {
			return xa
		}
	}
	return nil
}

// xChatForDisconnect finds the X adapter of an account that is about to be disconnected, and reads the account's
// token while the row still exists: the subscriptions are removed with it after the row is gone.
func (s *Server) xChatForDisconnect(ctx context.Context, id int64, p db.Platform, ref string) (*chat.XAdapter, string) {
	if s.chat == nil || p != db.PlatformX {
		return nil, ""
	}
	a, ok := s.chat.Adapter(db.PlatformX, ref)
	if !ok {
		return nil, ""
	}
	xa, ok := a.(*chat.XAdapter)
	if !ok {
		return nil, ""
	}
	tok, err := s.chatToken(id)(ctx)
	if err != nil {
		return xa, ""
	}
	return xa, tok
}

// removeXSubscriptions removes a disconnected account's chat subscriptions from X. A Run's exit never does this
// (subscriptions persist and the next Run reuses them), so the explicit disconnect is the one place they go. Skipped
// when the account was connected again in the meantime: its new adapter is using them.
func (s *Server) removeXSubscriptions(ctx context.Context, xa *chat.XAdapter, tok string, p db.Platform, ref string) {
	if xa == nil || tok == "" {
		return
	}
	if _, found, err := s.accountByKey(p, ref); err != nil || found {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := xa.RemoveSubscriptions(ctx, tok); err != nil {
		s.log.Warn("x chat: could not remove the disconnected account's subscriptions from X; they stay until removed in console.x.com", "account", ref, "err", err)
	}
}
