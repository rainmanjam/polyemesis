package auth

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func fromPeer(peer, xff, realIP string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	r.RemoteAddr = peer
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	if realIP != "" {
		r.Header.Set("X-Real-IP", realIP)
	}
	return r
}

// THE HOLE THIS CLOSES. trustProxyHeaders used to mean "believe these headers
// from anyone". The shipped unit's --addr :8080 beats addr: 127.0.0.1 in
// config.yaml, so the port behind the proxy was usually public too, and a
// client that connected to it directly picked its own throttle key -- a fresh
// one per request -- and wrote its own address into the audit log.
func TestADirectClientCannotChooseItsOwnAddressWithTrustOn(t *testing.T) {
	p := NewProxies(true, nil, nil)
	th := NewThrottle()
	const attacker = "203.0.113.9:51000"

	var last string
	for i, spoof := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
		for _, r := range []*http.Request{fromPeer(attacker, spoof, ""), fromPeer(attacker, "", spoof)} {
			key := ClientIP(r, p)
			if key != "203.0.113.9" {
				t.Fatalf("request %d from a direct client keyed on %q, want its socket address", i, key)
			}
			last = key
			th.Fail(key)
		}
	}
	if th.Failures(last) != 6 {
		t.Errorf("throttle counted %d failures from one direct client, want 6", th.Failures(last))
	}
}

func TestTrustedProxiesExtendsLoopback(t *testing.T) {
	p := NewProxies(true, []netip.Prefix{
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("2001:db8::/32"),
	}, nil)
	for _, tc := range []struct {
		peer, want string
	}{
		{"127.0.0.1:1", "198.51.100.7"},           // loopback is always trusted
		{"[::1]:1", "198.51.100.7"},               // in both families
		{"172.17.0.1:1", "198.51.100.7"},          // the docker bridge, listed
		{"[::ffff:172.17.0.1]:1", "198.51.100.7"}, // v4-mapped is the same peer
		{"[2001:db8::5]:1", "198.51.100.7"},       // a listed v6 range
		{"192.168.1.9:1", "192.168.1.9"},          // a LAN host that is not listed
		{"not-an-address", "not-an-address"},      // unparsable peer is never trusted
	} {
		if got := ClientIP(fromPeer(tc.peer, "198.51.100.7", ""), p); got != tc.want {
			t.Errorf("peer %s: ClientIP = %q, want %q", tc.peer, got, tc.want)
		}
	}
}

// The proxy nobody listed -- nginx in another container -- is reported once,
// not on every request, and not at all for requests that carry no header.
func TestAnIgnoredForwardingHeaderIsReportedOnce(t *testing.T) {
	var told []string
	p := NewProxies(true, nil, func(peer string) { told = append(told, peer) })

	ClientIP(fromPeer("172.17.0.1:1", "", ""), p)
	if len(told) != 0 {
		t.Fatalf("reported a request with no forwarding header: %v", told)
	}
	for i := 0; i < 3; i++ {
		ClientIP(fromPeer("172.17.0.1:1", "198.51.100.7", ""), p)
	}
	ClientIP(fromPeer("127.0.0.1:1", "198.51.100.7", ""), p)
	if len(told) != 1 || told[0] != "172.17.0.1" {
		t.Fatalf("ignored-header reports = %v, want exactly [172.17.0.1]", told)
	}
}

func TestProxiesOffIgnoresExtraAndHeaders(t *testing.T) {
	p := NewProxies(false, []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}, nil)
	if p != nil {
		t.Fatal("NewProxies(false, ...) built a trusted set")
	}
	if got := ClientIP(fromPeer("127.0.0.1:1", "198.51.100.7", ""), p); got != "127.0.0.1" {
		t.Errorf("ClientIP with trust off = %q, want the socket address", got)
	}
}
