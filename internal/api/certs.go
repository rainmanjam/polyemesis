package api

import (
	"errors"
	"net/http"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/tlsx"
)

// tlsStatus is what the Settings page renders for transport security.
//
// Every field is public information about a certificate this server already
// presents to anyone who completes a handshake with it. There is no field for
// key material and there must never be one: the CA private key and the leaf
// key exist only as 0600 files under the data directory and have no route.
type tlsStatus struct {
	// Mode is what tls.mode resolved to; Configured is what config.yaml
	// literally says. The two differ only for mode: auto, and both are sent so
	// an operator can see what auto decided without reading the startup log.
	Mode       config.Mode `json:"mode"`
	Configured config.Mode `json:"configured"`

	Hostname          string `json:"hostname"`
	ServesTLS         bool   `json:"servesTls"`
	TrustProxyHeaders bool   `json:"trustProxyHeaders"`

	// HSTS is the policy decision, not a promise that the header was on this
	// response: the middleware also requires the connection to genuinely be
	// TLS. HSTSWarning is non-empty when tls.hsts was asked for and refused,
	// which is the case the UI has to explain rather than silently drop.
	HSTS        bool   `json:"hsts"`
	HSTSWarning string `json:"hstsWarning"`

	// Certificate is null when there is nothing to describe: TLS is off, or
	// ACME has not completed its first issuance. CertificateError then says
	// which, in words an operator can act on.
	Certificate      *tlsx.CertInfo `json:"certificate"`
	CertificateError string         `json:"certificateError"`

	// CAAvailable and CAFingerprint are set in selfsigned mode only. The
	// fingerprint is here so the user can compare what they are about to trust
	// against what their browser is showing them.
	CAAvailable   bool   `json:"caAvailable"`
	CAFingerprint string `json:"caFingerprint"`
}

// handleTLSStatus describes the certificate the server is presenting.
func (s *Server) handleTLSStatus(w http.ResponseWriter, r *http.Request) {
	send, warning := s.cfg.HSTSPolicy()
	st := tlsStatus{
		Mode:              s.cfg.ResolvedTLSMode(),
		Configured:        s.cfg.TLS.Mode,
		Hostname:          s.cfg.TLS.Hostname,
		ServesTLS:         s.cfg.ServesTLS(),
		TrustProxyHeaders: s.cfg.TrustProxyHeaders,
		HSTS:              send,
		HSTSWarning:       warning,
	}

	// A nil provider means the process is running without one — the config
	// still describes the intent, so report that rather than 500. Startup is
	// allowed to degrade to plain HTTP precisely so the operator keeps a UI in
	// which to fix whatever broke.
	if p := s.tls; p != nil {
		st.CAFingerprint = p.CAFingerprint()
		st.CAAvailable = len(p.CACertificatePEM()) > 0

		info, err := p.CertInfo()
		switch {
		case err == nil:
			st.Certificate = &info
		case errors.Is(err, tlsx.ErrNoCertificate):
			st.CertificateError = noCertificateReason(st.Mode, st.Hostname)
		default:
			st.CertificateError = err.Error()
		}
	} else if st.ServesTLS {
		st.CertificateError = "the certificate provider is not available in this process"
	}

	writeJSON(w, http.StatusOK, st)
}

// noCertificateReason turns "there is no certificate" into the next action.
func noCertificateReason(mode config.Mode, hostname string) string {
	switch mode {
	case config.ModeACME:
		host := hostname
		if host == "" {
			host = "this host"
		}
		return "Let's Encrypt has not issued a certificate yet. Issuance starts on the first HTTPS " +
			"request for " + host + ", and port 80 must reach this machine for the HTTP-01 challenge."
	case config.ModeOff:
		return "polyemesis is serving plain HTTP; nothing terminates TLS here."
	default:
		return "no certificate is loaded."
	}
}

// handleDownloadCA serves the locally generated CA certificate.
//
// This route is deliberately reachable without a session, which is a decision
// rather than an oversight. In selfsigned mode a browser refuses the
// connection until this CA is installed, so on a fresh box the user cannot get
// past the warning to sign in — gating the download behind a session would
// deadlock the only path out of it. And there is nothing to gate: this is the
// public half of a CA certificate, sent in the clear to every client that
// opens a TLS connection to this server, so withholding it protects nothing an
// attacker cannot already read off the wire. The CA private key is a different
// thing entirely; it stays 0600 on disk and no handler can reach it.
func (s *Server) handleDownloadCA(w http.ResponseWriter, r *http.Request) {
	var pem []byte
	if s.tls != nil {
		pem = s.tls.CACertificatePEM()
	}
	if len(pem) == 0 {
		writeError(w, http.StatusNotFound,
			"there is no local certificate authority in "+string(s.cfg.ResolvedTLSMode())+" mode")
		return
	}

	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="polyemesis-ca.crt"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	// The response is already committed; a broken pipe here is the client's.
	_, _ = w.Write(pem)
}
