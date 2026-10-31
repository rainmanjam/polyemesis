package db

import (
	"testing"
	"time"
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
