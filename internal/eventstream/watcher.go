package eventstream

import (
	"sort"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/hooks"
)

// Watcher turns one programme's sweep into stream events: the webhook edges the
// engine already derived, plus the one destination fact no webhook carries --
// restarted.
//
// IT DERIVES NOTHING THE WEBHOOK WATCHER DERIVES. UP, DOWN and HELD arrive as
// the hooks.Event values hooks.Watcher.Observe returned for the same snapshot,
// and are converted, not recomputed. HELD used to be derived here, when no
// webhook carried it; it became the destination.held trigger, its rule moved to
// hooks.Watcher with its tests, and deriving it here as well would announce
// every hold twice: a stream that announced a destination down at
// a different moment from the webhook would be a second opinion, and the
// disagreement would be about exactly the event somebody is scripting against.
//
// Like hooks.Watcher, it is deterministic: the same snapshots and edges give
// the same events, so every rule below is a table test rather than a stopwatch.
type Watcher struct {
	// restarts is the respawn count at the previous sweep.
	restarts map[int64]int
}

// NewWatcher builds a watcher for one programme.
func NewWatcher() *Watcher {
	return &Watcher{restarts: map[int64]int{}}
}

// Observe converts one sweep of programme src. edges are what
// hooks.Watcher.Observe returned for snap, in its order.
func (w *Watcher) Observe(src hooks.SourceRef, snap alerts.Snapshot, edges []hooks.Event) []Envelope {
	rows := make(map[int64]alerts.DestState, len(snap.Destinations))
	for _, d := range snap.Destinations {
		rows[d.ID] = d
	}

	out := make([]Envelope, 0, len(edges))
	for _, ev := range edges {
		env := FromHook(ev)
		if ev.Destination != nil {
			id := ev.Destination.ID
			switch ev.Trigger {
			case hooks.TriggerDestinationDown, hooks.TriggerDestinationHeld:
				// The whole classified reason, beside the flat fields. Only
				// for a failure, under the rule hooks.Watcher applies to the
				// flat ones: a disabled destination's leftover cause would tell
				// a script that the disable was a key rejection.
				if d, ok := rows[id]; ok && d.Enabled && d.Reason != nil {
					r := *d.Reason
					env.Failure = &r
				}
			}
		}
		out = append(out, env)
	}

	// Sorted for the reason hooks.Watcher sorts: map order must never reach
	// the wire.
	ids := make([]int64, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		d := rows[id]
		ref := &hooks.DestinationRef{ID: d.ID, Name: d.Name, Platform: d.Platform}

		// RESTARTED: the respawn count went up on the same process. A count
		// that went DOWN is a new process -- a spec change, or Retry -- which
		// is not a respawn and is not announced as one. The first sight of a
		// destination is a baseline, so a server restarted mid-show does not
		// announce every destination's lifetime respawns as news.
		prev, seen := w.restarts[id]
		w.restarts[id] = d.Restarts
		if seen && d.Restarts > prev {
			env := Envelope{
				Type: TypeDestinationRestarted, At: snap.At, Destination: ref,
				Reason: "the process exited and was started again",
				Error:  d.Error,
				Data:   map[string]int{"restarts": d.Restarts, "since": d.Restarts - prev},
			}
			env.Source = &hooks.SourceRef{ID: src.ID, Name: src.Name}
			out = append(out, env)
		}
	}

	// Forget rows that are gone, so an id reused later starts afresh.
	for id := range w.restarts {
		if _, ok := rows[id]; !ok {
			delete(w.restarts, id)
		}
	}
	return out
}
