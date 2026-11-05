package db

import (
	"errors"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// UpdateDestinationMixes writes the two mix columns of several destinations, all
// of them or none, from the rows as the transaction reads them.
//
// MUTATION: drop the refusal check before the writes (commit what passed).
// Observed: FAIL, err = <nil>, want the refusal.
// MUTATION: write name, enabled and url along with the mixes. Observed: FAIL, "a
// column other than the mixes was written".
func TestUpdateDestinationMixesWritesOnlyTheMixesAndAllOrNone(t *testing.T) {
	d := testDB(t)
	mk := func(name string) *Destination {
		t.Helper()
		dst := validDest()
		dst.Name = name
		out, err := d.CreateDestination(dst)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	one, two, three := mk("one"), mk("two"), mk("three")
	gain := func(id int64) float64 {
		t.Helper()
		got, err := d.GetDestination(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range got.Profile.Tracks {
			if s.Track == 0 {
				return s.Gain
			}
		}
		t.Fatalf("destination %d has no track 0", id)
		return 0
	}
	setGain := func(dst *Destination, g float64) {
		for i := range dst.Profile.Tracks {
			if dst.Profile.Tracks[i].Track == 0 {
				dst.Profile.Tracks[i].Gain = g
			}
		}
	}

	// A refusal on the second row: nothing is stored, the first row included.
	refusal := errors.New("two cannot take it")
	_, _, err := d.UpdateDestinationMixes([]int64{one.ID, two.ID}, func(cur *Destination) error {
		setGain(cur, 0.5)
		if cur.ID == two.ID {
			return refusal
		}
		return nil
	})
	if !errors.Is(err, refusal) {
		t.Fatalf("err = %v, want the refusal", err)
	}
	if g := gain(one.ID); g != 1 {
		t.Fatalf("a refused write stored the first row's mix (gain %v)", g)
	}

	// Only the mixes: everything else apply sets is discarded, and a row is
	// read as it stands now -- here, renamed after the caller's listing.
	renamed, err := d.GetDestination(one.ID)
	if err != nil {
		t.Fatal(err)
	}
	renamed.Name = "renamed"
	if _, err := d.UpdateDestination(renamed); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteDestination(three.ID); err != nil {
		t.Fatal(err)
	}
	vod := routing.DefaultProfile()
	written, missing, err := d.UpdateDestinationMixes([]int64{one.ID, two.ID, three.ID}, func(cur *Destination) error {
		if cur.Name != "renamed" && cur.ID == one.ID {
			t.Errorf("apply was handed a stale row: %q", cur.Name)
		}
		if cur.ID == two.ID {
			return ErrMixSkipped
		}
		setGain(cur, 0.5)
		cur.VODProfile = &vod
		cur.Enabled = true
		cur.Name = "not a mix"
		cur.URL = "rtmp://elsewhere.example/live"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 || written[0].ID != one.ID {
		t.Fatalf("written = %v, want destination one alone", written)
	}
	if len(missing) != 1 || missing[0] != three.ID {
		t.Fatalf("missing = %v, want the deleted destination", missing)
	}
	got, err := d.GetDestination(one.ID)
	if err != nil {
		t.Fatal(err)
	}
	if g := gain(one.ID); g != 0.5 || got.VODProfile == nil {
		t.Fatalf("the mixes were not stored: gain %v, second mix %v", g, got.VODProfile)
	}
	if got.Name != "renamed" || got.Enabled || got.URL != "rtmp://ingest.example/live" {
		t.Fatalf("a column other than the mixes was written: name %q enabled %v url %q", got.Name, got.Enabled, got.URL)
	}
	if g := gain(two.ID); g != 1 {
		t.Fatalf("a skipped row was written (gain %v)", g)
	}

	// The row is checked as UpdateDestination checks it.
	_, _, err = d.UpdateDestinationMixes([]int64{two.ID}, func(cur *Destination) error {
		cur.Profile = routing.Profile{Mode: routing.ModeSimple}
		return nil
	})
	if err == nil {
		t.Fatal("a mix that selects nothing was stored")
	}
}
