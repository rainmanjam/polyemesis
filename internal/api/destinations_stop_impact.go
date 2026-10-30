package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// STOP IMPACT: a read-only preview of what Stop and Stop-all would do.
//
// A Stream Deck key, or any script, needs to show the cost of a stop before
// the operator confirms it. The count is the number of YouTube broadcasts the
// stop would END, which cannot be undone (see destinations_bulk.go's header).
//
// It is derived from planLifecycle(false, ...) -- the END branch that a stop
// actually reaches -- and not from a copy of its phase list, so the preview
// cannot disagree with the action. It reads the RECORDED phase on each
// destination row (Lifecycle.Phase), which is only ever written from a platform
// read, so no YouTube call is made to answer it.
//
// GET, so a read-scoped token may ask. Scoped by ?source= exactly as stop-all
// is, so the preview counts what the matching stop would touch and nothing
// else.

// stopImpactBody is the answer. Destinations is every row the stop would touch;
// YouTubeBroadcasts is the subset whose broadcast would be ended for good.
type stopImpactBody struct {
	Destinations      int `json:"destinations"`
	YouTubeBroadcasts int `json:"youtubeBroadcasts"`
}

// stopImpact counts what a stop over rows would end. A row counts toward
// YouTubeBroadcasts only when its recorded phase is one the END branch sends a
// transition for; created, ready, complete, revoked and an unrecorded phase do
// not, matching planLifecycle exactly.
func stopImpact(rows []*db.Destination) stopImpactBody {
	out := stopImpactBody{Destinations: len(rows)}
	for _, row := range rows {
		if row.Platform != db.PlatformYouTube {
			continue
		}
		st := &oauth.BroadcastLifecycleState{Status: row.Lifecycle.Phase}
		if planLifecycle(false, st).Send == oauth.PhaseComplete {
			out.YouTubeBroadcasts++
		}
	}
	return out
}

// destinationParam narrows the preview to one destination: what a per-destination
// stop of that row would end. The same count stop-all's preview gives, for one row.
const destinationParam = "destination"

// handleStopImpact answers GET /destinations/stop-impact?source=N, or with
// ?destination=N the preview of that one destination's stop. An id that names
// no destination is a 404, never a zero count: a zero is a claim that the stop
// ends nothing, and a caller that trusted it would act on a row it never saw.
func (s *Server) handleStopImpact(w http.ResponseWriter, r *http.Request) {
	if raw := strings.TrimSpace(r.URL.Query().Get(destinationParam)); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, `"`+destinationParam+`" must be a destination id`)
			return
		}
		row, err := s.store.GetDestination(id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, stopImpact([]*db.Destination{row}))
		return
	}
	sourceID, ok := s.namedSourceParam(w, r)
	if !ok {
		return
	}
	var (
		rows []*db.Destination
		err  error
	)
	if sourceID != nil {
		rows, err = s.store.ListDestinationsBySource(*sourceID)
	} else {
		rows, err = s.store.ListDestinations()
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stopImpact(rows))
}
