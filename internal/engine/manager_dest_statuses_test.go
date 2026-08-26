package engine

import (
	"context"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// DestinationStatuses is the whole-install view, and it has to be assembled
// from each engine's own answer rather than from any one of them.
//
// Engine.Status is scoped to its own source on purpose, so that a destination
// is never described using another programme's track count. Every caller that
// wants the BOX -- the dashboard's grouped list, the Prometheus scrape, the
// status pushed over the WebSocket -- was reading the default engine instead,
// and got the whole install only because that engine's read was unscoped. When
// that was fixed those callers silently lost every programme but one.
//
// This lives in the engine package because the api tests that exercise the same
// path cannot cover these lines: `go test ./...` without -coverpkg credits a
// package only for statements its OWN tests execute, so a method used entirely
// from internal/api reads as dead code in a coverage report while being on the
// hottest path in the product.
//
// Mutation: return only m.Default().Status().Destinations. Observed to fail
// with "programme 2's destination is missing".
func TestDestinationStatusesCoversEveryProgramme(t *testing.T) {
	m, store := managerFixture(t)

	first := addSource(t, store, "first programme")
	second := addSource(t, store, "second programme")
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := len(m.Engines()); got < 2 {
		t.Fatalf("the manager runs %d engine(s); this test needs two or it asserts nothing", got)
	}

	mine, err := store.CreateDestination(&db.Destination{
		Name: "on the first", Kind: db.DestFile, URL: "a.mkv",
		Enabled: false, AudioBitrate: 160, SourceID: &first.ID,
	})
	if err != nil {
		t.Fatalf("CreateDestination(first): %v", err)
	}
	foreign, err := store.CreateDestination(&db.Destination{
		Name: "on the second", Kind: db.DestFile, URL: "b.mkv",
		Enabled: false, AudioBitrate: 160, SourceID: &second.ID,
	})
	if err != nil {
		t.Fatalf("CreateDestination(second): %v", err)
	}

	seen := map[int64]bool{}
	for _, d := range m.DestinationStatuses() {
		seen[d.ID] = true
	}
	if !seen[foreign.ID] {
		t.Error("programme 2's destination is missing, so the dashboard shows that " +
			"programme empty and Prometheus emits no series for it")
	}
	// The control. An empty result, or one that dropped the first programme
	// instead, would otherwise satisfy the assertion above.
	if !seen[mine.ID] {
		t.Fatal("programme 1's destination is missing, so the assertion above proves nothing")
	}
}
