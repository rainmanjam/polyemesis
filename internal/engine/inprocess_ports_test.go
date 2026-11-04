package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/captions"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// IN-PROCESS SOCKETS DO NOT BORROW A RELAY PORT.
//
// The delay line and the SEI caption stage are sockets this process opens
// itself. They used to take a port from e.alloc and then bind it -- and the
// allocator's "free" means "free when I probed it", found by binding the port
// and closing it again. Between that probe and the real bind the port belonged
// to nobody, and any other process allocating from the same range could take
// it. CI hit exactly that: every test process builds engines on the default
// 21000 range, and a delayed destination failed to start with
// "listen udp 127.0.0.1:21000: bind: address already in use".
//
// The race itself is a gap of microseconds between two processes and cannot
// be staged on demand. What CAN be pinned is the property that removes it: these
// sockets bind :0 and hold what they get, so they do not depend on the
// allocator at all. So the allocator here is one port long and that port is
// held by somebody else for the whole test -- the range is, in effect, another
// process's -- and a delay line must still be built, and rebuilt back to back,
// and a caption stage must still start. Each rebuild binds again while the
// previous socket has only just closed, which is the turnaround a destination
// restart produces. (The destination's FFmpeg still takes an allocator port,
// correctly: a child has to be told its port before it binds it. That is why
// this builds the line and the stage directly rather than via startDest.)
//
// Mutation: put e.allocPort() back in front of either bind and the test fails
// with "relay: no free UDP port".
func TestInProcessSocketsDoNotDependOnTheRelayPortRange(t *testing.T) {
	e, _ := storeEngine(t)
	foreign := testenv.ReserveUDP(t) // held until the test ends
	e.alloc = relay.NewPortAllocator(foreign.Port(), 1)

	row := delayedRow(t, e)
	const restarts = 20
	for i := range restarts {
		dd, err := e.buildDelay(row)
		if err != nil {
			t.Fatalf("delay line %d of %d, built straight after the last one closed: %v", i+1, restarts, err)
		}
		if dd.inPort == foreign.Port() {
			t.Fatalf("the delay line is on %d, the port the relay range was limited to", dd.inPort)
		}
		e.closeDelay(dd)
	}

	upstream, err := relay.New(e.log, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	sei := seiEngine(t)
	sei.alloc = relay.NewPortAllocator(foreign.Port(), 1)
	capRow := &db.Destination{ID: 41, Name: "twitch", Platform: db.PlatformTwitch, CaptionMode: string(captions.ModeSEI)}
	capt, err := sei.startCaptionStage(capRow, upstream)
	if err != nil {
		t.Fatalf("a caption stage could not start while the relay range was taken: %v", err)
	}
	if capt == nil {
		t.Fatal("no caption stage for a Twitch destination with SEI captions")
	}
	sei.closeCaptionStage(capt)
}
