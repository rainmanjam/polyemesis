package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Opening a REAL previous-release database must converge on a fresh install.
//
// schema.sql is CREATE TABLE IF NOT EXISTS, so a column declared only there
// reaches fresh installs and NEVER an upgrade. That asymmetry has caused data
// loss in this repo before, and nothing in the test suite could see it:
// dbtest's template is a fresh install, each migration test hand-builds only
// the one table it cares about, and the "0.6.x-shaped" fixture in
// schema_version_test.go is a hand-written five-column users table rather than
// the schema v0.6.0 actually shipped.
//
// So every upgrade was correct because four separate changes each remembered a
// Migrate*, which is rung zero. This is the device: the previous release's real
// schema.sql, checked in, opened through the ordinary Open path, and compared
// object-for-object against a fresh install.
//
// WHEN THIS FAILS, READ IT AS "a schema.sql change has no migration". The fix
// is a Migrate* on Open's path -- not an edit to the fixture, which is a
// historical artefact and must never be updated to make this pass.
//
// AT THE NEXT RELEASE: re-point the fixture at the new previous release with
//
//	git show v0.7.0:internal/db/schema.sql > internal/db/testdata/schema-v0.7.0.sql
//
// and update prevRelease below. Leaving it on v0.6.0 keeps testing an
// upgrade nobody performs any more.
const prevRelease = "v0.6.0"

func TestOpeningAPreviousReleaseDatabaseConvergesOnAFreshInstall(t *testing.T) {
	fixture := filepath.Join("testdata", "schema-"+prevRelease+".sql")
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read %s: %v. This fixture IS the test -- without it nothing "+
			"here opens a database built from a shipped schema.", fixture, err)
	}

	// The old install, built exactly as that release built it.
	oldPath := filepath.Join(t.TempDir(), "old.db")
	raw0, err := sql.Open("sqlite", oldPath)
	if err != nil {
		t.Fatalf("open %s: %v", prevRelease, err)
	}
	if _, err := raw0.Exec(string(raw)); err != nil {
		raw0.Close()
		t.Fatalf("apply %s schema: %v", prevRelease, err)
	}
	if err := raw0.Close(); err != nil {
		t.Fatalf("close %s: %v", prevRelease, err)
	}

	// Upgrade it the only way an operator can: run the current binary at it.
	upgraded, err := Open(oldPath)
	if err != nil {
		t.Fatalf("Open refused a %s database: %v. An operator upgrading from "+
			"%s cannot start the server at all.", prevRelease, prevRelease, err)
	}
	defer upgraded.Close()

	fresh, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("Open on a fresh install: %v", err)
	}
	defer fresh.Close()

	up, fr := objectSet(t, upgraded), objectSet(t, fresh)

	var missing, extra []string
	for k := range fr {
		if _, ok := up[k]; !ok {
			missing = append(missing, k)
		}
	}
	for k := range up {
		if _, ok := fr[k]; !ok {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) > 0 {
		t.Errorf("an upgraded %s database is MISSING what a fresh install has:\n  %s\n\n"+
			"Each of these was added to schema.sql without a Migrate* on Open's path, so "+
			"fresh installs have it and every existing one does not. Add the migration; "+
			"do NOT edit the fixture.", prevRelease, strings.Join(missing, "\n  "))
	}
	if len(extra) > 0 {
		t.Errorf("an upgraded %s database has objects a fresh install does not:\n  %s\n\n"+
			"A migration created something schema.sql no longer declares, so the two "+
			"populations have permanently diverged.", prevRelease, strings.Join(extra, "\n  "))
	}
}

// objectSet is every table, index, trigger and view, plus each table's columns.
// Names alone would miss the case that matters most: a table that exists in
// both but is short a column on the upgraded side.
func objectSet(t *testing.T, d *DB) map[string]bool {
	t.Helper()
	out := map[string]bool{}

	rows, err := d.sql.Query(`SELECT type, name FROM sqlite_master
		WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	var tables []string
	for rows.Next() {
		var kind, name string
		if err := rows.Scan(&kind, &name); err != nil {
			rows.Close()
			t.Fatalf("scan sqlite_master: %v", err)
		}
		out[kind+" "+name] = true
		if kind == "table" {
			tables = append(tables, name)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}

	for _, tb := range tables {
		// A literal, not a bind parameter: PRAGMA refuses them. The name comes
		// from sqlite_master, not from a caller, so there is nothing to inject.
		cols, err := d.sql.Query(`PRAGMA table_info(` + quoteIdent(tb) + `)`)
		if err != nil {
			t.Fatalf("table_info(%s): %v", tb, err)
		}
		for cols.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt any
			if err := cols.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				cols.Close()
				t.Fatalf("scan table_info(%s): %v", tb, err)
			}
			out["column "+tb+"."+name] = true
		}
		cols.Close()
		if err := cols.Err(); err != nil {
			t.Fatalf("table_info(%s): %v", tb, err)
		}
	}
	return out
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
