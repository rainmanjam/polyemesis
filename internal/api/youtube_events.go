package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// The YouTube event picker's read and its one write.
//
// GET lists the channel's upcoming and active broadcasts with the flags polyemesis needs to explain itself (auto-start,
// auto-stop, the stream it is bound to and whether that stream is active). NEVER A KEY OR AN INGEST ADDRESS: the
// response is built from oauth.EventSummary, and "is this event on this destination's stream" is answered here by
// matching stored keys in-process and leaves as a boolean.
//
// POST .../auto-start turns enableAutoStart on for one event. It is the one place polyemesis changes an event's own
// flags, and only because the operator clicked it; nothing else ever edits a picked event.

// ytEventRow is one event in the picker's answer.
type ytEventRow struct {
	oauth.EventSummary
	// OnThisStream is true when the event is bound to the stream the queried destination publishes with.
	OnThisStream bool `json:"onThisStream,omitempty"`
}

// handleYouTubeEvents answers GET /platforms/accounts/{id}/youtube/events[?destinationId=N].
func (s *Server) handleYouTubeEvents(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, errInvalidID)
		return
	}
	var self int64
	if v := r.URL.Query().Get("destinationId"); v != "" {
		if self, err = strconv.ParseInt(v, 10, 64); err != nil || self < 0 {
			writeError(w, http.StatusBadRequest, "destinationId must be a destination id")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	acct, err := s.tokenFor(ctx, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	mgr, ok := oauth.As[oauth.EventManager](s.providers, acct.Platform)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"supported": false,
			"reason": "polyemesis lists events only for YouTube"})
		return
	}
	rows, err := s.store.ListDestinations()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	keys, selfIdx := ytHeldKeys(rows, acct.Platform, id, self)
	events, err := mgr.ListEvents(ctx, acct.AccessToken, keys)
	if err != nil {
		writeError(w, http.StatusBadGateway, scrubSourceKeys(err.Error(), keys))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"supported": true, "events": ytEventRows(events, selfIdx)})
}

// ytHeldKeys collects the stream keys of this account's destinations on the
// platform, and the position of the destination self among them (-1 if none).
func ytHeldKeys(rows []*db.Destination, platform db.Platform, accountID, self int64) ([]string, int) {
	var keys []string
	selfIdx := -1
	for _, d := range rows {
		if d.Platform == platform && d.AccountID != nil && *d.AccountID == accountID {
			if d.ID == self {
				selfIdx = len(keys)
			}
			keys = append(keys, d.StreamKey)
		}
	}
	return keys, selfIdx
}

// ytEventRows marks each event that is bound to the held stream at selfIdx.
func ytEventRows(events []oauth.EventSummary, selfIdx int) []ytEventRow {
	out := make([]ytEventRow, 0, len(events))
	for _, e := range events {
		row := ytEventRow{EventSummary: e}
		for _, k := range e.BoundToHeld {
			if k == selfIdx {
				row.OnThisStream = true
			}
		}
		out = append(out, row)
	}
	return out
}

// handleYouTubeEventAutoStart answers POST /platforms/accounts/{id}/youtube/events/{eventId}/auto-start.
func (s *Server) handleYouTubeEventAutoStart(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, errInvalidID)
		return
	}
	eventID := chi.URLParam(r, "eventId")
	if eventID == "" || len(eventID) > 128 {
		writeError(w, http.StatusBadRequest, "an event id is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	acct, err := s.tokenFor(ctx, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	mgr, ok := oauth.As[oauth.EventManager](s.providers, acct.Platform)
	if !ok {
		writeError(w, http.StatusBadRequest, "auto-start is a YouTube setting")
		return
	}
	if acct.Platform != db.PlatformYouTube {
		writeError(w, http.StatusBadRequest, "auto-start is a YouTube setting")
		return
	}
	if err := mgr.SetEventAutoStart(ctx, acct.AccessToken, eventID); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"eventId": eventID, "enableAutoStart": true})
}
