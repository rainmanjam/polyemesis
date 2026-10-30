package api

// The per-destination preview: GET /destinations/stop-impact?destination=N.
//
// The Stream Deck's per-destination Stop key confirms against this count. The
// key refuses when the count cannot be loaded, so the route must answer an
// unknown or malformed destination with an error, never with zero counts: a
// zero is a statement that the stop ends nothing, and a key that read it as
// one would arm with "no YouTube" over a live broadcast it had failed to see.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func TestStopImpactForOneDestinationCountsOnlyThatRow(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())
	read := createScopedToken(t, h, sign, "streamdeck", db.ScopeRead)

	// A custom RTMP destination on the install's only programme gives a row
	// with a real source id. Its platform is custom, so it can never count.
	custom := createDestination(t, h, sign, destinationBody("custom-out", true, nil))
	if custom.SourceID == nil {
		t.Fatal("the created destination carries no source, so the fixture is not a programme's row")
	}

	// A YouTube row that is on air: the only kind a stop ends for good.
	yt, err := store.CreateDestination(&db.Destination{
		Name:      "yt-main",
		Kind:      db.DestRTMP,
		Platform:  db.PlatformYouTube,
		Enabled:   true,
		SourceID:  custom.SourceID,
		URL:       "rtmp://example.com/live",
		StreamKey: "abc",
	})
	if err != nil {
		t.Fatalf("seed youtube destination: %v", err)
	}
	// CreateDestination ignores the lifecycle block; UpdateLifecycle is the only
	// writer, so the on-air phase is set the way the coordinator sets it.
	yt, err = store.UpdateLifecycle(yt.ID, func(d *db.Destination) bool {
		d.Lifecycle = db.BroadcastControl{BroadcastID: "bc", Phase: "live"}
		return true
	})
	if err != nil {
		t.Fatalf("set youtube lifecycle: %v", err)
	}

	preview := func(id int64) *http.Request {
		r := jsonRequest(t, http.MethodGet, "/api/v1/destinations/stop-impact?destination="+strconv.FormatInt(id, 10), nil)
		r.Header.Set("Authorization", "Bearer "+read)
		return r
	}

	w := do(t, h, preview(yt.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("youtube preview: status = %d, want 200, body %s", w.Code, w.Body.String())
	}
	var got stopImpactBody
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Destinations != 1 || got.YouTubeBroadcasts != 1 {
		t.Errorf("youtube row = %+v, want {Destinations:1 YouTubeBroadcasts:1}", got)
	}

	w = do(t, h, preview(custom.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("custom preview: status = %d, want 200, body %s", w.Code, w.Body.String())
	}
	got = stopImpactBody{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Destinations != 1 || got.YouTubeBroadcasts != 0 {
		t.Errorf("custom row = %+v, want {Destinations:1 YouTubeBroadcasts:0}", got)
	}
}

// An id that names no destination must be refused, not counted as zero. A
// zero would let a stop key arm over a destination it could not see.
func TestStopImpactForAnUnknownDestinationIsRefused(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	read := createScopedToken(t, h, sign, "streamdeck", db.ScopeRead)

	r := jsonRequest(t, http.MethodGet, "/api/v1/destinations/stop-impact?destination=999999", nil)
	r.Header.Set("Authorization", "Bearer "+read)
	w := do(t, h, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown destination: status = %d, want 404, body %s", w.Code, w.Body.String())
	}
}

func TestStopImpactForAMalformedDestinationIsRejected(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	read := createScopedToken(t, h, sign, "streamdeck", db.ScopeRead)

	for _, raw := range []string{"abc", "0", "-3"} {
		r := jsonRequest(t, http.MethodGet, "/api/v1/destinations/stop-impact?destination="+raw, nil)
		r.Header.Set("Authorization", "Bearer "+read)
		if w := do(t, h, r); w.Code != http.StatusBadRequest {
			t.Errorf("destination=%q: status = %d, want 400, body %s", raw, w.Code, w.Body.String())
		}
	}
}
