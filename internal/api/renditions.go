package api

import (
	"fmt"
	"net/http"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// ------------------------------------------------------------- renditions API
//
// A rendition is one shared video encode that any number of destinations can
// select, so N destinations wanting 1080p60 cost one encode rather than N. It
// re-encodes video only: every audio track passes through untouched, and each
// destination keeps doing -c:v copy plus its own routing graph on top. A
// destination with no rendition is passthrough, which is the default and what
// every pre-renditions install already does.

// renditionUsage is how many destinations point at each rendition, split by
// whether they are enabled.
//
// Enabled is the ref count that decides whether an encode runs at all; the
// total also counts disabled rows, because deleting a rendition drops every one
// of them back to passthrough. Both come from a single pass over the
// destinations so the two numbers can never disagree with each other, which
// they could if one were read from CountEnabledDestinationsByRendition a query
// later.
func (s *Server) renditionUsage() (total, enabled map[int64]int, err error) {
	rows, err := s.store.ListDestinations()
	if err != nil {
		return nil, nil, err
	}
	total, enabled = map[int64]int{}, map[int64]int{}
	for _, row := range rows {
		if row.RenditionID == nil {
			continue
		}
		total[*row.RenditionID]++
		if row.Enabled {
			enabled[*row.RenditionID]++
		}
	}
	return total, enabled, nil
}

// renditionView is one row plus the counts the UI needs to say "used by 3
// destinations" and to warn before a delete, without a second round trip.
func renditionView(r *db.Rendition, total, enabled map[int64]int) map[string]any {
	return map[string]any{
		"rendition":           r,
		"destinations":        total[r.ID],
		"enabledDestinations": enabled[r.ID],
	}
}

func (s *Server) handleListRenditions(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListRenditions()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	total, enabled, err := s.renditionUsage()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, renditionView(row, total, enabled))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetRendition(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	row, err := s.store.GetRendition(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	total, enabled, err := s.renditionUsage()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, renditionView(row, total, enabled))
}

func (s *Server) handleCreateRendition(w http.ResponseWriter, r *http.Request) {
	var row db.Rendition
	if !decodeJSON(w, r, &row) {
		return
	}
	row.ID = 0
	// The store fills in encoder, preset and GOP before validating, so the
	// smallest useful payload is {name, height, videoBitrate}.
	created, err := s.store.CreateRendition(&row)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Nothing selects a brand-new rendition yet, so this starts no encode; it
	// runs for the same reason every other mutation reconciles, which is that
	// the saved state and the running state are never allowed to drift.
	if err := s.eng.Reconcile(); err != nil {
		s.log.Warn("reconcile after rendition create", "err", err)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"rendition": created})
}

func (s *Server) handleUpdateRendition(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	existing, err := s.store.GetRendition(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Decode over the existing row so a client sending only a bitrate does not
	// blank the name, exactly as the destination editor does.
	if !decodeJSON(w, r, existing) {
		return
	}
	existing.ID = id

	updated, err := s.store.UpdateRendition(existing)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The rendition's signature rides in each downstream destination's, so this
	// restarts the encode and exactly the destinations reading it, and nothing
	// else.
	if err := s.eng.Reconcile(); err != nil {
		s.log.Warn("reconcile after rendition update", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rendition": updated})
}

// handleDeleteRendition removes a rendition and reports what that cost.
//
// The delete succeeds even while destinations are using it: the foreign key
// nulls their rendition_id, so they survive, stay enabled, and fall back to
// passthrough. That is the safe database outcome and the wrong thing to do
// silently — a destination that was being fed 1080p60 because its platform will
// not take the 4K source is now being handed the 4K source, and the first the
// user hears of it may be the platform rejecting the stream. So the counts are
// taken before the delete and returned with an explicit warning.
func (s *Server) handleDeleteRendition(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	total, enabled, err := s.renditionUsage()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.store.DeleteRendition(id); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.eng.Reconcile(); err != nil {
		s.log.Warn("reconcile after rendition delete", "err", err)
	}

	resp := map[string]any{
		"status":              "deleted",
		"destinations":        total[id],
		"enabledDestinations": enabled[id],
	}
	if n := total[id]; n > 0 {
		resp["warning"] = renditionDeleteWarning(n, enabled[id])
	}
	writeJSON(w, http.StatusOK, resp)
}

func renditionDeleteWarning(total, enabled int) string {
	subject := "destinations have"
	if total == 1 {
		subject = "destination has"
	}
	msg := fmt.Sprintf("%d %s fallen back to passthrough and will be sent the source video "+
		"unchanged. Check the source still fits what each platform accepts.", total, subject)
	if enabled > 0 {
		live := "are"
		if enabled == 1 {
			live = "is"
		}
		msg += fmt.Sprintf(" %d of them %s enabled and restarting now.", enabled, live)
	}
	return msg
}

// ---------------------------------------------------------------- presets

// handleRenditionPresets returns everything the create-a-rendition form needs:
// the starting points, the disclaimer that must be shown beside them, and the
// bounds the number inputs should use.
//
// The presets carry conservative numbers and each one's note already ends with
// the disclaimer. Platform ceilings move and differ by partner status, so the
// note is rendered verbatim and no ceiling here is presented as authoritative.
func (s *Server) handleRenditionPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"presets":    db.RenditionPresets(),
		"disclaimer": db.PresetDisclaimer,
		"bounds": map[string]any{
			"minDimension":  db.MinRenditionDimension,
			"maxDimension":  db.MaxRenditionDimension,
			"maxFps":        db.MaxRenditionFPS,
			"minBitrate":    db.MinRenditionBitrate,
			"maxBitrate":    db.MaxRenditionBitrate,
			"minGopSeconds": db.MinRenditionGOP,
			"maxGopSeconds": db.MaxRenditionGOP,
		},
	})
}

// ---------------------------------------------------------------- encoders

// encoderInfo is one choice in the rendition editor's encoder list.
type encoderInfo struct {
	Name  db.VideoEncoder `json:"name"`
	Codec string          `json:"codec"`
	// Hardware marks the vendor-accelerated encoders, which are the ones whose
	// behaviour depends on the driver rather than on us.
	Hardware bool `json:"hardware"`
	// Available is whether this FFmpeg registers the encoder. Picking one it
	// does not have costs the user a crash-looping stream to discover.
	Available bool `json:"available"`
}

// handleListEncoders reports which video encoders this install can actually
// use, so the UI offers only those rather than letting someone pick nvenc on a
// machine with no NVIDIA card.
//
// Every known encoder is listed rather than only the available ones: a
// rendition saved on a machine that had QSV must still render its own encoder
// in the form after the install moves to a machine that does not, and greying
// a choice out with a reason is more useful than making it vanish.
func (s *Server) handleListEncoders(w http.ResponseWriter, r *http.Request) {
	tools := s.eng.Tools()

	// An empty list means the -encoders probe did not run or failed, not that
	// the binary encodes nothing. Detection treats that as "assume the best" and
	// so must this: claiming every encoder is unavailable would leave the user
	// unable to create any rendition at all.
	probed := len(tools.VideoEncoders) > 0

	out := make([]encoderInfo, 0, len(db.KnownEncoders))
	for _, e := range db.KnownEncoders {
		out = append(out, encoderInfo{
			Name:      e,
			Codec:     e.Codec(),
			Hardware:  isHardwareEncoder(e),
			Available: !probed || tools.HasEncoder(string(e)),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"encoders": out,
		"default":  tools.DefaultVideoEncoder(),
		"probed":   probed,
	})
}

// isHardwareEncoder splits the list the way the UI groups it. libx264 and
// libx265 are the software encoders; everything else in KnownEncoders is a
// wrapper around a vendor's fixed-function block.
func isHardwareEncoder(e db.VideoEncoder) bool {
	return e != db.EncoderX264 && e != db.EncoderX265
}

// ---------------------------------------------------------------- restart

// handleRestartRendition cycles one shared encode, and with it the destinations
// reading from it. It mirrors the per-destination restart: the operator's
// escape hatch for an encoder that has wedged without dying.
func (s *Server) handleRestartRendition(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.eng.RestartRendition(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarting"})
}
