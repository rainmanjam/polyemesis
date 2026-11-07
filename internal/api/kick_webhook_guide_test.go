package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// The Kick webhook URL is what the operator pastes into their Kick app
// (Kick -> Settings -> Developer -> app -> Enable webhooks). It carries a path
// secret, so it is a credential: shown to a session or an admin token, never to
// a read token, and never when the server has no public HTTPS hostname.

type kickGuide struct {
	Platform         string   `json:"platform"`
	WebhookURL       string   `json:"webhookUrl"`
	WebhookURLReason string   `json:"webhookUrlReason"`
	Steps            []string `json:"steps"`
}

func fetchGuides(t *testing.T, h http.Handler, sign func(*http.Request)) (map[string]kickGuide, string) {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/platforms/guides", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("guides: status %d %s", w.Code, w.Body.String())
	}
	var list []kickGuide
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out := map[string]kickGuide{}
	for _, g := range list {
		out[g.Platform] = g
	}
	return out, w.Body.String()
}

func TestKickGuideShowsTheWebhookURLToTheOperator(t *testing.T) {
	s, h, _ := testServerWith(t, Options{Config: testConfigWithPublicURL()})
	sign := login(t, h)

	want := "https://stream.example.com" + kickWebhookPath(s.kickCallbackSecret())
	if got := s.KickChatCallbackURL(); got != want {
		t.Fatalf("KickChatCallbackURL = %q, want %q", got, want)
	}

	guides, _ := fetchGuides(t, h, sign)
	k := guides[string(db.PlatformKick)]
	if k.WebhookURL != want {
		t.Fatalf("kick guide webhookUrl = %q, want %q", k.WebhookURL, want)
	}
	if k.WebhookURLReason != "" {
		t.Errorf("a shown URL carries a reason: %q", k.WebhookURLReason)
	}
	// No other platform has one.
	for p, g := range guides {
		if p != string(db.PlatformKick) && (g.WebhookURL != "" || g.WebhookURLReason != "") {
			t.Errorf("%s guide carries a webhook URL: %+v", p, g)
		}
	}
	// The step list tells the operator to enable webhooks and paste the URL.
	joined := strings.ToLower(strings.Join(k.Steps, "\n"))
	if !strings.Contains(joined, "webhook") {
		t.Errorf("kick guide steps never mention the webhook: %q", k.Steps)
	}
}

// The URL the guide shows is the one the adapter probes and Kick must post to.
func TestKickGuideURLIsTheAdapterCallbackURL(t *testing.T) {
	s := &Server{cfg: testConfigWithPublicURL()}
	box, err := secrets.New(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	s.box = box
	a, nerr := chat.NewKick(chat.KickConfig{
		Token:          chat.StaticToken("t"),
		PublicURL:      publicBaseURL(s.cfg),
		CallbackSecret: s.kickCallbackSecret(),
		CallbackPath:   kickWebhookPath,
	})
	if nerr != nil {
		t.Fatal(nerr)
	}
	if got, want := s.KickChatCallbackURL(), a.CallbackURL(); got != want || got == "" {
		t.Fatalf("guide URL %q != adapter URL %q", got, want)
	}
}

func TestKickGuideWithoutAPublicHostnameSaysWhy(t *testing.T) {
	_, h, _ := testServerWith(t, Options{})
	sign := login(t, h)
	k := mustKick(t, h, sign)
	if k.WebhookURL != "" {
		t.Fatalf("no public hostname, yet the guide shows %q", k.WebhookURL)
	}
	if !strings.Contains(k.WebhookURLReason, "public HTTPS hostname") {
		t.Fatalf("reason = %q, want it to say a public HTTPS hostname is needed", k.WebhookURLReason)
	}
}

func TestKickGuideWithholdsTheWebhookURLFromAReadToken(t *testing.T) {
	s, h, _ := testServerWith(t, Options{Config: testConfigWithPublicURL()})
	sign := login(t, h)
	secret := s.kickCallbackSecret()
	if secret == "" {
		t.Fatal("no secret to leak")
	}

	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)
	k, body := fetchGuides(t, h, bearer(read))
	if strings.Contains(body, secret) {
		t.Fatalf("a read token was handed the webhook secret in the guides body")
	}
	g := k[string(db.PlatformKick)]
	if g.WebhookURL != "" || g.WebhookURLReason == "" {
		t.Fatalf("read token view = %+v, want no URL and a reason", g)
	}

	admin := createScopedToken(t, h, sign, "ops", db.ScopeAdmin)
	if k, _ := fetchGuides(t, h, bearer(admin)); k[string(db.PlatformKick)].WebhookURL == "" {
		t.Error("an admin token was not shown the URL")
	}
}

func mustKick(t *testing.T, h http.Handler, sign func(*http.Request)) kickGuide {
	t.Helper()
	g, _ := fetchGuides(t, h, sign)
	k, ok := g[string(db.PlatformKick)]
	if !ok {
		t.Fatal("no kick guide")
	}
	return k
}
