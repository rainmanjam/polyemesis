package engine

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/relay"
)

// startCounter is a slog handler that counts "destination starting" lines per
// destination, which is how an operator sees a start in the log.
type startCounter struct {
	mu     sync.Mutex
	starts map[string]int
}

func (c *startCounter) Enabled(context.Context, slog.Level) bool { return true }
func (c *startCounter) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *startCounter) WithGroup(string) slog.Handler            { return c }
func (c *startCounter) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "destination starting" {
		return nil
	}
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "dest" {
			c.mu.Lock()
			if c.starts == nil {
				c.starts = map[string]int{}
			}
			c.starts[a.Value.String()]++
			c.mu.Unlock()
		}
		return true
	})
	return nil
}

func (c *startCounter) count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.starts[name]
}

// flakyProbeSource is a fake ffprobe that exits non-zero for its first
// FAKE_PROBE_FAIL_FIRST runs (counted in the file FAKE_PROBE_COUNT) and then
// prints FAKE_PROBE_JSON, which is the probe the staging log showed: retried,
// failing, then landing.
const flakyProbeSource = `package main

import (
	"fmt"
	"os"
	"strconv"
)

func main() {
	path := os.Getenv("FAKE_PROBE_COUNT")
	b, _ := os.ReadFile(path)
	n, _ := strconv.Atoi(string(b))
	n++
	_ = os.WriteFile(path, []byte(strconv.Itoa(n)), 0o600)
	failFirst, _ := strconv.Atoi(os.Getenv("FAKE_PROBE_FAIL_FIRST"))
	if n <= failFirst {
		fmt.Fprintln(os.Stderr, "no stream yet")
		os.Exit(1)
	}
	fmt.Println(os.Getenv("FAKE_PROBE_JSON"))
}
`

// A START THAT ARRIVES BEFORE THE INGEST LAYOUT IS KNOWN MUST NOT BE TORN DOWN
// AND RESTARTED WHEN THE LAYOUT LANDS.
//
// Staging, OBS bridge in script mode: YouTube was started, went live, was
// removed ~5s later with no log line, then started again when the layout probe
// finally succeeded; Facebook's start sat held until then. Cause: the hold's
// 75s ceiling was stamped by the first held reconcile, which on an idle box
// runs at boot, so the ceiling had expired by the time OBS connected. The first
// start therefore went out provisionally, which cleared the clock; the second
// reconcile (the next start) held again with an empty plan and tore the first
// destination down.
//
// The test stages the idle box by backdating the hold clock, then runs the
// reconciles of two starts and a probe that fails twice before it lands.
func TestAStartBeforeTheLayoutIsKnownIsNotTornDownAndRestarted(t *testing.T) {
	e, store := storeEngine(t)
	standInFFmpeg(t, e)
	e.alloc = relay.NewPortAllocator(freeUDPPort(t), 8)
	counter := &startCounter{}
	e.log = slog.New(counter)
	setSettings(e, db.DefaultSettings())

	mk := func(name string) {
		t.Helper()
		if _, err := store.CreateDestination(&db.Destination{
			Name: name, Kind: db.DestRTMP, URL: "rtmp://example.invalid/live",
			StreamKey: "key", Enabled: true, SourceID: &e.sourceID,
		}); err != nil {
			t.Fatalf("CreateDestination %s: %v", name, err)
		}
	}
	reconcile := func() {
		t.Helper()
		e.reconcileMu.Lock()
		defer e.reconcileMu.Unlock()
		if err := e.reconcileOutputs(); err != nil {
			t.Fatalf("reconcileOutputs: %v", err)
		}
	}
	t.Cleanup(func() {
		e.reconcileMu.Lock()
		defer e.reconcileMu.Unlock()
		e.stopDestinations(map[int64]destPlan{})
	})

	// The engine boots and reconciles with no encoder connected. Time then
	// passes (backdated here, since the ceiling is 75s of wall clock): whatever
	// that idle reconcile left on the hold clock has expired by the time OBS
	// publishes. A clock that does not run without a stream leaves nothing to
	// age, which is the point.
	reconcile()
	if since := e.holdSince.Load(); since != 0 {
		e.holdSince.Store(time.Now().Add(-2 * holdCeiling).UnixNano())
	}

	mk("YouTube")
	reconcile()
	time.Sleep(300 * time.Millisecond)
	mk("Facebook Live")
	reconcile()

	// Nothing is known about the stream, so the only acceptable outcomes are
	// "still waiting" or "kept running"; a start followed by a teardown is the bug.
	e.mu.RLock()
	running := len(e.dests)
	e.mu.RUnlock()
	t.Logf("destinations present while the layout is unknown: %d", running)

	// The probe fails twice, then lands (three attempts, as in the log).
	e.tools.FFprobe = fakeFFprobe(t, flakyProbeSource)
	t.Setenv("FAKE_PROBE_COUNT", filepath.Join(t.TempDir(), "count"))
	t.Setenv("FAKE_PROBE_FAIL_FIRST", "2")
	t.Setenv("FAKE_PROBE_JSON", stereoProbeJSON)
	for i := 1; i <= 2; i++ {
		if e.probeOnce(context.Background()) {
			t.Fatalf("probe %d reported a layout; it was meant to fail", i)
		}
		reconcile()
	}
	if !e.probeOnce(context.Background()) {
		t.Fatal("the third probe did not land")
	}
	reconcile()

	waitUntil(t, func() bool {
		e.mu.RLock()
		defer e.mu.RUnlock()
		n := 0
		for _, d := range e.dests {
			if d != nil && d.proc != nil {
				n++
			}
		}
		return n == 2
	}, "both destinations to be running once the layout is known")

	for _, name := range []string{"YouTube", "Facebook Live"} {
		if n := counter.count(name); n != 1 {
			t.Errorf("%s was started %d times; a start made before the layout was known must "+
				"wait for it (or survive it), never be torn down and started again", name, n)
		}
	}
}

// Once the ceiling has fired on a stream that really was being probed, the
// destinations it let out are not pulled back by the next reconcile. The
// clock used to be cleared by the pass that fired it, so a second start
// re-held and tore the first down.
func TestAnExpiredHoldStaysExpiredUntilALayoutIsMeasured(t *testing.T) {
	e, store := storeEngine(t)
	standInFFmpeg(t, e)
	e.alloc = relay.NewPortAllocator(freeUDPPort(t), 8)
	counter := &startCounter{}
	e.log = slog.New(counter)
	setSettings(e, db.DefaultSettings())
	if _, err := store.CreateDestination(&db.Destination{
		Name: "YouTube", Kind: db.DestRTMP, URL: "rtmp://example.invalid/live",
		StreamKey: "key", Enabled: true, SourceID: &e.sourceID,
	}); err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	t.Cleanup(func() {
		e.reconcileMu.Lock()
		defer e.reconcileMu.Unlock()
		e.stopDestinations(map[int64]destPlan{})
	})
	e.streamSeen.Store(true)
	e.holdSince.Store(time.Now().Add(-2 * holdCeiling).UnixNano())

	for i := 0; i < 3; i++ {
		e.reconcileMu.Lock()
		err := e.reconcileOutputs()
		e.reconcileMu.Unlock()
		if err != nil {
			t.Fatalf("reconcileOutputs: %v", err)
		}
	}
	if n := counter.count("YouTube"); n != 1 {
		t.Errorf("started %d times across three reconciles after the ceiling fired, want 1", n)
	}
	e.mu.RLock()
	d := e.dests[1]
	e.mu.RUnlock()
	if d == nil {
		t.Error("the destination the ceiling let out was torn down by a later reconcile")
	}
}
