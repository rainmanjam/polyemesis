package tlsx

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// baseTime is a fixed origin so certificate lifetimes in the tests are
// arithmetic rather than a race against the wall clock.
var baseTime = time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)

// at returns a clock frozen d after baseTime.
func at(d time.Duration) func() time.Time {
	return func() time.Time { return baseTime.Add(d) }
}

// newSelfSigned builds a self-signed Provider rooted at dir, as of baseTime+d.
func newSelfSigned(t *testing.T, dir, hostname string, d time.Duration) *Provider {
	t.Helper()
	p, err := New(Options{Mode: ModeSelfSigned, Hostname: hostname, DataDir: dir, now: at(d)})
	if err != nil {
		t.Fatalf("New(selfsigned, %q) at +%v: %v", hostname, d, err)
	}
	return p
}

// writeManualPair mints an operator-style certificate/key pair on disk and
// returns the two paths.
func writeManualPair(t *testing.T, hostname string) (certPath, keyPath string) {
	t.Helper()
	// Reuse the self-signed generator: what matters to manual mode is that the
	// files are a valid pair, not how they were made.
	dir := t.TempDir()
	newSelfSigned(t, dir, hostname, 0)
	return filepath.Join(Dir(dir), serverCertFile), filepath.Join(Dir(dir), serverKeyFile)
}

func TestModeValidRejectsAutoBecauseResolutionIsTheCallersJob(t *testing.T) {
	tests := []struct {
		mode Mode
		want bool
	}{
		{ModeOff, true},
		{ModeACME, true},
		{ModeSelfSigned, true},
		{ModeManual, true},
		{"auto", false},
		{"", false},
		{"Manual", false},
		{"tls", false},
	}
	for _, tc := range tests {
		if got := tc.mode.Valid(); got != tc.want {
			t.Errorf("Mode(%q).Valid() = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

func TestUnknownModeIsRefusedRatherThanQuietlyServingPlaintext(t *testing.T) {
	for _, mode := range []Mode{"auto", "", "https", "SelfSigned"} {
		_, err := New(Options{Mode: mode, DataDir: t.TempDir()})
		if err == nil {
			t.Errorf("New(mode=%q) succeeded, want an error naming the accepted modes", mode)
			continue
		}
		if !strings.Contains(err.Error(), "selfsigned") {
			t.Errorf("New(mode=%q) error = %q, want it to list the accepted modes", mode, err)
		}
	}
}

func TestOffModeYieldsNoTLSConfigAndNoCertificate(t *testing.T) {
	p, err := New(Options{Mode: ModeOff, DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New(off): %v", err)
	}
	if p.Enabled() {
		t.Error("Enabled() = true in off mode, want false")
	}
	if p.TLSConfig() != nil {
		t.Error("TLSConfig() is non-nil in off mode; the caller would serve TLS on a plaintext deployment")
	}
	if _, err := p.CertInfo(); err != ErrNoCertificate {
		t.Errorf("CertInfo() error = %v, want ErrNoCertificate", err)
	}
	if p.CACertificatePEM() != nil {
		t.Error("CACertificatePEM() is non-nil in off mode")
	}
	if p.CAFingerprint() != "" {
		t.Error("CAFingerprint() is non-empty in off mode")
	}
}

func TestOffModeCreatesNothingOnDisk(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(Options{Mode: ModeOff, DataDir: dir}); err != nil {
		t.Fatalf("New(off): %v", err)
	}
	if _, err := os.Stat(Dir(dir)); !os.IsNotExist(err) {
		t.Errorf("off mode created %s; it must not mint material it will never serve", Dir(dir))
	}
}

func TestEveryServingModePinsTLS12AndExplicitCurves(t *testing.T) {
	certPath, keyPath := writeManualPair(t, "manual.example.com")

	tests := []struct {
		name string
		opts Options
	}{
		{"selfsigned", Options{Mode: ModeSelfSigned, Hostname: "box.local", DataDir: t.TempDir(), now: at(0)}},
		{"manual", Options{Mode: ModeManual, CertFile: certPath, KeyFile: keyPath, DataDir: t.TempDir()}},
		{"acme", Options{Mode: ModeACME, Hostname: "stream.example.com", ACMEEmail: "ops@example.com", DataDir: t.TempDir()}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := New(tc.opts)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			conf := p.TLSConfig()
			if conf == nil {
				t.Fatal("TLSConfig() = nil, want a servable config")
			}
			if conf.MinVersion != tls.VersionTLS12 {
				t.Errorf("MinVersion = %#x, want TLS 1.2 (%#x)", conf.MinVersion, tls.VersionTLS12)
			}
			want := []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384}
			if len(conf.CurvePreferences) != len(want) {
				t.Fatalf("CurvePreferences = %v, want %v", conf.CurvePreferences, want)
			}
			for i := range want {
				if conf.CurvePreferences[i] != want[i] {
					t.Fatalf("CurvePreferences = %v, want %v", conf.CurvePreferences, want)
				}
			}
			if len(conf.NextProtos) == 0 || conf.NextProtos[0] != "h2" {
				t.Errorf("NextProtos = %v, want HTTP/2 offered first", conf.NextProtos)
			}
		})
	}
}

func TestACMEConfigAdvertisesTheALPNChallengeSoPort443CanValidateAlone(t *testing.T) {
	p, err := New(Options{Mode: ModeACME, Hostname: "stream.example.com", ACMEEmail: "ops@example.com", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New(acme): %v", err)
	}
	var found bool
	for _, proto := range p.TLSConfig().NextProtos {
		if proto == "acme-tls/1" {
			found = true
		}
	}
	if !found {
		t.Errorf("NextProtos = %v, want acme-tls/1 so issuance survives a blocked port 80", p.TLSConfig().NextProtos)
	}
	if p.TLSConfig().GetCertificate == nil {
		t.Error("GetCertificate = nil in acme mode; nothing would issue a certificate")
	}
}

func TestManualModeServesTheOperatorsOwnFiles(t *testing.T) {
	certPath, keyPath := writeManualPair(t, "manual.example.com")

	p, err := New(Options{Mode: ModeManual, CertFile: certPath, KeyFile: keyPath, now: at(0)})
	if err != nil {
		t.Fatalf("New(manual): %v", err)
	}
	info, err := p.CertInfo()
	if err != nil {
		t.Fatalf("CertInfo(): %v", err)
	}
	if len(info.DNSNames) == 0 || info.DNSNames[0] != "manual.example.com" {
		t.Errorf("DNSNames = %v, want the operator's certificate, not a generated one", info.DNSNames)
	}
	// Manual material is whatever the operator supplied; we must not claim a
	// local CA exists for it.
	if p.CACertificatePEM() != nil || p.CAFingerprint() != "" {
		t.Error("manual mode reported a local CA; there is none to offer for download")
	}
}

func TestManualModeIsNotMisreportedAsSelfSigned(t *testing.T) {
	certPath, keyPath := writeManualPair(t, "manual.example.com")

	p, err := New(Options{Mode: ModeManual, CertFile: certPath, KeyFile: keyPath, now: at(0)})
	if err != nil {
		t.Fatalf("New(manual): %v", err)
	}
	info, err := p.CertInfo()
	if err != nil {
		t.Fatalf("CertInfo(): %v", err)
	}
	// The fixture leaf is issued by a local CA, so it is not self-issued.
	if info.SelfSigned {
		t.Error("SelfSigned = true for a CA-issued certificate in manual mode")
	}
}

func TestBrokenCertificateMaterialIsAClearErrorNotAPanic(t *testing.T) {
	_, goodKey := writeManualPair(t, "good.example.com")
	otherCert, _ := writeManualPair(t, "other.example.com")

	// corrupt writes a file whose content cannot possibly parse.
	corrupt := func(t *testing.T, name string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(p, []byte("-----BEGIN CERTIFICATE-----\nnot base64 at all\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	tests := []struct {
		name     string
		opts     func(t *testing.T) Options
		wantWord string
	}{
		{
			name:     "manual mode with no files configured",
			opts:     func(*testing.T) Options { return Options{Mode: ModeManual} },
			wantWord: "certFile",
		},
		{
			name: "manual certificate file is missing",
			opts: func(t *testing.T) Options {
				return Options{Mode: ModeManual, CertFile: filepath.Join(t.TempDir(), "absent.crt"), KeyFile: goodKey}
			},
			wantWord: "absent.crt",
		},
		{
			name: "manual certificate is corrupt",
			opts: func(t *testing.T) Options {
				return Options{Mode: ModeManual, CertFile: corrupt(t, "broken.crt"), KeyFile: goodKey}
			},
			wantWord: "broken.crt",
		},
		{
			name: "manual certificate and key are from different pairs",
			opts: func(*testing.T) Options {
				return Options{Mode: ModeManual, CertFile: otherCert, KeyFile: goodKey}
			},
			wantWord: "server.crt",
		},
		{
			name: "acme without a hostname",
			opts: func(t *testing.T) Options {
				return Options{Mode: ModeACME, ACMEEmail: "ops@example.com", DataDir: t.TempDir()}
			},
			wantWord: "hostname",
		},
		{
			name: "acme without a contact address",
			opts: func(t *testing.T) Options {
				return Options{Mode: ModeACME, Hostname: "stream.example.com", DataDir: t.TempDir()}
			},
			wantWord: "acmeEmail",
		},
		{
			name:     "selfsigned without a data directory",
			opts:     func(*testing.T) Options { return Options{Mode: ModeSelfSigned, Hostname: "box.local"} },
			wantWord: "data directory",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("New panicked: %v", r)
				}
			}()
			_, err := New(tc.opts(t))
			if err == nil {
				t.Fatal("New succeeded, want an actionable error")
			}
			if !strings.Contains(err.Error(), tc.wantWord) {
				t.Errorf("error = %q, want it to mention %q so the operator knows what to fix", err, tc.wantWord)
			}
		})
	}
}

func TestCorruptSelfSignedMaterialIsReportedInsteadOfSilentlyReplacingATrustedCA(t *testing.T) {
	tests := []struct {
		name string
		file string
	}{
		{"the CA certificate", caCertFile},
		{"the CA key", caKeyFile},
		{"the server certificate", serverCertFile},
		{"the server key", serverKeyFile},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			newSelfSigned(t, dir, "box.local", 0)

			path := filepath.Join(Dir(dir), tc.file)
			if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
				t.Fatal(err)
			}

			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("New panicked on corrupt %s: %v", tc.file, r)
				}
			}()
			_, err := New(Options{Mode: ModeSelfSigned, Hostname: "box.local", DataDir: dir, now: at(0)})
			if err == nil {
				t.Fatalf("New succeeded with a corrupt %s, want an error", tc.file)
			}
			if !strings.Contains(err.Error(), tc.file) {
				t.Errorf("error = %q, want it to name %s", err, tc.file)
			}
		})
	}
}

func TestSelfSignedRecoversWhenBothHalvesOfAPairAreDeleted(t *testing.T) {
	dir := t.TempDir()
	first := newSelfSigned(t, dir, "box.local", 0)
	caBefore := first.CAFingerprint()

	// Deleting the leaf pair is the documented remedy, so it must actually
	// work — and it must not cost the user the CA they already installed.
	for _, name := range []string{serverCertFile, serverKeyFile} {
		if err := os.Remove(filepath.Join(Dir(dir), name)); err != nil {
			t.Fatal(err)
		}
	}
	second := newSelfSigned(t, dir, "box.local", 0)
	if second.CAFingerprint() != caBefore {
		t.Error("regenerating the leaf replaced the CA; every client that trusts it would have to re-trust")
	}
}

func TestServeChallengeReportsAnUnavailablePortInsteadOfHanging(t *testing.T) {
	p, err := New(Options{Mode: ModeACME, Hostname: "stream.example.com", ACMEEmail: "ops@example.com", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New(acme): %v", err)
	}

	// Occupy a port, then ask for it: this is what "something else already has
	// :80" looks like from inside the process.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()

	_, err = p.ServeChallenge(blocker.Addr().String())
	if err == nil {
		t.Fatal("ServeChallenge succeeded on an occupied port, want an error the caller can log and continue past")
	}
	for _, want := range []string{blocker.Addr().String(), "http-01"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

func TestServeChallengeServesTheACMEResponderWhenThePortIsFree(t *testing.T) {
	p, err := New(Options{Mode: ModeACME, Hostname: "stream.example.com", ACMEEmail: "ops@example.com", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New(acme): %v", err)
	}
	srv, err := p.ServeChallenge("127.0.0.1:0")
	if err != nil {
		t.Fatalf("ServeChallenge: %v", err)
	}
	if srv == nil {
		t.Fatal("ServeChallenge returned no server to shut down")
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestServeChallengeSendsOrdinaryTrafficOnPort80ToHTTPS(t *testing.T) {
	p, err := New(Options{Mode: ModeACME, Hostname: "stream.example.com", ACMEEmail: "ops@example.com", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New(acme): %v", err)
	}
	// The challenge listener is the only thing on port 80, so anything that is
	// not a challenge should be redirected rather than 404'd.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req.Host = "stream.example.com"
	p.acme.HTTPHandler(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if got, want := rec.Header().Get("Location"), "https://stream.example.com/settings"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

func TestServeChallengeIsRefusedOutsideACMEMode(t *testing.T) {
	dir := t.TempDir()
	p := newSelfSigned(t, dir, "box.local", 0)
	if _, err := p.ServeChallenge("127.0.0.1:0"); err == nil {
		t.Error("ServeChallenge succeeded in selfsigned mode; nothing would answer the challenge")
	}
}

func TestHTTPChallengeHandlerInterceptsOnlyTheChallengePath(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Inner", "yes")
	})

	// Outside ACME the handler must pass straight through, so the caller can
	// mount it unconditionally without an if.
	selfSigned := newSelfSigned(t, t.TempDir(), "box.local", 0)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/token", nil)
	selfSigned.HTTPChallengeHandler(inner).ServeHTTP(rec, req)
	if rec.Header().Get("X-Inner") != "yes" {
		t.Error("selfsigned mode swallowed a request instead of passing it to the wrapped handler")
	}

	acmeProvider, err := New(Options{Mode: ModeACME, Hostname: "stream.example.com", ACMEEmail: "ops@example.com", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New(acme): %v", err)
	}
	handler := acmeProvider.HTTPChallengeHandler(inner)

	tests := []struct {
		name       string
		path       string
		host       string
		wantInner  bool
		wantStatus int
	}{
		{
			name:      "ordinary traffic still reaches the app",
			path:      "/api/streams",
			host:      "stream.example.com",
			wantInner: true,
		},
		{
			name:       "an unknown challenge token is answered by autocert, not the app",
			path:       "/.well-known/acme-challenge/token",
			host:       "stream.example.com",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "a challenge for another host is refused by the host policy",
			path:       "/.well-known/acme-challenge/token",
			host:       "evil.example.net",
			wantStatus: http.StatusForbidden,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Host = tc.host
			handler.ServeHTTP(rec, req)

			if gotInner := rec.Header().Get("X-Inner") == "yes"; gotInner != tc.wantInner {
				t.Errorf("reached the wrapped handler = %v, want %v", gotInner, tc.wantInner)
			}
			if tc.wantStatus != 0 && rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}

func TestCACertificatePEMCannotBeMutatedByItsCaller(t *testing.T) {
	dir := t.TempDir()
	p := newSelfSigned(t, dir, "box.local", 0)

	first := p.CACertificatePEM()
	if len(first) == 0 {
		t.Fatal("CACertificatePEM() is empty in selfsigned mode")
	}
	for i := range first {
		first[i] = 'x'
	}
	if second := p.CACertificatePEM(); string(second) == string(first) {
		t.Error("CACertificatePEM() handed out its backing array; a caller scribbled on the CA the UI serves")
	}
}
