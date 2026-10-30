package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/failreason"
)

func downWithReason(r *failreason.Reason) []Event {
	w := NewWatcher(WatchConfig{DownFor: time.Second})
	d := DestState{ID: 4, Name: "YouTube", Enabled: true, Platform: "youtube", Reason: r}
	w.Observe(Snapshot{At: base, Destinations: []DestState{d}})
	return w.Observe(Snapshot{At: base.Add(2 * time.Second), Destinations: []DestState{d}})
}

// TestADestinationDownAlertSaysWhy is the alert-rule outlet. The reason goes in
// the TEXT, not only in a field, because the timeline stores Title and Text and
// drops Fields -- a reason only in a field would be in Slack and missing from
// the history page that an operator reads the morning after.
func TestADestinationDownAlertSaysWhy(t *testing.T) {
	evs := downWithReason(&failreason.Reason{
		Cause: failreason.KeyRejected, Line: "[rtmp @ 0x1] Server error: authentication failed",
		Quick: true, Held: true, Rejections: 3,
	})
	if len(evs) != 1 || evs[0].Type != TypeDestinationDown {
		t.Fatalf("events = %+v, want one destination.down", evs)
	}
	ev := evs[0]
	for _, want := range []string{"stream key rejected", "held after 3 quick rejections", "Retry"} {
		if !strings.Contains(ev.Text, want) {
			t.Errorf("text %q does not say %q", ev.Text, want)
		}
	}
	// One machine-readable name for the cause in every outlet: "cause", the
	// same name the status JSON, MQTT and the webhook use.
	if got := fieldOf(ev, "cause"); got != "key_rejected" {
		t.Errorf("cause field = %q, want key_rejected", got)
	}
	if got := fieldOf(ev, "reason"); got != "stream key rejected" {
		t.Errorf("reason field = %q", got)
	}
	if got := fieldOf(ev, "detail"); !strings.Contains(got, "authentication failed") {
		t.Errorf("detail field = %q, want FFmpeg's line", got)
	}
}

// TestARotatedKeyIsNamedInTheAlert: the one refusal with a one-click fix gets
// said, so somebody reading Slack on a phone knows what to press.
func TestARotatedKeyIsNamedInTheAlert(t *testing.T) {
	evs := downWithReason(&failreason.Reason{Cause: failreason.KeyRejected, KeyChanged: true})
	if len(evs) != 1 || !strings.Contains(evs[0].Text, "different stream key") {
		t.Fatalf("events = %+v, want the text to name the rotated key", evs)
	}
}

// TestAnUnknownCauseCarriesFFmpegsOwnWords: "unknown failure" alone would be a
// message nobody can act on; the raw line is what they search for.
func TestAnUnknownCauseCarriesFFmpegsOwnWords(t *testing.T) {
	evs := downWithReason(&failreason.Reason{Cause: failreason.Unknown, Line: "Error closing file: Broken pipe"})
	if len(evs) != 1 || !strings.Contains(evs[0].Text, "Error closing file: Broken pipe") {
		t.Fatalf("events = %+v", evs)
	}
}

// TestADownAlertWithNoReasonIsUnchanged: a destination that could not be built
// (a routing compile error) has no FFmpeg run to classify. Its message stays
// what it always was rather than growing an empty "Reason:".
func TestADownAlertWithNoReasonIsUnchanged(t *testing.T) {
	evs := downWithReason(nil)
	if len(evs) != 1 || strings.Contains(evs[0].Text, "Reason") || fieldOf(evs[0], "cause") != "" {
		t.Fatalf("events = %+v", evs)
	}
}
