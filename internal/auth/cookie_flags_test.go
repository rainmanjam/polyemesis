package auth

// THE COOKIE FLAGS ARE CONDITIONAL, AND THE CONDITION IS THE POINT.
//
// Sonar reports go:S2092 four times and go:S3330 twice against this package and
// internal/api: "omitting the Secure flag makes cookie insecure", "make sure
// setting HttpOnly to false is safe here". Both are conditional-by-design and
// neither is a promise worth taking on trust, so this pins the behaviour rather
// than the spelling:
//
//   - Secure is not a literal `true` because polyemesis is self-hosted and
//     legitimately runs behind a terminating proxy or, in a lab, on plain HTTP.
//     Hardcoding it would silently drop the session cookie on those deployments
//     -- a login that appears to succeed and then does not. What matters is that
//     the flag is set whenever the connection IS secure, and that a client
//     cannot talk the server into thinking so.
//
//   - HttpOnly is false on the CSRF cookie ALONE, because the double-submit
//     pattern requires the SPA to read it and echo it back. The session cookie,
//     which is the bearer credential, is HttpOnly and must stay that way.
//
// The hazard these guards close is not "someone flips a boolean". It is that
// X-Forwarded-Proto is a header a client can send: if isSecure honoured it
// without trustProxy, any client could set the Secure flag from outside, and
// the far worse mirror -- a proxy deployment where it is NOT honoured -- drops
// the cookie. Both directions are tested below.

import (
	"bytes"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func mgr(secure, trustProxy bool) *Manager {
	return New(bytes.Repeat([]byte{0x7a}, 32), secure, trustProxy, staticEpoch(0))
}

func cookies(t *testing.T, m *Manager, r *http.Request) map[string]*http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	if err := m.SetSession(w, r, "token-value"); err != nil {
		t.Fatal(err)
	}
	out := map[string]*http.Cookie{}
	for _, c := range w.Result().Cookies() {
		out[c.Name] = c
	}
	if len(out) != 2 {
		t.Fatalf("SetSession wrote %d cookies, want the session and CSRF pair", len(out))
	}
	return out
}

func TestTheSessionCookieIsNeverReadableByScript(t *testing.T) {
	// Every configuration, because this one is not conditional on anything.
	for _, tc := range []struct{ secure, trust bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		got := cookies(t, mgr(tc.secure, tc.trust), httptest.NewRequest(http.MethodGet, "/", nil))
		if !got[SessionCookie].HttpOnly {
			t.Fatalf("session cookie is readable by script (secure=%v trustProxy=%v). "+
				"It is the bearer credential; HttpOnly is not a deployment choice.", tc.secure, tc.trust)
		}
		if got[CSRFCookie].HttpOnly {
			t.Fatal("the CSRF cookie is HttpOnly, so the SPA cannot read it to echo back. " +
				"Double-submit needs exactly this one cookie readable, which is why " +
				"go:S3330 fires here and why it is the only place it may.")
		}
	}
}

func TestSecureIsSetWheneverTheConnectionIsAndNotWhenAClientSaysSo(t *testing.T) {
	plain := func() *http.Request { return httptest.NewRequest(http.MethodGet, "/", nil) }
	forged := func() *http.Request {
		r := plain()
		r.Header.Set("X-Forwarded-Proto", "https")
		return r
	}
	tls := func() *http.Request {
		r := plain()
		r.TLS = &tls.ConnectionState{}
		return r
	}

	for _, tc := range []struct {
		name   string
		m      *Manager
		r      *http.Request
		secure bool
		why    string
	}{
		{"configured secure", mgr(true, false), plain(), true,
			"the operator configured TLS, so the flag must be set regardless of the request"},
		{"real TLS", mgr(false, false), tls(), true,
			"the request arrived over TLS, so the flag must be set"},
		{"forged header, proxy NOT trusted", mgr(false, false), forged(), false,
			"a client set X-Forwarded-Proto itself; honouring it lets any client " +
				"choose our cookie flags from outside"},
		{"forwarded header, proxy trusted", mgr(false, true), forged(), true,
			"a proxy we were told to trust reports TLS; NOT setting the flag here " +
				"drops the cookie on every proxied deployment"},
	} {
		got := cookies(t, tc.m, tc.r)
		for _, name := range []string{SessionCookie, CSRFCookie} {
			if got[name].Secure != tc.secure {
				t.Errorf("%s: %s cookie Secure=%v, want %v -- %s",
					tc.name, name, got[name].Secure, tc.secure, tc.why)
			}
		}
	}
}
