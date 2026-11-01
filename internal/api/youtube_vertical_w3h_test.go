package api

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// Follow-ups to the YouTube paired vertical (#7) from the wave 3 re-judge.

// #37: creating the pair reconciles, the way creating any destination does, so the new vertical
// half (and its rendition) reach the pipeline without waiting for some other save.
func TestPairingReconcilesSoTheVerticalHalfReachesThePipeline(t *testing.T) {
	fake := newYTVerticalPairStub(t)
	s, h, store := verticalServer(t, fake.srv.URL)
	sign := login(t, h)
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid-abc", "secret-xyz"); err != nil {
		t.Fatalf("seed youtube credentials: %v", err)
	}
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")
	horiz := ytDest(t, store, acct, "Main channel", "key-abc")
	var reconciles atomic.Int32
	s.reconciled = func() { reconciles.Add(1) }
	send(t, h, sign, http.MethodPost, "/api/v1/destinations/"+itoa(horiz.ID)+"/youtube-vertical",
		map[string]any{"tier": "vertical1080p30"}, http.StatusOK)
	if reconciles.Load() == 0 {
		t.Error("the vertical half was stored and the pipeline was not reconciled")
	}
}

// #35: the create body goes through decodeJSON, which refuses unknown fields and a body over 1 MiB.
func TestPairingRefusesAnUnknownFieldAndAnOversizedBody(t *testing.T) {
	fake := newYTVerticalPairStub(t)
	s, h, store := verticalServer(t, fake.srv.URL)
	sign := login(t, h)
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid-abc", "secret-xyz"); err != nil {
		t.Fatalf("seed youtube credentials: %v", err)
	}
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")
	horiz := ytDest(t, store, acct, "Main channel", "key-abc")
	path := "/api/v1/destinations/" + itoa(horiz.ID) + "/youtube-vertical"
	send(t, h, sign, http.MethodPost, path, map[string]any{"tier": "vertical1080p30", "streamKey": "x"}, http.StatusBadRequest)
	big := map[string]any{"tier": "vertical1080p30", "pad": strings.Repeat("a", 2<<20)}
	r := jsonRequest(t, http.MethodPost, path, big)
	sign(r)
	if code := do(t, h, r).Code; code < 400 || code >= 500 {
		t.Errorf("a 2 MiB body got %d, want a 4xx refusal", code)
	}
	if _, ok := fake.called("POST", "/liveStreams"); ok {
		t.Error("a refused body still created a vertical stream on YouTube")
	}
}

// #36: a rendition is reused only when it encodes the same way, bitrate included. Reusing a
// portrait rendition at another bitrate would change what an existing destination sends.
func TestAPortraitRenditionAtAnotherBitrateIsNotReused(t *testing.T) {
	s, _, store := verticalServer(t, "http://127.0.0.1:1")
	src := int64(1)
	tier, ok := findYouTubeVerticalTier("vertical1080p30")
	if !ok {
		t.Fatal("no vertical1080p30 tier")
	}
	other := *tier.Rendition
	other.SourceID = &src
	other.Name = "operator's portrait"
	other.VideoBitrate = tier.Rendition.VideoBitrate / 2
	created, err := store.CreateRendition(&other)
	if err != nil {
		t.Fatalf("CreateRendition: %v", err)
	}
	got, _, err := s.verticalRenditionFor(&db.Destination{SourceID: &src}, tier)
	if err != nil {
		t.Fatalf("verticalRenditionFor: %v", err)
	}
	if got == created.ID {
		t.Errorf("reused rendition %d at %d kbps for a %d kbps tier", got, other.VideoBitrate, tier.Rendition.VideoBitrate)
	}
	again, _, err := s.verticalRenditionFor(&db.Destination{SourceID: &src}, tier)
	if err != nil || again != got {
		t.Errorf("the matching rendition was not reused: %d then %d (%v)", got, again, err)
	}
}

// #34/#63: concurrent status reads make one YouTube call, not one each.
func TestConcurrentVerticalStatusReadsMakeOneYouTubeCall(t *testing.T) {
	_, h, _, sign, fake, horizontalID := verticalCreate(t)
	fake.mu.Lock()
	fake.calls = nil
	fake.healthDelay = 100 * time.Millisecond
	fake.mu.Unlock()
	path := "/api/v1/destinations/" + itoa(horizontalID) + "/youtube-vertical"
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := jsonRequest(t, http.MethodGet, path, nil)
			sign(r)
			_ = do(t, h, r)
		}()
	}
	wg.Wait()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	n := 0
	for _, c := range fake.calls {
		if strings.HasPrefix(c, "GET /liveStreams?") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d YouTube health reads for five concurrent requests, want 1", n)
	}
}

// #63: the pacer is keyed by the vertical stream too, so a pair re-made on a new stream is not
// answered with the old stream's health.
func TestTheVerticalHealthPacerIsKeyedByTheVerticalStream(t *testing.T) {
	var p verticalHealthPacer
	now := time.Now()
	p.remember(verticalHealthKey{horizontalID: 1, streamID: "old"}, verticalHealthRead{at: now})
	if _, ok := p.recent(verticalHealthKey{horizontalID: 1, streamID: "new"}, now); ok {
		t.Error("a read of the old stream answered for the new one")
	}
	if _, ok := p.recent(verticalHealthKey{horizontalID: 1, streamID: "old"}, now); !ok {
		t.Error("a read inside the window was not reused")
	}
}
