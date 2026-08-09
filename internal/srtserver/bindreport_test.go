package srtserver

import (
	"io"
	"log/slog"
	"net"
	"strconv"
	"testing"
)

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
	// Hold the IPv6 wildcard on some port, then ask the server for that same
	// port. gosrt sets IPV6_V6ONLY for a "udp6" address, so this occupies
	// exactly one of the two families the wildcard splits into and leaves the
	// IPv4 one free -- which is the shape of the bug.
	occupied, err := net.ListenPacket("udp6", "[::]:0")
	if err != nil {
		// A host with no IPv6 at all cannot stage this. Logged rather than
		// silently skipped: a skip nobody sees is how a whole fleet of CI
		// runners quietly stops testing something.
		t.Skipf("SKIPPING: this host cannot bind udp6 at all (%v), so the "+
			"partial-bind condition cannot be staged here", err)
	}
	defer occupied.Close()

	_, portStr, err := net.SplitHostPort(occupied.LocalAddr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	if _, err := strconv.Atoi(portStr); err != nil {
		t.Fatalf("port %q is not a number: %v", portStr, err)
	}

	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)), ":"+portStr,
		func(string) (Target, bool) { return Target{}, false })
	if err := s.Start(); err != nil {
		t.Fatalf("Start returned %v; one family failing must not stop the "+
			"server, because the other family's encoders still work", err)
	}
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
