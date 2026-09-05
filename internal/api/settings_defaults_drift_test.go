package api

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// These two guards live here rather than in internal/db, and the reason is the
// import graph rather than taste: chat and alerts both depend on db, so db
// cannot import either to check itself. internal/api is the nearest package
// that already sees all three.
//
// WHAT THEY GUARD
//
// A package default and a settings default are the same number written twice.
// When they drift the failure is quiet and delayed: a fresh install runs on the
// PACKAGE default until somebody opens the settings form and saves anything at
// all, at which point every value in the tree is written and the install jumps
// to the SETTINGS default. The operator changed a chat retention field and the
// scrollback depth moved, or changed nothing and the alert retry budget moved.
// Nothing errors, and the change is attributed to whatever they happened to
// touch.
//
// db.ChatSettings' own comment already claimed a guard by this name kept the
// two in step. It did not exist -- in any package, under any name -- so the
// claim was load-bearing documentation for a check nobody had written. Making a
// setting reachable is the moment that stops being free, because now there are
// two ways to end up with a value.

func TestChatDefaultsMatchTheChatPackage(t *testing.T) {
	got := db.DefaultSettings().Chat

	if got.HistoryMessages != chat.DefaultHistory {
		t.Errorf("db default chat.historyMessages = %d, chat.DefaultHistory = %d -- "+
			"a fresh install would run on one and switch to the other the first time "+
			"anything at all was saved",
			got.HistoryMessages, chat.DefaultHistory)
	}
}

// The YouTube quota defaults are the pacer's own, for the same reason as the
// two above: making a knob reachable is not an occasion to move it. #732.
//
// This one matters more than the parity itself. The allowance is the
// denominator of every pacing decision internal/chat makes, so a default here
// that disagreed with the package would change how fast an install polls the
// first time anybody saved the settings form -- for a field they never touched,
// on the one platform that bills per poll.
func TestYouTubeQuotaDefaultsMatchTheChatPackage(t *testing.T) {
	got := db.DefaultSettings().Chat

	if got.YouTubeQuotaUnits != chat.DefaultQuotaUnits {
		t.Errorf("db default chat.youtubeQuotaUnits = %d, chat.DefaultQuotaUnits = %d",
			got.YouTubeQuotaUnits, chat.DefaultQuotaUnits)
	}
	if got.YouTubeQuotaReserve != chat.DefaultQuotaReserve {
		t.Errorf("db default chat.youtubeQuotaReserve = %d, chat.DefaultQuotaReserve = %d -- "+
			"the reserve is what keeps sending possible after reading has spent the day, "+
			"so a disagreement here is four messages an operator thought they had",
			got.YouTubeQuotaReserve, chat.DefaultQuotaReserve)
	}
}

func TestAlertDefaultsMatchTheAlertsPackage(t *testing.T) {
	got := db.DefaultSettings().Alerts

	if got.RetryAttempts != alerts.DefaultAttempts {
		t.Errorf("db default alerts.retryAttempts = %d, alerts.DefaultAttempts = %d -- "+
			"the budget an install runs on would change the first time the settings "+
			"form was saved, for a field the operator may not have touched",
			got.RetryAttempts, alerts.DefaultAttempts)
	}
}

// The defaults must also be inside the bounds the same file declares. Obvious
// enough to be worth one assertion: a default outside its own validator is an
// install that cannot save its settings back unchanged, and the error names a
// field nobody edited.
func TestNewSettingsDefaultsSatisfyTheirOwnValidators(t *testing.T) {
	s := db.DefaultSettings()
	if err := s.Validate(); err != nil {
		t.Fatalf("DefaultSettings does not satisfy Validate: %v", err)
	}

	if s.Chat.HistoryMessages < db.MinChatHistoryMessages ||
		s.Chat.HistoryMessages > db.MaxChatHistoryMessages {
		t.Errorf("default chat history %d is outside its own bounds (%d-%d)",
			s.Chat.HistoryMessages, db.MinChatHistoryMessages, db.MaxChatHistoryMessages)
	}
	if s.Alerts.RetryAttempts < db.MinAlertRetryAttempts ||
		s.Alerts.RetryAttempts > db.MaxAlertRetryAttempts {
		t.Errorf("default alert retry %d is outside its own bounds (%d-%d)",
			s.Alerts.RetryAttempts, db.MinAlertRetryAttempts, db.MaxAlertRetryAttempts)
	}
}
