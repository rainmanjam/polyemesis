package db

import "testing"

// A13: a timeline row remembers WHICH subject it is about -- the alert's key,
// "destination:3" or "ingest:1" -- so the page can offer that subject's log.
// The title names it in words, and words are not an address: two destinations
// can share a name, and a rename after the fact would point the link at the
// wrong one.
func TestTimelineRowsKeepTheirSubject(t *testing.T) {
	d := testDB(t)
	if err := d.RecordTimelineEvent(TimelineEvent{
		At: tlBase, Type: "destination.down", Severity: "critical",
		Title: "Destination down: Twitch", Subject: "destination:3",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := d.ListTimelineEvents(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Subject != "destination:3" {
		t.Fatalf("got %+v, want subject destination:3", got)
	}
}

// An install upgraded from before the column must gain it on open and keep
// every row it already had, with an empty subject -- an old row simply offers
// no log link. Every other timeline test builds from schema.sql, where the
// column exists, so without this the suite would pass while an upgraded
// install answered "no such column: subject" on its first timeline write --
// and the recorder would drop every event from then on.
func TestAnUpgradedInstallGainsTheTimelineSubjectColumn(t *testing.T) {
	d := testDB(t)
	if err := d.RecordTimelineEvent(TimelineEvent{At: tlBase, Type: "ingest.lost", Severity: "critical", Title: "old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(`ALTER TABLE timeline_events DROP COLUMN subject`); err != nil {
		t.Fatalf("could not reproduce the old schema: %v", err)
	}
	if _, err := d.ListTimelineEvents(10); err == nil {
		t.Fatal("reading the timeline on the old schema succeeded; the premise of this test is wrong")
	}
	if err := d.MigrateTimelineSubject(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	got, err := d.ListTimelineEvents(10)
	if err != nil {
		t.Fatalf("timeline unreadable after the migration: %v", err)
	}
	if len(got) != 1 || got[0].Title != "old" || got[0].Subject != "" {
		t.Fatalf("got %+v, want the old row with no subject", got)
	}
	if err := d.MigrateTimelineSubject(); err != nil {
		t.Fatalf("second run: %v", err)
	}
}
