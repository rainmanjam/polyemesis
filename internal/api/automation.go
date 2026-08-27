package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/clips"
	"github.com/rainmanjam/polyemesis/internal/meters"
	"github.com/rainmanjam/polyemesis/internal/recording"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// This file is the HTTP surface for everything that runs without an operator
// watching: alert webhooks, schedules, the rolling clip buffer, the loudness
// compliance monitor, and the stem files a recording session left beside its
// master.

// ------------------------------------------------------------- alert rules

// alertRuleRequest is a PATCH-shaped body: every field is a pointer, and an
// omitted one leaves the stored value alone.
//
// It exists instead of decoding straight into alerts.Rule because of the URL.
// A webhook URL carries its secret in the path, so alerts.Rule marshals a
// masked one and refuses to unmarshal any at all — which means the only way a
// client can change the endpoint is a field the API defines itself.
type alertRuleRequest struct {
	Name               *string          `json:"name"`
	Enabled            *bool            `json:"enabled"`
	URL                *string          `json:"url"`
	Format             *alerts.Format   `json:"format"`
	Events             *[]alerts.Type   `json:"events"`
	MinSeverity        *alerts.Severity `json:"minSeverity"`
	DebounceSeconds    *int             `json:"debounceSeconds"`
	MinIntervalSeconds *int             `json:"minIntervalSeconds"`
	// AllowPrivateTarget is the SSRF opt-in. A pointer like every field here,
	// so PATCHing one unrelated field does not silently clear a guard the
	// operator turned off on purpose -- or, worse, turn one off they never
	// touched. Absent means "leave it alone"; on create the zero value is
	// false, which is the direction that fails closed.
	AllowPrivateTarget *bool `json:"allowPrivateTarget"`
}

// applyTo folds the request onto an existing rule.
func (q alertRuleRequest) applyTo(r alerts.Rule) alerts.Rule {
	if q.Name != nil {
		r.Name = *q.Name
	}
	if q.Enabled != nil {
		r.Enabled = *q.Enabled
	}
	if q.URL != nil {
		// The client was shown "https://host/[redacted]" and may well hand it
		// back untouched — every form does, because the field it renders is the
		// only URL it has. Storing that string would point the rule at a URL
		// that has never existed and break alerting silently, so a value still
		// carrying the mask means "unchanged" rather than an error nobody could
		// have avoided.
		if u := strings.TrimSpace(*q.URL); !strings.Contains(u, alerts.Mask) {
			r.URL = u
		}
	}
	if q.Format != nil {
		r.Format = *q.Format
	}
	if q.Events != nil {
		r.Events = append([]alerts.Type(nil), *q.Events...)
	}
	if q.MinSeverity != nil {
		r.MinSeverity = *q.MinSeverity
	}
	if q.DebounceSeconds != nil {
		r.DebounceSeconds = *q.DebounceSeconds
	}
	if q.MinIntervalSeconds != nil {
		r.MinIntervalSeconds = *q.MinIntervalSeconds
	}
	if q.AllowPrivateTarget != nil {
		r.AllowPrivateTarget = *q.AllowPrivateTarget
	}
	return r
}

func (s *Server) handleListAlertRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.store.ListAlertRules()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

func (s *Server) handleGetAlertRule(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	rule, err := s.store.GetAlertRule(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func (s *Server) handleCreateAlertRule(w http.ResponseWriter, r *http.Request) {
	var req alertRuleRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// A new rule with no explicit enabled flag is on: somebody who just typed a
	// webhook URL in wants it to alert.
	rule := req.applyTo(alerts.Rule{Enabled: true}).Normalized()
	if err := rule.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := s.store.CreateAlertRule(&rule)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleUpdateAlertRule(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	existing, err := s.store.GetAlertRule(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req alertRuleRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	rule := req.applyTo(*existing).Normalized()
	if err := rule.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := s.store.UpdateAlertRule(&rule)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteAlertRule(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteAlertRule(id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleTestAlertRule posts a test message to one rule's endpoint, right now.
//
// It reads the rule from the store rather than taking one from the body, so the
// URL under test is the URL that will really be used — a test that passes
// against a URL the client supplied proves nothing about the stored one.
func (s *Server) handleTestAlertRule(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	rule, err := s.store.GetAlertRule(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// engOrNil, not eng(): this route carries no requireSource, so it is reached
	// on a build with no manager at all and eng() would panic inside
	// Manager.Default before there is an engine to test. Read ONCE and use the
	// answer.
	n := s.engOrNil().Alerts()
	if n == nil {
		// IT IS THE NO-SOURCE REFUSAL WEARING A SUBSYSTEM'S NAME. Engine.New
		// always builds an alerter, so Alerts() answers nil for exactly one
		// reason: there is no engine, which on this install means there is no
		// programme. "The alert notifier is not running" sent the operator
		// looking for a subsystem to restart; the code is what lets the rule
		// editor say the true thing instead.
		//
		// The route carries no requireSource because everything else about an
		// alert rule -- creating it, editing it, deleting it -- is install-wide
		// and works perfectly well before the first source exists. Only the
		// SEND needs a notifier.
		writeNoSource(w)
		return
	}
	if err := n.Test(r.Context(), *rule); err != nil {
		// 502, not 500: the failure is the operator's endpoint refusing the
		// message, and the message says which. Errors out of the notifier are
		// already redacted.
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// handleAlertsMeta is the catalogue the rule editor builds its pickers from,
// so a subscribable event type is added in exactly one place.
func (s *Server) handleAlertsMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"events":     alerts.AllTypes(),
		"formats":    []alerts.Format{alerts.FormatJSON, alerts.FormatDiscord, alerts.FormatSlack},
		"severities": []alerts.Severity{alerts.SeverityInfo, alerts.SeverityWarning, alerts.SeverityCritical},
		"bounds": map[string]int{
			"minDebounceSeconds": alerts.MinDebounceSeconds,
			"maxDebounceSeconds": alerts.MaxDebounceSeconds,
			"minIntervalSeconds": alerts.MinIntervalSeconds,
			"maxIntervalSeconds": alerts.MaxIntervalSeconds,
			"maxNameLen":         alerts.MaxRuleNameLen,
			"maxUrlLen":          alerts.MaxURLLen,
		},
		"stats": s.alertStats(),
	})
}

// alertStats is the delivery counters for the WHOLE INSTALL.
//
// EVERY notifier, not the default engine's. Alert rules are install-wide -- one
// alert_rules table, read by every engine's notifier -- but the counters are
// not: each notifier keeps its own. So the rule editor's "sent / failed /
// coalesced" panel showed roughly one programme's share of the truth on a
// two-programme install, with nothing saying it was a share. Summing is the
// only reading that matches what the panel claims to describe, which is what
// this install has delivered.
//
// The two non-counters are handled as what they are: LastSent is the most
// recent across notifiers, and LastError the most recent non-empty one, because
// "when did anything last get through" and "what went wrong last" are questions
// about the install, not about a programme.
func (s *Server) alertStats() alerts.Stats {
	var out alerts.Stats
	if s.mgr == nil {
		return out
	}
	var errAt time.Time
	for _, e := range s.mgr.Engines() {
		st := e.Alerts().Stats()
		out.Queued += st.Queued
		out.Dropped += st.Dropped
		out.Coalesced += st.Coalesced
		out.Pending += st.Pending
		out.Sent += st.Sent
		out.Failed += st.Failed
		out.Retries += st.Retries
		out.Deferred += st.Deferred
		if st.LastSent.After(out.LastSent) {
			out.LastSent = st.LastSent
		}
		if st.LastError != "" && (errAt.IsZero() || st.LastSent.After(errAt)) {
			out.LastError, errAt = st.LastError, st.LastSent
		}
	}
	return out
}

// ---------------------------------------------------------------- schedules

// scheduleView is a stored schedule plus the two things only the server can
// work out: when it fires next, and what its wall-clock time reads as.
type scheduleView struct {
	scheduler.Schedule
	// NextAt is absent for a one-shot that has already run, and for a schedule
	// whose time zone no longer resolves — both of which the UI must show as
	// "never again" rather than as a date it invented.
	NextAt    *time.Time `json:"nextAt,omitempty"`
	LocalTime string     `json:"localTime"`
	// Warnings names anything about this schedule that will not work the way
	// an operator might expect. Never a refusal: everything here still saves
	// and still runs.
	Warnings []string `json:"warnings,omitempty"`
}

// scheduleWarnings names what this schedule cannot do. It never blocks a save.
//
// Only KindOnce can trip Facebook's bound: the NEXT occurrence of a daily
// schedule is at most a day away and of a weekly one at most seven days, BY
// DEFINITION. An earlier draft of the roadmap claimed weekly schedules collided
// with this; they cannot, and warning on every weekly show would be noise that
// teaches people to skip the warning that matters.
//
// It warns rather than refusing because the schedule works either way -- what
// the bound limits is the pre-announced event page, not the go-live path. And
// it could not refuse consistently even if that were wanted:
// Schedule.DestinationIDs is empty for "every destination", which is the
// commonest shape, so a save-time refusal cannot always tell whether a Facebook
// destination is involved, and a Facebook destination created tomorrow would
// change the answer.
func (s *Server) scheduleWarnings(sc scheduler.Schedule, now time.Time) []string {
	// No test on Kind, deliberately, and this is the second place that
	// correction has paid off. A `sc.Kind != KindOnce` guard was written here
	// first and a mutation removing it turned nothing red -- because the
	// horizon check below already excludes daily and weekly by construction.
	// It was dead code that read as load-bearing, which is worse than no code.
	if sc.Action != scheduler.ActionStart {
		return nil
	}
	at, ok := scheduler.Next(sc, now)
	if !ok {
		return nil
	}
	dests, err := s.store.ListDestinations()
	if err != nil {
		return nil
	}
	// The bound comes from the destination's own provider, exactly as the
	// pre-announce sweep reads it, so this warning and the sweep's decision to
	// skip can never disagree. They used to share a facebookScheduleHorizon
	// constant declared in preannounce.go and each repeat the platform name
	// beside it, which is two copies of a fact neither file can check.
	for _, d := range dests {
		if !scheduleTargets(sc, d.ID) {
			continue
		}
		sb, ok := s.providers.ScheduledBroadcastsFor(d.Platform)
		if !ok || at.Sub(now) <= sb.ScheduleHorizon() {
			continue
		}
		return []string{
			"No Facebook event page will be created for this schedule. Facebook " +
				"accepts a start time at most seven days ahead, and this fires later " +
				"than that. The destination will still go live on time.",
		}
	}
	return nil
}

func scheduleViewOf(sc scheduler.Schedule, now time.Time) scheduleView {
	v := scheduleView{Schedule: sc, LocalTime: sc.LocalTime()}
	if at, ok := scheduler.Next(sc, now); ok {
		v.NextAt = &at
	}
	return v
}

// scheduleRequest is a whole schedule as the editor submits it. Unlike an alert
// rule there is no secret to preserve, so this is a replace rather than a patch.
type scheduleRequest struct {
	Name           string           `json:"name"`
	Enabled        bool             `json:"enabled"`
	Action         scheduler.Action `json:"action"`
	Kind           scheduler.Kind   `json:"kind"`
	DestinationIDs []int64          `json:"destinationIds"`
	TZ             string           `json:"tz"`
	AtMinutes      int              `json:"atMinutes"`
	Days           []time.Weekday   `json:"days"`
	RunAt          time.Time        `json:"runAt"`
	GraceSeconds   int              `json:"graceSeconds"`
}

func (q scheduleRequest) applyTo(sc scheduler.Schedule) scheduler.Schedule {
	sc.Name = q.Name
	sc.Enabled = q.Enabled
	sc.Action = q.Action
	sc.Kind = q.Kind
	sc.DestinationIDs = append([]int64(nil), q.DestinationIDs...)
	sc.TZ = q.TZ
	sc.AtMinutes = q.AtMinutes
	sc.Days = append([]time.Weekday(nil), q.Days...)
	sc.RunAt = q.RunAt
	sc.GraceSeconds = q.GraceSeconds
	return sc
}

func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListSchedules()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	now := time.Now()
	out := make([]scheduleView, 0, len(list))
	for _, sc := range list {
		out = append(out, scheduleViewOf(sc, now))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetSchedule(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, scheduleViewOf(*sc, time.Now()))
}

func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	var req scheduleRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	sc := req.applyTo(scheduler.Schedule{}).Normalized()
	if err := sc.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := s.store.CreateSchedule(&sc)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	now := time.Now()
	view := scheduleViewOf(*out, now)
	view.Warnings = s.scheduleWarnings(*out, now)
	writeJSON(w, http.StatusCreated, view)
}

func (s *Server) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	existing, err := s.store.GetSchedule(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req scheduleRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// Built on top of the stored row so LastRunAt survives an edit: dropping it
	// is how a daily schedule fires a second time the same evening.
	sc := req.applyTo(*existing).Normalized()
	if err := sc.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := s.store.UpdateSchedule(&sc)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	now := time.Now()
	view := scheduleViewOf(*out, now)
	view.Warnings = s.scheduleWarnings(*out, now)
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteSchedule(id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleScheduleRuns reports what the last sweep did, fired and skipped alike.
// A schedule that skipped because the server was down is the single most
// confusing thing this feature can do, so it has to be visible.
func (s *Server) handleScheduleRuns(w http.ResponseWriter, r *http.Request) {
	// The INSTALL's scheduler, not the default engine's. There is one timetable
	// (schedules has no source_id); reading it off s.eng() reported programme
	// 1's runs on a multi-source install. See #526.
	last := s.mgr.Scheduler().Last()
	if last == nil {
		last = []scheduler.Result{}
	}
	writeJSON(w, http.StatusOK, last)
}

// -------------------------------------------------------------------- clips

// handleListClips is a READ OF A DIRECTORY, and that is why it answers on an
// install with no source.
//
// The rolling buffer is on the engine, but the clips it has already written
// are files under recordings/clips — one directory per install, outliving the
// buffer being switched off and outliving the programme itself, exactly as the
// recordings beside them do. 503-ing this route would blank a listing of files
// that are still on disk, for the one operator who has just deleted their last
// source and is trying to get their material off the box.
//
// The engine is still preferred when there is one: ClipUsage asks a RUNNING
// capturer, which knows its own retention, and only falls back to counting the
// directory. With no engine there is nothing to ask, so the count comes off
// disk against the same defaults engine.New would have configured.
//
// WHICH capturer is asked is not a detail, which is why the preference goes
// through scopedEngine (#578). The file list is install-wide and complete
// either way -- one clips directory -- but the retention window and the buffer
// state come from a capturer, and on a two-programme install where only Studio
// B's buffer is on, the default engine answered with a window nothing was
// enforcing. scopedEngine keeps the zero- and one-source answer exactly as it
// was, which is what lets this route go on serving an operator who has just
// deleted their last source and is trying to get their material off the box.
func (s *Server) handleListClips(w http.ResponseWriter, r *http.Request) {
	e, ok := s.scopedEngine(w, r)
	if !ok {
		return
	}

	var (
		list  []clips.Clip
		usage clips.Usage
		err   error
	)
	if e != nil {
		list, err = e.Clips()
	} else {
		list, err = clips.List(s.clipDir())
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []clips.Clip{}
	}
	if e != nil {
		usage, err = e.ClipUsage()
	} else {
		usage, err = clips.UsageOf(clips.Config{Dir: s.clipDir()}.Normalized())
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"clips":  list,
		"usage":  usage,
		"buffer": e.ClipBuffer(),
		"bounds": map[string]int{
			"minWindowSeconds": clips.MinWindowSeconds,
			"maxWindowSeconds": clips.MaxWindowSeconds,
		},
	})
}

// handleCaptureClip writes a file off one programme's rolling buffer.
//
// SCOPED BECAUSE IT WRITES, and because what it writes is unrecoverable: the
// operator watching Studio B pressed the button, got a 201 and a .ts of MAIN's
// output, under a filename that names no programme and an audit event that
// names none either. The buffer is rolling, so by the time anybody notices, the
// moment they wanted has aged out of Studio B's buffer and cannot be captured
// again.
func (s *Server) handleCaptureClip(w http.ResponseWriter, r *http.Request) {
	eng, ok := s.scopedEngine(w, r)
	if !ok {
		return
	}
	var req struct {
		// Seconds <= 0 means the whole window, which is what the big button
		// sends; longer than the window is clamped by the capturer, not refused.
		Seconds int `json:"seconds"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	clip, err := eng.Clip(req.Seconds)
	switch {
	case errors.Is(err, clips.ErrEmpty):
		// 409, not 500: nothing is wrong, there is simply no history yet. The
		// page turns this into "start streaming first".
		writeError(w, http.StatusConflict, "the clip buffer is empty — nothing has arrived to capture yet")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// A clip is the one operation here that takes content off the server, so it
	// is recorded even though nothing has gone wrong. Info severity: on a busy
	// stream this is somebody doing their job, and a rule that wants only the
	// incidents can drop it with MinSeverity rather than unsubscribing.
	s.publishAudit(auditClipCaptured(clip.Name, s.clientIP(r), eng.SourceName()))
	writeJSON(w, http.StatusCreated, map[string]any{"clip": clip})
}

// handleSetClipBuffer starts or stops one programme's rolling buffer, which
// spends disk and CPU for as long as it is on.
//
// Unscoped, turning capture on for Studio B started MAIN's buffer, returned
// Main's ClipBuffer() as the confirmation, and left Studio B's off -- so the
// next press of the clip button answered 409 "the clip buffer is empty" for a
// feature the operator had just switched on and been told was on.
func (s *Server) handleSetClipBuffer(w http.ResponseWriter, r *http.Request) {
	eng, ok := s.scopedEngine(w, r)
	if !ok {
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
		// 0 keeps the current window, so a page that only toggles the switch
		// does not have to know what the window is.
		WindowSeconds int `json:"windowSeconds"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := eng.SetClipBuffer(req.Enabled, req.WindowSeconds); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The SAME engine that was just written to. Two reaches here would let the
	// response confirm a programme other than the one that changed.
	writeJSON(w, http.StatusOK, eng.ClipBuffer())
}

// handleDeleteClip removes a file from disk, so it answers with no source for
// the same reason the listing and the download do — and because an operator who
// can see a clip and cannot delete it has a disk that only fills.
//
// The engine is preferred while there is one: Engine.DeleteClip hands the name
// to the RUNNING capturer, which owns the index the listing is built from, so a
// delete during an eviction is serialised rather than racing. With no capturer
// the engine's own method already falls back to removing the file, and this is
// that same fallback with the base directory re-plumbed off the config.
//
// RE-PLUMBED, never nil-safed, for the reason clipDir's comment gives: this
// path is a confinement base, and an accessor answering "" would confine the
// requested name against nothing.
func (s *Server) handleDeleteClip(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var err error
	if e := s.engOrNil(); e != nil {
		err = e.DeleteClip(name)
	} else {
		var path string
		if path, err = clips.Resolve(s.clipDir(), name); err == nil {
			err = os.Remove(path)
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleDownloadClip serves a file that is already on disk, so it answers with
// no source for the same reason the listing does.
//
// The base directory is RE-PLUMBED, never nil-safed. clips.Resolve confines
// the requested name against it; an accessor that answered "" on a nil engine
// would leave every download confined against nothing, which is the trap
// MUST NOT #6 names. s.clipDir() is the same real path engine.New computes.
func (s *Server) handleDownloadClip(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var (
		path string
		err  error
	)
	if e := s.engOrNil(); e != nil {
		// The engine stays authoritative while there is one, so the confinement
		// base cannot drift from the directory the capturer is writing into.
		path, err = e.ClipPath(name)
	} else {
		path, err = clips.Resolve(s.clipDir(), name)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	serveFileDownload(w, r, path, filepath.Base(name), "video/mp2t", "clip")
}

// ----------------------------------------------------------------- loudness

// handleLoudness is the compliance read, and a compliance figure attributed to
// the wrong programme is the worst kind of wrong number: a broadcaster reads a
// PASSING verdict for a programme nothing measured.
func (s *Server) handleLoudness(w http.ResponseWriter, r *http.Request) {
	eng, ok := s.scopedEngine(w, r)
	if !ok {
		return
	}
	reports := eng.Loudness()
	if reports == nil {
		reports = []meters.Report{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		// Whether the analyser tier is running at all. Without it the page had
		// no way to seed its Monitor switch and seeded it `true`, so a remount
		// drew the switch ON over a monitor that was off -- and then explained
		// the empty list as "nothing to measure yet".
		"enabled": eng.LoudnessMonitorEnabled(),
		"reports": reports,
		"bounds": map[string]float64{
			"toleranceLu":           meters.ToleranceLU,
			"warnToleranceLu":       meters.WarnToleranceLU,
			"minIntegrationSeconds": meters.MinIntegrationSeconds,
			"truePeakFailOverDb":    meters.TruePeakFailOverDB,
		},
	})
}

// handleSetLoudnessMonitor starts or stops a real FFmpeg analyser child.
//
// Unscoped, turning monitoring on for Studio B started MAIN's analyser tier --
// a process on a programme nobody asked about -- and answered {"enabled":true}.
// Studio B was never measured, so its compliance verdict never existed at all.
func (s *Server) handleSetLoudnessMonitor(w http.ResponseWriter, r *http.Request) {
	eng, ok := s.scopedEngine(w, r)
	if !ok {
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := eng.SetLoudnessMonitor(req.Enabled); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": req.Enabled})
}

// -------------------------------------------------------------------- stems

// stemFile is one per-track file the recorder wrote beside a master segment.
type stemFile struct {
	Name string `json:"name"`
	// Master is the recording filename this stem belongs to, without its
	// extension. The recorder builds every stem name from the master's own
	// pattern, so this is an exact join key — matching on timestamps instead
	// would break on the keyframe wait that legitimately gives a stem an
	// earlier stamp than its master.
	Master    string    `json:"master"`
	Track     string    `json:"track"`
	Bytes     int64     `json:"bytes"`
	StartedAt time.Time `json:"startedAt"`
}

// handleListStems lists the stem directory.
//
// Stems are not indexed in the database — they are the master's companions and
// are swept by following it — so this is a directory read rather than a query.
// Anything it does not recognise as a stem this build wrote is skipped.
func (s *Server) handleListStems(w http.ResponseWriter, r *http.Request) {
	dir := recording.StemsDir(s.cfg.RecordingsDir())
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Stems have never been enabled. An empty list, not a 500: the page
			// asks for this on every load.
			writeJSON(w, http.StatusOK, []stemFile{})
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := make([]stemFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		startedAt, track, ok := recording.ParseStemFilename(name)
		if !ok {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		base := strings.TrimSuffix(name, filepath.Ext(name))
		out = append(out, stemFile{
			Name:      name,
			Master:    strings.TrimSuffix(base, "-"+track),
			Track:     track,
			Bytes:     info.Size(),
			StartedAt: startedAt,
		})
	}
	// Newest first, then by name so the tracks of one session keep a stable
	// order rather than shuffling between polls.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.After(out[j].StartedAt)
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDownloadStem(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	path, err := s.resolveStem(name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	serveFileDownload(w, r, path, name, "application/octet-stream", "stem")
}

// resolveStem turns a stem filename into a path inside the stem directory.
//
// recording.Manager.Resolve cannot be reused: it refuses any name containing a
// separator, which is exactly what confines it to the recordings directory and
// exactly why it cannot reach a subdirectory. The confinement here is the same
// idea applied one level down, plus a filename shape check — a name that is not
// a stem this build wrote is not a file this route will serve.
func (s *Server) resolveStem(name string) (string, error) {
	// ContainsAny over BOTH separators, spelled literally. The previous form --
	// '/' or os.PathSeparator -- reads as "both" and is both only on Windows,
	// because on Linux that constant IS '/'. See internal/media for the note.
	if name == "" || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("invalid stem name %q", name)
	}
	if _, _, ok := recording.ParseStemFilename(name); !ok {
		return "", fmt.Errorf("%q is not a stem file", name)
	}
	base, err := filepath.Abs(recording.StemsDir(s.cfg.RecordingsDir()))
	if err != nil {
		return "", err
	}
	full := filepath.Join(base, name)
	if !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("stem %q escapes the stems directory", name)
	}
	return full, nil
}

// serveFileDownload streams an already-confined path as an attachment.
// ServeContent gives range requests, so a large file resumes rather than
// restarting.
func serveFileDownload(w http.ResponseWriter, r *http.Request, path, filename, contentType, kind string) {
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, kind+" file is missing from disk")
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(filename)))
	http.ServeContent(w, r, filename, stat.ModTime(), f)
}
