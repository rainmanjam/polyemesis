package tlsx

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSelfSignedMaterialIsReusedAcrossRestarts(t *testing.T) {
	dir := t.TempDir()

	first := newSelfSigned(t, dir, "box.local", 0)
	firstCA := first.CAFingerprint()
	firstLeaf := mustCertInfo(t, first).Fingerprint

	// A restart a week later must present the identical certificate: a box
	// that reissues on every boot is a box whose users re-approve a browser
	// warning on every boot.
	second := newSelfSigned(t, dir, "box.local", 7*24*time.Hour)
	if got := second.CAFingerprint(); got != firstCA {
		t.Errorf("CA fingerprint changed across a restart:\n first  %s\n second %s", firstCA, got)
	}
	if got := mustCertInfo(t, second).Fingerprint; got != firstLeaf {
		t.Errorf("leaf fingerprint changed across a restart:\n first  %s\n second %s", firstLeaf, got)
	}
}

func TestPrivateKeysAreWrittenUnreadableToEveryoneElse(t *testing.T) {
	dir := t.TempDir()
	newSelfSigned(t, dir, "box.local", 0)

	tests := []struct {
		file string
		want os.FileMode
	}{
		{caKeyFile, 0o600},
		{serverKeyFile, 0o600},
		{caCertFile, 0o644},
		{serverCertFile, 0o644},
	}
	for _, tc := range tests {
		path := filepath.Join(Dir(dir), tc.file)
		st, err := os.Stat(path)
		if err != nil {
			t.Errorf("stat %s: %v", tc.file, err)
			continue
		}
		if got := st.Mode().Perm(); got != tc.want {
			t.Errorf("%s mode = %#o, want %#o", tc.file, got, tc.want)
		}
	}

	if st, err := os.Stat(Dir(dir)); err != nil {
		t.Errorf("stat %s: %v", Dir(dir), err)
	} else if got := st.Mode().Perm(); got != 0o700 {
		t.Errorf("%s mode = %#o, want %#o", Dir(dir), got, 0o700)
	}
}

func TestLeafIsRenewedNearExpiryWhileTheInstalledCASurvives(t *testing.T) {
	dir := t.TempDir()

	first := newSelfSigned(t, dir, "box.local", 0)
	firstCA := first.CAFingerprint()
	firstLeaf := mustCertInfo(t, first).Fingerprint

	tests := []struct {
		name        string
		after       time.Duration
		wantRenewed bool
	}{
		{"comfortably inside the leaf lifetime", 300 * 24 * time.Hour, false},
		{"one day outside the renewal window", leafValidity - renewWindow - 24*time.Hour, false},
		{"inside the renewal window", leafValidity - renewWindow + time.Hour, true},
		{"after the leaf has expired", leafValidity + 24*time.Hour, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Each case starts from the same freshly minted material.
			sub := t.TempDir()
			copyDir(t, Dir(dir), Dir(sub))

			p := newSelfSigned(t, sub, "box.local", tc.after)
			renewed := mustCertInfo(t, p).Fingerprint != firstLeaf
			if renewed != tc.wantRenewed {
				t.Errorf("leaf renewed = %v at +%v, want %v", renewed, tc.after, tc.wantRenewed)
			}
			if got := p.CAFingerprint(); got != firstCA {
				t.Errorf("CA fingerprint changed on leaf renewal:\n was %s\n now %s", firstCA, got)
			}
		})
	}
}

func TestCAIsReplacedOnlyWhenItIsItselfNearExpiry(t *testing.T) {
	dir := t.TempDir()
	first := newSelfSigned(t, dir, "box.local", 0)
	firstCA := first.CAFingerprint()

	late := newSelfSigned(t, dir, "box.local", caValidity-renewWindow+time.Hour)
	if late.CAFingerprint() == firstCA {
		t.Error("the CA was not replaced despite being inside its renewal window")
	}
	// A new CA means the old leaf can no longer chain, so it must be reissued
	// under the new one or clients see a broken chain.
	info := mustCertInfo(t, late)
	if info.Issuer != subjectOfCA(t, late) {
		t.Errorf("leaf issuer = %q, want the new CA subject %q", info.Issuer, subjectOfCA(t, late))
	}
}

func TestLeafAlwaysCarriesLocalhostAndLoopbackSANs(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		wantDNS  []string
		wantIPs  []string
	}{
		{
			name:     "a LAN hostname is the primary name",
			hostname: "box.local",
			wantDNS:  []string{"box.local", "localhost"},
			wantIPs:  []string{"127.0.0.1", "::1"},
		},
		{
			name:     "a public FQDN is the primary name",
			hostname: "stream.example.com",
			wantDNS:  []string{"stream.example.com", "localhost"},
			wantIPs:  []string{"127.0.0.1", "::1"},
		},
		{
			name:     "an unset hostname still yields a usable local certificate",
			hostname: "",
			wantDNS:  []string{"localhost"},
			wantIPs:  []string{"127.0.0.1", "::1"},
		},
		{
			name:     "a LAN IP is a SAN, not a DNS name",
			hostname: "192.168.1.50",
			wantDNS:  []string{"localhost"},
			wantIPs:  []string{"127.0.0.1", "::1", "192.168.1.50"},
		},
		{
			name:     "loopback is not duplicated",
			hostname: "127.0.0.1",
			wantDNS:  []string{"localhost"},
			wantIPs:  []string{"127.0.0.1", "::1"},
		},
		{
			name:     "localhost is not duplicated",
			hostname: "localhost",
			wantDNS:  []string{"localhost"},
			wantIPs:  []string{"127.0.0.1", "::1"},
		},
		{
			name:     "a trailing dot is trimmed",
			hostname: "box.local.",
			wantDNS:  []string{"box.local", "localhost"},
			wantIPs:  []string{"127.0.0.1", "::1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newSelfSigned(t, t.TempDir(), tc.hostname, 0)
			info := mustCertInfo(t, p)
			if strings.Join(info.DNSNames, ",") != strings.Join(tc.wantDNS, ",") {
				t.Errorf("DNSNames = %v, want %v", info.DNSNames, tc.wantDNS)
			}
			if strings.Join(info.IPAddresses, ",") != strings.Join(tc.wantIPs, ",") {
				t.Errorf("IPAddresses = %v, want %v", info.IPAddresses, tc.wantIPs)
			}
		})
	}
}

func TestLeafIsReissuedWhenTheOperatorChangesTheHostname(t *testing.T) {
	dir := t.TempDir()

	first := newSelfSigned(t, dir, "old.local", 0)
	firstCA := first.CAFingerprint()

	// Same day, same material on disk, different tls.hostname: serving the old
	// certificate would give a name mismatch warning on the new address.
	second := newSelfSigned(t, dir, "new.local", 0)
	info := mustCertInfo(t, second)
	if len(info.DNSNames) == 0 || info.DNSNames[0] != "new.local" {
		t.Errorf("DNSNames = %v, want the new hostname first", info.DNSNames)
	}
	if second.CAFingerprint() != firstCA {
		t.Error("changing the hostname replaced the CA; only the leaf needs reissuing")
	}
}

func TestSelfSignedLeafIsReportedAsUntrustedEvenThoughACASignedIt(t *testing.T) {
	p := newSelfSigned(t, t.TempDir(), "box.local", 0)
	info := mustCertInfo(t, p)
	if !info.SelfSigned {
		t.Error("SelfSigned = false; the UI would not warn that a browser will reject this certificate")
	}
}

func TestServedChainIncludesTheCASoClientsCanBuildTheChain(t *testing.T) {
	dir := t.TempDir()
	p := newSelfSigned(t, dir, "box.local", 0)

	chain := p.TLSConfig().Certificates[0].Certificate
	if len(chain) != 2 {
		t.Fatalf("served chain has %d certificates, want the leaf plus the CA", len(chain))
	}
	ca, err := x509.ParseCertificate(chain[1])
	if err != nil {
		t.Fatalf("parse the CA from the served chain: %v", err)
	}
	if !ca.IsCA {
		t.Error("the second certificate in the served chain is not a CA")
	}
	if fingerprint(ca.Raw) != p.CAFingerprint() {
		t.Error("the CA in the served chain is not the one CAFingerprint reports")
	}
}

func TestCAIsAMultiYearAuthorityAndTheLeafIsAboutAYear(t *testing.T) {
	dir := t.TempDir()
	p := newSelfSigned(t, dir, "box.local", 0)

	caPEM := p.CACertificatePEM()
	ca, err := parseFirstCertificate(caPEM)
	if err != nil {
		t.Fatalf("parse the CA: %v", err)
	}
	if life := ca.NotAfter.Sub(ca.NotBefore); life < 5*365*24*time.Hour {
		t.Errorf("CA lifetime = %v, want multiple years so the user installs it once", life)
	}
	if !ca.IsCA || ca.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("the CA cannot sign certificates")
	}

	info := mustCertInfo(t, p)
	if life := info.NotAfter.Sub(info.NotBefore); life > 400*24*time.Hour {
		t.Errorf("leaf lifetime = %v, want roughly a year", life)
	}
}

func TestLeafIsUsableForServerAuthenticationAndChainsToTheCA(t *testing.T) {
	dir := t.TempDir()
	p := newSelfSigned(t, dir, "box.local", 0)

	ca, err := parseFirstCertificate(p.CACertificatePEM())
	if err != nil {
		t.Fatalf("parse the CA: %v", err)
	}
	leaf := p.leaf

	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{
		DNSName:     "box.local",
		Roots:       roots,
		CurrentTime: baseTime,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Errorf("the leaf does not verify against its own CA: %v", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		DNSName:     "localhost",
		Roots:       roots,
		CurrentTime: baseTime,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Errorf("the leaf does not verify for localhost: %v", err)
	}
}

func TestAKeyMissingItsCertificateIsReportedRatherThanOverwritten(t *testing.T) {
	dir := t.TempDir()
	newSelfSigned(t, dir, "box.local", 0)

	// A CA certificate with no key can still be installed in browsers, so
	// blowing it away silently would break trust the user already established.
	if err := os.Remove(filepath.Join(Dir(dir), caKeyFile)); err != nil {
		t.Fatal(err)
	}
	_, err := New(Options{Mode: ModeSelfSigned, Hostname: "box.local", DataDir: dir, now: at(0)})
	if err == nil {
		t.Fatal("New succeeded with a CA certificate whose key is gone, want an error")
	}
	if !strings.Contains(err.Error(), caKeyFile) {
		t.Errorf("error = %q, want it to name the missing key", err)
	}
}

func TestSANCoverageCheckIsCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	first := newSelfSigned(t, dir, "Box.Local", 0)
	firstLeaf := mustCertInfo(t, first).Fingerprint

	// DNS names are case-insensitive; reissuing over a case difference would
	// churn the certificate on every restart if the operator ever retyped it.
	second := newSelfSigned(t, dir, "box.local", 0)
	if got := mustCertInfo(t, second).Fingerprint; got != firstLeaf {
		t.Error("the leaf was reissued for a hostname that differs only in case")
	}
}

// mustCertInfo fetches CertInfo or fails the test.
func mustCertInfo(t *testing.T, p *Provider) CertInfo {
	t.Helper()
	info, err := p.CertInfo()
	if err != nil {
		t.Fatalf("CertInfo(): %v", err)
	}
	return info
}

func subjectOfCA(t *testing.T, p *Provider) string {
	t.Helper()
	ca, err := parseFirstCertificate(p.CACertificatePEM())
	if err != nil {
		t.Fatalf("parse the CA: %v", err)
	}
	return ca.Subject.String()
}

// copyDir clones a directory of TLS material, preserving permissions, so a
// table test can branch from one starting state.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, dirPerm); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, info.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
	}
}
