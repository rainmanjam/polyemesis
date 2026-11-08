package overlay

import (
	"io"
	"log/slog"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/chat"
)

// alertHubWithRule wires the REAL chat hub to the REAL automod engine with one
// flag-only rule matching the sample tip's message ("Great stream!"), nothing
// armed. hold says whether that rule has holdOverlay.
func alertHubWithRule(t *testing.T, hold bool) *chat.Hub {
	t.Helper()
	rules, err := automod.NewRuleSet([]automod.Rule{{
		ID: 1, Name: "great", Enabled: true, Pattern: `great stream`,
		Action: automod.ActionFlag, HoldOverlay: hold,
	}})
	if err != nil {
		t.Fatal(err)
	}
	hub := chat.New(chat.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(hub.Close)
	hub.SetModerator(automod.New(automod.DefaultMatrix(), automod.PlatformCaps{}, rules, nil, nil))
	return hub
}

// HOLD ON ACTIVITY ALERT TEXT. A rule with holdOverlay keeps the viewer's words
// off the alert, exactly as a removal does: the alert still shows (a tip is not
// withheld for what its sender said), without the message.
func TestAHeldAlertMessageIsWithheldButTheAlertShows(t *testing.T) {
	g, sub, _ := newActivityGate(t, alertHubWithRule(t, true))
	g.Offer(act(activity.KindTip))
	d, ok := nextDisplay(t, sub)
	if !ok || d.Amount == nil {
		t.Fatalf("the alert did not show: %+v ok=%v", d, ok)
	}
	if d.Message != "" {
		t.Fatalf("held alert text reached the overlay: %q", d.Message)
	}
	if st := g.Stats(); st.Unworded != 1 || st.Shown != 1 {
		t.Errorf("stats %+v", st)
	}
}

// THE CONTROL: the identical rule WITHOUT holdOverlay flags and passes, so the
// message goes out as it always did. Without this the test above could pass
// for any reason that blanks the message.
func TestTheSameRuleWithoutHoldLeavesTheAlertMessageAlone(t *testing.T) {
	g, sub, _ := newActivityGate(t, alertHubWithRule(t, false))
	g.Offer(act(activity.KindTip))
	d, ok := nextDisplay(t, sub)
	if !ok || d.Message != "Great stream!" {
		t.Fatalf("display %+v ok=%v: a flag-only rule changed what an alert shows", d, ok)
	}
}
