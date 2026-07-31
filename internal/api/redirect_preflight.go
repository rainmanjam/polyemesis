package api

// Redirect-URI preflight.
//
// redirect_uri_mismatch is the single most common way OAuth setup fails, and it
// fails LATE: the operator registers a URI in the platform console, pastes the
// credentials, clicks Connect, and only then learns the URI was wrong. By that
// point they are debugging an opaque error from someone else's server.
//
// Everything here moves that discovery earlier -- to the moment the URI is
// displayed, before it has been copied anywhere.
//
// Every warning names the exact URI to register. A warning that says only "this
// may be wrong" relocates the problem rather than solving it.
//
// Nothing here blocks. A reverse proxy terminating TLS upstream is
// indistinguishable, from inside this process, from a misconfiguration, and
// refusing to proceed would trap a working deployment in order to protect a
// hypothetical broken one.

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/config"
)

func redirectWarnings(cfg config.Config, r *http.Request, redirectURI string) []string {
	u, err := url.Parse(redirectURI)
	if err != nil || u.Host == "" {
		// Nothing useful to say about a URI we cannot read. Saying nothing
		// beats guessing at what the operator meant.
		return nil
	}

	host := u.Hostname()
	var out []string

	if u.Scheme == "http" && !isLoopbackHost(host) {
		out = append(out, fmt.Sprintf(
			"This redirect URI is plain HTTP: %s. Google rejects non-HTTPS redirect "+
				"URIs outright, and Twitch allows them only for localhost. Serve "+
				"polyemesis over HTTPS, or reach it through a proxy that does, before "+
				"registering it.", redirectURI))
	}

	// Loopback is excluded deliberately. 127.0.0.1 IS a bare IP, and it is also
	// the address the platforms' own documentation tells you to use for local
	// development -- Google names http://127.0.0.1 alongside http://localhost.
	// Warning about the configuration they recommend is how an operator learns
	// to click past these.
	if net.ParseIP(host) != nil && !isLoopbackHost(host) {
		out = append(out, fmt.Sprintf(
			"This redirect URI uses a bare IP address: %s. Google will not accept an "+
				"IP for a web application client. Use a hostname.", redirectURI))
	}

	if configured := strings.TrimSpace(cfg.TLS.Hostname); configured != "" && !strings.EqualFold(configured, host) {
		out = append(out, fmt.Sprintf(
			"You are browsing %s, but this server is configured as %s. Register the "+
				"URI for %s or the connection will fail with redirect_uri_mismatch.",
			host, configured, configured))
	}

	// Only when the headers are NOT trusted. With trustProxyHeaders on, origin()
	// has already reconstructed the browser-visible address from them, so the
	// URI shown is the right one and a warning would be noise.
	if !cfg.TrustProxyHeaders && r.Header.Get("X-Forwarded-Host") != "" {
		out = append(out, fmt.Sprintf(
			"A reverse proxy is forwarding this request, but trustProxyHeaders is off, "+
				"so polyemesis cannot see the address your browser actually used. The "+
				"URI shown (%s) is probably not the one to register.", redirectURI))
	}

	return out
}

// isLoopbackHost reports whether plain HTTP is acceptable for this host.
// Platforms carve out localhost precisely because there is no network hop to
// protect, so warning about it would train an operator to ignore the warning
// that matters.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
