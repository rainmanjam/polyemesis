package db

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A DATABASE WITH A CORRUPT TABLE PAGE BOOTED, AND NOTHING SAID SO where a
// monitor would look. The exploratory run (CH-09) damaged the hooks table's
// root page: the server started, /health answered {"status":"ok"} and Docker
// called the container healthy, while GET /hooks answered 500 and every hook
// had silently stopped firing. Ping reads page one, which was fine. The store
// now remembers what its real statements were told.
func TestACorruptTablePageIsReportedAsAStorageFault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var root, pageSize int64
	if err := d.sql.QueryRow(`SELECT rootpage FROM sqlite_master WHERE type='table' AND name='hooks'`).Scan(&root); err != nil {
		t.Fatalf("find the hooks table: %v", err)
	}
	if err := d.sql.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	// Overwrite the hooks table's root page with bytes no b-tree page has.
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{0xA5}, int(pageSize)), (root-1)*pageSize); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path)
	if err != nil {
		t.Skipf("this build refuses to open the damaged file (%v); the fault is reported at boot instead", err)
	}
	t.Cleanup(func() { d.Close() })

	if err := d.Ping(); err != nil {
		t.Fatalf("Ping failed (%v); the point of this test is that it does not", err)
	}
	if _, err := d.ListHooks(testBox(t)); err == nil {
		t.Fatal("reading the damaged hooks table succeeded; the corruption did not take")
	}
	f2, bad := d.StorageFault()
	if !bad {
		t.Fatal("the store read a corrupt page and reports no storage fault")
	}
	if !f2.Damaged || !strings.Contains(f2.Err, "malformed") {
		t.Errorf("fault = %+v, want a damaged-file fault carrying SQLite's message", f2)
	}

	// Damage is sticky: a write elsewhere succeeding does not mend the page.
	if err := d.CreateSource(&Source{Name: "other", Ingest: DefaultSettings().Ingest, Position: 9}); err != nil {
		t.Fatalf("a write to an undamaged table: %v", err)
	}
	if _, bad := d.StorageFault(); !bad {
		t.Error("a successful write elsewhere cleared the report of a damaged page")
	}
}

// Faults about the QUERY are not faults about the storage. A constraint
// violation must not turn /health's database check red.
func TestAQueryErrorIsNotAStorageFault(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "polyemesis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.sql.Exec(`INSERT INTO no_such_table VALUES (1)`); err == nil {
		t.Fatal("expected an error")
	}
	if f, bad := d.StorageFault(); bad {
		t.Errorf("a query error was recorded as a storage fault: %+v", f)
	}
}
