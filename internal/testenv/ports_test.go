package testenv_test

// Tests for the shared port helpers, issue #211.
//
// WHAT IS AND IS NOT CLAIMED HERE.
//
// These do not prove that a test using ReserveUDP never loses a port race --
// nothing can prove that, because the race is with the whole machine. What they
// prove is the one property that distinguishes a reservation from the four
// copies it replaces: WHILE A RESERVATION IS HELD, THE PORT CANNOT BE TAKEN.
// That is the entire difference, it is deterministic on every platform Go
// supports, and without a test for it a Reservation that quietly closed its
// socket in the constructor would be indistinguishable from one that works
// while being exactly as racy as what came before.
//
// The double-bind assertions are cross-platform on purpose. Go sets
// SO_REUSEADDR on TCP listeners on unix but not on Windows, and sets it on
// neither for unicast UDP; in no configuration does it permit two live sockets
// on the same 127.0.0.1:port. So "the second bind fails" is a fact on ubuntu,
// macos and windows alike, which is where CI runs.

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// Mutation: in ReserveUDP, close the socket before returning (which is exactly
// what the four helpers this replaces do).
// Observed to fail with "a second UDP bind on 127.0.0.1:NNNNN succeeded while
// the reservation was still held".
func TestAUDPReservationHoldsThePortUntilRelease(t *testing.T) {
	r := testenv.ReserveUDP(t)
	if r.Port() == 0 {
		t.Fatal("the reservation reports port 0, which is the wildcard rather than a port")
	}
	addr := udpAddr(r.Port())

	if c, err := net.ListenPacket("udp", addr); err == nil {
		_ = c.Close()
		t.Fatalf("a second UDP bind on %s succeeded while the reservation was still held: "+
			"the port is not reserved at all, and every caller that believes it is has the "+
			"same window the four copies of this helper had", addr)
	}

	r.Release()

	c, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Fatalf("the port was still not bindable after Release: %v. A reservation that "+
			"never hands the port back is worse than the race it replaces -- it refuses "+
			"the bind the test is about", err)
	}
	_ = c.Close()
}

// Mutation: in ReserveTCP, close the listener before returning.
// Observed to fail with "a second TCP listen on 127.0.0.1:NNNNN succeeded".
func TestATCPReservationHoldsThePortUntilRelease(t *testing.T) {
	r := testenv.ReserveTCP(t)
	addr := udpAddr(r.Port())

	if l, err := net.Listen("tcp", addr); err == nil {
		_ = l.Close()
		t.Fatalf("a second TCP listen on %s succeeded while the reservation was still held", addr)
	}

	r.Release()

	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("the port was still not listenable after Release: %v", err)
	}
	_ = l.Close()
}

// Release runs from t.Cleanup as well as from the call site, so it is called
// twice on every well-behaved test. If it were not idempotent the SECOND call
// would close a socket the caller had since reopened on the same variable, or
// panic -- and it would do so at cleanup time, after the test had already
// reported PASS.
func TestReleaseIsIdempotent(t *testing.T) {
	r := testenv.ReserveUDP(t)
	r.Release()
	r.Release()
	c, err := net.ListenPacket("udp", udpAddr(r.Port()))
	if err != nil {
		t.Fatalf("the port is not bindable after two Releases: %v", err)
	}
	_ = c.Close()
}

// The counterpart claim, and the reason both shapes exist: FreeUDPPort is NOT
// held. Every call site that takes a bare int depends on that -- a Free*Port
// that forgot to release would refuse the bind of the very thing under test, in
// four packages at once.
//
// Mutation: drop the `r.Release()` from FreeUDPPort.
// Observed to fail with "FreeUDPPort returned a port that is still held".
func TestFreePortIsNotHeld(t *testing.T) {
	p := testenv.FreeUDPPort(t)
	c, err := net.ListenPacket("udp", udpAddr(p))
	if err != nil {
		t.Fatalf("FreeUDPPort returned a port that is still held (%v): every caller binds "+
			"the number it was given, so this is an immediate failure in four packages", err)
	}
	_ = c.Close()

	tp := testenv.FreeTCPPort(t)
	l, err := net.Listen("tcp", udpAddr(tp))
	if err != nil {
		t.Fatalf("FreeTCPPort returned a port that is still held: %v", err)
	}
	_ = l.Close()
}

// WaitUDPPortBound in both directions. The positive is what replaces a guessed
// sleep before pushing datagrams at a child's socket; the negative is what stops
// it becoming a hang when the child never binds.
//
// Mutation: invert the sense of the bind check in WaitUDPPortBound
// (`if err != nil` -> `if err == nil`).
// Observed to fail on both sub-cases: the held port reported unbound, and the
// free port reported bound.
func TestWaitUDPPortBoundSeesABindAndGivesUpBounded(t *testing.T) {
	held := testenv.ReserveUDP(t)
	if !testenv.WaitUDPPortBound(held.Port(), 2*time.Second) {
		t.Errorf("WaitUDPPortBound said nothing holds %d while this test holds it: a caller "+
			"would push datagrams into a socket it believes is not there yet, or wait out "+
			"its whole budget after the child was already up", held.Port())
	}

	free := testenv.FreeUDPPort(t)
	started := time.Now()
	if testenv.WaitUDPPortBound(free, 200*time.Millisecond) {
		t.Errorf("WaitUDPPortBound said something holds %d, which nothing does: a wait that "+
			"is satisfied by an unbound port is the guessed sleep again with extra steps", free)
	}
	// Bounded, because the alternative in a CI step is a hang that burns the job
	// timeout -- the same failure shape as issue #179.
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("WaitUDPPortBound ran %s against a 200ms budget", elapsed)
	}
}

func udpAddr(port int) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}
