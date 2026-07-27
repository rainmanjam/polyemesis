package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeClock lets the tests advance time instead of sleeping through a
// five-minute lockout.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func testThrottle(t *testing.T, max int) (*Throttle, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)}
	return newThrottle(max, clock.now), clock
}

func TestFirstFailuresAreFreeThenTheDelayDoubles(t *testing.T) {
	tr, _ := testThrottle(t, 16)

	want := map[int]time.Duration{
		1: 0, 2: 0, 3: 0, 4: 0, 5: 0,
		6: 2 * time.Second,
		7: 4 * time.Second,
		8: 8 * time.Second,
		9: 16 * time.Second,
	}
	for n := 1; n <= 9; n++ {
		got := tr.Fail("1.2.3.4")
		if got != want[n] {
			t.Errorf("delay after %d consecutive failures = %v, want %v", n, got, want[n])
		}
	}
}

func TestAttemptIsRefusedUntilThePenaltyHasElapsed(t *testing.T) {
	tr, clock := testThrottle(t, 16)

	for i := 0; i < throttleFreeAttempts; i++ {
		if d := tr.Fail("1.2.3.4"); d != 0 {
			t.Fatalf("failure %d imposed %v, want no delay inside the free allowance", i+1, d)
		}
	}
	if d := tr.Retry("1.2.3.4"); d != 0 {
		t.Fatalf("Retry inside the free allowance = %v, want 0", d)
	}

	tr.Fail("1.2.3.4")
	if d := tr.Retry("1.2.3.4"); d != throttleBaseDelay {
		t.Fatalf("Retry = %v, want %v", d, throttleBaseDelay)
	}

	clock.advance(throttleBaseDelay - time.Millisecond)
	if d := tr.Retry("1.2.3.4"); d != time.Millisecond {
		t.Errorf("Retry just before expiry = %v, want 1ms", d)
	}

	clock.advance(time.Millisecond)
	if d := tr.Retry("1.2.3.4"); d != 0 {
		t.Errorf("Retry after the penalty elapsed = %v, want 0", d)
	}
}

func TestThrottleIsKeyedPerAddress(t *testing.T) {
	tr, _ := testThrottle(t, 16)

	for i := 0; i < throttleFreeAttempts+4; i++ {
		tr.Fail("1.2.3.4")
	}
	if tr.Retry("1.2.3.4") == 0 {
		t.Fatal("the failing address should be waiting")
	}
	if d := tr.Retry("5.6.7.8"); d != 0 {
		t.Errorf("Retry for an untouched address = %v, want 0", d)
	}
}

func TestSuccessfulLoginClearsTheCounter(t *testing.T) {
	tr, clock := testThrottle(t, 16)

	for i := 0; i < throttleFreeAttempts+3; i++ {
		tr.Fail("1.2.3.4")
	}
	clock.advance(throttleMaxDelay) // wait out the penalty, well short of the idle TTL
	tr.Succeed("1.2.3.4")

	if d := tr.Fail("1.2.3.4"); d != 0 {
		t.Errorf("first failure after a success imposed %v, want the free allowance back", d)
	}
}

func TestPenaltyIsCappedSoTheAdminIsNeverLockedOutForever(t *testing.T) {
	tr, clock := testThrottle(t, 16)

	var last time.Duration
	for i := 0; i < 500; i++ {
		last = tr.Fail("1.2.3.4")
	}
	if last != throttleMaxDelay {
		t.Errorf("delay after 500 failures = %v, want it capped at %v", last, throttleMaxDelay)
	}

	clock.advance(throttleMaxDelay)
	if d := tr.Retry("1.2.3.4"); d != 0 {
		t.Errorf("Retry after waiting the cap = %v, want the admin let back in", d)
	}
}

func TestAnIdleCounterIsForgotten(t *testing.T) {
	tr, clock := testThrottle(t, 16)

	for i := 0; i < throttleFreeAttempts+5; i++ {
		tr.Fail("1.2.3.4")
	}
	clock.advance(throttleIdleTTL)

	if d := tr.Retry("1.2.3.4"); d != 0 {
		t.Errorf("Retry after the idle TTL = %v, want 0", d)
	}
	if d := tr.Fail("1.2.3.4"); d != 0 {
		t.Errorf("failure after the idle TTL imposed %v, want the free allowance back", d)
	}
}

func TestTrackedAddressesStayBoundedUnderAFloodOfSources(t *testing.T) {
	const max = 8
	tr, _ := testThrottle(t, max)

	for i := 0; i < 1000; i++ {
		tr.Fail(fmt.Sprintf("10.0.%d.%d", i/256, i%256))
	}
	if n := len(tr.attempts); n > max {
		t.Errorf("tracked addresses = %d, want at most %d", n, max)
	}
}

func TestClientIPTrustsForwardedHeadersOnlyWhenConfiguredTo(t *testing.T) {
	tests := []struct {
		name       string
		trustProxy bool
		remoteAddr string
		headers    map[string]string
		want       string
	}{
		{
			name:       "untrusted proxy headers are ignored",
			remoteAddr: "203.0.113.9:51000",
			headers:    map[string]string{"X-Forwarded-For": "1.1.1.1", "X-Real-IP": "2.2.2.2"},
			want:       "203.0.113.9",
		},
		{
			name:       "trusted X-Forwarded-For wins",
			trustProxy: true,
			remoteAddr: "10.0.0.1:51000",
			headers:    map[string]string{"X-Forwarded-For": "198.51.100.7"},
			want:       "198.51.100.7",
		},
		{
			name:       "leftmost entry of a chain is the client",
			trustProxy: true,
			remoteAddr: "10.0.0.1:51000",
			headers:    map[string]string{"X-Forwarded-For": " 198.51.100.7 , 10.0.0.5 "},
			want:       "198.51.100.7",
		},
		{
			name:       "X-Real-IP is the fallback",
			trustProxy: true,
			remoteAddr: "10.0.0.1:51000",
			headers:    map[string]string{"X-Real-IP": "198.51.100.7"},
			want:       "198.51.100.7",
		},
		{
			name:       "trusted but no headers falls back to the socket",
			trustProxy: true,
			remoteAddr: "10.0.0.1:51000",
			want:       "10.0.0.1",
		},
		{
			name:       "IPv6 loses its port",
			remoteAddr: "[2001:db8::1]:51000",
			want:       "2001:db8::1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
			r.RemoteAddr = tc.remoteAddr
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			if got := ClientIP(r, tc.trustProxy); got != tc.want {
				t.Errorf("ClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}
