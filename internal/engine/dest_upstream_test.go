package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// A9. startDest subscribes to the hub it was HANDED -- a rendition's, or a
// running selector's -- and on a ResolveForWrite failure it unsubscribed from
// e.hub. The subscriber stayed in the other hub for ever while the port went
// back to the allocator, so the port is reissued and the stale entry blasts
// transport-stream datagrams into whatever now owns that socket.
//
// Mutation: in startDest's ResolveForWrite failure path, change
// `hub.Unsubscribe(subName)` back to `e.hub.Unsubscribe(subName)`. Observed to
// fail -- the rendition hub still listed dest:3.
func TestAFileDestinationThatCannotResolveUnsubscribesFromItsOwnHub(t *testing.T) {
	e, _ := storeEngine(t)
	e.alloc = relay.NewPortAllocator(freeUDPPort(t), 1)

	// A rendition's hub, deliberately not e.hub: that difference is the bug.
	rendHub, err := relay.New(testLogger(), 0)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	defer rendHub.Close()

	// A separator in the name is refused by recording.Manager.Resolve, which is
	// the failure this path exists for.
	row := &db.Destination{ID: 3, Name: "archive", Kind: db.DestFile, URL: "sub/dir.mkv"}
	if err := e.startDest(row, routing.Result{}, "spec", rendHub, 0); err == nil {
		t.Fatal("startDest accepted a file destination whose name escapes the recordings directory")
	}

	if subs := rendHub.Subscribers(); len(subs) != 0 {
		t.Errorf("the rendition hub still forwards to %v; the destination unsubscribed "+
			"from a different hub than the one it subscribed to", subs)
	}
	if _, err := e.alloc.Allocate(); err != nil {
		t.Errorf("the relay port was not released: %v", err)
	}
}
