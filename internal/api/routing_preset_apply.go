package api

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// ============================================================================
// APPLYING A ROUTING PRESET: POST /routing/presets/{preset}/apply
//
// POST /routing/presets/{preset} COMPILES a preset against a programme's ingest
// and answers {profile, routing}. It stores nothing and restarts nothing, which
// is what the console's routing editor wants from it: the profile lands in a
// draft, the operator reviews it, and Save is a PUT /destinations/{id}.
//
// The Stream Deck's Routing preset key sent that route and showed OK. Nothing
// had changed: the key compiled a mix and threw it away. The MCP tool knew, and
// followed the compile with a PUT of its own, carrying the mutes over in its own
// copy of the console's rule. Three clients, two implementations of "apply", and
// one of them a no-op.
//
// This route is the apply, once, for every client that has no editor of its
// own. It does what the console's preset-then-Save does, by the same rules:
//
//   - THE SAME WRITE AS A DESTINATION PUT. Held under trackGainMu, repaired by
//     dropUnsendableSettings, stored by UpdateDestination, reconciled once, and
//     each row answers `applied`/`reason` read back from the engine
//     (destinationApplied): "live" where the destination kept its process,
//     "restart" where the reconcile replaced it, absent where nothing was
//     running. A preset that changes only levels goes out live; one that
//     changes what is in the graph restarts that destination and no other.
//   - EVERY DESTINATION OF THE PROGRAMME, or the one `?destination=` names. A
//     disabled destination is stored for its next start, as a PUT stores it.
//   - THE LIVE MIX by default; `?mix=vod` replaces the second (VOD) mix, and
//     only on destinations that have one: the console offers a VOD preset only
//     while the second mix is on, and creating one here would put a second
//     audio track on an ingest that may refuse it (Twitch without Multitrack).
//   - THE OPERATOR'S DECISIONS SURVIVE: a muted track stays muted and an
//     excluded role (the music guard) stays excluded, on either mix. See
//     routing.KeepOperatorDecisions.
//   - A DESTINATION THAT FORWARDS ITS AUDIO (audio copy) IS SKIPPED. It has no
//     mix for a preset to replace, so an OK there would be the original bug.
//   - OPTIONS: a body is a full-replacement routing.PresetOpts, exactly as the
//     compile route reads it. With no body the pickers follow the programme's
//     track roles (routing.PresetOptsFromRoles), as the console's do.
//
// ATOMIC WHERE IT CAN BE. Every destination's new mix is built and checked --
// the row's own Validate, which UpdateDestination will run, and a compile
// against the programme's tracks, which the compile route runs -- before any
// row is written. One refusal refuses the request with 400 and every reason,
// and nothing is stored. What can still fail after that is the store itself or
// a row deleted in between (DELETE does not take trackGainMu); such a row is
// answered "failed" with the store's reason, the others stay saved, and a
// warning says how many of how many.
//
// `?dryRun=true` stops after the checks and answers each new mix without
// storing it: the MCP tool's preview, so it shows what this route will write
// rather than its own guess.
// ============================================================================

// presetMixParam and presetDryRunParam are the apply route's own query
// parameters. `source` and `destination` are spelt as every other
// programme-scoped route spells them (sourceParam, destinationParam).
const (
	presetMixParam    = "mix"
	presetDryRunParam = "dryRun"
)

// The two mixes a preset can replace, as ?mix= names them.
const (
	presetMixLive = "live"
	presetMixVOD  = "vod"
)

// presetOutcome is what the route did to one destination.
type presetOutcome string

const (
	// presetSaved: the new mix is stored and the pipeline was reconciled.
	// `applied` says how it reached the running output, when anything ran.
	presetSaved presetOutcome = "saved"
	// presetReady: a dry run's row. Checked and would be saved; nothing stored.
	presetReady presetOutcome = "ready"
	// presetSkipped: not this preset's to change (no second mix, audio copy).
	// Untouched.
	presetSkipped presetOutcome = "skipped"
	// presetRefused: the check refused this row's new mix, so the whole request
	// was refused and nothing was stored anywhere.
	presetRefused presetOutcome = "refused"
	// presetFailed: checked, then the write failed. Left as it was.
	presetFailed presetOutcome = "failed"
)

// presetApplyRow is one destination's answer.
type presetApplyRow struct {
	ID       int64         `json:"id"`
	Name     string        `json:"name"`
	Platform db.Platform   `json:"platform"`
	Outcome  presetOutcome `json:"outcome"`
	// Applied and Reason are PUT /destinations/{id}'s, read back after the
	// reconcile. Absent when nothing was running to apply the mix to.
	Applied string `json:"applied,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// Message is why, on every outcome that is not saved or ready.
	Message string `json:"message,omitempty"`
	// Kept names the mutes and exclusions carried from the stored mix.
	Kept *routing.Kept `json:"kept,omitempty"`
	// Profile is the mix as stored (saved) or as it would be (ready).
	Profile *routing.Profile `json:"profile,omitempty"`
}

// presetApplyBetweenPhases runs after every row is checked and before the
// first is written. A seam for the one failure the check cannot rule out -- a
// row deleted in between -- and nothing else. Never written outside a test.
var presetApplyBetweenPhases = func() {}

// presetApplyPlan is one destination the check passed: the row with its new
// mix already in place, ready for UpdateDestination.
type presetApplyPlan struct {
	row *db.Destination
	out presetApplyRow
}

func (s *Server) handleApplyPresetToProgramme(w http.ResponseWriter, r *http.Request) {
	eng, ok := s.scopedEngine(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	mix := strings.TrimSpace(q.Get(presetMixParam))
	switch mix {
	case "":
		mix = presetMixLive
	case presetMixLive, presetMixVOD:
	default:
		writeError(w, http.StatusBadRequest, `"`+presetMixParam+`" must be "live" or "vod"`)
		return
	}
	dryRun := false
	if raw := strings.TrimSpace(q.Get(presetDryRunParam)); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, `"`+presetDryRunParam+`" must be true or false`)
			return
		}
		dryRun = v
	}
	var only *int64
	if raw := strings.TrimSpace(q.Get(destinationParam)); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, `"`+destinationParam+`" must be a destination id`)
			return
		}
		only = &id
	}
	named := strings.TrimSpace(q.Get(sourceParam)) != ""

	src := eng.Source()
	opts := routing.PresetOptsFromRoles(routing.DefaultPresetOpts(), src)
	body, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	if len(bytes.TrimSpace(body)) > 0 {
		// FULL REPLACEMENT, as the compile route reads it: see handleApplyPreset.
		opts = routing.PresetOpts{}
		if err := decodeJSONInto(body, &opts); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	preset := presetParam(r)
	// The preset itself first. An unknown id, or one that cannot build against
	// this ingest at all, is the request's fault rather than any destination's.
	built, err := routing.ApplyPreset(preset, src, opts)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// The PUT's lock, from the read to the reconcile, so a mute stored by the
	// gain route in between is neither lost nor read as this request's outcome.
	s.trackGainMu.Lock()
	defer s.trackGainMu.Unlock()

	rows, ok := s.presetTargets(w, eng.SourceID(), named, only)
	if !ok {
		return
	}

	var (
		plans   []presetApplyPlan
		skipped []presetApplyRow
		refused []presetApplyRow
	)
	for _, row := range rows {
		out := presetApplyRow{ID: row.ID, Name: row.Name, Platform: row.Platform}
		if why := presetSkipReason(row, mix); why != "" {
			out.Outcome, out.Message = presetSkipped, why
			skipped = append(skipped, out)
			continue
		}
		next, why := presetMixFor(row, mix, built, src)
		if why != "" {
			out.Outcome, out.Message = presetRefused, why
			refused = append(refused, out)
			continue
		}
		out.Profile = &next.mix
		if len(next.kept.Muted) > 0 || len(next.kept.Excluded) > 0 {
			kept := next.kept
			out.Kept = &kept
		}
		plans = append(plans, presetApplyPlan{row: row, out: out})
	}

	if only != nil && len(skipped) == 1 {
		// Named, and not this preset's to change: a refusal, not a 200 that
		// changed nothing.
		writeError(w, http.StatusConflict, skipped[0].Message)
		return
	}
	if len(refused) > 0 {
		names := make([]string, 0, len(refused))
		for _, row := range refused {
			names = append(names, fmt.Sprintf("%q", row.Name))
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("the %s preset was not applied: %s cannot take it, so nothing was changed on any destination",
				preset, strings.Join(names, ", ")),
			"results": refused,
		})
		return
	}

	resp := map[string]any{"preset": preset, "mix": mix}
	if dryRun {
		results := make([]presetApplyRow, 0, len(rows))
		for _, p := range plans {
			p.out.Outcome = presetReady
			results = append(results, p.out)
		}
		resp["dryRun"] = true
		resp["results"] = append(results, skipped...)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	presetApplyBetweenPhases()
	results, warnings, rw := s.writePresetPlans(plans)
	resp["results"] = append(results, skipped...)
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	writeMutation(w, http.StatusOK, rw, resp)
}

// presetParam is the {preset} segment, unescaped.
//
// chi routes on RawPath whenever it differs from Path, so URLParam hands back
// the segment as it was sent (handleProcessLogs has the same note). Every
// platform preset's id carries a colon -- "platform:youtube" -- and a client
// that escapes it, as encodeURIComponent does in the Stream Deck plugin and the
// console, arrived as "platform%3Ayoutube" and was refused as an unknown preset.
// A segment that will not unescape is used as it arrived, and is refused by the
// catalogue as unknown.
func presetParam(r *http.Request) string {
	id := chi.URLParam(r, "preset")
	if decoded, err := url.PathUnescape(id); err == nil {
		return decoded
	}
	return id
}

// presetTargets reads the destinations a preset applies to, under the caller's
// lock: the one ?destination= names, which must be on this programme, or every
// destination of the programme.
//
// Unnamed means the whole store, for bulkSetDestinationsEnabled's reason: a
// request reaches here without ?source= only on an install with one programme
// (scopedEngine refuses it otherwise).
func (s *Server) presetTargets(w http.ResponseWriter, programme int64, named bool, only *int64) ([]*db.Destination, bool) {
	if only != nil {
		row, err := s.store.GetDestination(*only)
		if err != nil {
			writeStoreError(w, err)
			return nil, false
		}
		if named && (row.SourceID == nil || *row.SourceID != programme) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf(
				"destination %d is not on source %d; name its own programme with ?%s=", *only, programme, sourceParam))
			return nil, false
		}
		return []*db.Destination{row}, true
	}
	var (
		rows []*db.Destination
		err  error
	)
	if named {
		rows, err = s.store.ListDestinationsBySource(programme)
	} else {
		rows, err = s.store.ListDestinations()
	}
	if err != nil {
		writeStoreError(w, err)
		return nil, false
	}
	return rows, true
}

// presetSkipReason is why a preset does not apply to this destination at all,
// or "".
func presetSkipReason(row *db.Destination, mix string) string {
	// The gain route's test, and its reason: audio copy forwards the ingest's
	// audio untouched, so there is no mix here for a preset to replace.
	if row.Audio.Copy && row.Kind != db.DestAudio {
		return "this destination passes its audio through unchanged (audio copy), so it has no mix for a preset to replace"
	}
	if mix == presetMixVOD && row.VODProfile == nil {
		return "this destination has no second (VOD) mix; turn one on in the routing editor first"
	}
	return ""
}

// presetMix is one destination's new mix and what it kept.
type presetMix struct {
	mix  routing.Profile
	kept routing.Kept
}

// presetMixFor puts the preset, with the stored mix's mutes and guards, into
// row's live or VOD mix, and checks the row exactly as UpdateDestination will
// and the compile route does. It answers why the row cannot take it, or "".
// The row is edited in place: the caller writes it, or throws it away.
func presetMixFor(row *db.Destination, mix string, built routing.Profile, src routing.Source) (presetMix, string) {
	stored := &row.Profile
	if mix == presetMixVOD {
		stored = row.VODProfile
	}
	next, kept := routing.KeepOperatorDecisions(built, stored)
	if mix == presetMixVOD {
		v := next
		row.VODProfile = &v
	} else {
		row.Profile = next
	}
	// UpdateDestination's own first steps, so its refusal is had here, before
	// any row is written.
	row.Profile.ApplyDefaults()
	if err := row.Validate(); err != nil {
		return presetMix{}, err.Error()
	}
	if mix == presetMixLive {
		next = row.Profile
	}
	// Against the programme's tracks, as POST /routing/presets/{preset} checks
	// the preset alone. A kept exclusion can take out every track the preset
	// selected, and a destination that would stream silence is refused, never
	// saved (refuseIfSilent's rule).
	if _, err := routing.Compile(next, src); err != nil {
		return presetMix{}, err.Error()
	}
	return presetMix{mix: next, kept: kept}, ""
}

// writePresetPlans stores every checked row, reconciles once, and reads back
// how each took effect. Called with trackGainMu held.
//
// One reconcile for the lot rather than one per row: every row changes at the
// same moment, and a row that has to restart restarts in that one pass rather
// than one after another while the others wait on stale mixes.
func (s *Server) writePresetPlans(plans []presetApplyPlan) (results []presetApplyRow, warnings []string, reconcileWarn string) {
	type written struct {
		i      int
		source *int64
		before engine.DestinationRun
	}
	results = make([]presetApplyRow, len(plans))
	var done []written
	var failed []string
	for i, p := range plans {
		row := p.row
		results[i] = p.out
		before := s.destinationRunBefore(row.ID, row.SourceID, row.SourceID)
		// The same repair every destination write makes first
		// (TestEveryDestinationWriteIsRepairedFirst).
		repairs, err := s.dropUnsendableSettings(row)
		if err != nil {
			results[i].Outcome, results[i].Message, results[i].Profile, results[i].Kept = presetFailed, err.Error(), nil, nil
			failed = append(failed, fmt.Sprintf("%q (%s)", row.Name, err.Error()))
			continue
		}
		updated, err := s.store.UpdateDestination(row)
		if err != nil {
			results[i].Outcome, results[i].Message, results[i].Profile, results[i].Kept = presetFailed, err.Error(), nil, nil
			failed = append(failed, fmt.Sprintf("%q (%s)", row.Name, err.Error()))
			continue
		}
		for _, line := range repairs {
			warnings = append(warnings, row.Name+": "+line)
		}
		results[i].Outcome = presetSaved
		done = append(done, written{i: i, source: updated.SourceID, before: before})
	}
	if len(done) > 0 {
		reconcileWarn = s.reconcileNow("the routing preset")
		for _, d := range done {
			applied, reason := s.destinationApplied(results[d.i].ID, d.source, d.before, reconcileWarn)
			results[d.i].Applied, results[d.i].Reason = applied, reason
		}
	}
	if len(failed) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"the preset was saved on %d of %d destinations; not saved, and left as they were: %s",
			len(done), len(plans), strings.Join(failed, ", ")))
	}
	return results, warnings, reconcileWarn
}
