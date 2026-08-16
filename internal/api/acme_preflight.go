package api

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
)

// The Let's Encrypt walkthrough's server half.
//
// WHAT THIS IS FOR. An operator on a self-signed certificate, with a real
// domain already pointed at the box, is one config change away from a trusted
// one and has no way to know it. The Settings page can show them the change --
// it is four lines of YAML -- but the sentence that matters is the one before
// it: what happens if you try. Issuance either works or fails for a small,
// enumerable set of reasons, and every one of them is knowable BEFORE the
// restart that would otherwise turn a working self-signed server into a broken
// one for an hour while the rate limit clears.
//
// WHAT IT DELIBERATELY DOES NOT DO. It does not write config.yaml. That file is
// root:polyemesis 0640 and this service cannot write it; giving it the power to
// rewrite its own transport security is a privilege decision, not a feature.
// And the operator who needs this most is reaching the UI over plain HTTP,
// where a form that accepts a contact address and reconfigures the server is
// exactly the wrong thing to offer. Guidance is safe to show over HTTP; a write
// path is not.
//
// WHAT IT REFUSES TO GUESS. Nothing inside this box can establish that the
// public internet reaches port 80 on it -- that requires something on the other
// side of the NAT, and this server does not phone anywhere to find out. Where
// the answer is unknowable from here, the check says so and names what to run
// from elsewhere. A check that claimed to have proven external reachability
// would be worse than no check, because it would be believed.

// Verdicts. Only acmeFail blocks readiness: acmeUnknown is the honest answer
// where this process cannot see far enough, and an operator must not be stopped
// by a question nobody can answer from in here.
const (
	acmePass    = "pass"
	acmeFail    = "fail"
	acmeUnknown = "unknown"
)

// acmeCheck is one prerequisite and what this server can honestly say about it.
//
// Detail is English prose from the server, like tlsStatus.CertificateError and
// HSTSWarning beside it: these sentences name config keys, systemd directives
// and paths, and they are what the operator will paste into a search or an
// issue. The UI translates the LABEL, which it derives from ID -- see
// acmeCheckLabel in SettingsPage.tsx.
type acmeCheck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// acmePreflight is the whole answer to "what happens if I try".
type acmePreflight struct {
	// Hostname is the name the checks were run against: the one asked for,
	// falling back to tls.hostname.
	Hostname string      `json:"hostname"`
	Mode     config.Mode `json:"mode"`
	// Ready is true when nothing this server can see would stop issuance. It
	// is not a promise that issuance will succeed — see the note above about
	// what cannot be established from inside this box.
	Ready  bool        `json:"ready"`
	Checks []acmeCheck `json:"checks"`
}

// handleACMEPreflight reports what Let's Encrypt would need from this host.
//
// The hostname is a QUERY PARAMETER rather than the request's Host header, and
// that is deliberate twice over. An operator on tls.mode: off has no configured
// hostname at all, so the name has to come from somewhere — and the browser
// already knows which name was typed to reach this page, so the UI fills it in
// without this server having to trust a header for it. It also keeps the one
// piece of network I/O in here — a DNS lookup — pointed at a name an
// authenticated operator asked for on purpose, rather than at whatever a Host
// header happened to carry.
func (s *Server) handleACMEPreflight(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSuffix(strings.TrimSpace(r.URL.Query().Get("hostname")), ".")
	if host == "" {
		host = s.cfg.TLS.Hostname
	}
	// 253 is the longest a DNS name can be. Past that there is nothing to
	// resolve, and refusing here keeps an arbitrarily long string out of the
	// resolver rather than out of politeness.
	if len(host) > 253 {
		writeError(w, http.StatusBadRequest,
			"that is not a hostname: a DNS name is at most 253 characters")
		return
	}

	out := acmePreflight{Hostname: host, Mode: s.cfg.ResolvedTLSMode()}
	out.Checks = []acmeCheck{
		acmeNameCheck(host),
		s.acmeDNSCheck(r.Context(), host),
		s.acmePort80Check(),
		s.acmeEmailCheck(),
	}
	if c, ok := s.acmeIssuanceCheck(); ok {
		out.Checks = append(out.Checks, c)
	}

	out.Ready = true
	for _, c := range out.Checks {
		if c.Status == acmeFail {
			out.Ready = false
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// acmeNameCheck is the one prerequisite that needs nothing but the string.
func acmeNameCheck(host string) acmeCheck {
	switch {
	case host == "":
		return acmeCheck{ID: "name", Status: acmeFail, Detail: "No hostname is set. Let's Encrypt issues " +
			"for a name, never for an address, so tls.hostname has to hold the name people type to reach this server."}
	case config.IsPublicFQDN(host):
		return acmeCheck{ID: "name", Status: acmePass, Detail: host + " is the shape of name a public " +
			"certificate authority can issue for. Whether you control it is between you and your registrar."}
	default:
		return acmeCheck{ID: "name", Status: acmeFail, Detail: host + " is not a name Let's Encrypt can " +
			"issue for. An IP address, a single label with no dot, and the reserved suffixes .local, .internal, " +
			".lan, .home, .arpa and localhost never receive a public certificate. Use a name in a domain you " +
			"control, or stay on the self-signed CA and install it — the traffic is encrypted either way."}
	}
}

// acmeDNSCheck asks the resolver where the name points, and is careful about
// what that proves.
//
// A name resolving to an address this machine holds is as close to proof as
// anything in here gets. A name resolving somewhere else proves NOTHING from
// this side: it is exactly what a correct deployment behind NAT, a floating IP
// or a load balancer looks like, and also exactly what a record pointing at the
// wrong host looks like. Those two are indistinguishable from inside, so the
// check says so rather than picking one.
func (s *Server) acmeDNSCheck(ctx context.Context, host string) acmeCheck {
	if !config.IsPublicFQDN(host) {
		return acmeCheck{ID: "dns", Status: acmeUnknown,
			Detail: "There is no public name to look up yet."}
	}

	// Bounded, because a resolver that never answers must not hold an HTTP
	// handler open until the client gives up.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	ips, err := s.resolver()(ctx, host)
	if err != nil || len(ips) == 0 {
		detail := host + " does not resolve from this machine"
		if err != nil {
			detail += ": " + err.Error()
		}
		return acmeCheck{ID: "dns", Status: acmeFail, Detail: detail + ". Let's Encrypt looks the name up " +
			"from the public internet, so a record missing here is very likely missing there too. Add an A or " +
			"AAAA record for it before restarting."}
	}

	list := make([]string, 0, len(ips))
	for _, ip := range ips {
		list = append(list, ip.String())
	}
	joined := strings.Join(list, ", ")
	if s.holdsAnyAddress(ips) {
		return acmeCheck{ID: "dns", Status: acmePass,
			Detail: host + " resolves to " + joined + ", which is an address on this machine."}
	}
	return acmeCheck{ID: "dns", Status: acmeUnknown, Detail: host + " resolves to " + joined + ", which is " +
		"not an address this machine holds. Behind NAT, a floating IP or a load balancer that is exactly right; " +
		"a record left pointing at an old host looks identical from in here. Confirm it from outside."}
}

// acmePort80Check reports what this process knows about port 80, and no more.
func (s *Server) acmePort80Check() acmeCheck {
	// The one sentence every branch has to carry, because the check that
	// actually decides issuance happens on the far side of the firewall.
	const outside = " Whether the public internet reaches it cannot be established from inside this box: " +
		"run `curl -sS http://<name>/.well-known/acme-challenge/probe` from another network to find out."

	if config.ListenPort(s.cfg.Addr) == "80" {
		return acmeCheck{ID: "port80", Status: acmePass,
			Detail: "This server's own listener is on :80, so an HTTP-01 challenge arrives on it." + outside}
	}

	st := s.httpHelper.Load()
	switch {
	case st == nil:
		return acmeCheck{ID: "port80", Status: acmeUnknown, Detail: "The plain-HTTP companion on :80 runs " +
			"only when this server terminates TLS, and it is not running in this process. It will try to bind " +
			"on the next start, and the startup log says whether it managed to."}
	case st.bound:
		return acmeCheck{ID: "port80", Status: acmePass,
			Detail: "This server holds :80 and answers /.well-known/acme-challenge/ on it." + outside}
	default:
		return acmeCheck{ID: "port80", Status: acmeFail, Detail: "This server could not bind :80: " + st.reason +
			". HTTP-01 validation would have nothing to answer it. Free the port, add " +
			"AmbientCapabilities=CAP_NET_BIND_SERVICE to the unit, or forward 80 to this host. polyemesis also " +
			"advertises TLS-ALPN-01 on the HTTPS listener, so issuance can still succeed over 443 alone — treat " +
			"that as a fallback, not a plan."}
	}
}

// acmeEmailCheck never echoes the address back.
//
// Whether one is set is the whole of what the walkthrough needs, and the
// address itself is a contact detail that no read-scoped token has any reason
// to be handed. It is in config.yaml for anyone who can read config.yaml.
func (s *Server) acmeEmailCheck() acmeCheck {
	if strings.TrimSpace(s.cfg.TLS.ACMEEmail) == "" {
		return acmeCheck{ID: "email", Status: acmeFail, Detail: "tls.acmeEmail is empty. Let's Encrypt " +
			"requires a contact address for expiry warnings, and tls.mode: auto will not resolve to acme " +
			"without one — a box with real DNS and no contact stays self-signed rather than failing issuance " +
			"over and over."}
	}
	return acmeCheck{ID: "email", Status: acmePass, Detail: "A contact address is configured."}
}

// acmeIssuanceCheck is the only check that reports something that already
// happened, so it exists only where something could have. The second return is
// false outside acme mode.
func (s *Server) acmeIssuanceCheck() (acmeCheck, bool) {
	if s.cfg.ResolvedTLSMode() != config.ModeACME || s.tls == nil {
		return acmeCheck{}, false
	}
	if msg, at := s.tls.LastIssuanceError(); msg != "" {
		return acmeCheck{ID: "issuance", Status: acmeFail,
			Detail: "Let's Encrypt refused at " + at.UTC().Format(time.RFC3339) + ": " + msg}, true
	}
	if _, err := s.tls.CertInfo(); err == nil {
		return acmeCheck{ID: "issuance", Status: acmePass,
			Detail: "A certificate is in place, and autocert renews it without a restart."}, true
	}
	return acmeCheck{ID: "issuance", Status: acmeUnknown, Detail: "Nothing has been ordered yet. Issuance " +
		"starts on the first HTTPS request for this name, so open the UI by its name once and come back."}, true
}

// holdsAnyAddress reports whether one of ips is configured on an interface of
// this machine. A false answer is not evidence of anything — see acmeDNSCheck.
func (s *Server) holdsAnyAddress(ips []net.IP) bool {
	addrs, err := s.interfaces()()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		var local net.IP
		switch v := a.(type) {
		case *net.IPNet:
			local = v.IP
		case *net.IPAddr:
			local = v.IP
		default:
			continue
		}
		for _, ip := range ips {
			if local.Equal(ip) {
				return true
			}
		}
	}
	return false
}

// resolver and interfaces are the two seams this file needs to be testable.
// Nil means the real machine, which is what a running server wants; a test
// hands in an answer so the checks can be driven without a network or a
// particular host's addresses.
func (s *Server) resolver() func(context.Context, string) ([]net.IP, error) {
	if s.resolveHost != nil {
		return s.resolveHost
	}
	return func(ctx context.Context, host string) ([]net.IP, error) {
		return net.DefaultResolver.LookupIP(ctx, "ip", host)
	}
}

func (s *Server) interfaces() func() ([]net.Addr, error) {
	if s.localAddrs != nil {
		return s.localAddrs
	}
	return net.InterfaceAddrs
}

// httpHelperStatus is what happened when the plain-HTTP companion on :80 tried
// to bind. cmd/polyemesis owns that attempt; this is how the answer reaches a
// handler.
type httpHelperStatus struct {
	bound  bool
	reason string
}

// SetHTTPHelperStatus records whether the :80 companion is up.
//
// A SETTER RATHER THAN AN OPTION because of the order things start in: the API
// server is built before the listener that decides this, so New cannot be told.
// It is set once, before the mux serves its first request, and read from
// handlers afterwards — the atomic is there so that "before" is a fact about
// the memory model and not just about the wall clock.
func (s *Server) SetHTTPHelperStatus(bound bool, reason string) {
	s.httpHelper.Store(&httpHelperStatus{bound: bound, reason: reason})
}
