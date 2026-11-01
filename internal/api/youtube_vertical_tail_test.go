package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// THE LAST THREE VERTICAL-PAIR REVIEW FINDINGS (w3h tail, 25-27). Each test drives the real router, so the refusal,
// the rollback and the cached read are checked on the path an operator's click takes.

// TestAFailedVerticalPairRemovesThePortraitRenditionItMade pins the rollback. The rendition is made before the
// YouTube stream, so a stream create that fails used to leave a portrait rendition on the programme that nothing
// selected. The rendition this request made is removed; nothing else is.
//
// MUTATION, internal/api/youtube_vertical.go, in handleCreateYouTubeVertical: delete the deferred
// dropUnusedVerticalRendition call. Observed: FAIL -- "the failed pairing left 1 portrait rendition(s)".
func TestAFailedVerticalPairRemovesThePortraitRenditionItMade(t *testing.T) {
	yt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /channels":
			io.WriteString(w, `{"items":[{"id":"chan-1","snippet":{"title":"Night Owl"}}]}`)
		default:
			// The stream create fails, which is the step after the rendition is made.
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"message":"backend unavailable"}}`)
		}
	}))
	t.Cleanup(yt.Close)
	s, h, store := verticalServer(t, yt.URL)
	sign := login(t, h)
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid-abc", "secret-xyz"); err != nil {
		t.Fatalf("seed youtube credentials: %v", err)
	}
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")
	horiz := ytDest(t, store, acct, "Main channel", "key-abc")
	if horiz.SourceID == nil {
		t.Fatal("the fixture destination has no programme")
	}
	tier, ok := findYouTubeVerticalTier("vertical1080p30")
	if !ok {
		t.Fatal("the vertical1080p30 tier is gone")
	}
	portrait := func() int {
		rs, err := store.ListRenditionsBySource(*horiz.SourceID)
		if err != nil {
			t.Fatalf("list renditions: %v", err)
		}
		n := 0
		for _, r := range rs {
			if sameVerticalEncode(*r, *tier.Rendition) {
				n++
			}
		}
		return n
	}
	if n := portrait(); n != 0 {
		t.Fatalf("the programme already has %d portrait rendition(s)", n)
	}
	send(t, h, sign, http.MethodPost, "/api/v1/destinations/"+itoa(horiz.ID)+"/youtube-vertical",
		map[string]any{"tier": "vertical1080p30"}, http.StatusBadGateway)
	if n := portrait(); n != 0 {
		t.Fatalf("the failed pairing left %d portrait rendition(s) on the programme", n)
	}
}

// TestAStoreErrorOnTheVerticalHalfCheckRefusesThePair pins the guard failing closed. isVerticalHalf answered "no" on
// a store error, so a transient failure let a vertical half be given a vertical half of its own. The failure here
// reaches only the pair listing: one unreadable pair row, for two other destinations, leaves the per-destination
// read that runs first working.
//
// MUTATION, internal/api/youtube_vertical.go, in isVerticalHalf: return false, nil on the error. Observed: FAIL --
// status 200, want 500.
func TestAStoreErrorOnTheVerticalHalfCheckRefusesThePair(t *testing.T) {
	fake := newYTVerticalPairStub(t)
	s, h, store := verticalServer(t, fake.srv.URL)
	sign := login(t, h)
	if err := store.PutPlatformCreds(s.box, db.PlatformYouTube, "cid-abc", "secret-xyz"); err != nil {
		t.Fatalf("seed youtube credentials: %v", err)
	}
	acct := connectAccount(t, store, s.box, db.PlatformYouTube, "night-owl")
	horiz := ytDest(t, store, acct, "Main channel", "key-abc")
	a := ytDest(t, store, acct, "Other A", "key-a")
	b := ytDest(t, store, acct, "Other B", "key-b")
	if _, err := store.SQL().Exec(`INSERT INTO youtube_vertical_pairs (horizontal_id, vertical_id, vertical_stream_id,
		pairing_confirmed_at, created_at) VALUES (?, ?, 'x', 0, 'not-a-number')`, a.ID, b.ID); err != nil {
		t.Fatalf("seed an unreadable pair row: %v", err)
	}
	if _, err := store.ListYouTubeVerticalPairs(); err == nil {
		t.Fatal("the seeded row did not make the pair listing fail, so this test checks nothing")
	}
	send(t, h, sign, http.MethodPost, "/api/v1/destinations/"+itoa(horiz.ID)+"/youtube-vertical",
		map[string]any{"tier": "vertical1080p30"}, http.StatusInternalServerError)
	if _, ok := fake.called("POST", "/liveStreams"); ok {
		t.Fatal("a vertical stream was created while the pair check could not be read")
	}
}

// TestAnAbandonedVerticalStatusReadIsNotCachedAsAnError pins the cached read against the browser. The read is
// shared by every caller for a minute, so a request cancelled mid-read (a page navigated away) used to cache
// context.Canceled and show every operator an error banner on the sub-row for that minute.
//
// MUTATION, internal/api/youtube_vertical.go, in handleYouTubeVerticalStatus: derive the read's context from
// r.Context() again. Observed: FAIL -- the next read reports the cancelled one's error.
func TestAnAbandonedVerticalStatusReadIsNotCachedAsAnError(t *testing.T) {
	_, h, _, sign, _, id := verticalCreate(t)
	path := "/api/v1/destinations/" + itoa(id) + "/youtube-vertical"

	r := jsonRequest(t, http.MethodGet, path, nil)
	sign(r)
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	do(t, h, r.WithContext(ctx))

	body := send(t, h, sign, http.MethodGet, path, nil, http.StatusOK)
	var view struct {
		HealthError  string `json:"healthError"`
		HealthStatus string `json:"healthStatus"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if view.HealthError != "" || view.HealthStatus != "good" {
		t.Fatalf("the read after an abandoned one shows %q / %q, want no error and the health: %s",
			view.HealthError, view.HealthStatus, body)
	}
}
