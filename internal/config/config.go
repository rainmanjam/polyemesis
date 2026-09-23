// Package config holds deployment-time configuration: the things you must
// know before the database is open.
//
// Everything a user can change from the web UI (ingest ports, recording
// retention, platform credentials) lives in SQLite instead — see internal/db.
// The split matters: config.yaml is owned by whoever deploys the box, settings
// are owned by whoever streams from it.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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
	// AddrFromFlag records that Addr came from --addr on the command line, which
	// main applies after config.yaml and which therefore beats it. Like
	// AddrDefaulted it is not a setting. It exists so the startup warnings tell
	// the operator to change the flag -- in the unit's ExecStart or the
	// container's command -- rather than an addr: line the flag overrides.
	// ProxyHeaderWarning uses it too: the answer to "I set addr: 127.0.0.1 and
	// it still listens publicly" is almost always the unit's --addr.
	AddrFromFlag bool `yaml:"-"`
	// TrustProxyHeaders makes the server honour X-Forwarded-Proto when
	// deciding whether to set the Secure flag on the session cookie. Only
	// enable it when polyemesis really is behind a reverse proxy, otherwise a
	// client can forge the header.
	TrustProxyHeaders bool `yaml:"trustProxyHeaders"`
	// TrustedProxies lists the proxies, besides loopback, whose
	// X-Forwarded-For and X-Real-IP are believed when TrustProxyHeaders is on:
	// CIDRs ("172.16.0.0/12") or single addresses ("172.17.0.1"). A request
	// from any other peer is keyed on its own socket address, so a client
	// that reaches the listener directly cannot choose the address the login
	// throttle and the audit log see. Empty means loopback only, which is
	// right for a proxy on the same host and wrong for one in another
	// container or on another machine -- list that one here.
	TrustedProxies []string `yaml:"trustedProxies"`
}

// An `enhancedRtmp` key used to live here as a declared-but-inert placeholder
// for OBS 30.2+ multitrack FLV ingest. Enhanced RTMP is still not implemented
// and RTMP ingest is single-track either way; SRT is the multitrack path. The
// key is now a RETIRED key -- see onDisk -- so an old config that carries it
// keeps loading while a key nobody ever defined stops the server.

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

// UnmarshalYAML decodes the tls block and REFUSES A KEY IT DOES NOT KNOW.
//
// The whole file is decoded with KnownFields(true) now (see onDisk), but
// this block keeps its own check for the sake of the message: inside it,
// leniency had the worst possible failure mode. `mdoe: selfsigned` -- or
// `Mode:`, since yaml keys are case-sensitive -- leaves mode absent,
// normalizeTLS maps absent to off, and the server starts on plain HTTP with
// session cookies missing their Secure flag. On a loopback bind nothing was
// logged at all. A typo that silently turns TLS off is not one to
// warn about; it is one to stop at, naming the key.
//
// Every key this block has ever had is still a field below, so it has no
// retired keys of its own.
func (t *TLS) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.MappingNode {
		known := tlsKeys()
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if _, ok := known[k]; ok {
				continue
			}
			hint := ""
			for name := range known {
				if strings.EqualFold(name, k) {
					hint = fmt.Sprintf(" (did you mean %q? keys are case-sensitive)", name)
				}
			}
			return fmt.Errorf("line %d: tls has no key %q%s. Refusing to start: an "+
				"unrecognised tls key would otherwise be ignored, and a misspelled "+
				"mode falls back to off -- plain HTTP, cookies without Secure. Valid "+
				"keys: %s", n.Content[i].Line, k, hint, strings.Join(tlsKeyList(), ", "))
		}
	}
	// The alias has TLS's fields and none of its methods, so this Decode does
	// not recurse back into UnmarshalYAML.
	type plain TLS
	return n.Decode((*plain)(t))
}

// tlsKeyList is every yaml key TLS declares, read from its struct tags so a
// field added later is accepted without anyone remembering to list it here.
func tlsKeyList() []string { return yamlKeyList(reflect.TypeOf(TLS{})) }

// yamlKeyList is every yaml key a struct type declares, in field order.
func yamlKeyList(rt reflect.Type) []string {
	keys := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("yaml"), ",")
		if name != "" && name != "-" {
			keys = append(keys, name)
		}
	}
	return keys
}

func tlsKeys() map[string]struct{} {
	m := map[string]struct{}{}
	for _, k := range tlsKeyList() {
		m[k] = struct{}{}
	}
	return m
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
// writes an addr into the config.yaml it generates. A flag or a file key wins
// over this, so every one of those paths binds exactly what it bound before.
//
// config.example.yaml USED to carry `addr: ":8080"` and was listed here as
// unaffected, which was true of the binary and false of the operator: copying
// the example is how a new install gets its config, so the hardening above was
// undone by the very file people start from. It now carries
// `127.0.0.1:8080`, and TestTheShippedExampleDoesNotBindEveryInterface keeps
// it that way.
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
	return load(path, false)
}

// LoadRequired is Load for a path the operator typed. An absent file is an
// error rather than a default, because defaulting here does not "run without
// a config" -- it boots a DIFFERENT install: creates ./data, mints a new
// secret.key, opens an empty database, binds :8080 in the clear and reopens
// unauthenticated POST /setup, while looking healthy. A typo in --config must
// stop at the door, naming the path. #644.
func LoadRequired(path string) (Config, error) {
	return load(path, true)
}

func load(path string, required bool) (Config, error) {
	cfg := Default()

	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if required {
				return cfg, fmt.Errorf("config %s: %w (the path was given explicitly, so refusing to start on defaults)", path, err)
			}
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
	if err := decodeStrict(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.TLS.checkHostnameWithoutMode(); err != nil {
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

// onDisk is what config.yaml may contain: every Config key, plus the RETIRED
// ones -- keys the file once had and the server no longer reads -- each held as
// a raw node so its value is accepted and then dropped.
//
// WHY AN ALLOWLIST AND NOT LENIENCY. Load used to be yaml.Unmarshal, which
// ignores any key it does not recognise, and that turned every typo into a
// silent default: `trustProxyhHeaders: true` drops the Secure cookie flag
// behind a proxy, `tsl:` leaves TLS off, `dataDIr:` puts the database in
// ./data. Nothing was logged, because nothing had noticed. Decoding with
// KnownFields(true) makes the typo stop startup and name itself; the fields
// below are what keep that strictness from breaking a file that was valid the
// day it was written. A key removed from Config gets a field here, with the
// release it went in, and the field is never deleted.
type onDisk struct {
	Config `yaml:",inline"`
	// enhancedRtmp: removed in v0.2.0. Enhanced RTMP was never implemented and
	// the key never did anything.
	EnhancedRTMP yaml.Node `yaml:"enhancedRtmp"`
}

// unknownFieldRE matches the one line yaml.v3 writes per unknown key, e.g.
// "line 2: field Binary not found in type config.FFmpeg".
var unknownFieldRE = regexp.MustCompile(`^(line \d+): field (\S+) not found in type (\S+)$`)

// decodeStrict decodes config.yaml into cfg and REFUSES A KEY IT DOES NOT KNOW,
// at any depth. See onDisk for why, and for the retired keys it still accepts.
func decodeStrict(b []byte, cfg *Config) error {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	file := onDisk{Config: *cfg}
	if err := dec.Decode(&file); err != nil {
		// An empty file, or one that is only comments, is a file that set
		// nothing -- which is what yaml.Unmarshal made of it too.
		if errors.Is(err, io.EOF) {
			return nil
		}
		return explainUnknownKey(err)
	}
	*cfg = file.Config
	return nil
}

// explainUnknownKey turns yaml.v3's "field X not found in type config.FFmpeg"
// lines into sentences an operator can act on: the key the server stopped on,
// the block it sits in, a case-insensitive near miss if there is one, and the
// keys that ARE valid at that spot. Any other decode error is returned as it
// came.
//
// WHERE THE KEY SITS DECIDES THE LIST. yaml.v3 names the Go type it was
// decoding into, and that is the only record of the depth: `Binary:` under
// ffmpeg must be matched against ffmpeg's keys -- so the hint is "binary" --
// and listing the top-level keys there would send the operator to the wrong
// place. The Go type names are internal and are not repeated to the operator.
func explainUnknownKey(err error) error {
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		return err
	}
	blocks := keyBlocks()
	lines := make([]string, 0, len(te.Errors))
	matched := false
	for _, line := range te.Errors {
		m := unknownFieldRE.FindStringSubmatch(line)
		if m == nil {
			lines = append(lines, line)
			continue
		}
		matched = true
		lines = append(lines, describeUnknownKey(m[1], m[2], blocks[m[3]]))
	}
	if !matched {
		return err
	}
	return fmt.Errorf("%s. Refusing to start: an unrecognised key would otherwise be "+
		"ignored and its setting silently left at the default",
		strings.Join(lines, "; "))
}

// keyBlock is one place in config.yaml that holds keys: its name as the
// operator writes it ("" for the top level) and the keys valid there.
type keyBlock struct {
	name string
	keys []string
}

// keyBlocks maps each yaml.v3 type name ("config.FFmpeg") to the block it
// decodes. It is derived from Config's own fields, so a new nested block is
// covered the day it is added. tls is listed for completeness; its
// UnmarshalYAML refuses its own unknown keys before this is reached.
func keyBlocks() map[string]keyBlock {
	ct := reflect.TypeOf(Config{})
	top := keyBlock{keys: yamlKeyList(ct)}
	blocks := map[string]keyBlock{
		reflect.TypeOf(onDisk{}).String(): top,
		ct.String():                       top,
	}
	for i := 0; i < ct.NumField(); i++ {
		f := ct.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if f.Type.Kind() != reflect.Struct || name == "" || name == "-" {
			continue
		}
		blocks[f.Type.String()] = keyBlock{name: name, keys: yamlKeyList(f.Type)}
	}
	return blocks
}

// describeUnknownKey writes one unknown key as a sentence. A block the map does
// not know (a zero keyBlock) still names the key and line; it only loses the
// list, rather than borrowing one from the wrong level.
func describeUnknownKey(line, key string, b keyBlock) string {
	where, valid := "at the top level", "Valid top-level keys"
	if b.name != "" {
		where, valid = "under "+b.name, "Valid keys under "+b.name
	}
	if b.keys == nil {
		return fmt.Sprintf("%s: unknown key %q", line, key)
	}
	hint := ""
	for _, name := range b.keys {
		if strings.EqualFold(name, key) {
			hint = fmt.Sprintf(" (did you mean %q? keys are case-sensitive)", name)
		}
	}
	return fmt.Sprintf("%s: unknown key %q %s%s. %s: %s",
		line, key, where, hint, valid, strings.Join(b.keys, ", "))
}

// checkHostnameWithoutMode refuses a tls block that names a host but never
// says how to serve it.
//
// tls.hostname exists to go into a certificate -- ACME issues for it, the
// self-signed leaf carries it -- so writing one down is a statement that this
// server terminates TLS. An absent mode quietly contradicts that: normalizeTLS
// maps it to off, and the server comes up on plain HTTP with cookies missing
// their Secure flag while the operator believes they configured HTTPS. It is
// the misspelled-mode failure with the key deleted rather than mistyped.
//
// ONLY THE ABSENT MODE. An explicit `mode: off` next to a hostname is
// legitimate -- behind a proxy the hostname still names the public origin for
// the OAuth redirect preflight -- and so is `mode: auto` resolving to off under
// trustProxyHeaders; both said something on purpose. A legacy `enabled: true`
// is a mode too (manual), so only the combination that said nothing is refused.
func (t TLS) checkHostnameWithoutMode() error {
	if strings.TrimSpace(string(t.Mode)) != "" || t.Enabled || strings.TrimSpace(t.Hostname) == "" {
		return nil
	}
	return fmt.Errorf("tls.hostname is %q but tls.mode is not set, which means off: this "+
		"server would serve plain HTTP, not a certificate for that name. Set tls.mode "+
		"(auto, acme or selfsigned), or write mode: \"off\" if TLS is terminated in front "+
		"of this server", t.Hostname)
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
	if _, err := c.TrustedProxyPrefixes(); err != nil {
		return err
	}
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
	return fmt.Sprintf("listening on %s without TLS: passwords and session cookies cross the network in plaintext. Set tls.mode: auto in config.yaml, or bind to 127.0.0.1 and put a reverse proxy in front (then set trustProxyHeaders: true).%s", c.Addr, c.addrFlagNote())
}

// addrFlagNote is appended to any advice about the listen address when that
// address came from --addr: the flag beats config.yaml, so an operator who
// follows "set addr: in config.yaml" restarts onto the same port and the same
// warning. Empty when the address came from the file or the default.
func (c Config) addrFlagNote() string {
	if !c.AddrFromFlag {
		return ""
	}
	return fmt.Sprintf(" The listen address %s comes from --addr on the command line -- the "+
		"systemd unit's ExecStart or the container's command -- and that flag overrides addr: "+
		"in config.yaml, so change the address there (sudo systemctl edit --full polyemesis), "+
		"or remove the flag and let config.yaml decide.", c.Addr)
}

// TrustedProxyPrefixes parses TrustedProxies. A bare address is that one
// address; an entry that is neither an address nor a CIDR is an error naming
// it, because a typo here would silently trust nobody -- or somebody else.
func (c Config) TrustedProxyPrefixes() ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(c.TrustedProxies))
	for _, raw := range c.TrustedProxies {
		s := strings.TrimSpace(raw)
		var (
			p   netip.Prefix
			err error
		)
		if strings.Contains(s, "/") {
			p, err = netip.ParsePrefix(s)
			p = p.Masked()
		} else {
			var a netip.Addr
			if a, err = netip.ParseAddr(s); err == nil {
				a = a.Unmap()
				p = netip.PrefixFrom(a, a.BitLen())
			}
		}
		if err != nil {
			return nil, fmt.Errorf("trustedProxies: %q is not an address or a CIDR", raw)
		}
		out = append(out, p)
	}
	return out, nil
}

// ProxyHeaderWarning returns a message when trustProxyHeaders is on but the
// listener can be reached without going through the proxy, or "" otherwise.
//
// Forwarding headers are believed only from loopback and trustedProxies, so a
// direct client can no longer pick its own throttle key -- but a listener that
// is public, or that terminates TLS itself, is still a sign the deployment is
// not the one trustProxyHeaders describes. The usual cause is the systemd
// unit's --addr :8080, which beats addr: 127.0.0.1 in config.yaml, so the
// message says which of the two set the address.
func (c Config) ProxyHeaderWarning() string {
	if !c.TrustProxyHeaders || (!BindsPublicly(c.Addr) && !c.ServesTLS()) {
		return ""
	}
	var why string
	switch {
	case BindsPublicly(c.Addr) && c.AddrFromFlag:
		why = fmt.Sprintf("the listener %s, set by the --addr flag (which overrides addr in config.yaml; "+
			"on a systemd install it is in the unit's ExecStart), is reachable without going through the proxy", c.Addr)
	case BindsPublicly(c.Addr):
		why = fmt.Sprintf("the listener %s, set by addr in config.yaml, is reachable without going through the proxy", c.Addr)
	default:
		why = fmt.Sprintf("this server terminates TLS itself on %s (tls.mode %s), so clients reach it directly", c.Addr, c.ResolvedTLSMode())
	}
	return fmt.Sprintf("trustProxyHeaders is on, but %s. Forwarding headers are believed only from loopback and trustedProxies, "+
		"so direct clients are keyed on their own address; if the reverse proxy is meant to be the only way in, bind 127.0.0.1.", why)
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
	return fmt.Sprintf("TLS is on but the listener is %s, not :443. Browsers reach this server only if every visitor types the port, and http:// redirects will carry it too. Set addr: \":443\" in config.yaml; a service running as a non-root user also needs AmbientCapabilities=CAP_NET_BIND_SERVICE in its unit, which install.sh grants for you. Keep %s if something in front of this box terminates TLS on 443 or the port is deliberate.%s", c.Addr, port, c.addrFlagNote())
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

// PlayoutDirFor is one source's playout root: its master playlist, one
// directory per variant, and the live-caption sidecar.
//
// PER SOURCE FOR THE REASON HLSDirFor IS. Every engine runs its own playout
// manager, and while each was handed PlayoutDir() itself, two programmes with
// the default "main" variant both muxed into <root>/main/: the segments
// overwrote each other, either engine's teardown cleared the other's live
// window, and playout.sourceId -- which picks the engine whose handler serves
// -- chose between two handlers serving the same files.
//
// Siblings under the shared root, never the root itself for one of them: the
// sweeper walks its directory recursively, so a programme nested inside
// another's root would have its window pruned under the other's limit. The
// public URL does not change -- the handler serves relative to this directory.
func (c Config) PlayoutDirFor(sourceID int64) string {
	return filepath.Join(c.PlayoutDir(), strconv.FormatInt(sourceID, 10))
}

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
