package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
)

func TestRedirectWarnings(t *testing.T) {
	tests := []struct {
		name        string
		cfg         config.Config
		host        string
		forwarded   string
		redirectURI string
		wantSubstr  string // "" means: expect NO warnings
	}{
		{
			name:        "https on a real hostname is fine",
			cfg:         config.Config{TLS: config.TLS{Hostname: "stream.example.com"}},
			host:        "stream.example.com",
			redirectURI: "https://stream.example.com/api/v1/oauth/youtube/callback",
		},
		{
			name:        "loopback over http is fine",
			host:        "localhost:8080",
			redirectURI: "http://localhost:8080/api/v1/oauth/youtube/callback",
		},
		{
			name:        "127.0.0.1 over http is fine too",
			host:        "127.0.0.1:8080",
			redirectURI: "http://127.0.0.1:8080/api/v1/oauth/youtube/callback",
		},
		{
			name:        "plain http on a routable host",
			host:        "box.lan",
			redirectURI: "http://box.lan/api/v1/oauth/youtube/callback",
			wantSubstr:  "HTTPS",
		},
		{
			name:        "bare IP address",
			host:        "192.168.1.50:8080",
			redirectURI: "http://192.168.1.50:8080/api/v1/oauth/youtube/callback",
			wantSubstr:  "IP address",
		},
		{
			name:        "browsed host disagrees with configured hostname",
			cfg:         config.Config{TLS: config.TLS{Hostname: "stream.example.com"}},
			host:        "192.168.1.50:8080",
			redirectURI: "http://192.168.1.50:8080/api/v1/oauth/youtube/callback",
			wantSubstr:  "stream.example.com",
		},
		{
			name:        "proxied but proxy headers not trusted",
			host:        "internal:8080",
			forwarded:   "stream.example.com",
			redirectURI: "http://internal:8080/api/v1/oauth/youtube/callback",
			wantSubstr:  "reverse proxy",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/platforms/guides", nil)
			req.Host = tc.host
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-Host", tc.forwarded)
			}

			got := redirectWarnings(tc.cfg, req, tc.redirectURI)

			if tc.wantSubstr == "" {
				if len(got) != 0 {
					t.Fatalf("warned about a usable redirect URI: %v", got)
				}
				return
			}
			joined := strings.Join(got, " | ")
			if !strings.Contains(joined, tc.wantSubstr) {
				t.Fatalf("warnings = %q, want one containing %q", joined, tc.wantSubstr)
			}
			// Every warning must name a URI the operator can act on. One that
			// says only "this may be wrong" relocates the problem.
			for _, w := range got {
				if !strings.Contains(w, tc.redirectURI) && !strings.Contains(w, "stream.example.com") {
					t.Errorf("warning does not name a URI to register: %q", w)
				}
			}
		})
	}
}

// A proxy that IS trusted must not be warned about: trustProxyHeaders on means
// the origin was reconstructed from the forwarded headers, so the URI shown is
// the one the browser actually used.
func TestNoProxyWarningWhenHeadersAreTrusted(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/platforms/guides", nil)
	req.Host = "internal:8080"
	req.Header.Set("X-Forwarded-Host", "stream.example.com")

	cfg := config.Config{TrustProxyHeaders: true}
	got := redirectWarnings(cfg, req, "https://stream.example.com/api/v1/oauth/youtube/callback")

	for _, w := range got {
		if strings.Contains(w, "reverse proxy") {
			t.Fatalf("warned about a proxy whose headers are trusted: %q", w)
		}
	}
}

// A malformed URI produces no warnings rather than a panic or a nonsense one.
func TestRedirectWarningsIgnoresAnUnparseableURI(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/platforms/guides", nil)
	req.Host = "localhost:8080"

	for _, bad := range []string{"", "://nope", "not a url at all"} {
		if got := redirectWarnings(config.Config{}, req, bad); len(got) != 0 {
			t.Errorf("redirectWarnings(%q) = %v, want none", bad, got)
		}
	}
}
