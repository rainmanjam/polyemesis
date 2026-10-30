package engine

import (
	"strconv"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/hooks"
)

// The engine's half of the real-time events API (internal/eventstream).
//
// EVERY PUBLISH HERE SITS BESIDE AN EXISTING ONE, never in place of it: the
// webhook edge, the alert, the bus message, the reload note. The stream is told
// the same fact at the same moment, by the same code path, so it cannot come to
// disagree with the surfaces that were there first.

// Stream returns the events hub, or nil. Under e.mu, like Hooks and for the
// same reason: SetStream reaches running engines through the manager.
func (e *Engine) Stream() *eventstream.Hub {
	e.requireEngine("Stream")
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.stream
}

// SetStream attaches the shared events hub, and taps this engine's alert
// notifier into it.
//
// A setter rather than a New parameter, matching SetHooks: engines are created
// whenever a source is added, long after main built the hub.
func (e *Engine) SetStream(h *eventstream.Hub) {
	e.mu.Lock()
	e.stream = h
	e.mu.Unlock()
	if h == nil {
		e.alerter.SetTap(nil)
		return
	}
	// The notifier's own redacted copy, from Publish itself, so every alert --
	// the sweep's, the API's audit events, anything added later -- reaches the
	// stream through the one door they all already use.
	//
	// NO PROGRAMME IS STAMPED. An alert names its subject in its own key and
	// title, scoped by the watcher that raised it; and the API's audit events
	// (sign-ins, token changes) are published through whichever engine is
	// first, so stamping this engine's programme on them would attach a
	// programme they have nothing to do with.
	e.alerter.SetTap(func(ev alerts.Event) {
		h.Publish(eventstream.Envelope{Type: eventstream.TypeAlert, At: ev.At, Data: ev})
	})
}

// observeEdges is one sweep's edge work for every consumer of hookWatch: the
// webhook dispatcher, the lifecycle coordinator and the events stream.
//
// Its own function, extracted from observeLoop, so a test drives exactly the
// path the loop drives.
func (e *Engine) observeEdges(snap alerts.Snapshot) {
	if e.hookWatch == nil {
		return
	}
	// Re-stamped every sweep: the source row is named after the
	// engine is built, and an event carrying only an id tells a
	// script which programme but not which one an operator would
	// recognise.
	src := hooks.SourceRef{ID: e.sourceID, Name: e.SourceName()}
	e.hookWatch.SetSource(src)
	// ONE DERIVATION, THREE CONSUMERS -- four, with the events stream. The
	// lifecycle coordinator sees exactly the events the webhook dispatcher
	// sees, from the same Observe call on the same watcher over the same
	// snapshot. A second sampler would mean two Status() calls at two
	// cadences that could disagree about what they saw -- and the
	// disagreement that matters is "was this destination disabled
	// or did it crash", which is the difference between ending a
	// broadcast and leaving it alone.
	lc := e.lifecycleObserver()
	// Read ONCE for the whole batch rather than per event: a
	// SetHooks landing mid-loop would otherwise publish some of one
	// Observe's events to the old dispatcher and some to the new,
	// which is a split nobody could explain from the delivery log.
	dispatcher := e.Hooks()
	edges := e.hookWatch.Observe(snap)
	for _, ev := range edges {
		dispatcher.Publish(ev)
		if lc != nil {
			// Contractually non-blocking; see LifecycleObserver.
			lc.Observe(ev)
		}
	}
	// The stream converts the SAME edges, plus held and restarted, which it
	// derives from the same snapshot. Run whether or not anybody is
	// connected, so a connection opened mid-hold is told about the next
	// hold, not about one it already missed; Hub.Publish with nobody
	// listening only fills the replay ring.
	if e.streamWatch != nil {
		hub := e.Stream()
		for _, env := range e.streamWatch.Observe(src, snap, edges) {
			hub.Publish(env)
		}
	}
}

// publishLifecycleEvent sends one webhook edge raised outside the sweep -- the
// rollover -- to both surfaces that carry it. Nil-safe on both.
func (e *Engine) publishLifecycleEvent(ev hooks.Event) {
	e.Hooks().Publish(ev)
	e.Stream().Publish(eventstream.FromHook(ev))
}

// streamLevelLocked announces a level change's outcome on one destination.
// Caller holds e.mu (for writing), which is why it reads e.stream and e.dests
// directly rather than through their accessors.
//
// Hub.Publish takes only the hub's own mutex and never calls back into the
// engine, so publishing under e.mu cannot invert any lock order here.
func (e *Engine) streamLevelLocked(id int64, n liveNote) {
	if e.stream == nil {
		return
	}
	env := eventstream.Envelope{
		Type:   eventstream.TypeDestinationLevel,
		At:     time.Now(),
		Source: &hooks.SourceRef{ID: e.sourceID, Name: e.sourceName},
		Reason: n.reason,
	}
	data := map[string]string{"applied": n.applied}
	env.Data = data
	if d := e.dests[id]; d != nil && d.row != nil {
		env.Destination = &hooks.DestinationRef{ID: id, Name: d.row.Name, Platform: string(d.row.Platform)}
		if d.row.DelaySeconds > 0 {
			// The change reached the FFmpeg; the audience hears it this much
			// later, behind the broadcast delay (#11).
			data["delaySeconds"] = strconv.Itoa(d.row.DelaySeconds)
		}
	} else {
		env.Destination = &hooks.DestinationRef{ID: id}
	}
	e.stream.Publish(env)
}

// streamRecording announces the recorder starting or stopping. Called with no
// engine lock held.
func (e *Engine) streamRecording(started bool, reason string) {
	hub := e.Stream()
	if hub == nil {
		return
	}
	t := eventstream.TypeRecordingStopped
	if started {
		t = eventstream.TypeRecordingStarted
	}
	hub.Publish(eventstream.Envelope{
		Type:   t,
		Source: &hooks.SourceRef{ID: e.sourceID, Name: e.SourceName()},
		Reason: reason,
	})
}

// streamFailover announces a source switch. Called with no engine lock held,
// beside the bus event that already announces it to the console.
//
// The payload is the switch, not the whole FailoverStatus: the process
// statuses inside that carry a child's argv-derived fields, and none of them is
// news to a consumer of "the programme moved to its backup".
func (e *Engine) streamFailover(st *FailoverStatus) {
	hub := e.Stream()
	if hub == nil || st == nil {
		return
	}
	hub.Publish(eventstream.Envelope{
		Type:   eventstream.TypeFailoverSwitched,
		Source: &hooks.SourceRef{ID: e.sourceID, Name: e.SourceName()},
		Reason: st.Reason,
		Error:  st.Error,
		Data: map[string]any{
			"active":        st.Active,
			"pinned":        st.Pinned,
			"switches":      st.Switches,
			"primaryLive":   st.PrimaryLive,
			"backupLive":    st.BackupLive,
			"backupEnabled": st.BackupEnabled,
			"slateEnabled":  st.SlateEnabled,
		},
	})
}
