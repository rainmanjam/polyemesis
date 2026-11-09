package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

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
	if l.Arriving != nil && l.Arriving() {
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

func acted(rs []obsbridge.Outcome) bool {
	for _, r := range rs {
		if r.Outcome != string(bulkSkipped) {
			return true
		}
	}
	return false
}

// announceIngestStop writes the log line and publishes the event. It is called
// only when something was actually stopped.
func (s *Server) announceIngestStop(l engine.IngestLoss, results []obsbridge.Outcome) {
	stopped := 0
	for _, r := range results {
		if r.Outcome == string(bulkStopped) || r.Outcome == string(bulkWarned) {
			stopped++
		}
	}
	s.log.Warn("stop-on-ingest-loss stopped the programme's live destinations",
		"source", l.SourceID, "name", l.SourceName, "wait", l.Wait, "lostFor", l.LostFor.Round(time.Second),
		"stopped", stopped, "of", len(results))
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
func sentAutoStopAck(body []byte) bool {
	var b struct {
		Ingest struct {
			AutoStop struct {
				Acknowledged *bool `json:"acknowledged"`
			} `json:"autoStop"`
		} `json:"ingest"`
	}
	if json.Unmarshal(body, &b) != nil {
		return false
	}
	a := b.Ingest.AutoStop.Acknowledged
	return a != nil && *a
}
