package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/tlsx"
)

// An install upgraded from a release that minted an unconstrained CA gets a
// new CA on its first start (see tlsx.caReplacementReason), and so does one
// whose tls.hostname changed. Every client that trusted the old CA breaks at
// that moment, so the start must say so, say why, and name the file to trust
// instead.
func TestAReplacedLocalCAIsAnnouncedAtStartup(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	// First start: a fresh CA, nothing anyone trusted was replaced.
	first, err := tlsx.New(tlsx.Options{Mode: tlsx.ModeSelfSigned, Hostname: "old.local", DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	warnIfCAReplaced(log, first, "/data/tls/ca.crt")
	if strings.Contains(buf.String(), "local CA was replaced") {
		t.Errorf("a first start announced a replaced CA:\n%s", buf.String())
	}

	// A hostname change replaces the CA.
	second, err := tlsx.New(tlsx.Options{Mode: tlsx.ModeSelfSigned, Hostname: "new.local", DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	warnIfCAReplaced(log, second, "/data/tls/ca.crt")
	out := buf.String()
	for _, want := range []string{"level=WARN", "local CA was replaced", "Remove the old", "tls.hostname changed", "/data/tls/ca.crt", second.CAFingerprint()} {
		if !strings.Contains(out, want) {
			t.Errorf("the startup warning does not contain %q:\n%s", want, out)
		}
	}
}

// The case UPGRADING.md is written for, driven through the startup path rather
// than the helper: a dataDir holding the unconstrained CA an older release
// minted, and a start in selfsigned mode. newTLSProvider is the only way run()
// gets a provider, so if the warning is not in its log, the operator never
// hears that every client of theirs is about to break.
func TestStartupWithAnOlderUnconstrainedCAWarns(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir, TLS: config.TLS{Mode: config.ModeSelfSigned, Hostname: "studio.local"}}
	seedUnconstrainedCA(t, cfg.TLSDir())

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	provider, err := newTLSProvider(log, cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"level=WARN", "local CA was replaced", "no name constraints", cfg.SelfSignedCACertPath(), provider.CAFingerprint()} {
		if !strings.Contains(out, want) {
			t.Errorf("the startup log does not contain %q:\n%s", want, out)
		}
	}

	// The second start keeps the CA the first one minted, and says nothing:
	// a warning on every boot would teach the operator to ignore it.
	buf.Reset()
	if _, err := newTLSProvider(log, cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "local CA was replaced") {
		t.Errorf("a start that kept its CA announced a replacement:\n%s", buf.String())
	}
}

func TestNewTLSProviderReportsAConfigError(t *testing.T) {
	// selfsigned with a hostname that is not a name at all: the error is the
	// config's, and no provider (and no log line claiming one) comes back.
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	cfg := config.Config{DataDir: t.TempDir(), TLS: config.TLS{Mode: config.ModeSelfSigned, Hostname: "not a host name"}}
	if p, err := newTLSProvider(log, cfg); err == nil {
		t.Fatalf("an unusable hostname built a provider (%v)", p.Mode())
	}
	if strings.Contains(buf.String(), "msg=tls ") {
		t.Errorf("a failed start logged a tls mode:\n%s", buf.String())
	}
}

// seedUnconstrainedCA writes the kind of CA a release before name constraints
// minted: a self-signed CA certificate with no permitted subtrees.
func seedUnconstrainedCA(t *testing.T, tlsDir string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "polyemesis local CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tlsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tlsDir, "ca.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tlsDir, "ca.key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
}
