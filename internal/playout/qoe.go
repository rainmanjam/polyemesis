package playout

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"
)

// QoE beacons: what the public player reports about how playback went.
//
// THIS FILE IS THE BEACON CONTRACT. The Go types here, BeaconV1 in
// ui/src/lib/types.ts and the "QoE beacons" section of docs/API.md are the same
// schema written three times, and TestUITypesCanNameEveryBeaconField holds the
// first two together. Cross-platform analytics reads the aggregate below, so a
// field changes meaning only with a new BeaconVersion, never in place.
//
// What a beacon may carry is decided by what it must NOT carry. There is no IP,
// no user agent, no cookie and no account here, and there is no free text
// anywhere: an hls.js error detail includes the fragment URL, the fragment URL
// carries ?t=<playback token>, and a free-text "message" field would have
// shipped the stream's credential into the analytics. Every string is either a
// closed enum, a variant name checked against the running ladder, or the
// session id, which is random per page load and is never a metric label.

// BeaconVersion is the schema version a beacon must declare. A beacon from a
// newer page is refused rather than half-understood.
const BeaconVersion = 1

// MaxBeaconBytes caps one beacon body. A well-formed beacon is about 300 bytes;
// the cap leaves room for the eight error codes and nothing else.
const MaxBeaconBytes = 2048

// BeaconKind is where in the session a beacon was sent from.
type BeaconKind string

const (
	// BeaconStart is sent once playback has begun (or failed to begin).
	BeaconStart BeaconKind = "start"
	// BeaconHeartbeat is the periodic report while the page is open.
	BeaconHeartbeat BeaconKind = "heartbeat"
	// BeaconEnd is sent when the page is hidden or closed.
	BeaconEnd BeaconKind = "end"
)

// BeaconError is a closed set of failure classes. Never a message.
type BeaconError string

const (
	BeaconErrNetwork          BeaconError = "network"
	BeaconErrManifest         BeaconError = "manifest"
	BeaconErrMedia            BeaconError = "media"
	BeaconErrDecode           BeaconError = "decode"
	BeaconErrCodecUnsupported BeaconError = "codec_unsupported"
	BeaconErrOther            BeaconError = "other"
)

// BeaconErrors is every error code, in the order they are reported.
var BeaconErrors = []BeaconError{
	BeaconErrNetwork, BeaconErrManifest, BeaconErrMedia,
	BeaconErrDecode, BeaconErrCodecUnsupported, BeaconErrOther,
}

// Bounds. Generous: they catch a unit mix-up or a forged body, not a bad
// connection.
const (
	MaxBeaconSeq           = 1_000_000
	MaxBeaconRebuffers     = 100_000
	MaxBeaconRebufferMs    = 24 * 60 * 60 * 1000
	MaxBeaconStartupMs     = 10 * 60 * 1000
	MaxBeaconLatencyMs     = 6 * 60 * 60 * 1000
	MaxBeaconErrors        = 8
	MaxBeaconVariantLength = 64
)

// sessionIDPattern is the per-page-load id: 16 to 64 URL-safe characters. The
// page draws 16 random bytes and hex-encodes them; anything outside this shape
// is not something the page sent.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

// Beacon is one report, schema version 1.
//
// Counters are CUMULATIVE for the session and the server takes the deltas, so a
// keepalive retry or a duplicated post cannot inflate a total: the second copy
// carries the same seq and is dropped. Errors are the exception -- they are the
// codes seen since the previous beacon -- and the seq rule is what keeps them
// from being counted twice.
type Beacon struct {
	// V is the schema version; must be BeaconVersion.
	V int `json:"v"`
	// SessionID is random per page load. It is never stored past the session
	// table's idle window and never used as a metric label.
	SessionID string `json:"sid"`
	// Seq increases by at least one per beacon within a session.
	Seq  int        `json:"seq"`
	Kind BeaconKind `json:"kind"`
	// StartupMs is from the player being created to the first frame. Sent on
	// the beacon after playback begins; counted once per session.
	StartupMs *int `json:"startupMs,omitempty"`
	// RebufferCount and RebufferMs are cumulative stalls after startup.
	RebufferCount int `json:"rebufferCount"`
	RebufferMs    int `json:"rebufferMs"`
	// Variant is the rung playing now: a NAME from the ladder, or empty when
	// the player has not picked one (native HLS does not say).
	Variant string `json:"variant,omitempty"`
	// LatencyMs is how far behind the live edge the playhead is.
	LatencyMs *int `json:"latencyMs,omitempty"`
	// Errors are the failure classes seen since the previous beacon.
	Errors []BeaconError `json:"errors,omitempty"`
}

// ErrBeaconInvalid wraps every validation refusal, so the handler can answer
// 400 without inspecting the reason.
var ErrBeaconInvalid = errors.New("invalid beacon")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBeaconInvalid, fmt.Sprintf(format, a...))
}

// ValidateBeacon checks one beacon against the schema and the running ladder.
// variants is the set of names a beacon may report; an empty Variant is always
// allowed.
func ValidateBeacon(b Beacon, variants map[string]bool) error {
	if err := ValidateBeaconSchema(b); err != nil {
		return err
	}
	if b.Variant != "" && !variants[b.Variant] {
		return invalid("variant is not on the ladder")
	}
	return nil
}

// ValidateBeaconSchema is every check that needs nothing but the beacon: the
// version, the session id's shape, every bound and every enum. The ladder
// check is ValidateBeacon's, because it needs the running manager.
func ValidateBeaconSchema(b Beacon) error {
	if b.V != BeaconVersion {
		return invalid("v must be %d", BeaconVersion)
	}
	if !sessionIDPattern.MatchString(b.SessionID) {
		return invalid("sid must be 16-64 characters of [A-Za-z0-9_-]")
	}
	if b.Seq < 0 || b.Seq > MaxBeaconSeq {
		return invalid("seq out of range")
	}
	switch b.Kind {
	case BeaconStart, BeaconHeartbeat, BeaconEnd:
	default:
		return invalid("kind must be start, heartbeat or end")
	}
	if b.StartupMs != nil && (*b.StartupMs < 0 || *b.StartupMs > MaxBeaconStartupMs) {
		return invalid("startupMs out of range")
	}
	if b.RebufferCount < 0 || b.RebufferCount > MaxBeaconRebuffers {
		return invalid("rebufferCount out of range")
	}
	if b.RebufferMs < 0 || b.RebufferMs > MaxBeaconRebufferMs {
		return invalid("rebufferMs out of range")
	}
	if b.LatencyMs != nil && (*b.LatencyMs < 0 || *b.LatencyMs > MaxBeaconLatencyMs) {
		return invalid("latencyMs out of range")
	}
	if len(b.Variant) > MaxBeaconVariantLength {
		return invalid("variant name too long")
	}
	if len(b.Errors) > MaxBeaconErrors {
		return invalid("at most %d errors per beacon", MaxBeaconErrors)
	}
	for _, e := range b.Errors {
		if !knownBeaconError(e) {
			return invalid("unknown error code")
		}
	}
	return nil
}

func knownBeaconError(e BeaconError) bool {
	for _, k := range BeaconErrors {
		if e == k {
			return true
		}
	}
	return false
}

// QoETotals are the counters since the last reset. Monotonic between resets,
// which is what lets Prometheus read them as counters.
type QoETotals struct {
	// Beacons is every beacon accepted, including duplicates dropped by seq.
	Beacons int64 `json:"beacons"`
	// Sessions is how many distinct session ids reported.
	Sessions int64 `json:"sessions"`
	// Startups and StartupMs give the mean startup time.
	Startups  int64 `json:"startups"`
	StartupMs int64 `json:"startupMs"`
	// Rebuffers and RebufferMs are stalls after startup, summed over sessions.
	Rebuffers  int64 `json:"rebuffers"`
	RebufferMs int64 `json:"rebufferMs"`
	// Errors counts each error code.
	Errors map[BeaconError]int64 `json:"errors"`
	// Untracked is beacons from sessions that arrived with the table full.
	// Their errors still count; their cumulative counters cannot.
	Untracked int64 `json:"untracked"`
}

// QoESummary is the aggregate the admin analytics and the metrics read.
type QoESummary struct {
	QoETotals
	// Active is sessions that reported within the idle window.
	Active int `json:"active"`
	// ByVariant is active sessions by the rung they last reported.
	ByVariant map[string]int `json:"byVariant"`
	// LatencyMsMedian is the median of active sessions' latest latency, absent
	// when none reported one.
	LatencyMsMedian *int `json:"latencyMsMedian,omitempty"`
}

// qoeSession is the server's memory of one page load: just enough to take
// deltas. Nothing here identifies the viewer.
type qoeSession struct {
	seq        int
	rebuffers  int
	rebufferMs int
	startup    bool
	variant    string
	latencyMs  int
	hasLatency bool
	lastSeen   time.Time
	ended      bool
}

// QoE aggregates beacons. Safe for concurrent use.
type QoE struct {
	mu       sync.Mutex
	idle     time.Duration
	max      int
	sessions map[string]*qoeSession
	totals   QoETotals
}

// Default session-table bounds.
const (
	DefaultQoEIdle        = 2 * time.Minute
	DefaultQoEMaxSessions = 10_000
)

// NewQoE creates an aggregator. Zero arguments take the defaults.
func NewQoE(idle time.Duration, max int) *QoE {
	if idle <= 0 {
		idle = DefaultQoEIdle
	}
	if max <= 0 {
		max = DefaultQoEMaxSessions
	}
	return &QoE{idle: idle, max: max, sessions: map[string]*qoeSession{}, totals: emptyTotals()}
}

func emptyTotals() QoETotals {
	return QoETotals{Errors: map[BeaconError]int64{}}
}

// Record folds one VALIDATED beacon into the aggregate. It reports false when
// the beacon was a duplicate or out of order and changed nothing but the
// beacon count.
func (q *QoE) Record(b Beacon, now time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.totals.Beacons++

	s, ok := q.sessions[b.SessionID]
	if !ok {
		q.pruneLocked(now)
		if len(q.sessions) >= q.max {
			// The table is full. The errors are still a fact about this
			// install's playback; the cumulative counters cannot be turned into
			// deltas without a row, so they are not counted at all rather than
			// counted as if this were the session's first report.
			q.totals.Untracked++
			q.countErrorsLocked(b.Errors)
			return true
		}
		s = &qoeSession{seq: -1}
		q.sessions[b.SessionID] = s
		q.totals.Sessions++
	}
	if b.Seq <= s.seq {
		return false
	}
	// A counter that went backwards is a page that reset itself; take the new
	// value as the baseline rather than subtracting into a negative.
	if b.RebufferCount >= s.rebuffers {
		q.totals.Rebuffers += int64(b.RebufferCount - s.rebuffers)
	}
	if b.RebufferMs >= s.rebufferMs {
		q.totals.RebufferMs += int64(b.RebufferMs - s.rebufferMs)
	}
	s.rebuffers, s.rebufferMs = b.RebufferCount, b.RebufferMs
	if b.StartupMs != nil && !s.startup {
		s.startup = true
		q.totals.Startups++
		q.totals.StartupMs += int64(*b.StartupMs)
	}
	q.countErrorsLocked(b.Errors)
	s.seq = b.Seq
	s.variant = b.Variant
	if b.LatencyMs != nil {
		s.latencyMs, s.hasLatency = *b.LatencyMs, true
	}
	s.lastSeen = now
	s.ended = b.Kind == BeaconEnd
	return true
}

func (q *QoE) countErrorsLocked(errs []BeaconError) {
	for _, e := range errs {
		q.totals.Errors[e]++
	}
}

func (q *QoE) pruneLocked(now time.Time) {
	for id, s := range q.sessions {
		if now.Sub(s.lastSeen) > q.idle {
			delete(q.sessions, id)
		}
	}
}

// Summary reports the aggregate at now.
func (q *QoE) Summary(now time.Time) QoESummary {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pruneLocked(now)

	out := QoESummary{QoETotals: q.totals, ByVariant: map[string]int{}}
	out.Errors = make(map[BeaconError]int64, len(q.totals.Errors))
	for k, v := range q.totals.Errors {
		out.Errors[k] = v
	}
	var lat []int
	for _, s := range q.sessions {
		if s.ended {
			continue
		}
		out.Active++
		if s.variant != "" {
			out.ByVariant[s.variant]++
		}
		if s.hasLatency {
			lat = append(lat, s.latencyMs)
		}
	}
	if len(lat) > 0 {
		sort.Ints(lat)
		m := lat[len(lat)/2]
		if len(lat)%2 == 0 {
			m = (lat[len(lat)/2-1] + lat[len(lat)/2]) / 2
		}
		out.LatencyMsMedian = &m
	}
	return out
}

// Reset clears the aggregate, alongside the viewer table.
func (q *QoE) Reset() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sessions = map[string]*qoeSession{}
	q.totals = emptyTotals()
}

// BeaconLimiter is a per-client token bucket in front of the beacon endpoint,
// plus one bucket for the whole endpoint.
//
// Keys are whatever the caller hands in -- the API hashes the client address
// with a per-process salt first -- and live only in memory, only as long as the
// bucket is not full. A full bucket is the same as no bucket, so it is dropped.
type BeaconLimiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second, per key
	burst   float64
	gRate   float64
	gBurst  float64
	global  bucket
	buckets map[uint64]*bucket
	maxKeys int
}

type bucket struct {
	tokens float64
	at     time.Time
}

// Beacon limiter defaults. A page beacons every 15 seconds; a household or an
// office behind one address may hold dozens of pages open, so the per-address
// allowance is wide and the global one is what bounds the work.
const (
	BeaconRatePerKey   = 2.0
	BeaconBurstPerKey  = 40.0
	BeaconRateGlobal   = 500.0
	BeaconBurstGlobal  = 1000.0
	BeaconLimiterKeys  = 50_000
	beaconPruneTrigger = BeaconLimiterKeys
)

// NewBeaconLimiter returns a limiter with the default allowances.
func NewBeaconLimiter() *BeaconLimiter {
	return &BeaconLimiter{
		rate: BeaconRatePerKey, burst: BeaconBurstPerKey,
		gRate: BeaconRateGlobal, gBurst: BeaconBurstGlobal,
		global:  bucket{tokens: BeaconBurstGlobal},
		buckets: map[uint64]*bucket{},
		maxKeys: BeaconLimiterKeys,
	}
}

func refill(b *bucket, rate, burst float64, now time.Time) {
	if !b.at.IsZero() {
		if d := now.Sub(b.at).Seconds(); d > 0 {
			b.tokens += d * rate
		}
	}
	if b.tokens > burst {
		b.tokens = burst
	}
	b.at = now
}

// Allow spends one token for key, and one from the global bucket.
func (l *BeaconLimiter) Allow(key uint64, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	refill(&l.global, l.gRate, l.gBurst, now)
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= beaconPruneTrigger {
			l.pruneLocked(now)
		}
		if len(l.buckets) >= l.maxKeys {
			// Still full of live buckets: this many distinct addresses beaconing
			// at once is a flood, and the global bucket is the honest limit.
			return false
		}
		b = &bucket{tokens: l.burst, at: now}
		l.buckets[key] = b
	}
	refill(b, l.rate, l.burst, now)
	if b.tokens < 1 || l.global.tokens < 1 {
		return false
	}
	b.tokens--
	l.global.tokens--
	return true
}

func (l *BeaconLimiter) pruneLocked(now time.Time) {
	for k, b := range l.buckets {
		refill(b, l.rate, l.burst, now)
		if b.tokens >= l.burst {
			delete(l.buckets, k)
		}
	}
}
