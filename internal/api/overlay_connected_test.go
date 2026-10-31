package api

import (
	"io"
	"net/http"
	"testing"
	"time"
)

// app-council A98: whether an OBS browser source is actually connected.
//
// An overlay draws nothing when it fails -- by design, so a broken key never
// paints an error over somebody's stream -- which made broken look exactly
// like idle from the console. The server already kept every open display
// stream in overlayKicks so it could end them on a key change; the overlay list
// now reports that count, and when one was last connected, so Settings can say
// "Connected" or "Not connected" in words.
//
// Mutation: report 0 from overlayKicker.status. Observed to fail with
// "while a stream was open".
func TestTheOverlayListSaysHowManyDisplayStreamsAreOpenAndWhenOneLastWas(t *testing.T) {
	srv, h, sign, hub := overlayServer(t)
	o := createOverlay(t, h, sign, "All", nil)

	type row struct {
		ID        int64     `json:"id"`
		Connected int       `json:"connected"`
		LastSeen  time.Time `json:"lastSeen"`
	}
	read := func() row {
		t.Helper()
		var out struct {
			Overlays []row `json:"overlays"`
		}
		decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/overlays", nil, http.StatusOK), &out)
		for _, r := range out.Overlays {
			if r.ID == o.ID {
				return r
			}
		}
		t.Fatalf("overlay %d missing from the list", o.ID)
		return row{}
	}

	if r := read(); r.Connected != 0 || !r.LastSeen.IsZero() {
		t.Fatalf("before any source connected: %+v, want 0 and no last-seen", r)
	}

	res := openOverlay(t, srv, "/api/v1/overlay/events", o.OBSKey)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stream: %d", res.StatusCode)
	}
	waitForListener(t, hub)
	if r := read(); r.Connected != 1 || r.LastSeen.IsZero() {
		t.Errorf("while a stream was open: %+v, want 1 connected with a last-seen", r)
	}

	_ = res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	deadline := time.Now().Add(3 * time.Second)
	for {
		r := read()
		if r.Connected == 0 {
			if r.LastSeen.IsZero() {
				t.Error("the source disconnected and the list forgot it was ever connected")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still %d connected 3s after the source went away", r.Connected)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
