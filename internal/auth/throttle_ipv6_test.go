package auth

import (
	"fmt"
	"testing"
	"time"
)

// ONE VPS IS ONE GUESSER. Keyed on the full address, every address in a /64
// -- 2^64 of them, all routed to the same machine -- got its own five free
// attempts, so a single IPv6 host could guess the admin password without
// limit on a dual-stack listener.
func TestSixFailuresFromSixAddressesInOneSlash64AreThrottled(t *testing.T) {
	tr, _ := testThrottle(t, 16)
	for i := 1; i <= 6; i++ {
		tr.Fail(fmt.Sprintf("2001:db8:1:2::%x", i))
	}
	if wait := tr.Retry("2001:db8:1:2:ffff::7"); wait <= 0 {
		t.Fatal("a seventh address in the same /64 may try again at once; the /64 is not one key")
	}
	if got := tr.Failures("2001:db8:1:2::abcd"); got != 6 {
		t.Errorf("failures counted for the /64 = %d, want 6", got)
	}
	// The next /64 over is somebody else.
	if wait := tr.Retry("2001:db8:1:3::1"); wait != 0 {
		t.Errorf("a different /64 waits %v; it must not share the penalty", wait)
	}
}

func TestAMappedIPv4AddressIsTheIPv4Address(t *testing.T) {
	tr, _ := testThrottle(t, 16)
	for i := 0; i < 3; i++ {
		tr.Fail("192.0.2.1")
		tr.Fail("::ffff:192.0.2.1")
	}
	if got := tr.Failures("192.0.2.1"); got != 6 {
		t.Fatalf("failures for 192.0.2.1 = %d, want 6 counted as one client", got)
	}
	tr.Succeed("::ffff:192.0.2.1")
	if got := tr.Failures("192.0.2.1"); got != 0 {
		t.Errorf("Succeed on the mapped form left %d failures on the plain form", got)
	}
}

func TestThrottleKeyLeavesWhatIsNotAnAddressAlone(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.1":            "192.0.2.1",
		"::ffff:192.0.2.1":     "192.0.2.1",
		"2001:db8::1":          "2001:db8::/64",
		"fe80::1%eth0":         "fe80::/64",
		"not an address":       "not an address",
		"":                     "",
		"2001:db8:0:0:1:2:3:4": "2001:db8::/64",
	} {
		if got := throttleKey(in); got != want {
			t.Errorf("throttleKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// A pool of addresses, each staying inside its free allowance, still spends
// one shared budget -- and the budget refills, so it never becomes a lockout.
func TestAPoolOfAddressesSpendsOneSharedBudget(t *testing.T) {
	tr, clock := testThrottle(t, 4096)
	for i := 0; i < throttleGlobalBurst; i++ {
		if wait := tr.Retry(fmt.Sprintf("198.51.%d.%d", i/250, i%250)); wait != 0 {
			t.Fatalf("attempt %d from a fresh address waited %v inside the burst", i, wait)
		}
		tr.Fail(fmt.Sprintf("198.51.%d.%d", i/250, i%250))
	}
	wait := tr.Retry("203.0.113.200")
	if wait <= 0 || wait > throttleGlobalRefill {
		t.Fatalf("a fresh address after %d failures from %d others waits %v, want (0, %v]",
			throttleGlobalBurst, throttleGlobalBurst, wait, throttleGlobalRefill)
	}
	clock.advance(throttleGlobalRefill)
	if wait := tr.Retry("203.0.113.200"); wait != 0 {
		t.Errorf("after one refill interval the fresh address still waits %v", wait)
	}
	// Refills are capped at the burst, so an idle hour does not bank a flood.
	clock.advance(time.Hour)
	for i := 0; i < throttleGlobalBurst; i++ {
		tr.Fail(fmt.Sprintf("192.0.2.%d", i))
	}
	if wait := tr.Retry("203.0.113.201"); wait <= 0 {
		t.Error("an idle hour banked more than one burst of attempts")
	}
}
