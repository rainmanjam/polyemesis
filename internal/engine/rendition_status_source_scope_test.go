package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// Renditions() must report only the encodes belonging to ITS OWN programme.
//
// The read was ListRenditions() -- every rendition on the box -- ten lines from
// reconcileOutputs, which has always used ListRenditionsBySource(e.sourceID).
// So GET /status?source=1 listed a rendition whose sourceId is 2 while
// GET /sources correctly reported source 1 had none, and an operator deciding
// which encode to change was reading a card for a programme they were not
// looking at. #543 passed the engine into statusPayload and did not close this,
// because the engine it passed in was the thing answering install-wide.
//
// Driven through e.Renditions() rather than the store call, because a test of
// ListRenditionsBySource alone would pass while status.go went on reading every
// row -- the same shape of test this package has been burned by before.
//
// Mutation: restore `e.store.ListRenditions()` in status.go. Observed to fail
// with "Renditions() carries a rendition belonging to another programme:
// other-programme".
func TestRenditionsReportOnlyItsOwnProgrammesEncodes(t *testing.T) {
	e, store := storeEngine(t)

	// The control. Without a rendition that SHOULD appear, a Renditions() that
	// returned an empty list would satisfy the assertion below and this test
	// would pass while reporting nothing at all.
	mine, err := store.CreateRendition(&db.Rendition{Name: "mine", Height: 720, VideoBitrate: 4000})
	if err != nil {
		t.Fatalf("CreateRendition(mine): %v", err)
	}

	other := &db.Source{Name: "other", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := store.CreateSource(other); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	foreign, err := store.CreateRendition(&db.Rendition{
		Name: "other-programme", Height: 480, VideoBitrate: 1500, SourceID: &other.ID,
	})
	if err != nil {
		t.Fatalf("CreateRendition(foreign): %v", err)
	}

	var sawMine bool
	for _, rs := range e.Renditions() {
		if rs.ID == foreign.ID {
			t.Errorf("Renditions() carries a rendition belonging to another programme: %s", rs.Name)
		}
		if rs.ID == mine.ID {
			sawMine = true
		}
	}
	if !sawMine {
		t.Fatal("Renditions() dropped this programme's own rendition, so the assertion " +
			"above proved nothing")
	}
}

// The consumer count on a card must be this programme's consumers.
//
// CountEnabledDestinationsByRendition is install-wide, and the fold above it
// ("the same fold reconcileOutputs does") made the figure look deliberate. A
// destination on another programme that had been wired to this rendition -- the
// pairing checkRendition now refuses -- was added to this card's total, so the
// card claimed a consumer whose bytes this engine does not carry, and the
// operator's read of "is anything using this encode" was wrong in the direction
// that keeps an encode alive.
//
// The foreign row is written with raw SQL on purpose: CreateDestination now
// refuses to make one, and the count still has to be right for the rows that
// pre-date that refusal, which are exactly the rows the grandfather clause in
// UpdateDestination keeps saveable.
//
// Mutation: restore `e.store.CountEnabledDestinationsByRendition()` in
// status.go. Observed to fail with "Consumers = 2, want 1".
func TestRenditionConsumerCountIsThisProgrammesConsumers(t *testing.T) {
	e, store := storeEngine(t)

	rend, err := store.CreateRendition(&db.Rendition{Name: "shared", Height: 720, VideoBitrate: 4000})
	if err != nil {
		t.Fatalf("CreateRendition: %v", err)
	}
	// The control: a real consumer on this programme. Without it a scoped count
	// that answered zero for everything would pass.
	if _, err := store.CreateDestination(&db.Destination{
		Name: "mine", Kind: db.DestFile, URL: "mine.mkv", Enabled: true,
		AudioBitrate: 160, Profile: routing.DefaultProfile(), RenditionID: &rend.ID,
	}); err != nil {
		t.Fatalf("CreateDestination(mine): %v", err)
	}

	other := &db.Source{Name: "other", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := store.CreateSource(other); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	foreign, err := store.CreateDestination(&db.Destination{
		Name: "other-programme", Kind: db.DestFile, URL: "other.mkv", Enabled: true,
		AudioBitrate: 160, Profile: routing.DefaultProfile(), SourceID: &other.ID,
	})
	if err != nil {
		t.Fatalf("CreateDestination(foreign): %v", err)
	}
	if _, err := store.SQL().Exec(
		`UPDATE destinations SET rendition_id = ? WHERE id = ?`, rend.ID, foreign.ID,
	); err != nil {
		t.Fatalf("wire the foreign destination to this rendition: %v", err)
	}

	var found bool
	for _, rs := range e.Renditions() {
		if rs.ID != rend.ID {
			continue
		}
		found = true
		if rs.Consumers != 1 {
			t.Errorf("Consumers = %d, want 1: the card is counting a destination on "+
				"another programme, which this engine does not carry", rs.Consumers)
		}
	}
	if !found {
		t.Fatal("Renditions() did not report this programme's own rendition at all, so " +
			"the count above was never examined")
	}
}
