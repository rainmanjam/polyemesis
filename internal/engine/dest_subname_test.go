package engine

import "testing"

// The bug this exists for: both of a destination's outputs registered under one
// name, so the primary silently receives nothing while looking entirely
// healthy.
//
// relay.Hub.Subscribe is a map assignment keyed by the name, so a collision is
// a REPLACEMENT. The replaced FFmpeg keeps running with a correct command line
// and a correct target URL, and the card shows it up. Every obvious assertion
// -- two processes exist, their targets differ -- passes in that state, which
// is what makes it worth its own guard.
func TestEachOutputOfADestinationGetsItsOwnSubscriberName(t *testing.T) {
	primary := destSubName(7, "")
	backup := destSubName(7, destRoleBackup)

	if primary == backup {
		t.Fatalf("both outputs subscribe as %q; the second would replace the "+
			"first on the hub and starve it", primary)
	}
	// The primary's name is load-bearing history: changing it would move every
	// existing subscription for no reason.
	if primary != "dest:7" {
		t.Errorf("the primary's subscriber name changed to %q, want dest:7", primary)
	}
	if backup != "dest:7:backup" {
		t.Errorf("backup subscriber name = %q, want dest:7:backup", backup)
	}
}

// Two destinations must not collide either, which is the property the id was
// always carrying.
func TestSubscriberNamesAreDistinctAcrossDestinations(t *testing.T) {
	seen := map[string]string{}
	for _, id := range []int64{1, 2, 7} {
		for _, role := range []string{"", destRoleBackup} {
			n := destSubName(id, role)
			if prev, dup := seen[n]; dup {
				t.Fatalf("%q is produced by both %s and id=%d role=%q", n, prev, id, role)
			}
			seen[n] = "id=" + string(rune('0'+id)) + " role=" + role
		}
	}
}
