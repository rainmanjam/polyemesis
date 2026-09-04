package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

/* #631: an ffmpeg on the production host whose ppid was the LIVE server, with
 * no SIGKILL escalation anywhere in the log -- because nothing held the handle
 * to escalate with. The issue's own words: "reaped by this teardown, so the
 * host is left clean -- but nothing in polyemesis did it, and outside systemd
 * nothing would have."
 *
 * THE SHAPE. Every sidecar in this package is published by the same six lines:
 * build the Spec, take e.mu, assign the slot, drop e.mu, Start. Engine.Stop
 * collects those slots under that same e.mu and stops what it finds. So a
 * publish that lands AFTER Stop has collected finds nothing to be stopped by:
 * Stop saw a nil slot, the assignment publishes into a dead engine, and Start
 * spawns a child nothing will ever signal.
 *
 * reconcileRecorder has the guard and says why in its own comment -- "publishing
 * the process under the same lock Stop uses to collect them is what keeps a late
 * start from becoming an orphan". So do startRendition, startLoudness,
 * startFeed, reconcileBackupIngest, reconcilePlaylist, startSilence and
 * publishDest. Three did not: reconcileMeters, startPreviewLocked and
 * reconcileIngest.
 *
 * That is a fixed-value failure rather than three unrelated oversights: a
 * family of eleven near-identical sites where an incomplete set passed, and
 * nothing counted the set. The third test here is the counter.
 *
 * WHY THE WINDOW IS REAL rather than theoretical. Stop waits on the recording
 * loop and the probe loop rather than preceding them, and both reach these
 * functions -- probeLoop's settle path calls reconcileMeters directly. The
 * preview is worse: it is started ON DEMAND from a request handler, which is
 * the path least synchronised with anything.
 */

// stoppedSpawner is an engine that has already shut down, with a measured
// layout so the sidecars would otherwise have every reason to start.
func stoppedSpawner(t *testing.T, tracks int) *Engine {
	t.Helper()
	e, _ := storeEngine(t)
	e.alloc = relay.NewPortAllocator(freeUDPPort(t), 4)

	e.mu.Lock()
	e.source = routing.Source{}
	for i := 0; i < tracks; i++ {
		e.source.Tracks = append(e.source.Tracks,
			routing.Track{Index: i, Channels: 2, Codec: "aac", Layout: "stereo"})
	}
	e.measured = true
	e.measuredMode = db.IngestRTMP
	e.probed = true
	// Without it reconcileIngest refuses an RTMP mode before it ever reaches the
	// publish, and the ingest test below would pass by never entering the window
	// it is named after. It did exactly that on its first run.
	e.sourceToken = "publish-token"
	e.stopped = true
	e.mu.Unlock()
	return e
}

// portsFree drains the allocator to count what is available, then puts it all
// back. A publish that returned early must leave the pool as it found it: the
// relay ports are 500 shared across every source engine, and one lost per
// orphan is the second half of this bug.
func portsFree(t *testing.T, a *relay.PortAllocator) int {
	t.Helper()
	var held []int
	for {
		p, err := a.Allocate()
		if err != nil {
			break
		}
		held = append(held, p)
	}
	for _, p := range held {
		a.Release(p)
	}
	return len(held)
}

func TestAMetersReconcileThatPublishesIntoAShutdownStartsNothing(t *testing.T) {
	e := stoppedSpawner(t, 2)
	free := portsFree(t, e.alloc)

	s := db.DefaultSettings()
	s.Meters.Enabled = true
	e.reconcileMeters(s)

	e.mu.RLock()
	published := e.meters
	e.mu.RUnlock()
	if published != nil {
		t.Fatal("reconcileMeters published a process into a stopped engine. Stop has " +
			"already collected e.meters, so nothing will ever signal this child: it is " +
			"the #631 orphan, ppid the live server, absent from every map and every log " +
			"line and present only in the process table.")
	}
	if got := portsFree(t, e.alloc); got != free {
		t.Errorf("the relay pool went from %d free to %d: a publish that refuses must "+
			"give the port back, or every late start costs one of the 500 shared "+
			"across all source engines", free, got)
	}
}

// The preview needs the seam, because it is the one site whose window cannot be
// reached by setting e.stopped up front: it reads the flag early and returns
// there, never arriving at the publish this is about. beforePublish sits in the
// gap -- the same technique, and the same justification, as afterPublish in the
// destination path: the window is a few instructions wide and no timing test
// could sit in it reliably.
func TestAPreviewStartThatPublishesIntoAShutdownStartsNothing(t *testing.T) {
	e, _ := storeEngine(t)
	e.alloc = relay.NewPortAllocator(freeUDPPort(t), 4)

	// Flowing, or startPreviewLocked refuses before it reaches anything this
	// test is about: previewFlowing gates the encoder on the relay having
	// actually advanced, which is what stops an ffmpeg blocking for ever in
	// avformat_open_input on a quiet socket.
	hub := e.downstreamHub()
	if hub == nil {
		t.Skip("no downstream hub on this fixture")
	}
	e.mu.Lock()
	e.previewRxHub = hub
	e.previewRxBytes = hub.RxBytes()
	e.previewRxAt = time.Now()
	e.stopped = false
	e.mu.Unlock()

	free := portsFree(t, e.alloc)

	// The shutdown lands in the window: after the early read said "running",
	// before the publish.
	fired := false
	e.beforePublish = func() {
		fired = true
		e.mu.Lock()
		e.stopped = true
		e.mu.Unlock()
	}

	e.previewMu.Lock()
	e.startPreviewLocked(db.DefaultSettings())
	e.previewMu.Unlock()

	if !fired {
		t.Fatal("the seam never ran, so this test never entered the window it is " +
			"named after -- previewFlowing or an earlier refusal took the call first")
	}
	e.mu.RLock()
	published := e.preview
	e.mu.RUnlock()
	if published != nil {
		t.Fatal("startPreviewLocked published a preview encoder into an engine that " +
			"stopped after its early check. The preview is started ON DEMAND from a " +
			"request handler, the path least synchronised with a shutdown, and the " +
			"early read answers whether the engine was running when the request " +
			"arrived rather than whether it is running now.")
	}
	if got := portsFree(t, e.alloc); got != free {
		t.Errorf("the relay pool went from %d free to %d: a publish that refuses must "+
			"give the port back", free, got)
	}
}

func TestAnIngestReconcileThatPublishesIntoAShutdownStartsNothing(t *testing.T) {
	e := stoppedSpawner(t, 2)

	s := db.DefaultSettings()
	s.Ingest.Mode = db.IngestRTMP
	prev := s
	prev.Ingest.Mode = db.IngestSRT
	e.reconcileIngest(s, prev)

	e.mu.RLock()
	published := e.ingest
	e.mu.RUnlock()
	if published != nil {
		t.Fatal("reconcileIngest published an ingest into a stopped engine. This one " +
			"binds a listener, so the orphan holds the RTMP or SRT port as well: the " +
			"next start of the same install is refused by its own leftover.")
	}
}

// THE COUNTER, and the reason this is one bug rather than three.
//
// Whether a publish is safe is not a property a reader checks per site; it is a
// property of a SET, and nothing was counting the set. Two of the three sites
// this found had been wrong since they were written.
//
// Warning rung rather than Control. The real device is a single publish helper
// that cannot be called without the check, the way teardownDest is already the
// one definition of taking a destination down -- and this package makes that
// argument itself, in Stop: "through teardownDest rather than a second stop
// path, so there is exactly one definition". That refactor spans eleven call
// sites in five files and does not belong smuggled into a bug fix. Until it
// happens, this fails the moment a twelfth site is added without the guard, and
// names it.
func TestEveryProcessIsPublishedUnderTheShutdownLatch(t *testing.T) {
	sites := publishSites(t)
	if len(sites) < 10 {
		t.Fatalf("found only %d publish sites. This guard looks for `.Start()` on a "+
			"supervisor process; if the shape has changed it is now counting nothing, "+
			"which is the failure mode of every check that scans source", len(sites))
	}

	var bare []string
	for _, s := range sites {
		if !s.guarded {
			bare = append(bare, "  "+s.where)
		}
	}
	if len(bare) > 0 {
		t.Fatalf("%d process(es) are published without reading e.stopped in the same "+
			"critical section:\n%s\n\n"+
			"Engine.Stop collects these slots under e.mu and stops what it finds, so a "+
			"publish landing after it has collected produces a child nothing will ever "+
			"signal -- #631. reconcileRecorder shows the shape: check e.stopped under "+
			"the same lock, release the port, unsubscribe the hub, return without "+
			"starting.", len(bare), strings.Join(bare, "\n"))
	}
}
