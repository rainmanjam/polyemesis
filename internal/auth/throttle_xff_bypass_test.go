package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

/* THE BYPASS THIS FILE EXISTS TO KEEP CLOSED.
 *
 * deploy/nginx.conf.example shipped $proxy_add_x_forwarded_for, which APPENDS
 * to whatever the client sent, and SECURITY.md and docs/INSTALL.md both tell
 * the operator to set trustProxyHeaders: true. ClientIP took the leftmost
 * entry -- the client's own bytes. Rotating that header therefore produced a
 * fresh throttle key per request: the 5-attempt policy never fired, and the
 * audit log recorded addresses the attacker chose.
 *
 * The unit-level assertion (rightmost wins) lives in the table in
 * throttle_test.go. This one is written the way the attack is: many requests
 * from one address, each with a different forged leftmost hop, and the
 * question is whether the throttle counts them together. A test that only
 * checked ClientIP's return value would still pass if someone keyed the
 * throttle on something else. #647.
 */

func forged(remote, spoof string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	r.RemoteAddr = remote + ":51000"
	// What nginx produces with $proxy_add_x_forwarded_for: the client's
	// header, then the address nginx actually saw.
	r.Header.Set("X-Forwarded-For", spoof+", "+remote)
	return r
}

func TestRotatingTheForgedHopDoesNotMintFreshThrottleKeys(t *testing.T) {
	const attacker = "203.0.113.9"
	th := NewThrottle()

	var last string
	for i := 0; i < 12; i++ {
		key := ClientIP(forged(attacker, fmt.Sprintf("198.51.100.%d", i)), true)
		if i > 0 && key != last {
			t.Fatalf("request %d keyed on %q, previous keyed on %q: a rotating "+
				"X-Forwarded-For is minting a fresh throttle key per request, "+
				"which is the bypass", i, key, last)
		}
		last = key
		th.Fail(key)
	}

	if got := th.Failures(last); got != 12 {
		t.Errorf("throttle counted %d failures across 12 forged requests, want 12", got)
	}
	if wait := th.Retry(last); wait <= 0 {
		t.Error("throttle is not making the attacker wait after 12 failures")
	}
}

func TestTheKeyIsTheAddressTheProxySaw(t *testing.T) {
	// Not merely "stable" -- stable on the wrong value would throttle every
	// client of that proxy as one. It must be the hop nginx appended.
	const attacker = "203.0.113.9"
	if got := ClientIP(forged(attacker, "198.51.100.7"), true); got != attacker {
		t.Errorf("ClientIP = %q, want %q (the address the proxy saw)", got, attacker)
	}
}

func TestForgedHeadersStillIgnoredWithoutATrustedProxy(t *testing.T) {
	// The control. trustProxy=false must keep ignoring the header entirely,
	// or this fix would have opened a different door.
	if got := ClientIP(forged("203.0.113.9", "198.51.100.7"), false); got != "203.0.113.9" {
		t.Errorf("ClientIP = %q, want the socket address when no proxy is trusted", got)
	}
}
