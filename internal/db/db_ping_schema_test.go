package db

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// PING IS WHAT THE HEALTH ENDPOINT ASKS, AND IT WAS AT 0%.
//
// internal/api/health.go sets its fatal flag when this returns an error, so
// this one call is the difference between /healthz reporting a database the
// process cannot read and reporting nothing wrong. It has to answer truthfully
// in both directions: nil on a store that works, and an error on one that does
// not -- a Ping that cannot fail makes the health check decorative.
func TestPingAnswersForAStoreThatWorksAndOneThatDoesNot(t *testing.T) {
	d := testDB(t)
	if err := d.Ping(); err != nil {
		t.Fatalf("Ping on a healthy store = %v, want nil", err)
	}

	// Closing is the reachable way to make the handle unusable without
	// corrupting a file on disk. If Ping still says the store is fine here,
	// /healthz reports a working database for a process that has none.
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := d.Ping(); err == nil {
		t.Fatal("Ping returned nil on a closed store; the health endpoint would " +
			"report a database this process can no longer read as healthy")
	}
}

// AN OLDER BINARY MUST REFUSE A NEWER DATABASE (#498).
//
// A rollback that opens the file anyway is the dangerous case, not the loud
// one: an older binary CAN read it, and does not know what the columns a newer
// release added actually mean. The guard is the only thing between a downgrade
// and a store being written by code that misunderstands it, and its error is
// the only place an operator is told what to do about it.
func TestADatabaseFromANewerReleaseIsRefusedWithSomethingToActOn(t *testing.T) {
	// Built here rather than with testDB, because the path has to be known: the
	// file is stamped as a newer release's and then re-opened.
	tmpl, err := testTemplate()
	if err != nil {
		t.Fatalf("build template: %v", err)
	}
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	if err := os.WriteFile(path, tmpl, 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}

	ahead := currentSchemaVersion + 1
	stamp, err := Open(path, WithPasswordCost(bcrypt.MinCost))
	if err != nil {
		t.Fatalf("open to stamp: %v", err)
	}
	if _, err := stamp.sql.Exec(`PRAGMA user_version = ` + strconv.Itoa(ahead)); err != nil {
		t.Fatalf("stamp user_version: %v", err)
	}
	if err := stamp.Close(); err != nil {
		t.Fatalf("close after stamping: %v", err)
	}

	_, err = Open(path, WithPasswordCost(bcrypt.MinCost))
	if err == nil {
		t.Fatal("an older binary opened a database written by a newer release. It " +
			"can read the file and does not know what the newer columns mean, which " +
			"is how a rollback corrupts a store quietly rather than loudly (#498)")
	}
	// The message is the whole remediation: an operator who sees only "cannot
	// open database" reinstalls nothing and restores nothing.
	for _, want := range []string{strconv.Itoa(ahead), strconv.Itoa(currentSchemaVersion), "newer release", "backup"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q, so it does not tell the operator "+
				"what happened or what to do:\n  %v", want, err)
		}
	}
}

// A DATABASE AT OR BELOW THIS BINARY'S SCHEMA OPENS NORMALLY.
//
// The control for the test above: a guard that refused everything would pass it
// and make the product unstartable.
func TestADatabaseAtThisSchemaOpensNormally(t *testing.T) {
	dir := t.TempDir()
	tmpl, err := testTemplate()
	if err != nil {
		t.Fatalf("build template: %v", err)
	}
	path := filepath.Join(dir, "polyemesis.db")
	if err := os.WriteFile(path, tmpl, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	d, err := Open(path, WithPasswordCost(bcrypt.MinCost))
	if err != nil {
		t.Fatalf("a database at this binary's own schema was refused: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Ping(); err != nil {
		t.Fatalf("Ping after a normal open: %v", err)
	}
}
