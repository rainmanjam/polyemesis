package tlsx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/acme/autocert"
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
	// 0700: the cache holds the account key and every issued private key.
	if err := os.MkdirAll(cacheDir, dirPerm); err != nil {
		return nil, fmt.Errorf("tlsx: cannot create the acme cache %s: %w", cacheDir, err)
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

func normalizeHost(h string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
}
