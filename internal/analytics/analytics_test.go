package analytics

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func ip(v int) *int { return &v }

// TestTotalLeavesOutWhatWasNotReported is the rule the whole package exists to
// hold: a reading with no count adds nothing, and a total over readings none of
// which reported is absent rather than 0.
func TestTotalLeavesOutWhatWasNotReported(t *testing.T) {
	total, n := Total([]Reading{
		{Platform: "youtube", Viewers: ip(40)},
		{Platform: "kick", Reason: ReasonHidden},
		{Platform: "twitch", Viewers: ip(2)},
		{Platform: PlatformPlayer, Viewers: ip(0)},
	})
	if total == nil || *total != 42 || n != 3 {
		t.Fatalf("Total = %v over %d readings, want 42 over 3 (the hidden Kick count is not a 0)", deref(total), n)
	}

	total, n = Total([]Reading{{Platform: "kick", Reason: ReasonHidden}, {Platform: "youtube", Reason: ReasonQuota}})
	if total != nil || n != 0 {
		t.Fatalf("Total over nothing that reported = %v (%d), want nil: not reported is not zero viewers", deref(total), n)
	}
}

func deref(p *int) any {
	if p == nil {
		return "nil"
	}
	return *p
}

// TestSummariseAveragesOnlyReportedSamplesAndPeaksTheTickTotal pins the report
// math: per-platform average over the samples that reported (a hidden tick is
// not a 0 that halves the mean), and the peak total is the highest single-tick
// total -- not the sum of per-platform peaks, which adds moments that never
// happened together.
func TestSummariseAveragesOnlyReportedSamplesAndPeaksTheTickTotal(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	t1, t2 := t0.Add(time.Minute), t0.Add(2*time.Minute)
	s := Summarise([]Sample{
		{At: t0, Platform: "youtube", Account: "yt", Name: "Main", Viewers: ip(10)},
		{At: t0, Platform: "twitch", Account: "tw", Viewers: ip(50)},
		{At: t1, Platform: "youtube", Account: "yt", Viewers: nil},
		{At: t1, Platform: "twitch", Account: "tw", Viewers: ip(5)},
		{At: t2, Platform: "youtube", Account: "yt", Viewers: ip(30)},
		{At: t2, Platform: "twitch", Account: "tw", Viewers: ip(20)},
		{At: t2, Platform: PlatformPlayer, Viewers: ip(1)},
	})
	if len(s.Rows) != 3 {
		t.Fatalf("rows = %+v, want three", s.Rows)
	}
	// Sorted platform order, the player last.
	if s.Rows[0].Platform != "twitch" || s.Rows[1].Platform != "youtube" || s.Rows[2].Platform != PlatformPlayer {
		t.Fatalf("row order = %s, %s, %s", s.Rows[0].Platform, s.Rows[1].Platform, s.Rows[2].Platform)
	}
	yt := s.Rows[1]
	if yt.Samples != 3 || yt.Reported != 2 {
		t.Fatalf("youtube samples/reported = %d/%d, want 3/2", yt.Samples, yt.Reported)
	}
	if yt.Peak == nil || *yt.Peak != 30 {
		t.Fatalf("youtube peak = %v, want 30", deref(yt.Peak))
	}
	if yt.Average == nil || *yt.Average != 20 {
		t.Fatalf("youtube average = %v, want 20 ((10+30)/2, the hidden tick left out)", yt.Average)
	}
	if yt.Name != "Main" {
		t.Fatalf("youtube name = %q", yt.Name)
	}
	tw := s.Rows[0]
	if tw.Peak == nil || *tw.Peak != 50 || tw.Average == nil || *tw.Average != 25 {
		t.Fatalf("twitch peak/avg = %v/%v, want 50/25", deref(tw.Peak), tw.Average)
	}
	// Ticks: 60, 5, 51. Peak total 60 (not 50+30+1 = 81).
	if s.PeakTotal == nil || *s.PeakTotal != 60 {
		t.Fatalf("peak total = %v, want 60 (the best single moment)", deref(s.PeakTotal))
	}
	if s.AverageTotal == nil || *s.AverageTotal != 38.7 {
		t.Fatalf("average total = %v, want 38.7", *s.AverageTotal)
	}
	if len(s.Points) != 3 || *s.Points[1].Total != 5 {
		t.Fatalf("points = %+v", s.Points)
	}
}

func TestSummariseOfARowThatNeverReportedHasNoPeakOrAverage(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	s := Summarise([]Sample{
		{At: t0, Platform: "kick", Account: "k"},
		{At: t0.Add(time.Minute), Platform: "kick", Account: "k"},
	})
	if len(s.Rows) != 1 || s.Rows[0].Peak != nil || s.Rows[0].Average != nil || s.Rows[0].Reported != 0 {
		t.Fatalf("a row that never reported = %+v, want no peak and no average", s.Rows)
	}
	if s.PeakTotal != nil || s.AverageTotal != nil {
		t.Fatalf("totals over nothing reported = %v/%v, want nil", deref(s.PeakTotal), s.AverageTotal)
	}
	if len(s.Points) != 2 || s.Points[0].Total != nil {
		t.Fatalf("points = %+v, want two gaps", s.Points)
	}
}

func TestPeakIsTheHighestEvenWhenTheFirstSampleIsLower(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	s := Summarise([]Sample{
		{At: t0, Platform: "kick", Viewers: ip(0)},
		{At: t0.Add(time.Minute), Platform: "kick", Viewers: ip(7)},
		{At: t0.Add(2 * time.Minute), Platform: "kick", Viewers: ip(3)},
	})
	if *s.Rows[0].Peak != 7 {
		t.Fatalf("peak = %d, want 7", *s.Rows[0].Peak)
	}
}

func TestByPlatformSumsAccountsAndLeavesOutThePlayer(t *testing.T) {
	l := Live{Readings: []Reading{
		{Platform: "youtube", Account: "a", Viewers: ip(3)},
		{Platform: "youtube", Account: "b", Viewers: ip(4)},
		{Platform: "youtube", Account: "c", Reason: ReasonHidden},
		{Platform: "kick", Reason: ReasonHidden},
		{Platform: PlatformPlayer, Viewers: ip(9)},
	}}
	got := l.ByPlatform()
	if v := got["youtube"]; v == nil || *v != 7 {
		t.Fatalf("youtube = %v, want 7", deref(v))
	}
	if v, ok := got["kick"]; !ok || v != nil {
		t.Fatalf("kick = %v (present %v), want present and nil", deref(v), ok)
	}
	if _, ok := got[PlatformPlayer]; ok {
		t.Fatal("the player is published by the playout scrape and must not be re-exported per platform")
	}
}

func TestQoEDeltaRefusesACounterThatWentDown(t *testing.T) {
	start := PlayerQoE{Sessions: 5, Rebuffers: 2, StartupHistogram: []int64{1, 2}}
	end := PlayerQoE{Sessions: 9, Rebuffers: 3, StartupHistogram: []int64{2, 4}}
	d, ok := end.Delta(start)
	if !ok || d.Sessions != 4 || d.Rebuffers != 1 || d.StartupHistogram[1] != 2 {
		t.Fatalf("delta = %+v ok=%v", d, ok)
	}
	if _, ok := (PlayerQoE{Sessions: 1}).Delta(start); ok {
		t.Fatal("a reset mid-stream must not produce a delta")
	}
}

func TestCSVSaysNotReportedAndNeutralisesFormulae(t *testing.T) {
	var b bytes.Buffer
	err := WriteCSV(&b, Report{
		Stream: Stream{ID: 7, Title: "=cmd|calc", StartedAt: time.Unix(1_800_000_000, 0), DurationMs: 90_000},
		Summary: Summary{Rows: []Row{
			{Platform: "kick", Account: "k", Samples: 2},
			{Platform: "youtube", Samples: 2, Reported: 2, Peak: ip(12), Average: ptrF(10.5)},
		}, PeakTotal: ip(12), AverageTotal: ptrF(10.5)},
		Chat:        []ChatCount{{Platform: "twitch", Messages: 4}},
		TopChatters: []Chatter{{Platform: "twitch", Name: "@everyone", Messages: 4}, {Platform: "kick", Name: "+1", Messages: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"kick,k,,2,0,not reported,not reported",
		"youtube,,,2,2,12,10.5",
		"total,,,,,12,10.5",
		"'=cmd|calc",
		"twitch,'@everyone,4",
		"kick,'+1,1",
		"7,'=cmd|calc,2027-01-15T08:00:00Z,,90",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("CSV is missing %q:\n%s", want, out)
		}
	}
}

func ptrF(v float64) *float64 { return &v }

// ----------------------------------------------------------------- sampler

type fakeSource struct {
	mu      sync.Mutex
	onAir   bool
	targets []Target
	stats   map[string]Stats
	errs    map[string]error
	deny    map[string]bool
	reads   map[string]int
	allows  map[string]int
	player  *int
	qoe     PlayerQoE
	// interval, when set, is the source's pacing of the configured interval.
	interval func(time.Duration) time.Duration
}

func (f *fakeSource) Interval(configured time.Duration, _ []Target) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.interval != nil {
		return f.interval(configured)
	}
	return configured
}

func (f *fakeSource) OnAir() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.onAir }
func (f *fakeSource) Targets(context.Context) ([]Target, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Target(nil), f.targets...), nil
}
func (f *fakeSource) Allow(t Target) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allows[t.Account]++
	return !f.deny[t.Account]
}
func (f *fakeSource) Read(_ context.Context, t Target) (Stats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads[t.Account]++
	if err := f.errs[t.Account]; err != nil {
		return Stats{}, err
	}
	return f.stats[t.Account], nil
}
func (f *fakeSource) Player() (int, PlayerQoE, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.player == nil {
		return 0, PlayerQoE{}, false
	}
	return *f.player, f.qoe, true
}

type memStream struct {
	id               int64
	start, end, last time.Time
	open             bool
	title            string
	qoeStart, qoeEnd *PlayerQoE
}

type memStore struct {
	streams []*memStream
	samples map[int64][]Sample
	pruned  []time.Time
	// failClose and failRecord make the next N closes or records fail.
	failClose, failRecord int
	// fullReads counts AnalyticsSamples calls; recentTicks the tick counts asked of AnalyticsRecentSamples.
	fullReads   int
	recentTicks []int
	leads       []time.Duration
}

func newMem() *memStore { return &memStore{samples: map[int64][]Sample{}} }

func (m *memStore) OpenAnalyticsStream(at time.Time, q *PlayerQoE, lead time.Duration) (int64, error) {
	m.leads = append(m.leads, lead)
	st := &memStream{id: int64(len(m.streams) + 1), start: at, open: true, qoeStart: q}
	m.streams = append(m.streams, st)
	return st.id, nil
}
func (m *memStore) OpenAnalyticsStreamState() (OpenState, bool, error) {
	for _, s := range m.streams {
		if s.open {
			return OpenState{ID: s.id, Start: s.start, LastAt: s.last}, true, nil
		}
	}
	return OpenState{}, false, nil
}
func (m *memStore) RecordAnalyticsTick(id int64, at time.Time, rs []Reading, title string, q *PlayerQoE) error {
	if m.failRecord > 0 {
		m.failRecord--
		return errors.New("disk full")
	}
	st := m.streams[id-1]
	st.last = at
	if st.title == "" {
		st.title = title
	}
	st.qoeEnd = q
	for _, r := range rs {
		m.samples[id] = append(m.samples[id], Sample{At: at, Platform: r.Platform, Account: r.Account, Viewers: r.Viewers, Reason: r.Reason})
	}
	return nil
}
func (m *memStore) CloseAnalyticsStream(id int64, at time.Time) error {
	if m.failClose > 0 {
		m.failClose--
		return errors.New("database is locked")
	}
	st := m.streams[id-1]
	st.open, st.end = false, at
	return nil
}
func (m *memStore) AnalyticsSamples(id int64) ([]Sample, error) {
	m.fullReads++
	return m.samples[id], nil
}
func (m *memStore) AnalyticsRecentSamples(id int64, ticks int) ([]Sample, error) {
	m.recentTicks = append(m.recentTicks, ticks)
	all := m.samples[id]
	seen := 0
	for i := len(all) - 1; i >= 0; i-- {
		if i == len(all)-1 || !all[i].At.Equal(all[i+1].At) {
			seen++
			if seen > ticks {
				return all[i+1:], nil
			}
		}
	}
	return all, nil
}
func (m *memStore) PruneAnalytics(cutoff time.Time) (int, error) {
	m.pruned = append(m.pruned, cutoff)
	return 0, nil
}
func (m *memStore) ForgetAnalyticsQoE(id int64) error { m.streams[id-1].qoeStart = nil; return nil }

type clock struct{ t time.Time }

func (c *clock) now() time.Time       { return c.t }
func (c *clock) step(d time.Duration) { c.t = c.t.Add(d) }
func enabled(d time.Duration) func() Config {
	return func() Config { return Config{Enabled: true, Interval: d} }
}

func newFake() *fakeSource {
	return &fakeSource{
		stats: map[string]Stats{}, errs: map[string]error{}, deny: map[string]bool{},
		reads: map[string]int{}, allows: map[string]int{},
	}
}

// TestSamplerAsksNothingWhileOffAir: the sampler spends quota only while the
// stream is on air.
func TestSamplerAsksNothingWhileOffAir(t *testing.T) {
	src := newFake()
	src.targets = []Target{{Platform: "youtube", AccountID: 1, Account: "yt"}}
	store := newMem()
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s := New(src, store, enabled(time.Minute), c.now, nil)
	s.Tick(context.Background())
	if src.allows["yt"] != 0 || src.reads["yt"] != 0 || len(store.streams) != 0 {
		t.Fatalf("off air: allows=%d reads=%d streams=%d, want nothing asked and no stream",
			src.allows["yt"], src.reads["yt"], len(store.streams))
	}
}

// TestSamplerRespectsThePacer: a read the quota pacer refuses is not made, and
// the reading says so instead of carrying a stale or zero count.
func TestSamplerRespectsThePacer(t *testing.T) {
	src := newFake()
	src.onAir = true
	src.targets = []Target{{Platform: "youtube", AccountID: 1, Account: "yt"}, {Platform: "twitch", AccountID: 2, Account: "tw"}}
	src.stats["yt"] = Stats{Live: true, Viewers: ip(100)}
	src.stats["tw"] = Stats{Live: true, Viewers: ip(3)}
	src.deny["yt"] = true
	store := newMem()
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s := New(src, store, enabled(time.Minute), c.now, nil)
	s.Tick(context.Background())

	if src.reads["yt"] != 0 {
		t.Fatalf("the pacer refused YouTube and the sampler read it anyway (%d reads)", src.reads["yt"])
	}
	if src.allows["yt"] != 1 || src.reads["tw"] != 1 {
		t.Fatalf("allows=%v reads=%v", src.allows, src.reads)
	}
	l := s.Live()
	if l.Total == nil || *l.Total != 3 || l.Reported != 1 {
		t.Fatalf("live total = %v (%d reported), want 3 from Twitch alone", deref(l.Total), l.Reported)
	}
	var yt Reading
	for _, r := range l.Readings {
		if r.Account == "yt" {
			yt = r
		}
	}
	if yt.Viewers != nil || yt.Reason != ReasonQuota {
		t.Fatalf("the refused reading = %+v, want no count and reason quota", yt)
	}
}

// TestSamplerReadingsMapEveryKindOfSilenceToNotReported.
func TestSamplerReadingsMapEveryKindOfSilenceToNotReported(t *testing.T) {
	src := newFake()
	src.onAir = true
	src.targets = []Target{
		{Platform: "kick", AccountID: 1, Account: "hidden"},
		{Platform: "kick", AccountID: 2, Account: "offline"},
		{Platform: "kick", AccountID: 3, Account: "broken"},
		{Platform: "kick", AccountID: 4, Account: "ok"},
	}
	src.stats["hidden"] = Stats{Live: true, Title: "Friday show"}
	src.stats["offline"] = Stats{Live: false, Viewers: ip(0)}
	src.errs["broken"] = errors.New("502")
	src.stats["ok"] = Stats{Live: true, Viewers: ip(0)}
	store := newMem()
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s := New(src, store, enabled(time.Minute), c.now, nil)
	s.Tick(context.Background())

	want := map[string]string{"hidden": ReasonHidden, "offline": ReasonOffline, "broken": ReasonError, "ok": ""}
	l := s.Live()
	for _, r := range l.Readings {
		if r.Reason != want[r.Account] {
			t.Errorf("%s: reason %q, want %q", r.Account, r.Reason, want[r.Account])
		}
		if (r.Account == "ok") != (r.Viewers != nil) {
			t.Errorf("%s: viewers %v", r.Account, deref(r.Viewers))
		}
	}
	// A real zero is reported; the offline channel's 0 is not counted as one.
	if l.Total == nil || *l.Total != 0 || l.Reported != 1 {
		t.Fatalf("total = %v over %d, want a reported 0 over 1", deref(l.Total), l.Reported)
	}
	if store.streams[0].title != "Friday show" {
		t.Fatalf("stream title = %q", store.streams[0].title)
	}
}

// TestSamplerClosesAStreamOnlyAfterTheGrace and opens a new one after.
func TestSamplerClosesAStreamOnlyAfterTheGrace(t *testing.T) {
	src := newFake()
	src.onAir = true
	store := newMem()
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s := New(src, store, enabled(time.Minute), c.now, nil)
	ctx := context.Background()

	s.Tick(ctx)
	lastOn := c.t
	src.onAir = false
	c.step(time.Minute)
	s.Tick(ctx)
	if !store.streams[0].open {
		t.Fatal("one off-air tick closed the stream; a destination restart would split the report")
	}
	src.onAir = true
	c.step(time.Minute)
	s.Tick(ctx)
	if len(store.streams) != 1 {
		t.Fatalf("a short gap opened a second stream (%d)", len(store.streams))
	}
	lastOn = c.t
	src.onAir = false
	c.step(CloseGrace)
	s.Tick(ctx)
	if store.streams[0].open {
		t.Fatal("the stream outlived the close grace")
	}
	if !store.streams[0].end.Equal(lastOn) {
		t.Fatalf("closed at %v, want the last on-air tick %v", store.streams[0].end, lastOn)
	}
	src.onAir = true
	c.step(time.Minute)
	s.Tick(ctx)
	if len(store.streams) != 2 || !store.streams[1].open {
		t.Fatalf("going live again did not open a new stream: %d", len(store.streams))
	}
}

func TestSamplerResumesAFreshStreamAndClosesAStaleOne(t *testing.T) {
	store := newMem()
	t0 := time.Unix(1_800_000_000, 0)
	id, _ := store.OpenAnalyticsStream(t0, &PlayerQoE{Sessions: 3}, 0)
	_ = store.RecordAnalyticsTick(id, t0, []Reading{{Platform: "kick", Viewers: ip(4)}}, "", nil)

	src := newFake()
	src.onAir = true
	c := &clock{t: t0.Add(time.Minute)}
	s := New(src, store, enabled(time.Minute), c.now, nil)
	s.Tick(context.Background())
	if len(store.streams) != 1 || !store.streams[0].open {
		t.Fatalf("a restart inside the grace started a new stream (%d)", len(store.streams))
	}
	if store.streams[0].qoeStart != nil {
		t.Fatal("the player baseline from before the restart was kept; the delta across it would be invented")
	}
	if pts := s.Live().Points; len(pts) != 2 {
		t.Fatalf("resumed sparkline has %d points, want the stored one plus this tick", len(pts))
	}

	// A process that comes back long after: the old stream ends at its last tick.
	store2 := newMem()
	id2, _ := store2.OpenAnalyticsStream(t0, nil, 0)
	_ = store2.RecordAnalyticsTick(id2, t0.Add(time.Minute), nil, "", nil)
	c2 := &clock{t: t0.Add(time.Hour)}
	s2 := New(src, store2, enabled(time.Minute), c2.now, nil)
	s2.Tick(context.Background())
	if store2.streams[0].open || !store2.streams[0].end.Equal(t0.Add(time.Minute)) {
		t.Fatalf("stale stream: open=%v end=%v, want closed at its last tick", store2.streams[0].open, store2.streams[0].end)
	}
	if len(store2.streams) != 2 {
		t.Fatal("on air after the stale stream closed, but no new stream opened")
	}
}

// TestSamplerPrunesByRetentionHourly: the cutoff is now minus the retention,
// and the prune runs at most once an hour.
func TestSamplerPrunesByRetentionHourly(t *testing.T) {
	src := newFake()
	store := newMem()
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	ret := 7 * 24 * time.Hour
	s := New(src, store, func() Config { return Config{Enabled: true, Interval: time.Minute, Retention: ret} }, c.now, nil)
	s.Tick(context.Background())
	c.step(time.Minute)
	s.Tick(context.Background())
	if len(store.pruned) != 1 {
		t.Fatalf("pruned %d times in two minutes, want once", len(store.pruned))
	}
	if want := time.Unix(1_800_000_000, 0).Add(-ret); !store.pruned[0].Equal(want) {
		t.Fatalf("cutoff = %v, want %v", store.pruned[0], want)
	}
	c.step(time.Hour)
	s.Tick(context.Background())
	if len(store.pruned) != 2 {
		t.Fatalf("pruned %d times after an hour, want twice", len(store.pruned))
	}
}

func TestSamplerAddsThePlayerAsItsOwnRowAndReportsItsQoEDelta(t *testing.T) {
	src := newFake()
	src.onAir = true
	src.targets = []Target{{Platform: "twitch", AccountID: 1, Account: "tw"}}
	src.stats["tw"] = Stats{Live: true, Viewers: ip(10)}
	src.player = ip(4)
	src.qoe = PlayerQoE{Sessions: 2}
	store := newMem()
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s := New(src, store, enabled(time.Minute), c.now, nil)
	s.Tick(context.Background())
	src.qoe = PlayerQoE{Sessions: 5, Rebuffers: 1}
	c.step(time.Minute)
	s.Tick(context.Background())

	l := s.Live()
	if l.Total == nil || *l.Total != 14 || l.Reported != 2 {
		t.Fatalf("total = %v over %d, want 14 (10 Twitch + 4 player, each once)", deref(l.Total), l.Reported)
	}
	if l.Player == nil || l.Player.Sessions != 3 || l.Player.Rebuffers != 1 {
		t.Fatalf("player QoE = %+v, want the delta since the stream began", l.Player)
	}
}

func TestDisabledSamplerAsksNothingAndClosesTheOpenStream(t *testing.T) {
	src := newFake()
	src.onAir = true
	src.targets = []Target{{Platform: "twitch", AccountID: 1, Account: "tw"}}
	store := newMem()
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	on := true
	s := New(src, store, func() Config { return Config{Enabled: on, Interval: time.Minute} }, c.now, nil)
	s.Tick(context.Background())
	on = false
	c.step(time.Minute)
	s.Tick(context.Background())
	if src.reads["tw"] != 1 || store.streams[0].open {
		t.Fatalf("disabled: reads=%d open=%v, want one read before and the stream closed", src.reads["tw"], store.streams[0].open)
	}
	if s.Live().Enabled {
		t.Fatal("the live view still says enabled")
	}
}

// TestAverageTotalSkipsTicksAtWhichNothingReported: a tick where every
// platform was silent is a gap, not a tick with an audience of 0, so it does
// not pull the average down.
func TestAverageTotalSkipsTicksAtWhichNothingReported(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	s := Summarise([]Sample{
		{At: t0, Platform: "kick", Viewers: ip(10)},
		{At: t0.Add(time.Minute), Platform: "kick"},
		{At: t0.Add(2 * time.Minute), Platform: "kick", Viewers: ip(20)},
	})
	if s.AverageTotal == nil || *s.AverageTotal != 15 {
		t.Fatalf("average total = %v, want 15 (the silent tick left out)", s.AverageTotal)
	}
	if s.Rows[0].Average == nil || *s.Rows[0].Average != 15 {
		t.Fatalf("row average = %v, want 15", s.Rows[0].Average)
	}
}

// TestLiveBeforeTheFirstTickSaysWhetherSamplingIsOn: the picture served
// before the loop's first tick must not read as switched off.
func TestLiveBeforeTheFirstTickSaysWhetherSamplingIsOn(t *testing.T) {
	s := New(newFake(), newMem(), enabled(2*time.Minute), nil, nil)
	l := s.Live()
	if !l.Enabled || l.OnAir || l.IntervalSeconds != 120 || l.At != nil {
		t.Fatalf("before the first tick = %+v, want enabled, off air, interval 120, no tick", l)
	}
}

// ---------------------------------------------------------- review round

// TestAPartialTickIsAGapNotADip (#6): a tick at which a platform failed --
// offline while its destination runs, refused by the quota pacer, or an
// error -- is not an audience that dropped. It is left out of the peak and
// the average, and the sparkline has a gap there.
func TestAPartialTickIsAGapNotADip(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	s := Summarise([]Sample{
		{At: t0, Platform: "youtube", Viewers: ip(100)},
		{At: t0, Platform: "twitch", Viewers: ip(10)},
		{At: t0.Add(time.Minute), Platform: "youtube", Reason: ReasonQuota},
		{At: t0.Add(time.Minute), Platform: "twitch", Viewers: ip(12)},
		{At: t0.Add(2 * time.Minute), Platform: "youtube", Viewers: ip(80)},
		{At: t0.Add(2 * time.Minute), Platform: "twitch", Viewers: ip(14)},
	})
	if s.AverageTotal == nil || *s.AverageTotal != 102 {
		t.Fatalf("average total = %v, want 102 ((110+94)/2, the partial tick left out)", s.AverageTotal)
	}
	if p := s.Points[1]; p.Total != nil || !p.Partial {
		t.Fatalf("partial tick point = %+v, want a gap marked partial", p)
	}
	// A hidden count is a steady fact, not a failure: that tick is complete.
	h := Summarise([]Sample{
		{At: t0, Platform: "youtube", Reason: ReasonHidden},
		{At: t0, Platform: "twitch", Viewers: ip(5)},
	})
	if h.Points[0].Partial || h.Points[0].Total == nil || *h.Points[0].Total != 5 {
		t.Fatalf("hidden-count tick = %+v, want complete with total 5", h.Points[0])
	}
	// The per-platform rows still count the platforms that reported.
	if tw := s.Rows[0]; tw.Platform != "twitch" || *tw.Peak != 14 || tw.Reported != 3 {
		t.Fatalf("twitch row = %+v", tw)
	}
}

func TestLiveSaysAPartialTickIsPartial(t *testing.T) {
	src := newFake()
	src.onAir = true
	src.targets = []Target{{Platform: "youtube", AccountID: 1, Account: "yt"}, {Platform: "twitch", AccountID: 2, Account: "tw"}}
	src.errs["yt"] = ErrQuota
	src.stats["tw"] = Stats{Live: true, Viewers: ip(3)}
	s := New(src, newMem(), enabled(time.Minute), (&clock{t: time.Unix(1_800_000_000, 0)}).now, nil)
	s.Tick(context.Background())
	l := s.Live()
	if !l.Partial || l.Readings[1].Reason != ReasonQuota {
		t.Fatalf("live = %+v, want partial with YouTube refused by the quota", l)
	}
	if len(l.Points) != 1 || l.Points[0].Total != nil {
		t.Fatalf("points = %+v, want a gap", l.Points)
	}
}

// TestAFailedCloseIsRetried (#10): a close that fails leaves the stream open
// in memory too, so the next tick tries again instead of orphaning the row.
func TestAFailedCloseIsRetried(t *testing.T) {
	src := newFake()
	src.onAir = true
	store := newMem()
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s := New(src, store, enabled(time.Minute), c.now, nil)
	s.Tick(context.Background())
	src.onAir = false
	store.failClose = 1
	c.step(CloseGrace)
	s.Tick(context.Background())
	if !store.streams[0].open {
		t.Fatal("the store reports the close failed, yet nothing is open")
	}
	c.step(time.Minute)
	s.Tick(context.Background())
	if store.streams[0].open {
		t.Fatal("a failed close was not retried")
	}
	src.onAir = true
	c.step(time.Minute)
	s.Tick(context.Background())
	if len(store.streams) != 2 {
		t.Fatalf("%d streams, want the retried close then one new stream", len(store.streams))
	}
}

// TestAnOpenStreamRollsOverEveryDay (#11): a 24/7 broadcast is split into
// day-long streams, so retention can prune it.
func TestAnOpenStreamRollsOverEveryDay(t *testing.T) {
	src := newFake()
	src.onAir = true
	store := newMem()
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s := New(src, store, enabled(time.Minute), c.now, nil)
	s.Tick(context.Background())
	c.step(RolloverAfter - time.Minute)
	s.Tick(context.Background())
	last := c.t
	if len(store.streams) != 1 {
		t.Fatalf("rolled over early: %d streams", len(store.streams))
	}
	c.step(time.Minute)
	s.Tick(context.Background())
	if len(store.streams) != 2 || store.streams[0].open || !store.streams[0].end.Equal(last) || !store.streams[1].open {
		t.Fatalf("after a day: %d streams, first open=%v end=%v", len(store.streams), store.streams[0].open, store.streams[0].end)
	}
}

// TestResumeLoadsOnlyTheRecentTicks (#12): resuming reads the last sparkline's
// worth of ticks, never the whole stream.
func TestResumeLoadsOnlyTheRecentTicks(t *testing.T) {
	store := newMem()
	t0 := time.Unix(1_800_000_000, 0)
	id, _ := store.OpenAnalyticsStream(t0, nil, 0)
	for i := 0; i < 300; i++ {
		_ = store.RecordAnalyticsTick(id, t0.Add(time.Duration(i)*time.Minute), []Reading{{Platform: "kick", Viewers: ip(i)}}, "", nil)
	}
	src := newFake()
	src.onAir = true
	c := &clock{t: t0.Add(300 * time.Minute)}
	s := New(src, store, enabled(time.Minute), c.now, nil)
	s.Tick(context.Background())
	if store.fullReads != 0 {
		t.Fatalf("resume read the whole stream %d times", store.fullReads)
	}
	if len(store.recentTicks) != 1 || store.recentTicks[0] != sparkPoints {
		t.Fatalf("recent reads = %v, want one of %d ticks", store.recentTicks, sparkPoints)
	}
	if n := len(s.Live().Points); n != sparkPoints {
		t.Fatalf("sparkline has %d points, want %d", n, sparkPoints)
	}
}

// TestAFailedRecordDoesNotAdvanceTheStream (#13): memory follows the store.
func TestAFailedRecordDoesNotAdvanceTheStream(t *testing.T) {
	src := newFake()
	src.onAir = true
	store := newMem()
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s := New(src, store, enabled(time.Minute), c.now, nil)
	s.Tick(context.Background())
	first := c.t
	store.failRecord = 1
	c.step(time.Minute)
	s.Tick(context.Background())
	if n := len(s.Live().Points); n != 1 {
		t.Fatalf("%d points after a failed write, want 1", n)
	}
	// Off air now: the grace counts from the last tick that was stored.
	src.onAir = false
	c.t = first.Add(CloseGrace)
	s.Tick(context.Background())
	if store.streams[0].open || !store.streams[0].end.Equal(first) {
		t.Fatalf("closed=%v at %v, want closed at the last stored tick %v", !store.streams[0].open, store.streams[0].end, first)
	}
}

// TestABackedOffTargetIsNotAskedUntilItsRetryAfter (#4).
func TestABackedOffTargetIsNotAskedUntilItsRetryAfter(t *testing.T) {
	src := newFake()
	src.onAir = true
	src.targets = []Target{{Platform: "twitch", AccountID: 1, Account: "tw"}}
	src.errs["tw"] = &RetryAfterError{After: 3 * time.Minute}
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s := New(src, newMem(), enabled(time.Minute), c.now, nil)
	s.Tick(context.Background())
	delete(src.errs, "tw")
	src.stats["tw"] = Stats{Live: true, Viewers: ip(4)}
	for i := 0; i < 2; i++ {
		c.step(time.Minute)
		s.Tick(context.Background())
	}
	if src.reads["tw"] != 1 {
		t.Fatalf("read %d times inside the platform's Retry-After, want 1", src.reads["tw"])
	}
	if r := s.Live().Readings[0]; r.Reason != ReasonError || r.Viewers != nil {
		t.Fatalf("backed-off reading = %+v", r)
	}
	c.step(time.Minute)
	s.Tick(context.Background())
	if src.reads["tw"] != 2 || s.Live().Total == nil {
		t.Fatalf("after the Retry-After: reads=%d total=%v", src.reads["tw"], s.Live().Total)
	}
}

// An error with no Retry-After backs off exponentially, capped.
func TestAnErroringTargetBacksOff(t *testing.T) {
	if d := backoffFor(1, nil); d != time.Duration(0) {
		t.Fatalf("first failure backoff = %v, want none (ask again next tick)", d)
	}
	if d := backoffFor(3, nil); d != 2*time.Minute {
		t.Fatalf("third failure backoff = %v, want 2m", d)
	}
	if d := backoffFor(30, nil); d != maxBackoff {
		t.Fatalf("backoff = %v, want capped at %v", d, maxBackoff)
	}
	if d := backoffFor(1, &RetryAfterError{After: 7 * time.Minute}); d != 7*time.Minute {
		t.Fatalf("Retry-After backoff = %v", d)
	}
}

// TestAnUnlinkedDestinationIsShownNotAsked (owner question 2): a running
// destination with a pasted key has no account to ask; it is a row that says
// so, never a read, and does not make the tick partial.
func TestAnUnlinkedDestinationIsShownNotAsked(t *testing.T) {
	src := newFake()
	src.onAir = true
	src.targets = []Target{{Platform: "kick", Name: "Kick (pasted key)", Unlinked: true}, {Platform: "twitch", AccountID: 2, Account: "tw"}}
	src.stats["tw"] = Stats{Live: true, Viewers: ip(6)}
	s := New(src, newMem(), enabled(time.Minute), (&clock{t: time.Unix(1_800_000_000, 0)}).now, nil)
	s.Tick(context.Background())
	l := s.Live()
	if src.allows[""]+src.reads[""] != 0 {
		t.Fatal("an unlinked destination was asked")
	}
	if l.Readings[0].Reason != ReasonUnlinked || l.Partial || l.Total == nil || *l.Total != 6 {
		t.Fatalf("live = %+v", l)
	}
}

// TestAStretchedIntervalIsReported (#2): when the source paces the interval
// for quota, the live view says so and the wait uses it.
func TestAStretchedIntervalIsReported(t *testing.T) {
	src := newFake()
	src.onAir = true
	src.interval = func(time.Duration) time.Duration { return 3 * time.Minute }
	s := New(src, newMem(), enabled(time.Minute), (&clock{t: time.Unix(1_800_000_000, 0)}).now, nil)
	s.Tick(context.Background())
	l := s.Live()
	if l.IntervalSeconds != 60 || l.EffectiveIntervalSeconds != 180 || !l.Stretched {
		t.Fatalf("live = interval %d effective %d stretched %v", l.IntervalSeconds, l.EffectiveIntervalSeconds, l.Stretched)
	}
	if w := s.nextWait(); w != 3*time.Minute {
		t.Fatalf("next wait = %v, want the stretched 3m", w)
	}
}

// TestWakeAppliesALoweredIntervalNow (#15).
func TestWakeAppliesALoweredIntervalNow(t *testing.T) {
	src := newFake()
	interval := time.Hour
	var mu sync.Mutex
	cfg := func() Config { mu.Lock(); defer mu.Unlock(); return Config{Enabled: true, Interval: interval} }
	store := newMem()
	s := New(src, store, cfg, nil, nil)
	s.firstWait = time.Millisecond
	ticks := make(chan struct{}, 10)
	s.onTick = func() { ticks <- struct{}{} }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	<-ticks
	mu.Lock()
	interval = 20 * time.Millisecond
	mu.Unlock()
	s.Wake()
	select {
	case <-ticks:
	case <-time.After(2 * time.Second):
		t.Fatal("a lowered interval waited out the old hour")
	}
}

func TestOpenPassesTheChatLeadOfOneInterval(t *testing.T) {
	src := newFake()
	src.onAir = true
	store := newMem()
	s := New(src, store, enabled(90*time.Second), (&clock{t: time.Unix(1_800_000_000, 0)}).now, nil)
	s.Tick(context.Background())
	if len(store.leads) != 1 || store.leads[0] != 90*time.Second {
		t.Fatalf("leads = %v, want one interval (chat between go-live and the first tick)", store.leads)
	}
}

func TestCSVNeutralisesAFormulaBehindWhitespace(t *testing.T) {
	for _, in := range []string{" =1+1", "\n@SUM(A1)", "\t\t-2+3", "  +cmd"} {
		if got := neutralise(in); got[0] != '\'' {
			t.Errorf("neutralise(%q) = %q, want it marked as text", in, got)
		}
	}
	for _, in := range []string{"plain", "a=b", "Twitch 4"} {
		if got := neutralise(in); got != in {
			t.Errorf("neutralise(%q) = %q, want it unchanged", in, got)
		}
	}
}
