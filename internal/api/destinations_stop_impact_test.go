package api

// The stop-impact preview, asserted at the function that decides it.
//
// The count must come from planLifecycle(false, ...) -- the same branch that
// stop sends to YouTube -- and not from a second copy of the phase list, so a
// preview cannot drift from what stop does. The fixtures cover each side of
// that branch and a non-YouTube row that must never be counted.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func stopImpactFixture(id int64, p db.Platform, enabled bool, phase string) *db.Destination {
	return &db.Destination{
		ID:       id,
		Platform: p,
		Enabled:  enabled,
		Lifecycle: db.BroadcastControl{
			BroadcastID: "bc",
			Phase:       phase,
		},
	}
}

func TestStopImpactCountsOnlyYouTubeBroadcastsStopWouldEnd(t *testing.T) {
	rows := []*db.Destination{
		stopImpactFixture(1, db.PlatformYouTube, true, "live"),          // ends
		stopImpactFixture(2, db.PlatformYouTube, true, "testing"),       // ends
		stopImpactFixture(3, db.PlatformYouTube, true, "complete"),      // already finished
		stopImpactFixture(4, db.PlatformYouTube, true, "created"),       // never aired
		stopImpactFixture(5, db.PlatformTwitch, true, "live"),           // not a broadcast
		stopImpactFixture(6, db.PlatformYouTube, true, ""),              // nothing recorded
		stopImpactFixture(7, db.PlatformYouTube, false, "liveStarting"), // ends
	}

	got := stopImpact(rows)

	if got.YouTubeBroadcasts != 3 {
		t.Fatalf("YouTubeBroadcasts = %d, want 3 (live, testing, liveStarting)", got.YouTubeBroadcasts)
	}
	if got.Destinations != len(rows) {
		t.Fatalf("Destinations = %d, want %d (every row a stop would touch)", got.Destinations, len(rows))
	}
}

func TestStopImpactOnAnEmptyInstallEndsNothing(t *testing.T) {
	got := stopImpact(nil)
	if got.YouTubeBroadcasts != 0 || got.Destinations != 0 {
		t.Fatalf("empty install = %+v, want zero counts", got)
	}
}

// The preview is a GET, so a read token may ask it. It must still refuse a
// token with no authority at all, and the stop it previews must still be
// refused to a read token -- a preview that a read token can reach does not
// make the stop reachable.
func TestStopImpactRouteIsReadableButStopAllStaysAdminOnly(t *testing.T) {
	// requireSource answers 503 on an install with no programme, so the fixture
	// must have one; renditionServer seeds a source.
	h, _, sign := renditionServer(t, defaultTools())
	read := createScopedToken(t, h, sign, "streamdeck", db.ScopeRead)

	get := func(token string) *httptest.ResponseRecorder {
		r := jsonRequest(t, http.MethodGet, "/api/v1/destinations/stop-impact", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		return do(t, h, r)
	}

	if w := get(""); w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous stop-impact: status = %d, want 401", w.Code)
	}
	w := get(read)
	if w.Code != http.StatusOK {
		t.Fatalf("read token stop-impact: status = %d, want 200, body %s", w.Code, w.Body.String())
	}
	var body stopImpactBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.YouTubeBroadcasts != 0 {
		t.Errorf("empty install YouTubeBroadcasts = %d, want 0", body.YouTubeBroadcasts)
	}

	stop := jsonRequest(t, http.MethodPost, "/api/v1/destinations/stop-all", map[string]any{"confirm": true})
	stop.Header.Set("Authorization", "Bearer "+read)
	if w := do(t, h, stop); w.Code != http.StatusForbidden {
		t.Errorf("read token stop-all: status = %d, want 403, body %s", w.Code, w.Body.String())
	}
}
