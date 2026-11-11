package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/hooks"
	"github.com/rainmanjam/polyemesis/internal/obsbridge"
)

// STOP-ON-INGEST-LOSS: THE HOST HALF (settings.ingest.autoStop).
//
// The engine decides (internal/engine/autostop.go) and calls ingestLossStop. It
// is deliberately a SECOND CALLER OF THE STOP PATH THE OBS BRIDGE USES --
// applyDestinationEnabled through bulkOneCut -- and not a new one, so the
// fallback can never do more than the operator's own Stop button does.
//
// WHAT THAT MEANS FOR YOUTUBE. A stop ends the destination's YouTube broadcast
// permanently (destinations_bulk.go's file header). That is why the setting
// needs its own acknowledgement (db.AutoStopSettings.Acknowledged) and why the
// OBS bridge's stopAcknowledged does not stand in for it.
//
// THE BROADCAST DELAY. The stop is a DRAIN, not a cut: a destination holding a
// delay buffer keeps airing its tail, and only then ends (the lifecycle end
// waits for the drain: delayStillPublishing). Stop-all cuts, because it is the
// panic button; this is a safety net, and the tail is programme the audience
// has not yet seen. So the stop is REQUESTED at loss + wait and TAKES EFFECT, for
// a delayed destination, when its tail has aired -- at loss + max(wait, delay).
// Waiting a further `delay` first would only add dead air behind the tail.

// ingestLossStop is the engine's IngestLossHandler. It returns at once.
func (s *Server) ingestLossStop(l engine.IngestLoss) {
	run := func(ctx context.Context) []obsbridge.Outcome { return s.stopForIngestLoss(ctx, l) }
	// When the OBS bridge drives this programme the stop runs on ITS worker, so
	// it is serialised with OBS's own stop and recorded in lastActions.
	if s.obs != nil && s.obs.RunIngestStop(l.SourceID, run) {
		return
	}
	// Otherwise it runs on the server's own lifetime, which shutdown cancels and
	// waits for (StopIngestLossWithin). A queue that was full still gets its
	// record: RecordIngestStop is a no-op when the bridge is not involved.
	s.ingestStops.start(func(ctx context.Context) {
		res := run(ctx)
		if s.obs != nil {
			s.obs.RecordIngestStop(l.SourceID, res)
		}
	})
}

// StopIngestLossWithin ends the fallback's off-worker stops before the process
// goes away: it cancels their context and waits for them within ctx.
func (s *Server) StopIngestLossWithin(ctx context.Context) { s.ingestStops.shutdown(ctx) }

// ingestStopRunner runs the fallback's stops that are not on the OBS worker, on
// one lifetime context with a WaitGroup, so shutdown can end and await them
// instead of leaving a goroutine mid-stop. Lazily initialised: a Server is
// built bare in tests.
type ingestStopRunner struct {
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed bool
}

func (r *ingestStopRunner) start(fn func(ctx context.Context)) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	if r.ctx == nil {
		r.ctx, r.cancel = context.WithCancel(context.Background())
	}
	ctx := r.ctx
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		fn(ctx)
	}()
}

func (r *ingestStopRunner) shutdown(ctx context.Context) {
	r.mu.Lock()
	r.closed = true
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// stopForIngestLoss stops the programme's live destinations and reports what it
// did. It stops only destinations that are still on this programme and still
// switched on, and says "skipped" for the rest, which is what lets a second stop
// of the same destinations (OBS's, arriving at the same moment) do and record
// nothing.
func (s *Server) stopForIngestLoss(ctx context.Context, l engine.IngestLoss) []obsbridge.Outcome {
	out := make([]obsbridge.Outcome, 0, len(l.Destinations))
	skip := func(id int64, name, why string) {
		out = append(out, obsbridge.Outcome{ID: id, Name: name, Outcome: string(bulkSkipped), Message: why})
	}
	// Re-read at run time: the stop may have waited behind a paced OBS start, and
	// an operator may have turned the setting off, or removed the programme,
	// since the engine decided.
	src, err := s.store.GetSource(l.SourceID)
	if err != nil || !src.Ingest.AutoStop.Armed() {
		for _, id := range l.Destinations {
			skip(id, "", "the stop-on-ingest-loss setting was turned off before it ran, so nothing was stopped")
		}
		return out
	}
	// And the ingest itself, at THIS moment: the stop was decided when the wait
	// ran out but it may have queued behind a paced OBS start for a long time,
	// and an encoder that came back in between (OBS restarting near the end of
	// the wait) is not a loss. Skips every destination, ends nothing.
	// l.Arriving is the engine as it was when it decided; if that engine has
	// been rebuilt since, its answer is for a hub that no longer exists, so the
	// engine now serving this programme is asked too.
	if (l.Arriving != nil && l.Arriving()) || s.currentIngestArriving(l.SourceID) {
		for _, id := range l.Destinations {
			skip(id, "", "the ingest returned before the stop ran, so nothing was stopped")
		}
		return out
	}
	for _, id := range l.Destinations {
		if ctx.Err() != nil {
			skip(id, "", "the server was shutting down")
			continue
		}
		row, err := s.store.GetDestination(id)
		if err != nil {
			out = append(out, obsbridge.Outcome{ID: id, Outcome: string(bulkFailed), Message: err.Error()})
			continue
		}
		src := l.SourceID
		if !sameProgramme(row.SourceID, &src) {
			out = append(out, obsbridge.Outcome{ID: id, Name: row.Name, Outcome: string(bulkFailed),
				Message: "this destination has moved to another programme, so stop-on-ingest-loss left it alone"})
			continue
		}
		if !row.Enabled {
			skip(id, row.Name, "already stopped")
			continue
		}
		out = append(out, fromBulk(s.bulkOneCut(row, false, false)))
	}
	if acted(out) {
		s.announceIngestStop(l, out)
	}
	return out
}

// currentIngestArriving asks the engine that serves the programme NOW, by
// source ID. False when there is none.
func (s *Server) currentIngestArriving(sourceID int64) bool {
	if s.mgr == nil {
		return false
	}
	return s.mgr.Engine(sourceID).IngestArriving()
}

func acted(rs []obsbridge.Outcome) bool {
	for _, r := range rs {
		if r.Outcome != string(bulkSkipped) {
			return true
		}
	}
	return false
}

// auditIngestAutoStopped builds the alert: stop-on-ingest-loss ended a
// programme's live destinations. Operational rather than an audit event, so it
// carries a coalescing Key (one subject per programme, so two programmes
// stopping together are two messages and one flapping programme is one).
//
// Programme and counts are fields, and sourceId/sourceName are spelled as the
// engine watcher spells them, so a script routing ingest.lost by programme
// routes this the same way. The programme name is operator-typed free text and
// goes through the notifier's redaction like every other field. failed is the
// number of destinations the stop tried and could not end; it is mentioned only
// when it is not zero, because then "stopped 0" would hide that something is
// still on air.
func auditIngestAutoStopped(l engine.IngestLoss, stopped, failed int) alerts.Event {
	lost := l.LostFor.Round(time.Second)
	text := fmt.Sprintf("Neither the primary nor the backup ingest arrived for %s (the wait was %s), so %d "+
		"live destination(s) were stopped. Nothing restarts by itself: go live again once the encoder is back.",
		lost, l.Wait.Round(time.Second), stopped)
	if failed > 0 {
		text += fmt.Sprintf(" %d more could not be stopped and may still be on air.", failed)
	}
	title := "Stopped automatically: ingest lost"
	if l.SourceName != "" {
		title += ": " + l.SourceName
	}
	ev := alerts.Event{
		Type:     alerts.TypeIngestAutoStopped,
		Severity: alerts.SeverityCritical,
		Key:      fmt.Sprintf("ingest.autostopped:%d", l.SourceID),
		Title:    title,
		Text:     text,
		At:       time.Now(),
	}.
		WithField("sourceId", strconv.FormatInt(l.SourceID, 10)).
		WithField("sourceName", l.SourceName).
		WithField("wait", l.Wait.Round(time.Second).String()).
		WithField("lostFor", lost.String()).
		WithField("stopped", strconv.Itoa(stopped))
	if failed > 0 {
		ev = ev.WithField("failed", strconv.Itoa(failed))
	}
	return ev
}

// announceIngestStop writes the log line and publishes the event. It is called
// only when something was actually stopped.
func (s *Server) announceIngestStop(l engine.IngestLoss, results []obsbridge.Outcome) {
	stopped, failed := 0, 0
	for _, r := range results {
		switch r.Outcome {
		case string(bulkStopped), string(bulkWarned):
			stopped++
		case string(bulkFailed):
			failed++
		}
	}
	s.log.Warn("stop-on-ingest-loss stopped the programme's live destinations",
		"source", l.SourceID, "name", l.SourceName, "wait", l.Wait, "lostFor", l.LostFor.Round(time.Second),
		"stopped", stopped, "of", len(results))
	// The alert is raised HERE, where the stop has actually happened, and not at
	// the engine's decision: the run-time re-check above can skip everything (the
	// ingest came back, the setting was turned off), and an alert for a stop that
	// did not happen would be a false page. It goes before the stream check so a
	// server without an events hub still alerts.
	s.publishAudit(auditIngestAutoStopped(l, stopped, failed))
	if s.stream == nil {
		return
	}
	s.stream.Publish(eventstream.Envelope{
		Type:   eventstream.TypeIngestAutoStopped,
		At:     time.Now(),
		Source: &hooks.SourceRef{ID: l.SourceID, Name: l.SourceName},
		Reason: fmt.Sprintf("neither the primary nor the backup ingest arrived for %ds", int(l.LostFor.Seconds())),
		Data: map[string]any{
			"waitSeconds":    int(l.Wait.Seconds()),
			"lostForSeconds": int(l.LostFor.Seconds()),
			"results":        results,
		},
	})
}

// autoStopNewlyEnabledWithoutAck reports a write that turned the fallback on,
// which was off before, without the request itself carrying the
// acknowledgement. A source PUT merges over the stored row, so an
// acknowledgement given once last month would otherwise let a script enable it
// by omission.
func autoStopNewlyEnabledWithoutAck(prev, next db.AutoStopSettings, sentAck bool) bool {
	return next.Enabled && !prev.Enabled && !sentAck
}

// sentAutoStopAck reports whether a source body says ingest.autoStop.acknowledged
// is true. Unparseable bodies say false; the strict decode has already refused
// them by the time this matters.
// sourceAutoStopAck, sourceIngestAck and sourceBodyAck mirror the part of a
// source body that sentAutoStopAck reads; the JSON tags match the wire names.
type sourceAutoStopAck struct {
	Acknowledged *bool `json:"acknowledged"`
}

type sourceIngestAck struct {
	AutoStop sourceAutoStopAck `json:"autoStop"`
}

type sourceBodyAck struct {
	Ingest sourceIngestAck `json:"ingest"`
}

func sentAutoStopAck(body []byte) bool {
	var b sourceBodyAck
	if json.Unmarshal(body, &b) != nil {
		return false
	}
	a := b.Ingest.AutoStop.Acknowledged
	return a != nil && *a
}

// autoStopInfo is what GET /sources/{id} reports beside the stored setting.
type autoStopInfo struct {
	// Version identifies the stored setting. A source PUT that CHANGES
	// ingest.autoStop sends it back as autoStopVersion, and one that turns the
	// fallback ON must: see autoStopStaleWrite.
	Version string `json:"version"`
	// EffectiveWaitSeconds is the wait the engine applies: the saved wait,
	// lifted by the floor, the failover grace and a pull source's reconnect
	// delay (db.AutoStopSettings.EffectiveWaitWhy, the engine's own rule).
	EffectiveWaitSeconds int `json:"effectiveWaitSeconds"`
	// RaisedBy names what lifted it above the saved wait ("failover" or "pull"),
	// absent when nothing did.
	RaisedBy string `json:"raisedBy,omitempty"`
}

// autoStopVersion is a digest of the stored setting, so it changes whenever any
// door changes the setting and no writer can forget to bump it (the same
// reasoning as settingsVersion).
func autoStopVersion(a db.AutoStopSettings) string {
	b, err := json.Marshal(a.Stored())
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func autoStopInfoFor(src *db.Source, fo db.FailoverSettings) autoStopInfo {
	grace := 0
	if fo.Enabled {
		grace = fo.GraceSeconds
	}
	as := src.Ingest.AutoStop
	w, by := as.EffectiveWaitWhy(grace, engine.PullReconnectSeconds(src.Ingest))
	return autoStopInfo{Version: autoStopVersion(as), EffectiveWaitSeconds: w, RaisedBy: by}
}

// takeAutoStopVersion removes the top-level `autoStopVersion` from a source PUT
// body and returns it (nil when absent): removed, not merely read, because the
// body is then strictly decoded into db.Source and it is not a source field.
func takeAutoStopVersion(body []byte) ([]byte, *string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return body, nil, nil
	}
	raw, ok := fields["autoStopVersion"]
	if !ok {
		return body, nil, nil
	}
	delete(fields, "autoStopVersion")
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, nil, fmt.Errorf("autoStopVersion must be the string GET /sources/{id} returned in autoStopInfo.version")
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, err
	}
	return out, &v, nil
}

// autoStopStaleWrite refuses a source write that changes ingest.autoStop on the
// strength of a read that is no longer true. status 0 means accepted.
//
// Why a version and not the acknowledgement alone: consent is given to the
// state the operator SAW. A control left open after the setting was turned off
// elsewhere still holds enabled+acknowledged, and an old cached page sends the
// whole ingest; either would otherwise turn a YouTube-ending setting back on
// with a perfectly valid acknowledgement in the body.
//
//   - A write that changes the setting and sends a version that is not the
//     stored one is a 409, and nothing is saved.
//   - A write that turns it ON must send a version at all (400 otherwise): a
//     script reads GET /sources/{id} first, which is what makes the consent
//     informed. Other changes (the wait, turning it off) may omit it: they cannot
//     start a stop that the operator had not agreed to.
func autoStopStaleWrite(prev, next db.AutoStopSettings, sent *string) (status int, msg string) {
	prev, next = prev.Stored(), next.Stored()
	if prev == next {
		return 0, ""
	}
	if sent == nil {
		if next.Enabled && !prev.Enabled {
			return http.StatusBadRequest, "turning stop on ingest loss on needs the current autoStopInfo.version from " +
				"GET /sources/{id}, sent as autoStopVersion, so the acknowledgement is given to the setting as it is now"
		}
		return 0, ""
	}
	if *sent != autoStopVersion(prev) {
		return http.StatusConflict, "this programme's stop-on-ingest-loss setting was changed after this page " +
			"loaded it, so nothing was saved. Reload to see the current setting, then make your change again"
	}
	return 0, ""
}
