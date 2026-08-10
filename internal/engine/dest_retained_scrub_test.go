package engine

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// TestScrubDestinationTextCoversTheRetainedTopic is the #160 half that has no
// principal and no expiry.
//
// cmd/polyemesis/mqtt.go publishes DestState.Error to a RETAINED topic. Retained
// is the whole severity argument: the broker keeps the last message and hands it
// to every future subscriber, so a credential that reaches it cannot be undone
// by rotating anything -- the old value is already sitting on somebody else's
// broker, and it will be delivered again to the next client that connects.
//
// SourceState.IngestError on the same tree was already covered, by
// supervisor.(*Process).scrub at the point the status is built. DestState.Error
// came from the engine and had NOTHING. That asymmetry is the gap, and it is a
// gap rather than a leak-in-practice: the strings that reach this field are
// compile diagnostics. Zero coverage on a permanent sink is still worth one
// function.
func TestScrubDestinationTextCoversTheRetainedTopic(t *testing.T) {
	e, _ := storeEngine(t)

	// Named `sentinel`, not `key`: see the note in
	// internal/api/ws_policy_array_test.go. gitleaks matches on the identifier,
	// so `const key = "<high-entropy string>"` is a finding, and a finding in
	// the working tree disables the allowlist self-test entirely.
	const sentinel = "SENTINEL-dest-retained-3b9f11ac77"
	row := &db.Destination{
		ID: 42, Name: "twitch", Kind: db.DestRTMP,
		URL: "rtmp://live.example/app", StreamKey: sentinel,
	}
	e.mu.Lock()
	e.dests[row.ID] = &destination{row: row}
	e.mu.Unlock()

	// The shapes the exact pass exists for: the credential GLUED to whatever
	// surrounded it, which is what defeats a grammar and what a literal
	// replacement does not notice.
	for _, in := range []string{
		"cannot start: rtmp://live.example/app/" + sentinel,
		"-rtmp_conn S:" + sentinel,
		"-passphrase " + sentinel,
		"bare " + sentinel + " in the middle",
	} {
		got := e.ScrubDestinationText(row.ID, in)
		if strings.Contains(got, sentinel) {
			t.Errorf("ScrubDestinationText(%q) = %q, which still carries the stream key. "+
				"This string is published to a RETAINED MQTT topic and cannot be recalled.",
				in, got)
		}
		if !strings.Contains(got, alerts.Mask) {
			t.Errorf("ScrubDestinationText(%q) = %q -- the key is gone but nothing says a "+
				"redaction happened, which usually means the text was destroyed rather "+
				"than masked", in, got)
		}
	}

	// The diagnostic around the credential survives. Masking the message along
	// with the key would take away the only thing this topic exists for on a
	// headless box.
	if got := e.ScrubDestinationText(row.ID, "cannot start: rtmp://live.example/app/"+sentinel); !strings.Contains(got, "cannot start") {
		t.Errorf("the diagnostic was lost: %q", got)
	}

	// Empty stays empty. omitempty drops the field, and a bare "[redacted]"
	// against a destination with no error would read as a fault.
	if got := e.ScrubDestinationText(row.ID, ""); got != "" {
		t.Errorf("ScrubDestinationText(\"\") = %q, want \"\"", got)
	}

	// An id with no RUNNING destination gets the residual only, and must not
	// panic or mask the message.
	if got := e.ScrubDestinationText(9999, "rendition 3 is no longer available"); got != "rendition 3 is no longer available" {
		t.Errorf("a compile diagnostic for a destination that is not running came back "+
			"as %q; nothing in routing.Compile's output is a credential", got)
	}
}
