package tlsx

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/net/idna"

	"github.com/rainmanjam/polyemesis/internal/fsperm"
)

// newACMEManager builds the autocert manager. It performs no network I/O:
// issuance happens lazily on the first handshake for the configured host.
func newACMEManager(opts Options) (*autocert.Manager, error) {
	host := strings.TrimSuffix(strings.TrimSpace(opts.Hostname), ".")
	if host == "" {
		return nil, errors.New("tlsx: acme mode needs tls.hostname — Let's Encrypt issues for a name, not an address")
	}
	if strings.TrimSpace(opts.ACMEEmail) == "" {
		return nil, errors.New("tlsx: acme mode needs tls.acmeEmail — Let's Encrypt requires a contact for expiry warnings")
	}
	if opts.DataDir == "" {
		return nil, errors.New("tlsx: acme mode needs a data directory to cache certificates in")
	}

	cacheDir := ACMECacheDir(opts.DataDir)
	// The cache holds the ACME account key and every issued private key, so it
	// is restricted to this account.
	//
	// fsperm.SecureDir rather than os.MkdirAll(.., 0o700): a FileMode is
	// discarded on Windows, where that call created a world-readable directory
	// and returned nil. Also note this does NOT rely on config.EnsureDirs
	// having run -- tlsx is reachable without it, so it establishes the
	// property itself rather than assuming somebody else did.
	if err := fsperm.SecureDir(cacheDir); err != nil {
		return nil, fmt.Errorf("tlsx: cannot create the acme cache: %w", err)
	}

	return &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(cacheDir),
		HostPolicy: hostPolicy(host),
		Email:      strings.TrimSpace(opts.ACMEEmail),
	}, nil
}

// hostPolicy pins issuance to the single configured name.
//
// autocert will happily request a certificate for whatever SNI arrives if you
// let it, which turns a public port into a way for anyone to burn this box's
// Let's Encrypt rate limit — five failures per account per hostname per hour
// is not much of a budget. So: one name, exact match, nothing else.
func hostPolicy(hostname string) autocert.HostPolicy {
	want := normalizeHost(hostname)
	return func(_ context.Context, host string) error {
		if normalizeHost(host) != want {
			return fmt.Errorf("tlsx: refusing to request a certificate for %q; this server is configured as %q", host, hostname)
		}
		return nil
	}
}

// normalizeHost puts a name into the one form both sides of the policy can be
// compared in.
//
// PUNYCODE, NOT JUST LOWERCASE. Lowercasing alone compares two spellings of the
// same name and calls them different: an operator configures muenchen with an
// umlaut, and the browser connecting to it sends SNI already converted to
// xn--... . The policy then refuses to request a certificate for the very
// hostname it was configured with, and reports the punycode back at an operator
// who never typed it.
//
// This is what autocert's own HostWhitelist does, and the reason hostPolicy has
// to repeat it is that hostPolicy exists to be stricter than a whitelist, not
// differently spelled.
//
// A name that will not convert falls back to lowercase rather than failing: the
// Lookup profile is deliberately strict, and a name it rejects should be turned
// away by the comparison below with a clear message, not by an error here.
func normalizeHost(h string) string {
	h = strings.TrimSuffix(strings.TrimSpace(h), ".")
	if ascii, err := idna.Lookup.ToASCII(h); err == nil {
		return ascii
	}
	return strings.ToLower(h)
}
