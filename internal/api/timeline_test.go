package api

import (
	"encoding/json"
	"net/http"
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
