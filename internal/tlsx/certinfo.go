package tlsx

import (
	"bytes"
	"crypto/x509"
	"fmt"
	"time"
)

// CertInfo describes a served certificate for the UI's status card and the
// expiry warning.
//
// Every field here comes from the public half of the certificate. There is no
// field for key material and there must never be one: this struct is JSON
// encoded straight onto an API response.
type CertInfo struct {
	Subject     string   `json:"subject"`
	Issuer      string   `json:"issuer"`
	DNSNames    []string `json:"dnsNames"`
	IPAddresses []string `json:"ipAddresses"`

	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
	// DaysRemaining is negative once the certificate has expired, so a UI can
	// render "expired 3 days ago" without a second field.
	DaysRemaining int  `json:"daysRemaining"`
	Expired       bool `json:"expired"`

	// Fingerprint is the SHA-256 of the DER, colon-separated uppercase hex,
	// the format browsers and openssl show.
	Fingerprint string `json:"fingerprint"`
	// SelfSigned means no public CA vouches for this certificate, so a browser
	// will warn until the user installs the local CA.
	SelfSigned bool `json:"selfSigned"`
}

// Inspect describes cert as of now.
func Inspect(cert *x509.Certificate, now time.Time) CertInfo {
	// Both name slices start empty rather than nil so the JSON always carries
	// an array; a UI that maps over them should not have to guard for null.
	info := CertInfo{
		Subject:       cert.Subject.String(),
		Issuer:        cert.Issuer.String(),
		DNSNames:      append([]string{}, cert.DNSNames...),
		IPAddresses:   []string{},
		NotBefore:     cert.NotBefore,
		NotAfter:      cert.NotAfter,
		Fingerprint:   fingerprint(cert.Raw),
		DaysRemaining: daysBetween(now, cert.NotAfter),
		Expired:       !now.Before(cert.NotAfter),
		// A certificate whose issuer is its own subject signed itself. A leaf
		// from the local CA does not match this and is corrected by the caller
		// that knows the mode.
		SelfSigned: bytes.Equal(cert.RawIssuer, cert.RawSubject),
	}
	for _, ip := range cert.IPAddresses {
		info.IPAddresses = append(info.IPAddresses, ip.String())
	}
	return info
}

// InspectPEM describes the first certificate in a PEM bundle. Blocks that are
// not certificates are skipped, so it is safe to hand it a bundle that leads
// with a private key: the key is never decoded.
func InspectPEM(pemBytes []byte, now time.Time) (CertInfo, error) {
	cert, err := parseFirstCertificate(pemBytes)
	if err != nil {
		return CertInfo{}, fmt.Errorf("tlsx: %w", err)
	}
	return Inspect(cert, now), nil
}

// daysBetween floors, so twelve hours short of expiry reads as 0 days left
// rather than rounding up into a day that does not exist.
func daysBetween(from, to time.Time) int {
	const day = 24 * time.Hour
	d := to.Sub(from)
	days := int(d / day)
	if d < 0 && d%day != 0 {
		days--
	}
	return days
}
