package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// X-Forwarded-Proto is believed by the same rule as X-Forwarded-For: only from
// a peer the Proxies set contains. With trustProxyHeaders on, a client that
// reaches the listener directly could otherwise choose its own cookie flags.
func TestForwardedProtoIsBelievedOnlyFromAListedProxy(t *testing.T) {
	for _, tc := range []struct {
		peer   string
		secure bool
	}{
		{"203.0.113.9:4444", false},
		{"127.0.0.1:4444", true},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-Proto", "https")
		got := cookies(t, New(make([]byte, 32), false, NewProxies(true, nil, nil), staticEpoch(0)), r)
		if got[SessionCookie].Secure != tc.secure {
			t.Errorf("peer %s: Secure=%v, want %v", tc.peer, got[SessionCookie].Secure, tc.secure)
		}
	}
}
