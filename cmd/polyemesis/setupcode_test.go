package main

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/auth"
)

// The boot path end to end: a fresh data directory gets a code on disk and in
// the banner, and the same directory with an admin in it gets neither.
func TestBootPrintsTheSetupCodeOnlyWhileNoAdminExists(t *testing.T) {
	t.Setenv(auth.SetupCodeEnv, "")
	cfg, store := resetFixture(t)

	code, err := prepareSetupCode(cfg, store)
	if err != nil {
		t.Fatalf("prepareSetupCode: %v", err)
	}
	var out, logs bytes.Buffer
	reportSetupCode(&out, slog.New(slog.NewTextHandler(&logs, nil)), code)
	if !strings.Contains(out.String(), code.Code()) || !strings.Contains(out.String(), code.Path()) {
		t.Fatalf("banner does not show the code and its file:\n%s", out.String())
	}
	// The structured log points at the file and never carries the code, so
	// the code is in one place in the journal, not in every shipped log line.
	if !strings.Contains(logs.String(), "first run: creating the admin account needs the setup code") ||
		strings.Contains(logs.String(), code.Code()) {
		t.Fatalf("log line wrong or carries the code:\n%s", logs.String())
	}

	// A restart before setup says so rather than looking like a new code.
	again, err := prepareSetupCode(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	reportSetupCode(&out, slog.New(slog.NewTextHandler(io.Discard, nil)), again)
	if !strings.Contains(out.String(), "same code as before") {
		t.Errorf("restart banner does not say the code is unchanged:\n%s", out.String())
	}

	if _, err := store.CreateUser("admin", "a-long-enough-password"); err != nil {
		t.Fatal(err)
	}
	after, err := prepareSetupCode(cfg, store)
	if err != nil || after != nil {
		t.Fatalf("with an admin: code %v, err %v; want none", after, err)
	}
	if _, err := os.Stat(code.Path()); !os.IsNotExist(err) {
		t.Errorf("setup-code file survived a boot with an admin (stat err %v)", err)
	}
	out.Reset()
	reportSetupCode(&out, slog.New(slog.NewTextHandler(io.Discard, nil)), after)
	if out.Len() != 0 {
		t.Errorf("banner printed a setup block on a claimed install:\n%s", out.String())
	}
}

func TestBootNamesAPresetSetupCode(t *testing.T) {
	t.Setenv(auth.SetupCodeEnv, "readable-placeholder-code")
	cfg, store := resetFixture(t)

	code, err := prepareSetupCode(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	reportSetupCode(&out, slog.New(slog.NewTextHandler(io.Discard, nil)), code)
	if !strings.Contains(out.String(), auth.SetupCodeEnv) {
		t.Errorf("banner does not say the code came from %s:\n%s", auth.SetupCodeEnv, out.String())
	}
}

func TestBootReportsAStoreThatCannotSayWhetherAnAdminExists(t *testing.T) {
	cfg, store := resetFixture(t)
	store.Close()
	if _, err := prepareSetupCode(cfg, store); err == nil {
		t.Fatal("no error from a closed store")
	}
}
