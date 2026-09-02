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
	"strconv"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/fsperm"
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
	// Transcription pins the optional whisper.cpp binary. Every field is
	// optional and an empty Binary means "look on $PATH"; a machine without
	// whisper installed is an ordinary machine, not a misconfigured one.
	Transcription Transcription `yaml:"transcription"`
	// AddrDefaulted records that nobody chose Addr -- there was no config.yaml,
	// or it carried no addr key -- so the loopback default below is in force.
	// It is not a setting and never appears in config.yaml (`yaml:"-"`); it is
	// how InsecureExposureWarning tells "bound to loopback on purpose, behind a
	// proxy" apart from "bound to loopback because nobody said otherwise", which
	// are the same address and need opposite messages.
	AddrDefaulted bool `yaml:"-"`
	// TrustProxyHeaders makes the server honour X-Forwarded-Proto when
	// deciding whether to set the Secure flag on the session cookie. Only
	// enable it when polyemesis really is behind a reverse proxy, otherwise a
	// client can forge the header.
	TrustProxyHeaders bool `yaml:"trustProxyHeaders"`
}

// An `enhancedRtmp` key used to live here as a declared-but-inert placeholder
// for OBS 30.2+ multitrack FLV ingest, on the stated grounds that it had to
// survive "so config files that already carry the key keep parsing".
//
// That reason was not true. Load uses yaml.Unmarshal, not a decoder with
// KnownFields(true), so an unrecognised key is ignored rather than rejected --
// pinned by TestOldConfigWithEnhancedRtmpStillParses. The field was therefore
// buying nothing, while presenting a settable knob that did nothing, which is
// the failure mode the settings drift guards exist to prevent.
//
// Enhanced RTMP is still not implemented and RTMP ingest is single-track either
// way; SRT is the multitrack path. An old config carrying the key keeps
// loading, and now it is ignored for the same reason any unknown key is.

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

// Transcription pins the optional whisper.cpp CLI. It is deliberately not
// validated: whisper is an optional external tool, so an unusable path degrades
// transcription and must never stop the server from serving a live stream.
type Transcription struct {
	// Binary is the whisper.cpp CLI (whisper-cli, or the older `main`). Empty
	// means search $PATH.
	Binary string `yaml:"binary"`
}

// DefaultAddr is the listen address used when nobody picks one.
//
// LOOPBACK, NOT ":8080", AND THAT IS A DELIBERATE BREAK. The old default was
// ":8080" -- every interface -- with tls.mode defaulting to off and the session
// cookie's Secure flag therefore unset. So the shipped, do-nothing
// configuration served a login form and its session cookie in cleartext to the
// whole network, and the only thing standing between that and an operator was
// InsecureExposureWarning below: one line, in a banner, on a boot nobody reads
// twice. A log line is rung 0. It announces the exposure to somebody who has
// already been exposed and cannot un-send the password they just typed.
//
// The safest default that does not break a deliberate plaintext install is to
// make the exposure a thing somebody TYPED. An operator who wants plaintext on
// every interface still has it: `addr: "0.0.0.0:8080"` in config.yaml, or
// `--addr :8080` on the command line, and both keep the warning they had. What
// no longer happens is reaching that state by installing the software and
// doing nothing.
//
// WHO THIS DOES NOT TOUCH, which is most people. The Dockerfile's CMD passes
// `-addr :8080`; deploy/polyemesis.service passes `--addr :8080`; install.sh
// writes an addr into the config.yaml it generates; config.example.yaml carries
// `addr: ":8080"`. A flag or a file key wins over this, so every one of those
// paths binds exactly what it bound before.
//
// WHO IT DOES, stated plainly because it is a real cost: an install that has no
// config.yaml, or one with no addr key, and that was being reached from another
// machine over plain HTTP, becomes reachable only from the box itself after
// this upgrade. That operator sees the banner print http://127.0.0.1:8080 and
// the sentence InsecureExposureWarning returns for exactly this case, which
// names the two keys that give them back what they had. It is a one-line fix
// with a printed instruction, and the alternative is leaving a cleartext login
// form on every interface of every default install.
const DefaultAddr = "127.0.0.1:8080"

// Default returns the configuration used when no config.yaml exists.
//
// TLS defaults to off rather than auto: a config file that predates tls.mode
// must keep serving exactly what it served yesterday. New deployments opt in
// by copying config.example.yaml, which ships mode: auto. That is also why the
// bind, not the TLS mode, is what moved: turning TLS on by default would swap a
// working plaintext install for a certificate warning, whereas narrowing the
// bind leaves the protocol alone.
func Default() Config {
	return Config{
		Addr:          DefaultAddr,
		AddrDefaulted: true,
		DataDir:       "./data",
		TLS:           TLS{Mode: ModeOff},
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
	// Same trick for addr, and for the same reason: the default has to be
	// applied AFTER the file is read, or "the file said nothing" and "the file
	// said 127.0.0.1:8080" become the same state and AddrDefaulted lies.
	cfg.Addr = ""
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.AddrDefaulted = strings.TrimSpace(cfg.Addr) == ""
	if cfg.AddrDefaulted {
		cfg.Addr = DefaultAddr
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
	// The other half of the loopback default, and the half that keeps it from
	// being a silent break. An operator who upgrades and finds the box
	// unreachable from their laptop needs the reason in the same eight lines
	// that used to tell them the URL. Gated on the address still BEING the
	// default: someone who typed 127.0.0.1 themselves, or passed --addr with a
	// loopback host, chose this and is not told anything.
	if c.AddrDefaulted && c.Addr == DefaultAddr && !c.ServesTLS() {
		return fmt.Sprintf("no addr is configured, so polyemesis is listening on %s and is "+
			"reachable only from this machine. That is the default because the alternative -- "+
			"plaintext on every interface -- puts the login form and the session cookie on the "+
			"network in the clear. To reach it from elsewhere, set tls.mode: auto in config.yaml "+
			"(recommended), or set addr: \":8080\" to keep serving plain HTTP everywhere, or bind "+
			"loopback deliberately behind a reverse proxy and set trustProxyHeaders: true.",
			c.Addr)
	}
	if !BindsPublicly(c.Addr) || c.TrustProxyHeaders || c.ServesTLS() {
		return ""
	}
	return fmt.Sprintf("listening on %s without TLS: passwords and session cookies cross the network in plaintext. Set tls.mode: auto in config.yaml, or bind to 127.0.0.1 and put a reverse proxy in front (then set trustProxyHeaders: true).", c.Addr)
}

// TLSPortWarning returns a message when TLS is on but the listener is not on
// 443, or "" when there is nothing to say.
//
// WHY THIS IS WORTH A LINE. install.sh already asks -- "HTTPS is normally
// served on 443, so browsers reach it without a port" -- and defaults the
// answer to yes, granting CAP_NET_BIND_SERVICE in the unit it writes. So an
// operator who used the installer never sees this.
//
// The one who does is the operator who did NOT: an Ansible role, a hand-written
// unit, a Dockerfile, a compose file copied from a blog post. They set
// tls.mode and get a working server on 8080, and nothing anywhere tells them
// that every person they send the URL to will need to type a port, that
// redirects from :80 land on a port the client did not ask for, and that an
// HSTS policy is being advertised for an authority browsers will not treat as
// canonical. It works, so nobody investigates. That is exactly the shape of
// thing this warning exists for.
//
// NOT FATAL, and deliberately so. A non-standard port is a legitimate choice --
// behind a reverse proxy that terminates nothing and forwards to 8443, or on a
// host where 443 belongs to something else. The operator gets told once, at
// startup, in the same place the FFmpeg and plaintext warnings appear, and is
// left to decide.
//
// It is silent when TLS is off, because then the port is not the problem and
// InsecureExposureWarning above has already said the thing that matters.
func (c Config) TLSPortWarning() string {
	if !c.ServesTLS() {
		return ""
	}
	port := ListenPort(c.Addr)
	if port == "" || port == "443" {
		return ""
	}
	return fmt.Sprintf("TLS is on but the listener is %s, not :443. Browsers reach this server only if every visitor types the port, and http:// redirects will carry it too. Set addr: \":443\" in config.yaml; a service running as a non-root user also needs AmbientCapabilities=CAP_NET_BIND_SERVICE in its unit, which install.sh grants for you. Keep %s if something in front of this box terminates TLS on 443 or the port is deliberate.", c.Addr, port)
}

// ListenPort is the port from an addr like ":8080" or "0.0.0.0:443", or "" when
// there is none to read.
//
// Exported so cmd/polyemesis can share one answer with TLSPortWarning rather
// than keeping a private copy that could disagree with it -- the two are asked
// the same question about the same string and must not drift.
func ListenPort(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return port
	}
	return ""
}

// ListenPortNumber is the TCP port this process serves HTTP on, or 0 when the
// configured addr names no port that can be read as one.
//
// It exists so the settings API can refuse an ingest listener that asked for
// the port the web UI is already answering on. That save used to return 200:
// the port was stored, the settings page drew it green, and the RTMP listener
// then failed to bind on the next reconcile with nothing but one log line to
// say so. Ingest was dead and every screen said it was fine, so the operator
// spent the outage debugging their encoder.
//
// A ZERO IS "DO NOT KNOW", NOT "PORT ZERO". Callers must treat it as "nothing
// is reserved" and let the save through -- refusing on an unreadable addr would
// lock an operator out of their own settings page over a string this function
// merely failed to parse.
func (c Config) ListenPortNumber() int {
	n, err := strconv.Atoi(ListenPort(c.Addr))
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return n
}

// Paths derived from DataDir.

func (c Config) DBPath() string        { return filepath.Join(c.DataDir, "polyemesis.db") }
func (c Config) RecordingsDir() string { return filepath.Join(c.DataDir, "recordings") }
func (c Config) HLSDir() string        { return filepath.Join(c.DataDir, "hls") }

// HLSDirFor is one source's preview directory.
//
// PER SOURCE BECAUSE THE PREVIEW IS A FILESYSTEM RESOURCE and there is one
// engine per source. While every engine wrote into HLSDir() itself, two of them
// could not coexist: startPreviewLocked clears the directory before it starts,
// and stopPreviewLocked clears it on the way out, so an engine becoming the
// default deleted the live playlist of the one it replaced -- and the outgoing
// engine's idle sweep, up to its whole idle window later, deleted the
// replacement's. Both are reachable by reordering sources, which is an ordinary
// operator action.
//
// The bare HLSDir() remains, and is where the legacy unscoped /hls route reads
// from for the default source, so an existing player keeps working.
func (c Config) HLSDirFor(sourceID int64) string {
	return filepath.Join(c.HLSDir(), strconv.FormatInt(sourceID, 10))
}
func (c Config) SecretPath() string { return filepath.Join(c.DataDir, "secret.key") }

// ModelsDir holds downloaded speech models.
//
// Spelled out here rather than calling transcribe.ModelsDir for the same reason
// PlayoutDir does not call playout.DirIn: config is a leaf package and importing
// transcribe would drag ffmpeg and jobs in behind it.
// TestModelsDirMatchesTheTranscribePackage pins the two against each other.
func (c Config) ModelsDir() string { return filepath.Join(c.DataDir, "models", "whisper") }

// PlayoutDir is the public HLS/DASH origin's root, one directory per variant.
//
// Spelled out here rather than calling playout.DirIn so config stays a leaf
// package — importing playout would drag db, ffmpeg and routing in behind it.
// TestPlayoutDirMatchesThePlayoutPackage pins the two against each other.
func (c Config) PlayoutDir() string { return filepath.Join(c.DataDir, "playout") }

// FontsDir holds the fonts text overlays draw with: the two polyemesis embeds
// and writes at startup, and any the operator drops in beside them.
//
// One directory for both, so there is a single resolution rule and the picker
// is just a listing. Spelled out rather than calling ffmpeg.FontsDirName for
// the reason PlayoutDir gives -- config is a leaf package, and importing ffmpeg
// would drag its dependencies in behind it. TestFontsDirMatchesTheFfmpegPackage
// pins the two against each other.
func (c Config) FontsDir() string { return filepath.Join(c.DataDir, "fonts") }

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
		// 0755, not private: these are public typefaces, and the operator has
		// to be able to drop their own in without fighting permissions.
		{c.FontsDir(), 0o755},
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d.path, d.perm); err != nil {
			return fmt.Errorf("create %s: %w", d.path, err)
		}
	}
	// Private keys land in one of these, so they go through fsperm rather than
	// MkdirAll, and only the mode that actually needs them creates one.
	//
	// fsperm.SecureDir, not os.MkdirAll(.., 0o700): a FileMode is a Unix
	// concept and Windows discards it, so the 0700 that used to be here
	// compiled, succeeded, and restricted nothing on that platform. See
	// internal/fsperm.
	var private []string
	switch c.ResolvedTLSMode() {
	case ModeACME:
		// BOTH, parent first. SecureDir creates missing parents but only
		// restricts the leaf, and under ACME the tls/ directory exists solely
		// to hold the acme cache -- leaving it open would be a strange thing to
		// have deliberately arranged.
		private = []string{c.TLSDir(), c.ACMECacheDir()}
	case ModeSelfSigned:
		private = []string{c.TLSDir()}
	}
	for _, p := range private {
		if err := fsperm.SecureDir(p); err != nil {
			return err
		}
	}
	return nil
}
