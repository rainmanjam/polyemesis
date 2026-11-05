package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/rainmanjam/polyemesis/internal/config"
)

// #3 of the 2026-11-04 lingering-state audit: sign-out cleared the cookie and
// nothing else. The session is a stateless JWT good for seven days, so a copy
// of the cookie went on working after "signed out", and so did the sockets it
// had opened.

func meStatus(t *testing.T, h http.Handler, sign func(*http.Request)) int {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/auth/me", nil)
	sign(r)
	return do(t, h, r).Code
}

func TestSigningOutEndsTheSessionItWasSentWith(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	if code := meStatus(t, h, sign); code != http.StatusOK {
		t.Fatalf("before sign-out /auth/me answered %d", code)
	}
	r := jsonRequest(t, http.MethodPost, "/api/v1/auth/logout", nil)
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("logout: %d %s", w.Code, w.Body.String())
	}
	if code := meStatus(t, h, sign); code != http.StatusUnauthorized {
		t.Fatalf("the cookie that signed out still answers %d on /auth/me; a copy of it keeps the "+
			"session for the rest of its seven days", code)
	}
	// And signing in again works: the new session is issued against the new epoch.
	again := login(t, h)
	if code := meStatus(t, h, again); code != http.StatusOK {
		t.Fatalf("a fresh sign-in after sign-out answered %d", code)
	}
}

// A script holding an API token reaches the route too, and must not sign the
// operator out of their browser.
func TestABearerTokenLogoutLeavesTheBrowserSessionAlone(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	plaintext := createToken(t, h, sign, "script")
	r := jsonRequest(t, http.MethodPost, "/api/v1/auth/logout", nil)
	r.Header.Set("Authorization", "Bearer "+plaintext)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("bearer logout: %d %s", w.Code, w.Body.String())
	}
	if code := meStatus(t, h, sign); code != http.StatusOK {
		t.Fatalf("a bearer-token logout ended the browser session (/auth/me %d)", code)
	}
}

// The socket the session opened is closed by the existing epoch heartbeat.
func TestSigningOutClosesTheSessionsOpenSocket(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	s := serverUnderTest(t, h)
	s.revokedMu.Lock()
	s.wsPingEvery = wsTestPing
	s.revokedMu.Unlock()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	c := sessionSocket(t, srv, sign)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := c.ReadMessage(); err != nil {
		t.Fatalf("the session socket delivered nothing before sign-out: %v", err)
	}
	closed := make(chan int, 1)
	go func() { closed <- drainUntilClosed(t, c, 20*wsTestPing+10*time.Second) }()

	r := jsonRequest(t, http.MethodPost, "/api/v1/auth/logout", nil)
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("logout: %d %s", w.Code, w.Body.String())
	}
	switch code := <-closed; code {
	case websocket.ClosePolicyViolation:
	case wsStillOpen:
		t.Fatal("the socket opened by the session that signed out is still delivering")
	default:
		t.Fatalf("close code = %d, want %d", code, websocket.ClosePolicyViolation)
	}
}
