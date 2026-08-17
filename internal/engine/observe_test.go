package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/hooks"
)

// The gate on the sweep, extracted so it is testable without an engine.
//
// The failure it guards is specific and silent: alertLoop used to skip the
// whole sweep when no ALERT rules existed. A hook is a second consumer of the
// same snapshot, so an install with hooks configured and no alert rules would
// have built no snapshot, observed no transitions, and fired nothing -- with a
// perfectly healthy hook listed as enabled in the UI.
//
// THE THIRD CONSUMER MADE THE SAME HOLE A THIRD TIME, and it is the worst of
// the three. The broadcast-lifecycle coordinator reads these edges; leave it out
// of the gate and a DEFAULT install -- no alert rules, no webhooks -- builds no
// snapshot, so no edge is ever crossed, so no broadcast is ever put live or
// ended, and the only symptom is a watch page that says "starting soon" beside a
// stream that is going out perfectly.
func TestObserveWanted(t *testing.T) {
	tests := []struct {
		name                     string
		alerts, hooks, lifecycle bool
		want                     bool
	}{
		{"nothing at all", false, false, false, false},
		{"alerts only", true, false, false, true},
		{"hooks only", false, true, false, true},
		{"lifecycle only", false, false, true, true},
		{"alerts and hooks", true, true, false, true},
		{"everything", true, true, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := observeWanted(tc.alerts, tc.hooks, tc.lifecycle); got != tc.want {
				t.Fatalf("observeWanted(%v, %v, %v) = %v, want %v",
					tc.alerts, tc.hooks, tc.lifecycle, got, tc.want)
			}
		})
	}
}

// The gate a coordinator with nothing to do must leave shut.
//
// observeLoop promises that an install with no alert rule and no webhook pays
// for two cached lookups and nothing else -- no status snapshot, no queries, no
// disk read. Wiring a lifecycle observer that always answered true would repeal
// that for every install on earth, including the great majority with no
// lifecycle platform configured at all.
func TestLifecycleWantedIsOffWhenTheObserverHasNothingToDrive(t *testing.T) {
	e := &Engine{}
	if e.lifecycleWanted() {
		t.Fatal("an engine with no lifecycle observer wanted the sweep built")
	}
	e.SetLifecycle(stubObserver{wanted: false})
	if e.lifecycleWanted() {
		t.Fatal("an observer that reported nothing to drive still wanted the sweep built; " +
			"every install without a lifecycle destination now pays for a status snapshot " +
			"every two seconds")
	}
	e.SetLifecycle(stubObserver{wanted: true})
	if !e.lifecycleWanted() {
		t.Fatal("an observer with work to do did not want the sweep built; no broadcast on " +
			"this install would ever go live")
	}
}

type stubObserver struct{ wanted bool }

func (s stubObserver) Observe(hooks.Event) {}
func (s stubObserver) Wanted() bool        { return s.wanted }
