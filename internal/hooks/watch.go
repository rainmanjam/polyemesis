package hooks

import (
	"sort"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

// Watcher defaults.
const (
	// DefaultDisconnectAfter is how long the ingest must be silent before a
	// disconnection is announced. Much shorter than alerts.DefaultDownFor (20s)
	// because the questions differ: an alert waits to be sure this is an
	// incident and not a reconnect, while a hook is reporting a fact somebody
	// asked to be told about. Three sweeps at the engine's 2s cadence.
	DefaultDisconnectAfter = 5 * time.Second
	// DefaultDestinationDownAfter absorbs a supervisor reconnect. It only
	// applies to the DOWN edge; the UP edge is immediate, which is what stops a
	// flapping destination producing a storm -- it never goes down, so it never
	// comes up again either.
	DefaultDestinationDownAfter = 10 * time.Second
)

// WatchConfig tunes the dwell times. A zero value takes every default.
type WatchConfig struct {
	DisconnectAfter      time.Duration
	DestinationDownAfter time.Duration
}

func (c WatchConfig) normalized() WatchConfig {
	if c.DisconnectAfter < 0 {
		c.DisconnectAfter = 0
	} else if c.DisconnectAfter == 0 {
		c.DisconnectAfter = DefaultDisconnectAfter
	}
	// THE DEFAULT WAS DECLARED AND NEVER APPLIED. This clamped negatives and
	// stopped, so a zero -- which the struct above documents as "takes every
	// default", and which is what engine.go:623 constructs -- meant NO DWELL AT
	// ALL. DefaultDestinationDownAfter was referenced nowhere but its own
	// declaration and a comment reasoning about a delay that never happened.
	//
	// The effect is the storm the constant exists to prevent: with a zero dwell
	// the DOWN edge fires the instant a destination stops running, so every
	// supervisor reconnect published a destination-down hook and an
	// up-again hook behind it.
	//
	// It also silently falsified the LifecycleObserver design at
	// engine.go:3853, which reasons that "a torn-down-and-restarted destination
	// crosses no edge at all, because the DOWN direction has a 10s dwell
	// (hooks.DefaultDestinationDownAfter) and one reconcile completes well
	// inside it." Every spec-change restart crossed one.
	if c.DestinationDownAfter < 0 {
		c.DestinationDownAfter = 0
	} else if c.DestinationDownAfter == 0 {
		c.DestinationDownAfter = DefaultDestinationDownAfter
	}
	return c
}

// edgeState is the on/off detector both the ingest and every destination use.
//
// It is NOT alerts.downState with different numbers, and the difference is the
// starting position. alerts starts things UP and waits to be proven down,
// because an incident is a departure from working. This starts everything OFF
// and waits to be proven on, because "the stream started" is the event a script
// is written against and there is no such thing as a stream that was always
// running.
//
// That asymmetry is the whole reason this package exists: alerts.watchIngest
// only emits ingest.recovered after emitting ingest.lost, so an install whose
// streamer connects inside DownFor of boot produces no publish edge at all.
type edgeState struct {
	on       bool
	offSince time.Time
}

// observe folds one observation and reports which edge was crossed. The dwell
// applies to the OFF direction only.
func (s *edgeState) observe(on bool, now time.Time, offAfter time.Duration) (turnedOn, turnedOff bool) {
	if on {
		s.offSince = time.Time{}
		if !s.on {
			s.on = true
			return true, false
		}
		return false, false
	}
	if !s.on {
		return false, false
	}
	if s.offSince.IsZero() {
		s.offSince = now
	}
	if now.Before(s.offSince.Add(offAfter)) {
		return false, false
	}
	s.on, s.offSince = false, time.Time{}
	return false, true
}

// Watcher turns a stream of snapshots into lifecycle events for one source.
//
// One per engine, because the state it holds is per programme. Given the same
// sequence of snapshots it emits the same sequence of events, which is what
// makes every dwell above a table test rather than a stopwatch.
type Watcher struct {
	src  SourceRef
	cfg  WatchConfig
	ing  edgeState
	dest map[int64]*edgeState
	// lastOut is each destination's output time at the previous observation,
	// in milliseconds. It is how "delivering" is told apart from "has a
	// process"; see delivering.
	lastOut map[int64]int64
	// names remembers what a destination was called, so a row that disappears
	// between two snapshots can still be identified in the event that says so.
	names map[int64]DestinationRef
}

// NewWatcher creates a watcher for one source.
func NewWatcher(src SourceRef, cfg WatchConfig) *Watcher {
	return &Watcher{
		src:     src,
		cfg:     cfg.normalized(),
		dest:    map[int64]*edgeState{},
		lastOut: map[int64]int64{},
		names:   map[int64]DestinationRef{},
	}
}

// SetSource refreshes the programme label. The engine re-reads the source row on
// every reconcile, and a hook payload naming a programme by its old name after a
// rename is the kind of small lie that costs an operator an afternoon.
func (w *Watcher) SetSource(src SourceRef) { w.src = src }

// Observe judges one snapshot and returns every transition in it.
func (w *Watcher) Observe(s alerts.Snapshot) []Event {
	now := s.At
	if now.IsZero() {
		now = time.Now()
	}
	out := w.watchIngest(s, now)
	return append(out, w.watchDestinations(s, now)...)
}

func (w *Watcher) watchIngest(s alerts.Snapshot, now time.Time) []Event {
	if !s.IngestConfigured {
		// Nothing to lose, and nothing to continue. Forgetting the session
		// means re-adding an ingest publishes afresh rather than resuming one
		// that ended while it was not configured.
		w.ing = edgeState{}
		return nil
	}
	published, disconnected := w.ing.observe(s.IngestLive, now, w.cfg.DisconnectAfter)
	switch {
	case published:
		return []Event{{
			Trigger: TriggerIngestPublished, At: now, Source: w.src,
			Reason: "data is arriving on the ingest",
		}}
	case disconnected:
		return []Event{{
			Trigger: TriggerIngestDisconnected, At: now, Source: w.src,
			Reason: "no data for " + w.cfg.DisconnectAfter.String(),
			Error:  s.IngestError,
		}}
	}
	return nil
}

func (w *Watcher) watchDestinations(s alerts.Snapshot, now time.Time) []Event {
	// Sorted so map iteration order can never reach the wire. A receiver
	// correlating deliveries by sequence would otherwise see a different order
	// on every run.
	rows := append([]alerts.DestState(nil), s.Destinations...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })

	live := make(map[int64]bool, len(rows))
	var out []Event
	for _, d := range rows {
		live[d.ID] = true
		ref := DestinationRef{ID: d.ID, Name: d.Name, Platform: d.Platform}
		w.names[d.ID] = ref

		st := w.dest[d.ID]
		if st == nil {
			st = &edgeState{}
			w.dest[d.ID] = st
		}
		// A disabled destination is OFF rather than exempt. That is the
		// deliberate divergence from alerts, which treats it as "not down"
		// because nobody should be woken for a switch somebody flipped. A hook
		// is a fact: a script mirroring what is live needs the edge whoever
		// caused it.
		//
		// AND "ON" MEANS DELIVERING, NOT SPAWNED. This used to be Enabled &&
		// Running, and Running is true from the moment the child exists. A
		// destination pointed at a closed port was therefore announced as
		// "delivering" on every spawn -- one exploratory run saw the up hook
		// 14s before a byte left for anywhere -- and a sink that stopped
		// reading was never announced at all, because the blocked child stays
		// Running for as long as its TCP connection does.
		flowing := w.delivering(d)
		stalled := d.Enabled && d.Running && !flowing
		if stalled && s.IngestConfigured && !s.IngestLive {
			// NOTHING ARRIVING IS NOT A DESTINATION STALLING. With the source
			// gone every destination's output time stops, and that is
			// ingest.disconnected's news; a down per destination on top of it
			// would tell a script that every platform had failed when none had.
			// Hold whatever edge the destination is on, and restart the dwell
			// so the gap is not counted against it once the source returns.
			st.offSince = time.Time{}
			continue
		}
		up, down := st.observe(d.Enabled && d.Running && flowing, now, w.cfg.DestinationDownAfter)
		switch {
		case up:
			out = append(out, Event{
				Trigger: TriggerDestinationUp, At: now, Source: w.src,
				Destination: &ref, Reason: "delivering",
			})
		case down:
			reason := "stopped"
			switch {
			case !d.Enabled:
				reason = "disabled"
			case stalled:
				// The process is up and its output is not moving: the sink
				// stopped taking data. Not "stopped", which a script (and
				// the lifecycle coordinator) reads as a crash.
				reason = "stalled"
			}
			out = append(out, Event{
				Trigger: TriggerDestinationDown, At: now, Source: w.src,
				Destination: &ref, Reason: reason, Error: d.Error,
			})
		}
	}

	// Rows that disappeared. A destination that was live and has been deleted
	// must not simply vanish: the last thing a script heard was that it came
	// up, and nothing would ever correct that.
	var gone []int64
	for id := range w.dest {
		if !live[id] {
			gone = append(gone, id)
		}
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i] < gone[j] })
	for _, id := range gone {
		if w.dest[id].on {
			ref := w.names[id]
			out = append(out, Event{
				Trigger: TriggerDestinationDown, At: now, Source: w.src,
				Destination: &ref, Reason: "removed",
			})
		}
		delete(w.dest, id)
		delete(w.lastOut, id)
		delete(w.names, id)
	}
	return out
}

// delivering reports whether d's output has moved since the previous
// observation, and records where it is now.
//
// FFmpeg reports progress every half second and the sweep runs every two, so a
// destination moving media always shows a different output time from one sweep
// to the next, and one whose write is blocked shows the same one. Zero is a
// child that has not moved anything yet. A value BELOW the last one is a
// respawned child that has already moved media, which is delivering too. The
// very first observation of a destination already carrying media counts as
// delivering, so a server restarted mid-show replays its live destinations
// straight away, as documented.
func (w *Watcher) delivering(d alerts.DestState) bool {
	last := w.lastOut[d.ID]
	if !d.Running {
		w.lastOut[d.ID] = 0
		return false
	}
	w.lastOut[d.ID] = d.OutTimeMS
	return d.OutTimeMS > 0 && d.OutTimeMS != last
}
