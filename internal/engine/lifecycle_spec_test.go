package engine

// The one property that lets a sweep write to a LIVE destination.
//
// internal/api/preannounce.go refuses to touch an enabled destination, and says
// why in as many words: it writes the stream key, the stream key is inside
// Target(), Target() is the first element of destSpec, and changing it under a
// running FFmpeg cycles the process at a moment nobody chose. The broadcast
// lifecycle coordinator writes to enabled destinations constantly -- every
// confirmed phase, every retry count -- and it is allowed to because the column
// it writes reaches no FFmpeg argument at all.
//
// That is a claim about a hash, so it is pinned with the hash. Nothing here
// asserts intent; it mutates every field of db.BroadcastControl and requires
// both spec signatures to come back byte for byte identical.

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// TestEditingBroadcastLifecycleBookkeepingWouldNotRestartALiveDestination is
// named for what a failure COSTS rather than for what it is.
//
// If this fails, the lifecycle block has reached ffmpeg.DestSpec, and the
// consequence is not a stale hash: it is that recording "YouTube says this
// broadcast is now live" -- which happens seconds after an operator goes on air,
// on a destination that is delivering -- tears the destination down and brings
// it back. The operator sees their stream drop at the exact moment it started
// working, and nothing in the logs blames the thing that did it.
func TestEditingBroadcastLifecycleBookkeepingWouldNotRestartALiveDestination(t *testing.T) {
	compiled := routing.Result{FilterComplex: "[0:a:0]anull[out]", OutLabel: "[out]"}
	basePrimary := destSpec(testDestination(7, nil), compiled, "")
	baseBackup := backupSpecOf(testDestination(7, nil), compiled, "")

	// One case per field, plus the whole block at once. Per field because a
	// partial leak -- one member of the struct reaching the argv builder -- is
	// exactly what a single "set everything" case would still catch but would
	// not localise.
	tests := []struct {
		name   string
		mutate func(*db.Destination)
	}{
		{"recording which broadcast this is", func(d *db.Destination) {
			d.Lifecycle.BroadcastID = "kJ8x-broadcast-id"
		}},
		{"recording the phase the platform reported", func(d *db.Destination) {
			d.Lifecycle.Phase = "live"
		}},
		{"counting a failed transition", func(d *db.Destination) {
			d.Lifecycle.Attempts = 17
		}},
		{"recording a fault for the operator to read", func(d *db.Destination) {
			d.Lifecycle.Fault = "the channel already has the maximum number of concurrent " +
				"live broadcasts"
		}},
		{"a whole block written at once, as a real sweep writes it", func(d *db.Destination) {
			d.Lifecycle = db.BroadcastControl{
				BroadcastID: "kJ8x-broadcast-id", Phase: "testing",
				Attempts: 3, Fault: "the platform is asking us to slow down",
			}
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := testDestination(7, nil)
			tc.mutate(row)
			if got := destSpec(row, compiled, ""); got != basePrimary {
				t.Errorf("the primary spec changed: %s would restart a live destination, "+
					"and the operator's stream drops at the moment their broadcast starts "+
					"working", tc.name)
			}
			if got := backupSpecOf(row, compiled, ""); got != baseBackup {
				t.Errorf("the backup spec changed: %s would cycle the redundant feed", tc.name)
			}
		})
	}
}
