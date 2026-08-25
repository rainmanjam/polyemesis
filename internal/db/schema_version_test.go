package db

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestOpenStampsAn06xShapedDatabase covers the upgrade path issue #498 exists
// for: an install that predates this stamp entirely, not merely one that
// predates the newest column.
//
// The fixture is a users table in the pre-token_epoch shape (see
// users_migrate_test.go), built with a raw sql.Open the way an operator's
// real 0.6.x binary would have left it -- schema.sql and every Migrate* in
// this package did not exist yet, so PRAGMA user_version was never touched
// and defaults to 0. Open must migrate it AND stamp it, not merely one or
// the other.
func TestOpenStampsAn06xShapedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`
		CREATE TABLE users (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			username      TEXT    NOT NULL UNIQUE,
			password_hash TEXT    NOT NULL,
			created_at    INTEGER NOT NULL,
			updated_at    INTEGER NOT NULL
		);
		INSERT INTO users (username, password_hash, created_at, updated_at)
		VALUES ('admin', 'not-a-real-hash', 1, 1);`); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := old.QueryRow(`PRAGMA user_version`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 0 {
		t.Fatalf("fixture already had user_version %d; this test would prove nothing", before)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a pre-stamp database must migrate, not refuse: %v", err)
	}
	defer d.Close()

	var got int
	if err := d.sql.QueryRow(`PRAGMA user_version`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != currentSchemaVersion {
		t.Fatalf("user_version = %d after Open, want %d: an existing install must end up "+
			"stamped or the NEXT rollback past this one is unprotected the same way 0.7.0's is",
			got, currentSchemaVersion)
	}
}

// TestOpenRefusesADatabaseWrittenByANewerSchema covers the actual device:
// this binary must not silently open, and silently misread, a database a
// future release wrote. Manufactured by opening a real database and then
// bumping user_version past what this binary understands, standing in for
// "a later release ran its own migrations here".
func TestOpenRefusesADatabaseWrittenByANewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")

	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const future = currentSchemaVersion + 41
	if _, err := d.sql.Exec(`PRAGMA user_version = ` + strconv.Itoa(future)); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = Open(path)
	if err == nil {
		t.Fatalf("Open succeeded against a database stamped %d, a version newer than this "+
			"binary's %d; an older binary must refuse, not silently misread it (issue #498)",
			future, currentSchemaVersion)
	}

	got := strconv.Itoa(future)
	want := strconv.Itoa(currentSchemaVersion)
	if !strings.Contains(err.Error(), got) || !strings.Contains(err.Error(), want) {
		t.Fatalf("refusal error %q does not name both the database's version (%s) and this "+
			"binary's (%s); an operator seeing this needs both numbers to know what happened",
			err, got, want)
	}
}

// TestSchemaVersionStampIsIdempotent covers the ordinary case: every Open of
// an already-current database, which is nearly every Open that will ever
// happen, must not error, drift the version, or otherwise behave differently
// on the second run than the first.
func TestSchemaVersionStampIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repeat.db")

	d1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var first int
	if err := d1.sql.QueryRow(`PRAGMA user_version`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := d1.Close(); err != nil {
		t.Fatal(err)
	}

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open of an already-stamped database: %v", err)
	}
	defer d2.Close()
	var second int
	if err := d2.sql.QueryRow(`PRAGMA user_version`).Scan(&second); err != nil {
		t.Fatal(err)
	}

	if first != currentSchemaVersion || second != currentSchemaVersion {
		t.Fatalf("user_version = %d then %d across two opens, want %d both times",
			first, second, currentSchemaVersion)
	}
}
