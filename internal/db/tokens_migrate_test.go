package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestMigrateAPITokenScopeGrandfathersExistingTokens covers the upgrade path CI
// otherwise never sees: every other test in this package starts from a database
// the current schema just created, so a migration that is wrong on a real older
// install would pass all of them.
//
// What has to be true here is a policy decision, not a mechanical one. A token
// created before scopes existed could do everything, because there was nothing
// to stop it. Backfilling 'read' would silently narrow a credential somebody's
// CI runner is holding, and it would fail as a 403 inside an unattended script
// rather than as a message an operator reads. So the column's default is
// 'admin' and this test is what keeps it that way -- the safe-looking value is
// the wrong one here, which is exactly the kind of thing a future reader would
// otherwise "fix".
func TestMigrateAPITokenScopeGrandfathersExistingTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	// api_tokens as it existed before scopes, holding a token that has been in
	// use -- a script somewhere is authenticating with it right now.
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`
		CREATE TABLE api_tokens (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			name         TEXT    NOT NULL,
			token_hash   TEXT    NOT NULL UNIQUE,
			prefix       TEXT    NOT NULL DEFAULT '',
			created_at   INTEGER NOT NULL,
			last_used_at INTEGER NOT NULL DEFAULT 0
		);
		INSERT INTO api_tokens (name, token_hash, prefix, created_at, last_used_at)
		VALUES ('ci runner', '` + hashToken(TokenPrefix+"pretend") + `', 'pmk_prete', 1, 1);`); err != nil {
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

	if has, err := columnExists(d.sql, "api_tokens", "scope"); err != nil || has {
		t.Fatalf("the fixture already had scope (has=%v, err=%v); "+
			"this test would prove nothing", has, err)
	}

	if err := d.MigrateAPITokenScope(); err != nil {
		t.Fatalf("MigrateAPITokenScope on an existing install: %v", err)
	}

	// Read it back the way production does, rather than by querying the column
	// directly: LookupAPIToken is what the auth middleware calls, and a scope
	// that is correct in the table but dropped on the way out would be the same
	// bug with a passing test.
	tok, err := d.LookupAPIToken(TokenPrefix + "pretend")
	if err != nil {
		t.Fatalf("LookupAPIToken after migration: %v", err)
	}
	if tok.Scope != ScopeAdmin {
		t.Fatalf("a token that predates scopes migrated to %q, want %q: an "+
			"upgrade must not quietly take away what a running script could do",
			tok.Scope, ScopeAdmin)
	}

	// And it is idempotent, because Open runs every migration on every start.
	if err := d.MigrateAPITokenScope(); err != nil {
		t.Fatalf("running the migration twice: %v", err)
	}
}

// A token minted after the migration gets the safe default instead, and the two
// halves of that sentence are asserted in the same place so nobody can read one
// without the other.
func TestFreshTokenDefaultsToReadNotAdmin(t *testing.T) {
	d := testDB(t)

	tok, _, err := d.CreateAPIToken("monitoring", "")
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if tok.Scope != ScopeRead {
		t.Errorf("Scope = %q, want %q", tok.Scope, ScopeRead)
	}

	if _, _, err := d.CreateAPIToken("typo", "readonly"); err == nil {
		t.Error("an unknown scope was accepted; a typo must not mint a token at all")
	}
}
