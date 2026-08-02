package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

func testStore(t *testing.T) *db.DB {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "polyemesis.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func testLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func seedLegacyPlaylist(t *testing.T, store *db.DB, filePath string) {
	t.Helper()
	blob := `{"failover":{"playlist":{"enabled":true,"filePath":"` + filePath + `"}}}`
	if _, err := store.SQL().Exec(`INSERT INTO settings (id, json) VALUES (1, ?)`, blob); err != nil {
		t.Fatalf("seed legacy settings: %v", err)
	}
}

// TestMigrateLegacyPlaylistFilePathMigratesABareExistingUpload is the one
// case migrateLegacyPlaylistFilePath can honestly preserve: a bare name that
// already exists as a real file under the uploads directory. Without this,
// an operator who configured FilePath under sub-project A would have it
// silently vanish on upgrade -- Items stays empty, the playlist never
// starts, and nothing says why.
func TestMigrateLegacyPlaylistFilePathMigratesABareExistingUpload(t *testing.T) {
	store := testStore(t)
	dataDir := t.TempDir()
	log, _ := testLogger()

	uploadsDir := filepath.Join(dataDir, uploads.Dir)
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		t.Fatalf("mkdir uploads dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(uploadsDir, "loop.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed upload file: %v", err)
	}
	seedLegacyPlaylist(t, store, "loop.mp4")

	if err := migrateLegacyPlaylistFilePath(store, dataDir, log); err != nil {
		t.Fatalf("migrateLegacyPlaylistFilePath: %v", err)
	}

	got, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	want := []db.PlaylistItem{{Upload: "loop.mp4"}}
	if len(got.Failover.Playlist.Items) != 1 || got.Failover.Playlist.Items[0] != want[0] {
		t.Fatalf("Items = %+v, want %+v -- a legacy FilePath naming a real "+
			"upload must survive the migration and be PERSISTED, or the next "+
			"read starts from nothing again", got.Failover.Playlist.Items, want)
	}
}

// TestMigrateLegacyPlaylistFilePathRefusesWhatItCannotHonestlyMigrate is the
// other half: FilePath and Upload are different namespaces (a
// data-dir-relative path vs. a bare name inside uploads/), so a value that
// cannot honestly be re-expressed as an Upload must not be smuggled across
// as one -- and it must be reported, not silently dropped.
func TestMigrateLegacyPlaylistFilePathRefusesWhatItCannotHonestlyMigrate(t *testing.T) {
	tests := []struct {
		name   string
		legacy string
	}{
		// A separator means the legacy value cannot even be an Upload's
		// shape, let alone name a real one.
		{"path-shaped", "media/loop.mp4"},
		// Bare, but nothing by that name has ever been uploaded -- the
		// uploads directory here stays empty.
		{"bare but no matching upload exists", "loop.mp4"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := testStore(t)
			dataDir := t.TempDir()
			log, buf := testLogger()

			seedLegacyPlaylist(t, store, tc.legacy)

			if err := migrateLegacyPlaylistFilePath(store, dataDir, log); err != nil {
				t.Fatalf("migrateLegacyPlaylistFilePath: %v", err)
			}

			got, err := store.GetSettings()
			if err != nil {
				t.Fatalf("GetSettings: %v", err)
			}
			if len(got.Failover.Playlist.Items) != 0 {
				t.Errorf("legacy filePath %q was migrated to %+v; it cannot be "+
					"honestly represented as an upload, so it must be left "+
					"unmigrated rather than becoming a playlist item that points "+
					"at the wrong file or fails validation on the next save",
					tc.legacy, got.Failover.Playlist.Items)
			}
			if !strings.Contains(buf.String(), "left unmigrated") {
				t.Errorf("no WARN logged for an unmigratable legacy filePath %q; "+
					"dropping it loudly is the whole point -- silently is what this "+
					"migration exists to avoid. log:\n%s", tc.legacy, buf.String())
			}
		})
	}
}

// TestMigrateLegacyPlaylistFilePathRefusesAValueTheValidatorWouldReject closes
// the gap between "names a real upload" and "is a legal playlist item".
//
// Resolve and the stat answer the first question; db.Settings.Validate answers
// the second, and they are not the same question. "C:loop.mp4" has no
// separator, so uploads.Store.Resolve is happy with it, and on a POSIX
// filesystem it is a perfectly ordinary filename that can really exist -- but
// PlaylistFileProblem refuses it, because on Windows that is a drive-relative
// path and an item is a bare name everywhere or nowhere. Migrated without the
// validator's opinion, it would be written into settings and then make the
// operator's NEXT save fail on a value they never typed.
//
// The mutation: delete the migrated.PlaylistFileProblem() check in
// migrateLegacyPlaylistFilePath and this fails, with the bad value persisted.
func TestMigrateLegacyPlaylistFilePathRefusesAValueTheValidatorWouldReject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the value under test cannot be created as a file on Windows, " +
			"which is the very reason the validator refuses it")
	}
	const legacy = "C:loop.mp4"

	store := testStore(t)
	dataDir := t.TempDir()
	log, buf := testLogger()

	uploadsDir := filepath.Join(dataDir, uploads.Dir)
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}
	// It really is on disk, so resolve and stat both succeed and only the
	// validator can be the reason this is refused.
	if err := os.WriteFile(filepath.Join(uploadsDir, legacy), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed upload: %v", err)
	}
	seedLegacyPlaylist(t, store, legacy)

	if err := migrateLegacyPlaylistFilePath(store, dataDir, log); err != nil {
		t.Fatalf("migrateLegacyPlaylistFilePath: %v", err)
	}

	got, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if len(got.Failover.Playlist.Items) != 0 {
		t.Errorf("legacy filePath %q was migrated to %+v even though the settings "+
			"validator refuses it; the operator's next save would fail on a value "+
			"this migration wrote", legacy, got.Failover.Playlist.Items)
	}
	if !strings.Contains(buf.String(), "left unmigrated") {
		t.Errorf("no WARN logged; dropping a value silently is what this migration "+
			"exists to avoid. log:\n%s", buf.String())
	}
}

// TestMigrateLegacyPlaylistFilePathRunsAtMostOnce demonstrates the "once" in
// migrateLegacyPlaylistFilePath's comment mechanically, not just by
// assertion: PutSettings persists Items, which means the raw blob's
// "filePath" key is gone on the very next read (there is no field left on
// PlaylistSettings for json.Marshal to write it from), so
// db.LegacyPlaylistFilePath reports "" and a second call is a true no-op --
// no second resolve, no second Stat, no second WARN.
func TestMigrateLegacyPlaylistFilePathRunsAtMostOnce(t *testing.T) {
	store := testStore(t)
	dataDir := t.TempDir()
	log, buf := testLogger()

	uploadsDir := filepath.Join(dataDir, uploads.Dir)
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		t.Fatalf("mkdir uploads dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(uploadsDir, "loop.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed upload file: %v", err)
	}
	seedLegacyPlaylist(t, store, "loop.mp4")

	if err := migrateLegacyPlaylistFilePath(store, dataDir, log); err != nil {
		t.Fatalf("first migrateLegacyPlaylistFilePath: %v", err)
	}
	firstLog := buf.String()
	if !strings.Contains(firstLog, "migrated legacy filePath") {
		t.Fatalf("first run did not log a migration; log:\n%s", firstLog)
	}

	// The pure signal this is durable, not per-call: the raw blob PutSettings
	// wrote in the first call no longer carries a "filePath" key at all, so
	// there is nothing left for a second call to find.
	legacy, err := store.LegacyPlaylistFilePath()
	if err != nil {
		t.Fatalf("LegacyPlaylistFilePath after first migration: %v", err)
	}
	if legacy != "" {
		t.Fatalf("LegacyPlaylistFilePath() = %q after the migration persisted, "+
			"want \"\" -- otherwise the migration would redo the same resolve, "+
			"Stat and warning on every restart forever", legacy)
	}

	// Run it again, exactly as a second process start would, and confirm
	// nothing new happened: no second migration line, and the same Items
	// that were already there.
	if err := migrateLegacyPlaylistFilePath(store, dataDir, log); err != nil {
		t.Fatalf("second migrateLegacyPlaylistFilePath: %v", err)
	}
	secondLog := buf.String()
	if strings.Count(secondLog, "migrated legacy filePath") != 1 {
		t.Fatalf("expected exactly one migration line across two runs, log:\n%s", secondLog)
	}

	got, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	want := []db.PlaylistItem{{Upload: "loop.mp4"}}
	if len(got.Failover.Playlist.Items) != 1 || got.Failover.Playlist.Items[0] != want[0] {
		t.Fatalf("Items = %+v after a second run, want unchanged %+v",
			got.Failover.Playlist.Items, want)
	}
}
