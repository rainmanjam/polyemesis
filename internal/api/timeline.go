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
	// SourceID is the programme that process belongs to. The timeline is
	// install-wide and the Monitoring log is per programme, and every
	// programme has an "ingest" -- without this a link would open whichever
	// programme happened to be selected. The page offers the link only on the
	// programme being viewed.
	SourceID *int64 `json:"sourceId,omitempty"`
}

// processForSubject maps an alert key onto the process name the log endpoints
// use, and the programme that process belongs to. Kept here so the key's
// format stays the server's business: the page receives a process name and a
// programme it can act on, not a key it would have to parse.
//
// A destination's programme is read off its row, so a destination deleted
// since the event names nothing -- its log is gone, and a link to it would be
// dead. An ingest's programme is the scope the alert watcher put on its key
// ("ingest:<source>"); an unscoped "ingest" names the process alone.
func (s *Server) processForSubject(subject string) (string, *int64) {
	head, rest, _ := strings.Cut(subject, ":")
	switch head {
	case "destination":
		idText, _, _ := strings.Cut(rest, ":")
		id, err := strconv.ParseInt(idText, 10, 64)
		if err != nil {
			return "", nil
		}
		d, err := s.store.GetDestination(id)
		if err != nil {
			return "", nil
		}
		return "dest:" + idText, d.SourceID
	case "ingest":
		if src, err := strconv.ParseInt(rest, 10, 64); err == nil {
			return "ingest", &src
		}
		return "ingest", nil
	}
	return "", nil
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
	type resolved struct {
		process string
		source  *int64
	}
	subjects := map[string]resolved{}
	for _, e := range events {
		row := timelineRow{
			ID: e.ID, At: e.At, Type: e.Type, Severity: e.Severity, Title: e.Title, Detail: e.Detail,
		}
		// Memoised per request: a flapping destination fills the page with
		// rows about the same subject, and each would otherwise be its own
		// destination read.
		r, ok := subjects[e.Subject]
		if !ok {
			r.process, r.source = s.processForSubject(e.Subject)
			subjects[e.Subject] = r
		}
		row.Process, row.SourceID = r.process, r.source
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}
