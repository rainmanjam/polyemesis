package engine

import (
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
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
// clean start. It disarms when NO destination of the programme is enabled any
// more (stopped by hand or deleted), and after it has fired once; only the
// ingest returning AND a destination delivering arms it again. Destinations
// that are enabled but merely not carrying for a while -- every one in backoff
// after the platform dropped a starved RTMP connection -- do NOT disarm it: that
// is exactly what a dead ingest does to them, and the stop must still come.
// Recovery cancels the clock; so does any change of the wait. Nothing is ever
// restarted.
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
	// Destinations are this programme's destinations that were switched on at
	// that moment and not terminally failed: carrying media, holding a delay
	// buffer, or waiting out a backoff to try again. Never another programme's:
	// the engine only knows its own.
	Destinations []int64
	// Arriving reports, at the time it is called, whether the primary or the
	// backup ingest is arriving. The host calls it just before it stops: the
	// stop may have queued behind slow work, and an ingest that came back in the
	// meantime is not a loss. Nil means "unknown", and the stop goes ahead.
	Arriving func() bool
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
	// wait is the wait the clock was last measured against. A different one
	// restarts the clock: lowering the wait mid-outage must not fire on the next
	// sweep because of time counted against the old one.
	wait time.Duration
}

func (w *lossWatch) reset() { *w = lossWatch{} }

// step judges one sweep. It returns the destinations to stop when the ingest has
// stayed lost for the wait, once.
func (w *lossWatch) step(now time.Time, in lossInputs) (ids []int64, lostFor time.Duration, fire bool) {
	if !in.configured {
		w.reset()
		return nil, 0, false
	}
	if w.wait != in.wait {
		w.lostSince = time.Time{}
		w.wait = in.wait
	}
	delivering, enabled, targets := lossDestinations(in.dests)
	if enabled == 0 {
		// Nothing of this programme is switched on any more: stopped by hand or
		// deleted. Nothing to guard until something delivers again, and this holds
		// while the ingest is live too: a later destination that never delivers
		// must not inherit an arming from before. Merely not carrying (backoff,
		// reconnecting) is NOT this: see the header.
		w.reset()
		return nil, 0, false
	}
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
	if len(targets) == 0 {
		// Enabled destinations exist but every one has failed for good: there is
		// nothing the stop could do, and nothing is carrying. Disarm, so an
		// operator restarting one of them hours later, with the ingest still
		// gone, is not stopped before it has carried a byte.
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
	return targets, lostFor, true
}

// lossDestinations reads one sweep's destinations: whether any has delivered
// media (what arms the watch), how many are switched on (what keeps it armed)
// and which the stop would reach -- switched on and not terminally failed, so a
// destination waiting out a backoff is included and a failed one is not.
func lossDestinations(dests []DestStatus) (delivering bool, enabled int, targets []int64) {
	for _, d := range dests {
		if !d.Enabled {
			continue
		}
		enabled++
		if processDelivering(d.Process) {
			delivering = true
		}
		if d.Process != nil && d.Process.State == supervisor.StateFailed && !d.OnAir() {
			continue
		}
		targets = append(targets, d.ID)
	}
	return delivering, enabled, targets
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

// IngestArriving reports whether the primary or the backup ingest is arriving
// now: it was at the last observe sweep, or the hub's byte counter has moved
// since that sweep (so the answer is never up to a sweep stale in the
// direction that would stop a programme whose encoder has just returned). The
// host re-checks it just before stopping, because the stop may have queued
// behind slow work.
func (e *Engine) IngestArriving() bool {
	if e == nil {
		return false
	}
	if e.lossLive.Load() {
		return true
	}
	return e.hub != nil && e.hub.RxBytes() > e.lossRx.Load()
}

// PullReconnectSeconds is how long a pull source may legitimately wait between
// attempts, 0 for any other ingest. The API reports the effective wait with the
// same input the engine applies.
func PullReconnectSeconds(ing db.IngestSettings) int {
	if ing.Mode != db.IngestPull {
		return 0
	}
	if d := ing.Pull.ReconnectDelayMaxSeconds; d > 0 {
		return d
	}
	return ffmpeg.DefaultPullReconnectDelayMax
}

// sweepIngestLoss is sweepIngestLossWith taking its own status snapshot.
func (e *Engine) sweepIngestLoss(now time.Time, primaryLive bool) {
	e.sweepIngestLossWith(now, primaryLive, func() []DestStatus { return e.Status().Destinations })
}

// sweepIngestLossWith is the fallback's one step per observe sweep. primaryLive
// is the same value the sweep hands the alert watcher as IngestLive; dests is
// only called when the fallback is armed.
func (e *Engine) sweepIngestLossWith(now time.Time, primaryLive bool, dests func() []DestStatus) {
	e.mu.RLock()
	as := e.settings.Ingest.AutoStop
	fg := 0
	if e.settings.Failover.Enabled {
		fg = e.settings.Failover.GraceSeconds
	}
	pull := PullReconnectSeconds(e.settings.Ingest)
	h := e.ingestLoss
	configured := ingestConfigured(e.ingest != nil, e.settings.Ingest.Mode, !e.sourceDisabled)
	e.mu.RUnlock()
	if !as.Armed() || h == nil {
		e.lossW.reset()
		return
	}
	backupLive := false
	if fo := e.Failover(); fo != nil {
		// The selector's own sampling of the backup ingest's hub. False when
		// failover is off, which is right: there is no backup then.
		backupLive = fo.BackupLive
	}
	e.lossLive.Store(primaryLive || backupLive)
	if e.lossDests != nil {
		dests = e.lossDests
	}
	in := lossInputs{
		configured:  configured,
		primaryLive: primaryLive,
		backupLive:  backupLive,
		wait:        time.Duration(as.EffectiveWait(fg, pull)) * time.Second,
		dests:       dests(),
	}
	ids, lostFor, fire := e.lossW.step(now, in)
	if !fire {
		return
	}
	loss := IngestLoss{
		SourceID: e.sourceID, SourceName: e.SourceName(),
		Wait: in.wait, LostFor: lostFor, Destinations: ids,
		Arriving: e.IngestArriving,
	}
	e.log.Warn("ingest lost for the whole auto-stop wait; stopping this programme's live destinations",
		"source", e.sourceID, "wait", in.wait, "lostFor", lostFor, "destinations", len(ids))
	h(loss)
}
