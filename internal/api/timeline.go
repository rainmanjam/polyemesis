package api

import (
	"net/http"
	"strconv"
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
		})
	}
	writeJSON(w, http.StatusOK, out)
}
