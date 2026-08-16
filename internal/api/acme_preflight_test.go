package api

// The Let's Encrypt walkthrough, driven.
//
// Every test in here is about the same thing: an operator finds out what would
// happen BEFORE the restart, rather than after it. The restart is the expensive
// part -- a self-signed server that was working becomes a server with no
// certificate at all, and Let's Encrypt's failure limit means the second
// attempt may be an hour away -- so a check that is wrong, or that claims to
// know something it cannot, costs more than no check.
//
// Which is why several of these assert an "unknown" rather than a verdict. The
// preflight is allowed to be unsure; it is not allowed to be confidently wrong.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/acme"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/tlsx"
)

// resolvesTo answers every lookup with the same addresses, so a test can stage
// "the record points here" and "the record points somewhere else" without a
// resolver, a network or a particular machine's interfaces.
func resolvesTo(addrs ...string) func(context.Context, string) ([]net.IP, error) {
	return func(context.Context, string) ([]net.IP, error) {
		out := make([]net.IP, 0, len(addrs))
		for _, a := range addrs {
			out = append(out, net.ParseIP(a))
		}
		return out, nil
	}
}

func holds(addrs ...string) func() ([]net.Addr, error) {
	return func() ([]net.Addr, error) {
		out := make([]net.Addr, 0, len(addrs))
		for _, a := range addrs {
			out = append(out, &net.IPAddr{IP: net.ParseIP(a)})
		}
		return out, nil
	}
}

func runPreflight(t *testing.T, s *Server, query string) acmePreflight {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleACMEPreflight(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tls/acme-preflight"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got acmePreflight
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding the response: %v", err)
	}
	return got
}

func check(t *testing.T, p acmePreflight, id string) acmeCheck {
	t.Helper()
	for _, c := range p.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no %q check in the preflight; the walkthrough would render a gap where a prerequisite belongs", id)
	return acmeCheck{}
}

func TestACMEPreflightRefusesNamesNoPublicCAWillIssueFor(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{"a public name", "stream.example.com", acmePass},
		{"nothing configured at all", "", acmeFail},
		{"a lan suffix", "polyemesis.lan", acmeFail},
		{"an mdns name", "nas.local", acmeFail},
		{"a bare host with no dot", "polyemesis", acmeFail},
		{"an address rather than a name", "192.168.1.10", acmeFail},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{
				cfg:         config.Config{TLS: config.TLS{Mode: config.ModeSelfSigned}},
				resolveHost: resolvesTo("203.0.113.5"),
				localAddrs:  holds("203.0.113.5"),
			}
			got := check(t, runPreflight(t, s, "?hostname="+tc.host), "name")
			if got.Status != tc.want {
				t.Fatalf("name check for %q = %q, want %q.\ndetail: %s\n"+
					"A wrong verdict here sends the operator to restart into a mode that\n"+
					"cannot work, or talks them out of one that can.", tc.host, got.Status, tc.want, got.Detail)
			}
			if tc.want == acmeFail && !strings.Contains(got.Detail, "hostname") && !strings.Contains(got.Detail, tc.host) {
				t.Errorf("the refusal neither names %q nor says which setting holds it: %s", tc.host, got.Detail)
			}
		})
	}
}

func TestACMEPreflightLooksUpNothingItCannotIssueFor(t *testing.T) {
	// A private name is settled by its spelling. Resolving it anyway would put
	// this operator's internal hostnames through whatever resolver the box
	// uses, to answer a question already answered.
	s := &Server{
		cfg: config.Config{TLS: config.TLS{Mode: config.ModeSelfSigned}},
		resolveHost: func(context.Context, string) ([]net.IP, error) {
			t.Error("the preflight resolved a name that can never receive a public certificate")
			return nil, nil
		},
	}
	if got := check(t, runPreflight(t, s, "?hostname=polyemesis.lan"), "dns"); got.Status != acmeUnknown {
		t.Fatalf("dns check = %q, want %q for a name with nothing to look up", got.Status, acmeUnknown)
	}
}

func TestACMEPreflightSaysSoWhenTheNameDoesNotResolve(t *testing.T) {
	s := &Server{
		cfg: config.Config{TLS: config.TLS{Mode: config.ModeSelfSigned}},
		resolveHost: func(context.Context, string) ([]net.IP, error) {
			return nil, &net.DNSError{Err: "no such host", Name: "stream.example.com", IsNotFound: true}
		},
	}
	p := runPreflight(t, s, "?hostname=stream.example.com")
	got := check(t, p, "dns")
	if got.Status != acmeFail {
		t.Fatalf("dns check = %q for a name with no record, want %q.\n"+
			"This is the failure Let's Encrypt reports as an unauthorized\n"+
			"authorization twenty seconds after a restart, by which time the\n"+
			"certificate the operator had is gone.", got.Status, acmeFail)
	}
	if p.Ready {
		t.Error("the preflight reports ready with a name that resolves nowhere")
	}
}

func TestACMEPreflightWillNotCallAnOffHostRecordWrong(t *testing.T) {
	// Behind NAT, a floating IP or an ELB, the public record points at
	// something that is not an interface on this box AND the deployment is
	// completely correct. From in here the two are the same picture.
	s := &Server{
		// A contact address, so the only unsettled row below is the one this
		// test is about.
		cfg: config.Config{Addr: ":80", TLS: config.TLS{
			Mode: config.ModeSelfSigned, ACMEEmail: "ops@example.com",
		}},
		resolveHost: resolvesTo("203.0.113.5"),
		localAddrs:  holds("10.0.0.4"),
	}
	p := runPreflight(t, s, "?hostname=stream.example.com")
	got := check(t, p, "dns")
	if got.Status != acmeUnknown {
		t.Fatalf("dns check = %q, want %q.\n"+
			"A fail here tells every NAT deployment -- which is most homelabs and\n"+
			"every cloud instance with an elastic IP -- that a correct record is\n"+
			"broken, and a pass claims proof this process does not have.", got.Status, acmeUnknown)
	}
	if !strings.Contains(got.Detail, "203.0.113.5") {
		t.Errorf("the detail does not say where the name actually points, which is the one fact that helps: %s", got.Detail)
	}
	if !p.Ready {
		t.Error("an unanswerable question blocked readiness; the operator has no way to clear it from in here")
	}
}

func TestACMEPreflightReportsAFailedPortEightyBind(t *testing.T) {
	s := &Server{
		cfg:         config.Config{Addr: ":8443", TLS: config.TLS{Mode: config.ModeSelfSigned}},
		resolveHost: resolvesTo("203.0.113.5"),
		localAddrs:  holds("203.0.113.5"),
	}
	s.SetHTTPHelperStatus(false, "listen tcp :80: bind: permission denied")

	p := runPreflight(t, s, "?hostname=stream.example.com")
	got := check(t, p, "port80")
	if got.Status != acmeFail {
		t.Fatalf("port80 check = %q with nothing bound to :80, want %q", got.Status, acmeFail)
	}
	// The remedy, not just the diagnosis: an unprivileged service told to use
	// port 80 and not told how meets "permission denied" a second time.
	for _, want := range []string{"permission denied", "CAP_NET_BIND_SERVICE"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("the detail never mentions %q, so it cannot be acted on: %s", want, got.Detail)
		}
	}
	if p.Ready {
		t.Error("the preflight reports ready with no listener on :80")
	}
}

func TestACMEPreflightSeparatesAFailedBindFromAnUnattemptedOne(t *testing.T) {
	// tls.mode: off never starts the :80 companion at all, and the operator on
	// plain HTTP is the one this walkthrough exists for. Reporting their
	// port 80 as broken would be a fabrication.
	s := &Server{
		cfg:         config.Config{Addr: ":8080", TLS: config.TLS{Mode: config.ModeOff}},
		resolveHost: resolvesTo("203.0.113.5"),
		localAddrs:  holds("203.0.113.5"),
	}
	got := check(t, runPreflight(t, s, "?hostname=stream.example.com"), "port80")
	if got.Status != acmeUnknown {
		t.Fatalf("port80 check = %q in a process that never tried to bind it, want %q.\ndetail: %s",
			got.Status, acmeUnknown, got.Detail)
	}
}

func TestACMEPreflightNeverEchoesTheContactAddress(t *testing.T) {
	s := &Server{
		cfg: config.Config{TLS: config.TLS{
			Mode: config.ModeSelfSigned, ACMEEmail: "ops@example.com",
		}},
		resolveHost: resolvesTo("203.0.113.5"),
		localAddrs:  holds("203.0.113.5"),
	}
	p := runPreflight(t, s, "?hostname=stream.example.com")
	if got := check(t, p, "email"); got.Status != acmePass {
		t.Fatalf("email check = %q with tls.acmeEmail set, want %q", got.Status, acmePass)
	}
	body, _ := json.Marshal(p)
	if strings.Contains(string(body), "ops@example.com") {
		t.Errorf("the preflight handed the configured contact address back to the caller:\n%s\n"+
			"Whether one is SET is the whole of what the walkthrough needs, and a\n"+
			"read-scoped token has no reason to be given a person's address.", body)
	}
}

func TestACMEPreflightSurfacesTheLastRefusalFromLetsEncrypt(t *testing.T) {
	const host = "stream.example.com"
	dir := t.TempDir()
	p, err := tlsx.New(tlsx.Options{
		Mode: tlsx.ModeACME, Hostname: host, ACMEEmail: "ops@example.com", DataDir: dir,
	})
	if err != nil {
		t.Fatalf("tlsx.New(acme): %v", err)
	}
	// A TLS-ALPN-01 validation connection with no challenge in flight: a real
	// refusal for this server's own name, produced without leaving the machine.
	if _, err := p.TLSConfig().GetCertificate(&tls.ClientHelloInfo{
		ServerName: host, SupportedProtos: []string{acme.ALPNProto},
	}); err == nil {
		t.Fatal("the handshake succeeded, so there is no refusal to report")
	}

	s := &Server{
		cfg: config.Config{DataDir: dir, TLS: config.TLS{
			Mode: config.ModeACME, Hostname: host, ACMEEmail: "ops@example.com",
		}},
		tls:         p,
		resolveHost: resolvesTo("203.0.113.5"),
		localAddrs:  holds("203.0.113.5"),
	}
	got := check(t, runPreflight(t, s, ""), "issuance")
	if got.Status != acmeFail {
		t.Fatalf("issuance check = %q after a refused handshake, want %q", got.Status, acmeFail)
	}
	if !strings.Contains(got.Detail, "no token cert") {
		t.Errorf("the check does not carry what Let's Encrypt's own side of the exchange said, which is\n"+
			"the sentence an operator can search for: %s", got.Detail)
	}
}

func TestACMEPreflightHasNoIssuanceCheckOutsideACMEMode(t *testing.T) {
	// Nothing has been ordered and nothing could have been. A row saying so
	// reads as a fault on a server that is working exactly as configured.
	//
	// The provider is REAL and holds a certificate, so the absence below is the
	// mode being consulted rather than there being nothing to report.
	s := selfSignedServer(t, config.Config{TLS: config.TLS{Mode: config.ModeSelfSigned}})
	s.resolveHost = resolvesTo("203.0.113.5")
	s.localAddrs = holds("203.0.113.5")
	for _, c := range runPreflight(t, s, "?hostname=stream.example.com").Checks {
		if c.ID == "issuance" {
			t.Fatalf("a selfsigned server reports on an issuance that never happened: %s", c.Detail)
		}
	}
}

func TestACMEPreflightPrefersTheAskedForNameOverTheConfiguredOne(t *testing.T) {
	// The operator is trying out a name they have not committed to config.yaml
	// yet, which is the entire point of checking before restarting.
	s := &Server{
		cfg: config.Config{TLS: config.TLS{
			Mode: config.ModeSelfSigned, Hostname: "polyemesis.lan",
		}},
		resolveHost: resolvesTo("203.0.113.5"),
		localAddrs:  holds("203.0.113.5"),
	}
	p := runPreflight(t, s, "?hostname=stream.example.com")
	if p.Hostname != "stream.example.com" {
		t.Fatalf("checked %q, want the name that was asked for", p.Hostname)
	}
	if got := check(t, p, "name"); got.Status != acmePass {
		t.Fatalf("name check = %q; the configured name was used instead of the asked-for one", got.Status)
	}

	// And with nothing asked for, the configured name is still what gets
	// checked -- otherwise the panel is blank on the page that opens it.
	if p := runPreflight(t, s, ""); p.Hostname != "polyemesis.lan" {
		t.Fatalf("with no hostname parameter the preflight checked %q, want the configured name", p.Hostname)
	}
}

func TestACMEPreflightRefusesAHostnameLongerThanADNSName(t *testing.T) {
	s := &Server{
		cfg: config.Config{TLS: config.TLS{Mode: config.ModeSelfSigned}},
		resolveHost: func(context.Context, string) ([]net.IP, error) {
			t.Error("a string too long to be a DNS name reached the resolver")
			return nil, nil
		},
	}
	rec := httptest.NewRecorder()
	long := strings.Repeat("a", 250) + ".example.com"
	s.handleACMEPreflight(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tls/acme-preflight?hostname="+long, nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d for a %d-character hostname, want 400", rec.Code, len(long))
	}
}
