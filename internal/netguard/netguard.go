// Package netguard is the one SSRF address guard every outbound webhook in
// this tree is measured against.
//
// It exists because there were nearly two of them. internal/hooks grew a
// careful guard -- loopback, the cloud metadata address, RFC1918, IPv6 ULA,
// and the CGNAT range Tailscale hands out -- and internal/alerts, two hundred
// lines away, grew none, so POST /alerts/rules accepted http://169.254.169.254/
// and POST /alerts/rules/{id}/test reported back whether the port answered.
// That is an internal port scanner and a reach into instance metadata, driven
// from a form, by an operator who was never meant to have either (#607).
//
// The fix could have been a copy of the hooks guard. It is not, because two
// copies of a security check drift, and the copy that drifts is always the one
// nobody is looking at: the next range somebody remembers to add -- the way
// 100.64.0.0/10 was added after review of #489 -- would land in one file and
// not the other, and nothing would fail. Every caller imports this instead, so
// there is one list to be wrong about and one place to fix it.
package netguard

import (
	"context"
	"fmt"
	"net"
	"time"
)

// dialTimeout bounds one connection attempt. A webhook endpoint that will not
// answer must not hold a delivery worker open indefinitely.
const dialTimeout = 10 * time.Second

// IsPublicAddr reports whether ip is safe to let an outbound webhook actually
// reach: not loopback, not link-local (which covers 169.254.169.254, the cloud
// metadata address, and IPv6 fe80::/10 alike), not a private range (RFC1918,
// and IPv6 ULA fc00::/7 -- both covered by net.IP.IsPrivate), not the
// unspecified address, and not multicast.
//
// Used at two points per subsystem that must agree: a save-time literal-IP
// check, and DialContext below. A nil ip -- which is what net.ParseIP hands
// back for anything malformed -- reads as non-public, so a caller that passes
// ParseIP's result straight through fails closed.
func IsPublicAddr(ip net.IP) bool {
	// Host-local is the strictest half of the same list, so it is asked first
	// and it is the half that handles nil.
	if IsHostLocalAddr(ip) {
		return false
	}
	if ip.IsPrivate() {
		return false
	}
	// net.IP.IsPrivate is RFC1918 and IPv6 ULA and NOTHING ELSE, which leaves
	// ranges that are unroutable on the public internet but very much reachable
	// from the host. 100.64.0.0/10 is the practical one: carrier NAT, and the
	// range Tailscale hands out -- so without this a webhook to
	// http://100.64.0.1 was accepted and dialed, which is the overlay network
	// the guard most needs to keep a webhook out of.
	for _, cidr := range nonPublicRanges {
		if cidr.Contains(ip) {
			return false
		}
	}
	return true
}

// IsHostLocalAddr reports whether ip names the machine polyemesis itself is
// running on, or the link it is attached to: loopback, the unspecified address,
// multicast, and link-local -- which is where 169.254.169.254, the cloud
// instance metadata service, lives.
//
// IT IS A SEPARATE, SMALLER LIST THAN IsPublicAddr BECAUSE ONE CALLER CANNOT
// USE THE WHOLE ONE. A webhook has no business reaching any non-public address,
// so it gets IsPublicAddr and the RFC1918 refusal that comes with it. A pull
// INGEST is not the same question: an RTSP camera on 192.168.1.50 is the
// ordinary, intended case, and refusing RFC1918 there would delete the feature
// rather than guard it. What a pull source must never be allowed to reach is
// the host itself -- polyemesis's own admin API on 127.0.0.1, the loopback RTMP
// listener a stream key is the credential for -- and the metadata endpoint that
// hands out cloud credentials to anything that asks.
//
// It lives HERE, beside IsPublicAddr, rather than in internal/ffmpeg where its
// caller is, for the reason this package's doc comment gives: a second address
// list somewhere else is the one that never learns about the next range.
// IsPublicAddr is defined in terms of it, so the two cannot disagree about
// loopback or about link-local.
//
// A nil ip -- what net.ParseIP returns for anything malformed -- reads as
// host-local, so a caller that passes ParseIP's result straight through fails
// closed.
func IsHostLocalAddr(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast()
}

// nonPublicRanges are reachable-but-not-globally-routable networks that
// net.IP.IsPrivate does not know about. Parsed once; a bad constant here would
// panic at init rather than silently letting a range through.
var nonPublicRanges = func() []*net.IPNet {
	out := make([]*net.IPNet, 0, 8)
	for _, s := range []string{
		"100.64.0.0/10",   // RFC6598 shared address space (CGNAT, Tailscale)
		"192.0.0.0/24",    // RFC6890 IETF protocol assignments
		"198.18.0.0/15",   // RFC2544 benchmarking
		"192.0.2.0/24",    // RFC5737 TEST-NET-1
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"240.0.0.0/4",     // RFC1112 reserved
		"64:ff9b::/96",    // RFC6052 IPv4/IPv6 translation -- an embedded v4 target
	} {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			panic("netguard: bad non-public CIDR " + s + ": " + err.Error())
		}
		out = append(out, n)
	}
	return out
}()

// allowPrivateTargetKey carries one endpoint's allowPrivateTarget opt-in
// through to DialContext via the request context. It has to travel this way
// because an http.Transport's DialContext runs several layers below anything
// that still has the hook or rule value in hand -- all it is ever given is a
// context and an address.
type allowPrivateTargetKey struct{}

// WithAllowPrivateTarget marks a request as belonging to an endpoint whose
// operator deliberately opted into a private destination, so DialContext does
// not re-refuse what a save-time check already let through on purpose.
func WithAllowPrivateTarget(ctx context.Context, allow bool) context.Context {
	return context.WithValue(ctx, allowPrivateTargetKey{}, allow)
}

// AllowsPrivateTarget reports whether the opt-in rode in on ctx. Absent means
// no, which is the direction that fails closed: a caller who forgets to set it
// gets the guard, not a hole.
func AllowsPrivateTarget(ctx context.Context) bool {
	allow, _ := ctx.Value(allowPrivateTargetKey{}).(bool)
	return allow
}

// DialContext is the SSRF guard's second and controlling half, meant to be
// installed as an http.Transport's DialContext.
//
// A save-time check can catch a literal private IP, but it deliberately does
// not resolve a hostname -- DNS from inside a save request is slow and flaky,
// and more importantly its answer is not binding: the same name can resolve
// somewhere else by the time a delivery actually dials it. That gap is DNS
// rebinding, and it is closed here rather than there, because this is the only
// point that cannot be lied to by a stale answer -- it resolves and dials in
// the same breath, and it dials the IP it just checked, never the hostname a
// second time.
//
// Install it on the transport, never at a call site: a call site is something
// a later feature can forget, and the one path that forgot would be the one an
// attacker used.
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: dialTimeout}
	if AllowsPrivateTarget(ctx) {
		return dialer.DialContext(ctx, network, addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, a := range addrs {
		if !IsPublicAddr(a.IP) {
			lastErr = fmt.Errorf("refusing to dial non-public address %s; set "+
				"allowPrivateTarget on this endpoint to reach a self-hosted "+
				"target on purpose", a.IP)
			continue
		}
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no addresses resolved for %s", host)
	}
	return nil, lastErr
}
