package api

import (
	"math"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// trackGainBody is the request for POST /api/v1/destinations/{id}/tracks/{track}/gain.
// Both fields are optional, and at least one must be sent.
type trackGainBody struct {
	Muted *bool    `json:"muted,omitempty"`
	Gain  *float64 `json:"gain,omitempty"`
}

// handleSetTrackGain changes one track's mute or gain on one destination.
//
// The change is written to the stored profile FIRST, then the destination is
// reconciled. The response says how it took effect: "live" when the running
// FFmpeg took the level on its command channel, or "restart" with the reason
// when it had to be restarted. A restart always rebuilds from the stored profile,
// so it cannot bring back a level the operator has already changed.
func (s *Server) handleSetTrackGain(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	track, err := strconv.Atoi(chi.URLParam(r, "track"))
	if err != nil || track < 0 {
		writeError(w, http.StatusBadRequest, "invalid track")
		return
	}
	var body trackGainBody
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Muted == nil && body.Gain == nil {
		writeError(w, http.StatusBadRequest, `send "muted", "gain", or both`)
		return
	}
	if body.Gain != nil && (math.IsNaN(*body.Gain) || math.IsInf(*body.Gain, 0) || *body.Gain <= 0) {
		writeError(w, http.StatusBadRequest, `gain must be a finite number above zero; to silence a track send "muted": true`)
		return
	}

	row, err := s.store.GetDestination(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	idx := -1
	for i, sel := range row.Profile.Tracks {
		if sel.Track == track {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeError(w, http.StatusNotFound, "track is not part of this destination's selection")
		return
	}
	if row.Profile.Mode == routing.ModeMatrix && body.Gain != nil {
		writeError(w, http.StatusBadRequest, "a matrix mix takes its gain per cell; mute the track instead")
		return
	}

	before := row.Profile.Tracks[idx]
	if body.Muted != nil {
		row.Profile.Tracks[idx].Muted = *body.Muted
	}
	if body.Gain != nil {
		row.Profile.Tracks[idx].Gain = *body.Gain
	}
	// Forget the last outcome so a stale note is never reported for this change.
	s.eng().ForgetDestinationLive(id)

	// Persist first. Everything below reads the stored profile, so a restart
	// triggered by the reconcile rebuilds with this level.
	updated, err := s.store.UpdateDestination(row)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if rw := s.reconcileNow("the track level"); rw != "" {
		writeError(w, http.StatusInternalServerError, rw)
		return
	}

	applied, reason, noted := s.eng().DestinationLive(id)
	if !noted {
		if row.Profile.Tracks[idx] == before {
			applied, reason = "live", "no change to apply"
		} else {
			applied, reason = "restart", "this change alters the command line, so the destination was restarted"
		}
	}
	resp := map[string]any{"applied": applied, "destination": updated}
	if reason != "" {
		resp["reason"] = reason
	}
	writeJSON(w, http.StatusOK, resp)
}
