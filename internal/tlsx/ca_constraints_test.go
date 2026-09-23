package tlsx

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// forgeLeaf does what anyone holding a copy of ca.key could do: sign a
// server certificate for a name of their choosing. The CA is read back from
// disk rather than from the Provider, because disk is where the attacker
// finds it.
func forgeLeaf(t *testing.T, dataDir string, dnsNames []string, ips []net.IP) (*x509.Certificate, *x509.CertPool) {
	t.Helper()
	caCert, caKey, _, err := loadCA(Dir(dataDir))
	if err != nil || caCert == nil {
		t.Fatalf("load the CA from disk: cert=%v err=%v", caCert, err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := newSerial()
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "forged"},
		NotBefore:    baseTime.Add(-time.Hour),
		NotAfter:     baseTime.Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("sign the forged leaf: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	return leaf, roots
}

func verifyFor(leaf *x509.Certificate, roots *x509.CertPool, name string) error {
	_, err := leaf.Verify(x509.VerifyOptions{
		DNSName:     name,
		Roots:       roots,
		CurrentTime: baseTime,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	return err
}

// The operator installs this CA into the trust store of the laptop they run
// their bank and their mail from, and its key sits on an internet-facing box.
// Without name constraints, whoever reads ca.key -- a backup, a shell through
// expert mode, a stolen disk -- can mint a certificate for any site that
// laptop will believe. Constrained, the worst they can mint is this box.
func TestTheLocalCACannotVouchForAnyOtherSite(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		forged   string // a name the CA must refuse
		forgedIP string // an address the CA must refuse
		allowed  []string
	}{
		{"a DNS hostname", "box.local", "bank.example", "93.184.216.34", []string{"box.local", "localhost", "127.0.0.1", "::1"}},
		{"an IP literal", "192.0.2.10", "mail.example", "192.0.2.11", []string{"192.0.2.10", "localhost", "127.0.0.1"}},
		{"an IPv6 literal", "2001:db8::10", "mail.example", "2001:db8::11", []string{"2001:db8::10", "::1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := newSelfSigned(t, dir, tc.hostname, 0)

			ca, err := parseFirstCertificate(p.CACertificatePEM())
			if err != nil {
				t.Fatal(err)
			}
			// Critical, so a client that does not understand the extension
			// refuses the CA outright instead of ignoring the limit.
			if !ca.PermittedDNSDomainsCritical {
				t.Error("the CA's name constraints are not marked critical")
			}

			leaf, roots := forgeLeaf(t, dir, []string{tc.forged}, nil)
			if err := verifyFor(leaf, roots, tc.forged); err == nil {
				t.Errorf("a certificate for %s signed with ca.key verifies against the local CA", tc.forged)
			}
			ipLeaf, roots := forgeLeaf(t, dir, nil, []net.IP{net.ParseIP(tc.forgedIP)})
			if err := verifyFor(ipLeaf, roots, tc.forgedIP); err == nil {
				t.Errorf("a certificate for %s signed with ca.key verifies against the local CA", tc.forgedIP)
			}

			// ...while the box's own certificate still verifies for every
			// name it is actually reached by.
			for _, name := range tc.allowed {
				if err := verifyFor(p.leaf, roots, name); err != nil {
					t.Errorf("the served certificate no longer verifies for %s: %v", name, err)
				}
			}
		})
	}
}

// writeUnconstrainedCA puts down a CA exactly as releases before name
// constraints minted it, so the upgrade path is tested against the real
// shape an existing install has on disk.
func writeUnconstrainedCA(t *testing.T, dataDir string) string {
	t.Helper()
	dir := Dir(dataDir)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := newSerial()
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "polyemesis local CA", Organization: []string{"polyemesis"}},
		NotBefore:             baseTime.Add(-time.Hour),
		NotAfter:              baseTime.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := encodeECKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, caKeyFile), keyPEM, keyPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, caCertFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), certPerm); err != nil {
		t.Fatal(err)
	}
	return string(keyPEM)
}

// An install upgraded from a release that minted unconstrained CAs already has
// one on disk, and every day it stays there is a day its key can sign for
// anything. It is replaced on the first start, its key overwritten, and the
// Provider says so, so the startup log can tell the operator to re-trust.
func TestAnUnconstrainedCAFromAnOlderReleaseIsReplacedOnUpgrade(t *testing.T) {
	dir := t.TempDir()
	oldKey := writeUnconstrainedCA(t, dir)

	p := newSelfSigned(t, dir, "box.local", 0)

	ca, err := parseFirstCertificate(p.CACertificatePEM())
	if err != nil {
		t.Fatal(err)
	}
	if !ca.PermittedDNSDomainsCritical || len(ca.PermittedDNSDomains) == 0 {
		t.Error("the upgraded install is still serving under an unconstrained CA")
	}
	got, err := os.ReadFile(filepath.Join(Dir(dir), caKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == oldKey {
		t.Error("the old, unconstrained CA key is still on disk")
	}
	if reason := p.CAReplaced(); !strings.Contains(reason, "any site") {
		t.Errorf("CAReplaced() = %q, want it to say the old CA could vouch for any site", reason)
	}
	// The leaf moved to the new CA with it.
	if err := p.leaf.CheckSignatureFrom(ca); err != nil {
		t.Errorf("the leaf was not reissued under the new CA: %v", err)
	}

	// And it happens once: the next start keeps the constrained CA.
	again := newSelfSigned(t, dir, "box.local", 24*time.Hour)
	if again.CAFingerprint() != p.CAFingerprint() {
		t.Error("the constrained CA was replaced again on the following start")
	}
	if r := again.CAReplaced(); r != "" {
		t.Errorf("CAReplaced() = %q on an ordinary restart, want empty", r)
	}
}

func TestAFreshInstallDoesNotReportAReplacedCA(t *testing.T) {
	p := newSelfSigned(t, t.TempDir(), "box.local", 0)
	if r := p.CAReplaced(); r != "" {
		t.Errorf("CAReplaced() = %q on a first start, want empty: there is nothing to re-trust yet", r)
	}
}

func TestAnExpiringCAIsReportedAsReplaced(t *testing.T) {
	dir := t.TempDir()
	newSelfSigned(t, dir, "box.local", 0)
	late := newSelfSigned(t, dir, "box.local", caValidity-renewWindow+time.Hour)
	if r := late.CAReplaced(); !strings.Contains(r, "expire") {
		t.Errorf("CAReplaced() = %q, want it to say the old CA was expiring", r)
	}
}

// Case-only differences must not churn the CA: that would force a re-trust
// every time the operator retyped the hostname.
func TestConstraintMatchIsCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	first := newSelfSigned(t, dir, "Box.Local", 0)
	second := newSelfSigned(t, dir, "box.local", 0)
	if first.CAFingerprint() != second.CAFingerprint() {
		t.Error("the CA was replaced for a hostname that differs only in case")
	}
}

func TestConstraintsMatchComparesEverySet(t *testing.T) {
	dns, nets := caConstraints(sansFor("box.local"))
	ca := &x509.Certificate{PermittedDNSDomainsCritical: true, PermittedDNSDomains: dns, PermittedIPRanges: nets}
	if !constraintsMatch(ca, dns, nets) {
		t.Fatal("a CA does not match the constraints it was built from")
	}

	otherDNS, otherNets := caConstraints(sansFor("192.0.2.10"))
	tests := []struct {
		name string
		ca   *x509.Certificate
	}{
		{"not critical", &x509.Certificate{PermittedDNSDomains: dns, PermittedIPRanges: nets}},
		{"different names", &x509.Certificate{PermittedDNSDomainsCritical: true, PermittedDNSDomains: otherDNS, PermittedIPRanges: nets}},
		{"different addresses", &x509.Certificate{PermittedDNSDomainsCritical: true, PermittedDNSDomains: dns, PermittedIPRanges: otherNets}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if constraintsMatch(tc.ca, dns, nets) {
				t.Error("constraintsMatch = true, want false")
			}
		})
	}
}
