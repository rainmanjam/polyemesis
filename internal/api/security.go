package api

import (
	"net/http"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/config"
)

// cspDirectives is the Content-Security-Policy, one directive per entry so the
// reason for each relaxation stays next to it. Every relaxation below is
// load-bearing for a feature that fails silently — usually as a blank page —
// when the directive is tightened.
var cspDirectives = []string{
	"default-src 'self'",
	// Note what is absent: 'unsafe-inline' for script-src. The UI is a Vite
	// bundle of hashed module files with no inline <script>, so nothing needs
	// it, and it is the one relaxation that would turn an injected string into
	// executable code.
	"img-src 'self' data:",
	// hls.js hands <video> its MediaSource as a blob: URL.
	"media-src 'self' blob:",
	// The telemetry WebSocket. ws: as well as wss: because a LAN box may
	// legitimately be serving plain HTTP.
	"connect-src 'self' ws: wss:",
	// hls.js compiles its demuxer worker from a blob: of generated source.
	"worker-src 'self' blob:",
	// Tailwind ships utility classes but the shell still carries a style
	// attribute, and the bundle injects <style> at runtime.
	"style-src 'self' 'unsafe-inline'",
	"frame-ancestors 'none'",
	"base-uri 'self'",
	"form-action 'self'",
}

var contentSecurityPolicy = strings.Join(cspDirectives, "; ")

// permissionsPolicy switches off the capabilities this product never asks for.
// Nothing in the UI captures media locally; the video it plays comes from the
// server.
const permissionsPolicy = "camera=(), microphone=(), geolocation=()"

// hstsMaxAge is one day rather than the fashionable two years. HSTS is
// browser-persistent and there is no server-side undo, so the blast radius of a
// misconfigured box has to stay small: a day is long enough to matter and short
// enough that an operator who turned it on by mistake is not stuck with it.
// No includeSubDomains, no preload — both widen that radius past this host.
const hstsMaxAge = "max-age=86400"

// securityHeaders sets the headers every response shares.
//
// mode is the resolved TLS mode (never config.ModeAuto) and hsts is tls.hsts.
// Both are parameters rather than reads off s.cfg so the HSTS decision — the
// one with no undo — is stated at the call site and can be tested without
// building a server.
func securityHeaders(mode config.Mode, hsts bool) func(http.Handler) http.Handler {
	// Decided once, at construction. Sending HSTS from a box on a self-signed
	// certificate teaches the browser to refuse plain HTTP to that host and then
	// to refuse the untrusted certificate too, which leaves the operator locked
	// out of their own LAN box with no server-side way to clear it. Only a
	// certificate a browser will actually validate earns the header.
	allowHSTS := hsts && (mode == config.ModeACME || mode == config.ModeManual)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", contentSecurityPolicy)
			h.Set("X-Frame-Options", "DENY")
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Permissions-Policy", permissionsPolicy)

			// r.TLS is the only proof of a genuinely encrypted hop. A forwarded
			// header is not consulted even when the proxy is trusted: in that
			// deployment the resolved mode is off, allowHSTS is already false,
			// and the policy for the connection the browser actually made
			// belongs to whoever terminated it.
			if allowHSTS && r.TLS != nil {
				h.Set("Strict-Transport-Security", hstsMaxAge)
			}

			next.ServeHTTP(w, r)
		})
	}
}
