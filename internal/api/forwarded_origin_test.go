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

func TestPlayoutOriginBelievesForwardedProtoOnlyFromAListedProxy(t *testing.T) {
	p := auth.NewProxies(true, nil, nil)
	if got := requestOrigin(forwardedReq("203.0.113.9:4000"), p); got != "http://internal:8080" {
		t.Errorf("unlisted peer: origin = %q", got)
	}
	if got := requestOrigin(forwardedReq("127.0.0.1:4000"), p); got != "https://internal:8080" {
		t.Errorf("trusted proxy: origin = %q", got)
	}
	if got := requestOrigin(forwardedReq("127.0.0.1:4000"), nil); got != "http://internal:8080" {
		t.Errorf("no proxies configured: origin = %q", got)
	}
}
