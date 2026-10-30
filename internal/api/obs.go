package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/obsbridge"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// ============================================================================
// THE OBS BRIDGE'S ROUTES AND ACTIONS (#16).
//
//	GET  /api/v1/obs           configuration and live status
//	PUT  /api/v1/obs           replace the configuration
//	PUT  /api/v1/obs/password  seal the obs-websocket password
//	POST /api/v1/obs/events    the OBS script reports a fact
//
// The bridge itself -- the obs-websocket client, the edge logic, the grace
// period -- is internal/obsbridge. This file is the part that touches
// destinations, and it touches them ONLY through the code the operator's own
// buttons run: bulkOne (= POST /destinations/{id}/start and /stop) and
// persistTrackLevels (= POST /destinations/{id}/tracks/{n}/gain). OBS can never
// do more to a destination than the operator could have done by hand.
// ============================================================================

// obsConfigPayload is GET /api/v1/obs's body.
type obsConfigPayload struct {
	Config obsbridge.Config `json:"config"`
	Status obsbridge.Status `json:"status"`
}

// OBSLoader returns the bridge's Load function: the stored configuration with
// HasPassword filled in, and the plaintext password for the client to answer
// obs-websocket's challenge with. Exported for the composition root.
func OBSLoader(store *db.DB, box *secrets.Box) func() (obsbridge.Config, string, error) {
	return func() (obsbridge.Config, string, error) {
		cfg, err := loadOBSConfig(store)
		if err != nil {
			return obsbridge.Config{}, "", err
		}
		pw, err := store.GetOBSPassword(box)
		if err != nil {
			return obsbridge.Config{}, "", err
		}
		cfg.HasPassword = pw != ""
		return cfg, pw, nil
	}
}

// loadOBSConfig reads the stored document. No row is the zero Config: a
// bridge that is off.
func loadOBSConfig(store *db.DB) (obsbridge.Config, error) {
	var cfg obsbridge.Config
	raw, err := store.GetOBSConfig()
	if err != nil || raw == nil {
		return cfg, err
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("the stored OBS bridge configuration is unreadable: %w", err)
	}
	return cfg, nil
}

func (s *Server) obsPayload(r *http.Request) (obsConfigPayload, error) {
	cfg, err := loadOBSConfig(s.store)
	if err != nil {
		return obsConfigPayload{}, err
	}
	has, err := s.store.HasOBSPassword()
	if err != nil {
		return obsConfigPayload{}, err
	}
	cfg.HasPassword = has
	// The obs-websocket address is masked for a read token, as mqtt.brokerUrl
	// is: it names a machine on the operator's network, and a dashboard has
	// no use for it. Blanked rather than redacted, because it carries no
	// credential to mask around -- credentials in it are refused at the door.
	if isReadScopedToken(r) {
		cfg.URL = ""
	}
	if cfg.Destinations == nil {
		cfg.Destinations = []int64{}
	}
	if cfg.Scenes == nil {
		cfg.Scenes = []obsbridge.SceneRule{}
	}
	p := obsConfigPayload{Config: cfg, Status: obsbridge.Status{
		Connection:  obsbridge.Connection{State: obsbridge.ConnOff},
		LastActions: []obsbridge.ActionRecord{},
	}}
	if s.obs != nil {
		p.Status = s.obs.Status()
	}
	return p, nil
}

func (s *Server) handleGetOBS(w http.ResponseWriter, r *http.Request) {
	p, err := s.obsPayload(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// handlePutOBS replaces the configuration. FULL REPLACEMENT: a field left out
// is its zero value, which for every switch here is "off" -- the safe reading
// of a body that did not mention it.
func (s *Server) handlePutOBS(w http.ResponseWriter, r *http.Request) {
	var cfg obsbridge.Config
	if !decodeJSON(w, r, &cfg) {
		return
	}
	// Output only. Stored as false and filled in on the way out, so a PUT that
	// echoes a GET cannot claim a password exists.
	cfg.HasPassword = false
	// No programme named means the install's default, RESOLVED HERE and stored
	// as an id. Left nil it would match only a destination with no programme,
	// which every create now refuses to make, and a later change of default
	// would silently move the bridge to another programme's destinations.
	if cfg.SourceID == nil && len(cfg.Destinations) > 0 {
		if id, err := s.defaultSourceID(); err == nil && id > 0 {
			cfg.SourceID = &id
		}
	}
	if probs := cfg.Problems(); len(probs) > 0 {
		writeError(w, http.StatusBadRequest, strings.Join(probs, "; "))
		return
	}
	if probs, err := s.obsDestinationProblems(cfg); err != nil {
		writeStoreError(w, err)
		return
	} else if len(probs) > 0 {
		writeError(w, http.StatusBadRequest, strings.Join(probs, "; "))
		return
	}
	doc, err := json.Marshal(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.PutOBSConfig(doc); err != nil {
		writeStoreError(w, err)
		return
	}
	if s.obs != nil {
		if err := s.obs.Reload(); err != nil {
			s.log.Error("OBS bridge: saved a configuration it then could not read back", "err", err)
		}
	}
	s.publishAudit(auditSettingsChanged([]string{"obs"}, s.clientIP(r)))
	p, err := s.obsPayload(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// obsDestinationProblems checks what Config.Problems cannot: that the
// programme exists and that every destination is on it.
//
// THE PROGRAMME RULE IS STOP-ALL'S. A control that names a programme must not
// reach another programme's destinations (see bulkSetDestinationsEnabled on
// Stop All ending Studio A's broadcasts from Studio B). The check is repeated
// at action time, because a destination can be moved after the save.
func (s *Server) obsDestinationProblems(cfg obsbridge.Config) ([]string, error) {
	var probs []string
	if cfg.SourceID != nil {
		if _, err := s.store.GetSource(*cfg.SourceID); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return []string{fmt.Sprintf("programme %d does not exist", *cfg.SourceID)}, nil
			}
			return nil, err
		}
	}
	for _, id := range cfg.Destinations {
		row, err := s.store.GetDestination(id)
		if errors.Is(err, db.ErrNotFound) {
			probs = append(probs, fmt.Sprintf("destination %d does not exist", id))
			continue
		}
		if err != nil {
			return nil, err
		}
		if !sameProgramme(row.SourceID, cfg.SourceID) {
			probs = append(probs, fmt.Sprintf("destination %d is on another programme; the OBS bridge drives one programme", id))
		}
	}
	return probs, nil
}

// sameProgramme compares two nullable programme ids.
func sameProgramme(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// handlePutOBSPassword seals the obs-websocket password. Its own route for the
// reason PUT /settings/mqtt-password is: the password never travels outward in
// the configuration, so it cannot travel inward in it either.
func (s *Server) handlePutOBSPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Password) > 1024 {
		writeError(w, http.StatusBadRequest, "the password is longer than 1024 characters")
		return
	}
	if err := s.store.PutOBSPassword(s.box, req.Password); err != nil {
		writeStoreError(w, err)
		return
	}
	if s.obs != nil {
		if err := s.obs.Reload(); err != nil {
			s.log.Error("OBS bridge: cannot reload after a password change", "err", err)
		}
	}
	// Only the section name travels; see handlePutMQTTPassword.
	s.publishAudit(auditSettingsChanged([]string{"obs"}, s.clientIP(r)))
	writeJSON(w, http.StatusOK, map[string]bool{"hasPassword": req.Password != ""})
}

// obsEventRequest is POST /api/v1/obs/events's body: what the OBS script sends.
type obsEventRequest struct {
	Event string `json:"event"`
	Scene string `json:"scene,omitempty"`
}

// handleOBSEvent takes one fact from the OBS script.
//
// 202, not 200: the fact is accepted and the bridge acts on it on its own
// worker, so a paced start of eight destinations does not hold the script's
// request -- and OBS's frontend thread, which the script runs on -- for tens
// of seconds. What happened is on GET /api/v1/obs and the event stream.
func (s *Server) handleOBSEvent(w http.ResponseWriter, r *http.Request) {
	var req obsEventRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.obs == nil {
		writeError(w, http.StatusServiceUnavailable, "the OBS bridge is not running in this process")
		return
	}
	err := s.obs.Handle(obsbridge.Event{Kind: obsbridge.Kind(req.Event), Scene: req.Scene, Via: obsbridge.ViaScript})
	switch {
	case errors.Is(err, obsbridge.ErrDisabled):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

// ------------------------------------------------------------------ actions

// obsActions is the bridge's obsbridge.Actions, on this server.
type obsActions struct{ s *Server }

// OBSActions returns the server's implementation of what the bridge does.
func (s *Server) OBSActions() obsbridge.Actions { return obsActions{s} }

// obsRow fetches one of the bridge's destinations and re-checks that it is
// still on the bridge's programme: it may have been moved since the save.
func (a obsActions) obsRow(id int64, cfg obsbridge.Config) (*db.Destination, *obsbridge.Outcome) {
	row, err := a.s.store.GetDestination(id)
	if err != nil {
		return nil, &obsbridge.Outcome{ID: id, Outcome: string(bulkFailed), Message: err.Error()}
	}
	if !sameProgramme(row.SourceID, cfg.SourceID) {
		return nil, &obsbridge.Outcome{ID: id, Name: row.Name, Outcome: string(bulkFailed),
			Message: "this destination has moved to another programme, so the OBS bridge left it alone"}
	}
	return row, nil
}

func fromBulk(r bulkDestResult) obsbridge.Outcome {
	return obsbridge.Outcome{ID: r.ID, Name: r.Name, Outcome: string(r.Outcome), Message: r.Message}
}

// Start is N presses of the per-destination start button, paced exactly as
// start-all paces them, and abandoned where it stands if OBS stops meanwhile.
func (a obsActions) Start(ctx context.Context, cfg obsbridge.Config) []obsbridge.Outcome {
	out := make([]obsbridge.Outcome, 0, len(cfg.Destinations))
	for i, id := range cfg.Destinations {
		if i > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(bulkStartPacing):
			}
		}
		if ctx.Err() != nil {
			for _, rest := range cfg.Destinations[i:] {
				out = append(out, obsbridge.Outcome{ID: rest, Outcome: string(bulkSkipped),
					Message: "OBS stopped streaming before this destination was reached, so it was left exactly as it was"})
			}
			return out
		}
		row, bad := a.obsRow(id, cfg)
		if bad != nil {
			out = append(out, *bad)
			continue
		}
		out = append(out, fromBulk(a.s.bulkOne(row, bulkStart.enabled())))
	}
	return out
}

// Stop is N presses of the per-destination stop button, on the bridge's
// selection only and never "every destination": stop-all's rules, with the
// confirmation given once, in writing, when the operator armed it
// (Config.StopAcknowledged) rather than per request.
func (a obsActions) Stop(_ context.Context, cfg obsbridge.Config) []obsbridge.Outcome {
	out := make([]obsbridge.Outcome, 0, len(cfg.Destinations))
	for _, id := range cfg.Destinations {
		row, bad := a.obsRow(id, cfg)
		if bad != nil {
			out = append(out, *bad)
			continue
		}
		out = append(out, fromBulk(a.s.bulkOne(row, bulkStop.enabled())))
	}
	return out
}

// outcomeApplied is a scene rule's levels reaching a destination.
const outcomeApplied = "applied"

// ApplyScene applies a scene's routing preset to each target, as live mutes.
func (a obsActions) ApplyScene(ctx context.Context, cfg obsbridge.Config, rule obsbridge.SceneRule) []obsbridge.Outcome {
	targets := cfg.Targets(rule)
	out := make([]obsbridge.Outcome, 0, len(targets))
	for _, id := range targets {
		if ctx.Err() != nil {
			out = append(out, obsbridge.Outcome{ID: id, Outcome: string(bulkSkipped), Message: "the bridge was shutting down"})
			continue
		}
		row, bad := a.obsRow(id, cfg)
		if bad != nil {
			out = append(out, *bad)
			continue
		}
		out = append(out, a.s.applySceneLevels(row.ID, rule))
	}
	return out
}

// liveLevelsSupported mirrors engine.liveGainsSupported: on Windows every
// level change restarts the destination, so "applied live where I4 allows"
// means not applied at all there.
var liveLevelsSupported = func() bool { return runtime.GOOS != "windows" }

// applySceneLevels applies one scene rule to one destination.
//
// WHAT A SCENE CAN CHANGE IS LEVELS, AND ONLY LEVELS. A routing preset, applied
// as POST /routing/presets/{preset} answers it, is a whole new profile: other
// tracks selected, a loudness target, a delay. Every one of those moves the
// destination's restart signature, and a scene switch that restarted the
// stream would put a gap in a live broadcast every time the streamer cut to
// "Just Chatting". So the preset is used only for WHICH TRACKS it keeps, and
// that is expressed as I4's per-track mute on the tracks already in this
// destination's mix: a track the preset keeps is unmuted, one it drops is
// muted, and nothing else about the profile is touched. Anything the preset
// would need beyond that -- a track this destination does not carry -- is left
// out and said so, never forced through by a restart.
//
// And then it is CHECKED rather than trusted: the edited profile is compiled
// beside the stored one, and if the two graphs differ in anything but their
// volume levels the change is refused. That is the engine's own test for
// "live" (destArgvSig over ffmpeg.MaskLiveVolumes), asked before the write
// instead of discovered after it.
func (s *Server) applySceneLevels(id int64, rule obsbridge.SceneRule) obsbridge.Outcome {
	s.trackGainMu.Lock()
	defer s.trackGainMu.Unlock()

	row, err := s.store.GetDestination(id)
	if err != nil {
		return obsbridge.Outcome{ID: id, Outcome: string(bulkFailed), Message: err.Error()}
	}
	res := obsbridge.Outcome{ID: row.ID, Name: row.Name}
	skip := func(why string) obsbridge.Outcome {
		res.Outcome, res.Message = string(bulkSkipped), why
		return res
	}
	if !liveLevelsSupported() {
		return skip("live track levels are not available on Windows, and a scene switch never restarts a destination")
	}
	if row.Audio.Copy && row.Kind != db.DestAudio {
		return skip("this destination passes its audio through unchanged, so there is no track level for a scene to change")
	}

	src := routing.DefaultSource()
	if eng := s.engineForSource(row.SourceID); eng != nil {
		src = eng.Source()
	}
	opts := routing.DefaultPresetOpts()
	if rule.Opts != nil {
		opts = *rule.Opts
	}
	preset, err := routing.ApplyPreset(rule.Preset, src, opts)
	if err != nil {
		res.Outcome, res.Message = string(bulkFailed), err.Error()
		return res
	}
	next, changed, notes, silent := sceneLevels(row.Profile, preset)
	if silent {
		return skip("the preset keeps none of the tracks in this destination's mix, so applying it as mutes would " +
			"silence the destination; changing which tracks it carries needs a restart, which a scene switch never does")
	}
	if !changed {
		res.Outcome = outcomeApplied
		res.Message = strings.Join(append([]string{"already matched the scene's preset"}, notes...), "; ")
		return res
	}
	if !levelsOnly(row.Profile, next, src) {
		return skip("matching this preset would change more than track levels, which needs a restart; a scene switch never restarts a destination")
	}

	row.Profile = next
	lw, storeErr, updateErr := s.persistTrackLevels(row, true)
	switch {
	case storeErr != nil:
		res.Outcome, res.Message = string(bulkFailed), storeErr.Error()
		return res
	case updateErr != nil:
		res.Outcome, res.Message = string(bulkFailed), updateErr.Error()
		return res
	}
	msgs := []string{}
	switch {
	case lw.reconcileWarn != "":
		res.Outcome = string(bulkWarned)
		msgs = append(msgs, lw.reconcileWarn)
	case lw.applied == "live":
		res.Outcome = outcomeApplied
		msgs = append(msgs, "applied live")
		if lw.reason != "" {
			msgs[0] += ": " + lw.reason
		}
	default:
		// "restart" with a reason. The common one is a destination that is
		// not running, where the levels are stored for its next start and
		// nothing was restarted; anything else is the engine falling back,
		// which the operator should hear about.
		res.Outcome = outcomeApplied
		if a := s.engineForSource(row.SourceID); a != nil && a.DestinationRunning(row.ID) {
			res.Outcome = string(bulkWarned)
		}
		msgs = append(msgs, "stored: "+lw.reason)
	}
	res.Message = strings.Join(append(msgs, notes...), "; ")
	return res
}

// sceneLevels expresses a preset as mutes on the tracks already in cur's mix.
//
// It returns the edited profile, whether any mute moved, notes about what the
// preset wanted that cannot be expressed as a mute, and silent when the result
// would mute every track in the mix. Gains are never touched: a scene says
// which tracks are heard, and the operator's balance between them stays theirs.
func sceneLevels(cur, preset routing.Profile) (next routing.Profile, changed bool, notes []string, silent bool) {
	next = cur
	next.Tracks = append([]routing.TrackSel(nil), cur.Tracks...)
	inMix := map[int]bool{}
	for _, t := range cur.SelectedTracks() {
		inMix[t] = true
	}
	want := map[int]bool{}
	for _, t := range preset.SelectedTracks() {
		want[t] = true
	}
	audible := 0
	for i, t := range next.Tracks {
		if !inMix[t.Track] {
			continue
		}
		muted := !want[t.Track]
		if t.Muted != muted {
			next.Tracks[i].Muted = muted
			changed = true
		}
		if !muted {
			audible++
		}
	}
	var missing []int
	for t := range want {
		if !inMix[t] {
			missing = append(missing, t)
		}
	}
	slices.Sort(missing)
	for _, t := range missing {
		notes = append(notes, fmt.Sprintf("the preset wants track %d, which is not in this destination's mix; "+
			"adding it needs a restart, so it was left out", t+1))
	}
	return next, changed, notes, audible == 0
}

// levelsOnly reports whether next compiles to cur's graph with nothing but
// volume levels changed -- the engine's own definition of a live change.
func levelsOnly(cur, next routing.Profile, src routing.Source) bool {
	a, errA := routing.Compile(cur, src)
	b, errB := routing.Compile(next, src)
	if errA != nil || errB != nil {
		return false
	}
	return a.OutLabel == b.OutLabel && a.SecondOutLabel == b.SecondOutLabel &&
		ffmpeg.MaskLiveVolumes(a.FilterComplex) == ffmpeg.MaskLiveVolumes(b.FilterComplex)
}
