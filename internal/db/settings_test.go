package db

import (
	"testing"
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

// TestAPlaylistItemRejectsADotUpload covers what
// TestAPlaylistItemRejectsAnythingPathShaped's cases cannot: "." has no
// separator and is too short for the ".." or drive-letter checks, so it slid
// past the shape check on its own. uploads.Store.Resolve already refuses it
// (same as "" and ".."), which stops it from escaping anywhere -- but without
// this, Validate() would accept a playlist item that can only ever fail to
// resolve, an enabled candidate that never starts, exactly the failure an
// enabled-with-zero-items playlist is refused for.
func TestAPlaylistItemRejectsADotUpload(t *testing.T) {
	s := DefaultSettings()
	s.Failover.Playlist.Enabled = true
	s.Failover.Playlist.Items = []PlaylistItem{{Upload: "."}}
	if err := s.Validate(); err == nil {
		t.Error(`item "." was accepted; it cannot resolve to any upload, so an ` +
			"enabled playlist naming it can only ever fail to start")
	}
}

// TestAPlaylistIsBoundedInLength is the ceiling on how much work one settings
// document can commit the engine to.
//
// The list is not free to hold. engine.playlistItemsReady stats every item
// twice on every reconcile while selMu is held -- the lock an operator's
// failover POST queues behind -- the whole document is a single JSON row read
// on most API requests, and in sub-project B2 the list becomes a concat file
// FFmpeg has to parse. Without a bound the only limit is what a client can POST.
//
// The mutation: delete the len(p.Items) > MaxPlaylistItems check in
// PlaylistFileProblem and this passes.
func TestAPlaylistIsBoundedInLength(t *testing.T) {
	s := DefaultSettings()
	s.Failover.Playlist.Enabled = true
	s.Failover.Playlist.Items = make([]PlaylistItem, MaxPlaylistItems+1)
	for i := range s.Failover.Playlist.Items {
		s.Failover.Playlist.Items[i] = PlaylistItem{Upload: "loop.mp4"}
	}
	if err := s.Validate(); err == nil {
		t.Errorf("a playlist of %d items was accepted; every reconcile walks the whole "+
			"list under selMu", MaxPlaylistItems+1)
	}

	// And the bound is a bound, not a refusal: exactly the maximum is fine, or
	// the constant would be off by one against its own name.
	s.Failover.Playlist.Items = s.Failover.Playlist.Items[:MaxPlaylistItems]
	if err := s.Validate(); err != nil {
		t.Errorf("a playlist of exactly %d items was refused: %v", MaxPlaylistItems, err)
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

// TestLegacyPlaylistFilePathReportsAPreItemsValue guards the pure half of the
// migration this package still owns: recovering a FilePath from a blob that
// predates Items, without acting on it. A deployment that set FilePath under
// sub-project A has that value sitting in its stored settings blob;
// PlaylistSettings no longer has a field for json.Unmarshal to land it in, so
// without LegacyPlaylistFilePath the value is unrecoverable and an operator's
// configured filler is gone with nothing saying why. Deciding whether it can
// be trusted, and persisting it, is cmd/polyemesis's job at startup -- not
// GetSettings's, which this test also pins: GetSettings must NOT act on it,
// because that is exactly the per-request-I/O migration this design moved
// out of the package (it used to block roughly twenty callers, several of
// them per-request API handlers, on a filesystem resolve and a Stat).
func TestLegacyPlaylistFilePathReportsAPreItemsValue(t *testing.T) {
	d := testDB(t)
	legacy := `{"failover":{"playlist":{"enabled":true,"filePath":"loop.mp4"}}}`
	if _, err := d.SQL().Exec(`INSERT INTO settings (id, json) VALUES (1, ?)`, legacy); err != nil {
		t.Fatalf("seed legacy settings: %v", err)
	}

	got, err := d.LegacyPlaylistFilePath()
	if err != nil {
		t.Fatalf("LegacyPlaylistFilePath: %v", err)
	}
	if got != "loop.mp4" {
		t.Errorf("LegacyPlaylistFilePath() = %q, want %q", got, "loop.mp4")
	}

	s, err := d.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if len(s.Failover.Playlist.Items) != 0 {
		t.Errorf("GetSettings() migrated a legacy filePath on its own (Items = %+v); "+
			"that decision belongs to cmd/polyemesis's one-shot startup migration",
			s.Failover.Playlist.Items)
	}
}

// TestLegacyPlaylistFilePathIsGoneOnceItemsIsSet is what makes the startup
// migration naturally idempotent without any extra bookkeeping: PutSettings
// marshals from the current Settings struct, which has no FilePath field
// left to write, so once Items is non-empty the stored blob no longer
// carries the legacy key at all and the next read reports "".
func TestLegacyPlaylistFilePathIsGoneOnceItemsIsSet(t *testing.T) {
	d := testDB(t)
	s := DefaultSettings()
	s.Failover.Playlist.Items = []PlaylistItem{{Upload: "loop.mp4"}}
	if err := d.PutSettings(s); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}

	got, err := d.LegacyPlaylistFilePath()
	if err != nil {
		t.Fatalf("LegacyPlaylistFilePath: %v", err)
	}
	if got != "" {
		t.Errorf(`LegacyPlaylistFilePath() = %q after Items was set, want "" -- `+
			"once the migration persists, the legacy key must not still read as "+
			"present, or the startup migration would redo the same work forever",
			got)
	}
}
