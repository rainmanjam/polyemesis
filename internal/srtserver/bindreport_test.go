package srtserver

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"syscall"
	"testing"
)

// stagePartialBind occupies the IPv6 half of one port and leaves the IPv4 half
// free, then starts a server on it.
//
// WHY THE IPv4 HALF IS RESERVED FIRST AND THEN RELEASED. The obvious staging --
// take an ephemeral port on udp6 and assume its IPv4 twin is free -- reserves
// only one of the two halves and then depends on nothing else claiming the
// other. That held almost always and failed in CI on 2026-08-14 with
// `no address family could be bound`: between the port being chosen and Start
// reaching it, something else took IPv4. `go test ./...` runs packages
// concurrently and several of them bind ephemeral ports, so the window is real
// even though it is small.
//
// Binding udp4 first pins the number in the IPv4 space while the udp6
// reservation is made, so the pair is never assigned to anyone else while the
// condition is being set up. The v4 socket is then closed immediately before
// Start, which is the only moment the half has to be free.
//
// That still leaves a window, so the caller retries. It cannot be closed
// entirely from user space -- the port has to be genuinely free for Start to
// bind it, and "free" is exactly what another process may act on.
//
// Returns ok=false when the race was lost, so the caller can restage on a
// different port rather than reporting a failure of the thing under test.
func stagePartialBind(t *testing.T) (s *Server, held net.PacketConn, ok bool) {
	t.Helper()

	// Pin the port number in the IPv4 space first.
	//
	// FATAL RATHER THAN A SKIP, unlike the udp6 case below. A host with no IPv6
	// is a legitimate deployment and the reason Start tolerates a partial bind
	// at all, so failing to stage that is a skip. A host that cannot bind an
	// ephemeral IPv4 port is not a deployment, it is a broken machine, and
	// skipping there would quietly stop testing #105 on it.
	v4, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("cannot bind an ephemeral udp4 port (%v); this is not a host "+
			"configuration the server is expected to survive", err)
	}
	_, portStr, err := net.SplitHostPort(v4.LocalAddr().String())
	if err != nil {
		v4.Close()
		t.Fatalf("SplitHostPort: %v", err)
	}
	if _, err := strconv.Atoi(portStr); err != nil {
		v4.Close()
		t.Fatalf("port %q is not a number: %v", portStr, err)
	}

	// Now the IPv6 half of that same port. gosrt sets IPV6_V6ONLY for a "udp6"
	// address, so this occupies exactly one of the two families the wildcard
	// splits into -- which is the shape of the bug.
	v6, err := net.ListenPacket("udp6", "[::]:"+portStr)
	if err != nil {
		v4.Close()
		// A host with no IPv6 at all cannot stage this. Logged rather than
		// silently skipped: a skip nobody sees is how a whole fleet of CI
		// runners quietly stops testing something.
		t.Skipf("SKIPPING: this host cannot bind udp6 at all (%v), so the "+
			"partial-bind condition cannot be staged here", err)
	}

	// Release the IPv4 half, and immediately ask the server for the port.
	v4.Close()

	s = New(slog.New(slog.NewTextHandler(io.Discard, nil)), ":"+portStr,
		func(string) (Target, bool) { return Target{}, false })
	if err := s.Start(); err != nil {
		// WHICH OF THE TWO HAPPENED. A failure here is either the race this
		// function exists to absorb, or the regression the test exists to
		// catch, and retrying the second one eight times before reporting it
		// would bury the real answer under the excuse.
		//
		// The server's OWN RECORD separates them, and re-binding the port does
		// not: a Start that fails after binding one family still holds that
		// socket, so probing the IPv4 half finds it occupied by the very
		// listener under test. Asking the report is also free of any race,
		// because it describes what happened rather than what is true now.
		//
		// Bound non-empty means Start got a family up and refused to run
		// anyway -- which is #105 coming back. Bound empty means it really
		// could not get either, and the IPv4 half was taken.
		rep := s.Report()
		s.Stop()
		v6.Close() // or eight retries hold eight sockets
		if len(rep.Bound) > 0 {
			t.Fatalf("Start returned %v after successfully binding %v; one "+
				"family failing must not stop the server, because the other "+
				"family's encoders still work", err, rep.Bound)
		}
		t.Logf("restaging: Start on :%s bound nothing (%v) -- another process "+
			"took the IPv4 half between releasing it and binding it", portStr, err)
		return nil, nil, false
	}
	return s, v6, true
}

// A partial bind starts, and says so.
//
// This is #105. Start already survived one address family failing, on purpose:
// a container with IPv6 disabled is a legitimate deployment, and refusing to
// boot there would turn a missing feature into an outage. What it did not do
// was leave any trace a program could read -- the record was a log line, and
// every downstream answer was derived from a slice being non-empty, so a
// half-bound listener reported itself as bound. An encoder pointed at the
// family that never came up then fails against a server calling itself healthy.
//
// The test EXECUTES Start against a real occupied port rather than asserting
// about bindAddrs, because the thing under test is what Start records, not what
// it intended to try.
func TestPartialBindStartsAndReportsDegraded(t *testing.T) {
	// Retried, because the condition is staged against the operating system's
	// port table and another process can take the half this needs free. Eight
	// attempts turns a rare flake into one that will not be seen again; a
	// permanent failure still fails, because every attempt fails -- and a
	// regression fails on the FIRST one, from inside stagePartialBind, rather
	// than being retried seven more times and reported as bad luck.
	var s *Server
	var occupied net.PacketConn
	const attempts = 8
	for i := 0; i < attempts; i++ {
		var ok bool
		if s, occupied, ok = stagePartialBind(t); ok {
			break
		}
		if i == attempts-1 {
			t.Fatalf("could not stage a partial bind in %d attempts; either "+
				"this host cannot leave an IPv4 half free, or Start no longer "+
				"survives one address family failing -- which is the bug this "+
				"test exists for", attempts)
		}
	}
	defer occupied.Close()
	defer s.Stop()

	report := s.Report()
	if !report.Degraded() {
		t.Fatalf("Report().Degraded() = false with one family occupied; "+
			"report = %+v", report)
	}
	if len(report.Bound) != 1 {
		t.Errorf("Bound = %v, want exactly the IPv4 address", report.Bound)
	}
	if len(report.Failed) != 1 {
		t.Fatalf("Failed = %+v, want exactly the IPv6 address", report.Failed)
	}
	// The operator needs the errno, not a bare "degraded": "address already in
	// use" and "address family not supported" call for different actions.
	if report.Failed[0].Err == "" {
		t.Error("the failure carried no error text, so nothing can explain itself")
	}
	if host, _, _ := net.SplitHostPort(report.Failed[0].Addr); host != "::" {
		t.Errorf("the failed address was %q, want the IPv6 wildcard",
			report.Failed[0].Addr)
	}
	// This host DOES have IPv6 -- the test could not have staged the condition
	// otherwise -- and the family was refused because something else holds the
	// port. That is the case an operator can act on, and it is the case the
	// badge exists for. See BindReport.Actionable and #105.
	if report.Failed[0].Unavailable {
		t.Errorf("the failure was classified as an absent address family, but this "+
			"host bound udp6 a moment ago to set the test up: %+v", report.Failed[0])
	}
	if !report.Actionable() {
		t.Errorf("Actionable() = false for a port held by another process; that is "+
			"exactly what the operator can fix, so it must reach the badge: %+v", report)
	}
}

// TestActionableSeparatesAnAbsentFamilyFromABrokenOne is #105's badge ruling,
// asserted where the classification lives.
//
// Degraded and Actionable answer two different questions and the difference is
// the whole change: Degraded asks whether every requested address bound, which
// on an IPv4-only host is permanently no and permanently nothing to fix;
// Actionable asks whether any address family this machine HAS was refused
// anyway. The first is the right input to the log line and the wrong input to
// an orange badge that never goes away.
//
// A table over reports rather than over listeners, because the case that
// matters cannot be staged: no CI runner with IPv6 can be made to pretend it
// has none. TestPartialBindStartsAndReportsDegraded above covers the other
// direction against a real socket.
func TestActionableSeparatesAnAbsentFamilyFromABrokenOne(t *testing.T) {
	const (
		v4 = "0.0.0.0:6000"
		v6 = "[::]:6000"
	)
	tests := []struct {
		name string
		rep  BindReport
		want bool
	}{
		{"a clean bind", BindReport{Requested: []string{v4, v6}, Bound: []string{v4, v6}}, false},
		{
			"an IPv4-only host: normal, and no operator action exists",
			BindReport{
				Requested: []string{v4, v6}, Bound: []string{v4},
				Failed: []BindFailure{{Addr: v6, Err: "address family not supported", Unavailable: true}},
			},
			false,
		},
		{
			"a family this host has, refused anyway",
			BindReport{
				Requested: []string{v4, v6}, Bound: []string{v4},
				Failed: []BindFailure{{Addr: v6, Err: "bind: address already in use"}},
			},
			true,
		},
		{
			"one of each: the fixable one wins",
			BindReport{
				Requested: []string{v4, v6}, Bound: []string{"127.0.0.1:6000"},
				Failed: []BindFailure{
					{Addr: v6, Err: "address family not supported", Unavailable: true},
					{Addr: v4, Err: "bind: permission denied"},
				},
			},
			true,
		},
		{
			"nothing bound at all is not degraded; Start returns an error instead",
			BindReport{
				Requested: []string{v4, v6},
				Failed: []BindFailure{
					{Addr: v4, Err: "bind: address already in use"},
					{Addr: v6, Err: "bind: address already in use"},
				},
			},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rep.Actionable(); got != tt.want {
				t.Fatalf("Actionable() = %v, want %v (Degraded() = %v)",
					got, tt.want, tt.rep.Degraded())
			}
		})
	}
}

// TestFamilyUnavailableReadsTheErrnoAndNotTheMessage pins how the two cases are
// told apart.
//
// Three errnos because three kernels spell it differently, and errors.Is
// against the syscall values rather than a substring of strerror -- a reworded
// or translated message must not turn a normal IPv4-only host into a permanent
// alarm, which is the failure mode this whole item is about.
//
// The errors are wrapped the way net actually delivers them, through
// *net.OpError and *os.SyscallError, because an unwrapped syscall.Errno would
// prove that errors.Is compares two constants and nothing else.
func TestFamilyUnavailableReadsTheErrnoAndNotTheMessage(t *testing.T) {
	wrap := func(errno syscall.Errno) error {
		return &net.OpError{
			Op: "listen", Net: "udp6", Addr: nil,
			Err: os.NewSyscallError("socket", errno),
		}
	}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"EAFNOSUPPORT: no such address family in this kernel", wrap(syscall.EAFNOSUPPORT), true},
		{"EPFNOSUPPORT: the BSD spelling of the same refusal", wrap(syscall.EPFNOSUPPORT), true},
		{"EADDRNOTAVAIL: IPv6 compiled in and disabled by sysctl", wrap(syscall.EADDRNOTAVAIL), true},
		{"EADDRINUSE: the family exists and something else has the port", wrap(syscall.EADDRINUSE), false},
		{"EACCES: the family exists and this process may not bind it", wrap(syscall.EACCES), false},
		{"an error carrying no errno at all", errors.New("listen udp6: something else entirely"), false},
		// THE DISCRIMINATING ROW, and without it this table proved nothing about
		// the claim in its own name.
		//
		// Every row above derives its message FROM its errno, so a naive
		// implementation -- strings.Contains over strerror rather than errors.Is
		// over the syscall values -- passed the whole table. Measured: replacing
		// familyUnavailable with substring matching left it green. The production
		// code was correct; the guarantee was simply unasserted.
		//
		// This message carries EAFNOSUPPORT's exact wording and no errno at all,
		// so a substring implementation says true and errors.Is says false.
		//
		// The wording is deliberately the one that is stable across kernels.
		// EADDRNOTAVAIL is spelled "can't assign requested address" on darwin and
		// "cannot assign requested address" on linux, so a row built from that
		// text would pin the guarantee on one platform's strerror and pass
		// vacuously on the other -- the same species of accident as the rest of
		// the table.
		{
			"the words without the errno",
			errors.New("listen udp6: address family not supported by protocol"),
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := familyUnavailable(tt.err); got != tt.want {
				t.Fatalf("familyUnavailable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// A clean bind must NOT read as degraded, or the signal is worthless: an
// operator who sees the warning on every healthy start stops reading it.
func TestAFullBindIsNotDegraded(t *testing.T) {
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)), ":0",
		func(string) (Target, bool) { return Target{}, false })
	if err := s.Start(); err != nil {
		t.Skipf("SKIPPING: this host could not bind the wildcard at all (%v)", err)
	}
	defer s.Stop()

	report := s.Report()
	if report.Degraded() {
		t.Errorf("a clean start reported degraded: %+v", report)
	}
	if len(report.Failed) != 0 {
		t.Errorf("a clean start recorded failures: %+v", report.Failed)
	}
}
