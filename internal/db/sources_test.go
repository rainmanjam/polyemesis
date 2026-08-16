package db

import (
	"database/sql"
	"errors"
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
//
// They also guard the other direction, which arrived with zero-source installs:
// a FRESH database must finish Open with no source at all, so the operator
// creates their own rather than inheriting a programme nobody configured. The
// two cases are told apart by a discriminator that is invisible when it is
// wrong -- both failure modes are silent -- so each branch of it has a test
// here named after the state it protects.

// preSourcesDB builds a database at path in the shape an install had before
// sources existed, and leaves it closed and ready for Open.
//
// The three tables that later grow a source_id are hand-built WITHOUT one, and
// the sources table is not created at all: schema.sql creates it empty on the
// next Open. Everything else is left to Open, whose CREATEs are all IF NOT
// EXISTS and whose earlier migrations add their own columns to these tables --
// which is what makes this the real upgrade path rather than an imitation of
// it. The DDL is schema.sql's own, minus source_id and its FOREIGN KEY line.
//
// The raw driver rather than db.Open, and that is load-bearing twice over: the
// raw DSN carries no pragmas, so foreign keys are OFF and rows can be inserted
// into tables whose sources table does not exist yet -- and Open is the thing
// under test, which cannot also be the thing that builds the fixture.
//
// extra runs after the tables exist, for the rows a caller wants to predate
// sources.
func preSourcesDB(t *testing.T, path, settingsJSON string, extra ...string) {
	t.Helper()
	preSourcesTables(t, path, settingsJSON,
		[]string{"destinations", "renditions", "recordings"}, extra...)
}

// preSourcesTables is preSourcesDB for an install that predates some of these
// tables as well as sources.
//
// A table left out is not created at all, so schema.sql creates it on the next
// Open -- WITH source_id, in schema.sql's shape. That is the real upgrade path
// for a database older than renditions, and it is the only fixture in which
// `migrating` is the sole witness: the tables schema.sql builds carry no
// evidence of an upgrade, and the one that predates it carries no column to read
// evidence from.
func preSourcesTables(t *testing.T, path, settingsJSON string, tables []string, extra ...string) {
	t.Helper()

	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	ddl := map[string]string{"destinations": `CREATE TABLE destinations (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		name           TEXT    NOT NULL,
		kind           TEXT    NOT NULL,
		platform       TEXT    NOT NULL DEFAULT '',
		account_id     INTEGER,
		url            TEXT    NOT NULL DEFAULT '',
		stream_key     TEXT    NOT NULL DEFAULT '',
		stream_key_enc BLOB,
		enabled        INTEGER NOT NULL DEFAULT 0,
		audio_bitrate  INTEGER NOT NULL DEFAULT 160,
		profile        TEXT    NOT NULL,
		rendition_id   INTEGER,
		position       INTEGER NOT NULL DEFAULT 0,
		created_at     INTEGER NOT NULL,
		updated_at     INTEGER NOT NULL
	)`, "renditions": `CREATE TABLE renditions (
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
		deinterlace   TEXT    NOT NULL DEFAULT '',
		created_at    INTEGER NOT NULL,
		updated_at    INTEGER NOT NULL
	)`, "recordings": `CREATE TABLE recordings (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		filename    TEXT    NOT NULL UNIQUE,
		started_at  INTEGER NOT NULL,
		finished_at INTEGER NOT NULL DEFAULT 0,
		bytes       INTEGER NOT NULL DEFAULT 0,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		tracks      INTEGER NOT NULL DEFAULT 0
	)`}
	stmts := []string{`CREATE TABLE settings (
		id   INTEGER PRIMARY KEY CHECK (id = 1),
		json TEXT    NOT NULL
	)`}
	for _, table := range tables {
		create, ok := ddl[table]
		if !ok {
			t.Fatalf("no pre-sources DDL for %q", table)
		}
		stmts = append(stmts, create)
	}
	stmts = append(stmts, `INSERT INTO settings (id, json) VALUES (1, '`+settingsJSON+`')`)
	stmts = append(stmts, extra...)
	for _, s := range stmts {
		if _, err := old.Exec(s); err != nil {
			t.Fatalf("build pre-sources database (%.40s): %v", s, err)
		}
	}

	// Prove the fixture is genuinely old before Open is ever called. Without
	// this the whole file could pass against a database that already had the
	// columns, which is the fresh case wearing a pre-sources costume.
	for _, table := range tables {
		if _, err := old.Exec(`SELECT source_id FROM ` + table); err == nil {
			t.Fatalf("%s.source_id already exists on the hand-built table; this test proves nothing", table)
		}
	}
	// Closed, and the error checked, because Open runs in WAL mode: until the
	// last connection closes and checkpoints, the committed tables are still in
	// the -wal file and the reopen would find an empty database.
	if err := old.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}
}

// A single-ingest install: RTMP with an app name and a stream key. If any of it
// fails to survive, the encoder stops connecting.
const preSourcesIngestJSON = `{"ingest":{"mode":"rtmp","rtmp":{"app":"live","streamKey":"secretkey"}}}`

func TestMigrationCarriesAnExistingIngestOntoTheFirstSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-sources.db")
	preSourcesDB(t, path, preSourcesIngestJSON)

	// Open, and only Open, is what an existing install actually runs on
	// startup; it must migrate, because nothing else will.
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a pre-sources database: %v", err)
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
	// Asserted only here, in the migrating case. A fresh install has no source
	// to be named anything.
	if s.Name != DefaultSourceName {
		t.Errorf("name = %q, want %q", s.Name, DefaultSourceName)
	}
	if !s.Enabled {
		t.Error("the migrated source is disabled; the install would stop ingesting")
	}
	if s.Ingest.Mode != IngestRTMP {
		t.Errorf("mode = %q, want rtmp", s.Ingest.Mode)
	}
	if s.Ingest.RTMP.App != "live" {
		t.Errorf("rtmp app = %q, want it carried across", s.Ingest.RTMP.App)
	}
	if s.Ingest.RTMP.StreamKey != "secretkey" {
		t.Errorf("stream key = %q, want it carried across", s.Ingest.RTMP.StreamKey)
	}
	if s.Token == "" {
		t.Error("migrated source has no publish token")
	}
}

func TestMigrationBackfillsExistingRowsOntoTheFirstSource(t *testing.T) {
	d := testDB(t)

	dst, err := d.CreateDestination(&Destination{
		Name: "pre-existing", Kind: DestRTMP, URL: "rtmp://example/live"})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	// Simulate a row written before sources existed.
	if _, err := d.SQL().Exec(`UPDATE destinations SET source_id = NULL`); err != nil {
		t.Fatalf("null out source_id: %v", err)
	}
	if err := d.MigrateSources(); err != nil {
		t.Fatalf("MigrateSources: %v", err)
	}

	want, err2 := d.DefaultSourceID()
	if err2 != nil {
		t.Fatalf("DefaultSourceID: %v", err2)
	}
	var got *int64
	if err := d.SQL().QueryRow(`SELECT source_id FROM destinations WHERE id = ?`, dst.ID).Scan(&got); err != nil {
		t.Fatalf("read back source_id: %v", err)
	}
	if got == nil {
		t.Fatal("destination still has a NULL source_id after the backfill")
	}
	if *got != want {
		t.Errorf("source_id = %d, want %d", *got, want)
	}
}

// The pre-sources install whose only history is recordings.
//
// It has no destinations and no renditions, so `orphans` is false and the seed
// fires on `migrating` alone. The recordings must STILL be attached: gating the
// backfill on `orphans` -- which counts destinations and renditions only, and
// deliberately not recordings -- seeds Main and then leaves every recording the
// operator ever made unattributed, with nothing to say so.
func TestMigrationAttachesRecordingsOnAnInstallThatHadNothingElse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-sources-recordings.db")
	preSourcesDB(t, path, preSourcesIngestJSON,
		`INSERT INTO recordings (filename, started_at) VALUES ('old.mkv', 1000)`)

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a pre-sources database: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	want, err := d.DefaultSourceID()
	if err != nil {
		t.Fatalf("DefaultSourceID: %v", err)
	}
	var got *int64
	if err := d.SQL().QueryRow(
		`SELECT source_id FROM recordings WHERE filename = 'old.mkv'`).Scan(&got); err != nil {
		t.Fatalf("read back source_id: %v", err)
	}
	if got == nil {
		t.Fatal("the recordings that predate sources came back unattached, " +
			"so the library shows them belonging to nothing")
	}
	if *got != want {
		t.Errorf("source_id = %d, want %d", *got, want)
	}
}

// The other half of the discriminator: a database with nothing behind it must
// come up with NO source, and stay that way however many times it is opened.
//
// Seeding one here is not a cosmetic difference. It hands a first-time operator
// a programme they did not configure, on an ingest mode nobody chose, and it is
// what the whole zero-source install exists to stop.
//
// GetSettings is called on every boot, and that is why it is called on every
// boot HERE. It SEEDS a settings row when it finds none, so a real install has
// one from its second start onwards -- main.go reads settings during startup,
// and so does the settings page. A version of this test that only opens the
// database never materialises that row, and then stays green against the
// forbidden discriminator MUST NOT #4 names by name: widen the seed condition to
// "or a settings row exists" and every fresh install grows Main on boot 2 with
// the whole suite passing. The one line below is what makes this test able to
// see that.
func TestAFreshDatabaseComesUpWithNoSourceAndStaysThatWay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")

	for i := 1; i <= 3; i++ {
		d, err := Open(path)
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		got, err := d.ListSources()
		if err != nil {
			t.Fatalf("ListSources after open %d: %v", i, err)
		}
		if len(got) != 0 {
			t.Fatalf("open %d left %d sources on a fresh database; "+
				"a new install must ask the operator for one, not invent it", i, len(got))
		}
		if _, err := d.GetSettings(); err != nil {
			t.Fatalf("GetSettings on boot %d: %v", i, err)
		}
		if err := d.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}
}

// An operator who deletes their last source has said something. The next boot
// must not undo it.
//
// This is the case that forbids counting recordings as orphans:
// recordings.source_id is ON DELETE SET NULL by design, so a NULL recording is
// the NORMAL state after a legitimate delete. A probe that included them would
// read this database as "pre-sources" and put Main back -- on the install with
// the largest library, every time it starts.
//
// Built against the store rather than through DeleteSource, which still refuses
// to remove the only source. The route to this state arrives with that guard's
// removal; the state itself is reachable now and the discriminator has to be
// right about it before the button exists.
func TestMigrationDoesNotResurrectASourceTheOperatorDeleted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deleted-last-source.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	src := &Source{Name: "Main camera", Enabled: true, Ingest: DefaultSettings().Ingest}
	if err := d.CreateSource(src); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if _, err := d.SQL().Exec(
		`INSERT INTO recordings (filename, started_at, source_id) VALUES ('kept.mkv', 1000, ?)`,
		src.ID); err != nil {
		t.Fatalf("seed recording: %v", err)
	}
	// Foreign keys are ON through this handle, so this is exactly what the
	// delete route will do: the recording survives with a NULL source_id.
	if _, err := d.SQL().Exec(`DELETE FROM sources`); err != nil {
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
		t.Fatalf("%q came back after the operator deleted their last source", got[0].Name)
	}
	var attached *int64
	if err := again.SQL().QueryRow(
		`SELECT source_id FROM recordings WHERE filename = 'kept.mkv'`).Scan(&attached); err != nil {
		t.Fatalf("read back source_id: %v", err)
	}
	if attached != nil {
		t.Errorf("the orphan recording was attached to source %d, which the boot invented", *attached)
	}
}

// The oldest blob of all: one written before ingest had a mode field.
//
// GetSettings decodes an existing blob onto mergeBaseSettings for exactly this,
// and says so: an omitted mode inheriting the unset default "would stop
// ingesting on upgrade -- a silent regression on exactly the servers that were
// working". The migration read the same blobs off the same base as a fresh
// install and produced a source with no mode, which spawns no ingest. The
// install it happens to is the pre-sources one, which is the oldest install
// there is and therefore the likeliest to have such a blob.
func TestMigrationGivesAModeToABlobWrittenBeforeThereWasOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-mode.db")
	preSourcesDB(t, path, `{"ingest":{"srt":{"latencyMs":300}}}`)

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a pre-sources database: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	got, err := d.ListSources()
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sources, want exactly 1", len(got))
	}
	if got[0].Ingest.Mode != IngestSRT {
		t.Errorf("mode = %q, want srt: a source with no mode spawns no ingest, "+
			"so this install comes up unreachable by the encoder already pointed at it",
			got[0].Ingest.Mode)
	}
	// The rest of the blob still has to arrive, or the mode is right and the
	// port it listens on is not.
	if got[0].Ingest.SRT.LatencyMS != 300 {
		t.Errorf("srt latency = %d, want 300 carried across", got[0].Ingest.SRT.LatencyMS)
	}
}

// The rotation columns are NOT evidence of a pre-sources install.
//
// prev_token and prev_token_until belong to the token-rotation release, which
// came AFTER sources. Folding their absence into the same flag is an easy tidy
// -- both loops are columnExists over an ALTER list -- and it reads as
// "pre-sources" for ever on a database that migrated to sources before rotation
// existed. Harmless while a source is there, and once the last-source delete
// guard goes it puts Main back on every boot.
func TestTheRotationColumnsAreNotEvidenceOfAPreSourcesInstall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-rotation.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Wind the sources table back to before rotation shipped, leaving every
	// source_id column exactly where this release put it.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	for _, s := range []string{
		`ALTER TABLE sources DROP COLUMN prev_token`,
		`ALTER TABLE sources DROP COLUMN prev_token_until`,
	} {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
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
		t.Fatalf("a missing rotation column seeded %q; that column says nothing "+
			"about whether this install predates sources", got[0].Name)
	}
}

// The columns arrived but the source never did. `migrating` cannot see this --
// every column exists -- so the second witness has to: a destination or
// rendition with a NULL source_id is a row from a life before sources, and an
// install that has one is not a fresh install.
//
// Reachable from a release that added the columns in one pass and the source in
// another, and from any database an operator hand-edited with the sqlite3 CLI,
// where foreign keys default to OFF.
//
// The destination is written with the raw driver onto a database that has NEVER
// held a source, and both halves of that are load-bearing. Building this state
// by creating a source and deleting it again produces the DIFFERENT install --
// the one that had a source and lost it, which sqlite_sequence remembers for
// ever and which must not be reseeded. It would also leave `orphans` as the only
// thing under test by accident rather than by design: here the column shapes are
// schema.sql's, so the shape witness reads false and this destination is the
// sole reason the seed fires.
func TestMigrationSeedsWhenTheColumnsArrivedButTheSourceNeverDid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "columns-without-source.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The raw handle, because its DSN carries no pragmas: with foreign keys off
	// a destination can be written with no source to belong to, which is the
	// state being reproduced and which the store itself refuses to create.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO destinations
		(name, kind, url, profile, created_at, updated_at)
		VALUES ('pre-existing', 'rtmp', 'rtmp://example/live', '{}', 1000, 1000)`); err != nil {
		t.Fatalf("insert an orphan destination: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
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
	if len(got) != 1 {
		t.Fatalf("got %d sources, want the migration to have built one for the "+
			"destination that has none", len(got))
	}
	var attached *int64
	if err := again.SQL().QueryRow(
		`SELECT source_id FROM destinations WHERE name = 'pre-existing'`).Scan(&attached); err != nil {
		t.Fatalf("read back source_id: %v", err)
	}
	if attached == nil || *attached != got[0].ID {
		t.Errorf("destination source_id = %v, want %d", attached, got[0].ID)
	}
}

// THE INTERRUPTED UPGRADE. This is the case the transaction exists for, and the
// only test that fails if someone finds it inconvenient and unwinds it.
//
// Without one, MigrateSources adds the source_id columns, commits them, and
// then creates the source. A process that dies in between -- crash, SIGKILL,
// power loss, `docker stop` -- leaves a database where the columns exist and
// the source does not. On the next open `migrating` is false because the
// columns are there, `orphans` is false because this install had no
// destinations or renditions to orphan, and the count is zero: the
// discriminator says "fresh install", the operator's ingest is never carried
// across, and all they see is that their encoder stopped connecting.
//
// The interruption here is a settings blob whose ingest no longer validates --
// an RTMP app name, required since the publish URL was made copyable, that an
// older release let through empty. It is a real failure, it happens after the
// ALTERs, and it is the only lever this test needs: the migration must leave
// the database in the state it found it, so the repaired install still
// migrates.
func TestAnInterruptedMigrationStillCarriesTheIngestOnTheNextBoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "interrupted.db")
	preSourcesDB(t, path, `{"ingest":{"mode":"rtmp","rtmp":{"app":"","streamKey":"secretkey"}}}`)

	if _, err := Open(path); err == nil {
		t.Fatal("Open accepted an ingest that does not validate; " +
			"this test needs the seed step to fail after the ALTER loop")
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := raw.Exec(
		`UPDATE settings SET json = '` + preSourcesIngestJSON + `' WHERE id = 1`); err != nil {
		t.Fatalf("repair settings: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open after repairing the settings blob: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	got, err := d.ListSources()
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sources; the half-finished migration left the columns "+
			"behind, so this boot read a pre-sources install as a fresh one and "+
			"dropped its ingest", len(got))
	}
	if got[0].Ingest.RTMP.StreamKey != "secretkey" {
		t.Errorf("stream key = %q, want it carried across", got[0].Ingest.RTMP.StreamKey)
	}
}

// halfMigrateAsAnEarlierRelease replays exactly what a release older than this
// one committed before it created the source, and nothing else: the sources
// table its schema.sql created, and one ALTER TABLE per named table.
//
// Deliberately built with the raw driver on a database the store has never
// opened, because the point is a half-finished migration performed by code that
// no longer exists here. The sources table is the pre-rotation one -- no
// prev_token -- since that is what an install this old actually has, and it
// keeps the rotation columns on the path this fixture exercises.
func halfMigrateAsAnEarlierRelease(t *testing.T, path string, tables ...string) {
	t.Helper()

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	stmts := []string{`CREATE TABLE sources (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT    NOT NULL,
		enabled    INTEGER NOT NULL DEFAULT 1,
		ingest     TEXT    NOT NULL,
		token      TEXT    NOT NULL DEFAULT '',
		position   INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`}
	onDelete := map[string]string{
		"destinations": "CASCADE", "renditions": "CASCADE", "recordings": "SET NULL"}
	for _, table := range tables {
		stmts = append(stmts, `ALTER TABLE `+table+
			` ADD COLUMN source_id INTEGER REFERENCES sources(id) ON DELETE `+onDelete[table])
	}
	for _, s := range stmts {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("replay the earlier release (%.40s): %v", s, err)
		}
	}
	// The state this fixture is: columns committed, sources empty. Asserted
	// rather than assumed, because a fixture that quietly created a source would
	// make every test built on it prove the opposite of what it claims.
	var n int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sources`).Scan(&n); err != nil {
		t.Fatalf("count sources: %v", err)
	}
	if n != 0 {
		t.Fatalf("the fixture created %d sources; an interrupted migration has none", n)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}
}

// THE UPGRADE INTERRUPTED ON A RELEASE OLDER THAN THIS ONE.
//
// The transaction above stops this state being created from here on. It cannot
// repair a database already in it -- and that is the state this build meets on
// its first boot, because the previous release committed the three ALTERs one at
// a time and created the source afterwards. Anything in between left the columns
// and no source: a crash, `docker stop`, or, with no crash at all, a stored
// ingest that release's validator rejected, which failed Open with the columns
// already committed.
//
// Every witness but one is blind to it. The columns are all present, so
// `migrating` is false; an install whose history is an ingest and a library has
// no destination or rendition to orphan; and the count is zero, which is the
// question rather than the answer. What is left is the SHAPE of those columns,
// which says ALTER wrote them and therefore that this database predates sources.
//
// Get this wrong and the operator repairs their settings, upgrades to the build
// that was meant to fix it, and boots into a fresh install: their passphrase,
// their latency, their whole ingest never reaches a source, and the only symptom
// is that the encoder stops connecting.
func TestAnUpgradeInterruptedByAnEarlierReleaseStillCarriesTheIngest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "half-migrated.db")
	preSourcesDB(t, path,
		`{"ingest":{"mode":"srt","srt":{"passphrase":"averylongpassphrase","latencyMs":1500}}}`,
		`INSERT INTO recordings (filename, started_at) VALUES ('old.mkv', 1000)`)
	halfMigrateAsAnEarlierRelease(t, path, "destinations", "renditions", "recordings")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a half-migrated database: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	got, err := d.ListSources()
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sources: an upgrade the PREVIOUS release left half-done "+
			"came up as a fresh install, so the operator's SRT ingest is gone", len(got))
	}
	if got[0].Ingest.Mode != IngestSRT {
		t.Errorf("mode = %q, want srt carried across", got[0].Ingest.Mode)
	}
	if got[0].Ingest.SRT.Passphrase != "averylongpassphrase" {
		t.Errorf("passphrase = %q, want it carried across", got[0].Ingest.SRT.Passphrase)
	}
	if got[0].Ingest.SRT.LatencyMS != 1500 {
		t.Errorf("srt latency = %d, want 1500 carried across", got[0].Ingest.SRT.LatencyMS)
	}
	// The library the operator already had comes with it: this install is being
	// given its first source, so nothing here can be the residue of a delete.
	var attached *int64
	if err := d.SQL().QueryRow(
		`SELECT source_id FROM recordings WHERE filename = 'old.mkv'`).Scan(&attached); err != nil {
		t.Fatalf("read back source_id: %v", err)
	}
	if attached == nil || *attached != got[0].ID {
		t.Errorf("recording source_id = %v, want %d", attached, got[0].ID)
	}
}

// A recording orphaned by a legitimate delete belongs to nobody, and the next
// boot must not hand it to whoever is left.
//
// recordings.source_id is ON DELETE SET NULL precisely so a recording outlives
// its source: the file is still on disk and still playable. A backfill that
// attaches every NULL recording to DefaultSourceID() therefore re-attributes one
// programme's entire archive to an unrelated one, on the next start, permanently
// and with nothing in the library to say it happened -- and DeleteSource's own
// documentation promises the opposite.
func TestTheBackfillDoesNotClaimTheRecordingsOfADeletedSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "two-programmes.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	studioA := &Source{Name: "Studio A", Enabled: true, Ingest: DefaultSettings().Ingest}
	if err := d.CreateSource(studioA); err != nil {
		t.Fatalf("CreateSource A: %v", err)
	}
	studioB := &Source{Name: "Studio B", Enabled: true, Ingest: DefaultSettings().Ingest}
	if err := d.CreateSource(studioB); err != nil {
		t.Fatalf("CreateSource B: %v", err)
	}
	if _, err := d.SQL().Exec(
		`INSERT INTO recordings (filename, started_at, source_id) VALUES ('studio-a.mkv', 1000, ?)`,
		studioA.ID); err != nil {
		t.Fatalf("seed recording: %v", err)
	}

	// The real route. Two sources exist, so the last-source guard does not fire
	// and this is exactly what an operator deleting a programme does today.
	if err := d.DeleteSource(studioA.ID); err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}
	if orphaned := recordingSource(t, d, "studio-a.mkv"); orphaned != nil {
		t.Fatalf("the delete left source_id = %d; this test needs the designed "+
			"orphan state to exist before the reopen", *orphaned)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { again.Close() })

	if got := recordingSource(t, again, "studio-a.mkv"); got != nil {
		t.Errorf("the next boot gave a deleted programme's recording to source %d "+
			"(Studio B is %d); the library now credits it to a programme that never made it",
			*got, studioB.ID)
	}
}

// And the same theft one step later: delete the last source, create a new one,
// and the archive of the old must not follow the new one home.
//
// This is the state PR 6 hands an operator a button for, and the confirmation it
// is contracted to show says their recordings survive as orphans. A backfill
// that runs on every boot makes that sentence false one restart later.
func TestANewSourceDoesNotInheritADeletedSourcesRecordings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replaced-programme.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	old := &Source{Name: "Studio A", Enabled: true, Ingest: DefaultSettings().Ingest}
	if err := d.CreateSource(old); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if _, err := d.SQL().Exec(
		`INSERT INTO recordings (filename, started_at, source_id) VALUES ('studio-a.mkv', 1000, ?)`,
		old.ID); err != nil {
		t.Fatalf("seed recording: %v", err)
	}
	// Foreign keys are ON through this handle, so this is what the delete route
	// will do once the last-source guard goes: the recording survives with a
	// NULL source_id.
	if _, err := d.SQL().Exec(`DELETE FROM sources`); err != nil {
		t.Fatalf("delete the last source: %v", err)
	}
	replacement := &Source{Name: "Studio B", Enabled: true, Ingest: DefaultSettings().Ingest}
	if err := d.CreateSource(replacement); err != nil {
		t.Fatalf("CreateSource for the replacement: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { again.Close() })

	if got := recordingSource(t, again, "studio-a.mkv"); got != nil {
		t.Errorf("the boot handed a deleted programme's recording to source %d, "+
			"which is a programme created after that recording was made", *got)
	}
}

func recordingSource(t *testing.T, d *DB, filename string) *int64 {
	t.Helper()
	var id *int64
	if err := d.SQL().QueryRow(
		`SELECT source_id FROM recordings WHERE filename = ?`, filename).Scan(&id); err != nil {
		t.Fatalf("read source_id of %s: %v", filename, err)
	}
	return id
}

// The resurrection case on a MIGRATED install, which is the shape the fresh-
// database version of it cannot reach.
//
// A migrated install carries two things a fresh one does not: destinations and
// renditions that were backfilled onto Main and then taken by ON DELETE CASCADE,
// and source_id columns whose shape says an upgrade wrote them. The second is a
// witness that stays true for ever, so this database asks the discriminator the
// hardest form of the question -- there IS evidence of a life before sources
// here, and the answer must still be no, because this operator had a source and
// removed it.
func TestAMigratedInstallDoesNotResurrectMainAfterItsSourceIsDeleted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrated-then-deleted.db")
	preSourcesDB(t, path, preSourcesIngestJSON,
		`INSERT INTO destinations (name, kind, url, profile, created_at, updated_at)
			VALUES ('twitch', 'rtmp', 'rtmp://example/live', '{}', 1000, 1000)`,
		`INSERT INTO renditions (name, created_at, updated_at) VALUES ('720p', 1000, 1000)`,
		`INSERT INTO recordings (filename, started_at) VALUES ('old.mkv', 1000)`)

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a pre-sources database: %v", err)
	}
	got, err := d.ListSources()
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sources, want the migration to have built one", len(got))
	}
	// Foreign keys are ON, so this takes the backfilled destination and
	// rendition with it and leaves the recording behind -- the state a real
	// delete produces on an install that has been through the migration.
	if _, err := d.SQL().Exec(`DELETE FROM sources`); err != nil {
		t.Fatalf("delete the last source: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	for i := 1; i <= 3; i++ {
		again, err := Open(path)
		if err != nil {
			t.Fatalf("reopen %d: %v", i, err)
		}
		back, err := again.ListSources()
		if err != nil {
			t.Fatalf("ListSources after reopen %d: %v", i, err)
		}
		if len(back) != 0 {
			t.Fatalf("boot %d put %q back on a migrated install whose operator "+
				"deleted their last source", i, back[0].Name)
		}
		if err := again.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}
}

// An install older than renditions, which is where `migrating` has to be
// OR-accumulated rather than assigned.
//
// Only destinations predates sources here; schema.sql creates renditions and
// recordings on this very Open, WITH source_id and in its own shape. So the
// shape witness reads false, there is nothing orphaned, and `migrating` is the
// only thing left saying this install has a past -- and it says so on the FIRST
// table of the three. Collapse the loop to an assignment and the last table's
// answer wins, this database reads as fresh, and the ingest is dropped.
//
// The state is real: a database can predate two releases at once, and this is
// the pair that produces it.
func TestADatabaseOlderThanRenditionsStillMigratesOnItsDestinationsAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-renditions.db")
	preSourcesTables(t, path, preSourcesIngestJSON, []string{"destinations"},
		`INSERT INTO destinations (name, kind, url, profile, created_at, updated_at)
			VALUES ('twitch', 'rtmp', 'rtmp://example/live', '{}', 1000, 1000)`)

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a pre-renditions, pre-sources database: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	got, err := d.ListSources()
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sources: the only table that predates sources here is "+
			"destinations, and its answer was lost, so this upgrade read as a "+
			"fresh install and dropped the operator's ingest", len(got))
	}
	if got[0].Ingest.RTMP.StreamKey != "secretkey" {
		t.Errorf("stream key = %q, want it carried across", got[0].Ingest.RTMP.StreamKey)
	}
	var attached *int64
	if err := d.SQL().QueryRow(
		`SELECT source_id FROM destinations WHERE name = 'twitch'`).Scan(&attached); err != nil {
		t.Fatalf("read back source_id: %v", err)
	}
	if attached == nil || *attached != got[0].ID {
		t.Errorf("destination source_id = %v, want %d", attached, got[0].ID)
	}
}

// The shape witness reads schema.sql, so schema.sql can silently disarm it.
//
// sourceColumnArrivedByUpgrade tells a column ALTER added from one the table was
// created with, by the fact that schema.sql spells the reference as its own
// FOREIGN KEY clause while ALTER TABLE can only write it inline. Respell it
// inline in schema.sql and every FRESH database starts answering "this was an
// upgrade" -- which, on an install that has never had a source, seeds Main on
// the first boot: the one outcome the zero-source work exists to prevent.
//
// Nothing about that failure is visible in schema.sql, so it is asserted here
// instead, in both directions.
func TestTheFreshSchemaKeepsTheShapeTheUpgradeProbeReads(t *testing.T) {
	tables := []string{"destinations", "renditions", "recordings"}

	fresh, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("Open fresh: %v", err)
	}
	t.Cleanup(func() { fresh.Close() })
	for _, table := range tables {
		altered, err := sourceColumnArrivedByUpgrade(fresh.SQL(), table)
		if err != nil {
			t.Fatalf("probe fresh %s: %v", table, err)
		}
		if altered {
			t.Errorf("%s.source_id reads as ALTER-added on a fresh database. "+
				"schema.sql no longer declares it with its own FOREIGN KEY clause, so "+
				"the probe cannot tell a new install from an upgraded one and a new "+
				"install is about to be seeded a source it never asked for", table)
		}
	}

	path := filepath.Join(t.TempDir(), "migrated.db")
	preSourcesDB(t, path, preSourcesIngestJSON)
	migrated, err := Open(path)
	if err != nil {
		t.Fatalf("Open migrated: %v", err)
	}
	t.Cleanup(func() { migrated.Close() })
	for _, table := range tables {
		altered, err := sourceColumnArrivedByUpgrade(migrated.SQL(), table)
		if err != nil {
			t.Fatalf("probe migrated %s: %v", table, err)
		}
		if !altered {
			t.Errorf("%s.source_id reads as original on a database this migration "+
				"ALTERed; an upgrade interrupted before its source exists is now "+
				"indistinguishable from a first run", table)
		}
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
