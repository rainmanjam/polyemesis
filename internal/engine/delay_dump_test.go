package engine

import (
	"errors"
	"fmt"
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
	if _, err := e.DumpDelay(row.ID, time.Second); !errors.Is(err, delayline.ErrNotOnAir) {
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

	res, err := e.DumpDelay(row.ID, time.Second)
	if err != nil {
		t.Fatalf("DumpDelay: %v", err)
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
}

func TestDumpingADestinationWithoutADelayIsRefused(t *testing.T) {
	e, _ := storeEngine(t)
	row := testDestination(0, nil)
	row.SourceID = &e.sourceID
	saved, err := e.store.CreateDestination(row)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.DumpDelay(saved.ID, time.Second); !errors.Is(err, ErrNotDelayed) {
		t.Fatalf("DumpDelay on an undelayed destination: %v, want ErrNotDelayed", err)
	}
	if e.DelayDumpable(saved.ID) {
		t.Fatal("an undelayed destination reported dumpable")
	}
}
