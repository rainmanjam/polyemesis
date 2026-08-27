package db

import (
	"strings"
	"testing"
)

// secondProgramme adds a source so a case can tell "held against its own
// programme" from "held against the only programme there is".
//
// Every cross-programme bug is invisible on a single-source install, which is
// every development box and every fixture in this file that does not call this.
func secondProgramme(t *testing.T, d *DB) *Source {
	t.Helper()
	src := &Source{Name: "Studio B", Enabled: true, Ingest: DefaultSettings().Ingest}
	if err := d.CreateSource(src); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	first, err := d.DefaultSourceID()
	if err != nil {
		t.Fatalf("DefaultSourceID: %v", err)
	}
	if src.ID == first {
		t.Fatal("the new source IS the default one, so nothing below can show a " +
			"destination being wired across programmes")
	}
	return src
}

// A DESTINATION MAY NOT SELECT ANOTHER PROGRAMME'S RENDITION.
//
// checkRendition asked only whether the rendition existed, so PUT
// /destinations/4 on source 2 with renditionId 1 (source 1's) was accepted with
// a 200 and no warning. Source 2's engine reconciles against its own
// renditions, found no rendition 1, gave that destination no process, and
// explained itself with "rendition 1 is no longer available" -- a sentence that
// is false, because rendition 1 exists and is encoding under the other
// programme. A live output stopped publishing and the operator was sent looking
// for a deletion that never happened.
//
// Mutation: drop the source comparison from checkRendition (return nil once the
// rendition is found). Observed to fail with "CreateDestination = <nil>, want a
// refusal naming both programmes" and "UpdateDestination = <nil>, want a
// refusal naming both programmes".
func TestADestinationCannotSelectAnotherProgrammesRendition(t *testing.T) {
	d := testDB(t)
	mine := mustCreateRendition(t, d, validRendition())
	other := secondProgramme(t, d)

	// THE CONTROL, and it comes first: a rendition on the destination's OWN
	// programme must still be selectable. A checkRendition that refused
	// everything would satisfy every assertion below and break every install.
	if _, err := d.CreateDestination(func() *Destination {
		dst := validDest()
		dst.Name = "same programme"
		dst.RenditionID = &mine.ID
		return dst
	}()); err != nil {
		t.Fatalf("CreateDestination on the rendition's own programme: %v. The guard is "+
			"refusing the ordinary case, which is every destination on every install", err)
	}

	// A create that reaches across.
	_, err := d.CreateDestination(func() *Destination {
		dst := validDest()
		dst.Name = "across"
		dst.SourceID = &other.ID
		dst.RenditionID = &mine.ID
		return dst
	}())
	if err == nil || !strings.Contains(err.Error(), "belongs to source") {
		t.Errorf("CreateDestination = %v, want a refusal naming both programmes", err)
	}

	// And an update that reaches across, which is the shape the field bug took:
	// the destination is created correctly and re-pointed afterwards.
	dst, err := d.CreateDestination(func() *Destination {
		dst := validDest()
		dst.Name = "studio b passthrough"
		dst.SourceID = &other.ID
		return dst
	}())
	if err != nil {
		t.Fatalf("CreateDestination(passthrough on the second programme): %v", err)
	}
	dst.RenditionID = &mine.ID
	if _, err := d.UpdateDestination(dst); err == nil ||
		!strings.Contains(err.Error(), "belongs to source") {
		t.Errorf("UpdateDestination = %v, want a refusal naming both programmes", err)
	}
}

// A ROW THAT ALREADY REACHES ACROSS STAYS EDITABLE.
//
// The API's update handler decodes the request body OVER the row it just read,
// so a client renaming a destination sends its stored renditionId straight back.
// Holding an update to the same rule as a create would make every pre-existing
// cross-programme row unsaveable: the operator could not rename it, disable it
// or fix its URL, and the refusal would name a field they had not touched.
// #607's trap exactly. So the rule is on the CHANGE, not on the state.
//
// Mutation: pass dst.SourceID to checkRendition unconditionally in
// UpdateDestination (delete the `kept` branch). Observed to fail with "renaming
// a destination that already reaches across was refused".
func TestAnInheritedCrossProgrammeRenditionDoesNotMakeTheRowUnsaveable(t *testing.T) {
	d := testDB(t)
	mine := mustCreateRendition(t, d, validRendition())
	other := secondProgramme(t, d)

	dst, err := d.CreateDestination(func() *Destination {
		dst := validDest()
		dst.Name = "legacy"
		dst.SourceID = &other.ID
		return dst
	}())
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	// Raw SQL because nothing in the store will make this row any more. It is
	// the shape an install upgraded from before the guard is already carrying.
	if _, err := d.SQL().Exec(
		`UPDATE destinations SET rendition_id = ? WHERE id = ?`, mine.ID, dst.ID,
	); err != nil {
		t.Fatalf("plant the inherited pairing: %v", err)
	}
	stored, err := d.GetDestination(dst.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}

	stored.Name = "renamed"
	if _, err := d.UpdateDestination(stored); err != nil {
		t.Fatalf("renaming a destination that already reaches across was refused: %v. "+
			"An operator cannot then correct its URL, disable it, or do anything else "+
			"to it, and the refusal names a field they did not touch", err)
	}

	// THE CONTROL ON THE GRANDFATHER CLAUSE. Inheriting a bad pairing must not
	// license a NEW one -- otherwise the clause is a hole rather than a
	// concession. Moving the row to a third programme is still a change.
	third := &Source{Name: "Studio C", Enabled: true, Ingest: DefaultSettings().Ingest}
	if err := d.CreateSource(third); err != nil {
		t.Fatalf("CreateSource(third): %v", err)
	}
	moved, err := d.GetDestination(dst.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	moved.SourceID = &third.ID
	if _, err := d.UpdateDestination(moved); err == nil ||
		!strings.Contains(err.Error(), "belongs to source") {
		t.Errorf("UpdateDestination = %v, want a refusal: the pairing changed, so the "+
			"grandfather clause must not cover it", err)
	}

	// And clearing the inherited pairing is always allowed, which is the exit
	// the refusal above tells the operator to take.
	fixed, err := d.GetDestination(dst.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	fixed.RenditionID = nil
	if _, err := d.UpdateDestination(fixed); err != nil {
		t.Fatalf("dropping the inherited rendition back to passthrough was refused: %v. "+
			"That is the one repair the refusal points at", err)
	}
}
