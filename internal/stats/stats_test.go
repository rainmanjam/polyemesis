package stats

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The ring backs the monitoring page's 30-minute bitrate chart, and the monitor
// feeds the post-production governor's CPU sensor. Both are read from HTTP
// handlers while a sampling goroutine writes them.

func TestRingIsEmptyBeforeAnythingIsAdded(t *testing.T) {
	if got := NewRing(4).Samples(); len(got) != 0 {
		t.Errorf("Samples() = %d entries on a fresh ring, want 0", len(got))
	}
}

func TestRingReturnsAPartialSeriesOldestFirst(t *testing.T) {
	r := NewRing(5)
	for i := 1; i <= 3; i++ {
		r.Add(Sample{Kbps: float64(i)})
	}
	got := r.Samples()
	if len(got) != 3 {
		t.Fatalf("Samples() = %d entries, want 3", len(got))
	}
	for i, s := range got {
		if s.Kbps != float64(i+1) {
			t.Errorf("sample %d = %v, want %v -- a partial ring must not expose its zero tail", i, s.Kbps, i+1)
		}
	}
}

func TestRingWrapsAndKeepsChronologicalOrder(t *testing.T) {
	r := NewRing(3)
	// 1,2,3 fills it; 4 and 5 overwrite the two oldest.
	for i := 1; i <= 5; i++ {
		r.Add(Sample{Kbps: float64(i)})
	}
	got := r.Samples()
	if len(got) != 3 {
		t.Fatalf("Samples() = %d entries, want 3 (capacity)", len(got))
	}
	// Oldest-first, or the chart draws the last half-hour out of order.
	want := []float64{3, 4, 5}
	for i, w := range want {
		if got[i].Kbps != w {
			t.Errorf("sample %d = %v, want %v; full series %v", i, got[i].Kbps, w, got)
		}
	}
}

func TestRingIsExactlyFullWithoutWrapping(t *testing.T) {
	r := NewRing(3)
	for i := 1; i <= 3; i++ {
		r.Add(Sample{Kbps: float64(i)})
	}
	// The boundary where next wraps to 0 and full flips: an off-by-one here
	// shows up as the chart briefly emptying every capacity-many samples.
	got := r.Samples()
	if len(got) != 3 {
		t.Fatalf("Samples() = %d entries at exactly capacity, want 3", len(got))
	}
	if got[0].Kbps != 1 || got[2].Kbps != 3 {
		t.Errorf("series = %v, want 1..3 in order", got)
	}
}

func TestSamplesReturnsACopy(t *testing.T) {
	r := NewRing(3)
	r.Add(Sample{Kbps: 42})
	got := r.Samples()
	got[0].Kbps = 999
	// A handler that serialises this must not be able to corrupt the ring, and
	// two concurrent readers must not see each other's edits.
	if again := r.Samples(); again[0].Kbps != 42 {
		t.Errorf("mutating the returned slice changed the ring: %v", again[0].Kbps)
	}
}

func TestRingIsSafeUnderConcurrentAddAndRead(t *testing.T) {
	r := NewRing(64)
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				r.Add(Sample{Time: time.Now(), Kbps: float64(i)})
			}
		}
	}()
	for i := 0; i < 500; i++ {
		_ = r.Samples()
	}
	close(stop)
	wg.Wait()
}

func TestMonitorDifferentiatesTheByteCounterIntoKbps(t *testing.T) {
	var counter atomic.Uint64
	m := NewMonitor(func() uint64 { return counter.Load() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	// 125_000 bytes/sec is 1000 kbps. The ticker is 1 Hz, so a couple of
	// seconds gives a sample or two without making this test slow.
	deadline := time.After(4 * time.Second)
	for {
		counter.Add(125_000)
		select {
		case <-deadline:
			t.Fatal("no bitrate sample was recorded")
		case <-time.After(200 * time.Millisecond):
		}
		if s := m.Bitrate(); len(s) > 0 {
			// Loose bounds on purpose: this asserts the arithmetic is roughly
			// right and in the correct units, not that the scheduler is exact.
			last := s[len(s)-1].Kbps
			if last < 100 || last > 20_000 {
				t.Errorf("kbps = %v, which is not a plausible rate for the bytes fed in", last)
			}
			if s[len(s)-1].Time.IsZero() {
				t.Error("sample carries no timestamp")
			}
			return
		}
	}
}

func TestMonitorTreatsACounterResetAsZeroRatherThanAHugeSpike(t *testing.T) {
	// The relay's RxBytes restarts at 0 whenever the hub is rebuilt, which
	// happens on every ingest reconfigure. Subtracting the previous total from
	// a smaller current one would underflow uint64 into an astronomical rate
	// and wreck the chart's scale for the next thirty minutes.
	var counter atomic.Uint64
	counter.Store(10_000_000)
	m := NewMonitor(func() uint64 { return counter.Load() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	time.Sleep(1200 * time.Millisecond)
	counter.Store(0) // the hub was rebuilt
	time.Sleep(2500 * time.Millisecond)

	for _, s := range m.Bitrate() {
		if s.Kbps < 0 {
			t.Errorf("negative bitrate %v after a counter reset", s.Kbps)
		}
		// Anything past a terabit means the subtraction underflowed.
		if s.Kbps > 1e9 {
			t.Fatalf("bitrate %v after a counter reset: the byte delta underflowed", s.Kbps)
		}
	}
}

func TestMonitorToleratesNoByteCounter(t *testing.T) {
	// The monitor is built before the hub exists in some start-up orderings,
	// and a nil rxBytes must not panic the sampling goroutine.
	m := NewMonitor(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	m.Run(ctx) // returns on ctx expiry rather than panicking

	if got := m.Bitrate(); len(got) != 0 {
		t.Errorf("recorded %d bitrate samples with no counter, want 0", len(got))
	}
}

func TestRunStopsWhenTheContextIsCancelled(t *testing.T) {
	m := NewMonitor(func() uint64 { return 0 })
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after its context was cancelled; shutdown would hang")
	}
}

func TestSystemSnapshotIsPopulatedAndConcurrencySafe(t *testing.T) {
	m := NewMonitor(func() uint64 { return 0 })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	// The governor calls System() on every job-claim decision while the monitor
	// writes it once a second.
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				_ = m.System()
			}
		}()
	}
	wg.Wait()

	// NumCPU is filled in synchronously on every sample, so once a sample has
	// landed it must be sane. Give the 1 Hz ticker room.
	time.Sleep(1500 * time.Millisecond)
	if n := m.System().NumCPU; n < 1 {
		t.Errorf("NumCPU = %d, want at least 1", n)
	}
}
