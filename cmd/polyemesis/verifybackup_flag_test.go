package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// THE -verify-backup FLAG'S TWO ANSWERS, BOTH ASSERTED.
//
// update.sh runs this immediately before an upgrade overwrites the binary, and
// acts on what it says: a zero exit is taken as permission to proceed. Both
// halves of that contract matter and neither was covered, because the logic sat
// inline in run() where nothing could reach it.
//
// The success MESSAGE is asserted, not just the nil error. update.sh prints it
// as the operator's evidence that a check happened, so a version of this
// function that silently returned nil would satisfy an error-only test while
// telling the operator nothing -- and "backup verified" printed by something
// that verified nothing is the exact failure #643 was filed for.

func goodBackupDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "polyemesis.db"))
	if err != nil {
		t.Fatalf("seeding a database: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.key"), []byte("00"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestVerifyBackupFlagReportsAUsableBackup(t *testing.T) {
	var out bytes.Buffer
	dir := goodBackupDir(t)

	if err := verifyBackup(dir, &out); err != nil {
		t.Fatalf("a real backup was reported unusable: %v", err)
	}

	got := out.String()
	// Each claim separately, because the operator acts on the specific claim
	// and a message that dropped one would still look like a success line.
	for _, want := range []string{dir, "opens", "integrity_check", "schema"} {
		if !strings.Contains(got, want) {
			t.Errorf("the success message does not mention %q, so it no longer says what "+
				"was actually checked: %q", want, got)
		}
	}
}

func TestVerifyBackupFlagRefusesAnUnusableBackupAndSaysWhere(t *testing.T) {
	var out bytes.Buffer
	dir := t.TempDir() // empty: no database, no key

	err := verifyBackup(dir, &out)
	if err == nil {
		t.Fatal("an empty directory was accepted as a backup, which would let update.sh " +
			"proceed with no way back")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("the error does not name the directory, so an operator running update.sh "+
			"cannot tell which path was wrong: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("a failing check still printed to stdout (%q). update.sh shows that stream "+
			"to the operator, so anything on it during a failure reads as reassurance.", out.String())
	}
}
