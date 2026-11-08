package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// PUT /destinations/{id} with a goLiveTitle used to answer 200 on a destination whose platform never sends one,
// and store nothing: dropUnsendableSettings cleared it before Validate could refuse it. Now the title is saved
// where it is sent (X, YouTube) and refused with 400 where it is not.
func TestAGoLiveTitleIsSavedForYouTubeAndRefusedWhereItIsNotSent(t *testing.T) {
	_, h, store, sign := engineServer(t, defaultTools(), Options{})
	mk := func(name string, p db.Platform, url string) *db.Destination {
		d, err := store.CreateDestination(&db.Destination{Name: name, Kind: db.DestRTMP, Platform: p, URL: url, StreamKey: "k"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		return d
	}
	yt := mk("yt", db.PlatformYouTube, "rtmp://a.rtmp.youtube.com/live2")
	kick := mk("kick", db.PlatformKick, "rtmps://ingest.example/app")
	put := func(id int64, body map[string]any) int {
		r := jsonRequest(t, http.MethodPut, "/api/v1/destinations/"+strconv.FormatInt(id, 10), body)
		sign(r)
		return do(t, h, r).Code
	}

	if code := put(yt.ID, map[string]any{"goLiveTitle": "Friday show"}); code != http.StatusOK {
		t.Fatalf("YouTube PUT = %d, want 200", code)
	}
	if got, _ := store.GetDestination(yt.ID); got.GoLiveTitle != "Friday show" {
		t.Errorf("stored title = %q, want it saved for YouTube", got.GoLiveTitle)
	}
	if code := put(yt.ID, map[string]any{"goLiveTitle": strings.Repeat("a", 101)}); code != http.StatusBadRequest {
		t.Errorf("a 101-character YouTube title = %d, want 400", code)
	}
	if code := put(yt.ID, map[string]any{"goLiveTitle": strings.Repeat("é", 100)}); code != http.StatusOK {
		t.Errorf("a 100-character YouTube title = %d, want 200", code)
	}
	if code := put(kick.ID, map[string]any{"goLiveTitle": "nope"}); code != http.StatusBadRequest {
		t.Errorf("a title on Kick = %d, want 400 rather than a 200 that stores nothing", code)
	}
	if got, _ := store.GetDestination(kick.ID); got.GoLiveTitle != "" {
		t.Errorf("Kick stored title %q", got.GoLiveTitle)
	}
	// POST is the same write path.
	r := jsonRequest(t, http.MethodPost, "/api/v1/destinations", map[string]any{
		"name": "k2", "kind": "rtmp", "platform": "kick", "url": "rtmps://ingest.example/app", "streamKey": "k", "goLiveTitle": "x"})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusBadRequest {
		t.Errorf("POST a title on Kick = %d, want 400: %s", w.Code, w.Body.String())
	}
}

// A title that was carried over by a platform switch is not "set by this save": the row stays saveable and the
// title is cleared with a warning, as before.
func TestSwitchingAnXDestinationWithATitleToKickStaysSaveable(t *testing.T) {
	_, h, store, sign := engineServer(t, defaultTools(), Options{})
	d, err := store.CreateDestination(&db.Destination{Name: "x", Kind: db.DestRTMP, Platform: db.PlatformX,
		URL: "rtmps://x.example/app", StreamKey: "k", GoLiveTitle: "old title"})
	if err != nil {
		t.Fatal(err)
	}
	r := jsonRequest(t, http.MethodPut, "/api/v1/destinations/"+strconv.FormatInt(d.ID, 10),
		map[string]any{"platform": "kick", "url": "rtmps://ingest.example/app"})
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("switching platform = %d: %s", w.Code, w.Body.String())
	}
	if got, _ := store.GetDestination(d.ID); got.GoLiveTitle != "" || !strings.Contains(w.Body.String(), "go-live title was removed") {
		t.Errorf("title = %q, body %s; want it cleared with a warning", got.GoLiveTitle, w.Body.String())
	}
}

func TestPinnedStreamIsClearedWhenAYouTubeDestinationSwitchesPlatform(t *testing.T) {
	_, h, store, sign := engineServer(t, defaultTools(), Options{})
	d, err := store.CreateDestination(&db.Destination{Name: "y", Kind: db.DestRTMP, Platform: db.PlatformYouTube,
		URL: "rtmp://a.rtmp.youtube.com/live2", StreamKey: "k",
		Compliance: db.Compliance{YouTube: &db.YouTubeBroadcastControls{StreamID: "s-1", StudioStreamNow: true}}})
	if err != nil {
		t.Fatal(err)
	}
	r := jsonRequest(t, http.MethodPut, "/api/v1/destinations/"+strconv.FormatInt(d.ID, 10),
		map[string]any{"platform": "kick", "url": "rtmps://ingest.example/app"})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("switching platform = %d: %s", w.Code, w.Body.String())
	}
	if got, _ := store.GetDestination(d.ID); got.Compliance.YouTube != nil {
		t.Errorf("compliance.youtube = %+v, want the YouTube-only settings cleared", got.Compliance.YouTube)
	}
}
