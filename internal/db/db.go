// Package db is the SQLite persistence layer: config, destinations, routing
// profiles, users, platform credentials and the recording index.
//
// Uses modernc.org/sqlite, a pure-Go translation of SQLite, so the whole
// server stays a single cgo-free static binary.
package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"sync"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// DB wraps the SQLite handle.
//
// Deliberately just that: no filesystem path and no logger. This package
// used to carry an optional data directory and logger for one migration's
// benefit (a legacy playlist FilePath, DESIGN 2026-08-01-playlist-items),
// which put filesystem I/O and logging on GetSettings's read path -- read by
// roughly twenty callers, several of them per-request API handlers. That
// migration now runs once at startup in cmd/polyemesis, where a real data
// directory and a real, configured logger already live; see
// LegacyPlaylistFilePath for the pure half this package still owns.
type DB struct {
	sql *sql.DB

	// settingsMu serialises READ-MODIFY-WRITE callers of the settings
	// singleton, and nothing else.
	//
	// The settings are ONE JSON document: PutSettings writes the whole blob,
	// so two callers that each read it, change a different field and write it
	// back do not merge -- whichever lands second silently discards every
	// field the other one changed, not just the field it meant to edit. Four
	// places in the running server do exactly that (the engine's scheduled
	// playlist flip, PUT /settings, the annotations mirror in PUT
	// /annotations, and PUT /jobs/policy), and before this mutex existed none
	// of them serialised against any of the others.
	//
	// The way it stays correct is that every such caller goes through
	// UpdateSettings. A lock is only a boundary while nobody walks around it,
	// and a new read-modify-write built out of GetSettings and PutSettings
	// would be exactly that walk.
	//
	// GetSettings and PutSettings deliberately do NOT take it. GetSettings
	// calls PutSettings to seed defaults on first run, so a lock in either
	// would deadlock the moment UpdateSettings -- which holds this across both
	// -- ran against a fresh database.
	settingsMu sync.Mutex
}

// Open opens (creating if needed) the database at path and applies the schema.
func Open(path string) (*DB, error) {
	// WAL keeps the API responsive while the retention sweeper writes, and
	// busy_timeout turns the rare writer collision into a short wait instead
	// of an SQLITE_BUSY error surfacing to the user.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// modernc's driver is not safe to hammer with many concurrent writers;
	// one connection removes a whole class of lock contention and this
	// workload is nowhere near needing more.
	sqldb.SetMaxOpenConns(1)

	if err := sqldb.Ping(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	if _, err := sqldb.Exec(schemaSQL); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	d := &DB{sql: sqldb}
	// Adds destinations.rendition_id to a database created before renditions
	// existed; CREATE TABLE IF NOT EXISTS cannot do it.
	if err := d.MigrateRenditions(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// Same story one release later: the expert-mode columns, plus draining the
	// sidecar table they were first stored in.
	if err := d.MigrateDestinationExpertArgs(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// And once more for the rendition aspect-conversion columns.
	if err := d.MigratePlatformAccountScopeVer(); err != nil {
		return nil, err
	}
	if err := d.MigrateRenditionAspect(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// users.token_epoch, so a password change can revoke sessions that are
	// already signed and in somebody's cookie jar.
	if err := d.MigrateUserTokenEpoch(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// Last, because it reads settings and writes to destinations, renditions
	// and recordings: every column those tables are going to have must already
	// be there. It also creates the first source from the existing ingest
	// configuration, which is what keeps an upgraded install reachable by the
	// encoder that was already pointed at it.
	if err := d.MigrateSources(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

// SQL exposes the underlying handle for the rare query that does not warrant
// a typed store method.
func (d *DB) SQL() *sql.DB { return d.sql }

// Close closes the database.
func (d *DB) Close() error { return d.sql.Close() }
