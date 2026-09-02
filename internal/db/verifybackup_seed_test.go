package db

import (
	"database/sql"
	"testing"
)

// sqldbSeed writes a valid SQLite database that is not polyemesis's.
func sqldbSeed(t *testing.T, path string) {
	t.Helper()
	d, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if _, err := d.Exec(`CREATE TABLE somebody_elses (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
}
