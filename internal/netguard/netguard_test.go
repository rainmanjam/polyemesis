package netguard

import (
	"context"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestIsPublicAddrRejectsMetadataAndPrivateRanges is the mutation-checked core
// of the guard. Before it existed on the alert-rule path, a webhook URL was
// only checked for scheme, parseability and a non-empty host -- nothing
// rejected the cloud metadata address, loopback, or any RFC1918/ULA range, so
// a webhook was a pivot from the operator console into whatever else lives on
// the box or its network.
//
// This list is the SINGLE list now. It used to live in internal/hooks with
// internal/alerts having none of it, which is the drift #607 was filed about.
func TestIsPublicAddrRejectsMetadataAndPrivateRanges(t *testing.T) {
	private := []string{
		"169.254.169.254", // cloud metadata -- the sharpest instance of this
		"127.0.0.1",       // loopback
		"127.53.1.2",      // loopback, non-canonical form
		"10.0.0.5",        // RFC1918
		"172.16.4.4",      // RFC1918
		"192.168.1.1",     // RFC1918
		"0.0.0.0",         // unspecified
		"224.0.0.1",       // multicast
		"::1",             // IPv6 loopback
		"fe80::1",         // IPv6 link-local
		"fc00::1",         // IPv6 ULA
	}
	for _, s := range private {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("test bug: %q did not parse as an IP", s)
		}
		if IsPublicAddr(ip) {
			t.Errorf("IsPublicAddr(%s) = true, want false -- this is not a "+
				"public address a webhook should be allowed to reach without "+
				"an explicit opt-in", s)
		}
	}

	public := []string{"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888"}
	for _, s := range public {
		if !IsPublicAddr(net.ParseIP(s)) {
			t.Errorf("IsPublicAddr(%s) = false, want true -- an ordinary "+
				"public address must not be refused", s)
		}
	}

	// An address that failed to parse must read as non-public rather than
	// panicking on a nil receiver -- callers pass net.ParseIP's result
	// straight through, and a malformed literal must fail closed.
	if IsPublicAddr(nil) {
		t.Error("IsPublicAddr(nil) = true, want false")
	}
}

// TestIsPublicAddrRefusesReachableButNotRoutableRanges covers what
// net.IP.IsPrivate does NOT know: it is RFC1918 and IPv6 ULA and nothing else,
// so the ranges below reached the dialer while looking public. 100.64.0.0/10 is
// the one that matters in practice -- carrier NAT, and what Tailscale hands out
// -- so a webhook could be pointed straight into an overlay network. Found in
// review of #489, and the reason this list must not be copied per package.
func TestIsPublicAddrRefusesReachableButNotRoutableRanges(t *testing.T) {
	for _, tc := range []struct{ name, ip string }{
		{"RFC6598 CGNAT / Tailscale", "100.64.0.1"},
		{"RFC6598 upper bound", "100.127.255.254"},
		{"IETF protocol assignments", "192.0.0.1"},
		{"benchmarking", "198.18.0.1"},
		{"TEST-NET-1", "192.0.2.1"},
		{"TEST-NET-2", "198.51.100.1"},
		{"TEST-NET-3", "203.0.113.1"},
		{"reserved", "240.0.0.1"},
		{"NAT64 embedding a v4 target", "64:ff9b::c000:221"},
		// The same address wearing its IPv6 clothes must not slip past.
		{"IPv4-mapped RFC1918", "::ffff:10.0.0.1"},
		{"IPv4-mapped CGNAT", "::ffff:100.64.0.1"},
		{"IPv4-mapped loopback", "::ffff:127.0.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("%q did not parse, so this case tests nothing", tc.ip)
			}
			if IsPublicAddr(ip) {
				t.Errorf("%s (%s) is treated as public, so a webhook may be "+
					"pointed at it with no opt-in", tc.name, tc.ip)
			}
		})
	}
	// And the guard must not have become "refuse everything", which would pass
	// every case above while breaking every real webhook.
	for _, ok := range []string{"1.1.1.1", "93.184.216.34", "2606:4700:4700::1111"} {
		if !IsPublicAddr(net.ParseIP(ok)) {
			t.Errorf("%s is refused; the guard now blocks legitimate targets", ok)
		}
	}
}

// TestDialContextClosesTheDNSRebindingGap proves the half a save-time check
// cannot do: it never resolves a hostname (see the package doc for why), so a
// hostname that resolves to a private address -- including one that only starts
// doing so after the endpoint was saved, i.e. DNS rebinding -- is caught here
// instead, at the moment of the actual dial. The server under test binds
// 127.0.0.1, which is exactly the shape of address this guard exists to keep an
// outbound webhook off without an explicit opt-in.
func TestDialContextClosesTheDNSRebindingGap(t *testing.T) {
	srv := httptest.NewServer(nil)
	defer srv.Close()

	addr := srv.Listener.Addr().String() // 127.0.0.1:<port>

	if _, err := DialContext(context.Background(), "tcp", addr); err == nil {
		t.Fatal("DialContext connected to a loopback address with no " +
			"allowPrivateTarget opt-in in the context")
	} else if !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("error = %q, want it to say the address is non-public", err)
	}

	ctx := WithAllowPrivateTarget(context.Background(), true)
	conn, err := DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatalf("DialContext refused a loopback dial with the opt-in set: %v "+
			"-- the opt-in must actually reach the dialer", err)
	}
	_ = conn.Close()
}

// TestAllowsPrivateTargetDefaultsToNo pins the direction the missing value
// fails in. A caller that forgets WithAllowPrivateTarget must get the guard,
// not a hole -- and a context carrying some OTHER package's bool under some
// other key must not be mistaken for the opt-in.
func TestAllowsPrivateTargetDefaultsToNo(t *testing.T) {
	if AllowsPrivateTarget(context.Background()) {
		t.Error("a context with no opt-in reads as allowed; the guard would be " +
			"off everywhere somebody forgot to set it")
	}
	if !AllowsPrivateTarget(WithAllowPrivateTarget(context.Background(), true)) {
		t.Error("the opt-in did not survive the context round trip")
	}
	if AllowsPrivateTarget(WithAllowPrivateTarget(context.Background(), false)) {
		t.Error("an explicit false reads as allowed")
	}
}

// TestDialContextRejectsAnUnsplittableAddress covers the guard ahead of
// resolution: an addr with no port cannot even be split, and that must be
// reported as the dial failing rather than panicking or silently resolving the
// wrong thing.
func TestDialContextRejectsAnUnsplittableAddress(t *testing.T) {
	if _, err := DialContext(context.Background(), "tcp", "no-port-here"); err == nil {
		t.Fatal("DialContext accepted an address with no port")
	}
}

// TestDialContextReportsAResolutionFailure covers the branch where the address
// DOES split but the host half fails to resolve. An empty host is the fastest
// deterministic way to force that failure without depending on real DNS or
// network access: net.SplitHostPort(":0") accepts the empty host as
// syntactically valid, and looking up "" fails locally, with no network round
// trip.
func TestDialContextReportsAResolutionFailure(t *testing.T) {
	if _, err := DialContext(context.Background(), "tcp", ":0"); err == nil {
		t.Fatal("DialContext resolved an empty host")
	}
}

// TestDialContextReportsADialFailureForAPublicAddress covers the loop's other
// branch: an address that resolves and passes IsPublicAddr, but where the
// actual dial then fails. 8.8.8.8 is a real public address; port 81 on it does
// not answer, so the attempt times out against the context deadline rather than
// reaching anything, and the deadline bounds the test either way.
func TestDialContextReportsADialFailureForAPublicAddress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := DialContext(ctx, "tcp", "8.8.8.8:81"); err == nil {
		t.Fatal("DialContext connected to 8.8.8.8:81; want the attempt to " +
			"fail against the context deadline")
	}
}

// TestHostLocalIsTheStricterHalfOfTheSameList pins the relationship the two
// predicates are meant to have: anything host-local is non-public, and the
// difference between them is exactly the ranges a pull ingest is allowed to
// reach and a webhook is not.
func TestHostLocalIsTheStricterHalfOfTheSameList(t *testing.T) {
	hostLocal := []string{
		"127.0.0.1", "127.7.7.7", "::1",
		"169.254.169.254", // the cloud metadata service
		"169.254.10.9", "fe80::1",
		"0.0.0.0", "::",
		"224.0.0.1", "ff02::1",
		"::ffff:127.0.0.1", // IPv4-mapped loopback
	}
	for _, s := range hostLocal {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("test bug: %q does not parse", s)
		}
		if !IsHostLocalAddr(ip) {
			t.Errorf("IsHostLocalAddr(%s) = false", s)
		}
		if IsPublicAddr(ip) {
			t.Errorf("IsPublicAddr(%s) = true; host-local must imply non-public", s)
		}
	}

	// Reachable but not the host: refused for a webhook, allowed for a pull.
	// This gap is the entire reason the two predicates exist separately.
	nearby := []string{"192.168.1.50", "10.0.0.8", "172.16.4.4", "100.64.0.1", "fd00::1"}
	for _, s := range nearby {
		ip := net.ParseIP(s)
		if IsHostLocalAddr(ip) {
			t.Errorf("IsHostLocalAddr(%s) = true; an RTSP camera lives here", s)
		}
		if IsPublicAddr(ip) {
			t.Errorf("IsPublicAddr(%s) = true; a webhook must not reach it", s)
		}
	}

	// nil fails closed in both directions.
	if !IsHostLocalAddr(nil) {
		t.Error("IsHostLocalAddr(nil) = false; a malformed address must fail closed")
	}
	if IsPublicAddr(nil) {
		t.Error("IsPublicAddr(nil) = true")
	}
}
