package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func openBootStore(t *testing.T) *db.DB {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "polyemesis.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// settingsOf is the boot read for a test that hands the result to a function
// under test.
func settingsOf(t *testing.T, store *db.DB) db.Settings {
	t.Helper()
	s, err := bootSettings(store)
	if err != nil {
		t.Fatalf("bootSettings: %v", err)
	}
	return s
}

// One policy for a settings row that cannot be read: the process refuses to
// start. Applying built-in defaults instead would shorten chat retention, loosen
// nothing it should keep tight, and quietly forget the operator's automod and
// alert choices -- and it would happen before anything said so.
func TestAnUnreadableSettingsRowStopsTheBoot(t *testing.T) {
	store := openBootStore(t)
	// A fresh store has no row until the first read writes the defaults.
	if _, err := store.GetSettings(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQL().Exec(`UPDATE settings SET json = '{not json' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}

	_, err := bootSettings(store)
	if err == nil {
		t.Fatal("an unreadable settings row did not stop the boot")
	}
	if !strings.Contains(err.Error(), "settings") {
		t.Errorf("the error does not say what failed: %v", err)
	}
}

func TestAReadableSettingsRowBoots(t *testing.T) {
	store := openBootStore(t)
	if _, err := bootSettings(store); err != nil {
		t.Fatalf("a fresh store refused to boot: %v", err)
	}
}
