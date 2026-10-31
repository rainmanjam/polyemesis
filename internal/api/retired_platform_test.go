package api

// TROVO AT THE REST BOUNDARY. Trovo ceased live streaming on 30 June 2026, and
// polyemesis retired it: internal/db refuses to create, retype to, or start a
// destination on it (see retired_platform_test.go there). These tests pin that
// the refusal reaches an API client as a 4xx that SAYS SO -- not a 500, and
// not an FFmpeg error about a hostname that no longer resolves -- and that the
// rows already on an install stay listed, readable and deletable.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

const trovoShutdown = "Trovo shut down on 30 June 2026"

func TestCreatingATrovoDestinationOverTheAPIIsRefusedNamingTheShutdown(t *testing.T) {
	_, h, store, sign := engineServer(t, defaultTools(), Options{})

	w := send(t, h, sign, http.MethodPost, "/api/v1/destinations", map[string]any{
		"name": "trovo", "kind": "rtmp", "platform": "trovo",
		"url": "rtmp://livepush.trovo.example/live", "streamKey": "typed-by-hand",
	}, http.StatusBadRequest)
	if !strings.Contains(string(w), trovoShutdown) {
		t.Fatalf("the refusal does not name the shutdown: %s", string(w))
	}
	if strings.Contains(string(w), "typed-by-hand") {
		t.Errorf("the refusal echoed the stream key")
	}
	rows, err := store.ListDestinations()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range rows {
		if r.Platform == db.PlatformTrovo {
			t.Fatalf("the refused create still wrote a Trovo row")
		}
	}
}

func TestStartingAnExistingTrovoDestinationIsRefusedWithTheReasonAndItCanStillBeDeleted(t *testing.T) {
	_, h, store, sign := engineServer(t, defaultTools(), Options{})

	var created destinationReply
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/destinations", map[string]any{
		"name": "old trovo", "kind": "rtmp", "platform": "custom",
		"url": "rtmp://livepush.trovo.example/live", "streamKey": "typed-by-hand",
	}, http.StatusCreated), &created)
	id := created.Destination.ID
	// A row saved before the shutdown, written behind the store's back because
	// the release that wrote it is not the one running this test.
	if _, err := store.SQL().Exec(`UPDATE destinations SET platform='trovo' WHERE id=?`, id); err != nil {
		t.Fatalf("retype to trovo: %v", err)
	}

	w := send(t, h, sign, http.MethodPost, "/api/v1/destinations/"+itoa(id)+"/start", nil, http.StatusConflict)
	if !strings.Contains(string(w), trovoShutdown) {
		t.Fatalf("the start refusal does not say why: %s", string(w))
	}

	// Readable: listed with its reason and its settings.
	var list []struct {
		Destination db.Destination `json:"destination"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/destinations", nil, http.StatusOK), &list)
	found := false
	for _, r := range list {
		if r.Destination.ID != id {
			continue
		}
		found = true
		if r.Destination.Enabled {
			t.Error("a retired destination is listed as enabled")
		}
		if !strings.Contains(r.Destination.Retired, trovoShutdown) {
			t.Errorf("the listed row carries no reason: %q", r.Destination.Retired)
		}
		if r.Destination.URL != "rtmp://livepush.trovo.example/live" {
			t.Errorf("the settings were not kept readable: url=%q", r.Destination.URL)
		}
	}
	if !found {
		t.Fatal("the retired destination is missing from the list")
	}

	send(t, h, sign, http.MethodDelete, "/api/v1/destinations/"+itoa(id), nil, http.StatusOK)
}
