package api

import (
	"math"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/db"
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

	s.trackGainMu.Lock()
	defer s.trackGainMu.Unlock()

	row, err := s.store.GetDestination(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// A copy destination forwards its audio untouched: there is no volume stage
	// for a level to reach. Storing it and answering success would tell a
	// controller the track is muted while its sound keeps flowing.
	if row.Audio.Copy && row.Kind != db.DestAudio {
		writeError(w, http.StatusConflict, "this destination passes its audio through unchanged, "+
			"so a track level cannot apply to it; turn off audio copy to mix and mute tracks")
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
	changed := row.Profile.Tracks[idx] != before

	// THE OWNING ENGINE, as handleRestartDestination explains: s.eng() is the
	// default programme's, which has never heard of another programme's
	// destination and would report on nothing. nil when the programme has no
	// running engine; the change is still stored for when it does.
	eng := s.engineForSource(row.SourceID)
	if eng != nil {
		// Forget the last outcome so a stale note is never reported for this change.
		eng.ForgetDestinationLive(id)
	}

	// The same repair every destination write makes first, so this route
	// cannot store a row keeping another platform's account
	// (TestEveryDestinationWriteIsRepairedFirst).
	warnings, err := s.dropUnsendableSettings(row)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// Persist first. Everything below reads the stored profile, so a restart
	// triggered by the reconcile, or any later one, rebuilds with this level.
	updated, err := s.store.UpdateDestination(row)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rw := s.reconcileNow("the track level")

	applied, reason := "", ""
	noted := false
	if eng != nil {
		applied, reason, noted = eng.DestinationLive(id)
	}
	switch {
	case noted:
	case !changed:
		applied, reason = "live", "no change to apply"
	case rw != "":
		// The reconcile failed before it could say anything about this
		// destination. The level is stored but nothing confirmed it reached the
		// running output, so it must not be reported as live.
		applied, reason = "restart", "the level is stored but the running pipeline could not be updated; it takes effect on the next start"
	case eng == nil || !eng.DestinationRunning(id):
		applied, reason = "restart", "the destination is not running; the level is stored and takes effect when it starts"
	default:
		// Running, and the engine saw no level move: a level change it sent or
		// restarted for would have left a note. The stored value reaches no
		// stage of the running mix (a track that is selected but not enabled,
		// or absent from the ingest), so nothing was restarted.
		applied, reason = "live", "the track is not part of the running mix, so the level is stored and changes nothing on the output yet"
	}
	resp := map[string]any{"applied": applied, "destination": updated}
	if reason != "" {
		resp["reason"] = reason
	}
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	// The change is stored whether or not the reconcile succeeded; a failed one
	// is reported the way every other mutation reports it (#709), not as a 500
	// that would read as "nothing happened".
	writeMutation(w, http.StatusOK, rw, resp)
}
