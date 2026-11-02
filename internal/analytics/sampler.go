package analytics

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Defaults and bounds for the operator's two knobs. The bounds are enforced by
// db.Settings.Validate; these are the numbers it is checked against.
const (
	DefaultInterval      = 60 * time.Second
	MinIntervalSeconds   = 30
	MaxIntervalSeconds   = 900
	DefaultRetentionDays = 90
	MinRetentionDays     = 1
	MaxRetentionDays     = 3650

	// CloseGrace is how long the stream may be off air before its record is
	// closed. A destination restart, a key refresh or a failover hand-off is a
	// few seconds with nothing running, and splitting one broadcast into two
	// reports for it would be wrong. The grace is at least two intervals, so a
	// slow sample rate cannot close a stream between two of its own ticks.
	CloseGrace = 3 * time.Minute

	// sparkPoints caps the live sparkline: two hours at the default interval.
	sparkPoints = 120
	// readTimeout bounds one platform read inside a tick.
	readTimeout = 15 * time.Second
	// pruneEvery is how often retention runs. Hourly is plenty for a bound
	// counted in days.
	pruneEvery = time.Hour
)

// Config is what the sampler reads from the settings on every tick, so a
// change takes effect on the next one.
type Config struct {
	Enabled   bool
	Interval  time.Duration
	Retention time.Duration
}

// Target is one connected account to ask.
type Target struct {
	Platform  string
	AccountID int64
	Account   string
	Name      string
}

// Stats is one platform read, reduced to what the sampler keeps.
type Stats struct {
	Live    bool
	Viewers *int
	Title   string
}

// Source is the world as the sampler sees it.
type Source interface {
	// OnAir reports whether any destination is running.
	OnAir() bool
	// Targets is the connected accounts the running destinations stream to.
	Targets(ctx context.Context) ([]Target, error)
	// Allow asks the platform's quota pacer for one read and spends it when it
	// answers true. False means the read must not be made.
	Allow(t Target) bool
	// Read asks the platform.
	Read(ctx context.Context, t Target) (Stats, error)
	// Player is the public player's viewer count and QoE counters, ok false
	// when the player is not running.
	Player() (viewers int, qoe PlayerQoE, ok bool)
}

// OpenState is the stream a previous process left open.
type OpenState struct {
	ID     int64
	Start  time.Time
	LastAt time.Time
}

// Store is where samples go.
type Store interface {
	// OpenAnalyticsStream starts a stream record. qoe is the player's
	// counters at the start, nil when the player is not running.
	OpenAnalyticsStream(at time.Time, qoe *PlayerQoE) (int64, error)
	// OpenAnalyticsStreamState is the stream left open, if any.
	OpenAnalyticsStreamState() (OpenState, bool, error)
	// RecordAnalyticsTick stores one tick's readings, the first broadcast
	// title seen, the player's counters, and tallies the chat that arrived
	// since the previous tick.
	RecordAnalyticsTick(id int64, at time.Time, rs []Reading, title string, qoe *PlayerQoE) error
	// CloseAnalyticsStream ends the stream at `at`, with a last chat tally.
	CloseAnalyticsStream(id int64, at time.Time) error
	// AnalyticsSamples is a stream's samples in time order.
	AnalyticsSamples(id int64) ([]Sample, error)
	// PruneAnalytics deletes streams that ended before cutoff.
	PruneAnalytics(cutoff time.Time) (int, error)
	// ForgetAnalyticsQoE drops a stream's player QoE baseline, so its report
	// shows no player QoE rather than a delta across a process restart.
	ForgetAnalyticsQoE(id int64) error
}

// Live is the panel's current picture.
type Live struct {
	Enabled bool `json:"enabled"`
	// OnAir is the sampler's last verdict on whether anything is streaming.
	OnAir  bool    `json:"onAir"`
	Stream *Stream `json:"stream,omitempty"`
	// At is the last tick, nil before the first.
	At *time.Time `json:"at,omitempty"`
	// Total is the sum of the readings that reported, nil when none did.
	Total    *int      `json:"total,omitempty"`
	Reported int       `json:"reported"`
	Readings []Reading `json:"readings"`
	Points   []Point   `json:"points"`
	// Player is the public player's QoE since the stream began, nil when it
	// is not running or its counters were reset mid-stream.
	Player          *PlayerQoE `json:"player,omitempty"`
	IntervalSeconds int        `json:"intervalSeconds"`
}

// ByPlatform sums the platform readings that reported, per platform, leaving
// out the player (the playout scrape already publishes it). A platform with
// readings none of which reported maps to nil.
func (l Live) ByPlatform() map[string]*int {
	out := map[string]*int{}
	for _, r := range l.Readings {
		if r.Platform == PlatformPlayer {
			continue
		}
		cur, seen := out[r.Platform]
		if r.Viewers == nil {
			if !seen {
				out[r.Platform] = nil
			}
			continue
		}
		v := *r.Viewers
		if cur != nil {
			v += *cur
		}
		out[r.Platform] = &v
	}
	return out
}

// Sampler polls the connected accounts while the stream is on air.
type Sampler struct {
	src   Source
	store Store
	cfg   func() Config
	now   func() time.Time
	log   *slog.Logger

	// tickMu makes ticks strictly sequential, whoever calls them.
	tickMu sync.Mutex
	// State below is owned by Tick under tickMu.
	loaded     bool
	open       int64
	openStart  time.Time
	lastOnAir  time.Time
	qoeStart   *PlayerQoE
	lastPruned time.Time

	mu   sync.Mutex
	live Live
}

// New builds a sampler. now may be nil for time.Now.
func New(src Source, store Store, cfg func() Config, now func() time.Time, log *slog.Logger) *Sampler {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Sampler{src: src, store: store, cfg: cfg, now: now, log: log}
	// Seeded from the settings so the picture before the first tick says
	// whether sampling is on, rather than reading as switched off for the
	// few seconds until the loop's first tick.
	c := s.config()
	s.live = Live{Enabled: c.Enabled, IntervalSeconds: int(c.Interval / time.Second),
		Readings: []Reading{}, Points: []Point{}}
	return s
}

// Live returns a copy of the current picture.
func (s *Sampler) Live() Live {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.live
	l.Readings = append([]Reading(nil), s.live.Readings...)
	l.Points = append([]Point(nil), s.live.Points...)
	if l.Readings == nil {
		l.Readings = []Reading{}
	}
	if l.Points == nil {
		l.Points = []Point{}
	}
	return l
}

// Run ticks until ctx ends. The interval is re-read after every tick, so a
// changed setting applies from the next one.
func (s *Sampler) Run(ctx context.Context) {
	wait := 5 * time.Second
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		s.Tick(ctx)
		wait = s.config().Interval
	}
}

func (s *Sampler) config() Config {
	c := s.cfg()
	if c.Interval <= 0 {
		c.Interval = DefaultInterval
	}
	if c.Retention <= 0 {
		c.Retention = DefaultRetentionDays * 24 * time.Hour
	}
	return c
}

func (s *Sampler) grace(c Config) time.Duration {
	return max(CloseGrace, 2*c.Interval)
}

// Tick takes one sample. Exported so tests drive the clock.
func (s *Sampler) Tick(ctx context.Context) {
	s.tickMu.Lock()
	defer s.tickMu.Unlock()

	now := s.now()
	c := s.config()
	if !s.loaded {
		s.resume(now, c)
	}
	s.prune(now, c)

	onAir := c.Enabled && s.src.OnAir()
	if !onAir {
		if s.open != 0 && (!c.Enabled || now.Sub(s.lastOnAir) >= s.grace(c)) {
			s.closeOpen()
		}
		s.publish(func(l *Live) {
			l.Enabled, l.OnAir, l.IntervalSeconds = c.Enabled, false, int(c.Interval/time.Second)
			l.At = &now
			l.Total, l.Reported, l.Readings, l.Player = nil, 0, []Reading{}, nil
			if s.open == 0 {
				l.Stream, l.Points = nil, []Point{}
			}
		})
		return
	}

	pv, pq, playerOK := s.src.Player()
	var qoe *PlayerQoE
	if playerOK {
		qoe = &pq
	}
	if s.open == 0 {
		id, err := s.store.OpenAnalyticsStream(now, qoe)
		if err != nil {
			s.log.Warn("analytics: could not open a stream record", "err", err)
			return
		}
		s.open, s.openStart, s.qoeStart = id, now, qoe
		s.publish(func(l *Live) { l.Points = []Point{} })
	}

	readings, title := s.read(ctx)
	if playerOK {
		v := pv
		readings = append(readings, Reading{Platform: PlatformPlayer, Live: true, Viewers: &v})
	}
	if err := s.store.RecordAnalyticsTick(s.open, now, readings, title, qoe); err != nil {
		s.log.Warn("analytics: could not store a sample", "err", err)
	}
	s.lastOnAir = now

	total, reported := Total(readings)
	var player *PlayerQoE
	if playerOK && s.qoeStart != nil {
		if d, ok := pq.Delta(*s.qoeStart); ok {
			player = &d
		}
	}
	start := s.openStart
	id := s.open
	s.publish(func(l *Live) {
		l.Enabled, l.OnAir, l.IntervalSeconds = true, true, int(c.Interval/time.Second)
		l.At = &now
		l.Stream = &Stream{ID: id, StartedAt: start, DurationMs: now.Sub(start).Milliseconds()}
		l.Total, l.Reported, l.Readings, l.Player = total, reported, readings, player
		l.Points = append(l.Points, Point{At: now, Total: total})
		if len(l.Points) > sparkPoints {
			l.Points = append([]Point(nil), l.Points[len(l.Points)-sparkPoints:]...)
		}
	})
}

// read asks every target, in a stable order, and returns the readings and
// the first broadcast title any of them reported.
func (s *Sampler) read(ctx context.Context) ([]Reading, string) {
	targets, err := s.src.Targets(ctx)
	if err != nil {
		s.log.Warn("analytics: could not list accounts", "err", err)
		return []Reading{}, ""
	}
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].Platform != targets[j].Platform {
			return targets[i].Platform < targets[j].Platform
		}
		return targets[i].AccountID < targets[j].AccountID
	})
	out := make([]Reading, 0, len(targets)+1)
	title := ""
	for _, t := range targets {
		r := Reading{Platform: t.Platform, Account: t.Account, Name: t.Name}
		if !s.src.Allow(t) {
			r.Reason = ReasonQuota
			out = append(out, r)
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, readTimeout)
		st, err := s.src.Read(rctx, t)
		cancel()
		switch {
		case err != nil:
			r.Reason = ReasonError
			if !errors.Is(err, context.Canceled) {
				s.log.Info("analytics: platform read failed", "platform", t.Platform, "err", err)
			}
		case !st.Live:
			// Not live is not a count of zero viewers on a live stream. A
			// platform that is not carrying this broadcast has nobody to
			// report, so it is left out of the total like any other silence.
			r.Reason = ReasonOffline
		case st.Viewers == nil:
			r.Live, r.Reason = true, ReasonHidden
		default:
			v := *st.Viewers
			r.Live, r.Viewers = true, &v
		}
		if st.Live && title == "" {
			title = st.Title
		}
		out = append(out, r)
	}
	return out, title
}

// resume picks up a stream a previous process left open: continued when it
// was on air within the grace, closed at its last tick otherwise.
func (s *Sampler) resume(now time.Time, c Config) {
	s.loaded = true
	st, ok, err := s.store.OpenAnalyticsStreamState()
	if err != nil {
		s.log.Warn("analytics: could not read the open stream", "err", err)
		return
	}
	if !ok {
		return
	}
	last := st.LastAt
	if last.IsZero() {
		last = st.Start
	}
	if now.Sub(last) >= s.grace(c) || !c.Enabled {
		if err := s.store.CloseAnalyticsStream(st.ID, last); err != nil {
			s.log.Warn("analytics: could not close a stale stream", "err", err)
		}
		return
	}
	s.open, s.openStart, s.lastOnAir = st.ID, st.Start, last
	// The QoE baseline did not survive the restart, and the counters it would
	// be subtracted from were reset with the process; the stream's player QoE
	// is reported from here on only if a baseline exists.
	s.qoeStart = nil
	if err := s.store.ForgetAnalyticsQoE(st.ID); err != nil {
		s.log.Warn("analytics: could not drop the player baseline", "err", err)
	}
	if samples, err := s.store.AnalyticsSamples(st.ID); err == nil {
		pts := Summarise(samples).Points
		if len(pts) > sparkPoints {
			pts = pts[len(pts)-sparkPoints:]
		}
		s.publish(func(l *Live) { l.Points = pts })
	}
}

func (s *Sampler) closeOpen() {
	if err := s.store.CloseAnalyticsStream(s.open, s.lastOnAir); err != nil {
		s.log.Warn("analytics: could not close the stream record", "err", err)
	}
	s.open, s.qoeStart = 0, nil
}

func (s *Sampler) prune(now time.Time, c Config) {
	if !s.lastPruned.IsZero() && now.Sub(s.lastPruned) < pruneEvery {
		return
	}
	s.lastPruned = now
	if _, err := s.store.PruneAnalytics(now.Add(-c.Retention)); err != nil {
		s.log.Warn("analytics: retention prune failed", "err", err)
	}
}

func (s *Sampler) publish(f func(*Live)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.live)
}
