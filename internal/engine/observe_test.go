package engine

import "testing"

// The gate on the sweep, extracted so it is testable without an engine.
//
// The failure it guards is specific and silent: alertLoop used to skip the
// whole sweep when no ALERT rules existed. A hook is a second consumer of the
// same snapshot, so an install with hooks configured and no alert rules would
// have built no snapshot, observed no transitions, and fired nothing -- with a
// perfectly healthy hook listed as enabled in the UI.
func TestObserveWanted(t *testing.T) {
	tests := []struct {
		name          string
		alerts, hooks bool
		want          bool
	}{
		{"neither", false, false, false},
		{"alerts only", true, false, true},
		{"hooks only", false, true, true},
		{"both", true, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := observeWanted(tc.alerts, tc.hooks); got != tc.want {
				t.Fatalf("observeWanted(%v, %v) = %v, want %v",
					tc.alerts, tc.hooks, got, tc.want)
			}
		})
	}
}
