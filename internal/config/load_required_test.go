package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A --config path that does not exist must not boot a different install.
//
// Load returns Default() on IsNotExist, and that is right for the implicit
// default name -- a fresh box with no config.yaml should start. It is wrong
// for a path the operator typed: a typo then creates ./data, mints a NEW
// secret.key, opens an empty database, binds :8080 in the clear and reopens
// unauthenticated POST /setup, all while looking healthy. Refusing is the
// only rung that stops it; a log line is rung 0. #644.

func TestLoadRequiredRefusesAMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	_, err := LoadRequired(missing)
	if err == nil {
		t.Fatal("LoadRequired returned no error for a path that does not exist")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error does not wrap ErrNotExist, so callers cannot tell absent from malformed: %v", err)
	}
	if got := err.Error(); !containsAll(got, missing) {
		t.Errorf("error does not name the path the operator typed: %q", got)
	}
}

func TestLoadRequiredReadsAnExistingFileLikeLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("addr: \":9999\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadRequired(p)
	if err != nil {
		t.Fatalf("LoadRequired on an existing file: %v", err)
	}
	if cfg.Addr != ":9999" {
		t.Errorf("Addr = %q, want :9999 -- LoadRequired must parse exactly as Load does", cfg.Addr)
	}
}

func TestLoadStillDefaultsForTheImplicitName(t *testing.T) {
	// The control. The fix must not make a fresh install refuse to start.
	//
	// The expected address is DefaultAddr rather than a literal: it is loopback
	// now, not ":8080", because a server with no configuration should not be
	// reachable from the network in the clear. Writing the literal here once
	// meant this control failed the moment that default was tightened -- which
	// is the test being wrong, not the code, and is exactly what integrating
	// two branches surfaced.
	cfg, err := Load(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatalf("Load on the absent default name must default, got: %v", err)
	}
	if cfg.Addr != DefaultAddr {
		t.Errorf("default Addr = %q, want DefaultAddr (%q)", cfg.Addr, DefaultAddr)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !contains(s, p) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
