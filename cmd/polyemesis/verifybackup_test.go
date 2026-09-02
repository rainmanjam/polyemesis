package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The flag exists to say NO. update.sh runs it on the copy it just took and
// refuses the upgrade when it fails, so the branch that matters is the failing
// one -- a check nobody has watched refuse is a check nobody should trust.
// The shapes it must reject are covered in internal/db; these two assert the
// command surface around it: a non-nil error naming the directory, and a line
// on stdout the operator can read. #643.

func TestVerifyBackupCommandRefusesAnUnusableDirectory(t *testing.T) {
	var out strings.Builder
	err := verifyBackup(t.TempDir(), &out)
	if err == nil {
		t.Fatal("verifyBackup accepted a directory with no database in it")
	}
	if !strings.Contains(err.Error(), "not usable") {
		t.Errorf("error does not say the backup is unusable: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("printed a success line for a failed check: %q", out.String())
	}
}

func TestVerifyBackupCommandAcceptsARealBackupAndSaysSo(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "polyemesis.db"))
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.key"), []byte("00"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := verifyBackup(dir, &out); err != nil {
		t.Fatalf("rejected a real backup: %v", err)
	}
	if !strings.Contains(out.String(), "integrity_check") {
		t.Errorf("success line does not say what was checked: %q", out.String())
	}
}
