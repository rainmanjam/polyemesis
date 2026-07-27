package tlsx

// ACME issuance itself is not tested here and cannot be: it needs a reachable
// public name, a live directory endpoint and a CA that can call back. What is
// testable offline — and is what actually goes wrong in practice — is the host
// policy, the cache location and how a missing certificate is reported. Those
// are covered below; issuance is covered by scripts/acceptance.sh against a
// real deployment.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/acme/autocert"
)

func TestHostPolicyPinsIssuanceToExactlyTheConfiguredName(t *testing.T) {
	policy := hostPolicy("stream.example.com")

	tests := []struct {
		name    string
		host    string
		wantErr bool
	}{
		{"the configured name", "stream.example.com", false},
		{"a different case of it", "STREAM.example.COM", false},
		{"an absolute form of it", "stream.example.com.", false},
		{"a name with surrounding space", " stream.example.com ", false},
		{"another host entirely", "evil.example.net", true},
		{"a subdomain of it", "sub.stream.example.com", true},
		{"its parent domain", "example.com", true},
		{"a prefix of it", "stream.example.co", true},
		{"an empty SNI", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := policy(context.Background(), tc.host)
			if (err != nil) != tc.wantErr {
				t.Fatalf("policy(%q) error = %v, wantErr %v", tc.host, err, tc.wantErr)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "stream.example.com") {
				t.Errorf("error = %q, want it to name the host this server is configured as", err)
			}
		})
	}
}

func TestHostPolicyIsNeverAllowAny(t *testing.T) {
	p, err := New(Options{Mode: ModeACME, Hostname: "stream.example.com", ACMEEmail: "ops@example.com", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New(acme): %v", err)
	}
	if p.acme.HostPolicy == nil {
		t.Fatal("HostPolicy is nil; autocert would request a certificate for any SNI that arrives")
	}
	// One unexpected SNI per second is enough to burn the account's rate limit
	// for the whole box, so this must fail closed.
	if err := p.acme.HostPolicy(context.Background(), "attacker.example.net"); err == nil {
		t.Error("HostPolicy accepted an unconfigured host")
	}
}

func TestACMECacheLivesUnderTheDataDirAndIsPrivate(t *testing.T) {
	dir := t.TempDir()

	if got, want := ACMECacheDir(dir), filepath.Join(dir, "tls", "acme"); got != want {
		t.Errorf("ACMECacheDir(%q) = %q, want %q", dir, got, want)
	}

	p, err := New(Options{Mode: ModeACME, Hostname: "stream.example.com", ACMEEmail: "ops@example.com", DataDir: dir})
	if err != nil {
		t.Fatalf("New(acme): %v", err)
	}
	cache, ok := p.acme.Cache.(autocert.DirCache)
	if !ok {
		t.Fatalf("Cache is %T, want a DirCache under the data dir so a restart does not re-issue", p.acme.Cache)
	}
	if string(cache) != ACMECacheDir(dir) {
		t.Errorf("cache dir = %q, want %q", string(cache), ACMECacheDir(dir))
	}

	// The cache holds the ACME account key and every issued private key.
	st, err := os.Stat(ACMECacheDir(dir))
	if err != nil {
		t.Fatalf("stat the cache dir: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o700 {
		t.Errorf("cache dir mode = %#o, want %#o", got, 0o700)
	}
}

func TestACMEAccountContactIsCarriedThrough(t *testing.T) {
	p, err := New(Options{Mode: ModeACME, Hostname: "stream.example.com", ACMEEmail: " ops@example.com ", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New(acme): %v", err)
	}
	if p.acme.Email != "ops@example.com" {
		t.Errorf("Email = %q, want the trimmed contact address", p.acme.Email)
	}
	if p.acme.Prompt == nil {
		t.Error("Prompt is nil; autocert refuses to accept the CA terms and never issues")
	}
}

func TestACMEReportsNoCertificateBeforeTheFirstIssuance(t *testing.T) {
	p, err := New(Options{Mode: ModeACME, Hostname: "stream.example.com", ACMEEmail: "ops@example.com", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New(acme): %v", err)
	}
	if _, err := p.CertInfo(); err != ErrNoCertificate {
		t.Errorf("CertInfo() error = %v, want ErrNoCertificate so the UI can say \"waiting for issuance\"", err)
	}
}

func TestACMECertInfoReadsTheCachedCertificateWithoutTouchingTheKey(t *testing.T) {
	dir := t.TempDir()

	// autocert caches key-then-certificate in one blob. Build a stand-in the
	// same shape, since real issuance cannot happen offline.
	fixture := t.TempDir()
	source := newSelfSigned(t, fixture, "stream.example.com", 0)
	keyPEM, err := os.ReadFile(filepath.Join(Dir(fixture), serverKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := os.ReadFile(filepath.Join(Dir(fixture), serverCertFile))
	if err != nil {
		t.Fatal(err)
	}
	blob := append(append([]byte{}, keyPEM...), certPEM...)

	p, err := New(Options{Mode: ModeACME, Hostname: "stream.example.com", ACMEEmail: "ops@example.com", DataDir: dir, now: at(0)})
	if err != nil {
		t.Fatalf("New(acme): %v", err)
	}
	if err := p.acme.Cache.Put(context.Background(), "stream.example.com", blob); err != nil {
		t.Fatalf("seed the cache: %v", err)
	}

	info, err := p.CertInfo()
	if err != nil {
		t.Fatalf("CertInfo(): %v", err)
	}
	if info.Fingerprint != mustCertInfo(t, source).Fingerprint {
		t.Error("CertInfo() did not describe the cached certificate")
	}
	if len(info.DNSNames) == 0 || info.DNSNames[0] != "stream.example.com" {
		t.Errorf("DNSNames = %v, want the issued name", info.DNSNames)
	}
}

func TestACMECacheReadReportsCorruptionInsteadOfPanicking(t *testing.T) {
	dir := t.TempDir()
	p, err := New(Options{Mode: ModeACME, Hostname: "stream.example.com", ACMEEmail: "ops@example.com", DataDir: dir})
	if err != nil {
		t.Fatalf("New(acme): %v", err)
	}
	if err := p.acme.Cache.Put(context.Background(), "stream.example.com", []byte("not a certificate")); err != nil {
		t.Fatalf("seed the cache: %v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("CertInfo panicked on a corrupt cache entry: %v", r)
		}
	}()
	if _, err := p.CertInfo(); err == nil {
		t.Fatal("CertInfo() succeeded on a corrupt cache entry, want an error")
	} else if !strings.Contains(err.Error(), "stream.example.com") {
		t.Errorf("error = %q, want it to name the host whose cache entry is bad", err)
	}
}
