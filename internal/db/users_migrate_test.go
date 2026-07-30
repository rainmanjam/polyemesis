package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestMigrateUserTokenEpochUpgradesAnExistingInstall covers the upgrade path
// that CI otherwise never exercises: every other test in this package starts
// from a database the current schema just created, so a migration that does not
// work on a real, older database would pass all of them.
//
// The specific hazard for this column is that it is NOT NULL. Adding a NOT NULL
// column to a table with existing rows fails outright unless it carries a
// default, and the default has to be the value a freshly issued token also
// carries — otherwise every operator is signed out by the upgrade, which for a
// live-streaming box means mid-broadcast.
func TestMigrateUserTokenEpochUpgradesAnExistingInstall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	// A users table as it existed before sessions were revocable, with a row
	// in it — an install that has been running with a logged-in operator.
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
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	d := &DB{}
	d.sql, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.sql.Close()

	if has, err := columnExists(d.sql, "users", "token_epoch"); err != nil || has {
		t.Fatalf("the fixture already had token_epoch (has=%v, err=%v); "+
			"this test would prove nothing", has, err)
	}

	if err := d.MigrateUserTokenEpoch(); err != nil {
		t.Fatalf("MigrateUserTokenEpoch on an existing install: %v", err)
	}

	has, err := columnExists(d.sql, "users", "token_epoch")
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("token_epoch was not added")
	}

	// The pre-existing row must have the same epoch a new token gets, or the
	// upgrade logs the operator out.
	epoch, err := d.TokenEpoch(1)
	if err != nil {
		t.Fatalf("TokenEpoch after migration: %v", err)
	}
	if epoch != 0 {
		t.Fatalf("the pre-existing user migrated to epoch %d, want 0: an "+
			"upgrade must not invalidate the session the operator is using", epoch)
	}

	// And it is idempotent, because Open runs every migration on every start.
	if err := d.MigrateUserTokenEpoch(); err != nil {
		t.Fatalf("running the migration twice: %v", err)
	}
}
