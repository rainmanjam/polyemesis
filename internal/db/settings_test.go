package db

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// Chat retention is an operator setting because the moderator's user card reads
// out of that table: no platform publishes a chat-history API, so how far back a
// moderation decision can see is exactly these numbers.
//
// The companion check that these defaults match internal/chat's constants lives
// in that package, not here. internal/chat imports internal/db, so importing it
// back from a `package db` test is an import cycle -- which is the difference
// between this and TestMQTTDefaultsMatchTheMQTTPackage, where internal/mqtt
// imports nothing from here.
func TestChatSettingsValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Settings)
		wantErr bool
	}{
		// Keep-forever is a real answer, not a mistake to reject. An operator
		// who wants a permanent moderation record should be able to have one,
		// and chat rows are small enough that it is not a foot-gun.
		{"zero hours is keep forever", func(s *Settings) { s.Chat.RetentionHours = 0 }, false},
		{"five years is allowed", func(s *Settings) { s.Chat.RetentionHours = MaxChatRetentionHours }, false},
		{"negative hours is refused", func(s *Settings) { s.Chat.RetentionHours = -1 }, true},
		{"a typo of 999999 hours is refused", func(s *Settings) { s.Chat.RetentionHours = 999999 }, true},
		{"zero keep is allowed", func(s *Settings) { s.Chat.KeepMessages = 0 }, false},
		{"negative keep is refused", func(s *Settings) { s.Chat.KeepMessages = -1 }, true},
		// Zero here would sweep on every tick, which is far more likely a slip
		// than a wish, so it is refused rather than silently defaulted.
		{"zero purge interval is refused", func(s *Settings) { s.Chat.PurgeMinutes = 0 }, true},
		{"a daily sweep is allowed", func(s *Settings) { s.Chat.PurgeMinutes = MaxChatPurgeMinutes }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := DefaultSettings()
			tc.mutate(&s)
			err := s.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("accepted, want a validation error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("refused: %v", err)
			}
		})
	}
}

// A fresh install must behave exactly as the hard-coded Hub did, so upgrading
// changes nothing until somebody decides to change it.
func TestChatDefaultsAreTheOldHardCodedOnes(t *testing.T) {
	c := DefaultSettings().Chat
	if c.RetentionHours != 2 || c.KeepMessages != 2000 || c.PurgeMinutes != 5 {
		t.Fatalf("defaults = %+v, want the 2h/2000/5m the Hub used as constants", c)
	}
}

func TestAPlaylistItemMustNameAKnownUpload(t *testing.T) {
	// Items reference uploads, never paths. That is the security boundary
	// this whole design rests on: the concat demuxer's -safe 0 is only
	// defensible because every path it sees was chosen by this process, and
	// uploads.SafeName is what guarantees that.
	s := DefaultSettings()
	s.Failover.Playlist.Enabled = true
	s.Failover.Playlist.Items = []PlaylistItem{{Upload: ""}}
	if err := s.Validate(); err == nil {
		t.Error("an item naming no upload was accepted")
	}
}

func TestAPlaylistItemRejectsAnythingPathShaped(t *testing.T) {
	for _, bad := range []string{"../escape.mp4", "/etc/passwd", "sub/dir.mp4"} {
		s := DefaultSettings()
		s.Failover.Playlist.Enabled = true
		s.Failover.Playlist.Items = []PlaylistItem{{Upload: bad}}
		if err := s.Validate(); err == nil {
			t.Errorf("item %q was accepted; an upload name is a bare filename, "+
				"and anything path-shaped means the caller is trying to reach "+
				"outside the uploads directory", bad)
		}
	}
}

func TestAnEnabledPlaylistNeedsAtLeastOneItem(t *testing.T) {
	s := DefaultSettings()
	s.Failover.Playlist.Enabled = true
	s.Failover.Playlist.Items = nil
	if err := s.Validate(); err == nil {
		t.Error("an enabled playlist with no items was accepted; it would " +
			"offer a candidate that can never deliver")
	}
}

// TestALegacyPlaylistFilePathMigratesToASingleItem guards the load-time
// migration for the one case it can honestly preserve: a bare name that
// ALREADY exists as a real upload. A deployment that set FilePath under
// sub-project A has that value sitting in its stored settings blob;
// PlaylistSettings no longer has a field for json.Unmarshal to land it in, so
// without this migration the value is silently dropped on the very next load
// -- Items stays empty, the playlist never starts, and an operator discovers
// it, if at all, during the outage the playlist exists to cover.
func TestALegacyPlaylistFilePathMigratesToASingleItem(t *testing.T) {
	d := testDB(t)
	dataDir := t.TempDir()
	d.WithDataDir(dataDir)

	// The file must actually exist under <dataDir>/uploads for the migration
	// to trust the name -- see migratePlaylistFilePath's comment for why a
	// bare name alone is not enough: FilePath and Upload are different
	// namespaces, and this is the one case where they happen to agree.
	uploadsDir := filepath.Join(dataDir, uploads.Dir)
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		t.Fatalf("mkdir uploads dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(uploadsDir, "loop.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed upload file: %v", err)
	}

	legacy := `{"failover":{"playlist":{"enabled":true,"filePath":"loop.mp4"}}}`
	if _, err := d.SQL().Exec(`INSERT INTO settings (id, json) VALUES (1, ?)`, legacy); err != nil {
		t.Fatalf("seed legacy settings: %v", err)
	}

	got, err := d.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}

	want := []PlaylistItem{{Upload: "loop.mp4"}}
	if !reflect.DeepEqual(got.Failover.Playlist.Items, want) {
		t.Fatalf("Items = %+v, want %+v -- a legacy FilePath naming a real "+
			"upload must survive as a single-item list, or an operator's "+
			"configured filler vanishes on upgrade with nothing saying why",
			got.Failover.Playlist.Items, want)
	}
}

// TestAnUnmigratableLegacyPlaylistFilePathIsNotSilentlyKept is the other half
// of the migration: FilePath and Upload are different namespaces (a
// data-dir-relative path vs. a bare name inside uploads/), so a value that
// cannot honestly be re-expressed as an Upload must not be smuggled across as
// one. A silently-wrong item -- pointing at a file that is not the operator's
// filler, or failing validation on the very next settings save -- is worse
// than an empty list, because an empty list at least fails the way
// TestAnEnabledPlaylistNeedsAtLeastOneItem already covers, visibly.
func TestAnUnmigratableLegacyPlaylistFilePathIsNotSilentlyKept(t *testing.T) {
	tests := []struct {
		name   string
		legacy string
	}{
		// A separator means the legacy value cannot even be an Upload's shape,
		// let alone name a real one.
		{"path-shaped", "media/loop.mp4"},
		// Bare, but nothing by that name has ever been uploaded -- the data
		// directory here is empty, so this must not be assumed to resolve.
		{"bare but no matching upload exists", "loop.mp4"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := testDB(t)
			d.WithDataDir(t.TempDir())

			legacy := `{"failover":{"playlist":{"enabled":true,"filePath":` +
				`"` + tc.legacy + `"}}}`
			if _, err := d.SQL().Exec(`INSERT INTO settings (id, json) VALUES (1, ?)`, legacy); err != nil {
				t.Fatalf("seed legacy settings: %v", err)
			}

			got, err := d.GetSettings()
			if err != nil {
				t.Fatalf("GetSettings: %v", err)
			}

			if len(got.Failover.Playlist.Items) != 0 {
				t.Errorf("legacy filePath %q was migrated to %+v; it cannot be "+
					"honestly represented as an upload, so it must be left "+
					"unmigrated (and reported, not silently dropped) rather than "+
					"becoming a playlist item that points at the wrong file or "+
					"fails validation on the next save",
					tc.legacy, got.Failover.Playlist.Items)
			}
		})
	}
}
