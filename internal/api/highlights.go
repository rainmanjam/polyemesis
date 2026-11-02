package api

// Highlight suggestions (Wave 4 #5a): queue a scan, read the review queue,
// accept a suggestion into an ordinary clip export, or dismiss it.
//
// NOTHING HERE CLIPS ON ITS OWN. The scan is a job that stores suggestions.
// The only route that creates a clip.export job is Accept, an operator's
// explicit action, and it goes through submitClipExport -- the same function
// the editor's Export button uses -- after DecideHighlight has won the
// one-shot pending -> accepted transition, so a double click makes one clip.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/highlights"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// highlightModelTimeout bounds the one scoring call a scan makes. Longer than
// moderation's four seconds: it is a dozen excerpts, not one message, and
// nobody is waiting on it in chat.
const highlightModelTimeout = 60 * time.Second

// HighlightModel returns the scan's model factory: the operator's PRIMARY
// automod connector -- its endpoint, model and key -- when the separate
// "Use AI for highlight scoring" switch is on and the connector speaks the
// OpenAI-compatible protocol, charged to budget -- the scan's own, so a post-pass over a
// recording can never spend the allowance live moderation runs on.
//
// Exported because the scan worker is wired in cmd/polyemesis, and the
// settings-to-connector translation must not exist twice.
func HighlightModel(store *db.DB, box *secrets.Box, budget *automod.Budget, log *slog.Logger) func() (highlights.Completer, string) {
	return func() (highlights.Completer, string) {
		cfg, why := highlightModelConfig(store, box, log)
		if why != "" {
			return nil, why
		}
		return func(ctx context.Context, system, user string) (string, error) {
			return automod.Complete(ctx, cfg, budget, system, user)
		}, ""
	}
}

// highlightModelConfig is the connector the scan would use, or why there is
// none. The reason is fixed text: it reaches the review queue, and the
// endpoint (which may carry a key in its query string) never does.
func highlightModelConfig(store *db.DB, box *secrets.Box, log *slog.Logger) (automod.ModelConfig, string) {
	if store == nil {
		return automod.ModelConfig{}, "there is no settings store"
	}
	settings, err := store.GetSettings()
	if err != nil {
		return automod.ModelConfig{}, "the automod settings could not be read"
	}
	conn := settings.Automod.Model
	switch {
	case !settings.Automod.HighlightScoring:
		// Its own consent (owner decision D): the Automod model block being on
		// for chat says nothing about sending what was said on stream.
		return automod.ModelConfig{}, "AI highlight scoring is switched off in Automod settings"
	case protocolOf(conn) == automod.ProtocolJev:
		return automod.ModelConfig{}, "the Automod model uses the Jev protocol, which answers moderation questions only"
	case conn.Endpoint == "":
		return automod.ModelConfig{}, "the Automod model has no endpoint"
	}
	cfg, _ := modelConfigFor(conn, settings.Automod.Model)
	key, binding, err := store.GetAutomodKeyBound(box, string(automod.SlotPrimary))
	if err != nil {
		if log != nil {
			log.Warn("highlight scoring: automod key unreadable; scoring without it", "err", err)
		}
	}
	cfg.APIKey, cfg.KeyBinding = key, binding
	cfg.Timeout = highlightModelTimeout
	return cfg, ""
}

// ---------------------------------------------------------------- the scan

type highlightScanBody struct {
	// UseModel asks the Automod provider to score the candidates. It sends
	// the transcript excerpts to that provider, so it is never the default.
	UseModel bool `json:"useModel"`
}

func (s *Server) handleScanHighlights(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body highlightScanBody
	if r.ContentLength != 0 && !decodeJSON(w, r, &body) {
		return
	}
	tl, err := s.clipTimeline(id)
	if err != nil {
		s.writeClipError(w, err)
		return
	}
	params := highlights.JobParams{RecordingID: tl.highlightKey(), UseModel: body.UseModel}
	for _, p := range tl.parts {
		part := highlights.Part{RecordingID: p.rec.ID, Path: p.path, StartMS: p.startMS, DurationMS: p.rec.DurationMS}
		if !p.rec.StartedAt.IsZero() {
			part.StartedAtMS = p.rec.StartedAt.UnixMilli()
		}
		params.Parts = append(params.Parts, part)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	scan := jobs.Job{
		Kind:   highlights.JobKind,
		Target: jobs.RecordingTarget(tl.highlightKey()),
		Params: raw,
		// Unique: pressing Scan twice is one scan.
		Unique:   true,
		Priority: jobs.PriorityUser,
	}
	job, created, err := s.jobq.Submit(scan)
	if err == nil && !created && body.UseModel && !scanUsesModel(job.Params) {
		// The press asked for scoring and found a plain scan already active.
		// A scan that has not started is upgraded in place; one already
		// running has read its params, so scoring gets a scan of its own.
		var upgraded bool
		if upgraded, err = s.store.UpgradeQueuedJobParams(job.ID, raw); err == nil {
			if upgraded {
				job.Params = raw
			} else {
				scan.Unique = false
				job, _, err = s.jobq.Submit(scan)
			}
		}
	}
	if err != nil {
		s.writeClipError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.clipJobView(job))
}

// ----------------------------------------------------------- the review queue

type highlightView struct {
	db.Highlight
	// Signals is decoded so the UI reads typed fields, not a string.
	Signals []highlights.Signal `json:"signals"`
}

type highlightsView struct {
	Suggestions []highlightView `json:"suggestions"`
	// ModelAvailable says whether a scan could be scored; ModelReason why not.
	ModelAvailable bool   `json:"modelAvailable"`
	ModelReason    string `json:"modelReason,omitempty"`
}

func viewOfHighlight(h db.Highlight) highlightView {
	v := highlightView{Highlight: h, Signals: []highlights.Signal{}}
	_ = json.Unmarshal(h.Signals, &v.Signals)
	return v
}

func (s *Server) handleListHighlights(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	tl, err := s.clipTimeline(id)
	if err != nil {
		s.writeClipError(w, err)
		return
	}
	list, err := s.store.ListHighlights(tl.highlightKey())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	view := highlightsView{Suggestions: make([]highlightView, 0, len(list))}
	for _, h := range list {
		view.Suggestions = append(view.Suggestions, viewOfHighlight(h))
	}
	_, why := highlightModelConfig(s.store, s.box, s.log)
	view.ModelAvailable, view.ModelReason = why == "", why
	writeJSON(w, http.StatusOK, view)
}

// highlightAcceptBody is a clip export body whose in and out points default to
// the suggestion's. Everything else -- mode, tracks, title, vertical -- is the
// editor's, so accepting can produce a straight cut or a short.
type highlightAcceptBody struct {
	clipRequestBody
}

type highlightDecisionView struct {
	Highlight highlightView `json:"highlight"`
	Job       any           `json:"job,omitempty"`
}

func (s *Server) handleAcceptHighlight(w http.ResponseWriter, r *http.Request) {
	if !s.requireJobs(w) {
		return
	}
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body highlightAcceptBody
	if r.ContentLength != 0 && !decodeJSON(w, r, &body) {
		return
	}
	h, err := s.store.GetHighlight(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if h.State != db.HighlightPending {
		writeError(w, http.StatusConflict, db.ErrHighlightDecided.Error())
		return
	}
	req := body.clipRequestBody
	if req.InMS == 0 && req.OutMS == 0 {
		req.InMS, req.OutMS = h.StartMS, h.EndMS
	}
	// Plan BEFORE claiming: an impossible cut is a 400 that leaves the
	// suggestion pending, not one that eats it.
	plan, tl, ok := s.planClipBody(w, r, h.RecordingID, req)
	if !ok {
		return
	}
	if _, err := s.store.DecideHighlight(id, db.HighlightAccepted); err != nil {
		if errors.Is(err, db.ErrHighlightDecided) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeStoreError(w, err)
		return
	}
	job, err := s.submitClipExport(plan, tl)
	if err != nil {
		// The clip was never queued, so the suggestion is not accepted either.
		if rerr := s.store.ReopenHighlight(id); rerr != nil {
			s.log.Warn("could not hand a highlight back after its clip failed to queue", "id", id, "err", rerr)
		}
		s.writeClipError(w, err)
		return
	}
	if err := s.store.SetHighlightJob(id, job.ID); err != nil {
		s.log.Warn("highlight accepted and queued, but its job id was not recorded", "id", id, "job", job.ID, "err", err)
	}
	h, _ = s.store.GetHighlight(id)
	writeJSON(w, http.StatusAccepted, highlightDecisionView{Highlight: viewOfHighlight(h), Job: s.clipJobView(job)})
}

func (s *Server) handleDismissHighlight(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	h, err := s.store.DecideHighlight(id, db.HighlightDismissed)
	if err != nil {
		if errors.Is(err, db.ErrHighlightDecided) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, highlightDecisionView{Highlight: viewOfHighlight(h)})
}

// highlightKey is the recording a timeline's suggestions are stored under: its
// FIRST file. A stream recorded as hourly files is one session and one review
// queue, so opening it from any of its files must scan, store and list under
// the same id -- the file that happened to be opened would give each file a
// queue of its own, and a second scan that the Unique fold could not see.
func (t clipTimeline) highlightKey() int64 { return t.parts[0].rec.ID }

// scanUsesModel reports whether a scan job's params ask for model scoring.
func scanUsesModel(raw json.RawMessage) bool {
	var p highlights.JobParams
	return json.Unmarshal(raw, &p) == nil && p.UseModel
}
