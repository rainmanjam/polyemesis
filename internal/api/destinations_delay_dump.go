package api

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/delayline"
	"github.com/rainmanjam/polyemesis/internal/engine"
)

// DUMP (#11 follow-up): drop the newest part of a delayed destination's buffer
// and keep it on the air. See engine/delay_dump.go and delayline/splice.go.
//
//	POST /destinations/{id}/delay/dump        one destination
//	POST /destinations/delay/dump-all         every delayed destination of a programme
//
// Both are writes: a read token is refused, as on every POST. What they do
// cannot be undone -- the dumped programme never airs, and the delay stays
// shorter until the destination next starts -- so dump-all, like stop-all,
// demands {"confirm": true} of every caller. One destination's dump does not,
// like one destination's stop: the console confirms it, and an API caller
// naming one destination by id has said what they meant.

// delayDumper is what the handlers need from the engine. The manager in
// production; a fake in this package's tests, which have no engine.
type delayDumper interface {
	DumpDelay(id int64, want time.Duration, done func(delayline.DumpResult)) (delayline.DumpResult, error)
	DelayDumpable(id int64) bool
}

func (s *Server) dumper() delayDumper {
	if s.delayDumper != nil {
		return s.delayDumper
	}
	return s.mgr
}

// dumpRequest is both routes' body. Seconds 0 or absent dumps everything
// since the last dump point; anything else is how much of the newest
// programme to drop, and is clamped to what the safety margin allows.
type dumpRequest struct {
	Seconds *float64 `json:"seconds"`
	Confirm bool     `json:"confirm"`
	// AllProgrammes is dump-all's explicit "every programme on the install",
	// required when no ?source= is named on an install with more than one.
	AllProgrammes bool `json:"allProgrammes"`
}

// wantFor is the dump asked of one destination: the request's seconds when it
// gave some (0 included: "since the last dump point"), the destination's own
// default otherwise.
func (q dumpRequest) wantFor(row *db.Destination, asked time.Duration) time.Duration {
	if q.Seconds == nil && row.DelayDumpSeconds > 0 {
		return time.Duration(row.DelayDumpSeconds) * time.Second
	}
	return asked
}

func (q dumpRequest) want() (time.Duration, error) {
	if q.Seconds == nil {
		return 0, nil
	}
	v := *q.Seconds
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > engine.MaxDumpRequest.Seconds() {
		return 0, fmt.Errorf("seconds must be from 0 to %d", int(engine.MaxDumpRequest.Seconds()))
	}
	return time.Duration(v * float64(time.Second)), nil
}

// readDumpRequest reads an optional body. An empty one is the default dump.
func readDumpRequest(w http.ResponseWriter, r *http.Request) (dumpRequest, time.Duration, bool) {
	var req dumpRequest
	body, ok := readJSONBody(w, r)
	if !ok {
		return req, 0, false
	}
	if len(body) > 0 {
		if err := decodeJSONInto(body, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return req, 0, false
		}
	}
	want, err := req.want()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return req, 0, false
	}
	return req, want, true
}

// dumpBody is what a dump reports, per destination.
func dumpBody(row *db.Destination, res delayline.DumpResult) map[string]any {
	return map[string]any{
		"id":                  row.ID,
		"name":                row.Name,
		"complete":            res.Complete,
		"requestedSeconds":    res.Requested.Seconds(),
		"dumpedSeconds":       res.Dumped.Seconds(),
		"clamped":             res.Clamped,
		"delayBeforeSeconds":  res.Before.Seconds(),
		"delayAfterSeconds":   res.After.Seconds(),
		"configuredSeconds":   row.OutputDelaySeconds(),
		"restoresOnNextStart": true,
		"recovery":            res.Recovery,
		"forced":              res.Forced,
	}
}

// dumpRefusal is the operator's sentence for a refused dump, and its status.
func dumpRefusal(err error) (int, string) {
	switch {
	case errors.Is(err, engine.ErrNotDelayed):
		return http.StatusConflict, "this destination is not running a broadcast delay, so there is nothing to dump"
	case errors.Is(err, delayline.ErrNotOnAir):
		return http.StatusConflict, "the delayed feed is not on the air: it is still filling its delay, " +
			"or it was stopped and is airing its tail (use Cut now for that)"
	case errors.Is(err, delayline.ErrDumpPending):
		return http.StatusConflict, "a dump is still waiting for the next keyframe; try again in a moment"
	case errors.Is(err, delayline.ErrNoCutPoint):
		return http.StatusConflict, fmt.Sprintf("there is no keyframe in the buffer that leaves the %d s "+
			"safety margin plus one GOP of delay, so nothing more can be dumped", int(delayline.DumpMargin.Seconds()))
	case errors.Is(err, delayline.ErrNotSpliceable):
		return http.StatusConflict, "this stream has no keyframe a dump can cut at: dump needs H.264 or HEVC " +
			"with IDR keyframes (a closed GOP), which every mainstream encoder sends by default"
	}
	return http.StatusInternalServerError, err.Error()
}

func (s *Server) handleDumpDestinationDelay(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, errInvalidID)
		return
	}
	req, want, ok := readDumpRequest(w, r)
	if !ok {
		return
	}
	// ?source= names the programme the caller means. A destination on another
	// programme is not found there: an agent bound to programme A must not
	// dump programme B's destination by its id.
	sourceID, ok := s.namedSourceParam(w, r)
	if !ok {
		return
	}
	row, err := s.store.GetDestination(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if sourceID != nil && (row.SourceID == nil || *row.SourceID != *sourceID) {
		writeError(w, http.StatusNotFound, "this destination is not on that programme")
		return
	}
	want = req.wantFor(row, want)
	if row.OutputDelaySeconds() <= 0 {
		code, msg := dumpRefusal(engine.ErrNotDelayed)
		writeError(w, code, msg)
		return
	}
	res, err := s.dumpAudited(s.dumper(), row, want, s.clientIP(r))
	if err != nil {
		code, msg := dumpRefusal(err)
		writeError(w, code, msg)
		return
	}
	writeJSON(w, http.StatusOK, dumpBody(row, res))
}

// dumpAllOutcome is one row of a dump-all.
type dumpAllOutcome string

const (
	dumpAllDumped  dumpAllOutcome = "dumped"
	dumpAllSkipped dumpAllOutcome = "skipped"
	dumpAllFailed  dumpAllOutcome = "failed"
)

// dumpAllTargets is which rows a dump-all acts on: the programme's delayed,
// enabled destinations, and nothing else. Undelayed destinations are not
// listed at all -- a dump has nothing to say about them.
func dumpAllTargets(rows []*db.Destination) []*db.Destination {
	var out []*db.Destination
	for _, row := range rows {
		if row.Enabled && row.OutputDelaySeconds() > 0 {
			out = append(out, row)
		}
	}
	return out
}

func (s *Server) handleDumpAllDelays(w http.ResponseWriter, r *http.Request) {
	req, want, ok := readDumpRequest(w, r)
	if !ok {
		return
	}
	if !req.Confirm {
		writeError(w, http.StatusBadRequest,
			"dumping every delayed destination drops the newest part of each one's buffer, and that "+
				"programme never airs anywhere; the delay stays shorter until each destination next starts. "+
				`Repeat this request with a JSON body of {"confirm": true} once that is intended.`)
		return
	}
	// Scoped to the programme named, exactly as stop-all is: unnamed means the
	// whole store, which is what a one-programme install sends.
	sourceID, ok := s.namedSourceParam(w, r)
	if !ok {
		return
	}
	if !s.checkDumpAllScope(w, sourceID, req.AllProgrammes) {
		return
	}
	rows, err := s.dumpAllRows(sourceID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	targets := dumpAllTargets(rows)
	results := make([]map[string]any, len(targets))
	d := s.dumper()
	addr := s.clientIP(r)
	// In parallel: each dump waits up to a GOP for its own resume, and the
	// lines are independent.
	var wg sync.WaitGroup
	for i, row := range targets {
		if !d.DelayDumpable(row.ID) {
			results[i] = map[string]any{"id": row.ID, "name": row.Name, "outcome": dumpAllSkipped,
				"message": "not on the air: still filling its delay, or stopped and airing its tail"}
			continue
		}
		wg.Add(1)
		go func(i int, row *db.Destination) {
			defer wg.Done()
			res, err := s.dumpAudited(d, row, req.wantFor(row, want), addr)
			if err != nil {
				_, msg := dumpRefusal(err)
				outcome := dumpAllFailureOutcome(err)
				results[i] = map[string]any{"id": row.ID, "name": row.Name, "outcome": outcome, "message": msg}
				return
			}
			out := dumpBody(row, res)
			out["outcome"] = dumpAllDumped
			results[i] = out
		}(i, row)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"action": "dump", "results": results})
}

// checkDumpAllScope refuses an unscoped dump on a multi-programme install, and
// reports false after writing the refusal. It reports true when the dump may run.
func (s *Server) checkDumpAllScope(w http.ResponseWriter, sourceID *int64, allProgrammes bool) bool {
	// UNSCOPED ON A MULTI-PROGRAMME INSTALL IS A SEPARATE INTENT. Without
	// ?source= this reaches every programme's delayed destinations, which a
	// caller that simply forgot the parameter (a deck key whose programme
	// lookup failed) must not do by accident.
	if sourceID != nil || allProgrammes {
		return true
	}
	srcs, err := s.store.ListSources()
	if err != nil {
		writeStoreError(w, err)
		return false
	}
	if len(srcs) > 1 {
		writeError(w, http.StatusBadRequest, "this install has more than one programme: name one with "+
			`?source=<id>, or send "allProgrammes": true to dump every programme's delayed destinations`)
		return false
	}
	return true
}

// dumpAllFailureOutcome is the outcome of a dump that returned an error: skipped
// when the destination had nothing it could dump, failed otherwise.
func dumpAllFailureOutcome(err error) dumpAllOutcome {
	if errors.Is(err, delayline.ErrNoCutPoint) || errors.Is(err, delayline.ErrNotOnAir) ||
		errors.Is(err, engine.ErrNotDelayed) {
		return dumpAllSkipped
	}
	return dumpAllFailed
}

// dumpAllRows returns the destinations a dump-all reaches: the named programme's,
// or every destination when none is named.
func (s *Server) dumpAllRows(sourceID *int64) ([]*db.Destination, error) {
	if sourceID != nil {
		return s.store.ListDestinationsBySource(*sourceID)
	}
	return s.store.ListDestinations()
}

// auditDelayDumped records a dump: programme was withheld from the air on
// purpose, and the delay is shorter for it. The record says which
// destination, how much, and who asked -- never what was dumped.
func auditDelayDumped(row *db.Destination, res delayline.DumpResult, address string) alerts.Event {
	text := fmt.Sprintf("%.1f s of programme was dropped from %s's broadcast delay and will not air there. "+
		"Its delay is now %.1f s, and returns to %d s when it next starts.",
		res.Dumped.Seconds(), row.Name, res.After.Seconds(), row.OutputDelaySeconds())
	status := "final"
	switch {
	case res.Aborted:
		status = "aborted"
		text = fmt.Sprintf("A dump cut at least %.1f s of programme from %s's broadcast delay, which will not "+
			"air there, and the destination was stopped or restarted before the stream resumed.",
			res.Dumped.Seconds(), row.Name)
	case res.Forced:
		status = "forced"
		text += " No keyframe came after the cut, so the stream resumed at the live edge: the platform" +
			" showed a broken picture until the source's next keyframe."
	case !res.Complete:
		status = "provisional"
		text += " Provisional: the stream resumes at the next keyframe, and a final record follows."
	}
	return alerts.Event{
		Type:     alerts.TypeDelayDumped,
		Severity: alerts.SeverityWarning,
		Title:    "Broadcast delay dumped",
		Text:     text,
	}.
		WithField(fieldDestination, row.Name).
		WithField(fieldDumpStatus, status).
		WithField(fieldAddress, address)
}

// dumpAudited runs one dump and audits it. EVERY ACCEPTED DUMP LEAVES A RECORD
// at once -- final when the stream resumed inside the answer, provisional
// otherwise -- and a provisional one is followed by the outcome: the final
// figures at the resume, or "aborted" when the line was cut first. The
// completion can arrive before the answer does, so the two are ordered here.
func (s *Server) dumpAudited(d delayDumper, row *db.Destination, want time.Duration, addr string) (delayline.DumpResult, error) {
	var (
		mu        sync.Mutex
		accepted  bool
		finalSeen bool
		pending   *delayline.DumpResult
	)
	// final is finalSeen as read under mu: every access to the shared state
	// is under the lock, the follow-up's included.
	followUp := func(out delayline.DumpResult, final bool) {
		if final {
			return // the record written at acceptance was already the outcome
		}
		s.publishAudit(auditDelayDumped(row, out, addr))
	}
	res, err := d.DumpDelay(row.ID, want, func(out delayline.DumpResult) {
		mu.Lock()
		if !accepted {
			pending = &out
			mu.Unlock()
			return
		}
		final := finalSeen
		mu.Unlock()
		followUp(out, final)
	})
	if err != nil {
		return res, err
	}
	s.publishAudit(auditDelayDumped(row, res, addr))
	mu.Lock()
	// Final or aborted at acceptance: that record is the dump's one outcome.
	accepted, finalSeen = true, res.Complete || res.Aborted
	out, final := pending, finalSeen
	mu.Unlock()
	if out != nil {
		followUp(*out, final)
	}
	return res, nil
}
