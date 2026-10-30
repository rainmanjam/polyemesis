package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The timeline answers "when did it fail over?" after the fact. It must come
// back newest first, in the shape the page renders, with the times the
// notifier stamped on the event rather than the time the row was read.
func TestTimelineListsTheStoredHistoryNewestFirst(t *testing.T) {
	h, store, sign := sourceServer(t)
	at := time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC)
	if err := store.RecordTimelineEvent(db.TimelineEvent{
		At: at, Type: "failover.switched", Severity: "critical", Title: "Failover: primary lost",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := store.RecordTimelineEvent(db.TimelineEvent{
		At: at.Add(time.Minute), Type: "destination.down", Severity: "warning", Title: "Destination down: Twitch",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	var rows []struct {
		Type     string `json:"type"`
		Severity string `json:"severity"`
		Title    string `json:"title"`
		At       string `json:"at"`
	}
	body := send(t, h, sign, http.MethodGet, "/api/v1/timeline", nil, http.StatusOK)
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("decode: %v\n%s", err, body)
	}
	if len(rows) != 2 || rows[0].Type != "destination.down" || rows[1].Type != "failover.switched" {
		t.Fatalf("want newest first, got %+v", rows)
	}
	if rows[1].At != at.Format(time.RFC3339Nano) {
		t.Errorf("at = %q, want the stamped time %q", rows[1].At, at.Format(time.RFC3339Nano))
	}
}

func TestTimelineRejectsANonPositiveLimit(t *testing.T) {
	h, _, sign := sourceServer(t)
	send(t, h, sign, http.MethodGet, "/api/v1/timeline?limit=0", nil, http.StatusBadRequest)
	send(t, h, sign, http.MethodGet, "/api/v1/timeline?limit=abc", nil, http.StatusBadRequest)
}

// A13: a row about a destination or an ingest names the PROCESS whose log
// holds the detail, and the PROGRAMME that process belongs to, so the page can
// offer "Show log" on it. Derived here from the stored alert key rather than by
// the page, so the key's format stays the server's business.
//
// THE PROGRAMME IS NOT DECORATION. The timeline is install-wide and the
// Monitoring log is per programme: every programme has an "ingest", so a link
// that named only the process would open whichever programme happened to be
// selected. A row about something with no single log (a failover), or about a
// destination that has since been deleted, names no process at all.
func TestTimelineRowsNameTheProcessAndProgrammeWhoseLogHoldsTheDetail(t *testing.T) {
	h, store, sign := sourceServer(t)
	src := int64(1)
	dest, err := store.CreateDestination(&db.Destination{
		Name: "Twitch", Kind: db.DestFile, URL: "t.mkv", Enabled: true, AudioBitrate: 160, SourceID: &src,
	})
	if err != nil {
		t.Fatal(err)
	}
	did := strconv.FormatInt(dest.ID, 10)
	at := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	for i, ev := range []db.TimelineEvent{
		{Type: "destination.down", Title: "Destination down: Twitch", Subject: "destination:" + did},
		{Type: "destination.falling_behind", Title: "Twitch is falling behind", Subject: "destination:" + did + ":speed"},
		{Type: "ingest.lost", Title: "Ingest lost: Vertical", Subject: "ingest:2"},
		{Type: "failover.switched", Title: "Failover", Subject: "failover:1"},
		{Type: "destination.down", Title: "Deleted since", Subject: "destination:99999"},
		{Type: "destination.down", Title: "old row"},
	} {
		ev.At, ev.Severity = at.Add(time.Duration(i)*time.Second), "critical"
		if err := store.RecordTimelineEvent(ev); err != nil {
			t.Fatal(err)
		}
	}
	var rows []struct {
		Title    string `json:"title"`
		Process  string `json:"process"`
		SourceID *int64 `json:"sourceId"`
	}
	body := send(t, h, sign, http.MethodGet, "/api/v1/timeline", nil, http.StatusOK)
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatal(err)
	}
	type want struct {
		process string
		source  int64 // 0: absent
	}
	wants := map[string]want{
		"Destination down: Twitch": {"dest:" + did, 1},
		"Twitch is falling behind": {"dest:" + did, 1},
		"Ingest lost: Vertical":    {"ingest", 2},
		"Failover":                 {"", 0},
		"Deleted since":            {"", 0},
		"old row":                  {"", 0},
	}
	for _, r := range rows {
		w := wants[r.Title]
		got := int64(0)
		if r.SourceID != nil {
			got = *r.SourceID
		}
		if r.Process != w.process || got != w.source {
			t.Errorf("%q: process=%q source=%d, want process=%q source=%d", r.Title, r.Process, got, w.process, w.source)
		}
	}
}
