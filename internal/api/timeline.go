package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// timelineDefaultLimit is what the page asks for when it does not say. The
// store keeps at most 1000 rows, so this is a screenful, not the whole history.
const timelineDefaultLimit = 200

type timelineRow struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	Type     string    `json:"type"`
	Severity string    `json:"severity"`
	Title    string    `json:"title"`
	Detail   string    `json:"detail,omitempty"`
	// Process is the supervised process whose log holds this row's detail,
	// when there is exactly one: "dest:3" for a destination, "ingest" for a
	// programme's ingest. Absent for a failover or a row from before the
	// subject was stored. The page offers "Show log" on it (A13).
	Process string `json:"process,omitempty"`
}

// processForSubject maps an alert key onto the process name the log endpoints
// use. Kept here so the key's format stays the server's business: the page
// receives a process name it can filter on, not a key it would have to parse.
func processForSubject(subject string) string {
	head, rest, _ := strings.Cut(subject, ":")
	switch head {
	case "destination":
		id, _, _ := strings.Cut(rest, ":")
		if _, err := strconv.ParseInt(id, 10, 64); err != nil {
			return ""
		}
		return "dest:" + id
	case "ingest":
		return "ingest"
	}
	return ""
}

// handleListTimeline serves the stream history, newest first. It is a read of
// rows the notifier already redacted, so it carries no secrets of its own.
func (s *Server) handleListTimeline(w http.ResponseWriter, r *http.Request) {
	limit := timelineDefaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = n
	}
	events, err := s.store.ListTimelineEvents(limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := make([]timelineRow, 0, len(events))
	for _, e := range events {
		out = append(out, timelineRow{
			ID: e.ID, At: e.At, Type: e.Type, Severity: e.Severity, Title: e.Title, Detail: e.Detail,
			Process: processForSubject(e.Subject),
		})
	}
	writeJSON(w, http.StatusOK, out)
}
