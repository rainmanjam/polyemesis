package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
)

func attemptSetup(t *testing.T, h http.Handler, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	r := jsonRequest(t, http.MethodPost, "/api/v1/setup",
		map[string]string{"username": "admin", "password": testPassword})
	r.RemoteAddr = remoteAddr
	return do(t, h, r)
}

// POST /api/v1/setup runs before any credential exists, so nothing about the
// request identifies the caller. GET /setup tells anyone who asks whether this
// install still needs one, which turns the gap between a process starting and
// its operator opening a browser into an announced race.
//
// CreateUser's WHERE NOT EXISTS keeps that race narrow -- the loser cannot take
// over an install that already has a user -- but narrow is not bounded, and
// unthrottled one address could hold the door open at whatever rate the network
// allows, paying a bcrypt hash of our CPU for each try.
func TestRepeatedSetupAttemptsEventuallyGet429WithRetryAfter(t *testing.T) {
	// This fixture already has an admin, so every attempt below is a losing
	// one -- which is the case that matters. An attacker's attempts are losing
	// attempts too, right up until the one that is not.
	_, h, _ := testServer(t, config.Config{})

	const addr = "203.0.113.5:44444"
	// Six, not five: the penalty is imposed BY the attempt that spends the
	// last free one, so it is the attempt after that which is turned away.
	for i := 0; i < 6; i++ {
		if code := attemptSetup(t, h, addr).Code; code != http.StatusBadRequest {
			t.Fatalf("attempt %d status = %d, want 400 while inside the free allowance", i+1, code)
		}
	}
	w := attemptSetup(t, h, addr)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt 7 status = %d, want %d -- setup is unthrottled",
			w.Code, http.StatusTooManyRequests)
	}
	if got := w.Header().Get("Retry-After"); got == "" || got == "0" {
		t.Errorf("Retry-After = %q, want a positive number of seconds", got)
	}
}

// One address hammering first-boot setup must not stop the operator's own
// address from completing it, which is the denial of service a global counter
// would trade this hole for.
func TestSetupThrottleIsPerAddress(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})

	for i := 0; i < 20; i++ {
		attemptSetup(t, h, "198.51.100.1:5000")
	}
	if code := attemptSetup(t, h, "203.0.113.5:44444").Code; code == http.StatusTooManyRequests {
		t.Errorf("untouched address status = %d; one attacker locked out everyone else", code)
	}
}

// The login throttle and the setup throttle count different things about the
// same address. Sharing one counter would mean a first-boot race spent the
// allowance the admin needs the moment their account exists.
func TestSetupAttemptsDoNotSpendTheLoginAllowance(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})

	const addr = "203.0.113.5:44444"
	for i := 0; i < 7; i++ {
		attemptSetup(t, h, addr)
	}
	if code := attemptLogin(t, h, addr, "", testPassword); code != http.StatusOK {
		t.Errorf("login status = %d, want 200 -- setup attempts throttled the login route", code)
	}
}
