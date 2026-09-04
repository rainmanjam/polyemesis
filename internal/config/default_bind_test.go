package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheDoNothingConfigurationDoesNotBindEveryInterfaceInTheClear is the
// finding, stated as a test: with no config.yaml, polyemesis served the login
// form and its non-Secure session cookie on 0.0.0.0:8080, guarded by a log
// line. A log line is rung 0 -- it announces the exposure to somebody who has
// already typed their password into it.
func TestTheDoNothingConfigurationDoesNotBindEveryInterfaceInTheClear(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "there-is-no-config.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ServesTLS() {
		t.Fatal("precondition failed: the default is supposed to be plaintext")
	}
	if BindsPublicly(cfg.Addr) {
		t.Errorf("the default addr %q reaches beyond loopback while serving plaintext", cfg.Addr)
	}
}

// A config.yaml that carries no addr key is the same "nobody chose" state as no
// file at all, and must land in the same place. This is the case that costs an
// existing install its remote access, so it is pinned rather than left implied.
func TestAConfigFileWithNoAddrKeyGetsTheLoopbackDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("dataDir: \"/srv/polyemesis\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != DefaultAddr {
		t.Errorf("addr = %q, want %q", cfg.Addr, DefaultAddr)
	}
	if !cfg.AddrDefaulted {
		t.Error("AddrDefaulted is false for a file that never mentions addr")
	}
	if warn := cfg.InsecureExposureWarning(); !strings.Contains(warn, "reachable only from this machine") {
		t.Errorf("the operator is not told why the box went local-only: %q", warn)
	}
}

// THE DELIBERATE PLAINTEXT INSTALL STILL WORKS. This is the whole reason the
// device is a default and not a refusal: an operator who writes the address
// down gets the address they wrote, and the warning they always had.
func TestAnExplicitPublicBindIsStillHonouredAndStillWarned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("addr: \"0.0.0.0:8080\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "0.0.0.0:8080" {
		t.Errorf("addr = %q, want the one the operator typed", cfg.Addr)
	}
	if cfg.AddrDefaulted {
		t.Error("AddrDefaulted is true for an addr the file actually names")
	}
	warn := cfg.InsecureExposureWarning()
	if !strings.Contains(warn, "plaintext") {
		t.Errorf("a deliberate public plaintext bind lost its warning: %q", warn)
	}
	if strings.Contains(warn, "reachable only from this machine") {
		t.Errorf("a public bind was described as loopback: %q", warn)
	}
}

// An operator who binds loopback ON PURPOSE -- the reverse-proxy deployment the
// docs recommend -- is not nagged about a decision they made.
func TestADeliberateLoopbackBindIsNotWarnedAbout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "addr: \"127.0.0.1:8080\"\ntrustProxyHeaders: true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if warn := cfg.InsecureExposureWarning(); warn != "" {
		t.Errorf("warned about a bind the operator chose: %q", warn)
	}
}
