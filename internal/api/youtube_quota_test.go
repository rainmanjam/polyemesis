package api

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// THE OPERATOR'S ALLOWANCE REACHES THE PACER. #732.
//
// YouTube is the only platform here that bills for chat: Twitch is an IRC
// socket and Kick posts webhooks, both free and real-time, while YouTube is
// polled and every poll costs 5 units of a daily 10,000. internal/chat's pacer
// is "time-until-reset divided by calls-still-affordable", so the allowance is
// the denominator of every decision it makes.
//
// YouTubeConfig.QuotaUnits existed and its comment said "operators who have
// been granted more should say so here". Nothing could: chat_wiring built the
// adapter with three fields and this was not one of them, so an install granted
// a million units after a YouTube API Services audit was paced against ten
// thousand -- polling about a hundred times slower than it was entitled to,
// with nothing on screen saying so.
func TestTheStoredYouTubeQuotaReachesTheAdapter(t *testing.T) {
	s, _, store := testServer(t, config.Config{})

	set, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	set.Chat.YouTubeQuotaUnits = 1_000_000
	set.Chat.YouTubeQuotaReserve = 5_000
	if err := store.PutSettings(set); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}

	got := s.youtubeQuota()
	if got.YouTubeQuotaUnits != 1_000_000 {
		t.Errorf("quota units = %d, want the stored 1,000,000. The pacer would poll "+
			"as though the project still had the default allowance", got.YouTubeQuotaUnits)
	}
	if got.YouTubeQuotaReserve != 5_000 {
		t.Errorf("quota reserve = %d, want the stored 5,000", got.YouTubeQuotaReserve)
	}
}

// A FRESH INSTALL IS UNCHANGED, which is the other half of making a knob
// reachable: exposing it must not move it.
func TestAnInstallThatSaidNothingKeepsThePackageDefaults(t *testing.T) {
	s, _, _ := testServer(t, config.Config{})

	got := s.youtubeQuota()
	if got.YouTubeQuotaUnits != chat.DefaultQuotaUnits {
		t.Errorf("quota units = %d, want chat.DefaultQuotaUnits (%d)",
			got.YouTubeQuotaUnits, chat.DefaultQuotaUnits)
	}
	if got.YouTubeQuotaReserve != chat.DefaultQuotaReserve {
		t.Errorf("quota reserve = %d, want chat.DefaultQuotaReserve (%d)",
			got.YouTubeQuotaReserve, chat.DefaultQuotaReserve)
	}
}

// A ROW WRITTEN BEFORE THIS FIELD EXISTED reads back as zero, and zero is not
// an allowance. It must fall to the package default rather than be handed to
// the pacer, which would divide by it.
func TestAZeroFromAnOlderRowFallsBackRatherThanReachingThePacer(t *testing.T) {
	s, _, store := testServer(t, config.Config{})

	set, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	set.Chat.YouTubeQuotaUnits = 0
	set.Chat.YouTubeQuotaReserve = 0
	// Straight to the store, past the validator, because that is exactly how an
	// older row arrives: it was written before the column had a meaning.
	if err := store.PutSettings(set); err != nil {
		t.Logf("the validator refused a zero on the way in, which is also correct: %v", err)
	}

	if got := s.youtubeQuota().YouTubeQuotaUnits; got != chat.DefaultQuotaUnits {
		t.Errorf("quota units = %d, want the default %d. A zero handed to the pacer is "+
			"an allowance of nothing, so chat would pause before it started",
			got, chat.DefaultQuotaUnits)
	}
}

// AND THE VALIDATOR REFUSES THE VALUES THAT WOULD BREAK IT SILENTLY.
//
// The two directions are not symmetric, which is the whole reason this is
// checked rather than defaulted: too low only makes chat slow, and the operator
// can see that. Too high makes chat die mid-broadcast and stay dead until
// midnight Pacific -- the exact failure the pacer exists to prevent.
func TestTheQuotaValidatorRefusesWhatWouldBreakThePacer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		units   int
		reserve int
		ok      bool
	}{
		{"the default", 10000, 200, true},
		{"a granted million", 1_000_000, 5_000, true},
		{"a small but real project", 1, 0, true},
		{"zero is not an allowance", 0, 200, false},
		{"negative", -1, 200, false},
		{"a typo two orders past any real grant", 100_000_000, 200, false},
		{"a negative reserve", 10000, -1, false},
		{"a reserve larger than the budget leaves nothing to read with", 10000, 9000, false},
		{"a reserve of exactly half is the boundary and is allowed", 10000, 5000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := db.DefaultSettings()
			set.Chat.YouTubeQuotaUnits = tc.units
			set.Chat.YouTubeQuotaReserve = tc.reserve
			err := set.Validate()
			if tc.ok && err != nil {
				t.Errorf("units=%d reserve=%d refused: %v", tc.units, tc.reserve, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("units=%d reserve=%d accepted; the pacer would divide by it",
					tc.units, tc.reserve)
			}
		})
	}
}
