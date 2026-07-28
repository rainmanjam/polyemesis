package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

func TestKickBroadcasterIDRejectsARefThatIsNotAnID(t *testing.T) {
	tests := []struct {
		name string
		ref  string
		want int
		ok   bool
	}{
		{"a numeric ref is the broadcaster id", "12345", 12345, true},
		{"surrounding space is not the operator's problem", "  99 ", 99, true},
		{"a slug means the row predates the Kick provider", "somechannel", 0, false},
		{"an empty ref is not an id", "", 0, false},
		{"zero is not a broadcaster", "0", 0, false},
		{"a negative id is not a broadcaster", "-4", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := kickBroadcasterID(tc.ref)
			if tc.ok && err != nil {
				t.Fatalf("kickBroadcasterID(%q) errored: %v", tc.ref, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("kickBroadcasterID(%q) = %d, want an error", tc.ref, got)
			}
			if tc.ok && got != tc.want {
				t.Fatalf("kickBroadcasterID(%q) = %d, want %d", tc.ref, got, tc.want)
			}
		})
	}
}

func TestPublicBaseURLIsEmptyRatherThanGuessed(t *testing.T) {
	// Empty is the honest answer whenever this server cannot know the URL a
	// browser reaches it on. A guess here ends up pasted into a Kick app
	// dashboard, where it fails as silence.
	tests := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{
			name: "a public name with TLS is knowable",
			cfg:  config.Config{TLS: config.TLS{Mode: config.ModeSelfSigned, Hostname: "stream.example.com"}},
			want: "https://stream.example.com",
		},
		{
			name: "no hostname at all",
			cfg:  config.Config{TLS: config.TLS{Mode: config.ModeSelfSigned}},
			want: "",
		},
		{
			name: "a LAN name is not reachable from Kick",
			cfg:  config.Config{TLS: config.TLS{Mode: config.ModeSelfSigned, Hostname: "polyemesis.local"}},
			want: "",
		},
		{
			name: "plain HTTP is not somewhere a webhook can post",
			cfg:  config.Config{TLS: config.TLS{Mode: config.ModeOff, Hostname: "stream.example.com"}},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := publicBaseURL(tc.cfg); got != tc.want {
				t.Fatalf("publicBaseURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestKickCallbackSecretSurvivesARestart(t *testing.T) {
	s, _, _ := testServer(t, config.Config{})

	first := s.kickCallbackSecret()
	if first == "" {
		t.Fatal("no callback secret derived; the webhook URL would be unmountable")
	}
	// The whole point of deriving rather than generating: a second call — which
	// is what a restart amounts to — must produce the URL the operator already
	// pasted into their Kick app.
	if second := s.kickCallbackSecret(); second != first {
		t.Fatalf("callback secret changed between calls (%q then %q); a restart would break the operator's webhook", first, second)
	}
	if strings.Contains(s.KickChatCallbackURL(), first) && publicBaseURL(s.cfg) == "" {
		t.Fatal("a callback URL was offered even though the server does not know its own public URL")
	}
}

func TestKickChatWebhookRefusesAWrongPathSecret(t *testing.T) {
	s, h, _ := testServer(t, config.Config{})
	secret := s.kickCallbackSecret()

	tests := []struct {
		name string
		path string
		want int
	}{
		// 404 rather than 401 on purpose: a prober must not learn that the
		// route exists but their secret is wrong.
		{"a guessed secret is not a route", "/api/v1/chat/kick/not-the-secret", http.StatusNotFound},
		// 200 with no Hub: Kick retries a non-2xx, and a server with chat off
		// would collect a retry storm for events it will never want.
		{"the right secret with no hub attached still answers 200", "/api/v1/chat/kick/" + secret, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := jsonRequest(t, http.MethodPost, tc.path, map[string]any{"content": "hello"})
			// Deliberately unsigned: Kick posts with no session and no CSRF
			// token, so this route has to work without either.
			if w := do(t, h, r); w.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestAccountStatsReportsAbsenceRatherThanFailing(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	// YouTube has no viewer-count capability, and "we cannot ask" must not look
	// like "the account is gone" — the two have different fixes.
	id := connectAccount(t, store, s.box, db.PlatformYouTube, "chan")

	r := jsonRequest(t, http.MethodGet, "/api/v1/platforms/accounts/"+strconv.FormatInt(id, 10)+"/stats", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var got struct {
		Supported bool   `json:"supported"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Supported {
		t.Fatal("youtube claimed a viewer count polyemesis does not read")
	}
	if got.Reason == "" {
		t.Fatal("absence was reported with no explanation, so the UI has nothing to show")
	}
}

func TestStartChatIsAQuietNoOpWithNothingConnected(t *testing.T) {
	s, _, _ := testServer(t, config.Config{})
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)

	// The degradation case: a fresh install with no accounts must attach
	// nothing, log nothing alarming and above all not fail the start.
	if n := s.StartChat(context.Background()); n != 0 {
		t.Fatalf("attached %d adapters with no connected account", n)
	}
	if len(s.chat.Statuses()) != 0 {
		t.Fatalf("statuses = %#v, want none", s.chat.Statuses())
	}
}

func TestStartChatSkipsAPlatformItCannotBuildWithoutLosingTheOthers(t *testing.T) {
	s, _, store := testServer(t, config.Config{})
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)

	// Twitch builds from an account name alone. Kick cannot, because
	// connectAccount stores a slug ref rather than the numeric broadcaster id —
	// which is exactly the shape of a row written by an older build.
	connectAccount(t, store, s.box, db.PlatformTwitch, "dj")
	connectAccount(t, store, s.box, db.PlatformKick, "kicker")

	if n := s.StartChat(context.Background()); n != 1 {
		t.Fatalf("attached %d adapters, want only the one that could be built", n)
	}
	for _, st := range s.chat.Statuses() {
		if st.Platform == db.PlatformKick {
			t.Fatal("kick attached despite having no broadcaster id")
		}
	}
}

func TestRefreshKeyTellsKickOperatorsToPasteRatherThanRetry(t *testing.T) {
	// 502 invites a retry that can never succeed. A platform that publishes no
	// key endpoint is a 400: the fix is the paste field, not the button again.
	s, h, store := testServer(t, config.Config{})
	sign := login(t, h)

	acctID := connectAccount(t, store, s.box, db.PlatformKick, "kicker")
	if err := store.PutPlatformCreds(s.box, db.PlatformKick, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	dest, err := store.CreateDestination(&db.Destination{
		Name: "Kick", Kind: db.DestRTMP, Platform: db.PlatformKick,
		URL: "rtmps://ingest.example/app", StreamKey: "sk-live-Zq7", AccountID: &acctID,
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}

	r := jsonRequest(t, http.MethodPost,
		"/api/v1/destinations/"+strconv.FormatInt(dest.ID, 10)+"/refresh-key", nil)
	sign(r)
	w := do(t, h, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 so the client stops retrying (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(strings.ToLower(w.Body.String()), "paste") {
		t.Fatalf("the error does not tell the operator what to do instead: %s", w.Body.String())
	}
	// Never the client secret, never the stream key already on the row.
	for _, leak := range []string{"topsecret", "sk-live-Zq7"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Fatalf("response leaked %q: %s", leak, w.Body.String())
		}
	}
}
