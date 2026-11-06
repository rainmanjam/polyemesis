package api

import (
	"bytes"
	"errors"
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
//   - LIVE WHERE IT CAN BE, A RESTART ONLY WHERE IT MUST, as a destination PUT:
//     held under trackGainMu, reconciled once, and each row answers
//     `applied`/`reason` read back from the engine (destinationApplied) --
//     "live" where the destination kept its process, "restart" where the
//     reconcile replaced it, absent where nothing was running. A preset that
//     changes only levels goes out live; one that changes what is in the graph
//     restarts that destination and no other.
//   - ONLY THE MIX IS WRITTEN. Not the whole row, as a PUT writes it: this
//     route reads every destination of a programme and writes them afterwards,
//     and a stop, a scheduled start or a key refresh that landed in between
//     (none take trackGainMu) would be reverted -- a destination just stopped,
//     written back enabled and started by the reconcile. UpdateAnnouncement
//     was written for the same hazard. db.UpdateDestinationMixes reads each row
//     again inside one transaction and writes its two mix columns, all of the
//     rows or none.
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
// ATOMIC. Every destination's new mix is built and checked -- the row's own
// Validate, which UpdateDestination runs, and a compile against the
// programme's tracks, which the compile route runs -- on the row as the
// transaction reads it, before any row is written. One refusal refuses the
// request with 400 and every reason, and nothing is stored. A destination
// deleted between the listing and the transaction is answered skipped. What can
// still go wrong after the commit is the reconcile: the mixes are stored, no row
// says how it applied, and `warnings` with `reconcileFailed` say why, as on
// every mutation.
//
// `?dryRun=true` runs the same checks on the rows as listed and answers each new
// mix without storing it: the MCP tool's preview, so it shows what this route
// will write rather than its own guess.
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
	// presetSkipped: not this preset's to change (no second mix, audio copy, or
	// deleted while the preset was applied). Untouched.
	presetSkipped presetOutcome = "skipped"
	// presetRefused: the check refused this row's new mix, so the whole request
	// was refused and nothing was stored anywhere.
	presetRefused presetOutcome = "refused"
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

// presetApplyBetweenPhases runs after the destinations are listed and before
// the transaction that writes them. A seam for what can land in between -- a
// stop, a rename, a delete -- and nothing else. Never written outside a test.
var presetApplyBetweenPhases = func() {}

// presetReconcile is the one reconcile an apply makes. A var so a test can stand
// in a reconcile that fails, which nothing in a test fixture does for real.
// Never written outside a test.
var presetReconcile = func(s *Server) string { return s.reconcileNow("the routing preset") }

// errPresetRefused is the presetRow verdict that refuses the whole request.
var errPresetRefused = errors.New("the destination cannot take the preset")

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
	opts := routing.DefaultPresetOptsFor(src)
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
	p := presetPlan{preset: preset, mix: mix, built: built, src: src}

	// The PUT's lock, from the listing to the reconcile: a PUT or a gain write
	// that read a row before the transaction and wrote it after would put the old
	// mix back, and a level note one of them leaves must not be read as this
	// request's outcome.
	s.trackGainMu.Lock()
	defer s.trackGainMu.Unlock()

	rows, ok := s.presetTargets(w, eng.SourceID(), named, only)
	if !ok {
		return
	}

	if dryRun {
		results := make([]presetApplyRow, 0, len(rows))
		for _, row := range rows {
			out, _ := p.row(row)
			if out.Outcome == presetSaved {
				out.Outcome = presetReady
			}
			results = append(results, out)
		}
		if writePresetRefusal(w, preset, only, results) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"preset": preset, "mix": mix, "dryRun": true, "results": results})
		return
	}

	ids := make([]int64, 0, len(rows))
	before := make(map[int64]engine.DestinationRun, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		// What each runs on now, and its last level note forgotten, before the
		// write: the answer is read back from what the reconcile then did.
		before[row.ID] = s.destinationRunBefore(row.ID, row.SourceID, row.SourceID)
	}
	presetApplyBetweenPhases()

	// The verdicts, from the rows as the transaction reads them. The callback
	// is pure: no store or engine call while the transaction is open.
	byID := make(map[int64]presetApplyRow, len(rows))
	written, missing, err := s.store.UpdateDestinationMixes(ids, func(cur *db.Destination) error {
		out, verdict := p.row(cur)
		byID[cur.ID] = out
		return verdict
	})
	for _, id := range missing {
		byID[id] = presetApplyRow{ID: id, Outcome: presetSkipped,
			Message: "the destination was deleted while the preset was being applied"}
	}
	results := make([]presetApplyRow, 0, len(rows))
	for _, row := range rows {
		out := byID[row.ID]
		if out.Name == "" {
			out.Name, out.Platform = row.Name, row.Platform
		}
		results = append(results, out)
	}
	if only != nil && len(missing) == 1 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if writePresetRefusal(w, preset, only, results) {
		return
	}
	if err != nil {
		// Not a row's refusal (those are answered above): the store or the
		// commit failed, and the transaction stored nothing.
		writeError(w, http.StatusInternalServerError,
			"the preset was not applied, and nothing was changed on any destination: "+err.Error())
		return
	}

	var reconcileWarn string
	if len(written) > 0 {
		reconcileWarn = presetReconcile(s)
		for _, d := range written {
			out := byID[d.ID]
			out.Applied, out.Reason = s.destinationApplied(d.ID, d.SourceID, before[d.ID], reconcileWarn)
			byID[d.ID] = out
		}
		for i := range results {
			if r, ok := byID[results[i].ID]; ok && r.Outcome == presetSaved {
				results[i] = r
			}
		}
	}
	writeMutation(w, http.StatusOK, reconcileWarn, map[string]any{"preset": preset, "mix": mix, "results": results})
}

// writePresetRefusal answers a request that cannot go ahead, and reports
// whether it did: any refused row refuses the request (400, every refused row
// listed), and a named destination that is not this preset's to change is a
// 409 rather than a 200 that changed nothing.
func writePresetRefusal(w http.ResponseWriter, preset string, only *int64, results []presetApplyRow) bool {
	var refused []presetApplyRow
	for _, r := range results {
		if r.Outcome == presetRefused {
			refused = append(refused, r)
		}
	}
	if len(refused) > 0 {
		names := make([]string, 0, len(refused))
		for _, r := range refused {
			names = append(names, fmt.Sprintf("%q", r.Name))
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("the %s preset was not applied: %s cannot take it, so nothing was changed on any destination",
				preset, strings.Join(names, ", ")),
			"results": refused,
		})
		return true
	}
	if only != nil && len(results) == 1 && results[0].Outcome == presetSkipped {
		writeError(w, http.StatusConflict, results[0].Message)
		return true
	}
	return false
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

// presetPlan is one request's preset, built once against the programme's
// ingest, and what it replaces.
type presetPlan struct {
	preset string
	mix    string
	built  routing.Profile
	src    routing.Source
}

// row decides one destination: skipped, refused, or its new mix put in place
// and checked. It edits cur's live or VOD mix in place -- the caller writes it,
// or throws it away -- and answers the row with its verdict for
// db.UpdateDestinationMixes: nil to write, db.ErrMixSkipped, or errPresetRefused.
//
// Pure: it reads only cur and the plan. The dry run calls it on the rows as
// listed and the apply on the rows as its transaction reads them, so the
// preview and the write cannot decide differently.
func (p presetPlan) row(cur *db.Destination) (presetApplyRow, error) {
	out := presetApplyRow{ID: cur.ID, Name: cur.Name, Platform: cur.Platform}
	// The gain route's test, and its reason: audio copy forwards the ingest's
	// audio untouched, so there is no mix here for a preset to replace.
	if cur.Audio.Copy && cur.Kind != db.DestAudio {
		out.Outcome, out.Message = presetSkipped,
			"this destination passes its audio through unchanged (audio copy), so it has no mix for a preset to replace"
		return out, db.ErrMixSkipped
	}
	stored := &cur.Profile
	if p.mix == presetMixVOD {
		if cur.VODProfile == nil {
			out.Outcome, out.Message = presetSkipped,
				"this destination has no second (VOD) mix; turn one on in the routing editor first"
			return out, db.ErrMixSkipped
		}
		stored = cur.VODProfile
	}
	next, kept := routing.KeepOperatorDecisions(p.built, stored)
	if p.mix == presetMixVOD {
		v := next
		cur.VODProfile = &v
	} else {
		cur.Profile = next
	}
	// UpdateDestination's own checks, so a refusal is had for every row before
	// any is written; db.UpdateDestinationMixes runs them again on the write.
	cur.Profile.ApplyDefaults()
	if err := cur.Validate(); err != nil {
		out.Outcome, out.Message = presetRefused, err.Error()
		return out, errPresetRefused
	}
	if p.mix == presetMixLive {
		next = cur.Profile
	}
	// Against the programme's tracks, as POST /routing/presets/{preset} checks
	// the preset alone. A kept exclusion can take out every track the preset
	// selected, and a destination that would stream silence is refused, never
	// saved (refuseIfSilent's rule).
	if _, err := routing.Compile(next, p.src); err != nil {
		out.Outcome, out.Message = presetRefused, err.Error()
		return out, errPresetRefused
	}
	out.Outcome, out.Profile = presetSaved, &next
	if len(kept.Muted) > 0 || len(kept.Excluded) > 0 {
		out.Kept = &kept
	}
	return out, nil
}
