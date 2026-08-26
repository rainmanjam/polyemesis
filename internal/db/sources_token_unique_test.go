package db

import (
	"errors"
	"strings"
	"testing"
)

// The upgrade tests below reproduce a real one: the fixture database is built
// by Open, so it already has the index this release adds. Dropping it is what
// puts the file back into the shape the previous release left, which is the
// only state where any of this can go wrong -- every other test in this package
// starts from a freshly migrated database, which is exactly why #489 shipped.

// dropTokenUniqueIndex puts the sources table back into its pre-#505 shape and
// proves it did, so a test that follows cannot silently assert nothing.
func dropTokenUniqueIndex(t *testing.T, d *DB) {
	t.Helper()
	if _, err := d.sql.Exec(`DROP INDEX IF EXISTS ` + sourceTokenUniqueIndex); err != nil {
		t.Fatalf("could not reproduce the old schema: %v", err)
	}
	has, err := indexExists(d.sql, sourceTokenUniqueIndex)
	if err != nil || has {
		t.Fatalf("the index is still there, so this test proves nothing (has=%v err=%v)", has, err)
	}
}

// setToken writes a token straight into storage, which is the only way to
// create the state these tests need: UpdateSource deliberately cannot write the
// column any more, and that is the other half of #505.
func setToken(t *testing.T, d *DB, id int64, token string) {
	t.Helper()
	if _, err := d.sql.Exec(`UPDATE sources SET token = ? WHERE id = ?`, token, id); err != nil {
		t.Fatalf("set token on source %d: %v", id, err)
	}
}

func secondSource(t *testing.T, d *DB, name string) *Source {
	t.Helper()
	s := &Source{Name: name, Enabled: true, Ingest: DefaultSettings().Ingest}
	if err := d.CreateSource(s); err != nil {
		t.Fatalf("create source %q: %v", name, err)
	}
	return s
}

// An install whose sources predate the uniqueness rule gains the index on open
// and keeps every row and every token exactly as they were.
func TestAnUpgradedInstallGainsTheSourceTokenUniqueIndex(t *testing.T) {
	d := testDB(t)
	rows, err := d.ListSources()
	if err != nil || len(rows) == 0 {
		t.Fatalf("ListSources: %v", err)
	}
	first := rows[0]
	second := secondSource(t, d, "Vertical")

	dropTokenUniqueIndex(t, d)
	// The state an upgrade actually starts from: the old release let two
	// sources hold one token, which is what makes the encoder land in the wrong
	// programme. Asserted rather than assumed, then undone.
	setToken(t, d, second.ID, first.Token)
	setToken(t, d, second.ID, second.Token)

	if err := d.MigrateSourceTokenUnique(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if has, err := indexExists(d.sql, sourceTokenUniqueIndex); err != nil || !has {
		t.Fatalf("the upgrade did not create the index (has=%v err=%v); the install "+
			"still admits two sources on one token", has, err)
	}
	after, err := d.ListSources()
	if err != nil {
		t.Fatalf("sources unreadable after the migration: %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("the migration lost rows: %d sources, want 2", len(after))
	}
	for _, want := range []*Source{first, second} {
		got, err := d.GetSource(want.ID)
		if err != nil {
			t.Fatalf("GetSource(%d): %v", want.ID, err)
		}
		if got.Token != want.Token {
			t.Errorf("source %d came back with token %q, want the one it had; "+
				"a migration that rewrites a publish secret takes an encoder off the air",
				want.ID, got.Token)
		}
	}
	// Idempotent: every later open runs this again.
	if err := d.MigrateSourceTokenUnique(); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

// The case this whole migration is careful about: an install that already holds
// two sources with one token. The index cannot be created over that data, and
// the answer is to refuse to start rather than to pick a row and rewrite it.
func TestAnUpgradeWithDuplicateTokensRefusesToStartAndChangesNothing(t *testing.T) {
	d := testDB(t)
	rows, err := d.ListSources()
	if err != nil || len(rows) == 0 {
		t.Fatalf("ListSources: %v", err)
	}
	first := rows[0]
	second := secondSource(t, d, "Vertical")
	shared := first.Token

	dropTokenUniqueIndex(t, d)
	setToken(t, d, second.ID, shared)

	err = d.MigrateSourceTokenUnique()
	if err == nil {
		t.Fatal("the migration accepted a database with duplicate tokens; " +
			"the index cannot exist over that data, so it either failed silently " +
			"or rewrote somebody's row")
	}
	var dup *DuplicateSourceTokensError
	if !errors.As(err, &dup) {
		t.Fatalf("error = %T (%v), want *DuplicateSourceTokensError", err, err)
	}
	if len(dup.Groups) != 1 || len(dup.Groups[0]) != 2 {
		t.Fatalf("groups = %v, want one group of two", dup.Groups)
	}
	if dup.Groups[0][0].ID != first.ID || dup.Groups[0][1].ID != second.ID {
		t.Errorf("group = %v, want sources %d and %d named", dup.Groups[0], first.ID, second.ID)
	}
	msg := err.Error()
	// An operator has to be able to act on this without reading the source.
	for _, want := range []string{"Vertical", "UPDATE sources SET token"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not mention %q, so the operator cannot act on it:\n%s", want, msg)
		}
	}
	// The startup log is read by journalctl and pasted into issues. A publish
	// secret printed there is a publish secret leaked.
	if strings.Contains(msg, shared) {
		t.Error("the refusal printed the shared token itself; that leaks the secret " +
			"into every log this line reaches")
	}

	// NOTHING was changed. Refusing is only the safe answer if it leaves the
	// operator's rows exactly as they were, for them to choose between.
	for _, id := range []int64{first.ID, second.ID} {
		got, err := d.GetSource(id)
		if err != nil {
			t.Fatalf("GetSource(%d): %v", id, err)
		}
		if got.Token != shared {
			t.Errorf("source %d now holds %q; the migration mutated operator data "+
				"instead of asking", id, got.Token)
		}
	}
	if has, err := indexExists(d.sql, sourceTokenUniqueIndex); err != nil || has {
		t.Errorf("the index exists after a refusal (has=%v err=%v)", has, err)
	}

	// And the refusal reaches Open, which is what stops the server booting into
	// an install that can admit an encoder to the wrong programme.
	if err := d.MigrateSources(); !errors.As(err, &dup) {
		t.Fatalf("MigrateSources error = %v, want the duplicate-token refusal; "+
			"without this the server starts anyway", err)
	}

	// The remedy the message prints must actually work.
	setToken(t, d, second.ID, "")
	if err := d.MigrateSourceTokenUnique(); err != nil {
		t.Fatalf("the remedy the refusal prints does not clear it: %v", err)
	}
}

// Several sources with no token yet are a working install, not a mistake, so
// the index has to be partial. A plain UNIQUE on the column would refuse the
// second one -- including the row an operator has just blanked while resolving
// the refusal above.
func TestSourcesWithNoTokenAreNotConsideredDuplicates(t *testing.T) {
	d := testDB(t)
	if has, err := indexExists(d.sql, sourceTokenUniqueIndex); err != nil || !has {
		t.Fatalf("the index is missing on a fresh install (has=%v err=%v)", has, err)
	}
	for _, name := range []string{"Blanked A", "Blanked B"} {
		if _, err := d.sql.Exec(
			`INSERT INTO sources (name, enabled, ingest, token, prev_token, prev_token_until,
			 position, created_at, updated_at)
			 VALUES (?, 1, '{}', '', '', 0, 0, 0, 0)`, name); err != nil {
			t.Fatalf("a second source with no token was refused (%q): %v", name, err)
		}
	}
	// And the migration still agrees, so the next boot is not the one that
	// refuses.
	if err := d.MigrateSourceTokenUnique(); err != nil {
		t.Fatalf("migrate over two tokenless sources: %v", err)
	}
}

// Once the index is there the duplicate cannot be created at all, which is the
// difference between Control and a one-off cleanup.
func TestASecondSourceCannotTakeATokenThatIsAlreadyInUse(t *testing.T) {
	d := testDB(t)
	rows, err := d.ListSources()
	if err != nil || len(rows) == 0 {
		t.Fatalf("ListSources: %v", err)
	}
	taken := rows[0].Token

	copycat := &Source{Name: "Copycat", Enabled: true, Ingest: DefaultSettings().Ingest, Token: taken}
	err = d.CreateSource(copycat)
	if err == nil {
		t.Fatal("a second source took a token another one already holds; RTMP's " +
			"lookup is last-writer-wins, so an encoder can be admitted into the " +
			"wrong programme")
	}
	if !errors.Is(err, ErrSourceTokenTaken) {
		t.Errorf("error = %v, want ErrSourceTokenTaken so the operator reads a "+
			"sentence rather than the name of an index", err)
	}
	// The raw write is refused too, not just the Go path in front of it.
	if _, err := d.sql.Exec(
		`UPDATE sources SET token = ? WHERE id != ?`, taken, rows[0].ID); err == nil {
		var n int
		if err := d.sql.QueryRow(
			`SELECT COUNT(*) FROM sources WHERE token = ?`, taken).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		if n > 1 {
			t.Fatal("the database itself still admits two sources on one token")
		}
	}
}

// #505's other half: a stale browser tab must not be able to put a retired
// token back. PUT /sources/{id} decodes over the stored row, so the tab sends
// the token it was rendered with, and UpdateSource used to write it.
func TestAStaleUpdateCannotRollBackATokenRotation(t *testing.T) {
	d := testDB(t)
	rows, err := d.ListSources()
	if err != nil || len(rows) == 0 {
		t.Fatalf("ListSources: %v", err)
	}
	// What the tab was rendered with, before the rotation happened elsewhere.
	stale := *rows[0]
	old := stale.Token

	rotated, err := d.RotateSourceToken(stale.ID)
	if err != nil {
		t.Fatalf("RotateSourceToken: %v", err)
	}
	if rotated == old {
		t.Fatal("rotation returned the same token; the premise of this test is wrong")
	}

	// The save that tab performs: a rename, carrying the token it still holds.
	stale.Name = "Renamed From A Stale Tab"
	if err := d.UpdateSource(&stale); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}

	back, err := d.GetSource(stale.ID)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	if back.Token != rotated {
		t.Fatalf("stored token = %q, want the rotated one; a stale tab rolled the "+
			"rotation back and the retired key works again", back.Token)
	}
	if back.Name != "Renamed From A Stale Tab" {
		t.Errorf("name = %q; the rest of the save must still go through", back.Name)
	}
	// The caller's copy is corrected, so the page the tab redraws from the
	// response shows the token that is actually live rather than the dead one
	// it sent.
	if stale.Token != rotated {
		t.Errorf("the returned source still carries %q; the tab would redraw the "+
			"retired token as the stream key to paste into an encoder", stale.Token)
	}
	// And the retired token no longer resolves to the programme.
	if _, err := d.SourceByToken(old); !errors.Is(err, ErrSourceNotFound) {
		t.Errorf("the retired token still resolves (err = %v)", err)
	}
}
