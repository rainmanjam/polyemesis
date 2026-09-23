package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
)

func databaseCheck(t *testing.T, body healthBody) (ok bool, detail string) {
	t.Helper()
	for _, c := range body.Checks {
		if c.Name == "database" {
			return c.OK, c.Detail
		}
	}
	t.Fatalf("no database check in %+v", body)
	return false, ""
}

// A FULL DISK IS A DATABASE THAT CANNOT BE WRITTEN, and health used to call it
// ok. The database check was a read of page one, which a full volume serves
// perfectly well, so with every save answering "database or disk is full"
// /health still said {"status":"ok"} (exploratory CH-03). It now reports the
// failure the writes are actually getting, and clears when a write succeeds.
//
// max_page_count is SQLite's own way to make the file "full" without filling a
// disk: a write that needs a new page fails with SQLITE_FULL, the same code
// ENOSPC produces.
func TestHealthReportsADatabaseThatCannotBeWritten(t *testing.T) {
	_, h, store := testServer(t, config.Config{})
	sqldb := store.SQL()

	var pages int
	if err := sqldb.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := sqldb.Exec(`CREATE TABLE fill_probe(b BLOB)`); err != nil {
		t.Fatal(err)
	}
	if err := sqldb.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := sqldb.Exec(`PRAGMA max_page_count = ` + itoa(int64(pages))); err != nil {
		t.Fatal(err)
	}
	_, err := sqldb.Exec(`INSERT INTO fill_probe VALUES (randomblob(1048576))`)
	if err == nil || !strings.Contains(err.Error(), "full") {
		t.Fatalf("the write was supposed to fail as full, got %v", err)
	}

	code, raw, body := getHealth(t, h)
	if body.Status == "ok" {
		t.Fatalf("health says %s while every write fails as full", raw)
	}
	ok, detail := databaseCheck(t, body)
	if ok {
		t.Fatalf("health says the database is ok while writes fail as full: %d %s", code, raw)
	}
	if !strings.Contains(detail, "full") {
		t.Errorf("detail = %q, want it to say why the writes fail", detail)
	}
	// Degraded, not unhealthy: a restart cannot add disk, and an orchestrator
	// restarting a live programme over it would end the broadcast -- and the
	// restart would itself fail, because opening the database writes.
	if code != http.StatusOK || body.Status != "degraded" {
		t.Errorf("got %d %q, want 200 degraded", code, body.Status)
	}

	// Room again, and a write goes through: the failure is over.
	if _, err := sqldb.Exec(`PRAGMA max_page_count = 1073741823`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqldb.Exec(`INSERT INTO fill_probe VALUES (x'00')`); err != nil {
		t.Fatal(err)
	}
	if code, raw, _ := getHealth(t, h); code != http.StatusOK || raw != `{"status":"ok"}` {
		t.Errorf("after a successful write health = %d %s, want the plain ok", code, raw)
	}
}
