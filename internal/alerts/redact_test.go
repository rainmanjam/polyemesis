package alerts

import (
	"strings"
	"testing"
	"time"
)

// The literal an operator would lose their channel over.
const secretKey = "live_284729384_pQ8fZmT3xR9wLkYvB2nHsA"

func TestRedactURLMasksTheCredentialAndKeepsTheEndpointRecognisable(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "rtmp stream key is the last path segment",
			in:   "rtmp://a.rtmp.youtube.com/live2/" + secretKey,
			want: "rtmp://a.rtmp.youtube.com/live2/" + Mask,
		},
		{
			name: "rtmps too",
			in:   "rtmps://live.twitch.tv/app/" + secretKey,
			want: "rtmps://live.twitch.tv/app/" + Mask,
		},
		{
			name: "a lone application segment carries no key and survives",
			in:   "rtmp://ingest.example/live",
			want: "rtmp://ingest.example/live",
		},
		{
			name: "srt passphrase and streamid are query parameters",
			in:   "srt://host:9000?streamid=publish/" + secretKey + "&passphrase=hunter2&latency=200",
			want: "srt://host:9000?latency=200&passphrase=" + Mask + "&streamid=" + Mask,
		},
		{
			name: "userinfo is a credential",
			in:   "http://admin:hunter2@example.test/hook",
			want: "http://" + Mask + "@example.test/hook",
		},
		{
			name: "http query token",
			in:   "https://example.test/notify?token=" + secretKey + "&room=ops",
			want: "https://example.test/notify?room=ops&token=" + Mask,
		},
		{
			name: "an http path is left alone, because it is not where a stream key lives",
			in:   "https://example.test/a/b/c",
			want: "https://example.test/a/b/c",
		},
		{
			name: "a malformed query does not stop the path being masked",
			in:   "rtmp://host/live2/" + secretKey + "?a=%zz",
			want: "rtmp://host/live2/" + Mask + "?a=%zz",
		},
		{
			name: "no scheme at all is not a URL and is masked whole",
			in:   "://nonsense",
			want: Mask,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactURL(tt.in)
			if got != tt.want {
				t.Errorf("RedactURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if strings.Contains(got, secretKey) {
				t.Errorf("RedactURL(%q) leaked the stream key: %q", tt.in, got)
			}
		})
	}
}

func TestRedactWebhookURLMasksEverythingBelowTheHost(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{
			name: "a Discord webhook carries its secret in the path",
			in:   "https://discord.com/api/webhooks/12345/abcdefSECRET",
			want: "https://discord.com/" + Mask,
		},
		{
			name: "a Slack webhook does too",
			in:   "https://hooks.slack.com/services/T000/B000/XXXXSECRET",
			want: "https://hooks.slack.com/" + Mask,
		},
		{
			name: "junk is masked whole",
			in:   "not a url",
			want: Mask,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RedactWebhookURL(tt.in); got != tt.want {
				t.Errorf("RedactWebhookURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRedactScrubsFreeText(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantAbsent []string
		wantSubstr string
	}{
		{
			name:       "an ffmpeg error quoting the target URL",
			in:         "rtmp://live.twitch.tv/app/" + secretKey + ": Connection refused",
			wantAbsent: []string{secretKey},
			wantSubstr: "Connection refused",
		},
		{
			name:       "a URL ending a sentence keeps its punctuation",
			in:         "publishing to rtmp://host/live2/" + secretKey + ".",
			wantAbsent: []string{secretKey},
			wantSubstr: "rtmp://host/live2/" + Mask + ".",
		},
		{
			name:       "a bare key=value pair",
			in:         "stream_key=" + secretKey + " retrying",
			wantAbsent: []string{secretKey},
			wantSubstr: "retrying",
		},
		{
			name:       "an authorization header echoed into a log line",
			in:         "Authorization: Bearer " + secretKey,
			wantAbsent: []string{secretKey},
		},
		{
			name:       "ordinary prose is untouched",
			in:         "the encoder reconnected after 3 attempts",
			wantSubstr: "the encoder reconnected after 3 attempts",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Redact(tt.in)
			for _, bad := range tt.wantAbsent {
				if strings.Contains(got, bad) {
					t.Errorf("Redact(%q) = %q, which still contains %q", tt.in, got, bad)
				}
			}
			if tt.wantSubstr != "" && !strings.Contains(got, tt.wantSubstr) {
				t.Errorf("Redact(%q) = %q, want it to contain %q", tt.in, got, tt.wantSubstr)
			}
		})
	}
}

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
	if ev.Fields[1].Value != Mask {
		t.Errorf("field named streamKey = %q, want %q", ev.Fields[1].Value, Mask)
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
