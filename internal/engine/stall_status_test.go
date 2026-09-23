package engine

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/stats"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// progressChild is a supervised child that prints FFmpeg -progress blocks. With
// moving false its out_time never changes after the first block, which is what
// a destination whose sink stopped reading prints; with moving true it
// advances every block, which is a feed delivering.
func progressChild(t *testing.T, name string, moving bool) *supervisor.Process {
	t.Helper()
	script := `while :; do printf 'out_time_us=1000000\ntotal_size=1000\nbitrate=2000.0kbits/s\nspeed=1.0x\nprogress=continue\n'; sleep 0.2; done`
	if moving {
		script = `i=0; while :; do i=$((i+200000)); printf 'out_time_us=%d\ntotal_size=%d\nprogress=continue\n' $i $i; sleep 0.2; done`
	}
	p := supervisor.New(slog.New(slog.NewTextHandler(io.Discard, nil)),
		supervisor.Spec{Name: name, Kind: "destination", Bin: "/bin/sh", Args: []string{"-c", script}})
	p.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = p.Stop(ctx)
	})
	return p
}

// liveMonitor is an ingest monitor whose hub is receiving bytes from the moment
// it is started, sampled by the real 1 Hz loop.
func liveMonitor(t *testing.T) *stats.Monitor {
	t.Helper()
	var rx atomic.Uint64
	m := stats.NewMonitor(func() uint64 { return rx.Add(10_000) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go m.Run(ctx)
	return m
}

// THE WIRING OF DestStatus.Stalled, THROUGH Engine.Status. The verdict itself
// is table-tested on destinationStalled; this is what feeds it -- whether an
// ingest is configured, whether the source the destinations read is arriving,
// and for how long -- read off a real engine with a real frozen child. Every
// case here is a line of Status that could be changed without the table
// noticing: calling the source always arriving puts a stall on every
// destination for one lost ingest; inverting "ingest configured" does the same
// and hides a stall with no ingest to blame; reading the primary hub alone
// hides a real stall while failover is on air; and ignoring how long the source
// has been back reads every destination stalled for the moment after an outage.
func TestStatusDecidesADestinationsStallFromTheSourceItReads(t *testing.T) {
	e := failoverEngine(t)
	row, err := e.store.CreateDestination(&db.Destination{
		Name: "frozen", Kind: db.DestFile, URL: "frozen.ts", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	// All started at once so their clocks overlap: the destination is stalled
	// supervisor.StallAfter after its first block, by which time the monitor
	// and the feed have been live nearly as long.
	frozen := progressChild(t, "dest:frozen", false)
	feed := progressChild(t, "source:slate", true)
	longLive := liveMonitor(t)
	e.dests[row.ID] = &destination{row: row, proc: frozen}

	idle := supervisor.New(slog.New(slog.NewTextHandler(io.Discard, nil)),
		supervisor.Spec{Name: "ingest", Kind: "ingest", Bin: "true"})
	silent := stats.NewMonitor(nil) // a hub nothing arrives on: no samples

	stalled := func() (bool, *supervisor.Status) {
		t.Helper()
		for _, d := range e.Status().Destinations {
			if d.ID == row.ID {
				return d.Stalled, d.Process
			}
		}
		t.Fatalf("destination %d missing from Status", row.ID)
		return false, nil
	}
	set := func(ingest *supervisor.Process, mon *stats.Monitor, sel *selector) {
		e.mu.Lock()
		e.ingest, e.mon, e.sel = ingest, mon, sel
		e.mu.Unlock()
	}

	// The source has been arriving the whole time and the destination is not
	// moving: its own stall. Bounded, so a wiring that never says so fails.
	set(idle, longLive, nil)
	deadline := time.Now().Add(20 * time.Second)
	for {
		if s, _ := stalled(); s {
			break
		}
		if time.Now().After(deadline) {
			_, p := stalled()
			t.Fatalf("frozen destination with the source arriving never read stalled; process %+v", p)
		}
		time.Sleep(100 * time.Millisecond)
	}

	if _, p := stalled(); p == nil || !p.Stalled {
		t.Fatalf("the process itself must be stalled for the cases below to mean anything: %+v", p)
	}

	// Ingest configured, nothing arriving: every output is frozen because the
	// source is, which is the ingest's fault reported once, not this platform's.
	set(idle, silent, nil)
	if s, _ := stalled(); s {
		t.Error("ingest configured and no source arriving, and the destination read stalled: " +
			"a lost ingest would put up=0 and a warning on every destination")
	}

	// No ingest configured: nothing to blame but the destination.
	set(nil, silent, nil)
	if s, _ := stalled(); !s {
		t.Error("no ingest configured and a frozen destination did not read stalled")
	}

	// FAILOVER ON AIR: the primary hub is silent, but the destinations read the
	// selector's hub, which the slate feed is delivering into. A frozen
	// destination is then its own stall; reading the primary alone hid it.
	set(idle, silent, &selector{active: sourceSlate, feed: &sourceFeed{kind: sourceSlate, proc: feed}})
	if s, _ := stalled(); !s {
		t.Error("failover's feed is delivering and the frozen destination did not read stalled: " +
			"the primary hub is not what it reads while failover is on air")
	}
	// And a feed that is itself frozen is the source failing, not the platform.
	set(idle, silent, &selector{active: sourceSlate, feed: &sourceFeed{kind: sourceSlate, proc: frozen}})
	if s, _ := stalled(); s {
		t.Error("failover's feed is frozen and the destination still read stalled")
	}
	// With primary active the primary hub is the truth, whatever the feed says.
	set(idle, silent, &selector{active: sourcePrimary, feed: &sourceFeed{kind: sourcePrimary, proc: feed}})
	if s, _ := stalled(); s {
		t.Error("primary active and silent, and the destination read stalled off the feed")
	}

	// THE SOURCE HAS ONLY JUST COME BACK. The destination's output was frozen
	// by the outage, so its process has been "stalled" since the outage began
	// and stays so until its next progress block. That is not this
	// destination's stall until the source has been arriving StallAfter.
	fresh := liveMonitor(t)
	for len(fresh.Bitrate()) == 0 {
		time.Sleep(50 * time.Millisecond)
	}
	set(idle, fresh, nil)
	if s, _ := stalled(); s {
		t.Error("the source came back a moment ago and the destination already read stalled: " +
			"its stall was measured from before the outage")
	}
}
