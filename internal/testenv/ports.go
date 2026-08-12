package testenv

// PORTS. Issue #211.
//
// Four packages carried their own copy of the same helper:
//
//	internal/srtserver/srtserver_test.go   freePort
//	internal/srtserver/wildcard_test.go    freeUDPPort
//	internal/api/renditions_test.go        freeUDPPort
//	internal/engine/manager_test.go        freeUDPPort, freeTCPPort
//
// Each binds :0, reads the number back, and closes the socket on return -- so
// the port is FREE from the moment the helper returns until the thing under
// test binds it for real, and every statement in between is a window in which
// something else in the process, in another parallel package, or on the machine
// can take it. The failure that produces is a rare "address already in use"
// reported against the product.
//
// TWO SHAPES, AND THE DIFFERENCE MATTERS.
//
//	Reserve*  keeps the socket open. The port cannot be taken until Release is
//	          called, so the window shrinks to whatever sits between Release and
//	          the real bind. Use it whenever the call site can put those two
//	          next to each other.
//	Free*Port is the old semantics exactly -- reserve, read, release -- for the
//	          call sites where the number travels through a settings row and is
//	          bound much later by something the test does not drive directly.
//
// The window is NOT closed by either, and renditions_test.go was right about
// why: a probe that tries to PREDICT whether a later bind will succeed cannot
// be honest, because gosrt sets SO_REUSEADDR on its listener and a plain
// ListenPacket does not, so the probe and the real thing do not even agree on
// what "in use" means. What this file changes is that the window is stated ONCE
// and is as short as each call site can make it, rather than being four copies
// of a comment.

import (
	"bytes"
	"io"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Reservation is a port the kernel has given out and which is still HELD.
//
// Release is what makes it available. It is idempotent, and it also runs from
// t.Cleanup, so a test that returns early cannot leak the socket into the rest
// of the package -- which would be a worse failure than the race this replaces:
// a port held for the whole run, refusing every later bind of it.
type Reservation struct {
	port int
	c    io.Closer
	once sync.Once
}

// Port is the reserved number. Valid before and after Release; what changes is
// whether anything else can take it.
func (r *Reservation) Port() int { return r.port }

// Release hands the port back. Call it immediately before the bind under test.
func (r *Reservation) Release() {
	r.once.Do(func() { _ = r.c.Close() })
}

// ReserveUDP takes a UDP port on the loopback and keeps it.
func ReserveUDP(t *testing.T) *Reservation {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not reserve a UDP port: %v", err)
	}
	r := &Reservation{port: c.LocalAddr().(*net.UDPAddr).Port, c: c}
	t.Cleanup(r.Release)
	return r
}

// ReserveTCP is the same for a TCP listener.
func ReserveTCP(t *testing.T) *Reservation {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not reserve a TCP port: %v", err)
	}
	r := &Reservation{port: l.Addr().(*net.TCPAddr).Port, c: l}
	t.Cleanup(r.Release)
	return r
}

// FreeUDPPort is ReserveUDP followed immediately by Release: a number that was
// free a moment ago and is nobody's now.
//
// The honest description of what this buys is "far less racy than a hard-coded
// 6000", which is what internal/engine's copy already said. It is here so the
// four packages share one implementation rather than four, and so a call site
// that CAN hold the port has an obvious upgrade path next to it.
func FreeUDPPort(t *testing.T) int {
	t.Helper()
	r := ReserveUDP(t)
	r.Release()
	return r.port
}

// FreeTCPPort is the same trick for a TCP listener.
func FreeTCPPort(t *testing.T) int {
	t.Helper()
	r := ReserveTCP(t)
	r.Release()
	return r.port
}

// WaitUDPPortBound waits until something else holds the UDP port, and reports
// whether it happened.
//
// THE OBSERVER FOR "THE CHILD HAS BOUND ITS SOCKET", which the shell suites
// already have as poly_wait_port_ready and Go did not. A test that starts an
// FFmpeg analyser on a UDP port and then sleeps a guessed interval before
// pushing at it is asserting that the guess was long enough, on every runner,
// under every load. When it is not, the datagrams land in nothing and the
// failure is attributed to the measurement rather than to the wait.
//
// It returns false rather than failing the test: what to do about a child that
// never bound belongs to the caller, who knows what it was and can say so.
//
// IT OBSERVES THE SOCKET TABLE AND DOES NOT BIND. Issue #279: this used to
// decide whether the child had taken the port by taking the port itself --
//
//	c, err := net.ListenPacket("udp", addr)
//	if err != nil { return true }   // somebody else has it
//	_ = c.Close()
//
// -- and while it held that socket the child could not bind. FFmpeg does not
// retry a failed UDP bind; it exits. The probe then found the port free for the
// rest of its budget and reported a timeout naming the child, which had died
// because of the probe. Measured at 14 in 400 with the child binding
// immediately, and every timeout in that run was a bind the probe had stolen.
//
// The docstring above already claimed this was "the same question the shell
// asks" through poly_wait_port_ready. It was not: lsof READS the table, and
// ListenPacket COMPETES for it. That sentence is why nobody looked again, so
// the code now does what it always said it did.
func WaitUDPPortBound(port int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		if udpPortHeld(port) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// udpPortHeld reports whether any process holds a UDP socket on the port,
// without opening one.
//
// A NON-ZERO EXIT IS THE NEGATIVE ANSWER, not an error to propagate: both lsof
// and findstr exit 1 when they match nothing, which is exactly "no process
// holds it". Only the presence of output is read as yes, so a tool that is
// missing entirely -- which exits non-zero with no output as well -- degrades
// to "not yet" and the caller's deadline still terminates the wait. That is the
// safe direction: the old failure was a wait that ended a child, and the worst
// this can do is time out without touching it.
func udpPortHeld(port int) bool {
	p := strconv.Itoa(port)
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// -ano lists numeric addresses with owning PIDs and never resolves
		// names, which is what keeps this bounded on a machine with no DNS.
		cmd = exec.Command("cmd", "/c", "netstat -ano -p UDP | findstr :"+p+" ")
	} else {
		// -nP: no host and no port name resolution, for the same reason.
		// -iUDP:<port> is the whole query; the socket table is read, not joined.
		cmd = exec.Command("lsof", "-nP", "-iUDP:"+p)
	}
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return false
	}
	return len(bytes.TrimSpace(out)) > 0
}
