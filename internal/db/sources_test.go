package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The migration is the dangerous part of this feature. An operator upgrades,
// their encoder is already pointed at a port, and if the first source does not
// come up on exactly that port with exactly that protocol they have lost their
// broadcast with no obvious cause. These tests exist for that.

// preSourcesRows is what a hand-built pre-sources database should contain
// besides its settings: whichever of the three tables the case under test
// needs a row in.
type preSourcesRows struct {
	destination bool
	rendition   bool
	recording   bool
}

// preSourcesDB writes a database file that GENUINELY predates the sources
// model: the three tables exist without their source_id column, and there is
// no sources table at all.
//
// It is built by hand rather than by opening a real database and deleting from
// sources, and that difference is the whole point. Since #387 an empty sources
// table on a database that already HAS the columns is the fresh-install case,
// and the discriminator is required to answer "fresh" to it. A fixture that
// builds its upgrade by emptying a modern database is therefore testing the
// opposite of what it claims to, and would only pass again if somebody
// loosened the rule to accommodate it. db_test.go's
// TestMigrateDestinationsCarriesBackupIntentOutOfTheFacebookBlob is the
// established pattern for this.
//
// ingestJSON is written into settings.json verbatim, so a caller can also hand
// it something that will not parse.
func preSourcesDB(t *testing.T, settingsJSON string, rows preSourcesRows) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "polyemesis.db")

	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	// The three tables as the release before sources declared them: every
	// column they have today EXCEPT source_id, and no foreign key to a table
	// that does not exist yet. The columns later migrations add are left off
	// on purpose -- an upgrading install does not have them either, and
	// watching them get added is part of what makes this a real upgrade.
	if _, err := old.Exec(`
		CREATE TABLE settings (
			id   INTEGER PRIMARY KEY CHECK (id = 1),
			json TEXT    NOT NULL
		);
		CREATE TABLE renditions (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			name          TEXT    NOT NULL,
			width         INTEGER NOT NULL DEFAULT 0,
			height        INTEGER NOT NULL DEFAULT 0,
			fps           INTEGER NOT NULL DEFAULT 0,
			video_bitrate INTEGER NOT NULL DEFAULT 0,
			encoder       TEXT    NOT NULL DEFAULT 'libx264',
			preset        TEXT    NOT NULL DEFAULT 'veryfast',
			gop_seconds   REAL    NOT NULL DEFAULT 2,
			note          TEXT    NOT NULL DEFAULT '',
			created_at    INTEGER NOT NULL,
			updated_at    INTEGER NOT NULL
		);
		CREATE TABLE destinations (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			name          TEXT    NOT NULL,
			kind          TEXT    NOT NULL,
			platform      TEXT    NOT NULL DEFAULT '',
			account_id    INTEGER,
			url           TEXT    NOT NULL DEFAULT '',
			stream_key    TEXT    NOT NULL DEFAULT '',
			enabled       INTEGER NOT NULL DEFAULT 0,
			audio_bitrate INTEGER NOT NULL DEFAULT 160,
			profile       TEXT    NOT NULL,
			rendition_id  INTEGER,
			position      INTEGER NOT NULL DEFAULT 0,
			created_at    INTEGER NOT NULL,
			updated_at    INTEGER NOT NULL
		);
		CREATE TABLE recordings (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			filename    TEXT    NOT NULL UNIQUE,
			started_at  INTEGER NOT NULL,
			finished_at INTEGER NOT NULL DEFAULT 0,
			bytes       INTEGER NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			tracks      INTEGER NOT NULL DEFAULT 0
		);`); err != nil {
		t.Fatalf("build pre-sources database: %v", err)
	}
	if _, err := old.Exec(`INSERT INTO settings (id, json) VALUES (1, ?)`, settingsJSON); err != nil {
		t.Fatalf("seed pre-sources settings: %v", err)
	}
	if rows.destination {
		if _, err := old.Exec(`INSERT INTO destinations (name, kind, url, profile, created_at, updated_at)
			VALUES ('pre-existing', 'rtmp', 'rtmp://example/live', '{}', 1000, 1000)`); err != nil {
			t.Fatalf("seed pre-sources destination: %v", err)
		}
	}
	if rows.rendition {
		if _, err := old.Exec(`INSERT INTO renditions (name, created_at, updated_at)
			VALUES ('pre-existing', 1000, 1000)`); err != nil {
			t.Fatalf("seed pre-sources rendition: %v", err)
		}
	}
	if rows.recording {
		if _, err := old.Exec(`INSERT INTO recordings (filename, started_at)
			VALUES ('pre-existing.mkv', 1000)`); err != nil {
			t.Fatalf("seed pre-sources recording: %v", err)
		}
	}
	if _, err := old.Exec(`SELECT source_id FROM destinations`); err == nil {
		t.Fatal("destinations.source_id already exists on the hand-built table; this fixture proves nothing")
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}
	return path
}

// preSourcesSettings is a settings blob for an install whose operator chose
// RTMP on a named app with a stream key -- deliberately none of the defaults,
// so a source that came up on the defaults instead is visibly wrong rather
// than accidentally right.
func preSourcesSettings(t *testing.T) string {
	t.Helper()
	s := DefaultSettings()
	s.Ingest.Mode = IngestRTMP
	s.Ingest.RTMP.App = "live"
	s.Ingest.RTMP.StreamKey = "secretkey"
	blob, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal pre-sources settings: %v", err)
	}
	return string(blob)
}

func TestMigrationCarriesAnExistingIngestOntoTheFirstSource(t *testing.T) {
	path := preSourcesDB(t, preSourcesSettings(t), preSourcesRows{destination: true})

	d, err := Open(path)
	if err != nil {
		t.Fatalf("open the pre-sources database: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	got, err := d.ListSources()
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sources, want exactly 1", len(got))
	}
	s := got[0]
	if s.Name != DefaultSourceName {
		t.Errorf("name = %q, want %q", s.Name, DefaultSourceName)
	}
	if !s.Enabled {
		t.Error("the migrated source is disabled; the install would stop ingesting")
	}
	if s.Ingest.Mode != IngestRTMP {
		t.Errorf("mode = %q, want rtmp", s.Ingest.Mode)
	}
	if s.Ingest.RTMP.StreamKey != "secretkey" {
		t.Errorf("stream key = %q, want it carried across", s.Ingest.RTMP.StreamKey)
	}
	if s.Token == "" {
		t.Error("migrated source has no publish token")
	}
}

func TestMigrationBackfillsExistingRowsOntoTheFirstSource(t *testing.T) {
	path := preSourcesDB(t, preSourcesSettings(t),
		preSourcesRows{destination: true, rendition: true, recording: true})

	d, err := Open(path)
	if err != nil {
		t.Fatalf("open the pre-sources database: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	want, err := d.DefaultSourceID()
	if err != nil {
		t.Fatalf("DefaultSourceID: %v", err)
	}
	for _, table := range []string{"destinations", "renditions", "recordings"} {
		var got *int64
		if err := d.SQL().QueryRow(
			`SELECT source_id FROM ` + table + ` LIMIT 1`).Scan(&got); err != nil {
			t.Fatalf("read back %s.source_id: %v", table, err)
		}
		if got == nil {
			t.Errorf("%s still has a NULL source_id after the backfill", table)
			continue
		}
		if *got != want {
			t.Errorf("%s.source_id = %d, want %d", table, *got, want)
		}
	}
}

// A pre-sources install can hold RECORDINGS and nothing else, and that install
// is the one an orphans-gated backfill loses.
//
// orphans counts destinations and renditions only -- deliberately, because an
// orphan recording is what a legitimate source delete leaves behind. So this
// install seeds on the migrating witness alone, and if the backfill were gated
// on orphans rather than on a source existing it would be skipped: the
// operator's whole library would stay at source_id NULL, unattributed, with
// nothing in the logs to say it had happened.
func TestAPreSourcesInstallWithOnlyRecordingsKeepsThem(t *testing.T) {
	path := preSourcesDB(t, preSourcesSettings(t), preSourcesRows{recording: true})

	d, err := Open(path)
	if err != nil {
		t.Fatalf("open the pre-sources database: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	srcs, err := d.ListSources()
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(srcs) != 1 {
		t.Fatalf("got %d sources, want the migrating install to get exactly 1", len(srcs))
	}
	var got *int64
	if err := d.SQL().QueryRow(`SELECT source_id FROM recordings LIMIT 1`).Scan(&got); err != nil {
		t.Fatalf("read back recordings.source_id: %v", err)
	}
	if got == nil {
		t.Fatal("the recording was left unattached: this install's library lost its source")
	}
	if *got != srcs[0].ID {
		t.Errorf("recordings.source_id = %d, want %d", *got, srcs[0].ID)
	}
}

// The fresh case. A brand new install has no programme until its operator
// makes one, and no number of restarts manufactures one for them.
func TestAFreshInstallComesUpWithNoSourceAndStaysThatWay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")

	for i := 1; i <= 3; i++ {
		d, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		got, err := d.ListSources()
		if err != nil {
			t.Fatalf("ListSources after open %d: %v", i, err)
		}
		if len(got) != 0 {
			t.Fatalf("open %d: got %d sources on a fresh install, want 0 (%q was seeded by the migration)",
				i, len(got), got[0].Name)
		}
		if err := d.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the fresh database was never written: %v", err)
	}
}

// An install that deliberately removed its last source must not find Main back
// the next time it starts.
//
// The state is built directly against the store rather than through
// DeleteSource, because DeleteSource still refuses to remove the only source.
// PR 6 of #387 removes that guard and re-runs this through the real delete
// path; this one proves the discriminator, that one proves the route to it.
//
// The recording is the point of the test. It is left behind by the delete --
// ON DELETE SET NULL, by design, because the file is still on disk and still
// playable -- so an orphan recording is the NORMAL state here. A migration
// that treated one as evidence of a pre-sources install would re-seed Main
// onto the install of the one operator who had just decided they wanted none.
func TestAnInstallThatDeletedItsLastSourceDoesNotGetMainBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	src := &Source{Name: "Studio", Enabled: true, Ingest: DefaultSettings().Ingest, Position: 1}
	if err := d.CreateSource(src); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if _, err := d.SQL().Exec(
		`INSERT INTO recordings (filename, started_at, source_id) VALUES ('kept.mkv', 1000, ?)`,
		src.ID); err != nil {
		t.Fatalf("seed recording: %v", err)
	}
	if _, err := d.SQL().Exec(`DELETE FROM sources WHERE id = ?`, src.ID); err != nil {
		t.Fatalf("delete the last source: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { again.Close() })

	got, err := again.ListSources()
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d sources after reopening an install that deleted its last one, want 0 "+
			"(%q came back)", len(got), got[0].Name)
	}
	// And the recording is still there, still orphaned, still listable.
	var n int
	if err := again.SQL().QueryRow(
		`SELECT COUNT(*) FROM recordings WHERE source_id IS NULL`).Scan(&n); err != nil {
		t.Fatalf("count orphan recordings: %v", err)
	}
	if n != 1 {
		t.Errorf("got %d orphan recordings, want the deleted source's one to survive", n)
	}
}

// THE INTERRUPTED UPGRADE, and the reason MigrateSources is one transaction.
//
// The ALTER loop runs before the source is created. If those two are not
// atomic there is a state in between -- columns present, no source -- and an
// upgrading install that dies in it comes back looking exactly like a fresh
// one: migrating false because the columns are there, orphans false because
// this install has no destinations or renditions, count zero. The
// discriminator says "fresh", the seed never fires, and the operator's ingest
// is gone with nothing to show for it but an encoder that stopped connecting.
//
// The failure here is a real one rather than an injected one: settings.json
// will not parse, which ingestForMigration refuses on rather than silently
// falling back to the defaults. It lands after the ALTER loop and before the
// source, which is precisely the window.
//
// The first assertion is the transaction: the columns must NOT have survived
// the failed open. The second is what that buys: repair the settings and the
// next open migrates from the beginning and the ingest still arrives.
func TestAnInterruptedUpgradeDoesNotReadAsAFreshInstall(t *testing.T) {
	path := preSourcesDB(t, `{"ingest": THIS IS NOT JSON}`, preSourcesRows{recording: true})

	if _, err := Open(path); err == nil {
		t.Fatal("Open succeeded on a settings blob that cannot be parsed; " +
			"this test cannot reach the window it is about")
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	for _, table := range []string{"destinations", "renditions", "recordings"} {
		has, err := columnExists(raw, table, "source_id")
		if err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		if has {
			t.Errorf("%s.source_id survived a migration that failed after the ALTER loop. "+
				"The next open will see the columns, no source and nothing to backfill, "+
				"call this a fresh install, and never carry the operator's ingest across.", table)
		}
	}
	// The operator fixes their settings file, or the upgrade that wrote it is
	// reverted. Either way the blob parses on the next boot.
	if _, err := raw.Exec(`UPDATE settings SET json = ? WHERE id = 1`, preSourcesSettings(t)); err != nil {
		t.Fatalf("repair settings: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after repairing settings: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	got, err := d.ListSources()
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sources, want the interrupted upgrade to finish on the next open", len(got))
	}
	if got[0].Ingest.Mode != IngestRTMP || got[0].Ingest.RTMP.StreamKey != "secretkey" {
		t.Errorf("ingest = %+v, want the operator's RTMP configuration carried across", got[0].Ingest)
	}
}

func TestMigrateSourcesIsIdempotent(t *testing.T) {
	d := testDB(t)
	// It runs on every open, so running it repeatedly must not accumulate
	// sources or fail on the already-added columns.
	for i := 0; i < 3; i++ {
		if err := d.MigrateSources(); err != nil {
			t.Fatalf("MigrateSources run %d: %v", i+1, err)
		}
	}
	got, err := d.ListSources()
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sources after 3 migrations, want 1", len(got))
	}
}

func TestSourceTokensAreUniqueAndURLSafe(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		tok, err := NewSourceToken()
		if err != nil {
			t.Fatalf("NewSourceToken: %v", err)
		}
		if seen[tok] {
			t.Fatalf("duplicate token after %d draws: %q", i, tok)
		}
		seen[tok] = true
		// This value goes into an SRT streamid and an RTMP path. A '+', '/'
		// or '=' would need escaping in at least one of them, which is how you
		// ship an ingest that works everywhere except for the one operator
		// whose token happened to contain a slash.
		if strings.ContainsAny(tok, "+/=") {
			t.Fatalf("token %q contains a character that needs URL escaping", tok)
		}
	}
}

func TestSourceByTokenRejectsTheEmptyToken(t *testing.T) {
	d := testDB(t)
	// A publisher sending no token must not authenticate as whichever source
	// happens to have an empty one stored.
	if _, err := d.SQL().Exec(`UPDATE sources SET token = ''`); err != nil {
		t.Fatalf("blank the token: %v", err)
	}
	if _, err := d.SourceByToken(""); err == nil {
		t.Fatal("SourceByToken(\"\") matched a source; anyone could publish")
	}
	if _, err := d.SourceByToken("   "); err == nil {
		t.Fatal("SourceByToken(whitespace) matched a source")
	}
}

func TestSourceByTokenResolvesTheRightSource(t *testing.T) {
	d := testDB(t)
	vert := &Source{Name: "Vertical", Enabled: true, Ingest: DefaultSettings().Ingest}
	if err := d.CreateSource(vert); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	got, err := d.SourceByToken(vert.Token)
	if err != nil {
		t.Fatalf("SourceByToken: %v", err)
	}
	if got.ID != vert.ID {
		t.Errorf("resolved source %d, want %d", got.ID, vert.ID)
	}
}

func TestDeletingASourceTakesItsDestinationsButKeepsItsRecordings(t *testing.T) {
	d := testDB(t)

	extra := &Source{Name: "Vertical", Enabled: true, Ingest: DefaultSettings().Ingest}
	if err := d.CreateSource(extra); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	dst, err := d.CreateDestination(&Destination{
		Name: "tiktok", Kind: DestRTMP, URL: "rtmp://example/live", SourceID: &extra.ID})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	if _, err := d.SQL().Exec(
		`INSERT INTO recordings (filename, started_at, source_id) VALUES ('v.mkv', 1, ?)`, extra.ID); err != nil {
		t.Fatalf("seed recording: %v", err)
	}

	if err := d.DeleteSource(extra.ID); err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}

	var dests int
	if err := d.SQL().QueryRow(`SELECT COUNT(*) FROM destinations WHERE id = ?`, dst.ID).Scan(&dests); err != nil {
		t.Fatalf("count destinations: %v", err)
	}
	if dests != 0 {
		t.Error("destination survived its source; it points nowhere meaningful now")
	}

	// The file is still on disk and still playable, and its transcript and
	// clips hang off this row. Deleting it would orphan all of that.
	var recs int
	var srcID *int64
	if err := d.SQL().QueryRow(`SELECT COUNT(*) FROM recordings WHERE filename = 'v.mkv'`).Scan(&recs); err != nil {
		t.Fatalf("count recordings: %v", err)
	}
	if recs != 1 {
		t.Fatal("recording was deleted with its source; the file is now orphaned")
	}
	if err := d.SQL().QueryRow(`SELECT source_id FROM recordings WHERE filename = 'v.mkv'`).Scan(&srcID); err != nil {
		t.Fatalf("read recording source_id: %v", err)
	}
	if srcID != nil {
		t.Errorf("recording source_id = %d, want NULL after the source was deleted", *srcID)
	}
}

func TestTheLastSourceCannotBeDeleted(t *testing.T) {
	d := testDB(t)
	id, err := d.DefaultSourceID()
	if err != nil {
		t.Fatalf("DefaultSourceID: %v", err)
	}
	// An install with no sources has no ingest and no way back through the UI.
	if err := d.DeleteSource(id); err == nil {
		t.Fatal("deleted the only source; the install now has no ingest at all")
	}
}

// And the state on the other side of that guard, which the zero-source work
// makes reachable.
//
// The count check was `n <= 1`, so with NO sources the store answered "cannot
// delete the only source: an install needs at least one ingest" -- a sentence
// about an ingest the install does not have, shown to an operator on a fresh
// install who clicked a stale row or to a client retrying a delete that already
// succeeded. It also hid the true answer, which is that the row is not there.
func TestDeletingASourceOnAnInstallWithNoneSaysTheRowIsMissing(t *testing.T) {
	d := testDB(t)
	id, err := d.DefaultSourceID()
	if err != nil {
		t.Fatalf("DefaultSourceID: %v", err)
	}
	// Straight to SQL: the guard above is exactly what stops the API producing
	// this state today, and PR 6 is what removes it.
	if _, err := d.SQL().Exec(`DELETE FROM sources`); err != nil {
		t.Fatalf("empty the sources table: %v", err)
	}

	err = d.DeleteSource(id)
	if !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("DeleteSource on an install with no sources returned %v, want "+
			"ErrSourceNotFound. The old answer described the last source of an install "+
			"that has none, and the API maps that sentinel to the 404 the row deserves.",
			err)
	}
}

func TestSourceValidationRejectsABadIngest(t *testing.T) {
	d := testDB(t)
	bad := &Source{Name: "Broken", Enabled: true, Ingest: DefaultSettings().Ingest}
	// SRT's own constraint: 10..79 characters. Ports are no longer a way to be
	// invalid, because a source no longer has one.
	bad.Ingest.SRT.Passphrase = "short"
	err := d.CreateSource(bad)
	if err == nil {
		t.Fatal("CreateSource accepted an SRT passphrase SRT itself will refuse")
	}
	// Shared with Settings.Validate, so the message the form shows is the same
	// wording in both places.
	if !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("error = %q, want it to name the passphrase", err)
	}
}

func TestUpdateSourceNeverStoresAnEmptyToken(t *testing.T) {
	d := testDB(t)
	got, err := d.ListSources()
	if err != nil || len(got) == 0 {
		t.Fatalf("ListSources: %v", err)
	}
	s := got[0]
	s.Token = ""
	if err := d.UpdateSource(s); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	if s.Token == "" {
		t.Fatal("UpdateSource stored an empty token: anyone reaching the port could publish")
	}
	back, err := d.GetSource(s.ID)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	if back.Token != s.Token {
		t.Errorf("stored token %q, want %q", back.Token, s.Token)
	}
}

func TestRotationKeepsThePreviousTokenAliveForAGraceWindow(t *testing.T) {
	d := testDB(t)
	before, err := d.ListSources()
	if err != nil || len(before) == 0 {
		t.Fatalf("ListSources: %v", err)
	}
	old := before[0].Token

	fresh, err := d.RotateSourceToken(before[0].ID)
	if err != nil {
		t.Fatalf("RotateSourceToken: %v", err)
	}
	if fresh == old {
		t.Fatal("rotate returned the same token")
	}

	got, err := d.GetSource(before[0].ID)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	now := time.Now()
	valid := got.ValidTokens(now)

	// Both work during the window. An encoder already publishing on the old
	// token must not be cut off the instant somebody clicks Rotate.
	if !slices.Contains(valid, fresh) {
		t.Error("the new token is not accepted")
	}
	if !slices.Contains(valid, old) {
		t.Error("the previous token was dropped immediately; rotating would kill a live stream")
	}

	// And it expires on its own.
	if after := got.ValidTokens(now.Add(TokenGrace + time.Minute)); slices.Contains(after, old) {
		t.Error("the previous token outlived its grace window")
	}
}

func TestASourceThatHasNeverRotatedAcceptsOnlyItsOwnToken(t *testing.T) {
	d := testDB(t)
	rows, err := d.ListSources()
	if err != nil || len(rows) == 0 {
		t.Fatalf("ListSources: %v", err)
	}
	// No empty string in the accepted set: a publisher sending no streamid must
	// never match a source that has simply never been rotated.
	valid := rows[0].ValidTokens(time.Now())
	if len(valid) != 1 || valid[0] != rows[0].Token {
		t.Errorf("ValidTokens = %v, want exactly the live token", valid)
	}
}

func TestADestinationWithNoSourceIsRefusedAtTheBoundary(t *testing.T) {
	d := testDB(t)
	dst, err := d.CreateDestination(&Destination{
		Name: "orphan", Kind: DestRTMP, URL: "rtmp://example/live"})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	// Only reachable by editing the database by hand or by a half-finished
	// migration -- which is exactly when it must fail loudly. A row with no
	// source belongs to no programme, so nothing ever starts it: it would be
	// listed by the API and silently do nothing forever.
	if _, err := d.SQL().Exec(`UPDATE destinations SET source_id = NULL WHERE id = ?`, dst.ID); err != nil {
		t.Fatalf("null the source: %v", err)
	}
	if _, err := d.GetDestination(dst.ID); err == nil {
		t.Fatal("a destination with no source was read back as valid")
	}
	if _, err := d.ListDestinations(); err == nil {
		t.Fatal("listing tolerated a destination with no source")
	}
}

func TestARenditionWithNoSourceIsRefusedAtTheBoundary(t *testing.T) {
	d := testDB(t)
	r, err := d.CreateRendition(&Rendition{
		Name: "orphan", Height: 720, VideoBitrate: 3000})
	if err != nil {
		t.Fatalf("CreateRendition: %v", err)
	}
	if _, err := d.SQL().Exec(`UPDATE renditions SET source_id = NULL WHERE id = ?`, r.ID); err != nil {
		t.Fatalf("null the source: %v", err)
	}
	if _, err := d.GetRendition(r.ID); err == nil {
		t.Fatal("a rendition with no source was read back as valid")
	}
}

// How many sources an install can run must NOT depend on the protocol.
//
// This is the whole point of internal/rtmpserver, asserted at the layer that
// used to refuse it: checkRTMPExclusive capped an install at one RTMP source
// because `ffmpeg -listen 1` cannot demultiplex by path. If this ever fails
// again, the rule has come back and RTMP is a second-class ingest once more.
func TestSeveralSourcesMayUseRTMP(t *testing.T) {
	d := testDB(t)
	if err := d.MigrateSources(); err != nil {
		t.Fatalf("MigrateSources: %v", err)
	}
	first, err := d.ListSources()
	if err != nil || len(first) == 0 {
		t.Fatalf("ListSources: %v", err)
	}
	base := first[0]
	base.Ingest.Mode = IngestRTMP
	base.Ingest.RTMP.App = "live"
	if err := d.UpdateSource(base); err != nil {
		t.Fatalf("putting the first source on RTMP: %v", err)
	}

	second := &Source{Name: "Vertical", Enabled: true, Ingest: DefaultSettings().Ingest}
	second.Ingest.Mode = IngestRTMP
	second.Ingest.RTMP.App = "live"
	if err := d.CreateSource(second); err != nil {
		t.Fatalf("a second RTMP source was refused: %v", err)
	}
	// Two distinct addresses, or the pair is worse than the refusal was: one
	// programme would answer for the other with nothing to say it had.
	if second.Token == "" || second.Token == base.Token {
		t.Fatalf("tokens = %q and %q, want two distinct addresses", base.Token, second.Token)
	}
}

// A source that keeps RTMP across an update must save. Trivial now, and it was
// trivial to get wrong before: an off-by-one on checkRTMPExclusive's excludeID
// made a source conflict with itself and become unsaveable.
func TestASourceKeepingRTMPCanStillBeSaved(t *testing.T) {
	d := testDB(t)
	if err := d.MigrateSources(); err != nil {
		t.Fatalf("MigrateSources: %v", err)
	}
	rows, _ := d.ListSources()
	s := rows[0]
	s.Ingest.Mode = IngestRTMP
	s.Ingest.RTMP.App = "live"
	if err := d.UpdateSource(s); err != nil {
		t.Fatalf("first save: %v", err)
	}
	s.Name = "Renamed"
	if err := d.UpdateSource(s); err != nil {
		t.Fatalf("a source on RTMP could not be saved again: %v", err)
	}
}

// The default settings must mint NO RTMP stream key.
//
// It used to be "stream" for every source, which was harmless while the key was
// a playpath FFmpeg checked and fatal the moment it became an address: two
// sources from the defaults would have claimed the same one, and
// engine.legacyRTMPKeys would have had to refuse both. A default here is also
// what would let the grandfather clause for upgraded installs collide with a
// source created afterwards.
func TestDefaultSettingsMintNoRTMPStreamKey(t *testing.T) {
	def := DefaultSettings()
	if k := def.Ingest.RTMP.StreamKey; k != "" {
		t.Errorf("default rtmp stream key = %q, want empty: every new source would claim it", k)
	}
	if k := def.Failover.Backup.RTMP.StreamKey; k != "" {
		t.Errorf("default backup rtmp stream key = %q, want empty", k)
	}
}

// Nothing in a source binds a port any more, so two sources created from the
// defaults must both be creatable and both be addressable.
func TestTwoSRTSourcesCoexistWithoutPorts(t *testing.T) {
	d := testDB(t)
	if err := d.MigrateSources(); err != nil {
		t.Fatalf("MigrateSources: %v", err)
	}
	for _, name := range []string{"Vertical", "Third"} {
		s := &Source{Name: name, Enabled: true, Ingest: DefaultSettings().Ingest}
		if err := d.CreateSource(s); err != nil {
			t.Fatalf("creating %q: %v", name, err)
		}
	}
	rows, err := d.ListSources()
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d sources, want 3", len(rows))
	}
	// Distinct tokens are what makes them distinguishable now.
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Token == "" {
			t.Fatalf("source %q has no token, so it has no address", r.Name)
		}
		if seen[r.Token] {
			t.Fatalf("two sources share a token; one of them is unreachable")
		}
		seen[r.Token] = true
	}
}
