package api

import (
	"net/http"
	"testing"
)

func presetBody(name string, extra map[string]any) map[string]any {
	b := map[string]any{
		"name": name, "kind": "file", "url": name + ".mkv", "enabled": false, "audioBitrate": 160,
		"profile": map[string]any{
			"mode": "simple", "tracks": trackSel(0), "matrix": []any{},
			"normalize": "off", "sampleRate": 48000,
		},
	}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

// presetId is set on create, kept by a PUT that leaves it out, cleared by one
// that sends it empty, and only a save that SETS it is checked.
func TestADestinationsPresetIsSetKeptAndCleared(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())

	send(t, h, sign, http.MethodPost, "/api/v1/destinations", presetBody("bad", map[string]any{"presetId": "no-such-preset"}), http.StatusBadRequest)
	send(t, h, sign, http.MethodPost, "/api/v1/destinations", presetBody("mismatch", map[string]any{"presetId": "youtube", "platform": "twitch"}), http.StatusBadRequest)

	var out struct {
		Destination struct {
			ID       int64  `json:"id"`
			PresetID string `json:"presetId"`
		} `json:"destination"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/destinations", presetBody("ig", map[string]any{"presetId": "instagram"}), http.StatusCreated), &out)
	id := out.Destination.ID
	if out.Destination.PresetID != "instagram" {
		t.Fatalf("created preset = %q", out.Destination.PresetID)
	}

	send(t, h, sign, http.MethodPut, destPath(id), map[string]any{"name": "ig renamed"}, http.StatusOK)
	if row, _ := store.GetDestination(id); row.PresetID != "instagram" {
		t.Fatalf("a PUT that left presetId out lost it: %q", row.PresetID)
	}
	send(t, h, sign, http.MethodPut, destPath(id), map[string]any{"presetId": "nope"}, http.StatusBadRequest)
	send(t, h, sign, http.MethodPut, destPath(id), map[string]any{"presetId": "youtube"}, http.StatusBadRequest)
	send(t, h, sign, http.MethodPut, destPath(id), map[string]any{"presetId": ""}, http.StatusOK)
	if row, _ := store.GetDestination(id); row.PresetID != "" {
		t.Fatalf("an empty presetId did not clear it: %q", row.PresetID)
	}
}
