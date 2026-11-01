package db

import (
	"errors"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// Planned rollover's storage keeps three things apart: the operator's configuration, the session a rollover
// is counting, and the result of the last attempt. These tests hold that separation. A margin change in the
// middle of a show must not reset the session start, and a rollover recording its result must not change
// the quiet window the operator set. Each of those would be a silent wrong answer on a live show.

// TestAConfigSaveNeverResetsTheSessionOrTheLastResult checks that writing the operator's settings leaves
// the session start and the last rollover result exactly as the rollover left them.
func TestAConfigSaveNeverResetsTheSessionOrTheLastResult(t *testing.T) {
	d := testDB(t)
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	start := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	if err := d.SetRolloverSession(7, "bcast-1", start, now); err != nil {
		t.Fatalf("set session: %v", err)
	}
	if err := d.RecordRolloverResult(7, "margin", "rolled over", now); err != nil {
		t.Fatalf("record result: %v", err)
	}
	if err := d.SaveRolloverConfig(RolloverSettings{DestinationID: 7, Enabled: true, MarginMinutes: 45}, now); err != nil {
		t.Fatalf("save config: %v", err)
	}
	got, err := d.GetRollover(7)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SessionBroadcastID != "bcast-1" || !got.SessionStartedAt.Equal(start) {
		t.Errorf("a config save reset the session: broadcast=%q started=%s", got.SessionBroadcastID, got.SessionStartedAt)
	}
	if got.LastTrigger != "margin" || got.LastResult != "rolled over" {
		t.Errorf("a config save reset the last result: trigger=%q result=%q", got.LastTrigger, got.LastResult)
	}
	if !got.Enabled || got.MarginMinutes != 45 {
		t.Errorf("config not stored: enabled=%v margin=%d", got.Enabled, got.MarginMinutes)
	}
}

// TestAResultRecordNeverChangesTheOperatorsQuietWindow checks the other direction: a rollover writing its
// outcome must not touch the configured quiet window or the OBS scene.
func TestAResultRecordNeverChangesTheOperatorsQuietWindow(t *testing.T) {
	d := testDB(t)
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	if err := d.SaveRolloverConfig(RolloverSettings{
		DestinationID: 3, Enabled: true, MarginMinutes: 30,
		QuietStart: "03:00", QuietEnd: "05:00", QuietZone: "Europe/London", OBSScene: "BRB",
	}, now); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := d.RecordRolloverResult(3, "quiet", "failed: stream key refused", now); err != nil {
		t.Fatalf("record result: %v", err)
	}
	got, err := d.GetRollover(3)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.QuietStart != "03:00" || got.QuietEnd != "05:00" || got.QuietZone != "Europe/London" || got.OBSScene != "BRB" {
		t.Errorf("a result record changed the configuration: %+v", got)
	}
}

// TestAnUnconfiguredDestinationReadsAsDisabledWithTheDefaultMargin guards the "never configured" read. Callers
// must not have to special-case a missing row, so it reads as the defaults, disabled.
func TestAnUnconfiguredDestinationReadsAsDisabledWithTheDefaultMargin(t *testing.T) {
	d := testDB(t)
	got, err := d.GetRollover(99)
	if err != nil {
		t.Fatalf("get unconfigured: %v", err)
	}
	if got.Enabled {
		t.Errorf("an unconfigured destination reads as enabled")
	}
	if got.MarginMinutes != DefaultRolloverMarginMinutes {
		t.Errorf("margin = %d, want the default %d", got.MarginMinutes, DefaultRolloverMarginMinutes)
	}
}

// TestOnlyEnabledRolloversAreListedForTheSweep checks the sweep's working set: a switched-off destination must
// never be offered to the planner, however old its session.
func TestOnlyEnabledRolloversAreListedForTheSweep(t *testing.T) {
	d := testDB(t)
	now := time.Now()
	if err := d.SaveRolloverConfig(RolloverSettings{DestinationID: 1, Enabled: true, MarginMinutes: 30}, now); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveRolloverConfig(RolloverSettings{DestinationID: 2, Enabled: false, MarginMinutes: 30}, now); err != nil {
		t.Fatal(err)
	}
	list, err := d.ListEnabledRollovers()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].DestinationID != 1 {
		t.Fatalf("enabled list = %+v, want only destination 1", list)
	}
}

// TestARolloverFindsTheTemplateTheLiveBroadcastCameFrom checks the link a rollover uses to keep the show's title,
// description and privacy: the recurring occurrence whose platform event is the live broadcast, then its template.
// A broadcast that did not come from a template must find none, so the caller copies it instead of inventing one.
func TestARolloverFindsTheTemplateTheLiveBroadcastCameFrom(t *testing.T) {
	d := testDB(t)
	at := time.Date(2026, 11, 1, 19, 0, 0, 0, time.UTC)
	if _, err := d.SaveBroadcastTemplate(scheduler.BroadcastTemplate{
		ScheduleID: 5, TitlePattern: "Thursday show {date}", Description: "Weekly notes", Privacy: "unlisted",
	}); err != nil {
		t.Fatalf("save template: %v", err)
	}
	id, err := d.ClaimOccurrence(scheduler.Occurrence{ScheduleID: 5, DestinationID: 8, At: at})
	if err != nil {
		t.Fatalf("claim occurrence: %v", err)
	}
	if err := d.ResolveOccurrence(id, "live-bcast-1", "Thursday show 2026-11-01"); err != nil {
		t.Fatalf("resolve occurrence: %v", err)
	}

	got, err := d.RolloverTemplateFor(8, "live-bcast-1")
	if err != nil {
		t.Fatalf("template for the live broadcast: %v", err)
	}
	if got.TitlePattern != "Thursday show {date}" || got.Description != "Weekly notes" || got.Privacy != "unlisted" {
		t.Errorf("template = %+v, want the one the occurrence came from", got)
	}
	if _, err := d.RolloverTemplateFor(8, "not-from-a-template"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a broadcast with no occurrence found a template: err = %v, want ErrNotFound", err)
	}
	if _, err := d.RolloverTemplateFor(9, "live-bcast-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the same broadcast on a different destination found a template: err = %v", err)
	}
}

// A rollover moves the occurrence onto the replacement, so the next rollover finds the template by the new live
// broadcast. Another destination's row with the same event id is never touched.
func TestRebindingAnOccurrenceMovesTheTemplateLinkToTheReplacement(t *testing.T) {
	d := testDB(t)
	at := time.Date(2026, 11, 1, 19, 0, 0, 0, time.UTC)
	if _, err := d.SaveBroadcastTemplate(scheduler.BroadcastTemplate{ScheduleID: 5, TitlePattern: "Show {date}"}); err != nil {
		t.Fatalf("save template: %v", err)
	}
	for _, dest := range []int64{8, 9} {
		id, err := d.ClaimOccurrence(scheduler.Occurrence{ScheduleID: 5, DestinationID: dest, At: at})
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if err := d.ResolveOccurrence(id, "live-1", "Show"); err != nil {
			t.Fatalf("resolve: %v", err)
		}
	}
	if err := d.RebindOccurrenceEvent(8, "live-1", "live-2"); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if _, err := d.RolloverTemplateFor(8, "live-2"); err != nil {
		t.Fatalf("the replacement finds no template: %v", err)
	}
	if _, err := d.RolloverTemplateFor(9, "live-1"); err != nil {
		t.Fatalf("another destination's occurrence was moved too: %v", err)
	}
}
