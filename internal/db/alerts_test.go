package db

import (
	"errors"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

func validRule() *alerts.Rule {
	return &alerts.Rule{
		Name: "ops channel", Enabled: true,
		URL:    "https://hooks.slack.com/services/T000/B000/secretpath",
		Format: alerts.FormatSlack,
		Events: []alerts.Type{alerts.TypeDestinationDown, alerts.TypeIngestLost},
	}
}

func TestAlertRuleRoundTrips(t *testing.T) {
	d := testDB(t)

	created, err := d.CreateAlertRule(validRule())
	if err != nil {
		t.Fatalf("CreateAlertRule: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("created rule has no id")
	}

	got, err := d.GetAlertRule(created.ID)
	if err != nil {
		t.Fatalf("GetAlertRule: %v", err)
	}
	if got.Name != "ops channel" || got.Format != alerts.FormatSlack {
		t.Errorf("rule = %+v", got)
	}
	if got.URL != validRule().URL {
		t.Errorf("URL = %q, want it stored intact — there is nothing to post to without it", got.URL)
	}
	if len(got.Events) != 2 || got.Events[0] != alerts.TypeDestinationDown {
		t.Errorf("Events = %v, want the subscription list preserved in order", got.Events)
	}
	// Defaults come from the domain type, not from a second copy here.
	if got.DebounceSeconds != alerts.DefaultDebounceSeconds ||
		got.MinIntervalSeconds != alerts.DefaultIntervalSeconds {
		t.Errorf("debounce/interval = %d/%d, want the package defaults",
			got.DebounceSeconds, got.MinIntervalSeconds)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("timestamps were not set")
	}
}

func TestAlertRuleValidationRunsOnWrite(t *testing.T) {
	d := testDB(t)
	tests := []struct {
		name string
		mut  func(*alerts.Rule)
	}{
		{name: "no name", mut: func(r *alerts.Rule) { r.Name = "" }},
		{name: "no URL", mut: func(r *alerts.Rule) { r.URL = "" }},
		{name: "an unpostable scheme", mut: func(r *alerts.Rule) { r.URL = "rtmp://host/live" }},
		{name: "an unknown format", mut: func(r *alerts.Rule) { r.Format = "carrier-pigeon" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validRule()
			tt.mut(r)
			if _, err := d.CreateAlertRule(r); err == nil {
				t.Fatal("CreateAlertRule accepted an invalid rule")
			}
		})
	}
}

// This test used to assert the opposite, and the opposite was the defect. The
// unknown name was stripped before Validate saw it, so a rule written as
// events:["nope.notreal"] saved with an EMPTY list -- and empty means every
// type. The narrowest rule an operator could write became the loudest thing on
// the install, with a 201 telling them it had worked.
//
// The write path now refuses it and names it. The read path still degrades
// rather than failing: scanAlertRule runs Normalized and never Validate, so a
// row naming an event a later version removed keeps loading and keeps alerting
// on the events it does name. See internal/alerts/rule_test.go for that half.
func TestCreateAlertRuleRefusesAnUnknownSubscriptionByName(t *testing.T) {
	d := testDB(t)
	r := validRule()
	r.Events = []alerts.Type{alerts.TypeDiskLow, "destination.exploded"}
	if _, err := d.CreateAlertRule(r); err == nil {
		t.Fatal("CreateAlertRule accepted an event name this build never raises")
	} else if !strings.Contains(err.Error(), "destination.exploded") {
		t.Fatalf("error = %q, want it to name the event the operator mistyped", err)
	}

	// The control: the same path must still accept real names, or the fix has
	// simply broken subscriptions instead of narrowing them.
	ok := validRule()
	ok.Events = []alerts.Type{alerts.TypeDiskLow, alerts.TypeDiskLow}
	got, err := d.CreateAlertRule(ok)
	if err != nil {
		t.Fatalf("CreateAlertRule refused a real subscription: %v", err)
	}
	if len(got.Events) != 1 || got.Events[0] != alerts.TypeDiskLow {
		t.Errorf("Events = %v, want the duplicate collapsed to one", got.Events)
	}
}

func TestAlertRulesReturnsOnlyTheEnabledOnes(t *testing.T) {
	d := testDB(t)

	on, err := d.CreateAlertRule(validRule())
	if err != nil {
		t.Fatalf("CreateAlertRule: %v", err)
	}
	off := validRule()
	off.Name = "muted"
	off.Enabled = false
	if _, err := d.CreateAlertRule(off); err != nil {
		t.Fatalf("CreateAlertRule: %v", err)
	}

	all, err := d.ListAlertRules()
	if err != nil {
		t.Fatalf("ListAlertRules: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListAlertRules = %d, want both", len(all))
	}

	live, err := d.AlertRules()
	if err != nil {
		t.Fatalf("AlertRules: %v", err)
	}
	if len(live) != 1 || live[0].ID != on.ID {
		t.Fatalf("AlertRules = %+v, want only the enabled rule", live)
	}

	if err := d.SetAlertRuleEnabled(on.ID, false); err != nil {
		t.Fatalf("SetAlertRuleEnabled: %v", err)
	}
	live, _ = d.AlertRules()
	if len(live) != 0 {
		t.Errorf("AlertRules = %+v after disabling the last rule, want none", live)
	}
}

func TestAlertRuleUpdateAndDelete(t *testing.T) {
	d := testDB(t)
	created, err := d.CreateAlertRule(validRule())
	if err != nil {
		t.Fatalf("CreateAlertRule: %v", err)
	}

	created.Name = "renamed"
	created.Format = alerts.FormatDiscord
	created.Events = nil
	created.DebounceSeconds = 45
	updated, err := d.UpdateAlertRule(created)
	if err != nil {
		t.Fatalf("UpdateAlertRule: %v", err)
	}
	if updated.Name != "renamed" || updated.Format != alerts.FormatDiscord {
		t.Errorf("updated = %+v", updated)
	}
	if len(updated.Events) != 0 {
		t.Errorf("Events = %v, want an empty subscription list to mean everything", updated.Events)
	}
	if updated.DebounceSeconds != 45 {
		t.Errorf("DebounceSeconds = %d, want 45", updated.DebounceSeconds)
	}

	if err := d.DeleteAlertRule(created.ID); err != nil {
		t.Fatalf("DeleteAlertRule: %v", err)
	}
	if _, err := d.GetAlertRule(created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetAlertRule after delete = %v, want ErrNotFound", err)
	}
}

func TestAlertRuleMissingRowsReportNotFound(t *testing.T) {
	d := testDB(t)
	if _, err := d.GetAlertRule(404); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetAlertRule = %v, want ErrNotFound", err)
	}
	if err := d.DeleteAlertRule(404); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteAlertRule = %v, want ErrNotFound", err)
	}
	if err := d.SetAlertRuleEnabled(404, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetAlertRuleEnabled = %v, want ErrNotFound", err)
	}
	r := validRule()
	r.ID = 404
	if _, err := d.UpdateAlertRule(r); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateAlertRule = %v, want ErrNotFound", err)
	}
}
