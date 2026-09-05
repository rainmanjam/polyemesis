package db

import (
	"database/sql"
	"fmt"
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

	var missing, extra, differs []string
	for k, want := range fr {
		got, ok := up[k]
		switch {
		case !ok:
			missing = append(missing, k)
		case got != want:
			differs = append(differs, k+"\n      fresh install: "+want+
				"\n      upgraded:      "+got)
		}
	}
	for k := range up {
		if _, ok := fr[k]; !ok {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	sort.Strings(differs)

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
	if len(differs) > 0 {
		t.Errorf("an upgraded %s database has the same columns as a fresh install but "+
			"NOT the same declarations:\n  %s\n\n"+
			"The column exists on both sides, so the presence check above is happy and "+
			"nothing else in this package can see it. What differs is what the column "+
			"MEANS: a DEFAULT decides what every existing row is backfilled to on the "+
			"upgrade path and what every new row starts at on both, so two spellings of "+
			"one column are two different products. Fix the declaration that is wrong; "+
			"better, delete one of the two so there is only ever one -- schema.sql's "+
			"destinations comment records that convention. Do NOT edit the fixture.",
			prevRelease, strings.Join(differs, "\n  "))
	}
}

// objectSet is every table, index, trigger and view, plus each table's columns
// AND WHAT EACH COLUMN IS DECLARED TO BE. Names alone would miss the case that
// matters most: a table that exists in both but is short a column on the
// upgraded side.
//
// THE VALUE IS THE DECLARATION, NOT `true`, and that is the whole of the
// device. This map used to record presence only, which made it a set of names
// wearing a map's clothes, and a name is exactly the part of a column that
// CREATE TABLE and ALTER TABLE ADD COLUMN are guaranteed to agree on. Every
// column that is declared twice -- once in schema.sql for fresh installs, once
// in a Migrate* for upgraded ones -- could therefore disagree about its type,
// its NOT NULL, its DEFAULT or its primary key, and this test would pass.
//
// The DEFAULT is the one that costs data. ALTER TABLE ADD COLUMN evaluates the
// default ONCE PER EXISTING ROW: it is not a forward-looking convention, it is
// a backfill, and it decides what every row an operator already has becomes.
// So `scope_ver INTEGER NOT NULL DEFAULT 0` in the migration and `DEFAULT 7`
// in schema.sql are not two spellings of one column -- they are "re-consent
// every account that predates scopes" and "silently trust every one of them",
// and the difference is invisible on a fresh install because a fresh install
// has no rows to backfill. Comparing the tuple is what makes those two
// populations comparable at all.
//
// The four fields are PRAGMA table_info's, minus cid: position is genuinely
// allowed to differ, because ALTER appends and CREATE TABLE does not, and
// nothing reads these tables positionally.
func objectSet(t *testing.T, d *DB) map[string]string {
	t.Helper()
	out := map[string]string{}

	rows, err := d.sql.Query(`SELECT type, name, IFNULL(sql, '') FROM sqlite_master
		WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	var tables []string
	for rows.Next() {
		var kind, name, ddl string
		if err := rows.Scan(&kind, &name, &ddl); err != nil {
			rows.Close()
			t.Fatalf("scan sqlite_master: %v", err)
		}
		if kind == "table" {
			// A TABLE'S STORED DDL IS NOT COMPARABLE and must not be recorded
			// here. SQLite keeps the CREATE TABLE text it was given and
			// APPENDS each ALTER TABLE ADD COLUMN's definition to it, so an
			// upgraded table's sql is "the v0.6.0 statement plus nine
			// afterthoughts" and a fresh one's is the current schema.sql
			// statement. They differ in text on every install that has ever
			// been upgraded, by design, while describing the same table. The
			// per-column loop below is what compares tables, and it compares
			// the resolved declarations rather than the words.
			tables = append(tables, name)
			out[kind+" "+name] = ""
			continue
		}
		// An index, trigger or view has no ALTER, so its stored text is
		// exactly what created it and the two sides must match. This is what
		// catches a partial unique index whose WHERE clause drifted between
		// schema.sql and the Migrate* that installs it -- two different rules
		// with one name, which is worse than not having the index.
		out[kind+" "+name] = strings.Join(strings.Fields(ddl), " ")
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
			out["column "+tb+"."+name] = fmt.Sprintf(
				"type=%s notnull=%d default=%s pk=%d",
				strings.ToUpper(ctype), notnull, renderDefault(dflt), pk)
		}
		cols.Close()
		if err := cols.Err(); err != nil {
			t.Fatalf("table_info(%s): %v", tb, err)
		}
	}
	return out
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// renderDefault turns PRAGMA table_info's dflt_value into something two
// installs can be compared on.
//
// It renders the ABSENCE of a default as the literal "<none>" rather than as
// an empty string, because "no DEFAULT at all" and "DEFAULT ”" are the two
// halves of the mistake this comparison exists to find, and collapsing them
// into the same text would hand the divergent case back its invisibility. The
// driver hands the value back as nil, []byte or string depending on the column
// type, so everything else is normalised through %s.
func renderDefault(v any) string {
	switch d := v.(type) {
	case nil:
		return "<none>"
	case []byte:
		return string(d)
	default:
		return fmt.Sprintf("%v", d)
	}
}
