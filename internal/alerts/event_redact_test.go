package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/redact"
)

// The literal an operator would lose their channel over.
const secretKey = "live_284729384_pQ8fZmT3xR9wLkYvB2nHsA"

func TestEventRedactedMasksFieldsNamedAfterCredentials(t *testing.T) {
	ev := Event{
		Type:  TypeDestinationDown,
		Title: "Destination down: rtmp://host/live2/" + secretKey,
		Text:  "last error: stream_key=" + secretKey,
		Fields: []Field{
			{Name: "destination", Value: "Twitch"},
			{Name: "streamKey", Value: secretKey},
			{Name: "target", Value: "rtmps://live.twitch.tv/app/" + secretKey},
		},
	}.Redacted()

	if ev.Fields[0].Value != "Twitch" {
		t.Errorf("an innocent field was mangled: %q", ev.Fields[0].Value)
	}
	if ev.Fields[1].Value != redact.Mask {
		t.Errorf("field named streamKey = %q, want %q", ev.Fields[1].Value, redact.Mask)
	}
	for _, s := range []string{ev.Title, ev.Text, ev.Fields[1].Value, ev.Fields[2].Value} {
		if strings.Contains(s, secretKey) {
			t.Errorf("Redacted left the stream key in %q", s)
		}
	}
}

// A stream key must not survive the whole path from Publish to the encoded
// body, in ANY format. This is the test the feature is judged on.
func TestEncodedPayloadNeverCarriesAStreamKey(t *testing.T) {
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	ev := Event{
		Type: TypeDestinationDown, Severity: SeverityCritical, Key: "destination:1",
		Title: "Destination down: Twitch",
		Text:  "rtmps://live.twitch.tv/app/" + secretKey + ": Broken pipe",
		Fields: []Field{
			{Name: "target", Value: "rtmps://live.twitch.tv/app/" + secretKey},
			{Name: "streamKey", Value: secretKey},
			{Name: "passphrase", Value: "hunter2"},
		},
		At: now,
	}.Redacted()

	for _, format := range []Format{FormatJSON, FormatDiscord, FormatSlack} {
		t.Run(string(format), func(t *testing.T) {
			d := Delivery{
				Rule:  Rule{ID: 1, Name: "ops", URL: "https://hooks.slack.com/services/T/B/SECRETPATH", Format: format},
				Items: []Item{{Event: ev, Count: 2, First: now, Last: now}},
			}
			body, _, err := Encode(d)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			for _, bad := range []string{secretKey, "hunter2", "SECRETPATH", "hooks.slack.com"} {
				if strings.Contains(string(body), bad) {
					t.Errorf("%s payload leaked %q:\n%s", format, bad, body)
				}
			}
		})
	}
}

func TestRuleJSONNeverCarriesTheWebhookURL(t *testing.T) {
	r := Rule{ID: 7, Name: "ops", URL: "https://hooks.slack.com/services/T/B/SECRETPATH", Format: FormatSlack}
	b, err := r.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if strings.Contains(string(b), "SECRETPATH") {
		t.Errorf("Rule JSON leaked the webhook path: %s", b)
	}
	if !strings.Contains(string(b), "hooks.slack.com") {
		t.Errorf("Rule JSON should still name the host so the operator knows which rule it is: %s", b)
	}
}
