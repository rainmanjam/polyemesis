package db

import "testing"

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
