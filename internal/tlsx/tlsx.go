// Package tlsx turns a resolved TLS mode into a *tls.Config the server can
// serve, and answers "what certificate am I actually presenting?" for the UI.
//
// It deliberately knows nothing about config.yaml. The caller resolves
// tls.mode: auto down to one of the concrete modes below and hands the result
// over, which keeps the auto-resolution rules (and their tests) in one place
// and keeps this package a pure certificate concern.
//
// Two rules hold everywhere in here: private key material is written 0600 and
// is never logged, returned or serialised; and every failure is an error with
// a path and a remedy in it, never a panic. The caller is expected to degrade
// to plain HTTP on error rather than refuse to start — an operator locked out
// of the UI cannot fix the certificate that locked them out.
package tlsx

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// Mode is a resolved TLS mode: "auto" has already been decided by the caller.
type Mode string

const (
	// ModeOff serves plain HTTP. Correct behind a TLS-terminating proxy.
	ModeOff Mode = "off"
	// ModeACME obtains a publicly trusted certificate from Let's Encrypt.
	ModeACME Mode = "acme"
	// ModeSelfSigned uses a locally generated CA and leaf, for homelab boxes
	// with no public DNS name.
	ModeSelfSigned Mode = "selfsigned"
	// ModeManual serves certificate files supplied by the operator.
	ModeManual Mode = "manual"
)

// Valid reports whether m is a mode this package can act on. "auto" is not:
// resolving it is the caller's job.
func (m Mode) Valid() bool {
	switch m {
	case ModeOff, ModeACME, ModeSelfSigned, ModeManual:
		return true
	}
	return false
}

// ErrNoCertificate is returned by CertInfo when there is nothing to describe:
// TLS is off, or ACME has not completed its first issuance yet.
var ErrNoCertificate = errors.New("tlsx: no certificate is available yet")

// Options is everything New needs. Mode is already resolved.
type Options struct {
	Mode Mode
	// Hostname is the DNS name (or IP literal) this server is reached by.
	// Required for acme, used as the primary SAN for selfsigned.
	Hostname string
	// ACMEEmail is the account contact address. Required for acme.
	ACMEEmail string
	// CertFile and KeyFile are the operator's own files, manual mode only.
	CertFile string
	KeyFile  string
	// DataDir is the server data directory; generated material lives in a
	// tls/ subdirectory of it.
	DataDir string

	// now is a seam so the expiry and renewal tests do not have to wait a
	// year. Zero means time.Now.
	now func() time.Time
}

func (o Options) clock() func() time.Time {
	if o.now != nil {
		return o.now
	}
	return time.Now
}

// Provider is a ready-to-serve TLS setup plus the introspection the UI needs.
// The zero value is not usable; call New.
type Provider struct {
	mode     Mode
	hostname string
	conf     *tls.Config
	now      func() time.Time

	// leaf is the certificate being served, when we can know it up front.
	// ACME issues lazily, so it stays nil there and CertInfo reads the cache.
	leaf *x509.Certificate

	// caPEM and caCert describe the locally generated CA, so the UI can offer
	// it for download and show the fingerprint the user is trusting.
	caPEM  []byte
	caCert *x509.Certificate

	acme *autocert.Manager
}

// Dir is where generated TLS material lives under a data directory.
func Dir(dataDir string) string { return filepath.Join(dataDir, "tls") }

// ACMECacheDir is autocert's cache location. Exported because it is part of
// the deployment contract: back this directory up and a redeploy will not
// re-issue, which matters against Let's Encrypt rate limits.
func ACMECacheDir(dataDir string) string { return filepath.Join(Dir(dataDir), "acme") }

// New builds a Provider for an already-resolved mode.
func New(opts Options) (*Provider, error) {
	if !opts.Mode.Valid() {
		return nil, fmt.Errorf("tlsx: unknown mode %q (want off, acme, selfsigned or manual)", opts.Mode)
	}

	p := &Provider{mode: opts.Mode, hostname: opts.Hostname, now: opts.clock()}

	switch opts.Mode {
	case ModeOff:
		return p, nil

	case ModeManual:
		if err := p.initManual(opts); err != nil {
			return nil, err
		}

	case ModeSelfSigned:
		if err := p.initSelfSigned(opts); err != nil {
			return nil, err
		}

	case ModeACME:
		if err := p.initACME(opts); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (p *Provider) initManual(opts Options) error {
	if opts.CertFile == "" || opts.KeyFile == "" {
		return errors.New("tlsx: manual mode needs both tls.certFile and tls.keyFile")
	}
	pair, err := tls.LoadX509KeyPair(opts.CertFile, opts.KeyFile)
	if err != nil {
		// LoadX509KeyPair's own error names at most one of the two files, and
		// a mismatched pair is the single most common way this goes wrong.
		return fmt.Errorf("tlsx: cannot load the certificate %s with the key %s: %w", opts.CertFile, opts.KeyFile, err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("tlsx: certificate %s is unreadable: %w", opts.CertFile, err)
	}
	pair.Leaf = leaf

	conf := baseTLSConfig()
	conf.Certificates = []tls.Certificate{pair}
	p.conf = conf
	p.leaf = leaf
	return nil
}

func (p *Provider) initSelfSigned(opts Options) error {
	if opts.DataDir == "" {
		return errors.New("tlsx: selfsigned mode needs a data directory to persist the CA in")
	}
	mat, err := ensureSelfSigned(Dir(opts.DataDir), opts.Hostname, p.now)
	if err != nil {
		return err
	}
	conf := baseTLSConfig()
	conf.Certificates = []tls.Certificate{mat.pair}
	p.conf = conf
	p.leaf = mat.leaf
	p.caCert = mat.caCert
	p.caPEM = mat.caPEM
	return nil
}

func (p *Provider) initACME(opts Options) error {
	m, err := newACMEManager(opts)
	if err != nil {
		return err
	}
	conf := baseTLSConfig()
	conf.GetCertificate = m.GetCertificate
	// Advertising the ACME ALPN protocol lets Let's Encrypt validate over 443
	// alone. It is the fallback that keeps issuance working on a box where
	// port 80 is taken or firewalled.
	conf.NextProtos = append(conf.NextProtos, acme.ALPNProto)
	p.conf = conf
	p.acme = m
	return nil
}

// baseTLSConfig pins the handshake policy. Go's server default already floors
// at TLS 1.2, but stating it here means a future default change cannot quietly
// alter what this server accepts, and an auditor can read the policy in one
// place instead of inferring it from the toolchain version.
func baseTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// X25519 first: it is the fastest and the most widely implemented.
		// P-256 and P-384 follow for clients that predate it.
		CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384},
		NextProtos:       []string{"h2", "http/1.1"},
	}
}

// Mode returns the resolved mode this Provider was built for.
func (p *Provider) Mode() Mode { return p.mode }

// Enabled reports whether there is anything to serve over TLS.
func (p *Provider) Enabled() bool { return p.conf != nil }

// TLSConfig returns the config to hand to http.Server, or nil when TLS is off.
func (p *Provider) TLSConfig() *tls.Config { return p.conf }

// CACertificatePEM returns the locally generated CA certificate, PEM encoded,
// for the UI to offer as a download. It is nil in every mode but selfsigned:
// there is no local CA to trust when a real one issued the certificate.
//
// This is a certificate, never a key. The CA private key never leaves disk.
func (p *Provider) CACertificatePEM() []byte {
	if p.caPEM == nil {
		return nil
	}
	out := make([]byte, len(p.caPEM))
	copy(out, p.caPEM)
	return out
}

// CAFingerprint is the SHA-256 fingerprint of the local CA certificate, so a
// user installing it can check they are trusting the box in front of them and
// not something that intercepted the download. Empty outside selfsigned mode.
func (p *Provider) CAFingerprint() string {
	if p.caCert == nil {
		return ""
	}
	return fingerprint(p.caCert.Raw)
}

// CertInfo describes the certificate currently being served.
func (p *Provider) CertInfo() (CertInfo, error) {
	if p.leaf != nil {
		info := Inspect(p.leaf, p.now())
		// A leaf signed by our own local CA is not literally self-issued, but
		// for the UI's purposes — "will a browser complain?" — it is the same
		// thing, and saying otherwise would be a lie of omission.
		if p.mode == ModeSelfSigned {
			info.SelfSigned = true
		}
		return info, nil
	}
	if p.acme != nil {
		return p.acmeCertInfo()
	}
	return CertInfo{}, ErrNoCertificate
}

// acmeCertInfo reads the issued certificate out of autocert's cache rather
// than calling GetCertificate, which would kick off issuance as a side effect
// of rendering a status card.
func (p *Provider) acmeCertInfo() (CertInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	blob, err := p.acme.Cache.Get(ctx, p.hostname)
	if err != nil {
		if errors.Is(err, autocert.ErrCacheMiss) {
			return CertInfo{}, ErrNoCertificate
		}
		return CertInfo{}, fmt.Errorf("tlsx: reading the acme cache for %s: %w", p.hostname, err)
	}
	// The cached blob leads with the private key. InspectPEM skips every
	// non-certificate block, so no key material is parsed or returned.
	info, err := InspectPEM(blob, p.now())
	if err != nil {
		return CertInfo{}, fmt.Errorf("tlsx: the cached acme certificate for %s is unreadable: %w", p.hostname, err)
	}
	return info, nil
}

// HTTPChallengeHandler wraps h with autocert's HTTP-01 responder. Outside ACME
// mode it returns h untouched, so the caller can mount it unconditionally.
func (p *Provider) HTTPChallengeHandler(h http.Handler) http.Handler {
	if p.acme == nil {
		return h
	}
	return p.acme.HTTPHandler(h)
}

// ServeChallenge starts the HTTP-01 responder on addr, normally ":80".
//
// The bind is done here rather than inside a goroutine so that "port 80 is
// already taken" comes back as an error the caller can log and carry on from.
// Refusing to start would leave the operator with no UI in which to fix the
// setting that broke startup.
func (p *Provider) ServeChallenge(addr string) (*http.Server, error) {
	if p.acme == nil {
		return nil, fmt.Errorf("tlsx: no http-01 challenge to serve in %s mode", p.mode)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tlsx: acme needs %s for the http-01 challenge but cannot bind it: %w "+
			"(free the port, grant CAP_NET_BIND_SERVICE, or forward it to this host); "+
			"certificate issuance will keep failing until it is reachable", addr, err)
	}
	srv := &http.Server{
		Handler:           p.acme.HTTPHandler(nil),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	return srv, nil
}

// fingerprint renders a SHA-256 digest the way certificate viewers do, so a
// user can compare it against their browser without reformatting it.
func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// writeFileAtomic writes via a temp file in the same directory and renames.
//
// A half-written key from a power cut would otherwise brick HTTPS on the next
// boot with material that is present but unparsable, which is precisely the
// case this package refuses to silently regenerate over.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	// CreateTemp makes the file 0600, so key bytes are never briefly readable
	// by anyone else even before the Chmod below.
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp")
	if err != nil {
		return fmt.Errorf("tlsx: cannot create a temporary file in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if err := f.Chmod(perm); err != nil {
		f.Close()
		return fmt.Errorf("tlsx: cannot set permissions on %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("tlsx: cannot write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("tlsx: cannot flush %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("tlsx: cannot close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("tlsx: cannot install %s: %w", path, err)
	}
	return nil
}
