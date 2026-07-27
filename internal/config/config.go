// Package config holds deployment-time configuration: the things you must
// know before the database is open.
//
// Everything a user can change from the web UI (ingest ports, recording
// retention, platform credentials) lives in SQLite instead — see internal/db.
// The split matters: config.yaml is owned by whoever deploys the box, settings
// are owned by whoever streams from it.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the on-disk config.yaml.
type Config struct {
	// Addr is the HTTP listen address for the API and the embedded UI.
	Addr string `yaml:"addr"`
	// DataDir holds polyemesis.db, recordings/, hls/ and the server secret.
	DataDir string `yaml:"dataDir"`
	TLS     TLS    `yaml:"tls"`
	FFmpeg  FFmpeg `yaml:"ffmpeg"`
	// TrustProxyHeaders makes the server honour X-Forwarded-Proto when
	// deciding whether to set the Secure flag on the session cookie. Only
	// enable it when polyemesis really is behind a reverse proxy, otherwise a
	// client can forge the header.
	TrustProxyHeaders bool `yaml:"trustProxyHeaders"`
	// EnhancedRTMP is a placeholder for OBS 30.2+ multitrack FLV ingest, which
	// is not implemented. No code branches on it and no endpoint reports it, so
	// setting it has no effect; it survives only so config files that already
	// carry the key keep parsing.
	EnhancedRTMP bool `yaml:"enhancedRtmp"`
}

// Mode selects how the built-in HTTPS listener obtains its certificate.
type Mode string

const (
	// ModeAuto picks one of the concrete modes below at startup; see Resolve.
	ModeAuto Mode = "auto"
	// ModeACME obtains a real certificate from Let's Encrypt over HTTP-01.
	ModeACME Mode = "acme"
	// ModeSelfSigned mints a local CA and a leaf for Hostname.
	ModeSelfSigned Mode = "selfsigned"
	// ModeManual serves an operator-supplied CertFile/KeyFile pair.
	ModeManual Mode = "manual"
	// ModeOff serves plaintext HTTP; something else is expected to do TLS.
	ModeOff Mode = "off"
)

// Modes lists every accepted value of tls.mode, in the order the docs use.
var Modes = []Mode{ModeAuto, ModeACME, ModeSelfSigned, ModeManual, ModeOff}

// Valid reports whether m is one of the five accepted modes.
func (m Mode) Valid() bool {
	for _, v := range Modes {
		if m == v {
			return true
		}
	}
	return false
}

// TLS configures the built-in HTTPS listener.
//
// Mode is the modern switch. Enabled is the pre-mode boolean and is still
// parsed because existing installs carry it: an upgrade that silently stopped
// serving HTTPS — or that swapped a real certificate for a self-signed one —
// would be a serious regression. See Config.normalizeTLS for the mapping.
type TLS struct {
	Mode Mode `yaml:"mode"`
	// Hostname is the DNS name this server is reached by. ACME issues for it
	// and the self-signed leaf carries it as a SAN.
	Hostname string `yaml:"hostname"`
	// ACMEEmail receives Let's Encrypt expiry warnings. Required for acme.
	ACMEEmail string `yaml:"acmeEmail"`
	CertFile  string `yaml:"certFile"`
	KeyFile   string `yaml:"keyFile"`
	// HSTS is opt-in because Strict-Transport-Security is browser-persistent:
	// sent once from a homelab box it can lock a user out of plain HTTP for
	// that host with no easy undo. Never honoured in selfsigned mode.
	HSTS bool `yaml:"hsts"`
	// Enabled is the legacy on/off switch, kept for backwards compatibility.
	Enabled bool `yaml:"enabled"`
}

// FFmpeg lets an operator pin specific binaries instead of relying on $PATH.
type FFmpeg struct {
	Binary string `yaml:"binary"`
	Probe  string `yaml:"probe"`
}

// Default returns the configuration used when no config.yaml exists.
//
// TLS defaults to off rather than auto: a config file that predates tls.mode
// must keep serving exactly what it served yesterday. New deployments opt in
// by copying config.example.yaml, which ships mode: auto.
func Default() Config {
	return Config{
		Addr:    ":8080",
		DataDir: "./data",
		TLS:     TLS{Mode: ModeOff},
	}
}

// Load reads config.yaml, falling back to defaults if the file is absent.
// A malformed file is an error: silently running on defaults after the
// operator wrote a config would be worse than refusing to start.
func Load(path string) (Config, error) {
	cfg := Default()

	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	// Zero the mode before unmarshalling so an absent tls.mode is
	// distinguishable from an explicit one and can fall back to tls.enabled.
	cfg.TLS.Mode = ""
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "./data"
	}
	cfg.normalizeTLS()
	return cfg, cfg.Validate()
}

// normalizeTLS maps the legacy tls.enabled boolean onto tls.mode and fills in
// the hostname self-signed issuance needs.
//
// The mapping is the whole backwards-compatibility story:
//
//	enabled: true  + cert/key  -> manual   (keep serving the operator's cert)
//	enabled: false / absent    -> off      (keep serving plaintext)
//
// An explicit tls.mode always wins; tls.enabled is only consulted when mode is
// absent, so a migrated config need not delete the old key.
func (c *Config) normalizeTLS() {
	// Normalise before the fallback so an explicitly empty mode: "" is treated
	// the same as an absent one.
	c.TLS.Mode = Mode(strings.ToLower(strings.TrimSpace(string(c.TLS.Mode))))
	if c.TLS.Mode == "" {
		if c.TLS.Enabled {
			c.TLS.Mode = ModeManual
		} else {
			c.TLS.Mode = ModeOff
		}
	}
	c.TLS.Hostname = strings.TrimSuffix(strings.TrimSpace(c.TLS.Hostname), ".")

	// Pin the hostname now so every later consumer — the SAN in the leaf, the
	// startup banner, the redirect target — agrees on one name.
	if c.TLS.Hostname == "" && c.ResolvedTLSMode() == ModeSelfSigned {
		if h, err := os.Hostname(); err == nil {
			c.TLS.Hostname = strings.TrimSuffix(h, ".")
		}
	}
}

// Resolve turns tls.mode into a concrete mode. Only "auto" does any work:
//
//   - behind a trusted proxy the proxy terminates TLS, so we must not
//   - a public FQDN plus an ACME contact is enough for a real certificate
//   - anything else (a LAN box, an IP, no email) gets a self-signed cert
//
// It never returns ModeAuto.
func (t TLS) Resolve(trustProxy bool) Mode {
	if t.Mode != ModeAuto {
		if t.Mode == "" {
			return ModeOff
		}
		return t.Mode
	}
	if trustProxy {
		return ModeOff
	}
	if IsPublicFQDN(t.Hostname) && t.ACMEEmail != "" {
		return ModeACME
	}
	return ModeSelfSigned
}

// ResolvedTLSMode is Resolve fed with this config's proxy setting.
func (c Config) ResolvedTLSMode() Mode { return c.TLS.Resolve(c.TrustProxyHeaders) }

// ServesTLS reports whether polyemesis itself terminates TLS. Callers deciding
// whether a request is really HTTPS (Secure cookies, OAuth redirect URIs)
// should prefer this over the legacy tls.enabled field.
func (c Config) ServesTLS() bool {
	switch c.ResolvedTLSMode() {
	case ModeACME, ModeSelfSigned, ModeManual:
		return true
	default:
		return false
	}
}

// privateSuffixes are the name suffixes a public CA will never issue for.
var privateSuffixes = []string{".local", ".internal", ".lan", ".home", ".arpa", ".localhost"}

// IsPublicFQDN reports whether host looks like a name Let's Encrypt could
// plausibly validate: dotted, not an IP literal, not a reserved private suffix.
func IsPublicFQDN(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if h == "" || h == "localhost" {
		return false
	}
	if !strings.Contains(h, ".") {
		return false
	}
	if net.ParseIP(h) != nil {
		return false
	}
	for _, s := range privateSuffixes {
		if strings.HasSuffix(h, s) {
			return false
		}
	}
	return true
}

// EffectiveHostname returns the configured hostname, falling back to the OS
// hostname so a self-signed cert can still be minted on a box nobody named.
func (t TLS) EffectiveHostname() (string, error) {
	if h := strings.TrimSpace(t.Hostname); h != "" {
		return strings.TrimSuffix(h, "."), nil
	}
	h, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("tls.hostname is empty and the system hostname is unavailable: %w", err)
	}
	h = strings.TrimSuffix(strings.TrimSpace(h), ".")
	if h == "" {
		return "", fmt.Errorf("tls.hostname is empty and the system hostname is blank")
	}
	return h, nil
}

// Validate checks the invariants that would otherwise fail confusingly later.
func (c Config) Validate() error {
	if !c.TLS.Mode.Valid() {
		return fmt.Errorf("tls.mode %q is not one of %v", c.TLS.Mode, Modes)
	}
	switch c.ResolvedTLSMode() {
	case ModeManual:
		return c.TLS.validateManual()
	case ModeACME:
		return c.TLS.validateACME()
	case ModeSelfSigned:
		if _, err := c.TLS.EffectiveHostname(); err != nil {
			return fmt.Errorf("tls: mode %q needs a name to put in the certificate: %w", ModeSelfSigned, err)
		}
	}
	return nil
}

func (t TLS) validateManual() error {
	if t.CertFile == "" || t.KeyFile == "" {
		return fmt.Errorf("tls: mode %q requires both tls.certFile and tls.keyFile (tls.enabled: true is treated as mode: manual)", ModeManual)
	}
	for _, p := range []string{t.CertFile, t.KeyFile} {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("tls: cannot read %s: %w", p, err)
		}
	}
	return nil
}

func (t TLS) validateACME() error {
	if t.Hostname == "" {
		return fmt.Errorf("tls: mode %q requires tls.hostname (the DNS name Let's Encrypt will validate)", ModeACME)
	}
	if t.ACMEEmail == "" {
		return fmt.Errorf("tls: mode %q requires tls.acmeEmail (Let's Encrypt sends expiry warnings there)", ModeACME)
	}
	if !IsPublicFQDN(t.Hostname) {
		return fmt.Errorf("tls: mode %q needs a public DNS name but tls.hostname is %q; use mode: selfsigned for a LAN or IP-only box", ModeACME, t.Hostname)
	}
	return nil
}

// HSTSPolicy reports whether Strict-Transport-Security may be sent, plus a
// warning to log when the operator asked for it somewhere it must not go.
//
// Refusing HSTS under a self-signed certificate is deliberate: a browser that
// pins a host it cannot validate is a browser that can no longer reach it, and
// the operator has no way to clear that from the server side.
func (c Config) HSTSPolicy() (send bool, warning string) {
	if !c.TLS.HSTS {
		return false, ""
	}
	switch c.ResolvedTLSMode() {
	case ModeACME, ModeManual:
		return true, ""
	case ModeSelfSigned:
		return false, "tls.hsts is set but the certificate is self-signed; HSTS is being suppressed because pinning a host browsers cannot validate can permanently break access to it"
	default:
		return false, "tls.hsts is set but this server is not terminating TLS; HSTS will not be sent"
	}
}

// BindsPublicly reports whether a listen address reaches beyond loopback. An
// empty host (":8080") means every interface, which is the default.
func BindsPublicly(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" || host == "*" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() {
			return true
		}
		return !ip.IsLoopback()
	}
	return true
}

// InsecureExposureWarning returns a message to log loudly, or "" when the
// listener is safe. Serving the UI — and therefore the login form and session
// cookie — in plaintext on every interface is the single biggest practical
// exposure this product has, and it is also the default bind.
func (c Config) InsecureExposureWarning() string {
	if !BindsPublicly(c.Addr) || c.TrustProxyHeaders || c.ServesTLS() {
		return ""
	}
	return fmt.Sprintf("listening on %s without TLS: passwords and session cookies cross the network in plaintext. Set tls.mode: auto in config.yaml, or bind to 127.0.0.1 and put a reverse proxy in front (then set trustProxyHeaders: true).", c.Addr)
}

// Paths derived from DataDir.

func (c Config) DBPath() string        { return filepath.Join(c.DataDir, "polyemesis.db") }
func (c Config) RecordingsDir() string { return filepath.Join(c.DataDir, "recordings") }
func (c Config) HLSDir() string        { return filepath.Join(c.DataDir, "hls") }
func (c Config) SecretPath() string    { return filepath.Join(c.DataDir, "secret.key") }

// PlayoutDir is the public HLS/DASH origin's root, one directory per variant.
//
// Spelled out here rather than calling playout.DirIn so config stays a leaf
// package — importing playout would drag db, ffmpeg and routing in behind it.
// TestPlayoutDirMatchesThePlayoutPackage pins the two against each other.
func (c Config) PlayoutDir() string { return filepath.Join(c.DataDir, "playout") }

// TLS material lives under DataDir so the one directory operators are told to
// back up carries everything the server cannot cheaply regenerate — an ACME
// cache that survives a redeploy is what keeps Let's Encrypt rate limits from
// biting. internal/tlsx writes these files; the names are mirrored here so the
// deployment contract is visible from the config package alone.

// TLSDir holds generated TLS material. Created 0700; private keys are 0600.
func (c Config) TLSDir() string { return filepath.Join(c.DataDir, "tls") }

// ACMECacheDir is autocert's cache: account key and issued certificates.
func (c Config) ACMECacheDir() string { return filepath.Join(c.TLSDir(), "acme") }

func (c Config) SelfSignedCACertPath() string { return filepath.Join(c.TLSDir(), "ca.crt") }
func (c Config) SelfSignedCAKeyPath() string  { return filepath.Join(c.TLSDir(), "ca.key") }
func (c Config) SelfSignedCertPath() string   { return filepath.Join(c.TLSDir(), "server.crt") }
func (c Config) SelfSignedKeyPath() string    { return filepath.Join(c.TLSDir(), "server.key") }

type dirSpec struct {
	path string
	perm os.FileMode
}

// EnsureDirs creates the data directory tree.
func (c Config) EnsureDirs() error {
	dirs := []dirSpec{
		{c.DataDir, 0o755},
		{c.RecordingsDir(), 0o755},
		{c.HLSDir(), 0o755},
		{c.PlayoutDir(), 0o755},
	}
	// Private keys land in these two, so they are 0700 and are only created
	// when the resolved mode actually needs them.
	switch c.ResolvedTLSMode() {
	case ModeACME:
		dirs = append(dirs, dirSpec{c.ACMECacheDir(), 0o700})
	case ModeSelfSigned:
		dirs = append(dirs, dirSpec{c.TLSDir(), 0o700})
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d.path, d.perm); err != nil {
			return fmt.Errorf("create %s: %w", d.path, err)
		}
	}
	return nil
}
