package engine

import (
	"time"
)

// STOP-ON-INGEST-LOSS (settings.ingest.autoStop; db.AutoStopSettings).
//
// ONE MECHANISM, HUNG OFF THE SIGNAL THE ENGINE ALREADY HAS. observeLoop reads
// the primary hub's byte counter once a sweep and calls the result `live`; that
// is what the alert watcher judges ingest.lost against. This takes the same
// `live`, and adds the one thing the alert does not look at -- the backup
// ingest's liveness, from the selector's own sampler (Failover().BackupLive) --
// because "a failover to the backup is not a loss". There is no second byte
// watcher and no second timer: lossWatch is a state machine stepped by the
// sweep's own clock.
//
// WHAT "LOST" MEANS HERE, AND WHAT IT DOES NOT. Neither the primary nor the
// backup ingest is arriving. A slate or a playlist on air is NOT a stream: they
// are read off the selector's output hub, which this never looks at, so a
// programme sitting on its slate with no encoder is lost -- the opposite of the
// start hold's rule (feedWatch), which counts them, and deliberately not
// reusing it.
//
// ARMED ONLY BY EVIDENCE. The watch arms while the ingest is arriving AND at
// least one enabled destination has delivered media (a process running with
// uptime). A boot, an idle programme, a destination still starting and a
// destination that never got data therefore never arm it: it cannot fire from a
// clean start. It disarms when nothing is on air any more, and after it has
// fired once; only the ingest returning AND a destination delivering arms it
// again. Recovery cancels the clock. Nothing is ever restarted.
//
// THE STOP ITSELF IS THE HOST'S. The engine hands IngestLoss to the handler the
// API installs (Manager.SetIngestLossHandler), which runs the existing stop path
// the OBS bridge uses. See internal/api/autostop.go.

// IngestLoss is one programme's ingest having stayed lost for its wait.
type IngestLoss struct {
	SourceID   int64
	SourceName string
	// Wait is the wait the engine applied, after the floor and the failover
	// clearance (db.AutoStopSettings.EffectiveWait), and LostFor how long the
	// ingest had actually been gone when it fired.
	Wait, LostFor time.Duration
	// Destinations are this programme's destinations that were on air at that
	// moment, enabled and with a process carrying media or holding a delay
	// buffer. Never another programme's: the engine only knows its own.
	Destinations []int64
}

// IngestLossHandler is called from the observe sweep, with no engine lock held.
// It MUST return promptly: the stop is slow, so the implementation hands it to
// its own goroutine.
type IngestLossHandler func(IngestLoss)

// lossInputs is everything one step reads, so a test supplies it directly.
type lossInputs struct {
	// configured is false for a programme with no ingest that could be lost
	// (disabled, mode unset): nothing to lose, so the watch resets.
	configured  bool
	primaryLive bool
	backupLive  bool
	dests       []DestStatus
	wait        time.Duration
}

// lossWatch is the state machine. Touched only by the observe sweep.
type lossWatch struct {
	armed     bool
	lostSince time.Time
}

func (w *lossWatch) reset() { *w = lossWatch{} }

// step judges one sweep. It returns the destinations to stop when the ingest has
// stayed lost for the wait, once.
func (w *lossWatch) step(now time.Time, in lossInputs) (ids []int64, lostFor time.Duration, fire bool) {
	if !in.configured {
		w.reset()
		return nil, 0, false
	}
	delivering, onAir := lossDestinations(in.dests)
	if in.primaryLive || in.backupLive {
		// Arriving: any loss clock is cancelled, whatever it had counted.
		w.lostSince = time.Time{}
		if delivering {
			w.armed = true
		}
		return nil, 0, false
	}
	if !w.armed {
		return nil, 0, false
	}
	if len(onAir) == 0 {
		// Whatever it would stop is already off: stopped by hand, failed, or
		// deleted. Nothing to guard until something delivers again.
		w.reset()
		return nil, 0, false
	}
	if w.lostSince.IsZero() {
		w.lostSince = now
	}
	if now.Sub(w.lostSince) < in.wait {
		return nil, 0, false
	}
	lostFor = now.Sub(w.lostSince)
	// Fired: disarmed until the ingest returns and something delivers again.
	w.armed = false
	return onAir, lostFor, true
}

// lossDestinations splits one sweep's destinations into "has delivered media"
// (what arms the watch) and "is on air" (what the stop would reach).
func lossDestinations(dests []DestStatus) (delivering bool, onAir []int64) {
	for _, d := range dests {
		if !d.Enabled {
			continue
		}
		if processDelivering(d.Process) {
			delivering = true
		}
		if d.OnAir() || delayHoldsBack(d) {
			onAir = append(onAir, d.ID)
		}
	}
	return delivering, onAir
}

// SetIngestLossHandler installs what is called when the fallback fires. A
// setter, like SetStream: engines are built whenever a programme is added.
func (e *Engine) SetIngestLossHandler(h IngestLossHandler) {
	e.mu.Lock()
	e.ingestLoss = h
	e.mu.Unlock()
}

// autoStopWanted reports whether the sweep must run for this fallback even if
// no alert rule, hook or stream subscriber would otherwise want it.
func (e *Engine) autoStopWanted() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.settings.Ingest.AutoStop.Armed() && e.ingestLoss != nil
}

// lossDestStatuses is where the sweep reads destinations from; a test replaces
// it, so no FFmpeg is needed to say "a destination delivered".
func (e *Engine) lossDestStatuses() []DestStatus {
	if e.lossDests != nil {
		return e.lossDests()
	}
	return e.Status().Destinations
}

// sweepIngestLoss is the fallback's one step per observe sweep. primaryLive is
// the same value the sweep hands the alert watcher as IngestLive.
func (e *Engine) sweepIngestLoss(now time.Time, primaryLive bool) {
	e.mu.RLock()
	as := e.settings.Ingest.AutoStop
	fg := 0
	if e.settings.Failover.Enabled {
		fg = e.settings.Failover.GraceSeconds
	}
	h := e.ingestLoss
	configured := ingestConfigured(e.ingest != nil, e.settings.Ingest.Mode, !e.sourceDisabled)
	e.mu.RUnlock()
	if !as.Armed() || h == nil {
		e.lossW.reset()
		return
	}
	in := lossInputs{
		configured:  configured,
		primaryLive: primaryLive,
		wait:        time.Duration(as.EffectiveWait(fg)) * time.Second,
		dests:       e.lossDestStatuses(),
	}
	if fo := e.Failover(); fo != nil {
		// The selector's own sampling of the backup ingest's hub. False when
		// failover is off, which is right: there is no backup then.
		in.backupLive = fo.BackupLive
	}
	ids, lostFor, fire := e.lossW.step(now, in)
	if !fire {
		return
	}
	loss := IngestLoss{
		SourceID: e.sourceID, SourceName: e.SourceName(),
		Wait: in.wait, LostFor: lostFor, Destinations: ids,
	}
	e.log.Warn("ingest lost for the whole auto-stop wait; stopping this programme's live destinations",
		"source", e.sourceID, "wait", in.wait, "lostFor", lostFor, "destinations", len(ids))
	h(loss)
}
