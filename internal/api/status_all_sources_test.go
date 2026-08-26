package api

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The status payload describes the BOX, so its destination list must carry
// every programme's destinations -- not the default engine's.
//
// Engine.Status is scoped to its own source on purpose (#515): it compiles each
// destination against the layout of the programme that owns it, so a 2-track
// programme's destination is never described using a 6-track one's count.
// Before that fix the default engine's status happened to carry every row on
// the machine, and three callers were relying on the leak: the dashboard's
// grouped destination list, the Prometheus scrape, and the WebSocket push.
//
// The metrics case is the one that justifies a test rather than a comment. A
// scrape covering only the default programme does not fail -- the series for
// every other destination simply stops existing, which looks exactly like a
// destination nobody configured, so an alert that should fire on a dead
// destination never evaluates at all.
//
// Mutation: return s.eng().Status() from statusPayload without replacing
// Destinations. Observed to fail with "destination on programme 2 is missing".
func TestStatusPayloadCarriesEveryProgrammesDestinations(t *testing.T) {
	s, _, _, _ := managerServer(t, defaultTools())
	store := s.store

	mine, err := store.CreateDestination(&db.Destination{
		Name: "on the default programme", Kind: db.DestFile, URL: "a.mkv",
		Enabled: false, AudioBitrate: 160,
	})
	if err != nil {
		t.Fatalf("CreateDestination(default): %v", err)
	}

	other := &db.Source{Name: "second programme", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := store.CreateSource(other); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if err := s.mgr.Sync(); err != nil {
		t.Fatalf("sync after creating a second source: %v", err)
	}
	if got := len(s.mgr.Engines()); got < 2 {
		t.Fatalf("the manager runs %d engine(s); this test needs two or it asserts nothing", got)
	}
	foreign, err := store.CreateDestination(&db.Destination{
		Name: "on the second programme", Kind: db.DestFile, URL: "b.mkv",
		Enabled: false, AudioBitrate: 160, SourceID: &other.ID,
	})
	if err != nil {
		t.Fatalf("CreateDestination(second): %v", err)
	}

	seen := map[int64]bool{}
	for _, d := range s.statusPayload().Destinations {
		seen[d.ID] = true
	}
	if !seen[foreign.ID] {
		t.Error("destination on programme 2 is missing from the status payload. " +
			"The dashboard shows that programme as empty and Prometheus stops " +
			"emitting a series for it, which reads as a destination nobody made.")
	}
	// The control: without it, a payload that returned every destination in the
	// database regardless of engine would pass, and so would an empty one.
	if !seen[mine.ID] {
		t.Fatal("the default programme's own destination is missing, so the assertion above proves nothing")
	}
}
