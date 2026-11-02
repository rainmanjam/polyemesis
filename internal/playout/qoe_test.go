package playout

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func intp(v int) *int { return &v }

func validBeacon() Beacon {
	return Beacon{
		V:             BeaconVersion,
		SessionID:     "0123456789abcdef0123456789abcdef",
		Seq:           1,
		Kind:          BeaconHeartbeat,
		StartupMs:     intp(850),
		RebufferCount: 1,
		RebufferMs:    400,
		Variant:       "720p",
		LatencyMs:     intp(9000),
		Errors:        []BeaconError{BeaconErrNetwork},
	}
}

var ladder = map[string]bool{"720p": true, "360p": true}

func TestValidateBeaconAcceptsAWellFormedBeacon(t *testing.T) {
	if err := ValidateBeacon(validBeacon(), ladder); err != nil {
		t.Fatalf("a well-formed beacon was refused: %v", err)
	}
	b := validBeacon()
	b.Variant, b.StartupMs, b.LatencyMs, b.Errors = "", nil, nil, nil
	if err := ValidateBeacon(b, ladder); err != nil {
		t.Fatalf("a minimal beacon (no variant, no optional numbers) was refused: %v", err)
	}
}

// Every refusal the schema promises, one mutation of a valid beacon each. A
// rule dropped from ValidateBeacon makes its row pass and this test fail.
func TestValidateBeaconRefusesEveryOutOfSchemaField(t *testing.T) {
	cases := map[string]func(b *Beacon){
		"wrong version":          func(b *Beacon) { b.V = 2 },
		"zero version":           func(b *Beacon) { b.V = 0 },
		"short sid":              func(b *Beacon) { b.SessionID = "abc" },
		"long sid":               func(b *Beacon) { b.SessionID = strings.Repeat("a", 65) },
		"sid with dot":           func(b *Beacon) { b.SessionID = "0123456789abcdef.0123" },
		"sid that is an ip":      func(b *Beacon) { b.SessionID = "203.0.113.7:51515:aa" },
		"negative seq":           func(b *Beacon) { b.Seq = -1 },
		"huge seq":               func(b *Beacon) { b.Seq = MaxBeaconSeq + 1 },
		"unknown kind":           func(b *Beacon) { b.Kind = "pageview" },
		"empty kind":             func(b *Beacon) { b.Kind = "" },
		"negative startup":       func(b *Beacon) { b.StartupMs = intp(-1) },
		"huge startup":           func(b *Beacon) { b.StartupMs = intp(MaxBeaconStartupMs + 1) },
		"negative rebuffers":     func(b *Beacon) { b.RebufferCount = -1 },
		"huge rebuffers":         func(b *Beacon) { b.RebufferCount = MaxBeaconRebuffers + 1 },
		"negative rebuffer ms":   func(b *Beacon) { b.RebufferMs = -1 },
		"huge rebuffer ms":       func(b *Beacon) { b.RebufferMs = MaxBeaconRebufferMs + 1 },
		"negative latency":       func(b *Beacon) { b.LatencyMs = intp(-5) },
		"huge latency":           func(b *Beacon) { b.LatencyMs = intp(MaxBeaconLatencyMs + 1) },
		"variant off the ladder": func(b *Beacon) { b.Variant = "1080p" },
		"variant too long":       func(b *Beacon) { b.Variant = strings.Repeat("v", MaxBeaconVariantLength+1) },
		"free-text error":        func(b *Beacon) { b.Errors = []BeaconError{"GET /playout/a/seg.ts?t=secret 404"} },
		"too many errors": func(b *Beacon) {
			b.Errors = make([]BeaconError, MaxBeaconErrors+1)
			for i := range b.Errors {
				b.Errors[i] = BeaconErrMedia
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			b := validBeacon()
			mutate(&b)
			err := ValidateBeacon(b, ladder)
			if err == nil {
				t.Fatalf("accepted a beacon with %s", name)
			}
			if !errors.Is(err, ErrBeaconInvalid) {
				t.Fatalf("refusal %v does not wrap ErrBeaconInvalid", err)
			}
		})
	}
}

func TestValidateBeaconBoundsAreInclusive(t *testing.T) {
	b := validBeacon()
	b.Seq, b.RebufferCount, b.RebufferMs = MaxBeaconSeq, MaxBeaconRebuffers, MaxBeaconRebufferMs
	b.StartupMs, b.LatencyMs = intp(MaxBeaconStartupMs), intp(MaxBeaconLatencyMs)
	b.Errors = make([]BeaconError, MaxBeaconErrors)
	for i := range b.Errors {
		b.Errors[i] = BeaconErrors[i%len(BeaconErrors)]
	}
	if err := ValidateBeacon(b, ladder); err != nil {
		t.Fatalf("a beacon at every upper bound was refused: %v", err)
	}
}

func TestQoERecordTakesDeltasAndDropsDuplicates(t *testing.T) {
	q := NewQoE(time.Minute, 10)
	now := time.Unix(1_700_000_000, 0)

	b := validBeacon()
	if !q.Record(b, now) {
		t.Fatal("first beacon not recorded")
	}
	// The same beacon again -- a keepalive retry -- changes nothing but the count.
	if q.Record(b, now) {
		t.Fatal("a duplicate seq was recorded as new")
	}
	b.Seq, b.RebufferCount, b.RebufferMs = 2, 3, 1000
	b.StartupMs = intp(5000) // a second startup in one session is ignored
	b.Errors = []BeaconError{BeaconErrMedia, BeaconErrMedia}
	q.Record(b, now.Add(15*time.Second))

	s := q.Summary(now.Add(16 * time.Second))
	if s.Beacons != 3 || s.Sessions != 1 {
		t.Fatalf("beacons=%d sessions=%d, want 3 and 1", s.Beacons, s.Sessions)
	}
	if s.Startups != 1 || s.StartupMs != 850 {
		t.Fatalf("startups=%d startupMs=%d, want one startup of 850ms", s.Startups, s.StartupMs)
	}
	if s.Rebuffers != 3 || s.RebufferMs != 1000 {
		t.Fatalf("rebuffers=%d rebufferMs=%d, want the cumulative 3 and 1000, not a sum of reports",
			s.Rebuffers, s.RebufferMs)
	}
	if s.Errors[BeaconErrNetwork] != 1 || s.Errors[BeaconErrMedia] != 2 {
		t.Fatalf("errors=%v, want network:1 media:2 (the duplicate's network not counted twice)", s.Errors)
	}
	if s.Active != 1 || s.ByVariant["720p"] != 1 {
		t.Fatalf("active=%d byVariant=%v", s.Active, s.ByVariant)
	}
	if s.LatencyMsMedian == nil || *s.LatencyMsMedian != 9000 {
		t.Fatalf("latency median = %v, want 9000", s.LatencyMsMedian)
	}
}

func TestQoECounterResetIsANewBaselineNotANegative(t *testing.T) {
	q := NewQoE(time.Minute, 10)
	now := time.Unix(1_700_000_000, 0)
	b := validBeacon()
	b.RebufferCount, b.RebufferMs = 5, 5000
	q.Record(b, now)
	b.Seq, b.RebufferCount, b.RebufferMs = 2, 1, 100
	q.Record(b, now)
	b.Seq, b.RebufferCount, b.RebufferMs = 3, 2, 300
	q.Record(b, now)
	s := q.Summary(now)
	if s.Rebuffers != 6 || s.RebufferMs != 5200 {
		t.Fatalf("rebuffers=%d ms=%d, want 5+1 and 5000+200", s.Rebuffers, s.RebufferMs)
	}
}

func TestQoEEndedAndIdleSessionsLeaveTheActiveCount(t *testing.T) {
	q := NewQoE(time.Minute, 10)
	now := time.Unix(1_700_000_000, 0)
	a := validBeacon()
	q.Record(a, now)
	b := validBeacon()
	b.SessionID = "fedcba9876543210fedcba9876543210"
	b.Variant, b.LatencyMs = "360p", intp(3000)
	q.Record(b, now)
	if s := q.Summary(now); s.Active != 2 || *s.LatencyMsMedian != 6000 {
		t.Fatalf("active=%d median=%v, want 2 and 6000", s.Active, s.LatencyMsMedian)
	}
	b.Seq, b.Kind = 2, BeaconEnd
	q.Record(b, now)
	if s := q.Summary(now); s.Active != 1 || s.ByVariant["360p"] != 0 {
		t.Fatalf("an ended session still counts: active=%d byVariant=%v", s.Active, s.ByVariant)
	}
	if s := q.Summary(now.Add(2 * time.Minute)); s.Active != 0 || s.LatencyMsMedian != nil {
		t.Fatalf("an idle session still counts: active=%d", s.Active)
	}
	if s := q.Summary(now.Add(2 * time.Minute)); s.Sessions != 2 || s.Beacons != 3 {
		t.Fatalf("pruning lost totals: %+v", s.QoETotals)
	}
}

func TestQoEFullTableCountsErrorsButNotCumulativeCounters(t *testing.T) {
	q := NewQoE(time.Minute, 1)
	now := time.Unix(1_700_000_000, 0)
	q.Record(validBeacon(), now)
	b := validBeacon()
	b.SessionID = "fedcba9876543210fedcba9876543210"
	b.RebufferCount, b.RebufferMs = 50, 50000
	q.Record(b, now)
	s := q.Summary(now)
	if s.Untracked != 1 || s.Sessions != 1 {
		t.Fatalf("untracked=%d sessions=%d", s.Untracked, s.Sessions)
	}
	if s.Rebuffers != 1 || s.Errors[BeaconErrNetwork] != 2 {
		t.Fatalf("rebuffers=%d errors=%v: an untracked session's counters were taken as deltas",
			s.Rebuffers, s.Errors)
	}
}

func TestQoEResetClearsEverything(t *testing.T) {
	q := NewQoE(0, 0)
	now := time.Unix(1_700_000_000, 0)
	q.Record(validBeacon(), now)
	q.Reset()
	s := q.Summary(now)
	if s.Beacons != 0 || s.Active != 0 || len(s.Errors) != 0 {
		t.Fatalf("reset left %+v", s)
	}
	// A summary is a copy: mutating it must not reach the aggregate.
	q.Record(validBeacon(), now)
	s = q.Summary(now)
	s.Errors[BeaconErrNetwork] = 99
	if q.Summary(now).Errors[BeaconErrNetwork] != 1 {
		t.Fatal("Summary handed out the live error map")
	}
}

func TestBeaconLimiterPerKeyAndGlobal(t *testing.T) {
	l := NewBeaconLimiter()
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < int(BeaconBurstPerKey); i++ {
		if !l.Allow(1, now) {
			t.Fatalf("refused request %d inside the burst", i)
		}
	}
	if l.Allow(1, now) {
		t.Fatal("allowed past the per-key burst")
	}
	if !l.Allow(2, now) {
		t.Fatal("one key's flood refused another key")
	}
	// Refill is clock-driven: one second buys BeaconRatePerKey tokens.
	if !l.Allow(1, now.Add(time.Second)) {
		t.Fatal("no refill after a second")
	}

	g := NewBeaconLimiter()
	g.gBurst, g.global.tokens = 3, 3
	for k := uint64(0); k < 3; k++ {
		if !g.Allow(k, now) {
			t.Fatalf("global refused key %d inside its burst", k)
		}
	}
	if g.Allow(99, now) {
		t.Fatal("allowed past the global burst")
	}
}

func TestBeaconLimiterForgetsFullBucketsAndBoundsKeys(t *testing.T) {
	l := NewBeaconLimiter()
	l.maxKeys = 2
	now := time.Unix(1_700_000_000, 0)
	l.Allow(1, now)
	l.Allow(2, now)
	if l.Allow(3, now) {
		t.Fatal("a third key was admitted with the table full of live buckets")
	}
	// Long enough for both buckets to refill: they are forgotten, and room is made.
	later := now.Add(time.Hour)
	l.mu.Lock()
	l.pruneLocked(later)
	n := len(l.buckets)
	l.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d full buckets survived a prune", n)
	}
	if !l.Allow(3, later) {
		t.Fatal("a new key was refused after the table emptied")
	}
}
