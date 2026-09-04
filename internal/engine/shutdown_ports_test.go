package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// STOPPING AN ENGINE GIVES BACK EVERY PORT IT TOOK. #707.
//
// The pool is 500 ports shared across every engine, and Manager.Sync stops an
// engine on every source delete while the daemon keeps running -- so a port an
// engine fails to return is gone for the life of the process. Nothing reports
// it until Allocate starts failing, and then it fails everywhere at once and
// reads as an unrelated fault.
//
// Four kinds were not being returned. Destinations, loudness, clips, captions,
// feeds, silence, backup and playlist went through a teardown that released;
// the recorder, preview, meters and renditions went through a bare `stop`
// helper that only called Stop. Measured before the fix: three ports plus one
// per rendition leaked per shutdown, and the hub kept their subscriptions.
//
// A SPAN WITH NO SPARE is what identifies the leak. If every port is taken and
// Stop returns them, the allocator can hand out exactly that many again; if it
// returns none, it can hand out none. A larger span would let a leak pass.
func TestStopReturnsEveryPortTheAuxChildrenTook(t *testing.T) {
	e, _ := storeEngine(t)

	const span = 4
	base, held := testenv.FreeUDPWindow(t, span)
	testenv.ReleaseAndSettle(t, held...)
	e.alloc = relay.NewPortAllocator(base, span)

	// The three aux consumers and one rendition -- the exact four kinds that
	// went through the non-releasing path.
	e.mu.Lock()
	e.recorder, e.preview, e.meters = loudTestProc(), loudTestProc(), loudTestProc()
	e.recorderPort = enginePort(t, e, "recorder")
	e.previewPort = enginePort(t, e, "preview")
	e.metersPort = enginePort(t, e, "meters")
	mustSubscribe(t, e.hub, "recorder", e.recorderPort)
	mustSubscribe(t, e.hub, "preview", e.previewPort)
	mustSubscribe(t, e.hub, "meters", e.metersPort)

	rp := enginePort(t, e, "rendition")
	e.rends = map[int64]*rendition{1: {
		proc: loudTestProc(), port: rp, subName: "rend:1", in: e.hub,
	}}
	mustSubscribe(t, e.hub, "rend:1", rp)
	e.mu.Unlock()

	if got := e.heldPortCount(); got != span {
		t.Fatalf("the fixture holds %d of %d ports; the assertion below only means "+
			"something when the pool is exactly full", got, span)
	}

	e.Stop()

	// EVERY ONE BACK. Allocate span times: if any was leaked, one of these fails.
	var reclaimed []int
	for i := 0; i < span; i++ {
		p, err := e.alloc.Allocate()
		if err != nil {
			t.Fatalf("port %d of %d was not returned by Stop: %v.\n"+
				"    The pool is shared across every engine, so each deleted source "+
				"burns these permanently -- three plus one per rendition, silently, "+
				"until Allocate starts failing everywhere at once.", i+1, span, err)
		}
		reclaimed = append(reclaimed, p)
	}
	for _, p := range reclaimed {
		e.alloc.Release(p)
	}

	// And the engine agrees it is holding nothing, which is what the
	// post-condition in StopWithin reports on.
	if n := e.heldPortCount(); n != 0 {
		t.Errorf("the engine still records %d held port(s) after Stop", n)
	}

	// The subscriptions too: a released port with a live subscription still
	// forwards, and the hub outlives the engine on a selector tier.
	for _, name := range []string{"recorder", "preview", "meters", "rend:1"} {
		if hasSubscriber(e.hub, name) {
			t.Errorf("the hub still forwards to %q after Stop", name)
		}
	}
}

// RELEASING A PORT THIS ENGINE DOES NOT HOLD IS REFUSED. #708.
//
// Release took a bare int and did delete(a.held, p), so releasing twice
// silently un-held a port the pool may have since given to a DIFFERENT engine
// -- two engines pointed at one UDP port, which is what the allocator's bind
// probe exists to prevent. The probe only masks it while the first owner's
// child is bound; during restart backoff, or between Allocate and Start, it is
// open.
//
// The reachable path: stopBackup deliberately does not clear d.backupPort, so
// the struct still names a released port and a later teardown releases it again.
func TestReleasingAPortTwiceDoesNotHandItBackTwice(t *testing.T) {
	e, _ := storeEngine(t)
	base, held := testenv.FreeUDPWindow(t, 2)
	testenv.ReleaseAndSettle(t, held...)
	e.alloc = relay.NewPortAllocator(base, 2)

	p := enginePort(t, e, "the port under test")
	e.releasePort(p)

	// A second engine takes it, which is what makes the double release harmful
	// rather than untidy.
	other, _ := storeEngine(t)
	other.alloc = e.alloc
	taken, err := other.allocPort()
	if err != nil {
		t.Fatalf("the second engine could not take the released port: %v", err)
	}

	// The first engine releases the stale int it still holds a copy of.
	e.releasePort(p)

	if taken == p {
		// The port the second engine holds must still be held by the pool. If
		// the stale release un-held it, the pool will hand the SAME port out
		// again -- to a third caller, while the second engine is using it.
		again, err := e.alloc.Allocate()
		if err == nil && again == p {
			t.Fatalf("port %d was handed out again while another engine holds it. "+
				"A stale release returned a port the pool had already reassigned, "+
				"which is two engines on one UDP port -- one programme's "+
				"destination receiving another's stream.", p)
		}
		if err == nil {
			e.alloc.Release(again)
		}
	}
}
