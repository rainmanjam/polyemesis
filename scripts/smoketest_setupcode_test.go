package main

// The smoke driver has to present the first-run setup code, or POST /setup is
// refused with 403 and the whole broadcast step fails on every matrix OS. These
// pin where it finds the code.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSmokeSetupCodePrefersTheVariable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "setup-code"), []byte("code-from-the-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POLYEMESIS_SETUP_CODE", " code-from-the-env ")
	if got := smokeSetupCode(dir); got != "code-from-the-env" {
		t.Fatalf("smokeSetupCode = %q, want the variable's value", got)
	}
}

func TestSmokeSetupCodeFallsBackToTheServersFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "setup-code"), []byte("code-from-the-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POLYEMESIS_SETUP_CODE", "")
	if got := smokeSetupCode(dir); got != "code-from-the-file" {
		t.Fatalf("smokeSetupCode = %q, want the code the server wrote", got)
	}
}

func TestSmokeSetupCodeIsEmptyWithNeither(t *testing.T) {
	t.Setenv("POLYEMESIS_SETUP_CODE", "")
	if got := smokeSetupCode(t.TempDir()); got != "" {
		t.Fatalf("smokeSetupCode = %q, want empty", got)
	}
}
