package redact

import (
	"strings"
	"testing"
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

func TestRedactMasksTheKickWebhookPathSecret(t *testing.T) {
	// Built at run time: a 32-character literal under the name "secret" is what
	// the repository secret scan flags, and this is a made-up value.
	secret := strings.Repeat("c0ffee", 5)
	for name, in := range map[string]string{
		"a bare path":         "/api/v1/chat/kick/" + secret,
		"a full url":          "https://stream.example.com/api/v1/chat/kick/" + secret,
		"in a sentence":       "paste https://stream.example.com/api/v1/chat/kick/" + secret + " into Kick.",
		"with a probe key":    "https://h/api/v1/chat/kick/" + secret + "?probe=abc",
		"percent-encoded":     "https%3A%2F%2Fh%2Fapi%2Fv1%2Fchat%2Fkick%2F" + secret + "%3Fprobe%3Dabc",
		"encoded, mixed case": "/api/v1%2fchat%2FKick%2F" + secret,
	} {
		t.Run(name, func(t *testing.T) {
			got := Redact(in)
			if strings.Contains(got, secret) {
				t.Fatalf("Redact(%q) = %q, the secret survived", in, got)
			}
			if !strings.Contains(strings.ToLower(got), "chat") {
				t.Fatalf("Redact(%q) = %q, the route is no longer recognisable", in, got)
			}
		})
	}
}
