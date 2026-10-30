package db

import (
	"strings"
	"testing"
	"time"
)

var tlBase = time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC)

func TestTimelineListsNewestFirstWithALimit(t *testing.T) {
	d := testDB(t)
	for i, title := range []string{"first", "second", "third"} {
		if err := d.RecordTimelineEvent(TimelineEvent{
			At: tlBase.Add(time.Duration(i) * time.Minute), Type: "failover.switched",
			Severity: "critical", Title: title,
		}); err != nil {
			t.Fatalf("record %s: %v", title, err)
		}
	}
	got, err := d.ListTimelineEvents(2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 || got[0].Title != "third" || got[1].Title != "second" {
		t.Fatalf("want newest two, newest first; got %+v", got)
	}
}

// The row cap is what keeps a flapping destination from growing the table
// without bound. Overflow must discard the OLDEST rows, never the newest.
func TestTimelineRowCapKeepsTheNewestRows(t *testing.T) {
	d := testDB(t)
	total := timelineMaxRows + 25
	for i := 0; i < total; i++ {
		if err := d.RecordTimelineEvent(TimelineEvent{
			At: tlBase.Add(time.Duration(i) * time.Second), Type: "destination.down",
			Severity: "warning", Title: "row",
		}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	// Count the table directly: ListTimelineEvents caps its own output, which
	// would hide a table that grew past the cap.
	var rows int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM timeline_events`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != timelineMaxRows {
		t.Fatalf("row cap: want %d rows kept, table holds %d", timelineMaxRows, rows)
	}
	got, err := d.ListTimelineEvents(1)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	newest := tlBase.Add(time.Duration(total-1) * time.Second)
	if len(got) != 1 || !got[0].At.Equal(newest) {
		t.Fatalf("newest row lost: got %+v, want At %v", got, newest)
	}
}

// The age cap is measured against the event being written, not the wall
// clock, so a restored or replayed history prunes the same way every time.
func TestTimelineAgeCapDropsRowsOlderThanTheWindow(t *testing.T) {
	d := testDB(t)
	old := tlBase.Add(-(timelineMaxAge + time.Hour))
	if err := d.RecordTimelineEvent(TimelineEvent{At: old, Type: "ingest.lost", Severity: "critical", Title: "ancient"}); err != nil {
		t.Fatalf("record old: %v", err)
	}
	if err := d.RecordTimelineEvent(TimelineEvent{At: tlBase, Type: "ingest.lost", Severity: "critical", Title: "now"}); err != nil {
		t.Fatalf("record new: %v", err)
	}
	got, err := d.ListTimelineEvents(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Title != "now" {
		t.Fatalf("age cap: want only the new row, got %+v", got)
	}
}

// A stream key must never reach this table. The notifier redacts before an
// event is published, but the store refuses to trust that: the detail column
// is clipped, and a title that is absurdly long is cut rather than stored.
func TestTimelineClipsOversizedText(t *testing.T) {
	d := testDB(t)
	long := strings.Repeat("x", timelineTextLimit*3)
	if err := d.RecordTimelineEvent(TimelineEvent{At: tlBase, Type: "destination.down", Severity: "warning", Title: long, Detail: long}); err != nil {
		t.Fatalf("record: %v", err)
	}
	got, _ := d.ListTimelineEvents(1)
	if len(got) != 1 || len(got[0].Title) > timelineTextLimit || len(got[0].Detail) > timelineTextLimit {
		t.Fatalf("text not clipped to %d: title %d detail %d", timelineTextLimit, len(got[0].Title), len(got[0].Detail))
	}
}
