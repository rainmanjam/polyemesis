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
	DumpDelay(id int64, want time.Duration) (delayline.DumpResult, error)
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
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	_, want, ok := readDumpRequest(w, r)
	if !ok {
		return
	}
	row, err := s.store.GetDestination(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if row.OutputDelaySeconds() <= 0 {
		code, msg := dumpRefusal(engine.ErrNotDelayed)
		writeError(w, code, msg)
		return
	}
	res, err := s.dumper().DumpDelay(id, want)
	if err != nil {
		code, msg := dumpRefusal(err)
		writeError(w, code, msg)
		return
	}
	s.publishAudit(auditDelayDumped(row, res, s.clientIP(r)))
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
	var (
		rows []*db.Destination
		err  error
	)
	if sourceID != nil {
		rows, err = s.store.ListDestinationsBySource(*sourceID)
	} else {
		rows, err = s.store.ListDestinations()
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	targets := dumpAllTargets(rows)
	results := make([]map[string]any, len(targets))
	d := s.dumper()
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
			res, err := d.DumpDelay(row.ID, want)
			if err != nil {
				_, msg := dumpRefusal(err)
				outcome := dumpAllFailed
				if errors.Is(err, delayline.ErrNoCutPoint) || errors.Is(err, delayline.ErrNotOnAir) ||
					errors.Is(err, engine.ErrNotDelayed) {
					outcome = dumpAllSkipped
				}
				results[i] = map[string]any{"id": row.ID, "name": row.Name, "outcome": outcome, "message": msg}
				return
			}
			out := dumpBody(row, res)
			out["outcome"] = dumpAllDumped
			results[i] = out
			s.publishAudit(auditDelayDumped(row, res, s.clientIP(r)))
		}(i, row)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"action": "dump", "results": results})
}

// auditDelayDumped records a dump: programme was withheld from the air on
// purpose, and the delay is shorter for it. The record says which
// destination, how much, and who asked -- never what was dumped.
func auditDelayDumped(row *db.Destination, res delayline.DumpResult, address string) alerts.Event {
	return alerts.Event{
		Type:     alerts.TypeDelayDumped,
		Severity: alerts.SeverityWarning,
		Title:    "Broadcast delay dumped",
		Text: fmt.Sprintf("%.1f s of programme was dropped from %s's broadcast delay and will not air there. "+
			"Its delay is now %.1f s, and returns to %d s when it next starts.",
			res.Dumped.Seconds(), row.Name, res.After.Seconds(), row.OutputDelaySeconds()),
	}.
		WithField(fieldDestination, row.Name).
		WithField(fieldAddress, address)
}
