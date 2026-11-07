package api

import (
	"context"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/analytics"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/startupenv"
)

// rumbleHealthAdapter is a Rumble chat adapter that reports a fixed health, as
// the real one does after a poll.
type rumbleHealthAdapter struct {
	moderatableAdapter
	health chat.Health
}

func (a *rumbleHealthAdapter) Health() chat.Health { return a.health }

func attachRumbleHealth(t *testing.T, s *Server, h chat.Health) {
	t.Helper()
	hub := newTestHub(t)
	s.chat = hub
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := &rumbleHealthAdapter{moderatableAdapter: moderatableAdapter{platform: db.PlatformRumble, account: "rumble"}, health: h}
	if err := hub.Attach(ctx, a); err != nil {
		t.Fatal(err)
	}
}

func TestRumbleViewersFeedAnalytics(t *testing.T) {
	f := newAnalyticsFixture(t)
	n := 42
	attachRumbleHealth(t, f.s, chat.Health{State: chat.StateLive, Viewers: &n})
	d, err := f.store.CreateDestination(&db.Destination{
		Name: "Rumble by key", Kind: db.DestRTMP, Platform: db.PlatformRumble,
		URL: "rtmp://ingest.example/app", StreamKey: "key-rumble",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.running[d.ID] = true
	f.tick(t)
	var live analytics.Live
	f.get(t, "/api/v1/analytics/live", &live)
	var got *analytics.Reading
	for i := range live.Readings {
		if live.Readings[i].Platform == "rumble" {
			got = &live.Readings[i]
		}
	}
	if got == nil || !got.Live || got.Viewers == nil || *got.Viewers != 42 {
		t.Fatalf("rumble reading = %+v, want live with 42 viewers", got)
	}
}

func TestRumbleReadIsOfflineWhenNotLiveOrNoChat(t *testing.T) {
	f := newAnalyticsFixture(t)
	src := analyticsSource{f.s}
	f.s.chat = nil
	if st := src.readRumble(); st.Live || st.Viewers != nil {
		t.Errorf("no hub: %+v, want offline", st)
	}
	attachRumbleHealth(t, f.s, chat.Health{State: chat.StateDegraded, Detail: "waiting"})
	if st := src.readRumble(); st.Live || st.Viewers != nil {
		t.Errorf("waiting: %+v, want offline", st)
	}
	attachRumbleHealth(t, f.s, chat.Health{State: chat.StateLive})
	if st := src.readRumble(); !st.Live || st.Viewers != nil {
		t.Errorf("live with no count: %+v, want live and hidden", st)
	}
}

func TestRumbleKeyIsInTheDebugAndMCPRedactionSets(t *testing.T) {
	key := strings.Repeat("rumblekey", 3)
	s, _, _ := testServerWith(t, Options{Env: startupenv.Env{RumbleChatKey: key}})
	lits, err := s.diagSecretLiterals()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(lits, "\n"), key) {
		t.Error("diagSecretLiterals does not hold the Rumble key")
	}
	found := false
	for _, v := range s.mcpSecrets() {
		found = found || v == key
	}
	if !found {
		t.Error("mcpSecrets does not hold the Rumble key")
	}
}
