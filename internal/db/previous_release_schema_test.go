package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
// Migrate*, which is rung zero. This is the device: every shipped release's
// database, checked in, opened through the ordinary Open path, and compared
// object-for-object against a fresh install.
//
// ONE FIXTURE PER RELEASE, NOT ONE "PREVIOUS RELEASE". This used to be a single
// schema-v0.6.0.sql with a note saying "AT THE NEXT RELEASE: re-point it". Four
// releases later it still said v0.6.0, the runbook had no step for it, and the
// 0.7, 0.8 and 0.9 schema shapes were never opened by CI (staging-readiness
// row 29). An operator does not always upgrade from the release before; a
// v0.7.0 host skipping to today is the same Open call on a different file. So
// every fixture is kept, and TestDocEveryReleaseHasAnUpgradeFixture below
// fails when the newest release in CHANGELOG.md has none -- the note that was
// forgotten is now a red build.
//
// THE .db FILES ARE WHAT EACH TAG'S OWN db.Open WROTE to an empty path -- not a
// schema.sql replayed by hand. A schema.sql misses everything a release's
// Migrate* chain and seed steps did on top of it (v0.6.0's default source row,
// its user_version), which is exactly the state an operator's file is in.
// schema-v0.6.0.sql is kept beside them: it is the shape the first version of
// this test found real bugs with, and it costs nothing.
//
// WHEN THIS FAILS, READ IT AS "a schema.sql change has no migration". The fix
// is a Migrate* on Open's path -- not an edit to a fixture, which is a
// historical artefact and must never be updated to make this pass.
func TestOpeningAPreviousReleaseDatabaseConvergesOnAFreshInstall(t *testing.T) {
	fixtures := upgradeFixtures(t)
	if len(fixtures) < 2 {
		t.Fatalf("found %d upgrade fixtures in testdata; this guard would check almost nothing",
			len(fixtures))
	}

	fresh, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("Open on a fresh install: %v", err)
	}
	defer fresh.Close()
	fr := objectSet(t, fresh)

	for _, f := range fixtures {
		t.Run(f, func(t *testing.T) {
			oldPath := materialiseFixture(t, f)

			// Upgrade it the only way an operator can: run the current binary at it.
			upgraded, err := Open(oldPath)
			if err != nil {
				t.Fatalf("Open refused a %s database: %v. An operator upgrading from "+
					"it cannot start the server at all.", f, err)
			}
			defer upgraded.Close()
			assertConverged(t, f, objectSet(t, upgraded), fr)
		})
	}
}

// upgradeFixtures lists testdata's release databases (release-vX.Y.Z.db) and
// hand-applied schemas (schema-vX.Y.Z.sql). Discovered rather than listed, so
// adding the next release's file is the whole of the work.
func upgradeFixtures(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, pat := range []string{"release-v*.db", "schema-v*.sql"} {
		m, err := filepath.Glob(filepath.Join("testdata", pat))
		if err != nil {
			t.Fatalf("glob %s: %v", pat, err)
		}
		for _, p := range m {
			out = append(out, filepath.Base(p))
		}
	}
	sort.Strings(out)
	return out
}

// materialiseFixture builds the old install in a temporary directory and
// returns its path. A .db is COPIED, never opened in place: Open migrates, and
// a fixture migrated by the test that reads it has stopped being the release
// it is named after. A .sql is applied to an empty file, as that release's
// schema.sql was.
func materialiseFixture(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("testdata", name)
	dst := filepath.Join(t.TempDir(), "old.db")
	if strings.HasSuffix(name, ".db") {
		if err := copyFile(src, dst); err != nil {
			t.Fatalf("copy %s: %v", src, err)
		}
		return dst
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	raw0, err := sql.Open("sqlite", dst)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	if _, err := raw0.Exec(string(raw)); err != nil {
		raw0.Close()
		t.Fatalf("apply %s: %v", name, err)
	}
	if err := raw0.Close(); err != nil {
		t.Fatalf("close %s: %v", name, err)
	}
	return dst
}

// assertConverged compares an upgraded install's objects with a fresh one's.
func assertConverged(t *testing.T, from string, up, fr map[string]string) {
	t.Helper()
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
		t.Errorf("a database upgraded from %s is MISSING what a fresh install has:\n  %s\n\n"+
			"Each of these was added to schema.sql without a Migrate* on Open's path, so "+
			"fresh installs have it and every existing one does not. Add the migration; "+
			"do NOT edit the fixture.", from, strings.Join(missing, "\n  "))
	}
	if len(extra) > 0 {
		t.Errorf("a database upgraded from %s has objects a fresh install does not:\n  %s\n\n"+
			"A migration created something schema.sql no longer declares, so the two "+
			"populations have permanently diverged.", from, strings.Join(extra, "\n  "))
	}
	if len(differs) > 0 {
		t.Errorf("a database upgraded from %s has the same columns as a fresh install but "+
			"NOT the same declarations:\n  %s\n\n"+
			"The column exists on both sides, so the presence check above is happy and "+
			"nothing else in this package can see it. What differs is what the column "+
			"MEANS: a DEFAULT decides what every existing row is backfilled to on the "+
			"upgrade path and what every new row starts at on both, so two spellings of "+
			"one column are two different products. Fix the declaration that is wrong; "+
			"better, delete one of the two so there is only ever one -- schema.sql's "+
			"destinations comment records that convention. Do NOT edit the fixture.",
			from, strings.Join(differs, "\n  "))
	}
}

// writeFixtureEnv names the release whose fixture a run should WRITE instead of
// checking. See TestDocEveryReleaseHasAnUpgradeFixture.
const writeFixtureEnv = "POLY_WRITE_UPGRADE_FIXTURE"

// TestDocEveryReleaseHasAnUpgradeFixture: every release in CHANGELOG.md from
// the oldest fixture on must have a testdata/release-vX.Y.Z.db. The first
// fixture's note said to re-point it "at the next release" and nobody did for
// four releases (staging-readiness row 29); docs/RELEASE-RUNBOOK.md now lists
// it, and this is what holds the runbook to it.
//
// Named Doc* so the documentation-only CI path runs it: a release commit that
// only dates CHANGELOG.md is a docs-only change, and it is the one commit this
// must see.
//
// WRITING ONE. On the release commit the code IS the release, so this package's
// own Open writes the fixture that tag's Open would:
//
//	POLY_WRITE_UPGRADE_FIXTURE=vX.Y.Z go test ./internal/db -run TestDocEveryReleaseHasAnUpgradeFixture
//
// Only on the release commit. Written later, it is some later commit's
// database wearing the release's name, and the upgrade it claims to test was
// never the one an operator performs.
func TestDocEveryReleaseHasAnUpgradeFixture(t *testing.T) {
	if v := os.Getenv(writeFixtureEnv); v != "" {
		writeReleaseFixture(t, v)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	if err != nil {
		t.Fatalf("read CHANGELOG.md: %v", err)
	}
	have := map[string]bool{}
	for _, f := range upgradeFixtures(t) {
		if m := fixtureVersion.FindStringSubmatch(f); m != nil {
			have[m[1]] = true
		}
	}
	for _, v := range missingUpgradeFixtures(string(raw), have) {
		t.Errorf("CHANGELOG.md has released %s and internal/db/testdata has no fixture for it "+
			"(release-v%s.db). Without it no test opens a %s database with the code that "+
			"will upgrade it. On the release commit, run\n\n    %s=v%s go test ./internal/db "+
			"-run TestDocEveryReleaseHasAnUpgradeFixture\n\nand commit the file; "+
			"docs/RELEASE-RUNBOOK.md's checklist has the step.", v, v, v, writeFixtureEnv, v)
	}
}

var (
	fixtureVersion  = regexp.MustCompile(`^(?:release|schema)-v(\d+\.\d+\.\d+)\.(?:db|sql)$`)
	releasedHeading = regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\]`)
)

// missingUpgradeFixtures returns each version CHANGELOG.md has released, from
// the oldest one that has a fixture onwards, that has no fixture. Releases
// before the first fixture are out of scope: they predate the device, and
// their schemas are what v0.6.0's Open migrates from anyway. With no fixture
// at all, every release is reported -- an empty testdata must not read as
// nothing to check. With no released heading at all, that is reported too.
func missingUpgradeFixtures(changelog string, have map[string]bool) []string {
	var released []string
	for _, m := range releasedHeading.FindAllStringSubmatch(changelog, -1) {
		released = append(released, m[1])
	}
	if len(released) == 0 {
		return []string{"(no released version found in CHANGELOG.md; the heading format moved)"}
	}
	oldest := ""
	for v := range have {
		if oldest == "" || semverLess(v, oldest) {
			oldest = v
		}
	}
	var out []string
	for _, v := range released {
		if have[v] || (oldest != "" && semverLess(v, oldest)) {
			continue
		}
		out = append(out, v)
	}
	return out
}

func semverLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return false
}

// writeReleaseFixture writes testdata/release-<v>.db with this package's Open.
func writeReleaseFixture(t *testing.T, v string) {
	t.Helper()
	if !regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(v) {
		t.Fatalf("%s=%q: want a release tag such as v1.2.3", writeFixtureEnv, v)
	}
	dst := filepath.Join("testdata", "release-"+v+".db")
	if _, err := os.Stat(dst); err == nil {
		t.Fatalf("%s already exists. A fixture is a historical artefact; it is never "+
			"rewritten, least of all to make a test pass.", dst)
	}
	d, err := Open(dst)
	if err != nil {
		t.Fatalf("Open %s: %v", dst, err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close %s: %v", dst, err)
	}
	for _, side := range []string{"-wal", "-shm"} {
		_ = os.Remove(dst + side)
	}
	t.Logf("wrote %s", dst)
}

func TestMissingUpgradeFixtures(t *testing.T) {
	const cl = "## [Unreleased]\n\n## [0.10.0] — x\n\n## [0.9.0] — x\n\n## [0.6.0] — x\n\n## [0.5.0] — x\n"
	for _, tc := range []struct {
		name string
		have map[string]bool
		want string
	}{
		{"all present", map[string]bool{"0.6.0": true, "0.9.0": true, "0.10.0": true}, ""},
		{"newest missing", map[string]bool{"0.6.0": true, "0.9.0": true}, "0.10.0"},
		{"a gap", map[string]bool{"0.6.0": true, "0.10.0": true}, "0.9.0"},
		{"older than the first fixture is out of scope", map[string]bool{"0.9.0": true, "0.10.0": true}, ""},
		{"no fixtures reads as everything missing", map[string]bool{}, "0.10.0,0.9.0,0.6.0,0.5.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(missingUpgradeFixtures(cl, tc.have), ","); got != tc.want {
				t.Errorf("missingUpgradeFixtures = %q, want %q", got, tc.want)
			}
		})
	}
	if got := missingUpgradeFixtures("## [Unreleased]\n", nil); len(got) != 1 ||
		!strings.Contains(got[0], "no released version") {
		t.Errorf("a CHANGELOG with no released heading must be reported, got %q", got)
	}
	// 0.10.0 sorts after 0.9.0 numerically, not as text.
	if !semverLess("0.9.0", "0.10.0") || semverLess("0.10.0", "0.9.0") || semverLess("1.0.0", "1.0.0") {
		t.Error("semverLess does not compare numerically")
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
