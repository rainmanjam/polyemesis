package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/fsperm"
)

// writeConfig drops a config.yaml in a fresh temp dir and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

// certPair creates stand-in cert/key files; Validate only needs them readable.
func certPair(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	for _, p := range []string{certFile, keyFile} {
		if err := os.WriteFile(p, []byte("placeholder"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	return certFile, keyFile
}

func TestLoadMapsLegacyEnabledFlagOntoModeWithoutChangingBehaviour(t *testing.T) {
	cert, key := certPair(t)

	tests := []struct {
		name         string
		yaml         string
		wantMode     Mode
		wantResolved Mode
		wantServes   bool
	}{
		{
			name:         "legacy enabled true with cert and key keeps serving that cert as manual",
			yaml:         "tls:\n  enabled: true\n  certFile: " + cert + "\n  keyFile: " + key + "\n",
			wantMode:     ModeManual,
			wantResolved: ModeManual,
			wantServes:   true,
		},
		{
			name:         "legacy enabled false stays plaintext",
			yaml:         "tls:\n  enabled: false\n",
			wantMode:     ModeOff,
			wantResolved: ModeOff,
		},
		{
			name:         "absent tls block stays plaintext",
			yaml:         "addr: \":8080\"\n",
			wantMode:     ModeOff,
			wantResolved: ModeOff,
		},
		{
			name:         "empty tls block stays plaintext",
			yaml:         "tls: {}\n",
			wantMode:     ModeOff,
			wantResolved: ModeOff,
		},
		{
			name:         "an explicitly blank mode falls back to the legacy flag",
			yaml:         "tls:\n  mode: \"\"\n  enabled: true\n  certFile: " + cert + "\n  keyFile: " + key + "\n",
			wantMode:     ModeManual,
			wantResolved: ModeManual,
			wantServes:   true,
		},
		{
			name:         "mode is matched case insensitively",
			yaml:         "tls:\n  mode: SelfSigned\n  hostname: box.lan\n",
			wantMode:     ModeSelfSigned,
			wantResolved: ModeSelfSigned,
			wantServes:   true,
		},
		{
			name:         "explicit mode off wins over a stale enabled true",
			yaml:         "tls:\n  mode: off\n  enabled: true\n  certFile: " + cert + "\n  keyFile: " + key + "\n",
			wantMode:     ModeOff,
			wantResolved: ModeOff,
		},
		{
			name:         "explicit mode selfsigned wins over enabled false",
			yaml:         "tls:\n  mode: selfsigned\n  hostname: box.lan\n  enabled: false\n",
			wantMode:     ModeSelfSigned,
			wantResolved: ModeSelfSigned,
			wantServes:   true,
		},
		{
			name:         "new style acme",
			yaml:         "tls:\n  mode: acme\n  hostname: stream.example.com\n  acmeEmail: ops@example.com\n",
			wantMode:     ModeACME,
			wantResolved: ModeACME,
			wantServes:   true,
		},
		{
			name:         "new style manual",
			yaml:         "tls:\n  mode: manual\n  certFile: " + cert + "\n  keyFile: " + key + "\n",
			wantMode:     ModeManual,
			wantResolved: ModeManual,
			wantServes:   true,
		},
		{
			name:         "auto behind a trusted proxy resolves to off",
			yaml:         "trustProxyHeaders: true\ntls:\n  mode: auto\n  hostname: stream.example.com\n  acmeEmail: ops@example.com\n",
			wantMode:     ModeAuto,
			wantResolved: ModeOff,
		},
		{
			name:         "auto with a public fqdn and an email resolves to acme",
			yaml:         "tls:\n  mode: auto\n  hostname: stream.example.com\n  acmeEmail: ops@example.com\n",
			wantMode:     ModeAuto,
			wantResolved: ModeACME,
			wantServes:   true,
		},
		{
			name:         "auto on a lan name resolves to selfsigned",
			yaml:         "tls:\n  mode: auto\n  hostname: box.lan\n",
			wantMode:     ModeAuto,
			wantResolved: ModeSelfSigned,
			wantServes:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tc.yaml))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.TLS.Mode != tc.wantMode {
				t.Errorf("mode = %q, want %q", cfg.TLS.Mode, tc.wantMode)
			}
			if got := cfg.ResolvedTLSMode(); got != tc.wantResolved {
				t.Errorf("ResolvedTLSMode() = %q, want %q", got, tc.wantResolved)
			}
			if got := cfg.ServesTLS(); got != tc.wantServes {
				t.Errorf("ServesTLS() = %v, want %v", got, tc.wantServes)
			}
		})
	}
}

func TestLoadKeepsLegacyCertPathsSoUpgradesServeTheSameCertificate(t *testing.T) {
	cert, key := certPair(t)
	cfg, err := Load(writeConfig(t, "tls:\n  enabled: true\n  certFile: "+cert+"\n  keyFile: "+key+"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.TLS.Enabled {
		t.Error("tls.enabled was dropped; legacy consumers depend on it")
	}
	if cfg.TLS.CertFile != cert || cfg.TLS.KeyFile != key {
		t.Errorf("cert/key = %q/%q, want %q/%q", cfg.TLS.CertFile, cfg.TLS.KeyFile, cert, key)
	}
	if cfg.ResolvedTLSMode() == ModeSelfSigned {
		t.Error("an install with a real certificate must never be upgraded onto a self-signed one")
	}
}

// The `enhancedRtmp` field was removed from Config, and this is the assumption
// that made removing it safe rather than a breaking change for anyone holding a
// config file that still names it.
//
// The field's own comment claimed it existed so such files would keep parsing.
// They keep parsing regardless, because Load uses yaml.Unmarshal rather than a
// decoder with KnownFields(true), and an unrecognised key is ignored. If that
// ever changes -- someone tightens Load to reject unknown keys, which is a
// defensible thing to want -- this test fails and names the upgrade that would
// otherwise break in the field rather than in CI.
func TestOldConfigWithEnhancedRtmpStillParses(t *testing.T) {
	cfg, err := Load(writeConfig(t, "addr: \":9001\"\nenhancedRtmp: true\ntrustProxyHeaders: true\n"))
	if err != nil {
		t.Fatalf("a config carrying the removed enhancedRtmp key failed to load: %v", err)
	}
	// The keys around it must survive too: an ignored key must be ignored, not
	// treated as the end of the document.
	if cfg.Addr != ":9001" {
		t.Errorf("addr = %q, want \":9001\"", cfg.Addr)
	}
	if !cfg.TrustProxyHeaders {
		t.Error("the key AFTER the removed one was dropped")
	}
}

func TestLoadWithNoFileDefaultsToTLSOff(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TLS.Mode != ModeOff {
		t.Errorf("mode = %q, want %q", cfg.TLS.Mode, ModeOff)
	}
	if cfg.Addr != ":8080" || cfg.DataDir != "./data" {
		t.Errorf("defaults = %q/%q", cfg.Addr, cfg.DataDir)
	}
}

// The shipped example is what every new install starts from, so a typo in it
// is a broken first run for someone.
func TestShippedExampleConfigParsesAndResolvesAsDocumented(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatalf("config.example.yaml does not load: %v", err)
	}
	if cfg.TLS.Mode != ModeAuto {
		t.Errorf("example ships tls.mode %q, want %q so new installs get TLS", cfg.TLS.Mode, ModeAuto)
	}
	if cfg.TLS.HSTS {
		t.Error("the example must not opt into HSTS")
	}
	if got := cfg.ResolvedTLSMode(); got != ModeSelfSigned {
		t.Errorf("unedited example resolves to %q, want %q", got, ModeSelfSigned)
	}
}

func TestLoadRejectsInvalidTLSConfigurations(t *testing.T) {
	cert, _ := certPair(t)

	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "unknown mode",
			yaml:    "tls:\n  mode: letsencrypt\n",
			wantErr: "not one of",
		},
		{
			name:    "legacy enabled true without cert or key",
			yaml:    "tls:\n  enabled: true\n",
			wantErr: "tls.certFile",
		},
		{
			name:    "manual with only a cert",
			yaml:    "tls:\n  mode: manual\n  certFile: " + cert + "\n",
			wantErr: "tls.keyFile",
		},
		{
			name:    "manual with an unreadable cert",
			yaml:    "tls:\n  mode: manual\n  certFile: /nope/cert.pem\n  keyFile: /nope/key.pem\n",
			wantErr: "cannot read",
		},
		{
			name:    "acme without a hostname",
			yaml:    "tls:\n  mode: acme\n  acmeEmail: ops@example.com\n",
			wantErr: "tls.hostname",
		},
		{
			name:    "acme without an email",
			yaml:    "tls:\n  mode: acme\n  hostname: stream.example.com\n",
			wantErr: "tls.acmeEmail",
		},
		{
			name:    "acme for a name no public ca can validate",
			yaml:    "tls:\n  mode: acme\n  hostname: box.lan\n  acmeEmail: ops@example.com\n",
			wantErr: "public DNS name",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.yaml))
			if err == nil {
				t.Fatalf("Load succeeded, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestResolveOnlyRewritesAuto(t *testing.T) {
	tests := []struct {
		name       string
		tls        TLS
		trustProxy bool
		want       Mode
	}{
		{name: "auto behind a proxy defers to the proxy", tls: TLS{Mode: ModeAuto, Hostname: "stream.example.com", ACMEEmail: "ops@example.com"}, trustProxy: true, want: ModeOff},
		{name: "auto with a public fqdn and an email picks acme", tls: TLS{Mode: ModeAuto, Hostname: "stream.example.com", ACMEEmail: "ops@example.com"}, want: ModeACME},
		{name: "auto with a public fqdn but no email cannot do acme", tls: TLS{Mode: ModeAuto, Hostname: "stream.example.com"}, want: ModeSelfSigned},
		{name: "auto with an email but a lan name cannot do acme", tls: TLS{Mode: ModeAuto, Hostname: "box.lan", ACMEEmail: "ops@example.com"}, want: ModeSelfSigned},
		{name: "auto with no hostname at all", tls: TLS{Mode: ModeAuto}, want: ModeSelfSigned},
		{name: "explicit acme is untouched by the proxy setting", tls: TLS{Mode: ModeACME, Hostname: "stream.example.com", ACMEEmail: "ops@example.com"}, trustProxy: true, want: ModeACME},
		{name: "explicit manual is untouched", tls: TLS{Mode: ModeManual}, want: ModeManual},
		{name: "explicit selfsigned is untouched", tls: TLS{Mode: ModeSelfSigned}, want: ModeSelfSigned},
		{name: "explicit off is untouched", tls: TLS{Mode: ModeOff}, want: ModeOff},
		{name: "zero value is off", tls: TLS{}, want: ModeOff},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tls.Resolve(tc.trustProxy); got != tc.want {
				t.Errorf("Resolve(%v) = %q, want %q", tc.trustProxy, got, tc.want)
			}
		})
	}
}

func TestIsPublicFQDNRejectsNamesNoPublicCACanIssueFor(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"stream.example.com", true},
		{"a.b.c.example.org", true},
		{"stream.example.com.", true},
		{"STREAM.EXAMPLE.COM", true},
		{"", false},
		{"localhost", false},
		{"polyemesis", false},
		{"box.local", false},
		{"box.internal", false},
		{"box.lan", false},
		{"box.home", false},
		{"10.in-addr.arpa", false},
		{"app.localhost", false},
		{"192.168.1.10", false},
		{"127.0.0.1", false},
		{"::1", false},
	}

	for _, tc := range tests {
		t.Run(tc.host, func(t *testing.T) {
			if got := IsPublicFQDN(tc.host); got != tc.want {
				t.Errorf("IsPublicFQDN(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

func TestSelfSignedHostnameFallsBackToTheSystemHostname(t *testing.T) {
	sys, err := os.Hostname()
	if err != nil || sys == "" {
		t.Skip("system hostname unavailable")
	}

	cfg, err := Load(writeConfig(t, "tls:\n  mode: selfsigned\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TLS.Hostname != strings.TrimSuffix(sys, ".") {
		t.Errorf("hostname = %q, want the system hostname %q", cfg.TLS.Hostname, sys)
	}

	explicit := TLS{Mode: ModeSelfSigned, Hostname: "box.lan."}
	got, err := explicit.EffectiveHostname()
	if err != nil {
		t.Fatalf("EffectiveHostname: %v", err)
	}
	if got != "box.lan" {
		t.Errorf("EffectiveHostname() = %q, want %q", got, "box.lan")
	}
}

func TestHSTSIsOptInAndNeverSentUnderASelfSignedCertificate(t *testing.T) {
	tests := []struct {
		name     string
		cfg      Config
		wantSend bool
		wantWarn string
	}{
		{
			name: "off by default even with a real certificate",
			cfg:  Config{TLS: TLS{Mode: ModeACME, Hostname: "stream.example.com", ACMEEmail: "ops@example.com"}},
		},
		{
			name:     "opted in with acme",
			cfg:      Config{TLS: TLS{Mode: ModeACME, Hostname: "stream.example.com", ACMEEmail: "ops@example.com", HSTS: true}},
			wantSend: true,
		},
		{
			name:     "opted in with a manual certificate",
			cfg:      Config{TLS: TLS{Mode: ModeManual, HSTS: true}},
			wantSend: true,
		},
		{
			name:     "opted in under selfsigned is suppressed with a warning",
			cfg:      Config{TLS: TLS{Mode: ModeSelfSigned, Hostname: "box.lan", HSTS: true}},
			wantWarn: "self-signed",
		},
		{
			name:     "opted in with tls off is suppressed with a warning",
			cfg:      Config{TLS: TLS{Mode: ModeOff, HSTS: true}},
			wantWarn: "not terminating TLS",
		},
		{
			name:     "opted in under auto that resolved to selfsigned is suppressed",
			cfg:      Config{TLS: TLS{Mode: ModeAuto, Hostname: "box.lan", HSTS: true}},
			wantWarn: "self-signed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			send, warn := tc.cfg.HSTSPolicy()
			if send != tc.wantSend {
				t.Errorf("send = %v, want %v", send, tc.wantSend)
			}
			if tc.wantWarn == "" && warn != "" {
				t.Errorf("unexpected warning %q", warn)
			}
			if tc.wantWarn != "" && !strings.Contains(warn, tc.wantWarn) {
				t.Errorf("warning = %q, want it to mention %q", warn, tc.wantWarn)
			}
		})
	}
}

func TestBindsPubliclyTreatsAWildcardBindAsPublic(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{":8080", true},
		{"0.0.0.0:8080", true},
		{"[::]:8080", true},
		{"192.168.1.10:8080", true},
		{"stream.example.com:443", true},
		{"127.0.0.1:8080", false},
		{"localhost:8080", false},
		{"[::1]:8080", false},
	}

	for _, tc := range tests {
		t.Run(tc.addr, func(t *testing.T) {
			if got := BindsPublicly(tc.addr); got != tc.want {
				t.Errorf("BindsPublicly(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}

func TestInsecureExposureWarningFiresOnlyForPlaintextOnAPublicBind(t *testing.T) {
	tests := []struct {
		name  string
		cfg   Config
		warns bool
	}{
		{
			name:  "the default bind with no tls is the exposure we care about",
			cfg:   Config{Addr: ":8080", TLS: TLS{Mode: ModeOff}},
			warns: true,
		},
		{
			name: "loopback with no tls is fine",
			cfg:  Config{Addr: "127.0.0.1:8080", TLS: TLS{Mode: ModeOff}},
		},
		{
			name: "public bind behind a trusted proxy is fine",
			cfg:  Config{Addr: ":8080", TrustProxyHeaders: true, TLS: TLS{Mode: ModeOff}},
		},
		{
			name: "public bind with a self-signed certificate is still encrypted",
			cfg:  Config{Addr: ":8080", TLS: TLS{Mode: ModeSelfSigned, Hostname: "box.lan"}},
		},
		{
			name: "public bind with acme is fine",
			cfg:  Config{Addr: ":8080", TLS: TLS{Mode: ModeACME, Hostname: "stream.example.com", ACMEEmail: "ops@example.com"}},
		},
		{
			name:  "auto that resolved to off because of the proxy setting does not double warn",
			cfg:   Config{Addr: ":8080", TrustProxyHeaders: true, TLS: TLS{Mode: ModeAuto}},
			warns: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.InsecureExposureWarning()
			if tc.warns && got == "" {
				t.Error("expected a warning, got none")
			}
			if !tc.warns && got != "" {
				t.Errorf("unexpected warning %q", got)
			}
		})
	}
}

func TestTLSMaterialPathsLiveUnderDataDir(t *testing.T) {
	cfg := Config{DataDir: "/var/lib/polyemesis"}
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"tls dir", cfg.TLSDir(), "/var/lib/polyemesis/tls"},
		{"acme cache", cfg.ACMECacheDir(), "/var/lib/polyemesis/tls/acme"},
		{"ca cert", cfg.SelfSignedCACertPath(), "/var/lib/polyemesis/tls/ca.crt"},
		{"ca key", cfg.SelfSignedCAKeyPath(), "/var/lib/polyemesis/tls/ca.key"},
		{"leaf cert", cfg.SelfSignedCertPath(), "/var/lib/polyemesis/tls/server.crt"},
		{"leaf key", cfg.SelfSignedKeyPath(), "/var/lib/polyemesis/tls/server.key"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != filepath.FromSlash(tc.want) {
				t.Errorf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestEnsureDirsCreatesKeyDirectoriesPrivateAndOnlyWhenNeeded(t *testing.T) {
	tests := []struct {
		name        string
		mode        Mode
		wantTLSDir  bool
		wantACMEDir bool
	}{
		{name: "off creates neither", mode: ModeOff},
		{name: "manual creates neither because the operator owns the files", mode: ModeManual},
		{name: "selfsigned creates the tls dir", mode: ModeSelfSigned, wantTLSDir: true},
		{name: "acme creates the cache dir", mode: ModeACME, wantTLSDir: true, wantACMEDir: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{DataDir: t.TempDir(), TLS: TLS{Mode: tc.mode, Hostname: "box.lan"}}
			if err := cfg.EnsureDirs(); err != nil {
				t.Fatalf("EnsureDirs: %v", err)
			}
			assertPrivateDir(t, cfg.TLSDir(), tc.wantTLSDir)
			assertPrivateDir(t, cfg.ACMECacheDir(), tc.wantACMEDir)
			assertDirExists(t, cfg.RecordingsDir(), true)
		})
	}
}

// assertDirExists checks presence only.
//
// The MODE is deliberately not asserted for the public directories. 0755 is a
// Unix rendering of "an ordinary directory", Windows has no equivalent, and
// nothing depends on the exact bits -- so asserting them would fail on Windows
// while describing no requirement.
func assertDirExists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	if !want {
		if err == nil {
			t.Errorf("%s exists but should not have been created", path)
		}
		return
	}
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
}

// assertPrivateDir checks the PROPERTY -- that nothing but this account can
// reach the directory -- rather than the mode bits that happen to express it on
// one platform.
//
// That distinction is the whole lesson here. This assertion read `mode ==
// 0700` until the cross-platform matrix ran it on Windows, where a FileMode is
// discarded: the TLS key directory was open to every local account, and this
// test reported it protected on every platform for as long as it existed.
func assertPrivateDir(t *testing.T, path string, want bool) {
	t.Helper()
	assertDirExists(t, path, want)
	if !want {
		return
	}
	if err := fsperm.CheckPrivate(path); err != nil {
		t.Errorf("TLS private key material is exposed: %v", err)
	}
}
