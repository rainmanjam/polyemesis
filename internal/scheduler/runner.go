package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DefaultTick is how often schedules are evaluated. Schedules have minute
// granularity and the grace period is measured in minutes, so a sweep this
// coarse is both cheap and precise enough; the alternative, a timer per
// schedule, would have to be rebuilt on every edit.
const DefaultTick = 20 * time.Second

// Store is where schedules live. An interface rather than *db.DB so the whole
// runner is testable without a database.
type Store interface {
	// Schedules returns the schedules to evaluate. Disabled ones may be
	// included; Evaluate ignores them.
	Schedules() ([]Schedule, error)
	// MarkScheduleRun records an occurrence as handled. It is called for a
	// skipped occurrence too, which is what stops a missed window from firing
	// later.
	MarkScheduleRun(id int64, at time.Time) error
}

// Actuator is the enable/disable path. The runner deliberately cannot start a
// process: it writes the same intent a human would and asks for a reconcile, so
// there is exactly one code path that brings a destination up.
type Actuator interface {
	SetDestinationEnabled(id int64, enabled bool) error
	// ListDestinationIDs expands a schedule that targets everything.
	ListDestinationIDs() ([]int64, error)
	// SetPlaylistEnabled flips the failover playlist's stored intent.
	//
	// INSTALL-WIDE: db.Settings is global, so this is not per-programme. There
	// is no id parameter because there is nothing to select.
	SetPlaylistEnabled(enabled bool) error
	// Reconcile is called once per sweep, and only when something changed.
	Reconcile() error
}

// Result is what happened to one schedule in one sweep.
type Result struct {
	ID      int64     `json:"id"`
	Name    string    `json:"name"`
	Action  Action    `json:"action"`
	Fired   bool      `json:"fired"`
	Skipped bool      `json:"skipped"`
	At      time.Time `json:"at"`
	Reason  string    `json:"reason"`
	Targets []int64   `json:"targets,omitempty"`
	Err     string    `json:"error,omitempty"`
}

// Option configures a Runner.
type Option func(*Runner)

// WithClock replaces time.Now, for tests.
func WithClock(fn func() time.Time) Option { return func(r *Runner) { r.now = fn } }

// WithTick sets the sweep interval.
func WithTick(d time.Duration) Option {
	return func(r *Runner) {
		if d > 0 {
			r.tick = d
		}
	}
}

// WithOnResult registers a callback fired for every schedule that acted. The
// engine uses it to raise an event; it must not block.
func WithOnResult(fn func(Result)) Option { return func(r *Runner) { r.onResult = fn } }

// Runner evaluates schedules on a ticker.
type Runner struct {
	log      *slog.Logger
	store    Store
	act      Actuator
	now      func() time.Time
	tick     time.Duration
	onResult func(Result)

	mu   sync.Mutex
	last []Result
}

// New creates a Runner. It does nothing until Run is called.
func New(log *slog.Logger, store Store, act Actuator, opts ...Option) *Runner {
	r := &Runner{log: log, store: store, act: act, now: time.Now, tick: DefaultTick}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Run sweeps until ctx ends.
func (r *Runner) Run(ctx context.Context) {
	if r == nil {
		return
	}
	t := time.NewTicker(r.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Tick(r.now())
		}
	}
}

// Tick evaluates every schedule against now and acts on the ones that are due.
//
// It is exported and takes the time so the whole behaviour — firing, skipping a
// missed window, expanding "all destinations" — is a table test with no ticker
// and no sleep in it.
func (r *Runner) Tick(now time.Time) []Result {
	rows, err := r.store.Schedules()
	if err != nil {
		r.log.Warn("cannot read schedules", "err", err)
		return nil
	}

	var (
		out     []Result
		changed bool
		all     []int64
		allErr  error
		allDone bool
	)
	for _, s := range rows {
		s = s.Normalized()
		d := Evaluate(s, now)
		if !d.Fire && !d.Skip {
			continue
		}
		res := Result{ID: s.ID, Name: s.Name, Action: s.Action, At: d.At, Reason: d.Reason}

		if d.Skip {
			// Recorded as handled without acting: this is the missed window,
			// and the only thing worse than not starting is starting late.
			res.Skipped = true
			if err := r.store.MarkScheduleRun(s.ID, d.At); err != nil {
				res.Err = err.Error()
			}
			r.log.Warn("schedule window missed; not acting on it",
				"schedule", s.Name, "occurrence", d.At.Format(time.RFC3339),
				"lateBy", now.UTC().Sub(d.At).Round(time.Second).String())
			out = append(out, res)
			r.emit(res)
			continue
		}

		// The playlist half. It must branch HERE, above the destination
		// expansion: Enables() answers Action == ActionStart, so playlist.stop
		// reaching the code below would disable every destination in the
		// install.
		if s.TargetsPlaylist() {
			enable := s.Action == ActionPlaylistStart
			if err := r.act.SetPlaylistEnabled(enable); err != nil {
				// NOT marked handled: unlike a destination sweep there is no
				// partial success to avoid re-applying, so the next sweep
				// should try again while the occurrence is still inside its
				// grace window.
				res.Err = err.Error()
				r.log.Warn("schedule cannot set the playlist",
					"schedule", s.Name, "err", err)
				out = append(out, res)
				r.emit(res)
				continue
			}
			// Without this the setting is written and nothing applies it: Tick
			// only reconciles when something changed.
			changed = true
			res.Fired = true
			if err := r.store.MarkScheduleRun(s.ID, d.At); err != nil {
				res.Err = err.Error()
			}
			// "target" rather than a playlist-specific key so one log query
			// finds every schedule that fired, whatever it acted on. The
			// destination branch below says `"target", "destinations"` for
			// the same reason and carries the count separately.
			r.log.Info("schedule fired",
				"schedule", s.Name, "action", string(s.Action),
				"target", "playlist", "occurrence", d.At.Format(time.RFC3339))
			out = append(out, res)
			r.emit(res)
			continue
		}

		targets := s.DestinationIDs
		if len(targets) == 0 {
			if !allDone {
				all, allErr = r.act.ListDestinationIDs()
				allDone = true
			}
			if allErr != nil {
				// Not marked as handled: the occurrence is still inside its
				// grace window, so the next sweep gets to try again.
				res.Err = allErr.Error()
				r.log.Warn("schedule cannot list destinations", "schedule", s.Name, "err", allErr)
				out = append(out, res)
				r.emit(res)
				continue
			}
			targets = all
		}

		enabled := s.Enables()
		var failed error
		for _, id := range targets {
			if err := r.act.SetDestinationEnabled(id, enabled); err != nil {
				failed = err
				r.log.Warn("schedule cannot set destination",
					"schedule", s.Name, "destination", id, "err", err)
				continue
			}
			changed = true
		}

		res.Fired = true
		res.Targets = targets
		if failed != nil {
			res.Err = failed.Error()
		}
		// Marked handled even when a destination flip failed: retrying the
		// whole occurrence on the next sweep would re-apply it to the ones that
		// worked, and the failure is already logged and surfaced.
		if err := r.store.MarkScheduleRun(s.ID, d.At); err != nil && res.Err == "" {
			res.Err = err.Error()
		}
		r.log.Info("schedule fired",
			"schedule", s.Name, "action", string(s.Action),
			"target", "destinations", "count", len(targets),
			"occurrence", d.At.Format(time.RFC3339))
		out = append(out, res)
		r.emit(res)
	}

	if changed {
		if err := r.act.Reconcile(); err != nil {
			r.log.Error("schedule reconcile failed", "err", err)
		}
	}
	if len(out) > 0 {
		r.mu.Lock()
		r.last = out
		r.mu.Unlock()
	}
	return out
}

// Last reports what the most recent sweep that did anything did, for a status
// endpoint.
func (r *Runner) Last() []Result {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Result(nil), r.last...)
}

func (r *Runner) emit(res Result) {
	if r.onResult != nil {
		r.onResult(res)
	}
}
