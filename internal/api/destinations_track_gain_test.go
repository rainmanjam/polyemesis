package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func trackGainPath(id int64, track int) string {
	return destPath(id) + "/tracks/" + strconv.Itoa(track) + "/gain"
}

// A zero gain is refused rather than stored as silence: mute is its own flag, so
// a track cannot be silenced by a gain that looks like a fader setting.
func TestTrackGainRefusesAZeroGainAndSendsMuteInstead(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	id := makeDest(t, h, sign, "gain-zero")
	msg := mustJSONError(t, h, sign, http.MethodPost, trackGainPath(id, 0),
		map[string]any{"gain": 0}, http.StatusBadRequest)
	if !strings.Contains(msg, `"muted"`) {
		t.Errorf("the refusal does not tell the caller to mute instead: %q", msg)
	}
}

func TestTrackGainNeedsATrackInTheSelection(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	id := makeDest(t, h, sign, "gain-missing")
	mustJSONError(t, h, sign, http.MethodPost, trackGainPath(id, 99),
		map[string]any{"muted": true}, http.StatusNotFound)
}

// Mute is persisted, and the answer says how it took effect. The store is read
// back to prove the write happened before the response.
func TestTrackGainMutePersistsAndAnswersWithHowItApplied(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())
	id := makeDest(t, h, sign, "gain-mute")

	raw := send(t, h, sign, http.MethodPost, trackGainPath(id, 0),
		map[string]any{"muted": true}, http.StatusOK)
	var out struct {
		Applied string `json:"applied"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("response is not JSON: %v: %s", err, raw)
	}
	if out.Applied != "live" && out.Applied != "restart" {
		t.Errorf("applied = %q, want \"live\" or \"restart\": %s", out.Applied, raw)
	}

	row, err := store.GetDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sel := range row.Profile.Tracks {
		if sel.Track == 0 {
			found = true
			if !sel.Muted {
				t.Error("the mute was answered but not stored")
			}
		}
	}
	if !found {
		t.Fatal("track 0 is gone from the stored profile")
	}

	// Setting the same state again is a no-op, and says so.
	raw = send(t, h, sign, http.MethodPost, trackGainPath(id, 0),
		map[string]any{"muted": true}, http.StatusOK)
	if err := json.Unmarshal(raw, &out); err != nil || out.Applied != "live" {
		t.Errorf("a repeated mute answered %q (%v), want live with no change", out.Applied, err)
	}
}
