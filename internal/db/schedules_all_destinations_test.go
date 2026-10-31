package db

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// app-council A54: AllDestinations is stored, and survives the round trip.
func TestAllDestinationsRoundTrips(t *testing.T) {
	d := testDB(t)
	sc := validSchedule()
	sc.Action = scheduler.ActionStop
	sc.DestinationIDs = nil
	sc.AllDestinations = true
	created, err := d.CreateSchedule(sc)
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	got, err := d.GetSchedule(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AllDestinations || len(got.DestinationIDs) != 0 {
		t.Fatalf("got all=%v ids=%v, want every destination", got.AllDestinations, got.DestinationIDs)
	}

	// And an edit to a named list turns it off: the column is written on
	// update too, not only on insert.
	got.AllDestinations = false
	got.DestinationIDs = []int64{4}
	if _, err := d.UpdateSchedule(got); err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	again, err := d.GetSchedule(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.AllDestinations || len(again.DestinationIDs) != 1 {
		t.Fatalf("after edit all=%v ids=%v, want just destination 4", again.AllDestinations, again.DestinationIDs)
	}
}

// The store refuses what Validate refuses: a stop with nothing named and not
// every destination never reaches the table.
func TestTheStoreRefusesAStopNamingNothingAndNotAll(t *testing.T) {
	d := testDB(t)
	sc := validSchedule()
	sc.Action = scheduler.ActionStop
	sc.DestinationIDs = nil
	if _, err := d.CreateSchedule(sc); err == nil {
		t.Fatal("a stop schedule naming nothing was stored")
	}
}

// THE MIGRATION. An install upgraded from before the column holds stop and
// start schedules with an empty destination_ids, which the old runner read as
// EVERY destination. They must come back as explicitly every destination -- the
// same behaviour, now stated -- and a schedule that named destinations must not
// be touched. Every other schedule test builds from schema.sql, where the
// column exists, so without this the suite would pass while an upgraded
// install's stop-everything schedule turned into an invalid row that never
// fires again.
func TestAnUpgradedInstallKeepsItsEmptySchedulesStoppingEverything(t *testing.T) {
	d := testDB(t)
	if _, err := d.sql.Exec(`ALTER TABLE schedules DROP COLUMN all_destinations`); err != nil {
		t.Fatalf("could not reproduce the old schema: %v", err)
	}
	now := time.Now().Unix()
	insert := func(name, action, dest string) int64 {
		t.Helper()
		res, err := d.sql.Exec(`INSERT INTO schedules
			(name, enabled, action, kind, destination_ids, tz, at_minutes, days,
			 run_at, grace_seconds, last_run_at, created_at, updated_at)
			VALUES (?,1,?,'daily',?,'UTC',1380,'[]',0,300,0,?,?)`, name, action, dest, now, now)
		if err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	oldStop := insert("stop everything at 23:00", "stop", "[]")
	oldStart := insert("start the show", "start", "[]")
	named := insert("stop the archive", "stop", "[5]")
	playlist := insert("overnight filler", "playlist.start", "[]")

	if err := d.MigrateScheduleAllDestinations(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, tc := range []struct {
		id       int64
		wantAll  bool
		wantIDs  int
		whatItIs string
	}{
		{oldStop, true, 0, "the old empty stop"},
		{oldStart, true, 0, "the old empty start"},
		{named, false, 1, "the stop that named a destination"},
		{playlist, false, 0, "the playlist schedule"},
	} {
		got, err := d.GetSchedule(tc.id)
		if err != nil {
			t.Fatalf("%s unreadable after the migration: %v", tc.whatItIs, err)
		}
		if got.AllDestinations != tc.wantAll || len(got.DestinationIDs) != tc.wantIDs {
			t.Errorf("%s: all=%v ids=%v, want all=%v and %d id(s)",
				tc.whatItIs, got.AllDestinations, got.DestinationIDs, tc.wantAll, tc.wantIDs)
		}
		if err := got.Validate(); err != nil {
			t.Errorf("%s does not validate after the migration, so it would never fire: %v", tc.whatItIs, err)
		}
	}

	// The migrated stop still stops everything: the runner, given the rows
	// the store now returns, disables every destination.
	stop, _ := d.GetSchedule(oldStop)
	if stop.Action != scheduler.ActionStop || !stop.AllDestinations {
		t.Fatalf("migrated stop = %+v", stop)
	}

	if err := d.MigrateScheduleAllDestinations(); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

// A row written WITH the column present but defaulted -- an older binary run
// against an upgraded database after a rollback writes all_destinations = 0 for
// its empty stop -- is folded on the next Open as well. The backfill is not
// tied to the column being new.
func TestAnEmptyScheduleWrittenAfterTheColumnExistedIsFoldedToo(t *testing.T) {
	d := testDB(t)
	now := time.Now().Unix()
	res, err := d.sql.Exec(`INSERT INTO schedules
		(name, enabled, action, kind, destination_ids, tz, at_minutes, days,
		 run_at, grace_seconds, last_run_at, created_at, updated_at, all_destinations)
		VALUES ('rolled back stop',1,'stop','daily','[]','UTC',1380,'[]',0,300,0,?,?,0)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if err := d.MigrateScheduleAllDestinations(); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetSchedule(id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AllDestinations {
		t.Error("an empty stop written by an older binary was left naming nothing")
	}
}
