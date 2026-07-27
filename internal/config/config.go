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
	"os"
	"path/filepath"

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

// TLS configures the optional built-in HTTPS listener. The common deployment
// is a reverse proxy terminating TLS, so this stays off by default.
type TLS struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"certFile"`
	KeyFile  string `yaml:"keyFile"`
}

// FFmpeg lets an operator pin specific binaries instead of relying on $PATH.
type FFmpeg struct {
	Binary string `yaml:"binary"`
	Probe  string `yaml:"probe"`
}

// Default returns the configuration used when no config.yaml exists.
func Default() Config {
	return Config{
		Addr:    ":8080",
		DataDir: "./data",
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
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "./data"
	}
	return cfg, cfg.Validate()
}

// Validate checks the invariants that would otherwise fail confusingly later.
func (c Config) Validate() error {
	if c.TLS.Enabled {
		if c.TLS.CertFile == "" || c.TLS.KeyFile == "" {
			return fmt.Errorf("tls.enabled is true but tls.certFile/tls.keyFile are not both set")
		}
		for _, p := range []string{c.TLS.CertFile, c.TLS.KeyFile} {
			if _, err := os.Stat(p); err != nil {
				return fmt.Errorf("tls: cannot read %s: %w", p, err)
			}
		}
	}
	return nil
}

// Paths derived from DataDir.

func (c Config) DBPath() string        { return filepath.Join(c.DataDir, "polyemesis.db") }
func (c Config) RecordingsDir() string { return filepath.Join(c.DataDir, "recordings") }
func (c Config) HLSDir() string        { return filepath.Join(c.DataDir, "hls") }
func (c Config) SecretPath() string    { return filepath.Join(c.DataDir, "secret.key") }

// EnsureDirs creates the data directory tree.
func (c Config) EnsureDirs() error {
	for _, d := range []string{c.DataDir, c.RecordingsDir(), c.HLSDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}
