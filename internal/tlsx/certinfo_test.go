package tlsx

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCertInfoDescribesTheCertificateTheUICardShows(t *testing.T) {
	dir := t.TempDir()
	p := newSelfSigned(t, dir, "box.local", 0)
	info := mustCertInfo(t, p)

	if !strings.Contains(info.Subject, "box.local") {
		t.Errorf("Subject = %q, want it to name the host", info.Subject)
	}
	if !strings.Contains(info.Issuer, "polyemesis local CA") {
		t.Errorf("Issuer = %q, want the local CA", info.Issuer)
	}
	if info.Fingerprint == "" || !strings.Contains(info.Fingerprint, ":") {
		t.Errorf("Fingerprint = %q, want colon-separated hex a user can compare with their browser", info.Fingerprint)
	}
	if got := len(strings.Split(info.Fingerprint, ":")); got != 32 {
		t.Errorf("Fingerprint has %d octets, want 32 for SHA-256", got)
	}
	if info.Expired {
		t.Error("Expired = true for a certificate minted moments ago")
	}
	// The leaf is backdated an hour but still runs a full leafValidity from
	// issuance, so a fresh certificate reads as its whole lifetime.
	if want := 365; info.DaysRemaining != want {
		t.Errorf("DaysRemaining = %d, want %d", info.DaysRemaining, want)
	}
}

func TestDaysRemainingFloorsAndGoesNegativeOnceExpired(t *testing.T) {
	notAfter := baseTime.Add(leafValidity)

	tests := []struct {
		name string
		now  time.Time
		want int
	}{
		{"a full year out", baseTime, 365},
		{"half a day before the last full day elapses", notAfter.Add(-36 * time.Hour), 1},
		{"twelve hours left rounds down, not up", notAfter.Add(-12 * time.Hour), 0},
		{"exactly at expiry", notAfter, 0},
		{"an hour past expiry", notAfter.Add(time.Hour), -1},
		{"three days past expiry", notAfter.Add(3 * 24 * time.Hour), -3},
		{"a week past expiry", notAfter.Add(7 * 24 * time.Hour), -7},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := daysBetween(tc.now, notAfter); got != tc.want {
				t.Errorf("daysBetween(%v, %v) = %d, want %d", tc.now, notAfter, got, tc.want)
			}
		})
	}
}

func TestExpiredIsReportedOnceTheCertificateIsPastNotAfter(t *testing.T) {
	dir := t.TempDir()
	p := newSelfSigned(t, dir, "box.local", 0)

	tests := []struct {
		name        string
		at          time.Duration
		wantExpired bool
	}{
		{"the day it was issued", 0, false},
		{"a minute before it lapses", leafValidity - 2*time.Hour, false},
		{"a day after it lapses", leafValidity + 24*time.Hour, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := Inspect(p.leaf, baseTime.Add(tc.at))
			if info.Expired != tc.wantExpired {
				t.Errorf("Expired = %v at +%v, want %v", info.Expired, tc.at, tc.wantExpired)
			}
			if tc.wantExpired && info.DaysRemaining > 0 {
				t.Errorf("DaysRemaining = %d for an expired certificate", info.DaysRemaining)
			}
		})
	}
}

func TestCertInfoNameListsAreArraysNeverNullInJSON(t *testing.T) {
	// A certificate with neither DNS nor IP SANs is unusual but perfectly
	// legal in manual mode, and the UI maps over both lists unconditionally.
	// The CA carries no SANs at all, which makes it the convenient stand-in.
	p := newSelfSigned(t, t.TempDir(), "box.local", 0)
	bare, err := parseFirstCertificate(p.CACertificatePEM())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(Inspect(bare, baseTime))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"dnsNames":[]`, `"ipAddresses":[]`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("JSON = %s, want it to contain %s", encoded, want)
		}
	}
}

func TestFingerprintIsStableForTheSameCertificate(t *testing.T) {
	dir := t.TempDir()
	p := newSelfSigned(t, dir, "box.local", 0)

	first := mustCertInfo(t, p).Fingerprint
	for i := 0; i < 3; i++ {
		if got := mustCertInfo(t, p).Fingerprint; got != first {
			t.Fatalf("fingerprint changed between calls: %s then %s", first, got)
		}
	}
	// The CA and the leaf are different certificates and must not collide.
	if p.CAFingerprint() == first {
		t.Error("the CA and the leaf report the same fingerprint")
	}
}

func TestInspectPEMSkipsPrivateKeyBlocks(t *testing.T) {
	fixture := t.TempDir()
	source := newSelfSigned(t, fixture, "box.local", 0)
	keyPEM, err := os.ReadFile(filepath.Join(Dir(fixture), serverKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := os.ReadFile(filepath.Join(Dir(fixture), serverCertFile))
	if err != nil {
		t.Fatal(err)
	}

	info, err := InspectPEM(append(append([]byte{}, keyPEM...), certPEM...), baseTime)
	if err != nil {
		t.Fatalf("InspectPEM: %v", err)
	}
	if info.Fingerprint != mustCertInfo(t, source).Fingerprint {
		t.Error("InspectPEM described something other than the certificate in the bundle")
	}
}

func TestInspectPEMRejectsInputWithNoCertificate(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty input", ""},
		{"not PEM at all", "hello"},
		{"a key and nothing else", "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"},
		{"a truncated certificate block", "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("InspectPEM panicked: %v", r)
				}
			}()
			if _, err := InspectPEM([]byte(tc.in), baseTime); err == nil {
				t.Error("InspectPEM succeeded, want an error")
			}
		})
	}
}

func TestCertInfoCarriesNoKeyMaterialWhenSerialised(t *testing.T) {
	fixture := t.TempDir()
	p := newSelfSigned(t, fixture, "box.local", 0)

	keyPEM, err := os.ReadFile(filepath.Join(Dir(fixture), serverKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	caKeyPEM, err := os.ReadFile(filepath.Join(Dir(fixture), caKeyFile))
	if err != nil {
		t.Fatal(err)
	}

	// This struct is JSON encoded onto an API response, so the check is on the
	// wire form, not on the fields.
	encoded, err := json.Marshal(mustCertInfo(t, p))
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)

	if strings.Contains(body, "PRIVATE KEY") {
		t.Fatal("CertInfo JSON contains a private key block")
	}
	for name, key := range map[string][]byte{"the server key": keyPEM, "the CA key": caKeyPEM} {
		// Compare on the base64 body, which is what would actually leak.
		for _, line := range strings.Split(string(key), "\n") {
			if len(line) < 32 || strings.HasPrefix(line, "-----") {
				continue
			}
			if strings.Contains(body, line) {
				t.Fatalf("CertInfo JSON contains bytes from %s", name)
			}
		}
	}
}

func TestCACertificatePEMIsACertificateNotAKey(t *testing.T) {
	dir := t.TempDir()
	p := newSelfSigned(t, dir, "box.local", 0)

	pemBytes := string(p.CACertificatePEM())
	if !strings.HasPrefix(pemBytes, "-----BEGIN CERTIFICATE-----") {
		t.Errorf("CACertificatePEM does not start with a certificate block: %.40q", pemBytes)
	}
	if strings.Contains(pemBytes, "PRIVATE KEY") {
		t.Fatal("CACertificatePEM contains a private key")
	}
	if got, want := strings.Count(pemBytes, "BEGIN CERTIFICATE"), 1; got != want {
		t.Errorf("CACertificatePEM holds %d certificates, want %d", got, want)
	}
}
