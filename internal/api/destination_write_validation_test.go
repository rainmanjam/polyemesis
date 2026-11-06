package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
)

// POST refused a destination that would carry no audio and PUT saved it: the
// silence guard ran on create alone. One validation now serves every path that
// writes a routing profile.
//
// Mutation: drop the profile check from handleUpdateDestination.
func TestPutRefusesASilentProfileThatPostRefuses(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	second := secondProgramme(t, s)
	send(t, h, sign, http.MethodPut,
		"/api/v1/source/annotations?source="+itoa(second.ID),
		map[string]any{"annotations": []map[string]any{
			{"track": 0, "label": "music bus", "role": "music"},
		}}, http.StatusOK)

	silent := map[string]any{
		"mode": "simple", "normalize": "auto", "sampleRate": 48000,
		"excludeRoles": []string{"music"},
		"tracks":       []map[string]any{{"track": 0, "enabled": true, "gain": 1.0}},
	}
	create := map[string]any{
		"name": "studio b", "kind": "rtmp", "platform": "custom",
		"url": "rtmp://example.invalid/live", "streamKey": "k", "sourceId": second.ID,
	}
	var created struct {
		Destination struct {
			ID int64 `json:"id"`
		} `json:"destination"`
	}
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/destinations", create, http.StatusCreated), &created)

	r := jsonRequest(t, http.MethodPut, "/api/v1/destinations/"+itoa(created.Destination.ID),
		map[string]any{"profile": silent})
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "no audio") {
		t.Fatalf("PUT of a profile POST refuses: status %d, body %s", w.Code, w.Body.String())
	}
}

// Deleting a destination cuts its buffered broadcast delay (#11). Deleting its
// programme removes the same destinations by cascade and must do the same, or
// the tail airs on after the delete.
//
// Mutation: drop the cut step from the source delete.
func TestDeletingAProgrammeCutsItsDelayedDestinationsLikeDeletingThem(t *testing.T) {
	s, h, sign, ids := delayServer(t, 1)
	id := ids[0]
	if phaseOf(s, id) != engine.DelayBuffering {
		t.Fatal("setup: not buffering")
	}
	src := onlySourceID(t, h, sign)
	del := jsonRequest(t, http.MethodDelete, "/api/v1/sources/"+itoa(src),
		map[string]any{"confirm": true, "destinations": 1})
	sign(del)
	if w := do(t, h, del); w.Code != http.StatusOK && w.Code != http.StatusNoContent {
		t.Fatalf("delete source: %d %s", w.Code, w.Body.String())
	}
	if p := phaseOf(s, id); p != "" {
		t.Fatalf("a destination deleted with its programme is still airing its tail (phase %q)", p)
	}
}

// Deleting a destination says when it strands its YouTube ingest stream.
// Deleting the programme takes the same destinations by cascade and said
// nothing.
//
// Mutation: drop the note from the shared teardown.
func TestDeletingAProgrammeNamesTheYouTubeStreamsItStrands(t *testing.T) {
	s, h, logs, sign := managerServer(t, defaultTools())
	acct := connectAccount(t, s.store, s.box, db.PlatformYouTube, "night-owl")
	ytDest(t, s.store, acct, "First show", "key-abc")
	ytDest(t, s.store, acct, "Second show", "key-own")
	src, err := s.store.DefaultSourceID()
	if err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	del := jsonRequest(t, http.MethodDelete, "/api/v1/sources/"+itoa(src),
		map[string]any{"confirm": true, "destinations": 2})
	sign(del)
	if w := do(t, h, del); w.Code != http.StatusOK && w.Code != http.StatusNoContent {
		t.Fatalf("delete source: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(logs.String(), "YouTube ingest stream on the channel") {
		t.Fatalf("deleting the programme stranded Second show's stream without saying so: %s", logs.String())
	}
}
