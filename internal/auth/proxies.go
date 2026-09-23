package auth

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
)

// Proxies is the set of peers whose X-Forwarded-For and X-Real-IP headers are
// believed. A nil *Proxies believes nobody, which is what trustProxyHeaders:
// false means.
//
// WHY A SET AND NOT A SWITCH. trustProxyHeaders used to be the whole answer:
// on, and every request's headers were read, whoever sent them. The docs say to
// bind 127.0.0.1 behind the proxy, but the shipped unit passes --addr :8080,
// which beats the file, so the port the proxy talks to was often still public.
// A client connecting to it directly could put any address in X-Forwarded-For
// and so choose its own login and setup throttle key, and the address the
// audit log records. Being "the proxy" now means being a peer this set
// contains: loopback always, plus whatever trustedProxies lists.
type Proxies struct {
	nets []netip.Prefix
	// onIgnored hears about the first request whose forwarding headers were
	// dropped because its peer is not trusted. Once, because the case it
	// exists for -- a proxy in another container or on another host that was
	// never listed -- sends such a request every time.
	onIgnored func(peer string)
	told      atomic.Bool
}

// NewProxies builds the trusted set: loopback, plus extra (config's
// TrustedProxyPrefixes). It returns nil when trust is false, and then extra is
// ignored. onIgnored may be nil.
func NewProxies(trust bool, extra []netip.Prefix, onIgnored func(peer string)) *Proxies {
	if !trust {
		return nil
	}
	nets := append([]netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	}, extra...)
	return &Proxies{nets: nets, onIgnored: onIgnored}
}

// trusts reports whether peer, a socket host with no port, is a proxy whose
// headers are believed.
func (p *Proxies) trusts(peer string) bool {
	if p == nil {
		return false
	}
	a, err := netip.ParseAddr(peer)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, n := range p.nets {
		if n.Contains(a) {
			return true
		}
	}
	return false
}

// ClientIP derives the address a request came from, for rate-limiting keys and
// for the audit log.
//
// X-Forwarded-For is honoured only when the request's own peer is a trusted
// proxy (see Proxies); otherwise any client could mint a fresh key per request
// and walk straight past the throttle.
//
// RIGHTMOST, NOT LEFTMOST, AND THAT WAS A REAL BYPASS. This used to take the
// leftmost entry on the premise that trustProxyHeaders asserts the proxy
// REWRITES the header. Our own deploy/nginx.conf.example did not: it shipped
// $proxy_add_x_forwarded_for, which APPENDS to whatever the client sent, and
// both SECURITY.md and docs/INSTALL.md tell the operator to turn
// trustProxyHeaders on. So on the documented deployment the leftmost entry was
// attacker-supplied: rotate it and every request gets a fresh throttle key,
// which is unlimited online guessing at the one admin password, with
// attacker-chosen addresses in the audit log to match.
//
// The rightmost entry is the hop the trusted proxy appended -- the address it
// actually saw -- so this is correct whether the proxy appends or overwrites,
// including proxy configurations we do not ship. The cost is a chain of
// several trusted proxies, where the rightmost is the previous proxy rather
// than the client: everyone behind it then shares one key, which throttles too
// much rather than too little. That is the direction a mistake here should
// point, and the fix for it is a trusted-hop count rather than trusting the
// client's own bytes. #647.
func ClientIP(r *http.Request, p *Proxies) string {
	peer := socketHost(r)
	if p == nil {
		return peer
	}
	xff := r.Header.Get("X-Forwarded-For")
	xri := strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if !p.trusts(peer) {
		if (xff != "" || xri != "") && p.onIgnored != nil && p.told.CompareAndSwap(false, true) {
			p.onIgnored(peer)
		}
		return peer
	}
	if xff != "" {
		if i := strings.LastIndexByte(xff, ','); i >= 0 {
			if last := strings.TrimSpace(xff[i+1:]); last != "" {
				return last
			}
			// A trailing comma means the proxy appended nothing usable;
			// fall through to the socket rather than to the client's half.
			return peer
		}
		if only := strings.TrimSpace(xff); only != "" {
			return only
		}
	}
	if xri != "" {
		return xri
	}
	return peer
}

// socketHost is the peer address with its port removed.
func socketHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
