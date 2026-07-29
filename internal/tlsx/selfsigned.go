package tlsx

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/fsperm"
)

const (
	// caValidity is deliberately long. Installing a CA into a browser, a phone
	// and a keychain is the single most annoying step of a homelab setup, and
	// making the user redo it every year would be a reason to give up on HTTPS
	// altogether. The leaf underneath it rotates instead.
	caValidity = 10 * 365 * 24 * time.Hour
	// leafValidity mirrors what public CAs issue, so nothing downstream is
	// surprised by an unusually long-lived certificate.
	leafValidity = 365 * 24 * time.Hour
	// renewWindow is how close to expiry we regenerate. A month is enough that
	// a box which is only powered on occasionally still renews before anything
	// starts failing.
	renewWindow = 30 * 24 * time.Hour

	keyPerm  os.FileMode = 0o600
	certPerm os.FileMode = 0o644
	dirPerm  os.FileMode = 0o700
)

// File names under <dataDir>/tls. serverCertFile holds the leaf followed by
// the CA, so a client that has been handed only the leaf can still build the
// chain and openssl s_client shows the user exactly what to trust.
const (
	caCertFile     = "ca.crt"
	caKeyFile      = "ca.key"
	serverCertFile = "server.crt"
	serverKeyFile  = "server.key"
)

// material is the loaded-or-freshly-minted self-signed set.
type material struct {
	pair   tls.Certificate
	leaf   *x509.Certificate
	caCert *x509.Certificate
	caPEM  []byte
}

// ensureSelfSigned loads the persisted CA and leaf, minting whatever is
// missing, expiring or no longer correct for the configured hostname.
//
// Material that exists but does not parse is an error rather than a silent
// regeneration: writes here are atomic, so a corrupt file means something
// outside polyemesis interfered, and quietly replacing a CA the user has
// already installed would be worse than saying so.
func ensureSelfSigned(dir, hostname string, now func() time.Time) (*material, error) {
	// fsperm.SecureDir rather than os.MkdirAll(.., 0o700): the CA key and the
	// server key are written in here, and a FileMode restricts nothing on
	// Windows. Established here rather than left to config.EnsureDirs because
	// this function is reachable without it.
	if err := fsperm.SecureDir(dir); err != nil {
		return nil, fmt.Errorf("tlsx: cannot create %s: %w", dir, err)
	}

	caCert, caKey, caPEM, err := loadCA(dir)
	if err != nil {
		return nil, err
	}
	if caCert == nil || expiringBy(caCert, now(), renewWindow) {
		caCert, caKey, caPEM, err = generateCA(dir, now())
		if err != nil {
			return nil, err
		}
	}

	dnsNames, ips := sansFor(hostname)

	leafPair, leaf, err := loadLeaf(dir)
	if err != nil {
		return nil, err
	}
	if leaf == nil || needsNewLeaf(leaf, caCert, dnsNames, ips, now()) {
		leafPair, leaf, err = generateLeaf(dir, caCert, caKey, caPEM, dnsNames, ips, now())
		if err != nil {
			return nil, err
		}
	}

	return &material{pair: leafPair, leaf: leaf, caCert: caCert, caPEM: caPEM}, nil
}

// needsNewLeaf covers the three ways a persisted leaf stops being usable:
// it is about to expire, it was signed by a CA we have since replaced, or the
// operator changed tls.hostname and it no longer names this server.
func needsNewLeaf(leaf, ca *x509.Certificate, dnsNames []string, ips []net.IP, now time.Time) bool {
	if expiringBy(leaf, now, renewWindow) {
		return true
	}
	if leaf.CheckSignatureFrom(ca) != nil {
		return true
	}
	return !covers(leaf, dnsNames, ips)
}

func expiringBy(cert *x509.Certificate, now time.Time, window time.Duration) bool {
	return !now.Add(window).Before(cert.NotAfter)
}

// covers reports whether cert already carries every name we want to serve.
func covers(cert *x509.Certificate, dnsNames []string, ips []net.IP) bool {
	have := make(map[string]bool, len(cert.DNSNames))
	for _, n := range cert.DNSNames {
		have[strings.ToLower(n)] = true
	}
	for _, want := range dnsNames {
		if !have[strings.ToLower(want)] {
			return false
		}
	}
	for _, want := range ips {
		found := false
		for _, got := range cert.IPAddresses {
			if got.Equal(want) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// sansFor builds the SAN set for the leaf.
//
// localhost and the loopback addresses are always included: the operator
// reaches a freshly installed box by IP or over an SSH tunnel long before DNS
// knows its name, and a certificate that only matches the configured hostname
// would turn the first login into a browser warning.
func sansFor(hostname string) ([]string, []net.IP) {
	dnsNames := []string{"localhost"}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}

	h := strings.TrimSuffix(strings.TrimSpace(hostname), ".")
	if h == "" {
		return dnsNames, ips
	}
	if ip := net.ParseIP(h); ip != nil {
		for _, got := range ips {
			if got.Equal(ip) {
				return dnsNames, ips
			}
		}
		return dnsNames, append(ips, ip)
	}
	if strings.EqualFold(h, "localhost") {
		return dnsNames, ips
	}
	// Primary name first: some clients display DNSNames[0] as "the" name.
	return append([]string{h}, dnsNames...), ips
}

func loadCA(dir string) (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	certPath := filepath.Join(dir, caCertFile)
	keyPath := filepath.Join(dir, caKeyFile)

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, fmt.Errorf("tlsx: cannot read %s: %w", certPath, err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		if os.IsNotExist(err) {
			// A certificate with no key cannot sign anything, and overwriting
			// it would strand every client that already trusts it. Say so.
			return nil, nil, nil, fmt.Errorf("tlsx: %s exists but its key %s is missing; "+
				"delete both to mint a new local CA (every client that trusts the old one must re-trust it)", certPath, keyPath)
		}
		return nil, nil, nil, fmt.Errorf("tlsx: cannot read %s: %w", keyPath, err)
	}

	cert, err := parseFirstCertificate(certPEM)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("tlsx: %s is not a readable certificate: %w (delete it and %s to regenerate)", certPath, err, keyPath)
	}
	key, err := parseECKey(keyPEM)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("tlsx: %s is not a readable private key: %w", keyPath, err)
	}
	return cert, key, certPEM, nil
}

func loadLeaf(dir string) (tls.Certificate, *x509.Certificate, error) {
	certPath := filepath.Join(dir, serverCertFile)
	keyPath := filepath.Join(dir, serverKeyFile)

	if _, err := os.Stat(certPath); err != nil {
		if os.IsNotExist(err) {
			return tls.Certificate{}, nil, nil
		}
		return tls.Certificate{}, nil, fmt.Errorf("tlsx: cannot read %s: %w", certPath, err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		if os.IsNotExist(err) {
			return tls.Certificate{}, nil, nil
		}
		return tls.Certificate{}, nil, fmt.Errorf("tlsx: cannot read %s: %w", keyPath, err)
	}

	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("tlsx: %s and %s are not a usable pair: %w "+
			"(delete both to regenerate; the local CA is untouched)", certPath, keyPath, err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("tlsx: %s is not a readable certificate: %w", certPath, err)
	}
	pair.Leaf = leaf
	return pair, leaf, nil
}

func generateCA(dir string, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("tlsx: cannot generate a CA key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "polyemesis local CA",
			Organization: []string{"polyemesis"},
		},
		// Backdate slightly so a client whose clock runs behind ours does not
		// reject a certificate we minted moments ago.
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("tlsx: cannot create the CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("tlsx: cannot parse the CA certificate we just made: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM, err := encodeECKey(key)
	if err != nil {
		return nil, nil, nil, err
	}
	// Key first: if the write of the certificate fails, the next start finds
	// no certificate and mints a fresh pair, rather than finding a certificate
	// whose key never landed.
	if err := writeFileAtomic(filepath.Join(dir, caKeyFile), keyPEM, keyPerm); err != nil {
		return nil, nil, nil, err
	}
	if err := writeFileAtomic(filepath.Join(dir, caCertFile), certPEM, certPerm); err != nil {
		return nil, nil, nil, err
	}
	return cert, key, certPEM, nil
}

func generateLeaf(dir string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, caPEM []byte, dnsNames []string, ips []net.IP, now time.Time) (tls.Certificate, *x509.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("tlsx: cannot generate a server key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return tls.Certificate{}, nil, err
	}

	cn := dnsNames[0]
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   cn,
			Organization: []string{"polyemesis"},
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(leafValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("tlsx: cannot create the server certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	chainPEM := append(append([]byte{}, certPEM...), caPEM...)
	keyPEM, err := encodeECKey(key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	if err := writeFileAtomic(filepath.Join(dir, serverKeyFile), keyPEM, keyPerm); err != nil {
		return tls.Certificate{}, nil, err
	}
	if err := writeFileAtomic(filepath.Join(dir, serverCertFile), chainPEM, certPerm); err != nil {
		return tls.Certificate{}, nil, err
	}

	pair, err := tls.X509KeyPair(chainPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("tlsx: the server certificate we just made does not match its key: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("tlsx: cannot parse the server certificate we just made: %w", err)
	}
	pair.Leaf = leaf
	return pair, leaf, nil
}

// newSerial draws a 128-bit random serial, the same width public CAs use.
func newSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("tlsx: cannot generate a certificate serial: %w", err)
	}
	return serial, nil
}

func encodeECKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("tlsx: cannot encode the private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func parseECKey(keyPEM []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		ec, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("key is %T, want an ECDSA key", key)
		}
		return ec, nil
	}
	// Material written by an older or hand-rolled tool may still be SEC1.
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("not a PKCS#8 or SEC1 ECDSA key")
	}
	return key, nil
}

// parseFirstCertificate returns the first CERTIFICATE block, ignoring any
// other block type. Feeding it a bundle that leads with a private key — which
// is exactly what autocert caches — parses the certificate and never the key.
func parseFirstCertificate(pemBytes []byte) (*x509.Certificate, error) {
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, fmt.Errorf("no CERTIFICATE block found")
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		return x509.ParseCertificate(block.Bytes)
	}
}
