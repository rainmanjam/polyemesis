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
	"github.com/rainmanjam/polyemesis/internal/db"
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
	n := s.eng().Alerts()
	if n == nil {
		writeError(w, http.StatusServiceUnavailable, "the alert notifier is not running")
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
		"stats": s.eng().Alerts().Stats(),
	})
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
	if !ok || at.Sub(now) <= facebookScheduleHorizon {
		return nil
	}
	dests, err := s.store.ListDestinations()
	if err != nil {
		return nil
	}
	for _, d := range dests {
		if d.Platform == db.PlatformFacebook && scheduleTargets(sc, d.ID) {
			return []string{
				"No Facebook event page will be created for this schedule. Facebook " +
					"accepts a start time at most seven days ahead, and this fires later " +
					"than that. The destination will still go live on time.",
			}
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
	last := s.eng().Scheduler().Last()
	if last == nil {
		last = []scheduler.Result{}
	}
	writeJSON(w, http.StatusOK, last)
}

// -------------------------------------------------------------------- clips

func (s *Server) handleListClips(w http.ResponseWriter, r *http.Request) {
	list, err := s.eng().Clips()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []clips.Clip{}
	}
	usage, err := s.eng().ClipUsage()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"clips":  list,
		"usage":  usage,
		"buffer": s.eng().ClipBuffer(),
		"bounds": map[string]int{
			"minWindowSeconds": clips.MinWindowSeconds,
			"maxWindowSeconds": clips.MaxWindowSeconds,
		},
	})
}

func (s *Server) handleCaptureClip(w http.ResponseWriter, r *http.Request) {
	var req struct {
		// Seconds <= 0 means the whole window, which is what the big button
		// sends; longer than the window is clamped by the capturer, not refused.
		Seconds int `json:"seconds"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	clip, err := s.eng().Clip(req.Seconds)
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
	writeJSON(w, http.StatusCreated, map[string]any{"clip": clip})
}

func (s *Server) handleSetClipBuffer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
		// 0 keeps the current window, so a page that only toggles the switch
		// does not have to know what the window is.
		WindowSeconds int `json:"windowSeconds"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.eng().SetClipBuffer(req.Enabled, req.WindowSeconds); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.eng().ClipBuffer())
}

func (s *Server) handleDeleteClip(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := s.eng().DeleteClip(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleDownloadClip(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	path, err := s.eng().ClipPath(name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	serveFileDownload(w, r, path, filepath.Base(name), "video/mp2t", "clip")
}

// ----------------------------------------------------------------- loudness

func (s *Server) handleLoudness(w http.ResponseWriter, r *http.Request) {
	reports := s.eng().Loudness()
	if reports == nil {
		reports = []meters.Report{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"reports": reports,
		"bounds": map[string]float64{
			"toleranceLu":           meters.ToleranceLU,
			"warnToleranceLu":       meters.WarnToleranceLU,
			"minIntegrationSeconds": meters.MinIntegrationSeconds,
			"truePeakFailOverDb":    meters.TruePeakFailOverDB,
		},
	})
}

func (s *Server) handleSetLoudnessMonitor(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.eng().SetLoudnessMonitor(req.Enabled); err != nil {
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
	dir := s.eng().Recordings().StemsDir()
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
	base, err := filepath.Abs(s.eng().Recordings().StemsDir())
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
