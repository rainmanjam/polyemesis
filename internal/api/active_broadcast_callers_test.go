package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// Each reader of "which broadcast is on air" must use the lifecycle's recorded
// id over the announcement mirror, and X must never fall back to its mirror.
// These drive the callers, not just db.Destination.ActiveBroadcastID.

func TestFacebookStreamHealthAsksAboutTheRecordedBroadcastNotTheMirror(t *testing.T) {
	g := newGraphStub(t)
	g.ingestStreams = map[string]any{"data": []any{}}
	s, h, store := testServerWith(t, Options{Providers: oauth.NewSet(oauth.WithBaseURL(g.URL))})
	sign := login(t, h)
	id := fbBroadcastDest(t, s, store, "mirror-1")
	if _, err := store.UpdateLifecycle(id, func(d *db.Destination) bool {
		d.Lifecycle.BroadcastID = "onair-1"
		return true
	}); err != nil {
		t.Fatal(err)
	}
	r := jsonRequest(t, http.MethodGet, "/api/v1/destinations/"+itoa(id)+"/facebook/stream-health", nil)
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	var asked []string
	for _, p := range g.seen {
		asked = append(asked, p)
	}
	all := strings.Join(asked, "\n")
	if !strings.Contains(all, "/onair-1") || strings.Contains(all, "/mirror-1") {
		t.Errorf("Graph was asked %q, want only the recorded broadcast onair-1", all)
	}
}

func TestAccountDestinationsReportTheRecordedBroadcastAndNeverXsMirror(t *testing.T) {
	s, _, store := testServer(t, config.Config{})
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "chan")
	mk := func(name string, p db.Platform, life, mirror string) int64 {
		row := &db.Destination{Name: name, Kind: db.DestRTMP, Platform: p,
			URL: "rtmp://example.invalid/live", StreamKey: "k", AccountID: &acct}
		row.Facebook.BroadcastID = mirror
		d, err := store.CreateDestination(row)
		if err != nil {
			t.Fatal(err)
		}
		if life != "" {
			if _, err := store.UpdateLifecycle(d.ID, func(d *db.Destination) bool {
				d.Lifecycle.BroadcastID = life
				return true
			}); err != nil {
				t.Fatal(err)
			}
		}
		return d.ID
	}
	mk("yt", db.PlatformYouTube, "recorded", "mirror")
	mk("x-stale", db.PlatformX, "", "stale-mirror")
	mk("x-live", db.PlatformX, "x-recorded", "stale-mirror")
	mk("fb-mirror-only", db.PlatformFacebook, "", "fb-mirror")

	got, err := s.accountDestinations(acct)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"yt": "recorded", "x-stale": "", "x-live": "x-recorded", "fb-mirror-only": "fb-mirror"}
	for _, d := range got {
		if d.BroadcastID != want[d.Name] {
			t.Errorf("%s: broadcast %q, want %q", d.Name, d.BroadcastID, want[d.Name])
		}
	}
	if len(got) != len(want) {
		t.Fatalf("listed %d destinations, want %d", len(got), len(want))
	}
}

func TestRolloverCountsFromTheRecordedBroadcastNotTheMirror(t *testing.T) {
	for _, c := range []struct {
		name     string
		platform db.Platform
		life     string
		mirror   string
		want     string
	}{
		{"recorded beats mirror", db.PlatformFacebook, "onair", "scheduled", "onair"},
		{"facebook falls back to mirror", db.PlatformFacebook, "", "scheduled", "scheduled"},
		{"x never uses its stale mirror", db.PlatformX, "", "stale", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRolloverRig(t)
			r.addDest(1, c.platform, c.life)
			r.store.dests[1].Facebook.BroadcastID = c.mirror
			r.enable(1, "")
			if _, err := r.c.considerOne(context.Background(), r.store.settings[1], false); err != nil {
				t.Fatal(err)
			}
			if got := r.store.settings[1].SessionBroadcastID; got != c.want {
				t.Errorf("session broadcast = %q, want %q", got, c.want)
			}
		})
	}
}
