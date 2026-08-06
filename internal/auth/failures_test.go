package auth

import "testing"

// Failures is what turns "somebody is guessing" into a number an operator can
// read, and it has to agree with the throttle's own arithmetic rather than
// keep a second count beside it.
//
// The alert path asks two questions of it. The sign-in failure alert asks "is
// this past the free allowance yet", because a first mistyped password must not
// reach anybody's phone. The sign-in success alert asks "how many failures came
// before this one", which is the question a reader of the failure alert asks
// next and which nothing else in the process can answer once Succeed has run.
func TestFailuresCountsWhatTheThrottleAlreadyKnows(t *testing.T) {
	tr, _ := testThrottle(t, 16)

	if n := tr.Failures("1.2.3.4"); n != 0 {
		t.Fatalf("Failures on an address never seen = %d, want 0", n)
	}
	for i := 1; i <= throttleFreeAttempts+2; i++ {
		tr.Fail("1.2.3.4")
		if n := tr.Failures("1.2.3.4"); n != i {
			t.Fatalf("Failures after %d rejections = %d, want %d", i, n, i)
		}
	}
	// The boundary the alert is published on: Fail returns a non-zero penalty
	// exactly when the count has passed the free allowance, so a handler that
	// gates on the penalty and a reader who counts the failures see the same
	// event.
	if tr.Failures("1.2.3.4") <= throttleFreeAttempts {
		t.Fatal("the free allowance was not actually exceeded; the rest of this test proves nothing")
	}

	// Other addresses are other counters. A distributed guesser must not be
	// able to inflate the number attributed to somebody else's address, which
	// is what a single global counter here would have done.
	if n := tr.Failures("5.6.7.8"); n != 0 {
		t.Fatalf("Failures for an untouched address = %d, want 0", n)
	}

	tr.Succeed("1.2.3.4")
	if n := tr.Failures("1.2.3.4"); n != 0 {
		t.Fatalf("Failures after a correct password = %d, want 0", n)
	}
}

// Idle expiry has to be visible here for the same reason it is visible in
// Retry: throttleIdleTTL is the guarantee that the admin who walks away comes
// back to a clean slate, and a Failures that kept reporting yesterday's count
// would put "47 previous failures" on a sign-in that had none.
func TestFailuresForgetsAnIdleCounterTheWayRetryDoes(t *testing.T) {
	tr, clock := testThrottle(t, 16)

	for i := 0; i < throttleFreeAttempts+1; i++ {
		tr.Fail("1.2.3.4")
	}
	if tr.Failures("1.2.3.4") == 0 {
		t.Fatal("Failures = 0 immediately after failing; nothing was recorded")
	}
	clock.advance(throttleIdleTTL)
	if n := tr.Failures("1.2.3.4"); n != 0 {
		t.Fatalf("Failures after the idle TTL = %d, want 0", n)
	}
}
