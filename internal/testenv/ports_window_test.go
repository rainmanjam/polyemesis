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
