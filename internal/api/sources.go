package api

import (
	"errors"
	"net/http"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// Sources are the multi-programme endpoints: one install, several independent
// ingests, each with its own destinations and renditions.
//
// Reconcile goes through the manager rather than a single engine here, because
// creating or deleting a source changes which engines should exist at all --
// s.eng().Reconcile() would only ever reconcile the default programme and a new
// source would sit in the database doing nothing until a restart.

// sourceView is a source plus the things the UI needs but does not store: the
// publish URLs an operator pastes into OBS, and whether this programme is the
// one unscoped requests act on.
type sourceView struct {
	*db.Source
	// PublishURLs are ready to paste into an encoder, one per protocol this
	// source accepts, with <server> where the hostname goes. Built here rather
	// than in the UI so the URL syntax lives next to the code that produces the
	// listener it has to match. See publishURLs for why the token is NOT in
	// them.
	PublishURLs map[string]string `json:"publishUrls"`
	IsDefault   bool              `json:"isDefault"`
	// TokenEnforced reports whether the publish token actually gates anything.
	// It is false in this build: sources are separated by port, and the
	// enforced secrets are the RTMP stream key and the SRT passphrase. The UI
	// reads this rather than assuming, so nobody is told a rotated token
	// protects an ingest that it does not.
	TokenEnforced bool `json:"tokenEnforced"`
	// Running reports whether an engine actually came up for this source. A
	// source whose ingest port was already taken is stored but not running, and
	// a UI that showed it as configured-and-fine would be lying about why
	// nothing arrives.
	Running bool `json:"running"`
}

func (s *Server) viewSource(src *db.Source, defaultID int64) sourceView {
	return sourceView{
		Source:      src,
		PublishURLs: publishURLs(src),
		IsDefault:   src.ID == defaultID,
		// Hardcoded false, not a config flag: it becomes true when a
		// streamid-demultiplexing listener lands, and a flag would let it be
		// switched on before the enforcement exists.
		TokenEnforced: false,
		Running:       s.mgr != nil && s.mgr.Engine(src.ID) != nil,
	}
}

// publishURLs renders what to paste into an encoder.
//
// It deliberately does NOT put source.Token into these URLs, and that needs
// saying plainly because the obvious reading of "per-source ingest token" is
// that it authenticates a publisher. Today it does not, and rendering it as
// though it did would be a capability claim this build cannot honour.
//
// What actually separates and protects sources right now:
//
//   - Separation is by PORT. Each source listens on its own SRT and RTMP port,
//     because the listener is FFmpeg's and an FFmpeg SRT listener accepts one
//     caller per port and never looks at streamid.
//   - RTMP is enforced by the stream key: it is baked into the listener URL as
//     rtmp://0.0.0.0:PORT/APP/KEY, so FFmpeg rejects a publisher whose playpath
//     does not match.
//   - SRT is enforced by the passphrase, which is real AES encryption rather
//     than a shared string comparison.
//
// The token is stored and rotatable because it is the credential a one-port,
// streamid-demultiplexed ingest needs -- the shape datarhei Core uses, which
// requires an in-process SRT server rather than FFmpeg's listener. Until that
// exists the token is inert, and the UI says so.
func publishURLs(src *db.Source) map[string]string {
	const host = "<server>"
	spec := ffmpeg.IngestSpec{
		Kind:          ffmpeg.IngestKind(src.Ingest.Mode),
		SRTPort:       src.Ingest.SRT.Port,
		SRTPassphrase: src.Ingest.SRT.Passphrase,
		SRTLatencyMS:  src.Ingest.SRT.LatencyMS,
		RTMPPort:      src.Ingest.RTMP.Port,
		RTMPApp:       src.Ingest.RTMP.App,
		RTMPStreamKey: src.Ingest.RTMP.StreamKey,
		PullURL:       src.Ingest.Pull.URL,
	}
	out := map[string]string{string(src.Ingest.Mode): spec.PublicIngestURL(host)}
	if src.Ingest.Mode == db.IngestRTMP {
		// Separate field: OBS wants the server and the key in two boxes, and
		// the key is the thing that actually gates this source.
		out["streamKey"] = src.Ingest.RTMP.StreamKey
	}
	return out
}

func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListSources()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defaultID, _ := s.store.DefaultSourceID()
	out := make([]sourceView, 0, len(rows))
	for _, row := range rows {
		out = append(out, s.viewSource(row, defaultID))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetSource(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := s.store.GetSource(id)
	if err != nil {
		writeError(w, sourceStatus(err), err.Error())
		return
	}
	defaultID, _ := s.store.DefaultSourceID()
	writeJSON(w, http.StatusOK, s.viewSource(row, defaultID))
}

func (s *Server) handleCreateSource(w http.ResponseWriter, r *http.Request) {
	var row db.Source
	if !decodeJSON(w, r, &row) {
		return
	}
	row.ID = 0
	// A payload that carries no ingest block would validate against the zero
	// value -- port 0, unknown mode -- and fail with three errors that say
	// nothing useful. Start from the defaults so the smallest useful request is
	// {"name":"Vertical"} and the operator edits ports afterwards.
	if row.Ingest.Mode == "" {
		row.Ingest = db.DefaultSettings().Ingest
	}
	if err := s.store.CreateSource(&row); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Through the manager: a new source needs an engine built for it, which
	// only Sync does.
	if err := s.mgr.Reconcile(); err != nil {
		s.log.Warn("reconcile after source create", "err", err)
	}
	defaultID, _ := s.store.DefaultSourceID()
	writeJSON(w, http.StatusCreated, s.viewSource(&row, defaultID))
}

func (s *Server) handleUpdateSource(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	existing, err := s.store.GetSource(id)
	if err != nil {
		writeError(w, sourceStatus(err), err.Error())
		return
	}
	// Start from the stored row so a PUT that omits the token does not blank a
	// secret the operator has already pasted into an encoder.
	row := *existing
	if !decodeJSON(w, r, &row) {
		return
	}
	row.ID = id
	if err := s.store.UpdateSource(&row); err != nil {
		writeError(w, sourceStatus(err), err.Error())
		return
	}
	if err := s.mgr.Reconcile(); err != nil {
		s.log.Warn("reconcile after source update", "err", err)
	}
	defaultID, _ := s.store.DefaultSourceID()
	writeJSON(w, http.StatusOK, s.viewSource(&row, defaultID))
}

func (s *Server) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Deleting a source takes its destinations and renditions with it, which
	// the schema does by CASCADE. Its recordings survive with a NULL source_id.
	if err := s.store.DeleteSource(id); err != nil {
		writeError(w, sourceStatus(err), err.Error())
		return
	}
	if err := s.mgr.Reconcile(); err != nil {
		s.log.Warn("reconcile after source delete", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRotateSourceToken issues a new publish secret.
//
// The old one stops working the moment this returns, so the response carries
// the new URLs: an operator who rotates and then cannot find what to paste has
// taken their own ingest down.
func (s *Server) handleRotateSourceToken(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.store.RotateSourceToken(id); err != nil {
		writeError(w, sourceStatus(err), err.Error())
		return
	}
	row, err := s.store.GetSource(id)
	if err != nil {
		writeError(w, sourceStatus(err), err.Error())
		return
	}
	defaultID, _ := s.store.DefaultSourceID()
	writeJSON(w, http.StatusOK, s.viewSource(row, defaultID))
}

func sourceStatus(err error) int {
	if errors.Is(err, db.ErrSourceNotFound) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}
