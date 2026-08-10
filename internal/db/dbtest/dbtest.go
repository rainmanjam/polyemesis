// Package dbtest hands tests in other packages a migrated database without
// paying to migrate one.
//
// db.Open executes the whole of schema.sql and then six migrations. That is
// the right thing for a real install, which does it once at startup, and the
// wrong thing for a test suite that does it 143 times in internal/api alone.
// The DDL, not the queries, is what made these packages slow -- and it is what
// makes them 40x slower again on a Windows runner, where the same pure-Go
// SQLite writes to a much slower filesystem.
//
// So the schema is built once per test binary, the resulting file snapshotted,
// and every caller handed a byte copy of it. internal/db's own tests do the
// same thing with an unexported helper; they cannot import this package
// because its tests are in package db and that would be an import cycle. The
// duplication is small and deliberate.
//
// Modelled on net/http/httptest: a normal package that imports testing,
// imported only by _test.go files, so it is never linked into the binary.
package dbtest

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/rainmanjam/polyemesis/internal/db"
)

var (
	templateOnce  sync.Once
	templateBytes []byte
	templateErr   error
)

// template returns the bytes of a database file that has had the schema and
// every migration applied to it.
func template() ([]byte, error) {
	templateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "polyemesis-dbtest")
		if err != nil {
			templateErr = fmt.Errorf("template tempdir: %w", err)
			return
		}
		// Nothing reopens this path once the bytes are read.
		defer os.RemoveAll(dir)

		path := filepath.Join(dir, "polyemesis.db")
		d, err := db.Open(path)
		if err != nil {
			templateErr = fmt.Errorf("template open: %w", err)
			return
		}
		// Closed before the file is read, and the ordering is load-bearing
		// rather than tidiness: Open runs in WAL mode, so until the last
		// connection closes and checkpoints, the committed schema is still in
		// polyemesis.db-wal and the file read here would be empty.
		if err := d.Close(); err != nil {
			templateErr = fmt.Errorf("template close: %w", err)
			return
		}
		templateBytes, templateErr = os.ReadFile(path)
	})
	return templateBytes, templateErr
}

// OpenAt returns a database at path, backed by a copy of the migrated
// template, closed when the test ends.
//
// path rather than a directory of this package's choosing because several
// callers put the database inside a t.TempDir() they also use as a DataDir,
// and the file has to land in that same tree.
//
// The copy is still handed to db.Open rather than returned as a live handle,
// which is what keeps this a pure speed change: the caller's options still
// apply, and the schema and migrations still run. They simply find nothing
// left to do, because every CREATE is IF NOT EXISTS and every migration checks
// for its column before adding it.
func OpenAt(t testing.TB, path string, opts ...db.Option) *db.DB {
	t.Helper()
	tmpl, err := template()
	if err != nil {
		t.Fatalf("build template: %v", err)
	}
	if err := os.WriteFile(path, tmpl, 0o600); err != nil {
		t.Fatalf("write template copy: %v", err)
	}
	store, err := db.Open(path, opts...)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// Open is OpenAt in a temporary directory the caller does not need to name.
func Open(t testing.TB, opts ...db.Option) *db.DB {
	t.Helper()
	return OpenAt(t, filepath.Join(t.TempDir(), "polyemesis.db"), opts...)
}

// OpenCheap is Open with the bcrypt cost floored.
//
// Every caller that creates a user wants this: #101 found the API suite
// spending its whole budget hashing passwords at the production cost. Kept as
// a separate constructor rather than the default so that a test which means to
// measure real hashing still can.
func OpenCheap(t testing.TB, opts ...db.Option) *db.DB {
	t.Helper()
	return Open(t, append([]db.Option{db.WithPasswordCost(bcrypt.MinCost)}, opts...)...)
}
