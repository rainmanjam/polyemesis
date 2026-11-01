package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The one-time Live Control Room step and the dashboard's view of a pair (#7). The pairing itself is pinned in
// youtube_vertical_test.go; these pin what happens after it: the confirmation is stored against the horizontal
// destination and nothing else, and the status payload draws the vertical half as the horizontal card's sub-row,
// not as a second destination.

// Confirming the step marks the pair confirmed, and the status read says so from then on. Before it, the pair reads
// unconfirmed, so the card keeps reminding the operator.
func TestConfirmingTheVerticalPairingIsStoredAndReadBackOnTheStatusRead(t *testing.T) {
	_, h, store, sign, _, horizID := verticalCreate(t)
	statusPath := "/api/v1/destinations/" + itoa(horizID) + "/youtube-vertical"

	var before verticalPairView
	decodeInto(t, send(t, h, sign, http.MethodGet, statusPath, nil, http.StatusOK), &before)
	if before.PairingConfirmed {
		t.Fatal("a fresh pair read as confirmed before the operator did the Live Control Room step")
	}

	var out map[string]any
	decodeInto(t, send(t, h, sign, http.MethodPost, statusPath+"/confirm", nil, http.StatusOK), &out)
	if out["pairingConfirmed"] != true {
		t.Fatalf("confirm answered %v, want pairingConfirmed true", out)
	}
	pair, err := store.GetYouTubeVerticalPairByHorizontal(horizID)
	if err != nil || pair.PairingConfirmedAt == 0 {
		t.Fatalf("pair after confirm = %+v (%v), want a stored confirmation time", pair, err)
	}

	var after verticalPairView
	decodeInto(t, send(t, h, sign, http.MethodGet, statusPath, nil, http.StatusOK), &after)
	if !after.PairingConfirmed {
		t.Fatal("the status read did not carry the stored confirmation")
	}
}

// A destination with no vertical half has nothing to confirm, and the confirmation is refused as not found rather
// than recorded against nothing. The vertical half itself is not a horizontal and is refused the same way.
func TestConfirmingAPairingForADestinationWithNoVerticalHalfIsNotFound(t *testing.T) {
	_, h, store, sign, _, horizID := verticalCreate(t)
	pair, err := store.GetYouTubeVerticalPairByHorizontal(horizID)
	if err != nil {
		t.Fatalf("GetYouTubeVerticalPairByHorizontal: %v", err)
	}
	send(t, h, sign, http.MethodPost, "/api/v1/destinations/"+itoa(pair.VerticalID)+"/youtube-vertical/confirm",
		nil, http.StatusNotFound)
	send(t, h, sign, http.MethodPost, "/api/v1/destinations/x/youtube-vertical/confirm", nil, http.StatusBadRequest)
	if got, _ := store.GetYouTubeVerticalPairByHorizontal(horizID); got.PairingConfirmedAt != 0 {
		t.Fatal("a refused confirmation was stored against the real pair")
	}
}

// The status payload links the two rows: the horizontal names its vertical half and whether it is confirmed, the
// vertical names the horizontal it belongs to, and neither row is linked to anything else.
func TestTheStatusPayloadDrawsTheVerticalHalfAsTheHorizontalsSubRow(t *testing.T) {
	_, h, store, sign, _, horizID := verticalCreate(t)
	pair, err := store.GetYouTubeVerticalPairByHorizontal(horizID)
	if err != nil {
		t.Fatalf("GetYouTubeVerticalPairByHorizontal: %v", err)
	}

	var st struct {
		Destinations []struct {
			ID              int64 `json:"id"`
			YouTubeVertical *struct {
				VerticalID       int64 `json:"verticalId"`
				PairingConfirmed bool  `json:"pairingConfirmed"`
			} `json:"youtubeVertical"`
			VerticalHalfOf *int64 `json:"verticalHalfOf"`
		} `json:"destinations"`
	}
	raw := send(t, h, sign, http.MethodGet, "/api/v1/status", nil, http.StatusOK)
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	seen := 0
	for _, d := range st.Destinations {
		switch d.ID {
		case horizID:
			seen++
			if d.YouTubeVertical == nil || d.YouTubeVertical.VerticalID != pair.VerticalID {
				t.Fatalf("horizontal row link = %+v, want vertical %d", d.YouTubeVertical, pair.VerticalID)
			}
			if d.YouTubeVertical.PairingConfirmed {
				t.Fatal("the horizontal row read confirmed before the step was done")
			}
			if d.VerticalHalfOf != nil {
				t.Fatal("the horizontal row was marked as somebody's vertical half")
			}
		case pair.VerticalID:
			seen++
			if d.VerticalHalfOf == nil || *d.VerticalHalfOf != horizID {
				t.Fatalf("vertical row's horizontal = %v, want %d", d.VerticalHalfOf, horizID)
			}
			if d.YouTubeVertical != nil {
				t.Fatal("the vertical row was given a vertical half of its own")
			}
		}
	}
	if seen != 2 {
		t.Fatalf("status listed %d of the pair's two rows", seen)
	}
}

// With the real engine and nothing running, neither half is live, so the status read makes no YouTube call and
// says it is not live. This is the production gate, not the test seam.
func TestTheVerticalStatusReadAsksTheEngineAndCallsNothingWhileNeitherHalfRuns(t *testing.T) {
	s, h, _, sign, fake, horizID := verticalCreate(t)
	s.verticalHalfLive = nil
	before := len(fake.calls)

	var v verticalPairView
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/destinations/"+itoa(horizID)+"/youtube-vertical",
		nil, http.StatusOK), &v)
	if v.Live {
		t.Fatal("the read reported a half live with nothing running in the engine")
	}
	if len(fake.calls) != before {
		t.Fatalf("the read reached YouTube while neither half ran: %v", fake.calls[before:])
	}
}
