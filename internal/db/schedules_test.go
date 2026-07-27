package db

import (
	"errors"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

func validSchedule() *scheduler.Schedule {
	return &scheduler.Schedule{
		Name: "evening show", Enabled: true,
		Action: scheduler.ActionStart, Kind: scheduler.KindWeekly,
		TZ: "America/New_York", AtMinutes: 19 * 60,
		Days:           []time.Weekday{time.Friday, time.Saturday},
		DestinationIDs: []int64{3, 1},
		GraceSeconds:   600,
	}
}

func TestScheduleRoundTrips(t *testing.T) {
	d := testDB(t)

	created, err := d.CreateSchedule(validSchedule())
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	got, err := d.GetSchedule(created.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.Name != "evening show" || got.Kind != scheduler.KindWeekly {
		t.Errorf("schedule = %+v", got)
	}
	if got.TZ != "America/New_York" || got.AtMinutes != 19*60 {
		t.Errorf("wall clock = %s %d, want the stored zone and minute", got.TZ, got.AtMinutes)
	}
	// Normalized sorts both collections, so the round trip is stable.
	if len(got.Days) != 2 || got.Days[0] != time.Friday || got.Days[1] != time.Saturday {
		t.Errorf("Days = %v", got.Days)
	}
	if len(got.DestinationIDs) != 2 || got.DestinationIDs[0] != 1 {
		t.Errorf("DestinationIDs = %v, want them sorted", got.DestinationIDs)
	}
	if got.GraceSeconds != 600 {
		t.Errorf("GraceSeconds = %d, want 600", got.GraceSeconds)
	}
}

// A one-shot's instant must come back as the same moment, whatever zone it went
// in as.
func TestOneShotStoresUTCAndComesBackAsTheSameInstant(t *testing.T) {
	d := testDB(t)
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("zone unavailable: %v", err)
	}
	local := time.Date(2026, 7, 27, 19, 0, 0, 0, ny)

	created, err := d.CreateSchedule(&scheduler.Schedule{
		Name: "the broadcast", Enabled: true, Action: scheduler.ActionStart,
		Kind: scheduler.KindOnce, RunAt: local,
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if !created.RunAt.Equal(local) {
		t.Errorf("RunAt = %s, want the same instant as %s", created.RunAt, local)
	}
	if created.RunAt.Location() != time.UTC {
		t.Errorf("RunAt is in %s, want UTC on the way out of the database", created.RunAt.Location())
	}
}

func TestScheduleValidationRunsOnWrite(t *testing.T) {
	d := testDB(t)
	tests := []struct {
		name string
		mut  func(*scheduler.Schedule)
	}{
		{name: "no name", mut: func(s *scheduler.Schedule) { s.Name = "" }},
		{name: "an unknown action", mut: func(s *scheduler.Schedule) { s.Action = "pause" }},
		{name: "an unknown kind", mut: func(s *scheduler.Schedule) { s.Kind = "fortnightly" }},
		{name: "an unknown zone", mut: func(s *scheduler.Schedule) { s.TZ = "Middle/Earth" }},
		{name: "a weekly schedule with no days", mut: func(s *scheduler.Schedule) { s.Days = nil }},
		{name: "a time off the clock", mut: func(s *scheduler.Schedule) { s.AtMinutes = 2000 }},
		{
			name: "a one-shot with no instant",
			mut:  func(s *scheduler.Schedule) { s.Kind = scheduler.KindOnce; s.RunAt = time.Time{} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSchedule()
			tt.mut(s)
			if _, err := d.CreateSchedule(s); err == nil {
				t.Fatal("CreateSchedule accepted an invalid schedule")
			}
		})
	}
}

func TestSchedulesReturnsOnlyTheEnabledOnes(t *testing.T) {
	d := testDB(t)

	on, err := d.CreateSchedule(validSchedule())
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	off := validSchedule()
	off.Name = "paused show"
	off.Enabled = false
	if _, err := d.CreateSchedule(off); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	all, err := d.ListSchedules()
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListSchedules = %d, want both", len(all))
	}
	live, err := d.Schedules()
	if err != nil {
		t.Fatalf("Schedules: %v", err)
	}
	if len(live) != 1 || live[0].ID != on.ID {
		t.Errorf("Schedules = %+v, want only the enabled one", live)
	}
}

func TestMarkScheduleRunOnlyEverMovesForward(t *testing.T) {
	d := testDB(t)
	created, err := d.CreateSchedule(validSchedule())
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	newer := time.Date(2026, 7, 27, 23, 0, 0, 0, time.UTC)
	older := time.Date(2026, 7, 20, 23, 0, 0, 0, time.UTC)

	if err := d.MarkScheduleRun(created.ID, newer); err != nil {
		t.Fatalf("MarkScheduleRun: %v", err)
	}
	// An out-of-order write from an overlapping sweep must not rewind the
	// marker, which would let an occurrence be acted on twice.
	if err := d.MarkScheduleRun(created.ID, older); err != nil {
		t.Fatalf("MarkScheduleRun: %v", err)
	}
	got, err := d.GetSchedule(created.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if !got.LastRunAt.Equal(newer) {
		t.Errorf("LastRunAt = %s, want it held at %s", got.LastRunAt, newer)
	}
}

func TestUpdateScheduleLeavesLastRunAtAlone(t *testing.T) {
	d := testDB(t)
	created, err := d.CreateSchedule(validSchedule())
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	ran := time.Date(2026, 7, 27, 23, 0, 0, 0, time.UTC)
	if err := d.MarkScheduleRun(created.ID, ran); err != nil {
		t.Fatalf("MarkScheduleRun: %v", err)
	}

	edit, err := d.GetSchedule(created.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	edit.Name = "renamed"
	edit.LastRunAt = time.Time{}
	updated, err := d.UpdateSchedule(edit)
	if err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	if updated.Name != "renamed" {
		t.Errorf("Name = %q", updated.Name)
	}
	if !updated.LastRunAt.Equal(ran) {
		t.Errorf("LastRunAt = %s, want the edit to leave it at %s: an edit must not "+
			"resurrect a window that was already handled", updated.LastRunAt, ran)
	}
}

func TestScheduleEnableAndDelete(t *testing.T) {
	d := testDB(t)
	created, err := d.CreateSchedule(validSchedule())
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if err := d.SetScheduleEnabled(created.ID, false); err != nil {
		t.Fatalf("SetScheduleEnabled: %v", err)
	}
	got, _ := d.GetSchedule(created.ID)
	if got.Enabled {
		t.Error("schedule is still enabled")
	}
	if err := d.DeleteSchedule(created.ID); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
	if _, err := d.GetSchedule(created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSchedule after delete = %v, want ErrNotFound", err)
	}
}

func TestScheduleMissingRowsReportNotFound(t *testing.T) {
	d := testDB(t)
	if _, err := d.GetSchedule(404); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSchedule = %v, want ErrNotFound", err)
	}
	if err := d.DeleteSchedule(404); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteSchedule = %v, want ErrNotFound", err)
	}
	if err := d.SetScheduleEnabled(404, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetScheduleEnabled = %v, want ErrNotFound", err)
	}
	s := validSchedule()
	s.ID = 404
	if _, err := d.UpdateSchedule(s); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateSchedule = %v, want ErrNotFound", err)
	}
}

// The runner takes what the database returns and evaluates it directly, so the
// two have to agree about what a stored schedule means.
func TestStoredScheduleEvaluatesTheWayItWasWritten(t *testing.T) {
	d := testDB(t)
	at := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)

	created, err := d.CreateSchedule(&scheduler.Schedule{
		Name: "the broadcast", Enabled: true, Action: scheduler.ActionStart,
		Kind: scheduler.KindOnce, RunAt: at, GraceSeconds: 300,
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	rows, err := d.Schedules()
	if err != nil {
		t.Fatalf("Schedules: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Schedules = %d rows, want 1", len(rows))
	}
	dec := scheduler.Evaluate(rows[0], at.Add(30*time.Second))
	if !dec.Fire || !dec.At.Equal(at) {
		t.Fatalf("Evaluate = %+v, want a fire at %s", dec, at)
	}

	if err := d.MarkScheduleRun(created.ID, dec.At); err != nil {
		t.Fatalf("MarkScheduleRun: %v", err)
	}
	rows, _ = d.Schedules()
	if again := scheduler.Evaluate(rows[0], at.Add(time.Minute)); again.Fire {
		t.Errorf("Evaluate after the run was recorded = %+v, want it handled", again)
	}
}
