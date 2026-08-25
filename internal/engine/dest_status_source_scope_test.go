package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// Status() must report only the destinations belonging to ITS OWN programme.
//
// The read was ListDestinations() -- every row on the box -- while the compile
// beside it uses e.Source(), this engine's measured layout. On a multi-source
// install that pairs one programme's profile with another's track count, and
// the operator is shown a "track N not present" warning on a destination that
// is correctly configured. Nothing fails at the time; the damage is that the
// natural response is to "fix" routing that was already right.
//
// Driven through e.Status() rather than the store call, because a test of
// ListDestinationsBySource alone would pass while status.go went on reading
// every row -- the same shape of test this package has been burned by before.
//
// Mutation: restore `e.store.ListDestinations()` in status.go. Observed to fail
// with "carries a destination belonging to another programme: other-programme".
func TestStatusReportsOnlyItsOwnProgrammesDestinations(t *testing.T) {
	e, store := storeEngine(t)

	// The control. Without a destination that SHOULD appear, a Status that
	// returned an empty list would satisfy the assertion below and this test
	// would pass while reporting nothing at all.
	mine, err := store.CreateDestination(&db.Destination{
		Name: "mine", Kind: db.DestFile, URL: "mine.mkv", Enabled: false,
		AudioBitrate: 160, Profile: routing.DefaultProfile(),
	})
	if err != nil {
		t.Fatalf("CreateDestination(mine): %v", err)
	}

	other := &db.Source{Name: "other", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := store.CreateSource(other); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	foreign, err := store.CreateDestination(&db.Destination{
		Name: "other-programme", Kind: db.DestFile, URL: "other.mkv", Enabled: false,
		AudioBitrate: 160, Profile: routing.DefaultProfile(),
		SourceID: &other.ID,
	})
	if err != nil {
		t.Fatalf("CreateDestination(foreign): %v", err)
	}

	var sawMine bool
	for _, ds := range e.Status().Destinations {
		if ds.ID == foreign.ID {
			t.Errorf("Status() carries a destination belonging to another programme: %s", ds.Name)
		}
		if ds.ID == mine.ID {
			sawMine = true
		}
	}
	if !sawMine {
		t.Fatal("Status() dropped this programme's own destination, so the assertion above proved nothing")
	}
}
