package testenv

import (
	"fmt"
	"net"
	"testing"
)

// The window must be CONTIGUOUS, and it must still be HELD when it is returned.
//
// Both halves matter and only one of them is obvious. A helper that returned
// three contiguous numbers and then let them go would look correct in every
// assertion about the numbers, and would reintroduce the exact race it exists to
// close: internal/engine takes the base, builds a three-port allocator on it,
// and anything that grabbed one of the three in between makes the test fail
// during setup while blaming the code under test.
//
// So the second loop asserts the ports cannot be bound by anyone else. That is
// the property, not the arithmetic.
func TestFreeUDPWindowIsContiguousAndStillHeld(t *testing.T) {
	base, held := FreeUDPWindow(t, 3)

	if len(held) != 3 {
		t.Fatalf("held %d reservations, want 3", len(held))
	}
	for i, r := range held {
		if want := base + i; r.Port() != want {
			t.Errorf("reservation %d is port %d, want %d — the window is not contiguous, "+
				"so an allocator built on `base` would hand out a port nobody reserved", i, r.Port(), want)
		}
	}

	for i := 0; i < 3; i++ {
		p := base + i
		c, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			c.Close()
			t.Errorf("port %d was bindable while the window supposedly held it — the helper "+
				"returned numbers rather than a reservation, which is the bug it was written to fix", p)
		}
	}
}

// An occupied port inside a candidate run must push the scan past it.
//
// This is the case the first implementation could not do at all: it drew random
// ephemeral ports and looked for a run among them, which essentially never found
// one. Binding explicitly and stepping over what is taken is the whole design,
// so occupying a port in the middle of the likely first candidate and asking for
// a window is the direct test of it.
func TestFreeUDPWindowSkipsAnOccupiedPort(t *testing.T) {
	// Hold one port, then ask for a window. Whatever the scan settles on, it
	// must not include the port we are sitting on.
	blocker := ReserveUDP(t)
	blocked := blocker.Port()

	base, held := FreeUDPWindow(t, 4)
	for _, r := range held {
		if r.Port() == blocked {
			t.Fatalf("the window includes port %d, which was already held — the scan "+
				"is not checking availability, it is assuming it", blocked)
		}
	}
	if base <= 0 {
		t.Fatalf("base = %d, want a real port", base)
	}
}

// TestTheWindowScanWrapsInsteadOfMarchingIntoTheCeiling pins the bug that made
// internal/engine fail on macOS while passing on Linux.
//
// The old scan was `start < from+4096 && start+n < 65535` -- upward only. A probe
// port near the top of the range made that guard false on the FIRST iteration,
// so FreeUDPWindow tried not a single bind and reported "no run of 3 free UDP
// ports" about a machine where all but two were free. Linux's ephemeral range
// stops at 60999 and never reaches it; macOS runs to 65535 and does.
//
// Asserted through windowStarts rather than through FreeUDPWindow because the
// trigger is the kernel handing out a port within n of the ceiling, which a test
// cannot ask for.
func TestTheWindowScanWrapsInsteadOfMarchingIntoTheCeiling(t *testing.T) {
	// The exact shape that failed: probe at 65533, so from is 65534, asking for
	// the three contiguous ports internal/engine's allocator needs.
	starts := windowStarts(65534, 3, 4096)
	if len(starts) != 4096 {
		t.Fatalf("got %d start positions from a probe near the ceiling, want 4096: "+
			"the scan gave up without trying a single bind, which is the bug", len(starts))
	}
	for i, s := range starts {
		if s < 1024 || s+3-1 > 65535 {
			t.Fatalf("start[%d] = %d puts a 3-port window outside 1024..65535", i, s)
		}
	}
	// It must actually go somewhere usable, not sit on one number.
	if starts[0] == starts[1] {
		t.Errorf("the scan repeats %d rather than advancing", starts[0])
	}
}

// And the ordinary case must not have been disturbed by the wrap: a probe in the
// middle of the range still scans consecutively upward from it.
func TestTheWindowScanIsStillConsecutiveAwayFromTheCeiling(t *testing.T) {
	starts := windowStarts(40000, 3, 8)
	for i, want := 0, 40000; i < len(starts); i, want = i+1, want+1 {
		if starts[i] != want {
			t.Fatalf("start[%d] = %d, want %d: away from the ceiling the scan is "+
				"consecutive, and the wrap must not change that", i, starts[i], want)
		}
	}
}

// The two guards, which are the difference between a useful nil and a loop that
// hands the caller start positions no window can fit inside.
func TestTheWindowScanRefusesAnImpossibleWidthAndClampsALowProbe(t *testing.T) {
	// A window wider than the unprivileged range cannot be placed anywhere, and
	// saying so with nil is better than returning starts the bind loop will
	// silently fail on 4096 times before reporting the wrong reason.
	if got := windowStarts(40000, 70000, 8); got != nil {
		t.Errorf("windowStarts with n wider than the port range returned %d starts, want nil",
			len(got))
	}

	// A probe below 1024 must be lifted, or the scan would hand back privileged
	// ports that bind only as root -- passing for the wrong reason in a container
	// and failing everywhere else.
	for i, s := range windowStarts(80, 3, 16) {
		if s < 1024 {
			t.Fatalf("start[%d] = %d is privileged; a low probe must be clamped to 1024", i, s)
		}
	}
}
