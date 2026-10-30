package engine

import (
	"log/slog"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"

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
		name                               string
		alerts, hooks, lifecycle, timeline bool
		want                               bool
	}{
		{"nothing at all", false, false, false, false, false},
		{"alerts only", true, false, false, false, true},
		{"hooks only", false, true, false, false, true},
		{"lifecycle only", false, false, true, false, true},
		{"timeline only", false, false, false, true, true},
		{"alerts and hooks", true, true, false, false, true},
		{"everything", true, true, true, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := observeWanted(tc.alerts, tc.hooks, tc.lifecycle, tc.timeline); got != tc.want {
				t.Fatalf("observeWanted(%v, %v, %v, %v) = %v, want %v",
					tc.alerts, tc.hooks, tc.lifecycle, tc.timeline, got, tc.want)
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

// THE CALL SITE, which is where all four of these bugs actually lived.
//
// The table above proves observeWanted ORs what it is given; it cannot see an
// input the loop never passes. A13's timeline was exactly that: a recorder on
// every engine with a store, and a gate that never asked about it, so a default
// install -- no alert rule, no hook, no lifecycle platform -- built no snapshot
// and /api/v1/timeline stayed empty through restarts and ingest loss.
func TestSweepWantedForTheTimelineAlone(t *testing.T) {
	noRules := alerts.RuleFunc(func() ([]alerts.Rule, error) { return nil, nil })

	e := &Engine{alerter: alerts.New(slog.Default(), noRules)}
	if e.sweepWanted() {
		t.Fatal("an engine with no rule, hook, lifecycle platform or recorder wanted the sweep built")
	}

	e = &Engine{alerter: alerts.New(slog.Default(), noRules, alerts.WithRecorder(func(alerts.Event) {}))}
	if !e.sweepWanted() {
		t.Fatal("an engine with a timeline recorder and nothing else did not want the sweep built; " +
			"the stream timeline records nothing on a default install")
	}
}

type stubObserver struct{ wanted bool }

func (s stubObserver) Observe(hooks.Event) {}
func (s stubObserver) Wanted() bool        { return s.wanted }
