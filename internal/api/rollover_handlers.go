package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/obsbridge"
)

// The planned-rollover API (#19). Four routes: the platform caps the countdown counts to, one destination's settings
// and its countdown, the save of those settings, and a manual rollover. The sweep is rollover.go.

// rolloverLimitView is one platform cap as the countdown card shows it. Source is the link the cap is read from, and
// Status says how well it is established, so a reported cap is shown as reported rather than as a fact.
type rolloverLimitView struct {
	PresetID string  `json:"presetId"`
	Kind     string  `json:"kind"`
	Hours    float64 `json:"hours"`
	Status   string  `json:"status"`
	Source   string  `json:"source,omitempty"`
	Note     string  `json:"note,omitempty"`
}

// rolloverView is one destination's rollover as the card reads it.
type rolloverView struct {
	Settings     db.RolloverSettings `json:"settings"`
	Platform     string              `json:"platform"`
	Action       string              `json:"action"`
	Limit        *rolloverLimitView  `json:"limit,omitempty"`
	CutoffAt     *time.Time          `json:"cutoffAt,omitempty"`
	Delivering   bool                `json:"delivering"`
	Due          bool                `json:"due"`
	DueTrigger   string              `json:"dueTrigger,omitempty"`
	NextAt       *time.Time          `json:"nextAt,omitempty"`
	PlanReason   string              `json:"planReason,omitempty"`
	ReasonIfNone string              `json:"reasonIfNone,omitempty"`
}

// rolloverActionFor names what a platform's rollover does, for the card. "automatic" acts on its own, "countdown"
// shows the cap and nothing else, and the card says so rather than implying an action that will not happen.
func (s *Server) rolloverActionFor(p db.Platform) string {
	if s.rollover != nil && s.rollover.actsOn(p) {
		return "automatic"
	}
	return "countdown"
}

// handleListRolloverLimits serves the table of platform caps. It is read-only and carries no destination.
func (s *Server) handleListRolloverLimits(w http.ResponseWriter, r *http.Request) {
	out := []rolloverLimitView{}
	for _, l := range oauth.PlatformLimits() {
		out = append(out, rolloverLimitView{
			PresetID: l.PresetID, Kind: string(l.Kind), Hours: l.Hours, Status: string(l.Status),
			Source: l.Source, Note: l.Note,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetRollover returns one destination's settings and where its countdown stands.
func (s *Server) handleGetRollover(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	d, err := s.store.GetDestination(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	cfg, err := s.store.GetRollover(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.rolloverViewFor(d, cfg, time.Now()))
}

// rolloverViewFor builds the card's view. Everything here is a read: the planner is asked what it would do, and
// nothing is acted on.
func (s *Server) rolloverViewFor(d *db.Destination, cfg db.RolloverSettings, now time.Time) rolloverView {
	v := rolloverView{Settings: cfg, Platform: string(d.Platform), Action: s.rolloverActionFor(d.Platform)}
	v.Delivering = s.destinationDelivering(d.ID)
	limit, hasLimit := oauth.RolloverLimitFor(d.Platform)
	if hasLimit {
		v.Limit = &rolloverLimitView{
			PresetID: limit.PresetID, Kind: string(limit.Kind), Hours: limit.Hours,
			Status: string(limit.Status), Source: limit.Source, Note: limit.Note,
		}
		if cutoff, ok := limit.CutoffFor(cfg.SessionStartedAt); ok {
			v.CutoffAt = &cutoff
		}
	}
	if v.Delivering {
		c := s.rolloverCoordinatorOrNew()
		plan := oauth.PlanRollover(c.planInput(cfg, d.Platform, now))
		v.Due, v.DueTrigger = plan.Due, string(plan.Trigger)
		if !plan.Next.IsZero() {
			next := plan.Next
			v.NextAt = &next
		}
		v.PlanReason = plan.Reason
	} else {
		v.ReasonIfNone = "the destination is not delivering, so there is no countdown to run"
	}
	return v
}

// rolloverCoordinatorOrNew returns the server's coordinator. A server without a database has none, and the view of
// it is then a plain planner over the stored settings, so a read never needs the sweep to be running.
func (s *Server) rolloverCoordinatorOrNew() *rolloverCoordinator {
	if s.rollover != nil {
		return s.rollover
	}
	return newRolloverCoordinator(s.log, s.store, rolloverHost{})
}

// rolloverSettingsBody is what the settings screen sends. Every field is optional on the wire and validated below.
type rolloverSettingsBody struct {
	Enabled       bool   `json:"enabled"`
	MarginMinutes int    `json:"marginMinutes"`
	QuietStart    string `json:"quietStart"`
	QuietEnd      string `json:"quietEnd"`
	QuietZone     string `json:"quietZone"`
	OBSScene      string `json:"obsScene"`
	ChatNotice    bool   `json:"chatNotice"`
}

// handleSaveRollover writes one destination's settings. It writes only the configuration; the session and the last
// result are left as the sweep wrote them.
func (s *Server) handleSaveRollover(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if _, err := s.store.GetDestination(id); err != nil {
		writeStoreError(w, err)
		return
	}
	var body rolloverSettingsBody
	if !decodeJSON(w, r, &body) {
		return
	}
	cfg := oauth.RolloverConfig{
		MarginMinutes: body.MarginMinutes, QuietStart: body.QuietStart, QuietEnd: body.QuietEnd,
		QuietZone: body.QuietZone, OBSScene: body.OBSScene,
	}
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.SaveRolloverConfig(db.RolloverSettings{
		DestinationID: id, Enabled: body.Enabled, MarginMinutes: body.MarginMinutes,
		QuietStart: body.QuietStart, QuietEnd: body.QuietEnd, QuietZone: body.QuietZone, OBSScene: body.OBSScene,
		ChatNotice: body.ChatNotice,
	}, time.Now()); err != nil {
		writeStoreError(w, err)
		return
	}
	s.handleGetRolloverAfterWrite(w, id)
}

// handleGetRolloverAfterWrite answers a save with the view as it now stands.
func (s *Server) handleGetRolloverAfterWrite(w http.ResponseWriter, id int64) {
	d, err := s.store.GetDestination(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	cfg, err := s.store.GetRollover(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.rolloverViewFor(d, cfg, time.Now()))
}

// handleRunRollover carries out a rollover now, on one destination, when an operator asks. It is refused, by name,
// for a destination that is not delivering or whose platform has no action.
func (s *Server) handleRunRollover(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if s.rollover == nil {
		writeError(w, http.StatusConflict, "planned rollover is not running on this server")
		return
	}
	err = s.rollover.RunNow(r.Context(), id)
	switch {
	case errors.Is(err, errRolloverNotLive), errors.Is(err, errRolloverNoAction), errors.Is(err, errRolloverTooSoon):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such destination")
	case errors.Is(err, errRolloverFailed):
		// The platform answered and refused. The message is already redacted by act.
		writeError(w, http.StatusBadGateway, err.Error())
	case err != nil:
		// A failed live-start read reaches here unredacted by act, and its text can carry a platform response body.
		writeError(w, http.StatusInternalServerError, alerts.Redact(err.Error()))
	default:
		// The call is synchronous, so the rollover has finished by the time this answers.
		writeJSON(w, http.StatusOK, map[string]string{"status": "rolled over"})
	}
}

// destinationDelivering is the read-only predicate the coordinator uses: the destination's process is running, is not
// stalled, and has no error. It never starts, stops or restarts anything.
func (s *Server) destinationDelivering(id int64) bool {
	if s.mgr == nil {
		return false
	}
	for _, ds := range s.mgr.DestinationStatuses() {
		if ds.ID != id {
			continue
		}
		return ds.Enabled && ds.Error == "" && ds.Process != nil && ds.Process.State == "running" && !ds.Process.Stalled
	}
	return false
}

// restartDestinationForRollover cycles one destination through its owning engine, the same call the operator's
// restart button makes. It is the only path from a rollover to a process, and it is used for Twitch only.
func (s *Server) restartDestinationForRollover(id int64) error {
	row, err := s.store.GetDestination(id)
	if err != nil {
		return err
	}
	eng := s.engineForSource(row.SourceID)
	if eng == nil {
		return errors.New("the programme this destination carries is not running")
	}
	return eng.RestartDestination(id)
}

// sendRolloverNotice posts the rollover notice to the chats of one platform only, and to none other. A
// platform with no connected chat is an error, so the log says why no line appeared.
func (s *Server) sendRolloverNotice(ctx context.Context, p db.Platform, text string) error {
	if s.chat == nil {
		return errors.New("no chat is running on this server")
	}
	results, err := s.chat.SendEach(ctx, func(pp db.Platform, _ string) string {
		if pp == p {
			return text
		}
		return ""
	})
	if err != nil {
		return err
	}
	if len(results) == 0 {
		return errors.New(string(p) + " has no connected chat to post the notice to")
	}
	for _, r := range results {
		if !r.OK {
			return errors.New(alerts.Redact(r.Detail))
		}
	}
	return nil
}

// twitchStreamStart reads when the channel's live stream began, from Helix GET /helix/streams via the same
// Stats call the viewer-count read uses. An unreadable or missing start is an error, and the caller falls back.
func (s *Server) twitchStreamStart(ctx context.Context, accessToken string) (time.Time, error) {
	st, ok := s.providers.StatsFor(db.PlatformTwitch)
	if !ok {
		return time.Time{}, errors.New("twitch has no stats read")
	}
	creds, err := s.store.GetPlatformCreds(s.box, db.PlatformTwitch)
	if err != nil {
		return time.Time{}, err
	}
	stats, err := st.Stats(ctx, creds.ClientID, accessToken)
	if err != nil {
		return time.Time{}, err
	}
	if stats == nil || stats.StartedAt == nil {
		return time.Time{}, errors.New("helix did not report a start time")
	}
	return *stats.StartedAt, nil
}

// publishRollover sends one rollover event to the webhook and the events stream, the same two channels a broadcast
// fault uses. Neither can stop a stream.
func (s *Server) publishRollover(ev hooks.Event) {
	if s.hooks != nil {
		s.hooks.Publish(ev)
	}
	if s.stream != nil {
		s.stream.Publish(eventstream.FromHook(ev))
	}
}

// RolloverLoop runs the planned-rollover sweep until ctx ends, and feeds it the OBS scene switches it needs for the
// scene trigger. It is started beside LifecycleLoop.
func (s *Server) RolloverLoop(ctx context.Context) {
	if s.rollover == nil || s.stream == nil {
		return
	}
	sub, _, _ := s.stream.Subscribe(eventstream.Filter{Topics: []eventstream.Topic{eventstream.TopicOBS}}, "")
	defer sub.Close()
	tick := time.NewTicker(rolloverSweepInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case env, ok := <-sub.C:
			if !ok {
				return
			}
			if sd, isScene := env.Data.(obsbridge.SceneData); isScene && env.Type == eventstream.TypeOBSScene {
				s.rollover.noteScene(sd.Scene, sd.Sync, env.At)
			}
		case <-tick.C:
			s.rollover.sweepOnce(ctx)
		}
	}
}
