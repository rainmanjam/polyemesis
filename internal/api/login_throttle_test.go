package api

import (
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
)

func attemptLogin(t *testing.T, h http.Handler, remoteAddr, forwardedFor, password string) int {
	t.Helper()
	r := jsonRequest(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "admin", "password": password})
	r.RemoteAddr = remoteAddr
	if forwardedFor != "" {
		r.Header.Set("X-Forwarded-For", forwardedFor)
	}
	return do(t, h, r).Code
}

func TestRepeatedBadPasswordsEventuallyGet429WithRetryAfter(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})

	const addr = "203.0.113.5:44444"
	for i := 0; i < 5; i++ {
		if code := attemptLogin(t, h, addr, "", "wrong"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401 while inside the free allowance", i+1, code)
		}
	}
	if code := attemptLogin(t, h, addr, "", "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("attempt 6 status = %d, want 401", code)
	}

	r := jsonRequest(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"username": "admin", "password": "wrong"})
	r.RemoteAddr = addr
	w := do(t, h, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt 7 status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}
	if got := w.Header().Get("Retry-After"); got == "" || got == "0" {
		t.Errorf("Retry-After = %q, want a positive number of seconds", got)
	}
}

func TestThrottledAddressIsRefusedEvenWithTheCorrectPassword(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})

	const addr = "203.0.113.5:44444"
	for i := 0; i < 6; i++ {
		attemptLogin(t, h, addr, "", "wrong")
	}
	// The lockout has to bite before the password check, or an attacker still
	// gets an unlimited number of bcrypt comparisons out of us.
	if code := attemptLogin(t, h, addr, "", testPassword); code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d", code, http.StatusTooManyRequests)
	}
}

func TestThrottleIsPerAddressSoOneAttackerCannotLockEveryoneOut(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})

	for i := 0; i < 20; i++ {
		attemptLogin(t, h, "198.51.100.1:5000", "", "wrong")
	}
	if code := attemptLogin(t, h, "203.0.113.5:44444", "", testPassword); code != http.StatusOK {
		t.Errorf("untouched address status = %d, want 200", code)
	}
}

func TestSuccessfulLoginResetsTheAddressBudget(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})

	const addr = "203.0.113.5:44444"
	for i := 0; i < 5; i++ {
		attemptLogin(t, h, addr, "", "wrong")
	}
	if code := attemptLogin(t, h, addr, "", testPassword); code != http.StatusOK {
		t.Fatalf("login with the right password status = %d, want 200", code)
	}
	// Budget restored, so five more mistakes are still free.
	for i := 0; i < 5; i++ {
		if code := attemptLogin(t, h, addr, "", "wrong"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d after a success status = %d, want 401", i+1, code)
		}
	}
}

func TestForwardedForCannotDefeatTheThrottleUnlessProxiesAreTrusted(t *testing.T) {
	tests := []struct {
		name       string
		trustProxy bool
		want       int
	}{
		{
			name: "spoofed X-Forwarded-For is ignored, so the socket stays throttled",
			want: http.StatusTooManyRequests,
		},
		{
			name:       "with a trusted proxy the header is the identity, so a new client starts fresh",
			trustProxy: true,
			want:       http.StatusUnauthorized,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, h, _ := testServer(t, config.Config{TrustProxyHeaders: tc.trustProxy})

			const addr = "203.0.113.5:44444"
			for i := 0; i < 6; i++ {
				attemptLogin(t, h, addr, "10.0.0.1", "wrong")
			}
			if code := attemptLogin(t, h, addr, "10.0.0.2", "wrong"); code != tc.want {
				t.Errorf("status = %d, want %d", code, tc.want)
			}
		})
	}
}
