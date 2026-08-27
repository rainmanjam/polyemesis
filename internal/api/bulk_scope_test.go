package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// STOP ALL MEANS THIS PROGRAMME, NOT THIS BOX.
//
// /destinations/start-all and /destinations/stop-all carry requireSource, so on
// a two-programme install the request cannot arrive without naming one -- and
// the handler then called s.store.ListDestinations() and acted on every row on
// the machine. The file contained no occurrence of the word "source".
//
// That pairing is the dangerous one. The middleware makes the operator name a
// programme, which is exactly what persuades them the action is confined to it,
// and the response hands back the full list as evidence they were heard. An
// operator on Studio B pressing Stop All ended Studio A's live broadcasts, and
// a completed YouTube broadcast cannot be returned to live.
//
// Mutation: restore s.store.ListDestinations() unconditionally. Observed to
// fail with "stop-all on programme 2 also acted on programme 1's destination".
func TestBulkStartStopActOnlyOnTheNamedProgramme(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	first := s.eng()
	if first == nil {
		t.Fatal("no default engine in the fixture")
	}
	second := secondProgramme(t, s)

	firstID := first.SourceID()
	mine, err := s.store.CreateDestination(&db.Destination{
		Name: "studio a", Kind: db.DestFile, URL: "a.mkv",
		Enabled: true, AudioBitrate: 160, SourceID: &firstID,
	})
	if err != nil {
		t.Fatalf("create on programme 1: %v", err)
	}
	theirs, err := s.store.CreateDestination(&db.Destination{
		Name: "studio b", Kind: db.DestFile, URL: "b.mkv",
		Enabled: true, AudioBitrate: 160, SourceID: &second.ID,
	})
	if err != nil {
		t.Fatalf("create on programme 2: %v", err)
	}

	body := send(t, h, sign, http.MethodPost,
		fmt.Sprintf("/api/v1/destinations/stop-all?source=%d", second.ID),
		map[string]any{"confirm": true}, http.StatusOK)

	var out struct {
		Results []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, body)
	}

	touched := map[int64]bool{}
	for _, r := range out.Results {
		touched[r.ID] = true
	}
	if touched[mine.ID] {
		t.Errorf("stop-all on programme %d also acted on programme %d's destination %q. "+
			"An operator stopping one show stopped another, and a completed broadcast "+
			"does not come back.", second.ID, firstID, mine.Name)
	}
	// The control: an implementation that acted on NOTHING would satisfy the
	// assertion above while being just as broken.
	if !touched[theirs.ID] {
		t.Errorf("stop-all on programme %d did not act on its own destination %q, so the "+
			"assertion above proves nothing", second.ID, theirs.Name)
	}
}
