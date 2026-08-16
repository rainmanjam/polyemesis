package tlsx

// What Let's Encrypt said when it refused.
//
// Issuance itself still cannot be tested offline (see the note at the top of
// acme_test.go), and none of this tries to. What IS testable is the bookkeeping
// around it: a refusal for our own name is kept, a refusal for somebody else's
// name is not, and a success clears what an earlier refusal left behind. Those
// three are the whole of what the Settings page reads.
//
// Both halves are real code paths rather than fields poked by hand. The failure
// is produced by pointing the ACME directory at a closed port on loopback, so
// autocert fails the way it fails on a box with no route to Let's Encrypt --
// immediately, and without touching anything off this machine. The success is
// produced by seeding autocert's own cache with a certificate for the name,
// which is exactly the state a renewal leaves behind and is served without any
// network at all.

import (
	"context"
	"crypto/tls"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/acme"
)

// offlineACME builds an acme-mode Provider whose orders cannot leave this
// machine. Port 1 on loopback refuses instantly, so the failure arrives in
// milliseconds rather than after a DNS timeout.
func offlineACME(t *testing.T, hostname string) *Provider {
	t.Helper()
	p, err := New(Options{
		Mode:      ModeACME,
		Hostname:  hostname,
		ACMEEmail: "ops@example.com",
		DataDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New(acme): %v", err)
	}
	p.acme.Client = &acme.Client{DirectoryURL: "http://127.0.0.1:1/directory"}
	return p
}

// clientHello names an ECDSA-capable client, which is what decides the key type
// autocert looks for in its cache. Anything else and the seeded certificate in
// the success case is filed under a name the lookup never asks for.
func clientHello(name string) *tls.ClientHelloInfo {
	return &tls.ClientHelloInfo{
		ServerName:   name,
		CipherSuites: []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
	}
}

// seedACMECache writes a usable certificate for hostname into the manager's
// cache, in the layout autocert stores a renewal in: the private key first,
// then the chain.
func seedACMECache(t *testing.T, p *Provider, hostname string) {
	t.Helper()
	dir := t.TempDir()
	if _, err := ensureSelfSigned(dir, hostname, time.Now); err != nil {
		t.Fatalf("minting a certificate to seed the cache with: %v", err)
	}
	key, err := os.ReadFile(filepath.Join(dir, "server.key"))
	if err != nil {
		t.Fatalf("reading the seeded key: %v", err)
	}
	chain, err := os.ReadFile(filepath.Join(dir, "server.crt"))
	if err != nil {
		t.Fatalf("reading the seeded chain: %v", err)
	}
	if err := p.acme.Cache.Put(context.Background(), hostname, append(key, chain...)); err != nil {
		t.Fatalf("seeding the acme cache: %v", err)
	}
}

func TestIssuanceFailureForOurOwnNameIsRemembered(t *testing.T) {
	p := offlineACME(t, "stream.example.com")

	if _, err := p.conf.GetCertificate(clientHello("stream.example.com")); err == nil {
		t.Fatal("issuance succeeded against a closed port, so this test proves nothing")
	}

	got, at := p.LastIssuanceError()
	if got == "" {
		t.Fatal("nothing was recorded about a refused issuance.\n" +
			"This is the whole of what an operator sees today: a certificate that\n" +
			"never appears, with the reason confined to a handshake nobody watched.")
	}
	if at.IsZero() {
		t.Error("the refusal carries no time, so the UI cannot say whether it is from a minute ago or from last month")
	}
	// Not an assertion about autocert's wording -- only that the wording
	// SURVIVES. A message flattened to "issuance failed" would satisfy every
	// other check here and tell the operator nothing.
	if !strings.Contains(got, "127.0.0.1:1") {
		t.Errorf("the recorded error is %q, which no longer carries what actually went wrong", got)
	}
}

func TestIssuanceFailureForSomebodyElsesNameIsNotRecorded(t *testing.T) {
	p := offlineACME(t, "stream.example.com")

	// hostPolicy refuses this before any order is attempted, which is correct
	// and is what keeps a public port from burning this account's rate limit.
	if _, err := p.conf.GetCertificate(clientHello("evil.example.net")); err == nil {
		t.Fatal("the host policy allowed a name this server is not configured as")
	}

	if got, _ := p.LastIssuanceError(); got != "" {
		t.Fatalf("a port scan against this box was recorded as an issuance failure: %q\n"+
			"An operator diagnosing their own certificate would be reading somebody\n"+
			"else's SNI, and every stranger who connects could overwrite the one\n"+
			"message that was about them.", got)
	}
}

func TestIssuanceSuccessClearsAnEarlierRefusal(t *testing.T) {
	const host = "stream.example.com"
	p := offlineACME(t, host)

	// A TLS-ALPN-01 validation connection arriving with no token to answer it.
	// That is a refusal for our own name like any other, and it is the one this
	// test can stage twice over: autocert remembers a FAILED order for a minute
	// and answers the next handshake from that memory, so an order-shaped
	// failure could never be followed by a success inside one test.
	alpn := clientHello(host)
	alpn.SupportedProtos = []string{acme.ALPNProto}
	if _, err := p.conf.GetCertificate(alpn); err == nil {
		t.Fatal("a token certificate was served for a challenge that was never started")
	}
	if got, _ := p.LastIssuanceError(); got == "" {
		t.Fatal("no refusal to clear")
	}

	// The operator fixes what was wrong; the next handshake is served.
	seedACMECache(t, p, host)
	if _, err := p.conf.GetCertificate(clientHello(host)); err != nil {
		t.Fatalf("the seeded certificate was not served: %v", err)
	}

	if got, _ := p.LastIssuanceError(); got != "" {
		t.Fatalf("a fixed problem is still being reported as though it were current: %q\n"+
			"An operator who has already done the work would be sent to look at it again.", got)
	}
}
