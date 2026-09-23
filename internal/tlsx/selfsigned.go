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
	// caReplaced says why an existing CA was just thrown away and a new one
	// minted, or is empty when the CA on disk was kept or there was none. A
	// replaced CA is one every client has to be told to trust again, so this
	// is surfaced rather than left for the operator to discover as a browser
	// warning.
	caReplaced string
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

	dnsNames, ips := sansFor(hostname)
	permittedDNS, permittedIPs := caConstraints(dnsNames, ips)

	replaced := caReplacementReason(caCert, permittedDNS, permittedIPs, now())
	if caCert == nil || replaced != "" {
		caCert, caKey, caPEM, err = generateCA(dir, dnsNames[0], permittedDNS, permittedIPs, now())
		if err != nil {
			return nil, err
		}
	}

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

	return &material{pair: leafPair, leaf: leaf, caCert: caCert, caPEM: caPEM, caReplaced: replaced}, nil
}

// caReplacementReason says why the CA on disk must be replaced, or "" when it
// can stay. A missing CA is not a replacement: minting the first one asks
// nobody to re-trust anything.
//
// THE CA IS LIMITED TO THE NAMES THIS BOX IS REACHED BY. It goes into the
// system trust store of the operator's own laptop, and its key sits in
// dataDir on an internet-facing server -- readable by anyone with a shell
// there (expert mode is shell-equivalent), in every backup, on a stolen disk.
// An unconstrained CA turns any of those into a certificate for the
// operator's bank that their browser accepts. Name constraints, marked
// critical, cap what that key can ever vouch for at this box.
//
// The price is that the CA follows tls.hostname: a CA constrained to the old
// name cannot sign for the new one, so changing the hostname mints a new CA
// that clients must trust again. That trade is deliberate. The alternative --
// one CA wide enough for any future name -- is the unconstrained CA again.
func caReplacementReason(ca *x509.Certificate, permittedDNS []string, permittedIPs []*net.IPNet, now time.Time) string {
	switch {
	case ca == nil:
		return ""
	case expiringBy(ca, now, renewWindow):
		return "the previous local CA was about to expire"
	case len(ca.PermittedDNSDomains) == 0 && len(ca.PermittedIPRanges) == 0:
		// Every CA a release before name constraints minted. Kept, it would
		// go on being able to sign for any site for the rest of its ten years.
		return "the previous local CA had no name constraints and could vouch for any site"
	case !constraintsMatch(ca, permittedDNS, permittedIPs):
		return "tls.hostname changed and the previous local CA was limited to the old name"
	}
	return ""
}

// caConstraints turns the leaf's SAN set into the CA's permitted subtrees.
//
// Lower-cased, because DNS names are case-insensitive and a CA replaced over
// a retyped capital letter would be a re-trust for nothing. Each address is
// a single-host range: the CA may vouch for exactly the addresses the leaf
// carries, not their neighbours.
func caConstraints(dnsNames []string, ips []net.IP) ([]string, []*net.IPNet) {
	dns := make([]string, 0, len(dnsNames))
	for _, n := range dnsNames {
		dns = append(dns, strings.ToLower(n))
	}
	nets := make([]*net.IPNet, 0, len(ips))
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			nets = append(nets, &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)})
			continue
		}
		nets = append(nets, &net.IPNet{IP: ip.To16(), Mask: net.CIDRMask(128, 128)})
	}
	return dns, nets
}

// constraintsMatch reports whether ca carries exactly these constraints,
// critically. Exactly, not "at least": a CA still permitted to sign for a
// hostname the operator has since abandoned is wider than this box needs.
func constraintsMatch(ca *x509.Certificate, permittedDNS []string, permittedIPs []*net.IPNet) bool {
	if !ca.PermittedDNSDomainsCritical {
		return false
	}
	return sameSet(lowered(ca.PermittedDNSDomains), permittedDNS) &&
		sameSet(cidrs(ca.PermittedIPRanges), cidrs(permittedIPs))
}

func lowered(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	return out
}

func cidrs(in []*net.IPNet) []string {
	out := make([]string, len(in))
	for i, n := range in {
		out[i] = n.String()
	}
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	have := make(map[string]bool, len(a))
	for _, s := range a {
		have[s] = true
	}
	for _, s := range b {
		if !have[s] {
			return false
		}
	}
	return true
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

func generateCA(dir, primaryName string, permittedDNS []string, permittedIPs []*net.IPNet, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
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
			// The name it serves is in the subject so that, in a keychain
			// holding this CA and the one it replaced, the operator can tell
			// which is which and remove the right one.
			CommonName:   "polyemesis local CA (" + primaryName + ")",
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
		// See caReplacementReason. Critical: a client that does not
		// understand name constraints must refuse this CA rather than
		// silently trust it for everything.
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         permittedDNS,
		PermittedIPRanges:           permittedIPs,
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
