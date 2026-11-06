package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/auth"
	"github.com/rainmanjam/polyemesis/internal/config"
)

func forwardedReq(peer string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Host = "internal:8080"
	r.RemoteAddr = peer
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "evil.example")
	return r
}

// The OAuth redirect URI is built from the scheme and host the browser used.
// Those come from forwarded headers by the same rule as the client address:
// only from loopback or a listed proxy. A client reaching the listener directly
// must not be able to name the host a redirect URI points at.
func TestOAuthOriginBelievesForwardedHeadersOnlyFromAListedProxy(t *testing.T) {
	s := &Server{
		cfg:     config.Config{TrustProxyHeaders: true},
		proxies: auth.NewProxies(true, nil, nil),
	}
	if got := s.origin(forwardedReq("203.0.113.9:4000")); got != "http://internal:8080" {
		t.Errorf("unlisted peer: origin = %q, want its own scheme and host", got)
	}
	if got := s.origin(forwardedReq("127.0.0.1:4000")); got != "https://evil.example" {
		t.Errorf("trusted proxy: origin = %q, want the forwarded scheme and host", got)
	}
}

// With forwarding trust configured, X-Forwarded-Proto is believed only from
// loopback or a listed proxy. Without it the header is believed as before: a
// forged value only changes the forger's own response, and refusing it would
// give http:// embed links behind a TLS proxy.
func TestPlayoutOriginBelievesForwardedProtoOnlyFromAListedProxyWhenTrustIsConfigured(t *testing.T) {
	p := auth.NewProxies(true, nil, nil)
	for _, tc := range []struct {
		name    string
		peer    string
		proxies *auth.Proxies
		want    string
	}{
		{"unlisted peer, trust configured", "203.0.113.9:4000", p, "http://internal:8080"},
		{"trusted proxy", "127.0.0.1:4000", p, "https://internal:8080"},
		{"no trust configured keeps believing it", "203.0.113.9:4000", nil, "https://internal:8080"},
	} {
		if got := requestOrigin(forwardedReq(tc.peer), tc.proxies); got != tc.want {
			t.Errorf("%s: origin = %q, want %q", tc.name, got, tc.want)
		}
	}
}
