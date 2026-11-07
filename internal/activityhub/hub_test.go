package activityhub

import (
	"errors"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
)

var t0 = time.Date(2026, 11, 7, 20, 0, 0, 0, time.UTC)

type fakeBus struct {
	mu  sync.Mutex
	got []activity.Event
	typ []events.Type
}

func (b *fakeBus) Publish(t events.Type, data any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.typ = append(b.typ, t)
	b.got = append(b.got, data.(activity.Event))
}

func (b *fakeBus) events() []activity.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]activity.Event(nil), b.got...)
}

type fakeDisplay struct {
	mu  sync.Mutex
	got []activity.Event
}

func (d *fakeDisplay) Offer(ev activity.Event) {
	d.mu.Lock()
	d.got = append(d.got, ev)
	d.mu.Unlock()
}

func (d *fakeDisplay) events() []activity.Event {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]activity.Event(nil), d.got...)
}

type fakeHooks struct {
	mu  sync.Mutex
	got []activity.Event
}

func (k *fakeHooks) Activity(ev activity.Event) {
	k.mu.Lock()
	k.got = append(k.got, ev)
	k.mu.Unlock()
}

type rig struct {
	hub *Hub
	bus *fakeBus
	dsp *fakeDisplay
	hk  *fakeHooks
	set atomic.Pointer[db.ActivitySettings]
	rd  atomic.Int64
	// failNext, when set, makes the next settings read fail; slow, when set, is
	// closed-over by a read that waits for it.
	failNext atomic.Bool
	hold     atomic.Pointer[chan struct{}]
	now      atomic.Pointer[time.Time]
}

func newRig(t *testing.T, opts ...Option) *rig {
	t.Helper()
	r := &rig{bus: &fakeBus{}, dsp: &fakeDisplay{}, hk: &fakeHooks{}}
	d := db.DefaultActivitySettings()
	r.set.Store(&d)
	n := t0
	r.now.Store(&n)
	base := []Option{
		WithBus(r.bus), WithDisplay(r.dsp), WithHooks(r.hk),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithSettings(func() (db.ActivitySettings, error) { r.rd.Add(1); return *r.set.Load(), r.loadErr() }),
		WithClock(func() time.Time { return *r.now.Load() }),
		WithBackoff(func(int) time.Duration { return time.Millisecond }),
	}
	r.hub = New(append(base, opts...)...)
	t.Cleanup(r.hub.Close)
	return r
}

func (r *rig) settings(f func(*db.ActivitySettings)) {
	s := *r.set.Load()
	f(&s)
	r.set.Store(&s)
	r.hub.InvalidateSettings()
}

func (r *rig) advance(d time.Duration) {
	n := r.now.Load().Add(d)
	r.now.Store(&n)
}

func real(kind activity.Kind, platform, id string) activity.Event {
	e := activity.Sample(kind, platform, activity.Overrides{}, t0)
	e.Test = false
	e.Source = activity.Source{Type: "channel.x", ID: id}
	return e
}

func TestSubmitFansOutToBusHooksAndDisplayAndRemembers(t *testing.T) {
	r := newRig(t)
	res := r.hub.Submit(real(activity.KindFollow, "twitch", "1"))
	if !res.Accepted || res.Reason != "" || res.Event.ID == "" {
		t.Fatalf("result: %+v", res)
	}
	if got := r.bus.events(); len(got) != 1 || got[0].ID != res.Event.ID || r.bus.typ[0] != events.TypeActivity {
		t.Errorf("bus: %+v %v", got, r.bus.typ)
	}
	if len(r.hk.got) != 1 || len(r.dsp.events()) != 1 {
		t.Errorf("hooks %d display %d", len(r.hk.got), len(r.dsp.events()))
	}
	if rec := r.hub.Recent(10, "", ""); len(rec) != 1 || rec[0].ID != res.Event.ID {
		t.Errorf("ring: %+v", rec)
	}
	if st := r.hub.Stats(); st.Received != 1 || st.Delivered != 1 {
		t.Errorf("stats %+v", st)
	}
}

func TestIngestIsSubmitWithoutAnAnswer(t *testing.T) {
	r := newRig(t)
	r.hub.Ingest(real(activity.KindFollow, "twitch", "1"))
	if len(r.bus.events()) != 1 {
		t.Fatal("Ingest did not deliver")
	}
}

func TestAnInvalidEventIsRefusedAndNeverFansOut(t *testing.T) {
	r := newRig(t)
	bad := real(activity.KindTip, "twitch", "1")
	bad.Amount = nil
	res := r.hub.Submit(bad)
	if res.Accepted || res.Reason != ReasonInvalid || res.Detail == "" {
		t.Fatalf("result: %+v", res)
	}
	if len(r.bus.events())+len(r.hk.got)+len(r.dsp.events()) != 0 || r.hub.Stats().Invalid != 1 {
		t.Error("an invalid event reached a consumer")
	}
}

func TestDedupeAcrossRedeliveryAndTestEventsBypassIt(t *testing.T) {
	r := newRig(t)
	if !r.hub.Submit(real(activity.KindFollow, "twitch", "dup")).Accepted {
		t.Fatal("first refused")
	}
	again := r.hub.Submit(real(activity.KindFollow, "twitch", "dup"))
	if again.Accepted || again.Reason != ReasonDuplicate {
		t.Fatalf("redelivery: %+v", again)
	}
	other := real(activity.KindFollow, "kick", "dup")
	if !r.hub.Submit(other).Accepted {
		t.Error("the same id on another platform is another fact")
	}
	// A test event is never a duplicate, even with an identical key.
	te := activity.Sample(activity.KindFollow, "twitch", activity.Overrides{}, t0)
	te.Source.ID = "same"
	if !r.hub.Submit(te).Accepted || !r.hub.Submit(te).Accepted {
		t.Error("a test event was deduped")
	}
	if r.hub.Stats().Deduped != 1 {
		t.Errorf("deduped = %d", r.hub.Stats().Deduped)
	}
}

func TestDedupeSetIsBoundedAndEvictsOldestFirst(t *testing.T) {
	r := newRig(t, WithDedupe(3))
	for i := 1; i <= 4; i++ {
		r.hub.Submit(real(activity.KindFollow, "twitch", strconv.Itoa(i)))
	}
	// 1 was evicted by 4: it is accepted again. 4 is still remembered.
	if !r.hub.Submit(real(activity.KindFollow, "twitch", "1")).Accepted {
		t.Error("the oldest key should have been evicted")
	}
	if r.hub.Submit(real(activity.KindFollow, "twitch", "4")).Accepted {
		t.Error("a recent key was evicted")
	}
	r.hub.mu.Lock()
	n, m := len(r.hub.seen), len(r.hub.order)
	r.hub.mu.Unlock()
	if n > 3 || m > 3 {
		t.Errorf("seen %d order %d exceed the cap", n, m)
	}
}

func TestSettingsFilterDropsDisabledKindsPlatformsAndTheMasterSwitch(t *testing.T) {
	r := newRig(t)
	r.settings(func(s *db.ActivitySettings) {
		s.Kinds = map[string][]string{"kick": {"follow"}, "youtube": {}}
	})
	if res := r.hub.Submit(real(activity.KindFollow, "kick", "1")); !res.Accepted {
		t.Errorf("an enabled kind was refused: %+v", res)
	}
	for name, ev := range map[string]activity.Event{
		"kind not listed":     real(activity.KindRaid, "kick", "2"),
		"platform emptied":    real(activity.KindFollow, "youtube", "3"),
		"reaction by default": real(activity.KindReaction, "facebook", "4"),
	} {
		if res := r.hub.Submit(ev); res.Accepted || res.Reason != ReasonDisabled || res.Detail == "" {
			t.Errorf("%s: %+v", name, res)
		}
	}
	r.settings(func(s *db.ActivitySettings) { s.Reactions = true })
	if !r.hub.Submit(real(activity.KindReaction, "facebook", "5")).Accepted {
		t.Error("reactions on, still refused")
	}
	r.settings(func(s *db.ActivitySettings) { s.Enabled = false })
	res := r.hub.Submit(real(activity.KindFollow, "twitch", "6"))
	if res.Accepted || res.Reason != ReasonDisabled {
		t.Errorf("master switch: %+v", res)
	}
	if r.hub.Stats().Disabled != 4 {
		t.Errorf("disabled = %d", r.hub.Stats().Disabled)
	}
}

func TestMinimumsApplyToTheOverlayOnly(t *testing.T) {
	r := newRig(t)
	r.settings(func(s *db.ActivitySettings) {
		s.MinTip = []db.MinAmount{{Currency: "USD", Minor: 1000}, {Unit: "bits", Units: 100}}
	})
	small := real(activity.KindTip, "twitch", "1") // USD 5.00 sample
	bits := real(activity.KindTip, "twitch", "2")
	bits.Amount = &activity.Amount{Unit: "bits", Units: 500}
	yen := real(activity.KindTip, "twitch", "3")
	yen.Amount = &activity.Amount{Currency: "JPY", Minor: 1}
	for _, e := range []activity.Event{small, bits, yen} {
		if !r.hub.Submit(e).Accepted {
			t.Fatal("refused")
		}
	}
	if len(r.bus.events()) != 3 || len(r.hk.got) != 3 {
		t.Errorf("the console and hooks must see every tip: bus %d hooks %d", len(r.bus.events()), len(r.hk.got))
	}
	shown := r.dsp.events()
	if len(shown) != 2 {
		t.Fatalf("overlay got %d tips, want the 500 bits and the unconverted yen", len(shown))
	}
	for _, e := range shown {
		if e.Amount.Unit == "" && e.Amount.Currency == "USD" {
			t.Error("a below-minimum tip reached the overlay")
		}
	}
}

func TestOnlyOverlayKindsAreOffered(t *testing.T) {
	r := newRig(t)
	for i, k := range activity.AllKinds() {
		r.hub.Submit(real(k, "twitch", strconv.Itoa(i)))
	}
	if got, want := len(r.dsp.events()), len(activity.OverlayKinds()); got != want {
		t.Errorf("overlay offered %d, want %d", got, want)
	}
	for _, e := range r.dsp.events() {
		if !e.OverlayVisible() {
			t.Errorf("offered %s", e.Kind)
		}
	}
	out := real(activity.KindRaid, "twitch", "out")
	out.Direction = "out"
	n := len(r.dsp.events())
	r.hub.Submit(out)
	if len(r.dsp.events()) != n {
		t.Error("a raid out reached the overlay")
	}
}

func TestOverlayMessagesOffStripsTheTextFromTheOverlayOnly(t *testing.T) {
	r := newRig(t)
	r.settings(func(s *db.ActivitySettings) { s.OverlayMessages = false })
	r.hub.Submit(real(activity.KindTip, "twitch", "1"))
	if m := r.dsp.events()[0].Message; m != "" {
		t.Errorf("overlay got message %q", m)
	}
	if m := r.bus.events()[0].Message; m == "" {
		t.Error("the console lost the message")
	}
}

func TestRingIsCappedAndRecentFiltersNewestFirst(t *testing.T) {
	r := newRig(t, WithRing(5))
	for i := 1; i <= 8; i++ {
		p := "twitch"
		k := activity.KindFollow
		if i%2 == 0 {
			p, k = "kick", activity.KindRaid
		}
		r.hub.Submit(real(k, p, strconv.Itoa(i)))
	}
	all := r.hub.Recent(0, "", "")
	if len(all) != 5 || all[0].Source.ID != "8" || all[4].Source.ID != "4" {
		t.Fatalf("ring: %v", ids(all))
	}
	if got := r.hub.Recent(2, "", ""); len(got) != 2 || got[0].Source.ID != "8" {
		t.Errorf("limit: %v", ids(got))
	}
	if got := r.hub.Recent(100, "kick", ""); len(got) != 3 {
		t.Errorf("platform filter: %v", ids(got))
	}
	if got := r.hub.Recent(100, "", activity.KindFollow); len(got) != 2 {
		t.Errorf("kind filter: %v", ids(got))
	}
	if got := r.hub.Recent(100, "kick", activity.KindFollow); len(got) != 0 {
		t.Errorf("both filters: %v", ids(got))
	}
	all[0].Message = "mutated"
	if r.hub.Recent(1, "", "")[0].Message == "mutated" {
		t.Error("Recent exposed the ring")
	}
}

func ids(es []activity.Event) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Source.ID)
	}
	return out
}

func TestReplayReoffersOnlyWhatTheOverlayCanDraw(t *testing.T) {
	r := newRig(t)
	r.settings(func(s *db.ActivitySettings) { s.MinTip = []db.MinAmount{{Currency: "USD", Minor: 100000}} })
	tip := r.hub.Submit(real(activity.KindTip, "twitch", "1")).Event
	if len(r.dsp.events()) != 0 {
		t.Fatal("below minimum was offered")
	}
	if !r.hub.Replay(tip.ID) {
		t.Fatal("replay refused")
	}
	if len(r.dsp.events()) != 1 {
		t.Error("an operator's replay is not subject to the minimum")
	}
	mod := r.hub.Submit(real(activity.KindModeration, "twitch", "2")).Event
	if r.hub.Replay(mod.ID) {
		t.Error("a moderation event was replayed to the overlay")
	}
	if r.hub.Replay("act_nope") {
		t.Error("an unknown id replayed")
	}
	r.settings(func(s *db.ActivitySettings) { s.OverlayMessages = false })
	r.hub.Replay(tip.ID)
	if got := r.dsp.events(); got[len(got)-1].Message != "" {
		t.Error("a replay carried the message with overlay messages off")
	}
}

func TestSettingsAreCachedUntilTheTTLOrInvalidated(t *testing.T) {
	r := newRig(t)
	r.hub.Submit(real(activity.KindFollow, "twitch", "1"))
	r.hub.Submit(real(activity.KindFollow, "twitch", "2"))
	if n := r.rd.Load(); n != 1 {
		t.Errorf("settings read %d times for two events", n)
	}
	r.advance(settingsTTL + time.Millisecond)
	r.hub.Submit(real(activity.KindFollow, "twitch", "3"))
	if n := r.rd.Load(); n != 2 {
		t.Errorf("settings not re-read after the TTL: %d", n)
	}
	r.hub.InvalidateSettings()
	if !r.hub.Settings().Enabled || r.rd.Load() != 3 {
		t.Error("InvalidateSettings did not force a read")
	}
}

func TestNewWithNoOptionsAcceptsAndFansOutNowhere(t *testing.T) {
	h := New()
	defer h.Close()
	if !h.Submit(real(activity.KindFollow, "twitch", "1")).Accepted {
		t.Fatal("a bare hub refused an event")
	}
	if h.Replay("x") {
		t.Error("replay with no display")
	}
}

func TestNilHubIsSafe(t *testing.T) {
	var h *Hub
	if h.Submit(activity.Event{}).Accepted {
		t.Error("nil hub accepted")
	}
	h.Ingest(activity.Event{})
	h.InvalidateSettings()
	h.Close()
	h.Detach("a", "b")
	h.AddStatusProvider(func() []SourceStatus { return nil })
	if h.Recent(1, "", "") != nil || h.Replay("x") || h.Stats() != (Stats{}) || len(h.Statuses()) != 0 {
		t.Error("nil hub returned data")
	}
	if err := h.Attach(nil, nil); err == nil {
		t.Error("nil attach")
	}
}

func TestConcurrentSubmitIsRaceFree(t *testing.T) {
	r := newRig(t)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				r.hub.Submit(real(activity.KindFollow, "twitch", strconv.Itoa(g*1000+i)))
				r.hub.Recent(10, "", "")
				r.hub.Statuses()
			}
		}()
	}
	wg.Wait()
	if r.hub.Stats().Delivered != 400 {
		t.Errorf("delivered %d", r.hub.Stats().Delivered)
	}
}

func (r *rig) loadErr() error {
	if r.failNext.Swap(false) {
		return errors.New("database is locked")
	}
	if c := r.hold.Load(); c != nil {
		<-*c
	}
	return nil
}

// A failed read keeps the LAST GOOD settings: a hiccup must not turn a
// switched-off activity back on.
func TestAFailedSettingsReadKeepsTheLastGoodSettings(t *testing.T) {
	r := newRig(t)
	r.settings(func(s *db.ActivitySettings) { s.Enabled = false })
	if r.hub.Submit(real(activity.KindFollow, "twitch", "1")).Accepted {
		t.Fatal("disabled activity delivered")
	}
	r.advance(settingsTTL + time.Second)
	r.failNext.Store(true)
	if res := r.hub.Submit(real(activity.KindFollow, "twitch", "2")); res.Accepted {
		t.Errorf("a failed read turned activity back on: %+v", res)
	}
	// And the next read (after the TTL) recovers.
	r.settings(func(s *db.ActivitySettings) { s.Enabled = true })
	if !r.hub.Submit(real(activity.KindFollow, "twitch", "3")).Accepted {
		t.Error("never recovered")
	}
}

// A read that began before InvalidateSettings must not overwrite the cache with
// what it read: it may predate the save that invalidated it.
func TestAStaleSettingsLoadNeverOverwritesANewerOne(t *testing.T) {
	r := newRig(t)
	r.hub.InvalidateSettings()
	hold := make(chan struct{})
	r.hold.Store(&hold)
	old := *r.set.Load() // enabled, as the slow read will return it
	r.advance(settingsTTL + time.Second)
	done := make(chan bool, 1)
	go func() { done <- r.hub.Submit(real(activity.KindFollow, "twitch", "slow")).Accepted }()
	for r.rd.Load() < 1 {
		time.Sleep(time.Millisecond)
	}
	// A save lands while that read is in flight: the stored document is now off.
	off := old
	off.Enabled = false
	r.set.Store(&off)
	r.hub.InvalidateSettings()
	r.hold.Store(nil)
	close(hold)
	<-done
	if r.hub.Settings().Enabled {
		t.Error("the stale load overwrote the newer settings")
	}
	if r.hub.Submit(real(activity.KindFollow, "twitch", "after")).Accepted {
		t.Error("an event after the save obeyed the old settings")
	}
}
