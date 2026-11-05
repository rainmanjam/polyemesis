package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/obsbridge"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// #10 through the API: the delete says which schedules it changed, and says
// nothing at all when it changed none.
func TestADestinationDeleteSaysWhichSchedulesItSwitchedOff(t *testing.T) {
	_, h, store, sign := engineServer(t, defaultTools(), Options{})
	a := createDestination(t, h, sign, destinationBody("A", false, nil))
	b := createDestination(t, h, sign, destinationBody("B", false, nil))
	lonely := createDestination(t, h, sign, destinationBody("lonely", false, nil))
	for _, sc := range []*scheduler.Schedule{
		{Name: "stop A", Enabled: true, Action: scheduler.ActionStop, Kind: scheduler.KindDaily, TZ: "UTC",
			AtMinutes: 60, DestinationIDs: []int64{a.ID}},
		{Name: "start A and B", Enabled: true, Action: scheduler.ActionStart, Kind: scheduler.KindDaily, TZ: "UTC",
			AtMinutes: 120, DestinationIDs: []int64{a.ID, b.ID}},
	} {
		if _, err := store.CreateSchedule(sc); err != nil {
			t.Fatal(err)
		}
	}

	var got deleteReply
	decodeInto(t, send(t, h, sign, http.MethodDelete, "/api/v1/destinations/"+itoa(a.ID), nil, http.StatusOK), &got)
	joined := strings.Join(got.Warnings, "\n")
	if !strings.Contains(joined, `"stop A"`) || !strings.Contains(joined, "switched off") {
		t.Errorf("warnings %q do not say the stop schedule was switched off", got.Warnings)
	}
	if !strings.Contains(joined, `"start A and B"`) {
		t.Errorf("warnings %q do not name the schedule that lost a destination", got.Warnings)
	}
	scheds, err := store.ListSchedules()
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range scheds {
		if slices.Contains(sc.DestinationIDs, a.ID) && sc.Enabled {
			t.Errorf("schedule %q still fires at the deleted destination", sc.Name)
		}
	}

	// A delete no schedule cared about answers exactly as it always has.
	raw := send(t, h, sign, http.MethodDelete, "/api/v1/destinations/"+itoa(lonely.ID), nil, http.StatusOK)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body["status"] != "deleted" {
		t.Errorf("an ordinary delete answered %s, want exactly {\"status\":\"deleted\"}", raw)
	}
}

// #5: the OBS bridge document keeps no dead ids, and never widens a rule.
func TestDeletingADestinationPrunesTheOBSBridgeWithoutWideningARule(t *testing.T) {
	_, h, _, sign := engineServer(t, defaultTools(), Options{})
	d1 := createDestination(t, h, sign, destinationBody("one", false, nil))
	d2 := createDestination(t, h, sign, destinationBody("two", false, nil))
	send(t, h, sign, http.MethodPut, "/api/v1/obs", map[string]any{
		"destinations": []int64{d1.ID, d2.ID},
		"scenes": []map[string]any{
			{"scene": "Wide", "preset": "everything", "destinations": []int64{d1.ID, d2.ID}},
			{"scene": "Only one", "preset": "everything", "destinations": []int64{d1.ID}},
			{"scene": "All", "preset": "everything"},
		},
	}, http.StatusOK)

	send(t, h, sign, http.MethodDelete, "/api/v1/destinations/"+itoa(d1.ID), nil, http.StatusOK)

	var got obsConfigPayload
	raw := send(t, h, sign, http.MethodGet, "/api/v1/obs", nil, http.StatusOK)
	decodeInto(t, raw, &got)
	if !slices.Equal(got.Config.Destinations, []int64{d2.ID}) {
		t.Errorf("bridge destinations %v, want only [%d]", got.Config.Destinations, d2.ID)
	}
	rules := map[string]obsbridge.SceneRule{}
	for _, r := range got.Config.Scenes {
		rules[r.Scene] = r
	}
	if r, ok := rules["Wide"]; !ok || !slices.Equal(r.Destinations, []int64{d2.ID}) {
		t.Errorf("the two-destination rule became %+v, want it narrowed to [%d]", r, d2.ID)
	}
	if r, ok := rules["Only one"]; ok {
		t.Errorf("the rule aimed only at the deleted destination survived as %+v; with an empty list it "+
			"would now drive EVERY destination", r)
	}
	if _, ok := rules["All"]; !ok {
		t.Error("the every-destination rule was removed")
	}

	// And the page's own save goes through again.
	var doc map[string]any
	decodeInto(t, raw, &doc)
	send(t, h, sign, http.MethodPut, "/api/v1/obs", doc["config"], http.StatusOK)
}

// The programme path, and the boot prune that heals an install already stuck.
func TestDeletingTheBridgesProgrammeClearsItAndBootHealsAStuckDocument(t *testing.T) {
	s, h, store, sign := engineServer(t, defaultTools(), Options{})
	src := secondProgramme(t, s)
	d3 := createDestination(t, h, sign, map[string]any{"name": "three", "kind": "rtmp", "platform": "custom",
		"url": "rtmp://example.com/live", "streamKey": "abc", "sourceId": src.ID})
	send(t, h, sign, http.MethodPut, "/api/v1/obs", map[string]any{
		"sourceId": src.ID, "destinations": []int64{d3.ID},
		"scenes": []map[string]any{{"scene": "S", "preset": "everything"}},
	}, http.StatusOK)
	send(t, h, sign, http.MethodDelete, "/api/v1/sources/"+itoa(src.ID),
		map[string]any{"confirm": true, "destinations": 1}, http.StatusNoContent)

	var got obsConfigPayload
	raw := send(t, h, sign, http.MethodGet, "/api/v1/obs", nil, http.StatusOK)
	decodeInto(t, raw, &got)
	if got.Config.SourceID != nil || len(got.Config.Destinations) != 0 || len(got.Config.Scenes) != 0 {
		t.Fatalf("after deleting its programme the bridge still reads %+v", got.Config)
	}
	var doc map[string]any
	decodeInto(t, raw, &doc)
	send(t, h, sign, http.MethodPut, "/api/v1/obs", doc["config"], http.StatusOK)

	// Stuck before this release: written behind the API's back, as an older
	// delete left it. The boot call cleans it.
	stale, _ := json.Marshal(obsbridge.Config{Destinations: []int64{d3.ID}})
	if err := store.PutOBSConfig(stale); err != nil {
		t.Fatal(err)
	}
	s.PruneOBSReferences()
	cfg, err := loadOBSConfig(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Destinations) != 0 {
		t.Errorf("the boot prune left %v", cfg.Destinations)
	}
}

// The refusal for a programme that does not exist is the sentence written for
// it, not a bare 404 "source not found".
func TestAnOBSSaveNamingADeadProgrammeSaysSo(t *testing.T) {
	_, h, _, sign := engineServer(t, defaultTools(), Options{})
	raw := send(t, h, sign, http.MethodPut, "/api/v1/obs", map[string]any{"sourceId": 9999}, http.StatusBadRequest)
	if !strings.Contains(string(raw), "programme 9999 does not exist") {
		t.Errorf("refusal %s does not say the programme does not exist", raw)
	}
}

func TestPruneOBSConfigTable(t *testing.T) {
	id := func(n int64) *int64 { return &n }
	live := map[int64]bool{1: true, 2: true}
	src := map[int64]bool{10: true}
	for _, tc := range []struct {
		name    string
		in      obsbridge.Config
		want    obsbridge.Config
		changed bool
	}{
		{"nothing dead", obsbridge.Config{SourceID: id(10), Destinations: []int64{1, 2}},
			obsbridge.Config{SourceID: id(10), Destinations: []int64{1, 2}}, false},
		{"dead programme clears everything",
			obsbridge.Config{SourceID: id(11), Destinations: []int64{1}, Scenes: []obsbridge.SceneRule{{Scene: "x"}}},
			obsbridge.Config{}, true},
		{"partial rule narrows", obsbridge.Config{Destinations: []int64{1, 3},
			Scenes: []obsbridge.SceneRule{{Scene: "a", Destinations: []int64{1, 3}}}},
			obsbridge.Config{Destinations: []int64{1}, Scenes: []obsbridge.SceneRule{{Scene: "a", Destinations: []int64{1}}}}, true},
		{"emptied rule goes", obsbridge.Config{Destinations: []int64{1, 3},
			Scenes: []obsbridge.SceneRule{{Scene: "a", Destinations: []int64{3}}, {Scene: "b"}}},
			obsbridge.Config{Destinations: []int64{1}, Scenes: []obsbridge.SceneRule{{Scene: "b"}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, changed := pruneOBSConfig(tc.in, live, src)
			if changed != tc.changed {
				t.Fatalf("changed=%v, want %v", changed, tc.changed)
			}
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(tc.want)
			if string(a) != string(b) {
				t.Errorf("got %s, want %s", a, b)
			}
		})
	}
}
