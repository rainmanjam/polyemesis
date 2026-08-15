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
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

// FreeUDPWindow reserves n CONTIGUOUS UDP ports and holds every one of them
// until the test ends, returning the base.
//
// WHY THIS EXISTS, and it is a bug that failed CI three times in one day on
// three different port ranges. internal/engine builds a deliberately tight
// allocator:
//
//	e.alloc = relay.NewPortAllocator(freeUDPPort(t), 3)
//
// The tightness is the point -- with three ports and no spare, a released port
// is the only one Allocate can return afterwards, which is what identifies it.
// But FreeUDPPort probes ONE port and releases it, so base+1 and base+2 were
// never checked at all and base is no longer held. Under `go test ./...` with
// packages in parallel, anything can take one of the three in the gap.
//
// The failure then surfaces while RESERVING the third port, during setup, and
// reads as though the code under test leaked one -- "the one taken by the path
// under test was never released". It had not. The window was short by one before
// the test began, and the diagnostic points at the wrong thing.
//
// HELD, NOT PROBED-AND-RELEASED. The caller gets a window nothing else on the
// machine can take, and Release is deliberately NOT called here: the reservation
// lives until t.Cleanup runs. A caller that needs the ports free for its own
// bind calls Release on the returned reservations itself, one at a time, at the
// moment it binds.
//
// Non-contiguous draws are discarded and retried rather than worked around. The
// kernel hands out ephemeral ports in no particular order, so a run of n is
// luck; 40 attempts is far more than enough in practice and failing loudly beats
// returning a window that is not one.
func FreeUDPWindow(t *testing.T, n int) (base int, held []*Reservation) {
	t.Helper()
	if n < 1 {
		t.Fatalf("FreeUDPWindow: n must be at least 1, got %d", n)
	}
	// EXPLICIT BINDS, SCANNING UPWARD -- not "draw ephemeral ports and hope".
	//
	// The first version of this asked the kernel for 4n random ports and looked
	// for a run of n among them. It essentially never found one: ephemeral ports
	// are handed out in no useful order, so a contiguous run is a coincidence.
	// It failed 40 attempts in under a second, which is at least a fast way to
	// learn that the idea was wrong.
	//
	// So: take one ephemeral port to find a region of the range the kernel is
	// currently willing to hand out, then try to bind n CONSECUTIVE numbers
	// starting a little above it. Anything already taken fails its bind and the
	// scan moves on.
	probe := ReserveUDP(t)
	from := probe.Port() + 1
	probe.Release()

	for start := from; start < from+4096 && start+n < 65535; start++ {
		run := make([]*Reservation, 0, n)
		for k := 0; k < n; k++ {
			c, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", start+k))
			if err != nil {
				break
			}
			r := &Reservation{port: start + k, c: c}
			t.Cleanup(r.Release)
			run = append(run, r)
		}
		if len(run) == n {
			return start, run
		}
		// Give the partial run straight back so the next start does not have to
		// step over ports this loop is holding.
		for _, r := range run {
			r.Release()
		}
	}
	t.Fatalf("FreeUDPWindow: no run of %d free UDP ports in 4096 numbers above %d", n, from)
	return 0, nil
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
// A NON-ZERO EXIT IS THE NEGATIVE ANSWER, not an error to propagate: lsof exits
// 1 when it matches nothing, which is exactly "no process holds it". Only the
// presence of a match is read as yes, so a tool that is missing entirely
// degrades to "not yet" and the caller's deadline still terminates the wait.
// That is the safe direction: the old failure was a wait that ENDED a child,
// and the worst this one can do is time out without touching it.
//
// ABSOLUTE PATHS AND NO SHELL. Both were flagged by go:S4036 -- a command
// resolved through PATH is a command whoever controls PATH chose. That is a
// fair objection even in a test helper: `go test` inherits the environment of
// whatever started it, so a helper that runs `lsof` from PATH inside CI runs
// whatever an earlier step left in front of it.
//
// The Windows arm also stops going through `cmd /c`, which removes a shell, a
// second PATH lookup for findstr, and the quoting that came with them. netstat
// prints every UDP row and the matching happens in Go, where it can be exact
// about the field it matches rather than substring-matching a whole line.
func udpPortHeld(port int) bool {
	suffix := ":" + strconv.Itoa(port)

	if runtime.GOOS == "windows" {
		// SystemRoot rather than a literal C:\Windows: the drive is not
		// guaranteed, and this is the variable Windows itself uses.
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		// -ano: numeric addresses, all sockets, owning PID. Numeric matters --
		// name resolution on a runner with no DNS is where a bounded check
		// becomes a hang.
		out, err := exec.Command(filepath.Join(root, "System32", "netstat.exe"), "-ano", "-p", "UDP").Output()
		if err != nil && len(out) == 0 {
			return false
		}
		for _, line := range strings.Split(string(out), "\n") {
			// Fields: Proto, LocalAddress, ForeignAddress, PID. The local
			// address is the only one that says what is BOUND here, and it is
			// matched as a whole field: a substring match on the line would
			// also hit a foreign address or a PID containing those digits.
			if f := strings.Fields(line); len(f) >= 2 && strings.HasSuffix(f[1], suffix) {
				return true
			}
		}
		return false
	}

	// lsof moves between /usr/sbin and /usr/bin across distributions, so the
	// candidates are tried in order rather than assumed. An absent lsof yields
	// "not held", the same safe direction as an empty result.
	for _, bin := range []string{"/usr/sbin/lsof", "/usr/bin/lsof", "/bin/lsof"} {
		if _, err := os.Stat(bin); err != nil {
			continue
		}
		// -nP: no host and no port-name resolution, for the reason above.
		// -iUDP:<port> is the whole query; the socket table is read, not joined.
		out, err := exec.Command(bin, "-nP", "-iUDP:"+strconv.Itoa(port)).Output()
		if err != nil && len(out) == 0 {
			return false
		}
		return len(bytes.TrimSpace(out)) > 0
	}
	return false
}
