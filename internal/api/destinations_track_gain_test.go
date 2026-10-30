package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/routing"
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

// A stopped destination was neither changed live nor restarted. The answer
// must say the level is stored for its next start, not claim a restart.
//
// MUTATION: drop the DestinationRunning arm. Observed: FAIL, the reason
// claimed "the destination was restarted".
func TestTrackGainOnAStoppedDestinationSaysItIsStored(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	id := makeDest(t, h, sign, "gain-stopped") // created disabled
	raw := send(t, h, sign, http.MethodPost, trackGainPath(id, 0),
		map[string]any{"muted": true}, http.StatusOK)
	var out struct {
		Applied string `json:"applied"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Applied != "restart" || !strings.Contains(out.Reason, "not running") {
		t.Errorf("a stopped destination answered %q / %q; want restart, saying it is not running", out.Applied, out.Reason)
	}
}

// A copy destination forwards its audio untouched, so a mute there would be
// stored and reach nothing while the response claimed success. A Stream Deck
// key would read "muted" while the sound kept flowing. Refused instead.
//
// MUTATION: drop the copy refusal. Observed: FAIL, 200.
func TestTrackGainRefusesACopyDestination(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())
	id := makeDest(t, h, sign, "gain-copy")
	row, err := store.GetDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	row.Audio.Copy = true
	if _, err := store.UpdateDestination(row); err != nil {
		t.Fatalf("make it a copy destination: %v", err)
	}
	msg := mustJSONError(t, h, sign, http.MethodPost, trackGainPath(id, 0),
		map[string]any{"muted": true}, http.StatusConflict)
	if !strings.Contains(msg, "unchanged") {
		t.Errorf("the refusal does not say why: %q", msg)
	}
	after, _ := store.GetDestination(id)
	for _, sel := range after.Profile.Tracks {
		if sel.Muted {
			t.Error("the refused mute was stored anyway")
		}
	}
}

// Two keys pressed together -- mute track 0, mute track 1 -- are two
// read-modify-writes of one profile. Unserialised, the second write can carry
// the first one's stale copy and silently undo it.
//
// MUTATION: remove the trackGainMu lock. Observed: FAIL (lost update) in 3 of
// 3 runs.
func TestConcurrentTrackChangesDoNotLoseEachOther(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())
	id := makeDest(t, h, sign, "gain-race")
	row, err := store.GetDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	row.Profile.Tracks = []routing.TrackSel{
		{Track: 0, Enabled: true, Gain: 1},
		{Track: 1, Enabled: true, Gain: 1},
	}
	if _, err := store.UpdateDestination(row); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 10; round++ {
		want := round%2 == 0
		var wg sync.WaitGroup
		for _, track := range []int{0, 1} {
			wg.Add(1)
			go func(track int) {
				defer wg.Done()
				req := httptest.NewRequest(http.MethodPost, trackGainPath(id, track),
					strings.NewReader(`{"muted":`+strconv.FormatBool(want)+`}`))
				req.Header.Set("Content-Type", "application/json")
				sign(req)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Errorf("track %d: %d %s", track, rec.Code, rec.Body.String())
				}
			}(track)
		}
		wg.Wait()
		got, err := store.GetDestination(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, sel := range got.Profile.Tracks {
			if sel.Track > 1 {
				continue // the store fills the rest of the selection in
			}
			if sel.Muted != want {
				t.Fatalf("round %d: track %d muted=%v, want %v: one change overwrote the other", round, sel.Track, sel.Muted, want)
			}
		}
	}
}
