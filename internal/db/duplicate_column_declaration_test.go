package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// A column declared in BOTH schema.sql and an `ALTER TABLE ... ADD COLUMN`
// must be declared to be the SAME COLUMN in both places.
//
// FOURTEEN columns in this package are declared twice. The number is measured,
// not counted by eye -- an earlier draft of this comment said thirteen and
// listed thirteen, having missed destinations.rendition_id, which schema.sql
// declares at line 157 and MigrateRenditions adds by ALTER as well. The list,
// as the scan below reports it:
//
//	alert_rules.allow_private_target   platform_accounts.scope_ver
//	api_tokens.scope                   recordings.source_id
//	destinations.rendition_id          renditions.deinterlace
//	destinations.source_id             renditions.source_id
//	destinations.stream_key_enc        sources.prev_token
//	hooks.allow_private_target         sources.prev_token_until
//	users.token_epoch                  users.tour_completed_at
//
// The duplication is not an accident to be tidied away: MigrateSources decides
// "is this a fresh install or an upgrade" by asking whether the three source_id
// columns exist, which is only true on a fresh install because schema.sql
// declares them AND the migration does. Take one of those declarations away and
// a brand-new install reads as an upgrade and seeds itself a programme its
// operator never configured. So the duplication stays; what has to go is the
// possibility of the two copies disagreeing.
//
// THE DISAGREEMENT THAT COSTS DATA IS THE DEFAULT, and it is invisible from
// either side on its own. `ALTER TABLE ... ADD COLUMN x INTEGER NOT NULL
// DEFAULT 0` does not express a preference about future rows: SQLite evaluates
// that default once for EVERY ROW ALREADY IN THE TABLE. It is a backfill of
// the operator's live data, written in the grammar of a schema. schema.sql's
// copy of the same column backfills nothing, because a fresh install has no
// rows. So the two declarations decide different things for different
// populations, and the population that gets the migration's answer is the one
// with real data in it.
//
// The concrete case this was written for: platform_accounts.scope_ver. Its
// migration says DEFAULT 0, meaning "this account connected before scopes were
// versioned, so compare its granted scopes and re-prompt if they are short".
// Change that one digit to DEFAULT 7 and every upgraded install silently
// stamps every stored account as current -- no re-consent, no prompt, tokens
// used with a narrower grant than the code believes it has -- while every
// fresh install, having no rows, behaves identically to before. The
// convergence test next door could not see it either: it compares a v0.6.0
// database against a fresh one, and v0.6.0's schema.sql ALREADY HAS
// scope_ver, so the migration is a no-op on the only upgrade path that test
// walks. A column added to schema.sql in the release AFTER its migration
// shipped is permanently outside that test's reach.
//
// So this one does not use a fixture at all. It hands each declaration to
// SQLite -- the CREATE TABLE from schema.sql, the ALTER against a bare probe
// table -- and compares what SQLite says the column IS. Comparing the two
// resolved declarations rather than their text is what makes it immune to
// `NOT NULL DEFAULT 0` vs `DEFAULT 0 NOT NULL`, to quoting, and to whitespace.
//
// Rung 2, a warning at `go test ./...` on the commit that introduces the
// divergence. Control -- one declaration, so there is nothing to disagree with
// -- is what schema.sql's destinations comment describes and is genuinely
// better, but it is unavailable for the source_id trio for the reason above,
// and buying it for the other eleven means moving eleven columns out of the
// file an operator reads to understand their database and onto the upgrade
// path, which is the riskier half of this package. This test costs nothing and
// covers all fourteen, including the three that can never be de-duplicated.
func TestAColumnDeclaredTwiceDeclaresTheSameThingBothTimes(t *testing.T) {
	fresh := columnsDeclaredBySchemaSQL(t)
	migrated := columnsDeclaredByMigrations(t)

	var checked, bad []string
	for key, viaAlter := range migrated {
		viaSchema, both := fresh[key]
		if !both {
			// Declared only by the migration. That is the convention
			// schema.sql's destinations comment sets out, and there is nothing
			// here for a second declaration to contradict.
			continue
		}
		checked = append(checked, key)
		if viaSchema != viaAlter {
			bad = append(bad, fmt.Sprintf("%s\n      schema.sql:    %s\n      ALTER TABLE:   %s",
				key, viaSchema, viaAlter))
		}
	}
	sort.Strings(checked)
	sort.Strings(bad)

	if len(bad) > 0 {
		t.Errorf("a column is declared twice and the two declarations do not agree:\n  %s\n\n"+
			"Fresh installs get schema.sql's version and every existing install gets the "+
			"ALTER's, so the two populations are running different products and no amount "+
			"of testing one of them can see it. Where they differ in DEFAULT, the ALTER's "+
			"value is not a default at all -- SQLite writes it into every row the operator "+
			"already has. Make the two identical, or delete the schema.sql declaration so "+
			"the migration is the only statement of the fact (the destinations comment in "+
			"schema.sql records that convention) -- but NOT for the source_id columns, "+
			"whose duplication is what MigrateSources uses to tell a fresh install from an "+
			"upgrade.", strings.Join(bad, "\n  "))
	}

	// Positive control. Every assertion above is inside `if both`, so a scan
	// that found no doubly-declared column at all would pass this test in
	// silence -- and a one-character slip in either regex below is enough to
	// produce exactly that. The number is deliberately a floor rather than an
	// equality: de-duplicating a column is an improvement and must not fail
	// the test that exists to make de-duplication unnecessary.
	//
	// THE FLOOR HAS TO BE CLOSE TO THE MEASURED COUNT OR IT CONTROLS NOTHING.
	// The measured count is 14 (the list is in this function's doc comment). At
	// a floor of 4, a regex change that silently dropped TEN of the fourteen
	// still passed -- which is exactly the failure this control exists to
	// catch, because the columns a broken scan drops are not a random four:
	// they are whichever ones the changed pattern stops matching, and the
	// remainder go on producing a green run that asserts nothing about them.
	//
	// 10 rather than 14 leaves headroom for the de-duplication the comment
	// above says is the better fix, without leaving room for a scan to lose a
	// third of its subjects unnoticed. Raise it when columns are de-duplicated
	// past it; do NOT lower it to make a broken scan pass.
	const knownDoubleDeclarations = 10
	if len(checked) < knownDoubleDeclarations {
		t.Fatalf("only %d doubly-declared columns found (%v); at least %d are known to "+
			"exist (14 were measured when this floor was set), so the scan below is "+
			"broken and this test is asserting nothing about the columns it stopped "+
			"seeing. Fix the scan -- do not lower the floor.",
			len(checked), checked, knownDoubleDeclarations)
	}

	// A COUNT CANNOT GUARD THE ONE COLUMN THIS SCAN WAS WRITTEN FOR.
	//
	// destinations.rendition_id is the only column in the package whose ALTER
	// spans more than one line, and handling that is precisely what
	// alterAddColumn's `\s+` exists to do. Change that one `\s+` to a literal
	// space -- the likeliest single-character slip in the whole pattern -- and
	// this column and only this column disappears. Thirteen survivors clears a
	// floor of ten in silence.
	//
	// So the floor is the coarse control and this is the specific one. It was
	// found by mutation: M-9 in the review of this patch, the only mutation of
	// nine that the floor did not catch.
	const multiLineAlter = "destinations.rendition_id"
	if !slices.Contains(checked, multiLineAlter) {
		t.Errorf("%s is not among the doubly-declared columns this scan found (%v).\n"+
			"It is the only multi-line ALTER in the package -- internal/db/renditions.go's "+
			"MigrateRenditions -- so its absence means alterAddColumn has stopped matching "+
			"statements that wrap, not that the column was de-duplicated. If it really was "+
			"de-duplicated, delete this check and say so; otherwise the pattern is broken in "+
			"a way the count above is too coarse to see.", multiLineAlter, checked)
	}
}

// columnsDeclaredBySchemaSQL applies schema.sql to an empty database and asks
// SQLite what it built: "table.column" -> the resolved declaration.
//
// Applying it rather than parsing it is the point. The text of a column
// definition has several spellings that mean one thing, and a parser that got
// any of them wrong would fail this test on a difference that is not one, or
// -- much worse -- read two different declarations as the same string.
func columnsDeclaredBySchemaSQL(t *testing.T) map[string]string {
	t.Helper()
	d := probeDatabase(t)
	if _, err := d.Exec(schemaSQL); err != nil {
		t.Fatalf("apply schema.sql to a probe database: %v", err)
	}
	rows, err := d.Query(`SELECT name FROM sqlite_master WHERE type = 'table'
		AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatalf("scan sqlite_master: %v", err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}

	out := map[string]string{}
	for _, tb := range tables {
		for col, decl := range declaredColumns(t, d, tb) {
			out[tb+"."+col] = decl
		}
	}
	return out
}

// alterAddColumn matches the ADD COLUMN statements this package runs on the
// upgrade path. They live in backtick literals, one statement per literal, so
// the definition runs from the column name to the closing backtick -- which is
// what lets a multi-line one (MigrateRenditions' rendition_id) be picked up
// whole, REFERENCES clause included.
var alterAddColumn = regexp.MustCompile("(?s)ALTER TABLE (\\w+)\\s+ADD COLUMN\\s+(\\w+)([^`]*)`")

// columnsDeclaredByMigrations executes every ALTER TABLE ... ADD COLUMN in
// this package's non-test sources against a bare probe table and reports what
// each one actually declares: "table.column" -> the resolved declaration.
//
// The probe table is a single id column, so the ALTER is the only thing that
// can have produced what comes back. Executing the statement rather than
// reading it means a REFERENCES clause, an unusual DEFAULT expression or a
// type SQLite reinterprets is measured rather than guessed at.
func columnsDeclaredByMigrations(t *testing.T) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/db: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, m := range alterAddColumn.FindAllStringSubmatch(string(src), -1) {
			table, col, tail := m[1], m[2], strings.TrimSpace(m[3])

			d := probeDatabase(t)
			// A one-column table with nothing else in it: whatever PRAGMA
			// reports for col afterwards came from the statement under test.
			if _, err := d.Exec(`CREATE TABLE ` + quoteIdent(table) +
				` (probe_id INTEGER PRIMARY KEY)`); err != nil {
				t.Fatalf("%s: create probe table %s: %v", name, table, err)
			}
			stmt := `ALTER TABLE ` + quoteIdent(table) + ` ADD COLUMN ` + col + ` ` + tail
			if _, err := d.Exec(stmt); err != nil {
				t.Fatalf("%s: %s\nis not a statement SQLite accepts on its own: %v\n\n"+
					"Either the migration is broken or this test's scan sliced the "+
					"statement in half; check both before changing either.",
					name, stmt, err)
			}
			decls := declaredColumns(t, d, table)
			decl, ok := decls[col]
			if !ok {
				t.Fatalf("%s: %s ran but added no column named %s", name, stmt, col)
			}
			out[table+"."+col] = decl
			d.Close()
		}
	}
	return out
}

// declaredColumns is PRAGMA table_info reduced to what a column IS, per name.
//
// cid is left out because position is genuinely allowed to differ between the
// two paths -- ALTER appends and CREATE TABLE does not -- and nothing in this
// package reads a row positionally. Everything else is in: a type change
// silently rewrites stored values, a NOT NULL change decides whether a write
// is refused or accepted, a DEFAULT change rewrites live rows on the upgrade
// path, and a pk change is a different table.
func declaredColumns(t *testing.T, d *sql.DB, table string) map[string]string {
	t.Helper()
	// A literal, not a bind parameter: PRAGMA refuses them. The name comes from
	// sqlite_master or from this package's own source, not from a caller.
	rows, err := d.Query(`PRAGMA table_info(` + quoteIdent(table) + `)`)
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		out[name] = fmt.Sprintf("type=%s notnull=%d default=%s pk=%d",
			strings.ToUpper(ctype), notnull, renderDefault(dflt), pk)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	return out
}

// probeDatabase is an empty SQLite database on disk, closed with the test.
// On disk rather than in memory so it behaves exactly as the real one does,
// including how the driver reports column defaults.
func probeDatabase(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatalf("open probe database: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}
