package captions

import (
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// fakeClock is a manual clock with timers. Advance moves time forward and fires
// every timer that is due, in order, so a test can say "29 seconds later" and
// "one second later" and see exactly which destination has been reached.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	due     time.Time
	f       func()
	stopped bool
	fired   bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{due: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if t.fired || t.stopped {
			return false
		}
		t.stopped = true
		return true
	}
}

// Advance moves the clock forward by d and runs each timer that is now due.
// Callbacks run without the clock's lock held, because they call back into the
// router, which may schedule more timers.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var due []*fakeTimer
	for _, t := range c.timers {
		if !t.fired && !t.stopped && !t.due.After(now) {
			t.fired = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	sort.SliceStable(due, func(i, j int) bool { return due[i].due.Before(due[j].due) })
	for _, t := range due {
		t.f()
	}
}

// recordingSink keeps every delivery with the destination it reached and the
// clock time it arrived, so tests can assert timing as well as content.
type recordingSink struct {
	mu    sync.Mutex
	clock *fakeClock
	got   []delivery
}

type delivery struct {
	dest int64
	text string
	at   time.Time
}

func (s *recordingSink) Deliver(dest int64, c transcribe.LiveCaption) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, delivery{dest: dest, text: c.Text, at: s.clock.Now()})
}

func (s *recordingSink) count(dest int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, d := range s.got {
		if d.dest == dest {
			n++
		}
	}
	return n
}

// TestCueReachesAnUndelayedDestinationAtOnceAndADelayedOneOnlyWhenItsDelayPasses
// is the #11 rule for captions. A destination with no delay gets the caption as
// it is spoken, while a destination that runs 30 s behind must not see the same
// caption until 30 s have passed, or the words land ahead of the picture there.
func TestCueReachesAnUndelayedDestinationAtOnceAndADelayedOneOnlyWhenItsDelayPasses(t *testing.T) {
	clock := newFakeClock()
	sink := &recordingSink{clock: clock}
	r := NewCueRouter(sink, clock.After, clock.Now)
	r.SetTargets([]CueTarget{{ID: 1, Delay: 0}, {ID: 2, Delay: 30 * time.Second}})

	r.Emit(transcribe.LiveCaption{Segment: transcribe.Segment{Text: "hello"}, At: clock.Now()})

	if n := sink.count(1); n != 1 {
		t.Fatalf("undelayed destination got %d cues at once, want 1", n)
	}
	if n := sink.count(2); n != 0 {
		t.Fatalf("30 s destination got %d cues before its delay, want 0", n)
	}

	clock.Advance(29 * time.Second)
	if n := sink.count(2); n != 0 {
		t.Fatalf("30 s destination got the cue at 29 s, want it held until 30 s")
	}

	clock.Advance(time.Second)
	if n := sink.count(2); n != 1 {
		t.Fatalf("30 s destination got %d cues at 30 s, want 1", n)
	}
	if n := sink.count(1); n != 1 {
		t.Fatalf("undelayed destination received the cue again: %d", n)
	}
}

// TestFanoutAppliesThePolicyOnceSoEveryPathShowsTheSameWords checks the emit
// boundary. The sidecar, the bus and every destination all receive the text the
// policy produced, and none of them receives the raw recogniser output.
func TestFanoutAppliesThePolicyOnceSoEveryPathShowsTheSameWords(t *testing.T) {
	clock := newFakeClock()
	sink := &recordingSink{clock: clock}
	route := NewCueRouter(sink, clock.After, clock.Now)
	route.SetTargets([]CueTarget{{ID: 7, Delay: 0}})

	var sidecar, published []string
	f := &Fanout{
		Policy:  TextPolicy{Blocklist: []string{"damn"}, Vocabulary: []string{"PolyEmesis"}},
		Sidecar: func(c transcribe.LiveCaption) error { sidecar = append(sidecar, c.Text); return nil },
		Publish: func(c transcribe.LiveCaption) { published = append(published, c.Text) },
		Route:   route,
	}
	f.Emit(transcribe.LiveCaption{Segment: transcribe.Segment{Text: "well damn, polyemesis"}, At: clock.Now()})

	const want = "well d***, PolyEmesis"
	if len(sidecar) != 1 || sidecar[0] != want {
		t.Fatalf("sidecar = %q, want %q", sidecar, want)
	}
	if len(published) != 1 || published[0] != want {
		t.Fatalf("published = %q, want %q", published, want)
	}
	if len(sink.got) != 1 || sink.got[0].text != want {
		t.Fatalf("destination got %+v, want text %q", sink.got, want)
	}
}

// TestChangingADestinationsDelayCancelsItsQueuedCues stops a caption queued under
// the old delay from arriving under the new one. Without the cancel, the operator
// would see the same line twice, once early and once late.
func TestChangingADestinationsDelayCancelsItsQueuedCues(t *testing.T) {
	clock := newFakeClock()
	sink := &recordingSink{clock: clock}
	r := NewCueRouter(sink, clock.After, clock.Now)
	r.SetTargets([]CueTarget{{ID: 2, Delay: 30 * time.Second}})
	r.Emit(transcribe.LiveCaption{Segment: transcribe.Segment{Text: "queued under 30 s"}, At: clock.Now()})

	r.SetTargets([]CueTarget{{ID: 2, Delay: 60 * time.Second}})
	if p := r.Pending(); p != 0 {
		t.Fatalf("pending after delay change = %d, want 0", p)
	}
	clock.Advance(120 * time.Second)
	if n := sink.count(2); n != 0 {
		t.Fatalf("cue queued under the old delay was delivered %d times", n)
	}
}

// TestRemovingADestinationCancelsItsQueuedCues covers a destination that stops
// being captioned while a cue is held for it. The cue must not reach a sink that
// no longer belongs to a live output.
func TestRemovingADestinationCancelsItsQueuedCues(t *testing.T) {
	clock := newFakeClock()
	sink := &recordingSink{clock: clock}
	r := NewCueRouter(sink, clock.After, clock.Now)
	r.SetTargets([]CueTarget{{ID: 3, Delay: 10 * time.Second}})
	r.Emit(transcribe.LiveCaption{Segment: transcribe.Segment{Text: "held"}, At: clock.Now()})

	r.SetTargets(nil)
	clock.Advance(time.Minute)
	if n := sink.count(3); n != 0 {
		t.Fatalf("removed destination received %d cues, want 0", n)
	}
}

// TestCloseCancelsEveryPendingCueAndRefusesLaterOnes is the teardown rule: a
// stopped captioner leaves no timer behind and no cue is sent after the stop.
func TestCloseCancelsEveryPendingCueAndRefusesLaterOnes(t *testing.T) {
	clock := newFakeClock()
	sink := &recordingSink{clock: clock}
	r := NewCueRouter(sink, clock.After, clock.Now)
	r.SetTargets([]CueTarget{{ID: 1, Delay: 5 * time.Second}, {ID: 2, Delay: 0}})
	r.Emit(transcribe.LiveCaption{Segment: transcribe.Segment{Text: "a"}, At: clock.Now()})

	r.Close()
	if p := r.Pending(); p != 0 {
		t.Fatalf("pending after Close = %d, want 0", p)
	}
	r.Emit(transcribe.LiveCaption{Segment: transcribe.Segment{Text: "after stop"}, At: clock.Now()})
	clock.Advance(time.Minute)
	if n := sink.count(1); n != 0 {
		t.Fatalf("delayed destination got %d cues after Close, want 0", n)
	}
	if n := sink.count(2); n != 1 {
		t.Fatalf("undelayed destination got %d cues, want only the one before Close, which is 1", n)
	}
}

// TestALateCueIsDeliveredAtOnceRatherThanDropped covers a caption whose release
// time has already passed, which happens when the recogniser runs slow. The
// caption is still worth showing, so it goes out now.
func TestALateCueIsDeliveredAtOnceRatherThanDropped(t *testing.T) {
	clock := newFakeClock()
	sink := &recordingSink{clock: clock}
	r := NewCueRouter(sink, clock.After, clock.Now)
	r.SetTargets([]CueTarget{{ID: 4, Delay: 30 * time.Second}})
	r.Emit(transcribe.LiveCaption{Segment: transcribe.Segment{Text: "slow"}, At: clock.Now().Add(-40 * time.Second)})
	if n := sink.count(4); n != 1 {
		t.Fatalf("late cue delivered %d times, want 1 at once", n)
	}
}

// TestCueIsReleasedAtTheMomentItWasSpokenPlusTheDelayNotAtRecognition is the
// lag rule. The line was produced 3 s after the speech ended (LagMS), so a 30 s
// destination must receive it 27 s after production, which is 30 s after the
// words were spoken. Releasing at production plus 30 s would put it 3 s late.
func TestCueIsReleasedAtTheMomentItWasSpokenPlusTheDelayNotAtRecognition(t *testing.T) {
	clock := newFakeClock()
	sink := &recordingSink{clock: clock}
	r := NewCueRouter(sink, clock.After, clock.Now)
	r.SetTargets([]CueTarget{{ID: 5, Delay: 30 * time.Second}})

	r.Emit(transcribe.LiveCaption{
		Segment: transcribe.Segment{Text: "lagged", StartMS: 0, EndMS: 0},
		At:      clock.Now(),
		LagMS:   3000,
	})
	clock.Advance(26 * time.Second)
	if n := sink.count(5); n != 0 {
		t.Fatalf("delivered at 26 s, want the release at 27 s")
	}
	clock.Advance(time.Second)
	if n := sink.count(5); n != 1 {
		t.Fatalf("delivered %d times at 27 s, want 1", n)
	}
}

// TestReleaseAtCountsTheLengthOfTheCueFromItsEnd checks the arithmetic on its
// own. LagMS runs from the end of the speech, so a 2 s cue that was produced 3 s
// after it ended was spoken 5 s before production.
func TestReleaseAtCountsTheLengthOfTheCueFromItsEnd(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	c := transcribe.LiveCaption{
		Segment: transcribe.Segment{StartMS: 1000, EndMS: 3000},
		At:      now,
		LagMS:   3000,
	}
	want := now.Add(-5 * time.Second).Add(30 * time.Second)
	if got := ReleaseAt(c, 30*time.Second); !got.Equal(want) {
		t.Fatalf("ReleaseAt = %v, want %v", got, want)
	}
}
