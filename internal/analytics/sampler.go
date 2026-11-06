package analytics

import (
	"context"
	"errors"
	"github.com/rainmanjam/polyemesis/internal/backoff"
	"log/slog"
	"slices"
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

	// RolloverAfter splits a broadcast that never stops into day-long
	// streams. Retention prunes ended streams, so a 24/7 stream that was one
	// open row would never be pruned and its samples would grow without bound.
	RolloverAfter = 24 * time.Hour

	// maxBackoff caps how long an account that keeps failing is left alone.
	maxBackoff = 10 * time.Minute
	// firstWait is the delay before the loop's first tick after boot.
	defaultFirstWait = 5 * time.Second
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
	// Unlinked is a running destination with a pasted stream key and no
	// connected account: shown as a row, never read.
	Unlinked bool
	// ChatOnly is a connected account whose platform has no viewer count to
	// read (Facebook, Rumble, X): its chat is the stream's, but it is never
	// asked for viewers and has no row (round 2, R2).
	ChatOnly bool
}

// ChatAccount names one chat the stream's tally counts.
type ChatAccount struct {
	Platform string
	Account  string
}

// Tick is one tick as the store records it.
type Tick struct {
	At       time.Time
	Readings []Reading
	// ChatAccounts are the chats the stream is on this tick. TallyChat is
	// false when the target list could not be read: the chat that arrived is
	// left for the next tick rather than counted for nobody (R2).
	ChatAccounts []ChatAccount
	TallyChat    bool
	Title        string
	QoE          *PlayerQoE
	// Wait is the interval the sampler chose after this tick, kept so a
	// restart judges its close grace by it (R5).
	Wait time.Duration
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
	// Allow reports whether a read may be made now -- the platform's quota
	// share can afford it -- WITHOUT spending anything. False means the read
	// must not be made.
	Allow(t Target) bool
	// Read asks the platform, charging the calls it actually makes. It answers
	// ErrQuota when the quota refused a call, and a *RetryAfterError when the
	// platform asked to wait.
	Read(ctx context.Context, t Target) (Stats, error)
	// Interval is the wait before the next tick: the configured one, or a
	// longer one when a platform's quota share cannot afford it.
	Interval(configured time.Duration, targets []Target) time.Duration
	// Player is the public player's viewer count and QoE counters, ok false
	// when the player is not running.
	Player() (viewers int, qoe PlayerQoE, ok bool)
}

// OpenState is the stream a previous process left open.
type OpenState struct {
	// Wait is the last tick's chosen interval.
	Wait   time.Duration
	ID     int64
	Start  time.Time
	LastAt time.Time
}

// Store is where samples go.
type Store interface {
	// OpenAnalyticsStream starts a stream record. qoe is the player's
	// counters at the start, nil when the player is not running.
	// lead is how far before `at` the stream's chat starts: the chat between
	// going live and the first tick belongs to the stream.
	OpenAnalyticsStream(at time.Time, qoe *PlayerQoE, lead time.Duration) (int64, error)
	// OpenAnalyticsStreams is every stream left open, newest first.
	OpenAnalyticsStreams() ([]OpenState, error)
	// RecordAnalyticsTick stores one tick's readings, the first broadcast
	// title seen, the player's counters, and tallies the chat that arrived
	// since the previous tick.
	RecordAnalyticsTick(id int64, t Tick) error
	// CloseAnalyticsStream ends the stream at `at`, with a last chat tally.
	CloseAnalyticsStream(id int64, at time.Time) error
	// AnalyticsRecentSamples is the samples of a stream's last `ticks` ticks,
	// in time order: what a resume needs for the sparkline, and no more.
	AnalyticsRecentSamples(id int64, ticks int) ([]Sample, error)
	// PruneAnalytics deletes streams that ended before cutoff, and samples
	// older than it in any stream.
	PruneAnalytics(cutoff time.Time) (int, error)
	// ForgetAnalyticsQoE drops a stream's player QoE baseline, so its report
	// shows no player QoE rather than a delta across a process restart.
	ForgetAnalyticsQoE(id int64) error
}

// Live is the panel's current picture.
type Live struct {
	Enabled bool `json:"enabled"`
	// OnAir is whether anything is streaming: the last tick's verdict, or,
	// when a destination has started since that tick, true with Pending set.
	OnAir bool `json:"onAir"`
	// Pending is set when a destination is running but the sampler has not
	// taken a sample since it started, so there is no count yet. The readings,
	// total and player are empty because nothing has been asked, not because
	// nobody is watching.
	Pending bool    `json:"pending,omitempty"`
	Stream  *Stream `json:"stream,omitempty"`
	// At is the last tick, nil before the first.
	At *time.Time `json:"at,omitempty"`
	// Total is the sum of the readings that reported, nil when none did.
	Total    *int      `json:"total,omitempty"`
	Reported int       `json:"reported"`
	Readings []Reading `json:"readings"`
	Points   []Point   `json:"points"`
	// Partial is set when a platform failed this tick (quota, error, or not
	// live while its destination runs): Total is the rest of the audience,
	// not all of it, and the scrape and MQTT leave the total out.
	Partial bool `json:"partial,omitempty"`
	// Player is the public player's QoE since the stream began, nil when it
	// is not running or its counters were reset mid-stream.
	Player *PlayerQoE `json:"player,omitempty"`
	// IntervalSeconds is the configured interval. EffectiveIntervalSeconds is
	// what the sampler actually waits, and Stretched says it is longer
	// because a platform's quota share cannot afford the configured one.
	IntervalSeconds          int  `json:"intervalSeconds"`
	EffectiveIntervalSeconds int  `json:"effectiveIntervalSeconds"`
	Stretched                bool `json:"stretched,omitempty"`
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
	// failures and retryAt are each account's consecutive failed reads and
	// when it may be asked again (#4: pacing and Retry-After per platform).
	failures map[string]int
	retryAt  map[string]time.Time
	// platformRetryAt holds a whole platform app back after a 429: the
	// bucket is the app's, shared by every account on it (R7).
	platformRetryAt map[string]time.Time
	// firstSeen is when each target came on air, for the offline window (R1).
	firstSeen map[string]time.Time
	// onAirWait is the wait the last on-air tick chose: the close grace is
	// counted in it, stretched or not (R5).
	onAirWait time.Duration
	// wait is the interval the last tick chose; lastTick when it ran.
	wait        time.Duration
	lastTick    time.Time
	lastTargets []Target

	// wake is signalled when the settings change, so a lowered interval does
	// not wait out the old one (#15).
	wake chan struct{}
	// firstWait and onTick are test seams.
	firstWait time.Duration
	onTick    func()

	mu   sync.Mutex
	live Live
}

// Wake tells a running loop the settings changed. Non-blocking.
func (s *Sampler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// WakePending reports whether a Wake is queued that no loop has taken yet. A
// queued wake makes Run skip its initial delay, so a caller that must not
// start sampling early (boot) can check it left none behind.
func (s *Sampler) WakePending() bool { return len(s.wake) > 0 }

// New builds a sampler. now may be nil for time.Now.
func New(src Source, store Store, cfg func() Config, now func() time.Time, log *slog.Logger) *Sampler {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Sampler{src: src, store: store, cfg: cfg, now: now, log: log,
		failures: map[string]int{}, retryAt: map[string]time.Time{},
		platformRetryAt: map[string]time.Time{}, firstSeen: map[string]time.Time{},
		wake: make(chan struct{}, 1), firstWait: defaultFirstWait}
	// Seeded from the settings so the picture before the first tick says
	// whether sampling is on, rather than reading as switched off for the
	// few seconds until the loop's first tick.
	c := s.config()
	s.live = Live{Enabled: c.Enabled, IntervalSeconds: int(c.Interval / time.Second),
		EffectiveIntervalSeconds: int(c.Interval / time.Second),
		Readings:                 []Reading{}, Points: []Point{}}
	return s
}

// Live returns a copy of the current picture.
//
// THE LAST TICK'S "OFF AIR" CAN BE A WHOLE INTERVAL OLD. A destination started
// a second after an off-air tick left the panel, the MQTT topic and the scrape
// saying "Off air" beside a running broadcast for up to a minute (the default
// interval) -- the dashboard was photographed that way. So the source is asked
// now, outside s.mu: a broadcast that has begun since the tick reads as on air
// with no sample yet (Pending), and one that has ended since reads as off air,
// exactly as the next tick will publish it (the stream record and its
// sparkline stay until the close grace runs out).
//
// ASKED BEFORE THE PICTURE IS COPIED, not after. The other order lets a tick
// land between the copy and the question, and the answer then pairs a
// pre-tick "off air" with a post-tick "on air": pending, with no readings,
// over a count that had already been published.
func (s *Sampler) Live() Live {
	enabled := s.config().Enabled
	now := enabled && s.src.OnAir()

	s.mu.Lock()
	l := s.live
	l.Readings = append([]Reading(nil), s.live.Readings...)
	l.Points = append([]Point(nil), s.live.Points...)
	s.mu.Unlock()
	if l.Readings == nil {
		l.Readings = []Reading{}
	}
	if l.Points == nil {
		l.Points = []Point{}
	}
	if l.Enabled && enabled {
		switch {
		case now && !l.OnAir:
			l.OnAir, l.Pending = true, true
			l.Total, l.Reported, l.Partial, l.Player = nil, 0, false, nil
			l.Readings = []Reading{}
		case !now && l.OnAir:
			l.OnAir = false
			l.Total, l.Reported, l.Partial, l.Player = nil, 0, false, nil
			l.Readings = []Reading{}
		}
	}
	return l
}

// Run ticks until ctx ends. The interval is re-read after every tick, so a
// changed setting applies from the next one.
func (s *Sampler) Run(ctx context.Context) {
	wait := s.firstWait
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-s.wake:
			// The settings changed: re-time the wait from the last tick with the
			// new interval, ticking now if it has already passed.
			t.Stop()
			wait = s.remaining()
			continue
		case <-t.C:
		}
		s.Tick(ctx)
		if s.onTick != nil {
			s.onTick()
		}
		wait = s.nextWait()
	}
}

// nextWait is the wait the last tick chose: the configured interval, or the
// source's stretched one.
func (s *Sampler) nextWait() time.Duration {
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	if s.wait > 0 {
		return s.wait
	}
	return s.config().Interval
}

// remaining is how long is left of the interval since the last tick, under
// the current settings.
func (s *Sampler) remaining() time.Duration {
	s.tickMu.Lock()
	last, targets := s.lastTick, s.lastTargets
	s.tickMu.Unlock()
	c := s.config()
	wait := s.src.Interval(c.Interval, targets)
	if last.IsZero() {
		return 0
	}
	if left := wait - s.now().Sub(last); left > 0 {
		return left
	}
	return 0
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

// grace is how long off air before the stream closes: two intervals of
// whatever the sampler is actually waiting -- a stretched wait included (R5)
// -- and never under CloseGrace.
func (s *Sampler) grace(c Config) time.Duration {
	return max(CloseGrace, 2*max(c.Interval, s.onAirWait))
}

// OfflineWindow is the least time after a platform's destination comes on
// air during which "not live" is read as going live (partial), rather than
// as a steady offline (R1). Two intervals, if longer.
const OfflineWindow = 2 * time.Minute

// Tick takes one sample. Exported so tests drive the clock.
func (s *Sampler) Tick(ctx context.Context) {
	s.tickMu.Lock()
	defer s.tickMu.Unlock()

	now := s.now()
	s.lastTick = now
	c := s.config()
	if !s.loaded {
		s.resume(now, c)
	}
	s.prune(now, c)
	configured := int(c.Interval / time.Second)

	if !c.Enabled || !s.src.OnAir() {
		s.offAir(now, c, configured)
		return
	}

	// A stream on air for a day is closed at its last tick and a new one
	// opened, so retention can reach a broadcast that never stops (#11). A
	// failed close keeps the old stream; the next tick tries again.
	if s.open != 0 && now.Sub(s.openStart) >= RolloverAfter {
		s.closeOpen()
	}

	pv, pq, playerOK := s.src.Player()
	var qoe *PlayerQoE
	if playerOK {
		qoe = &pq
	}
	if s.open == 0 && !s.openStream(now, c, qoe) {
		return
	}

	targets, listed, readings, title := s.read(ctx, now, c)
	if playerOK {
		v := pv
		readings = append(readings, Reading{Platform: PlatformPlayer, Live: true, Viewers: &v})
	}
	s.lastTargets = targets
	s.wait = s.src.Interval(c.Interval, targets)
	effective := int(s.wait / time.Second)
	if err := s.store.RecordAnalyticsTick(s.open, Tick{
		At: now, Readings: readings, ChatAccounts: chatAccounts(targets), TallyChat: listed,
		Title: title, QoE: qoe, Wait: s.wait,
	}); err != nil {
		// Memory follows the store (#13): nothing advances for a tick that was
		// not written, so the grace and the sparkline agree with the report.
		s.log.Warn("analytics: could not store a sample", "err", err)
		return
	}
	s.lastOnAir = now
	s.onAirWait = s.wait

	total, reported := Total(readings)
	partial := slices.ContainsFunc(readings, func(r Reading) bool { return transient(r.Reason) })
	point := Point{At: now, Total: total, Partial: partial}
	if partial {
		point.Total = nil
	}
	player := s.playerDelta(playerOK, pq)
	start := s.openStart
	id := s.open
	s.publish(func(l *Live) {
		l.Enabled, l.OnAir = true, true
		l.IntervalSeconds, l.EffectiveIntervalSeconds, l.Stretched = configured, effective, effective > configured
		l.At = &now
		l.Stream = &Stream{ID: id, StartedAt: start, DurationMs: now.Sub(start).Milliseconds()}
		l.Total, l.Reported, l.Readings, l.Player, l.Partial = total, reported, readings, player, partial
		l.Points = append(l.Points, point)
		if len(l.Points) > sparkPoints {
			l.Points = append([]Point(nil), l.Points[len(l.Points)-sparkPoints:]...)
		}
	})
}

// playerDelta is the player's QoE since the stream's baseline, nil without a
// reading or a baseline.
func (s *Sampler) playerDelta(playerOK bool, pq PlayerQoE) *PlayerQoE {
	if !playerOK || s.qoeStart == nil {
		return nil
	}
	if d, ok := pq.Delta(*s.qoeStart); ok {
		return &d
	}
	return nil
}

// offAir is a tick with nothing on air (or analytics off): the open stream
// closes once the grace has passed, and the live view goes quiet.
func (s *Sampler) offAir(now time.Time, c Config, configured int) {
	s.wait = c.Interval
	s.firstSeen = map[string]time.Time{}
	if s.open != 0 && (!c.Enabled || now.Sub(s.lastOnAir) >= s.grace(c)) {
		s.closeOpen()
	}
	s.publish(func(l *Live) {
		l.Enabled, l.OnAir = c.Enabled, false
		l.IntervalSeconds, l.EffectiveIntervalSeconds, l.Stretched = configured, configured, false
		l.At = &now
		l.Total, l.Reported, l.Readings, l.Player, l.Partial = nil, 0, []Reading{}, nil, false
		if s.open == 0 {
			l.Stream, l.Points = nil, []Point{}
		}
	})
}

// openStream opens a stream record for this broadcast, and reports whether
// it could.
func (s *Sampler) openStream(now time.Time, c Config, qoe *PlayerQoE) bool {
	// The chat lead is the wait actually in force, so a stream rolled over
	// while the interval is stretched keeps its chat (R9).
	id, err := s.store.OpenAnalyticsStream(now, qoe, max(c.Interval, s.onAirWait))
	if err != nil {
		s.log.Warn("analytics: could not open a stream record", "err", err)
		return false
	}
	s.open, s.openStart, s.qoeStart = id, now, qoe
	s.publish(func(l *Live) { l.Points = []Point{} })
	return true
}

// chatAccounts is the linked accounts whose chat is tallied.
func chatAccounts(targets []Target) []ChatAccount {
	chats := make([]ChatAccount, 0, len(targets))
	for _, t := range targets {
		if !t.Unlinked && t.Account != "" {
			chats = append(chats, ChatAccount{Platform: t.Platform, Account: t.Account})
		}
	}
	return chats
}

// backoffFor is how long an account is left alone after its n-th
// consecutive failure: the platform's own Retry-After when it sent one,
// otherwise nothing after the first failure (one blip is not a pattern) and
// then doubling from a minute, capped at maxBackoff.
func backoffFor(n int, retry *RetryAfterError) time.Duration {
	if retry != nil && retry.After > 0 {
		return min(retry.After, maxBackoff*6)
	}
	if n <= 1 {
		return 0
	}
	return backoff.Exponential(n-2, time.Minute, maxBackoff)
}

func targetKey(t Target) string { return t.Platform + "/" + t.Account }

// read asks every target, in a stable order, and returns them, the readings
// and the first broadcast title any of them reported.
//
// listed is false when the target list could not be read at all.
func (s *Sampler) read(ctx context.Context, now time.Time, c Config) ([]Target, bool, []Reading, string) {
	targets, err := s.src.Targets(ctx)
	if err != nil {
		s.log.Warn("analytics: could not list accounts", "err", err)
		return nil, false, []Reading{}, ""
	}
	seen := s.trackFirstSeen(targets, now)
	window := max(OfflineWindow, 2*max(c.Interval, s.onAirWait))
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].Platform != targets[j].Platform {
			return targets[i].Platform < targets[j].Platform
		}
		if targets[i].AccountID != targets[j].AccountID {
			return targets[i].AccountID < targets[j].AccountID
		}
		return targets[i].Name < targets[j].Name
	})
	out := make([]Reading, 0, len(targets)+1)
	title := ""
	for _, t := range targets {
		if t.ChatOnly {
			continue
		}
		r, liveTitle := s.readOne(ctx, t, now, now.Sub(seen[targetKey(t)]) < window)
		if title == "" {
			title = liveTitle
		}
		out = append(out, r)
	}
	return targets, true, out, title
}

// trackFirstSeen records when each target came on air, for the offline
// window; a target that left forgets it.
func (s *Sampler) trackFirstSeen(targets []Target, now time.Time) map[string]time.Time {
	seen := make(map[string]time.Time, len(targets))
	for _, t := range targets {
		k := targetKey(t)
		if at, ok := s.firstSeen[k]; ok {
			seen[k] = at
		} else {
			seen[k] = now
		}
	}
	s.firstSeen = seen
	return seen
}

// readOne asks one target for its viewers. starting is a target still inside
// its offline window. title is the broadcast title when the target is live.
func (s *Sampler) readOne(ctx context.Context, t Target, now time.Time, starting bool) (r Reading, title string) {
	r = Reading{Platform: t.Platform, Account: t.Account, Name: t.Name}
	key := targetKey(t)
	if reason := s.notAsked(t, key, now); reason != "" {
		r.Reason = reason
		return r, ""
	}
	rctx, cancel := context.WithTimeout(ctx, readTimeout)
	st, err := s.src.Read(rctx, t)
	cancel()
	switch {
	case errors.Is(err, ErrQuota):
		r.Reason = ReasonQuota
	case err != nil:
		r.Reason = ReasonError
		s.readFailed(t, key, now, err)
	case !st.Live:
		// Not live is not a count of zero viewers on a live stream. A
		// platform that is not carrying this broadcast has nobody to
		// report, so it is left out of the total like any other silence.
		// Just after its destination came on air it is going live, and the
		// tick is partial; after the window it is a steady offline (R1).
		r.Reason = ReasonOffline
		if starting {
			r.Reason = ReasonStarting
		}
	case st.Viewers == nil:
		r.Live, r.Reason = true, ReasonHidden
	default:
		v := *st.Viewers
		r.Live, r.Viewers = true, &v
	}
	if err == nil {
		delete(s.failures, key)
		delete(s.retryAt, key)
	}
	if st.Live {
		title = st.Title
	}
	return r, title
}

// notAsked is the reason a target is not read this tick, or "" to read it.
func (s *Sampler) notAsked(t Target, key string, now time.Time) string {
	switch {
	case t.Unlinked:
		return ReasonUnlinked
	case now.Before(s.retryAt[key]) || now.Before(s.platformRetryAt[t.Platform]):
		// Backing off: the platform asked us to wait, or keeps failing.
		return ReasonError
	case !s.src.Allow(t):
		return ReasonQuota
	}
	return ""
}

// readFailed counts a failed read and backs the account off.
func (s *Sampler) readFailed(t Target, key string, now time.Time, err error) {
	s.failures[key]++
	var retry *RetryAfterError
	errors.As(err, &retry)
	if d := backoffFor(s.failures[key], retry); d > 0 {
		s.retryAt[key] = now.Add(d)
		if retry != nil {
			// A 429 is the app's bucket: every account on it waits (R7).
			s.platformRetryAt[t.Platform] = now.Add(d)
		}
	}
	if !errors.Is(err, context.Canceled) {
		s.log.Info("analytics: platform read failed", "platform", t.Platform, "err", err)
	}
}

// resume picks up the stream a previous process left open: the newest one
// is continued when it was on air within the grace, closed at its last tick
// otherwise, and EVERY other open row is closed (R6). A close that fails is
// retried on the next tick: loaded stays false until they all succeed.
func (s *Sampler) resume(now time.Time, c Config) {
	open, err := s.store.OpenAnalyticsStreams()
	if err != nil {
		s.log.Warn("analytics: could not read the open streams", "err", err)
		return
	}
	ok := true
	for i, st := range open {
		if st.ID == s.open {
			continue
		}
		last := st.LastAt
		if last.IsZero() || last.Before(st.Start) {
			last = st.Start
		}
		grace := max(CloseGrace, 2*max(c.Interval, st.Wait))
		if i == 0 && s.open == 0 && c.Enabled && now.Sub(last) < grace {
			s.adopt(st, last)
			continue
		}
		if err := s.store.CloseAnalyticsStream(st.ID, last); err != nil {
			s.log.Warn("analytics: could not close a stale stream; will retry", "err", err)
			ok = false
		}
	}
	s.loaded = ok
}

// adopt continues a stream a previous process left open.
func (s *Sampler) adopt(st OpenState, last time.Time) {
	s.open, s.openStart, s.lastOnAir, s.onAirWait = st.ID, st.Start, last, st.Wait
	// The QoE baseline did not survive the restart, and the counters it would
	// be subtracted from were reset with the process; the stream's player QoE
	// is reported from here on only if a baseline exists.
	s.qoeStart = nil
	if err := s.store.ForgetAnalyticsQoE(st.ID); err != nil {
		s.log.Warn("analytics: could not drop the player baseline", "err", err)
	}
	if samples, err := s.store.AnalyticsRecentSamples(st.ID, sparkPoints); err == nil {
		pts := Summarise(samples).Points
		if len(pts) > sparkPoints {
			pts = pts[len(pts)-sparkPoints:]
		}
		s.publish(func(l *Live) { l.Points = pts })
	}
}

// closeOpen ends the open stream at its last on-air tick. A close that
// fails leaves it open here as well as in the store, so the next tick tries
// again rather than orphaning a row that retention can never reach (#10).
//
// A stream whose every write failed has no on-air tick: it ends at its own
// start, never before it (R8).
func (s *Sampler) closeOpen() bool {
	at := s.lastOnAir
	if at.Before(s.openStart) {
		at = s.openStart
	}
	if err := s.store.CloseAnalyticsStream(s.open, at); err != nil {
		s.log.Warn("analytics: could not close the stream record; will retry", "err", err)
		return false
	}
	s.open, s.qoeStart = 0, nil
	return true
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
