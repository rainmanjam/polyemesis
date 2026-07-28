package api

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/srtserver"
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
	// Publishing reports whether an encoder is live on the shared listener for
	// this source right now.
	Publishing bool `json:"publishing"`
	// Link is this publisher's uplink health, when one is connected on the
	// shared listener. Surfaced per source because with several programmes on
	// one install, "why is it breaking up" is a question about one encoder's
	// uplink and not about the server -- and answering it per programme is
	// something Restreamer's UI does not do.
	Link *srtserver.LinkStats `json:"link,omitempty"`
	// Destinations and Renditions are what a delete would take with it.
	//
	// Counts rather than prose, and computed server-side rather than guessed by
	// the UI: a confirmation that says "this also removes 3 destinations and 1
	// rendition" is a decision, and one that says "and its destinations" is a
	// click. Shingo's fixed-value method -- confirm a number.
	Destinations int `json:"destinations"`
	Renditions   int `json:"renditions"`
	// Running reports whether an engine actually came up for this source. A
	// source whose ingest port was already taken is stored but not running, and
	// a UI that showed it as configured-and-fine would be lying about why
	// nothing arrives.
	Running bool `json:"running"`
}

func (s *Server) viewSource(src *db.Source, defaultID int64) sourceView {
	var link *srtserver.LinkStats
	publishing := false
	// Derived from the RUNNING listener, never from configuration. Tokens are
	// how every SRT source is addressed now, so the only way they are not
	// enforced is that the listener is not up -- and reporting "enforced" while
	// nothing is bound is the exact false assurance this field exists to
	// prevent.
	tokenEnforced := src.Ingest.Mode == db.IngestSRT &&
		s.mgr != nil && s.mgr.SharedIngestListening()
	// The ports are install-wide, so the publish URL comes from the settings
	// rather than from the source. Defaults on a read failure: a URL with the
	// wrong port is more useful than no URL at all, and the Sources page is
	// where an operator goes precisely when something is not working.
	listeners := db.DefaultSettings().Listeners
	if st, err := s.store.GetSettings(); err == nil {
		listeners = st.Listeners
	}
	if s.mgr != nil {
		publishing = s.mgr.SharedIngestPublishing(src.ID)
		for _, l := range s.mgr.SRTLinks() {
			if l.SourceID == src.ID {
				stat := l
				link = &stat
				break
			}
		}
	}
	return sourceView{
		Publishing:    publishing,
		Link:          link,
		Source:        src,
		PublishURLs:   publishURLs(src, listeners),
		IsDefault:     src.ID == defaultID,
		TokenEnforced: tokenEnforced,
		Running:       s.mgr != nil && s.mgr.Engine(src.ID) != nil,
	}
}

// publishURLs is what the operator pastes into an encoder.
//
// The SRT URL carries the TOKEN as the streamid, because the token is now the
// only thing that identifies a source: every source is reached on one listener
// and told apart by it. A URL without the token addresses nothing.
//
// What separates and protects each source:
//
//   - SRT: the token. One listener, demultiplexed by streamid, matched in
//     constant time against every source's current and grace-period token. A
//     publisher presenting nothing, or something unrecognised, is refused with
//     a typed reason rather than quietly accepted into the wrong programme.
//   - SRT, additionally: the passphrase, which is real AES encryption rather
//     than a string comparison.
//   - RTMP: the stream key, baked into the listener URL as
//     rtmp://0.0.0.0:PORT/APP/KEY so FFmpeg rejects a mismatched playpath.
//     RTMP has one listener and therefore one source; see checkRTMPExclusive.
func publishURLs(src *db.Source, listeners db.ListenerSettings) map[string]string {
	const host = "<server>"
	spec := ffmpeg.IngestSpec{
		Kind:          ffmpeg.IngestKind(src.Ingest.Mode),
		SRTPort:       listeners.SRTPort,
		SRTPassphrase: src.Ingest.SRT.Passphrase,
		SRTLatencyMS:  src.Ingest.SRT.LatencyMS,
		RTMPPort:      listeners.RTMPPort,
		RTMPApp:       src.Ingest.RTMP.App,
		RTMPStreamKey: src.Ingest.RTMP.StreamKey,
		PullURL:       src.Ingest.Pull.URL,
	}
	u := spec.PublicIngestURL(host)
	if src.Ingest.Mode == db.IngestSRT && src.Token != "" {
		// The streamid IS the address. Appended here rather than inside
		// IngestSpec because the spec describes what the SERVER binds, and the
		// server binds one socket for every source -- the token only means
		// something on the publisher's side of it.
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + "streamid=" + url.QueryEscape(src.Token)
	}
	out := map[string]string{string(src.Ingest.Mode): u}
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
	// Enabled before decoding, not after: a bool cannot distinguish "absent"
	// from "false" once decoded, and seeding it here means an omitted field
	// keeps true while an explicit "enabled": false still wins.
	//
	// The default has to be true. Someone who adds a source has just said they
	// want it; shipping it disabled means the encoder is refused with "source
	// disabled" and the operator has no reason to suspect the thing they just
	// created is off. That is exactly how it failed the first time this path
	// was exercised end to end.
	row := db.Source{Enabled: true}
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
