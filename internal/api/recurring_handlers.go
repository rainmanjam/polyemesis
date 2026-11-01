package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// The HTTP handlers for recurring templates and occurrences (#18). They sit in the same route group as
// /api/v1/schedules, so the session and CSRF rules are the schedules' own. The sweep that acts on the
// records they write is recurring.go.

// calendarMaxWindow bounds one calendar read. A window wider than this is refused rather than
// truncated, so a caller never sees a partial month presented as the whole of it.
const calendarMaxWindow = 90 * 24 * time.Hour

// calendarDefaultWindow is what a calendar read with no bounds returns: the next two weeks.
const calendarDefaultWindow = 14 * 24 * time.Hour

// templateRequest is the body a template is saved from. It carries no schedule id, because the id is
// the path, and no thumbnail, because the picture has its own request.
type templateRequest struct {
	TitlePattern string `json:"titlePattern"`
	Description  string `json:"description"`
	Privacy      string `json:"privacy"`
	Category     string `json:"category"`
	Lookahead    int    `json:"lookahead"`
}

// handleGetBroadcastTemplate returns the template attached to a schedule, or not found.
func (s *Server) handleGetBroadcastTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if _, err := s.store.GetSchedule(id); err != nil {
		writeStoreError(w, err)
		return
	}
	tpl, err := s.store.GetBroadcastTemplate(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tpl)
}

// handleSaveBroadcastTemplate creates or replaces the template for a schedule. The schedule must exist,
// and the template must pass the scheduler's own validation before anything is stored.
func (s *Server) handleSaveBroadcastTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	sc, err := s.store.GetSchedule(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Only a START creates events. A template on a stop would promise a public page for a show that is
	// ending, so the schedule's action is checked before anything is read or stored.
	if sc.Action != scheduler.ActionStart {
		writeError(w, http.StatusBadRequest, "only a start schedule can carry a broadcast template; "+
			"a stop schedule has no show to announce")
		return
	}
	var req templateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	tpl := scheduler.BroadcastTemplate{
		ScheduleID:   id,
		TitlePattern: req.TitlePattern,
		Description:  req.Description,
		Privacy:      req.Privacy,
		Category:     req.Category,
		Lookahead:    req.Lookahead,
	}
	// Validated here, before the store, so an operator's mistake is a 400 that names it and a store
	// failure is never mistaken for one.
	if err := tpl.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := s.store.SaveBroadcastTemplate(tpl)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

// handleDeleteBroadcastTemplate removes a schedule's template. The schedule itself keeps running. Its
// upcoming events are cancelled by the next sweep, which sees the template is gone.
func (s *Server) handleDeleteBroadcastTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteBroadcastTemplate(id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetBroadcastThumbnail stores the picture for a template. The body is the raw image, and its
// content type must be PNG or JPEG and its bytes must start with that type's signature. A picture the
// platform would refuse is refused here, before any event is created with it.
func (s *Server) handleSetBroadcastThumbnail(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if _, err := s.store.GetBroadcastTemplate(id); err != nil {
		writeStoreError(w, err)
		return
	}
	mime := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if mime != "image/png" && mime != "image/jpeg" {
		writeError(w, http.StatusBadRequest, "a thumbnail must be a PNG or a JPEG")
		return
	}
	// The reader allows one byte past the limit, so a body of exactly the limit is read whole and a larger one is
	// seen as larger here rather than reaching the store, which refuses it with a 500.
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, scheduler.MaxThumbnailBytes+1))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig) || (err == nil && len(data) > scheduler.MaxThumbnailBytes):
		writeError(w, http.StatusRequestEntityTooLarge, "the thumbnail is larger than 2 MB")
		return
	case err != nil:
		// A body that broke off is not a picture that was too big.
		writeError(w, http.StatusBadRequest, "the thumbnail could not be read")
		return
	}
	if !matchesImageSignature(data, mime) {
		writeError(w, http.StatusBadRequest, "the body is not a "+strings.TrimPrefix(mime, "image/")+" picture")
		return
	}
	if err := s.store.SetBroadcastTemplateThumbnail(id, data, mime); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// matchesImageSignature reports whether data starts with the magic bytes of the claimed type.
func matchesImageSignature(data []byte, mime string) bool {
	switch mime {
	case "image/png":
		return bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n"))
	case "image/jpeg":
		return bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF})
	}
	return false
}

// occurrenceView is one calendar cell: the occurrence and the template title it was created with.
type occurrenceView struct {
	ID            int64                     `json:"id"`
	ScheduleID    int64                     `json:"scheduleId"`
	DestinationID int64                     `json:"destinationId"`
	At            time.Time                 `json:"at"`
	State         scheduler.OccurrenceState `json:"state"`
	Title         string                    `json:"title"`
	EventID       string                    `json:"eventId"`
}

// handleListOccurrences returns the occurrences in a window. from and to are RFC 3339; both default
// to now and two weeks on. A window wider than 90 days is refused.
func (s *Server) handleListOccurrences(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	from, to := now, now.Add(calendarDefaultWindow)
	if v := r.URL.Query().Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "from must be an RFC 3339 time")
			return
		}
		from = t
	}
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "to must be an RFC 3339 time")
			return
		}
		to = t
	}
	if !to.After(from) || to.Sub(from) > calendarMaxWindow {
		writeError(w, http.StatusBadRequest, "the calendar window must run forward and be at most 90 days")
		return
	}
	rows, err := s.store.ListOccurrences(from, to)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := make([]occurrenceView, 0, len(rows))
	for _, o := range rows {
		out = append(out, occurrenceView{
			ID: o.ID, ScheduleID: o.ScheduleID, DestinationID: o.DestinationID, At: o.At,
			State: o.State, Title: o.Title, EventID: o.PlatformEventID,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSkipOccurrence removes one occurrence: its platform event is cancelled and it is not created
// again. The schedule still fires for it; disable the schedule to stop the show outright.
func (s *Server) handleSkipOccurrence(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.skipOccurrence(r.Context(), id, time.Now()); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeStoreError(w, err)
			return
		}
		// A refusal the operator can act on (the show started, is live, or is still being created) is a
		// conflict with the current state, not a platform failure.
		if errors.Is(err, errOccurrenceRefused) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, oauth.ErrScheduledNotCancellable) {
			writeError(w, http.StatusConflict, "this show's event cannot be removed from here: it has started, "+
				"its destination is live, or the platform will not delete it; the platform keeps it as it is")
			return
		}
		writeError(w, http.StatusBadGateway, "the event could not be cancelled: "+alerts.Redact(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "state": scheduler.OccurrenceSkipped})
}

// occurrenceTitleRequest is the body a single occurrence's title is edited from.
type occurrenceTitleRequest struct {
	Title string `json:"title"`
}

// handleOccurrenceTitle edits the title of ONE occurrence's event. The template's description, privacy
// and category are sent again, because the platform write carries every field it changes, and the
// edit must not clear what the operator set on the template.
func (s *Server) handleOccurrenceTitle(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req occurrenceTitleRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		writeError(w, http.StatusBadRequest, "a title is required")
		return
	}
	if len([]rune(title)) > scheduler.MaxTitleLen {
		writeError(w, http.StatusBadRequest, "the title is longer than the platform allows")
		return
	}
	occ, err := s.store.GetOccurrence(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if occ.State != scheduler.OccurrenceUpcoming || occ.PlatformEventID == "" {
		writeError(w, http.StatusConflict, "only an upcoming occurrence has an event to retitle")
		return
	}
	// The same refusals skip makes. A show that has started is the platform's record of it, and a destination on
	// air may be bound to this very event: a retitle there is an edit of the live broadcast, which is the go-live
	// composer's job, and re-sending the template's privacy could undo a change made at go-live.
	if !occ.At.After(time.Now()) {
		writeError(w, http.StatusConflict, "this show has already started, so its event is past retitling from here")
		return
	}
	tpl, err := s.store.GetBroadcastTemplate(occ.ScheduleID)
	if err != nil {
		writeError(w, http.StatusConflict, "this occurrence's template was deleted, so its event is an orphan")
		return
	}
	d, err := s.store.GetDestination(occ.DestinationID)
	if err != nil {
		writeError(w, http.StatusConflict, "this occurrence's destination no longer exists")
		return
	}
	sb, ok := s.providers.ScheduledBroadcastsFor(d.Platform)
	if !ok || d.AccountID == nil {
		writeError(w, http.StatusConflict, "this destination cannot retitle a scheduled event")
		return
	}
	ed, ok := sb.(oauth.ScheduledEditor)
	if !ok {
		writeError(w, http.StatusConflict, "this destination cannot retitle a scheduled event")
		return
	}
	if d.Enabled || s.destinationLive(d.ID) {
		writeError(w, http.StatusConflict, "the destination is live, so its event may be on air; retitle it from the go-live composer")
		return
	}
	// Bounded, and not tied to the browser: a disconnect mid-write must not leave the title half-applied, and a
	// stuck platform call must not hold the request for ever.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), templateCallTimeout)
	defer cancel()
	acct, err := s.tokenFor(ctx, *d.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "the connected account could not be used: "+alerts.Redact(err.Error()))
		return
	}
	warnings, err := ed.EditScheduledBroadcast(ctx, acct.AccessToken, occ.PlatformEventID, oauth.ScheduledEdit{
		Title:       title,
		Description: tpl.Description,
		Privacy:     templatePrivacy(*tpl, d),
		Category:    tpl.Category,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "the platform refused the new title: "+alerts.Redact(err.Error()))
		return
	}
	for i := range warnings {
		warnings[i] = alerts.Redact(warnings[i])
	}
	if err := s.store.SetOccurrenceTitle(id, title); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "title": title, "warnings": warnings})
}
