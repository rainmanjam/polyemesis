package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
)

// headerProbe runs one request through the middleware alone, which is enough
// for every question about which headers come back and lets the TLS mode vary
// without standing up a server per case.
func headerProbe(t *testing.T, mode config.Mode, hsts bool, target string) http.Header {
	t.Helper()
	h := securityHeaders(mode, hsts)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	w := httptest.NewRecorder()
	// httptest.NewRequest populates r.TLS for an https target, which is exactly
	// the signal the middleware keys HSTS off.
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w.Result().Header
}

func TestEverySecurityHeaderIsSetOnEveryResponse(t *testing.T) {
	_, handler, _ := testServer(t, config.Config{})

	// Routes with deliberately different shapes: a public JSON 200, an
	// unauthorised JSON 401, and the embedded SPA. A header that only rides on
	// writeJSON would protect nothing on the page that renders.
	routes := []struct {
		name string
		path string
	}{
		{"public json route", "/api/v1/health"},
		{"unauthorised json route", "/api/v1/system"},
		{"embedded single-page UI", "/"},
	}
	headers := []struct {
		name string
		want string
	}{
		{"X-Frame-Options", "DENY"},
		{"X-Content-Type-Options", "nosniff"},
		{"Referrer-Policy", "no-referrer"},
		{"Permissions-Policy", "camera=(), microphone=(), geolocation=()"},
		{"Content-Security-Policy", contentSecurityPolicy},
	}

	for _, rt := range routes {
		for _, hdr := range headers {
			t.Run(rt.name+"/"+hdr.name, func(t *testing.T) {
				w := do(t, handler, httptest.NewRequest(http.MethodGet, rt.path, nil))
				if got := w.Header().Get(hdr.name); got != hdr.want {
					t.Errorf("%s = %q, want %q", hdr.name, got, hdr.want)
				}
			})
		}
	}
}

func TestHSTSIsSentOnlyOverRealHTTPSWithACertificateBrowsersTrust(t *testing.T) {
	tests := []struct {
		name   string
		mode   config.Mode
		hsts   bool
		target string
		want   bool
	}{
		{"acme over https with hsts on", config.ModeACME, true, "https://box.example.com/", true},
		{"manual cert over https with hsts on", config.ModeManual, true, "https://box.example.com/", true},

		// The footgun: a browser that pins a host it cannot validate is a
		// browser that can no longer reach it, and there is no server-side undo.
		{"selfsigned over https with hsts on", config.ModeSelfSigned, true, "https://box.local/", false},

		// A plaintext hop proves nothing about the next one, and the header
		// would still be believed.
		{"acme over plain http", config.ModeACME, true, "http://box.example.com/", false},
		{"manual cert over plain http", config.ModeManual, true, "http://box.example.com/", false},
		{"selfsigned over plain http", config.ModeSelfSigned, true, "http://box.local/", false},

		// Opt-in means opt-in.
		{"acme over https with hsts off", config.ModeACME, false, "https://box.example.com/", false},
		{"manual cert over https with hsts off", config.ModeManual, false, "https://box.example.com/", false},

		// mode off is either plaintext or a proxy terminating TLS; either way
		// the policy for the browser's connection is not ours to declare.
		{"tls off with hsts on", config.ModeOff, true, "https://box.example.com/", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := headerProbe(t, tc.mode, tc.hsts, tc.target).Get("Strict-Transport-Security")
			if tc.want {
				if got != hstsMaxAge {
					t.Fatalf("Strict-Transport-Security = %q, want %q", got, hstsMaxAge)
				}
				return
			}
			if got != "" {
				t.Fatalf("Strict-Transport-Security = %q, want it absent", got)
			}
		})
	}
}

func TestHSTSStaysNarrowSoAMistakeCannotSpreadOrPersist(t *testing.T) {
	got := headerProbe(t, config.ModeACME, true, "https://box.example.com/").Get("Strict-Transport-Security")

	for _, banned := range []string{"includeSubDomains", "preload"} {
		if strings.Contains(strings.ToLower(got), strings.ToLower(banned)) {
			t.Errorf("Strict-Transport-Security = %q, must not carry %s: it widens the blast radius past this host", got, banned)
		}
	}
	// A year-long pin is not something to hand out by default from a homelab.
	if got != "max-age=86400" {
		t.Errorf("Strict-Transport-Security = %q, want a modest max-age", got)
	}
}

func TestCSPPermitsWhatTheUIActuallyDoesAndNothingMore(t *testing.T) {
	directive := func(t *testing.T, name string) string {
		t.Helper()
		for _, d := range cspDirectives {
			if strings.HasPrefix(d, name+" ") {
				return d
			}
		}
		t.Fatalf("CSP has no %s directive: %q", name, contentSecurityPolicy)
		return ""
	}

	tests := []struct {
		name      string
		directive string
		want      string
		why       string
	}{
		{"hls.js attaches its MediaSource as a blob url", "media-src", "blob:", "video element is handed a blob: MediaSource"},
		{"hls.js compiles its demuxer worker from a blob url", "worker-src", "blob:", "worker source is generated at runtime"},
		{"the telemetry socket is same-origin over either scheme", "connect-src", "ws:", "the WebSocket may be plain on a LAN box"},
		{"inline svg and favicons arrive as data urls", "img-src", "data:", "icons are inlined by the bundler"},
		{"tailwind and the shell need inline styles", "style-src", "'unsafe-inline'", "the shell carries a style attribute"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if d := directive(t, tc.directive); !strings.Contains(d, tc.want) {
				t.Errorf("%q lacks %s (%s)", d, tc.want, tc.why)
			}
		})
	}

	// The relaxation that must never appear. style-src legitimately carries
	// 'unsafe-inline'; script-src must not inherit it, and default-src must not
	// hand it to scripts by omission.
	for _, d := range cspDirectives {
		if strings.HasPrefix(d, "style-src ") {
			continue
		}
		if strings.Contains(d, "'unsafe-inline'") || strings.Contains(d, "'unsafe-eval'") {
			t.Errorf("%q relaxes script execution; only style-src may carry 'unsafe-inline'", d)
		}
	}

	for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "base-uri 'self'", "form-action 'self'"} {
		if !strings.Contains(contentSecurityPolicy, want) {
			t.Errorf("CSP is missing %q: %s", want, contentSecurityPolicy)
		}
	}
}
