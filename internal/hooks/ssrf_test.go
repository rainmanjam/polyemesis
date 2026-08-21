package hooks

import (
	"context"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestIsPublicAddrRejectsMetadataAndPrivateRanges is the mutation-checked
// core of the SSRF guard (#4 in the poka-yoke audit). Before this, a webhook
// URL was only checked for scheme, parseability and a non-empty host --
// nothing rejected the cloud metadata address, loopback, or any RFC1918/ULA
// range, so a webhook was a pivot from the operator console into whatever
// else lives on the box or its network.
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
		if isPublicAddr(ip) {
			t.Errorf("isPublicAddr(%s) = true, want false -- this is not a "+
				"public address a webhook should be allowed to reach without "+
				"an explicit opt-in", s)
		}
	}

	public := []string{"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888"}
	for _, s := range public {
		ip := net.ParseIP(s)
		if !isPublicAddr(ip) {
			t.Errorf("isPublicAddr(%s) = false, want true -- an ordinary "+
				"public address must not be refused", s)
		}
	}
}

// TestHookValidateRejectsLiteralPrivateTargetUnlessAllowed is the save-time
// half of the guard: a hook whose URL is a literal private or metadata
// address is refused, and the refusal lifts only when the operator sets
// AllowPrivateTarget deliberately -- a per-hook opt-in, not a blanket
// disablement of the check.
func TestHookValidateRejectsLiteralPrivateTargetUnlessAllowed(t *testing.T) {
	blocked := Hook{Name: "ssrf", URL: "http://169.254.169.254/latest/meta-data/"}.Normalized()
	if err := blocked.Validate(); err == nil {
		t.Fatal("Validate accepted a hook targeting the cloud metadata address")
	} else if !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("error = %q, want it to explain the target is non-public", err)
	}

	lan := Hook{Name: "lan", URL: "http://10.1.2.3:9000/hook"}.Normalized()
	if err := lan.Validate(); err == nil {
		t.Fatal("Validate accepted a hook targeting a private LAN address")
	}

	// The opt-in lifts the refusal for the operator who genuinely wants a
	// self-hosted target -- this is the escape hatch the brief requires so
	// the device does not just get disabled by an operator it blocks.
	allowed := lan
	allowed.AllowPrivateTarget = true
	if err := allowed.Validate(); err != nil {
		t.Fatalf("Validate rejected a private target with AllowPrivateTarget "+
			"set: %v -- the opt-in must actually work", err)
	}

	public := Hook{Name: "public", URL: "http://8.8.8.8/hook"}.Normalized()
	if err := public.Validate(); err != nil {
		t.Fatalf("Validate rejected an ordinary public IP target: %v", err)
	}
}

// TestSafeDialContextClosesTheDNSRebindingGap proves the half of the guard
// Validate cannot do: Validate never resolves a hostname (see the comment in
// hooks.go for why), so a hostname that resolves to a private address --
// including one that only starts doing so after the hook was saved, i.e. DNS
// rebinding -- is caught here instead, at the moment of the actual dial. The
// server under test binds 127.0.0.1, which is exactly the shape of address
// this guard exists to keep a hook off without an explicit opt-in.
func TestSafeDialContextClosesTheDNSRebindingGap(t *testing.T) {
	srv := httptest.NewServer(nil)
	defer srv.Close()

	addr := srv.Listener.Addr().String() // 127.0.0.1:<port>

	if _, err := safeDialContext(context.Background(), "tcp", addr); err == nil {
		t.Fatal("safeDialContext connected to a loopback address with no " +
			"AllowPrivateTarget opt-in in the context")
	} else if !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("error = %q, want it to say the address is non-public", err)
	}

	ctx := withAllowPrivateTarget(context.Background(), true)
	conn, err := safeDialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatalf("safeDialContext refused a loopback dial with the opt-in "+
			"set: %v -- the opt-in must actually reach the dialer, not just "+
			"Validate", err)
	}
	_ = conn.Close()
}
