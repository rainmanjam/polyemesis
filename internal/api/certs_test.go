package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/tlsx"
)

// selfSignedServer builds a Server holding a real selfsigned Provider over a
// throwaway data directory, which is the only mode with a local CA to serve.
func selfSignedServer(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	cfg.DataDir = t.TempDir()
	p, err := tlsx.New(tlsx.Options{
		Mode:     tlsx.ModeSelfSigned,
		Hostname: "box.local",
		DataDir:  cfg.DataDir,
	})
	if err != nil {
		t.Fatalf("tlsx.New: %v", err)
	}
	return &Server{cfg: cfg, tls: p}
}

func getTLSStatus(t *testing.T, s *Server) tlsStatus {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleTLSStatus(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tls", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got tlsStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding the response: %v", err)
	}
	return got
}

func TestTLSStatusReportsTheResolvedModeNotTheConfiguredOne(t *testing.T) {
	tests := []struct {
		name       string
		cfg        config.Config
		wantMode   config.Mode
		wantServes bool
	}{
		{
			name:       "auto behind a trusted proxy resolves to off",
			cfg:        config.Config{TLS: config.TLS{Mode: config.ModeAuto}, TrustProxyHeaders: true},
			wantMode:   config.ModeOff,
			wantServes: false,
		},
		{
			name: "auto with a public name and an acme contact resolves to acme",
			cfg: config.Config{TLS: config.TLS{
				Mode: config.ModeAuto, Hostname: "stream.example.com", ACMEEmail: "op@example.com",
			}},
			wantMode:   config.ModeACME,
			wantServes: true,
		},
		{
			name:       "auto on a lan name resolves to selfsigned",
			cfg:        config.Config{TLS: config.TLS{Mode: config.ModeAuto, Hostname: "box.local"}},
			wantMode:   config.ModeSelfSigned,
			wantServes: true,
		},
		{
			name:       "legacy enabled true with files reports manual",
			cfg:        config.Config{TLS: config.TLS{Mode: config.ModeManual, Enabled: true, CertFile: "c.pem", KeyFile: "k.pem"}},
			wantMode:   config.ModeManual,
			wantServes: true,
		},
		{
			name:       "legacy enabled false reports off",
			cfg:        config.Config{TLS: config.TLS{Mode: config.ModeOff}},
			wantMode:   config.ModeOff,
			wantServes: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getTLSStatus(t, &Server{cfg: tt.cfg})
			if got.Mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", got.Mode, tt.wantMode)
			}
			if got.Configured != tt.cfg.TLS.Mode {
				t.Errorf("configured = %q, want %q", got.Configured, tt.cfg.TLS.Mode)
			}
			if got.ServesTLS != tt.wantServes {
				t.Errorf("servesTls = %v, want %v", got.ServesTLS, tt.wantServes)
			}
		})
	}
}

func TestTLSStatusReportsHSTSAsSuppressedRatherThanAsRequested(t *testing.T) {
	tests := []struct {
		name        string
		cfg         config.Config
		wantSend    bool
		wantWarning bool
	}{
		{
			name: "opted in on a self-signed certificate is suppressed with a warning",
			cfg: config.Config{TLS: config.TLS{
				Mode: config.ModeSelfSigned, Hostname: "box.local", HSTS: true,
			}},
			wantSend:    false,
			wantWarning: true,
		},
		{
			name: "opted in behind a proxy that terminates TLS is suppressed with a warning",
			cfg: config.Config{
				TLS:               config.TLS{Mode: config.ModeAuto, HSTS: true},
				TrustProxyHeaders: true,
			},
			wantSend:    false,
			wantWarning: true,
		},
		{
			name: "opted in with a publicly trusted certificate is sent",
			cfg: config.Config{TLS: config.TLS{
				Mode: config.ModeACME, Hostname: "stream.example.com", ACMEEmail: "op@example.com", HSTS: true,
			}},
			wantSend:    true,
			wantWarning: false,
		},
		{
			name:        "not opted in says nothing either way",
			cfg:         config.Config{TLS: config.TLS{Mode: config.ModeManual}},
			wantSend:    false,
			wantWarning: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getTLSStatus(t, &Server{cfg: tt.cfg})
			if got.HSTS != tt.wantSend {
				t.Errorf("hsts = %v, want %v", got.HSTS, tt.wantSend)
			}
			if (got.HSTSWarning != "") != tt.wantWarning {
				t.Errorf("hstsWarning = %q, want present = %v", got.HSTSWarning, tt.wantWarning)
			}
		})
	}
}

func TestTLSStatusDescribesTheServedCertificateInSelfSignedMode(t *testing.T) {
	s := selfSignedServer(t, config.Config{TLS: config.TLS{Mode: config.ModeSelfSigned, Hostname: "box.local"}})
	got := getTLSStatus(t, s)

	if got.Certificate == nil {
		t.Fatalf("certificate is null, want the served leaf (certificateError = %q)", got.CertificateError)
	}
	if !got.Certificate.SelfSigned {
		t.Error("selfSigned = false; a leaf from the local CA must still warn the user")
	}
	if got.Certificate.DaysRemaining <= 0 || got.Certificate.Expired {
		t.Errorf("a freshly minted leaf reports daysRemaining = %d, expired = %v",
			got.Certificate.DaysRemaining, got.Certificate.Expired)
	}
	if !got.CAAvailable || got.CAFingerprint == "" {
		t.Errorf("caAvailable = %v, caFingerprint = %q; the UI cannot offer the CA without both",
			got.CAAvailable, got.CAFingerprint)
	}
}

func TestTLSStatusNeverSerialisesKeyMaterial(t *testing.T) {
	s := selfSignedServer(t, config.Config{TLS: config.TLS{Mode: config.ModeSelfSigned, Hostname: "box.local"}})
	rec := httptest.NewRecorder()
	s.handleTLSStatus(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tls", nil))

	// A whole-body scan rather than a field check: a future field added to
	// tlsStatus that carries a key has to fail this test too.
	for _, needle := range []string{"PRIVATE KEY", "BEGIN RSA", "BEGIN EC", "server.key", "ca.key"} {
		if strings.Contains(rec.Body.String(), needle) {
			t.Errorf("the response body contains %q", needle)
		}
	}
}

func TestDownloadCAServesOnlyTheSelfSignedCACertificate(t *testing.T) {
	t.Run("selfsigned serves the CA certificate as an attachment", func(t *testing.T) {
		s := selfSignedServer(t, config.Config{TLS: config.TLS{Mode: config.ModeSelfSigned, Hostname: "box.local"}})
		rec := httptest.NewRecorder()
		s.handleDownloadCA(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tls/ca", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
			t.Errorf("Content-Disposition = %q, want an attachment so the browser saves it", cd)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "-----BEGIN CERTIFICATE-----") {
			t.Error("the body is not a PEM certificate")
		}
		if strings.Contains(body, "PRIVATE KEY") {
			t.Fatal("the CA download contains key material")
		}
	})

	t.Run("modes without a local CA return 404", func(t *testing.T) {
		modes := []config.Mode{config.ModeOff, config.ModeManual, config.ModeACME}
		for _, m := range modes {
			s := &Server{cfg: config.Config{TLS: config.TLS{Mode: m}}}
			rec := httptest.NewRecorder()
			s.handleDownloadCA(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tls/ca", nil))
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s mode: status = %d, want 404", m, rec.Code)
			}
		}
	})
}
