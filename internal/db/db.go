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

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// DB wraps the SQLite handle.
type DB struct {
	sql *sql.DB
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
	return &DB{sql: sqldb}, nil
}

// SQL exposes the underlying handle for the rare query that does not warrant
// a typed store method.
func (d *DB) SQL() *sql.DB { return d.sql }

// Close closes the database.
func (d *DB) Close() error { return d.sql.Close() }
