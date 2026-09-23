package db

import (
	"bytes"
	"fmt"
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

// DAMAGE PAST THE FIRST PAGE A QUERY READS surfaces while the rows are being
// iterated, not when the query opens: SELECT hands back every row up to the
// bad page and then rows.Err says "malformed". The first version of the fault
// log only looked at the open, so a table corrupt anywhere but its root page --
// the more likely case, since a large table is mostly leaves -- left /health
// saying ok while the reads came back short.
func TestACorruptLeafPageMidScanIsReportedAsAStorageFault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(`CREATE TABLE leafy(n INTEGER PRIMARY KEY, pad TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 400; i++ {
		if _, err := d.sql.Exec(`INSERT INTO leafy(pad) VALUES (?)`, strings.Repeat("x", 200)); err != nil {
			t.Fatal(err)
		}
	}
	var root, pageSize, pages int64
	if err := d.sql.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name='leafy'`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err := d.sql.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if err := d.sql.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	// leafy was the last thing to grow, and a root that splits keeps its page
	// number and moves its cells out, so every page after the root is one of
	// its leaves. Damage one in the middle: well past the root, with rows on
	// either side of it.
	if pages < root+4 {
		t.Fatalf("leafy has root %d in a %d-page file; it needs leaves past the root", root, pages)
	}
	leaf := (root + 1 + pages) / 2
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{0xA5}, int(pageSize)), (leaf-1)*pageSize); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	rows, err := d.sql.Query(`SELECT n, pad FROM leafy ORDER BY n`)
	if err != nil {
		t.Fatalf("the query failed at open (%v); this test is about damage met mid-scan", err)
	}
	got := 0
	for rows.Next() {
		got++
	}
	scanErr := rows.Err()
	rows.Close()
	if got == 0 || scanErr == nil || !strings.Contains(scanErr.Error(), "malformed") {
		t.Fatalf("read %d rows then err %v; want some rows, then a malformed-page error", got, scanErr)
	}
	fault, bad := d.StorageFault()
	if !bad || !fault.Damaged {
		t.Fatalf("the scan hit a corrupt page after %d rows and the store reports %+v (fault=%v)", got, fault, bad)
	}
}

// fillToTheBrim makes every write that needs a new page fail with SQLITE_FULL:
// max_page_count is SQLite's own "the volume is full", the same code ENOSPC
// produces. db.Open holds one connection, so the per-connection PRAGMA holds
// for every statement after it.
func fillToTheBrim(t *testing.T, d *DB) (unfill func()) {
	t.Helper()
	if _, err := d.sql.Exec(`CREATE TABLE fill_probe(b BLOB)`); err != nil {
		t.Fatal(err)
	}
	var pages int64
	if err := d.sql.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(fmt.Sprintf(`PRAGMA max_page_count = %d`, pages)); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := d.sql.Exec(`PRAGMA max_page_count = 1073741823`); err != nil {
			t.Fatal(err)
		}
	}
}

// A STATEMENT PREPARED ON A TRANSACTION is a separate path through the driver
// from tx.Exec: the chat batch insert and the destination reorders use it. A
// full disk met there was not recorded, and a transaction that wrote through
// it never counted as the write that proves the space came back.
func TestAPreparedStatementReportsAndClearsAFullDisk(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "polyemesis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	unfill := fillToTheBrim(t, d)

	tx, err := d.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO fill_probe VALUES (randomblob(1048576))`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stmt.Exec(); err == nil || !strings.Contains(err.Error(), "full") {
		t.Fatalf("the prepared insert was supposed to fail as full, got %v", err)
	}
	stmt.Close()
	_ = tx.Rollback()
	if f, bad := d.StorageFault(); !bad || f.Code != sqliteFull {
		t.Fatalf("a prepared statement failed as full and the store reports %+v (fault=%v)", f, bad)
	}

	unfill()
	tx, err = d.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err = tx.Prepare(`INSERT INTO fill_probe VALUES (?)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stmt.Exec([]byte{0}); err != nil {
		t.Fatal(err)
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if f, bad := d.StorageFault(); bad {
		t.Errorf("a transaction wrote through a prepared statement and committed, and the fault stayed: %+v", f)
	}
}

// THE COMMIT IS THE WRITE, in WAL mode: an explicit transaction's pages reach
// the file when it commits. The store's multi-row saves all run in one, so a
// successful commit of one that changed rows must end a full-disk fault.
func TestACommittedTransactionClearsAFullDisk(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "polyemesis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	unfill := fillToTheBrim(t, d)
	if _, err := d.sql.Exec(`INSERT INTO fill_probe VALUES (randomblob(1048576))`); err == nil {
		t.Fatal("the write was supposed to fail as full")
	}
	if _, bad := d.StorageFault(); !bad {
		t.Fatal("setup: the full disk was not recorded")
	}
	unfill()

	// A transaction that only reads commits too, and proves nothing.
	tx, err := d.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM fill_probe`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, bad := d.StorageFault(); !bad {
		t.Fatal("a read-only transaction's commit cleared the full-disk fault")
	}

	tx, err = d.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO fill_probe VALUES (x'00')`); err != nil {
		t.Fatal(err)
	}
	// Still in the transaction: nothing has reached the file yet.
	if _, bad := d.StorageFault(); !bad {
		t.Fatal("the fault cleared before the transaction committed")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if f, bad := d.StorageFault(); bad {
		t.Errorf("a transaction that wrote committed, and the fault stayed: %+v", f)
	}
}
