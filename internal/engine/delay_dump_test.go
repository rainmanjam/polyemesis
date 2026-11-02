package engine

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/delayline"
	"github.com/rainmanjam/polyemesis/internal/delayline/delaytest"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/eventstream/streamtest"
	"github.com/rainmanjam/polyemesis/internal/relay"
)

// dumpRow is a passthrough destination with a delay long enough to dump
// from: a dump leaves at least DumpMargin plus one GOP.
func dumpRow(t *testing.T, e *Engine, seconds int) *db.Destination {
	t.Helper()
	row := testDestination(0, nil)
	row.Name = "dumpable"
	row.DelaySeconds = seconds
	row.SourceID = &e.sourceID
	saved, err := e.store.CreateDestination(row)
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	return saved
}

// feedTS delivers a synthetic stream into a hub at its own pace, in
// datagrams of seven packets, until stop closes.
func feedTS(h *relay.Hub, s *delaytest.Stream, stop <-chan struct{}) {
	start := time.Now()
	for i := 0; i < len(s.Pkts); {
		at := s.Times[i]
		var dgram []byte
		for n := 0; n < 7 && i < len(s.Pkts) && s.Times[i] == at; n++ {
			dgram = append(dgram, s.Pkts[i]...)
			i++
		}
		if wait := time.Until(start.Add(at)); wait > 0 {
			select {
			case <-time.After(wait):
			case <-stop:
				return
			}
		}
		h.Deliver(dgram)
	}
}

// A DUMP ON A RUNNING DESTINATION. The engine finds the destination's line,
// the line cuts and resumes, and the delay on the status payload and on the
// event stream is shorter by what was dumped -- while the configured delay,
// `seconds`, stays what the operator set.
func TestDumpingARunningDelayedDestinationShortensItsDelayAndAnnouncesIt(t *testing.T) {
	e, _ := storeEngine(t)
	hub := eventstream.NewHub()
	e.SetStream(hub)
	sub := streamtest.Subscribe(t, hub)
	row := dumpRow(t, e, 6)
	if err := e.startDest(delayPlan(row), e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	if _, err := e.DumpDelay(row.ID, time.Second, nil); !errors.Is(err, delayline.ErrNotOnAir) {
		t.Fatalf("a dump while the delay is still filling: %v, want ErrNotOnAir", err)
	}

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go feedTS(e.hub, delaytest.New(30).Mux(30*time.Second), stop) // 1 s GOPs
	eventually(t, "the delayed feed to go on air", 10*time.Second, func() bool {
		d := statusFor(t, e, row.ID).Delay
		return d != nil && d.Phase == DelayOnAir && d.Dumpable
	})
	if d := statusFor(t, e, row.ID).Delay; d.EffectiveSeconds != 6 || d.DumpedSeconds != 0 {
		t.Fatalf("before any dump: effective %v dumped %v, want 6 and 0", d.EffectiveSeconds, d.DumpedSeconds)
	}

	final := make(chan delayline.DumpResult, 2)
	res, err := e.DumpDelay(row.ID, time.Second, func(r delayline.DumpResult) { final <- r })
	if err != nil {
		t.Fatalf("DumpDelay: %v", err)
	}
	// The final figures reach the caller's callback once, from the resume:
	// what the audit record is written from.
	select {
	case got := <-final:
		if !got.Complete || got.Dumped != res.Dumped || got.After != res.After {
			t.Fatalf("the completion callback got %+v; the dump answered %+v", got, res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the completion callback never ran")
	}
	if !res.Complete || res.Dumped < time.Second || res.After >= 6*time.Second || res.After < delayline.DumpMargin {
		t.Fatalf("result %+v: want a complete dump of 1 s or more, leaving at least the margin", res)
	}
	st := statusFor(t, e, row.ID).Delay
	if st.Seconds != 6 {
		t.Errorf("the configured delay changed to %d: a dump must not rewrite what the operator set", st.Seconds)
	}
	if got := time.Duration(st.EffectiveSeconds * float64(time.Second)); got-res.After > time.Millisecond || res.After-got > time.Millisecond {
		t.Errorf("status effectiveSeconds %v, the dump left %s", st.EffectiveSeconds, res.After)
	}

	var env eventstream.Envelope
	for {
		env = streamtest.Expect(t, sub, eventstream.TypeDestinationDelay)
		if env.Reason == delayEventDumped {
			break
		}
	}
	data, _ := env.Data.(map[string]any)
	if fmt.Sprint(data["seconds"]) != "6" || data["dumpSeconds"] == nil || data["dumpedSeconds"] == nil ||
		data["delayAfterSeconds"] == nil || data["effectiveSeconds"] == nil {
		t.Errorf("destination.delay dumped data = %v; want seconds 6 with dumpSeconds, dumpedSeconds, "+
			"delayAfterSeconds and effectiveSeconds", data)
	}
	if env.Destination == nil || env.Destination.ID != row.ID {
		t.Errorf("destination.delay dumped names %+v", env.Destination)
	}

	// CAPTIONS follow the shorter delay, and a dump is not an operator save:
	// the cue target's delay is the delay the audience has, while the intent's
	// signature -- which re-arms the captioner when it changes -- is still
	// taken from the configured delay. (The row is captioned in the store
	// only; nothing restarts the running destination.)
	row.Platform, row.CaptionMode = db.PlatformTwitch, "sei"
	if _, err := e.store.UpdateDestination(row); err != nil {
		t.Fatal(err)
	}
	in, err := e.captionIntentNow()
	if err != nil {
		t.Fatal(err)
	}
	var target *time.Duration
	for _, tg := range in.targets {
		if tg.ID == row.ID {
			target = &tg.Delay
		}
	}
	if target == nil {
		t.Fatalf("the captioned destination is not a cue target: %+v", in.targets)
	}
	if d := *target - res.After; d > time.Millisecond || d < -time.Millisecond {
		t.Errorf("cue delay %s; the audience's delay after the dump is %s", *target, res.After)
	}
	if !strings.Contains(in.sig, "delay=6s") {
		t.Errorf("caption intent signature %q is not the configured 6 s: a dump would re-arm the captioner", in.sig)
	}
}

func TestDumpingADestinationWithoutADelayIsRefused(t *testing.T) {
	e, _ := storeEngine(t)
	row := testDestination(0, nil)
	row.SourceID = &e.sourceID
	saved, err := e.store.CreateDestination(row)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.DumpDelay(saved.ID, time.Second, nil); !errors.Is(err, ErrNotDelayed) {
		t.Fatalf("DumpDelay on an undelayed destination: %v, want ErrNotDelayed", err)
	}
	if e.DelayDumpable(saved.ID) {
		t.Fatal("an undelayed destination reported dumpable")
	}
}

// TWO DUMPS AT ONCE, THEN A STOP BEFORE THE RESUME. The feed has stopped, so
// the dump that is accepted can only wait; the other is refused as pending and
// must not take over the accepted one's callback. When the engine then stops
// (the line is cut), the accepted dump's callback reports it aborted -- which
// is what the audit's outcome record is written from.
//
// Mutations: keep the callback in a per-destination slot that the second call
// overwrites; don't fire on abandon.
func TestConcurrentDumpsKeepTheirOwnCallbacksAndAStopReportsTheAbort(t *testing.T) {
	e, _ := storeEngine(t)
	row := dumpRow(t, e, 6)
	row.DelayDumpSeconds = 4
	if _, err := e.store.UpdateDestination(row); err != nil {
		t.Fatal(err)
	}
	if err := e.startDest(delayPlan(row), e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	stop := make(chan struct{})
	go feedTS(e.hub, delaytest.New(30).Mux(30*time.Second), stop)
	eventually(t, "the delayed feed to go on air", 10*time.Second, func() bool {
		d := statusFor(t, e, row.ID).Delay
		return d != nil && d.Phase == DelayOnAir && d.Dumpable
	})
	if got := statusFor(t, e, row.ID).DelayDumpSeconds; got != 4 {
		t.Errorf("status delayDumpSeconds %d, want the row's 4", got)
	}
	close(stop) // no resume keyframe will come

	type call struct {
		res delayline.DumpResult
		err error
		cb  chan delayline.DumpResult
	}
	calls := make([]call, 2)
	var wg sync.WaitGroup
	for i := range calls {
		calls[i].cb = make(chan delayline.DumpResult, 2)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cb := calls[i].cb
			calls[i].res, calls[i].err = e.DumpDelay(row.ID, time.Second, func(r delayline.DumpResult) { cb <- r })
		}(i)
	}
	wg.Wait()
	accepted, refused := 0, 0
	for i, c := range calls {
		switch {
		case c.err == nil:
			accepted = i
		case errors.Is(c.err, delayline.ErrDumpPending):
			refused = i
		default:
			t.Fatalf("dump %d: %v", i, c.err)
		}
	}
	if accepted == refused {
		t.Fatalf("want one dump accepted and one refused as pending: %+v", calls)
	}
	if calls[accepted].res.Complete {
		t.Fatal("the dump completed with the feed stopped")
	}
	e.Stop()
	select {
	case r := <-calls[accepted].cb:
		if !r.Aborted {
			t.Fatalf("the accepted dump's callback got %+v after a stop, want aborted", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the accepted dump's callback never ran after the stop")
	}
	select {
	case r := <-calls[refused].cb:
		t.Fatalf("the refused dump's callback ran: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
}
