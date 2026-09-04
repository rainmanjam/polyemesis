package engine

import (
	"log/slog"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// A DESTINATION WHOSE HUB IS SWAPPED UNDER IT MUST BE RESTARTED. #674
//
// destSpec hashes the argv with the relay URL BLANKED, on the reasoning that
// the URL is an implementation detail of the tier. The consequence was that a
// hub replacement did not change the spec, so stopDestinations KEPT the
// destination -- holding a subscription to a hub nobody feeds, on a port that
// is never written to again. Its FFmpeg took the relay URL on its command line
// and cannot be told a new one, so a restart is the only repair.
//
// Measured before the fix: the child execed 1ms after the engine started it and
// got its first byte 72.8 seconds later, five seconds before it exited.
func TestADestinationIsRestartedWhenItsHubIsReplaced(t *testing.T) {
	oldHub, err := relay.New(slog.New(slog.DiscardHandler), 0)
	if err != nil {
		t.Fatalf("old hub: %v", err)
	}
	defer oldHub.Close()

	// Running against oldHub, with a spec that has NOT changed.
	d := &destination{
		row:  &db.Destination{ID: 1, Name: "R-track2", Kind: db.DestRTMP},
		proc: &supervisor.Process{},
		hub:  oldHub,
		spec: "unchanged",
	}
	if sameHub := d.hub == oldHub; !sameHub {
		t.Fatal("fixture is wrong: the destination should start on oldHub")
	}

	// The engine swaps the tier: a NEW hub, same spec.
	newHub, err := relay.New(slog.New(slog.DiscardHandler), 0)
	if err != nil {
		t.Fatalf("new hub: %v", err)
	}
	defer newHub.Close()

	// THE REAL DECISION, not a copy of it.
	if keep := keepDestination(d, destPlan{spec: "unchanged"}, true, newHub); keep {
		t.Fatal("kept a destination whose hub was replaced under it. Its FFmpeg took " +
			"the relay URL on its command line and cannot be told a new one, so a " +
			"restart is the only repair. #674.")
	}
}

// The converse: an unchanged hub must NOT cause a restart, or every reconcile
// cycles every destination and the failover suite's zero-restart pin breaks.
func TestADestinationOnTheSameHubIsLeftAlone(t *testing.T) {
	h, err := relay.New(slog.New(slog.DiscardHandler), 0)
	if err != nil {
		t.Fatalf("hub: %v", err)
	}
	defer h.Close()

	d := &destination{
		row:  &db.Destination{ID: 1, Name: "A", Kind: db.DestFile},
		proc: &supervisor.Process{},
		hub:  h,
		spec: "unchanged",
	}
	if keep := keepDestination(d, destPlan{spec: "unchanged"}, true, h); !keep {
		t.Fatal("restarted a destination whose hub did NOT change.\n\n" +
			"acceptance-failover pins zero restarts across a switch, and a restart " +
			"splits a recording across files. The hub check must be an equality test, " +
			"not a liveness test.")
	}
}
