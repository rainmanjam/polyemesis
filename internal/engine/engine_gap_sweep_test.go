package engine

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/relay"
)

// ------------------------------------------------------------ real datagrams
//
// These two tests drive the PRODUCTION sweep -- e.sweepSelector, which is what
// selectorLoop calls every 500ms -- and never e.step() or e.deliver(). Those
// two helpers write a liveness straight into e.sel and skip sampleSources
// entirely, so a sweep that reads the WRONG HUB is invisible to every test
// built on them. That is the gap these two exist to close, and it is why the
// bytes below go in through relay.Hub.Deliver rather than through a helper that
// sets the counter the engine is supposed to be reading.

// tsDatagram builds one 1316-byte MPEG-TS datagram: seven 188-byte packets on a
// single PID, sync byte and continuity counter included, so relay.Hub.measure()
// walks the same framing a real ingest hands it.
//
// The counter is continuous within one datagram only. Nothing here reads the
// discontinuity count -- what is being measured is whether the engine notices
// bytes arriving on a particular hub -- but the framing is real so this cannot
// pass against a hub that has stopped parsing what it is given.
func tsDatagram(cc *uint8) []byte {
	pkt := make([]byte, 7*188)
	for i := range 7 {
		p := pkt[i*188:]
		p[0] = 0x47
		p[1] = 0x01
		p[2] = 0x00
		p[3] = 0x10 | (*cc & 0x0f)
		*cc++
	}
	return pkt
}

// deliverTS pushes n datagrams into a hub through relay.Hub.Deliver, which is
// the entry point the one-port SRT listener already uses: it increments the
// hub's byte counter and runs the real fanout and measure path.
//
// Deliver rather than a UDP write to InputURL on purpose. A loopback datagram
// can be dropped by the kernel, and a failover test whose premise is "these
// bytes arrived" must not have a drop in it.
func deliverTS(t *testing.T, h *relay.Hub, n int) {
	t.Helper()
	if h == nil {
		t.Fatal("no hub to deliver into")
	}
	before := h.RxBytes()
	var cc uint8
	for range n {
		h.Deliver(tsDatagram(&cc))
	}
	if h.RxBytes() <= before {
		t.Fatalf("delivered %d datagrams and the hub's counter did not move (%d -> %d)",
			n, before, h.RxBytes())
	}
}

// The failover decision must be taken from the PRIMARY's OWN hub, never from
// the selector's.
//
// The selector hub carries bytes whichever source is on air, so a sweep reading
// it sees an unbroken stream all the way through an outage -- the primary reads
// live forever and the tier never switches. sampleSources says exactly this in
// a comment above the line; nothing anywhere executed it. The two phases below
// are the two halves that make the confusion detectable: bytes on the primary's
// hub only, then bytes on the selector's hub only.
//
// Mutation: selector.go sampleSources, `primaryRx := e.hub.RxBytes()` ->
// `primaryRx := e.downstreamHub().RxBytes()`.
// Observed to fail on the first assertion -- the phase-1 slate -- and to be the
// only failing test in the package.
//
// BOTH DIRECTIONS ARE COVERED, and the note that used to sit here said
// otherwise. It conceded that the mutant above makes the primary read
// permanently DEAD in-process where production would make it read permanently
// ALIVE, and worried that the failure shown is therefore not the failure an
// operator would see. A reviewer measured the other direction and the concession
// was wrong.
//
// Mutation, the production direction: sampleSources
// `primaryRx := e.hub.RxBytes() + uint64(now.UnixNano())`, so the primary reads
// alive whatever it is doing -- the exact shape of "failover never fires again".
//
//	pre-existing suite, these files removed:  ok 36.4s, FULLY GREEN
//	with these files:                         both tests fail, at phase 2
//	  "the primary stopped delivering a full grace window ago and \"primary\" is
//	   still on air: failover never fired, because the sweep is reading a hub that
//	   carries bytes whichever source is up"
//
// Phase 1 catches the dead direction and phase 2 catches the alive one, which is
// why the test has two phases. And the first line of that measurement is the
// reason these two tests exist at all: NOTHING ELSE IN THE REPOSITORY NOTICES
// THAT FAILOVER HAS STOPPED FIRING. The acceptance suite catches the wrong-hub
// mutant by consequence, but its own named check -- switches >= 2 -- passed at
// 80 switches, and it flakes at 25% locally, so a real regression there is not
// distinguishable from noise.
func TestTheFailoverDecisionReadsThePrimarysOwnHub(t *testing.T) {
	e := failoverEngine(t)
	s := failoverOnSettings()
	setSettings(e, s)
	e.reconcileSelector(s, wantSelector(s), "")
	if e.selectorHub() == nil {
		t.Fatal("the source selector did not start, so there is no sweep to measure")
	}

	// Phase 1, and it is the POSITIVE half: the operator's encoder delivers into
	// its own hub and the primary must go on air. An engine that decided nothing
	// at all would fail here rather than sliding through to the phase below.
	t0 := time.Now()
	deliverTS(t, e.hub, 8)
	e.sweepSelector(t0)
	if act := e.Failover().Active; act != sourcePrimary {
		t.Fatalf("the operator's encoder delivered %d bytes into the primary's hub and the "+
			"selector put %q on air instead of the primary: the sweep is not reading the "+
			"hub the primary publishes to", e.hub.RxBytes(), act)
	}

	// Phase 2. The encoder stops. The selector hub keeps counting, because it
	// carries whatever source is on air -- which is precisely why it is the
	// wrong thing to ask about the primary.
	deliverTS(t, e.selectorHub(), 8)
	e.sweepSelector(t0.Add(failoverGrace(s) + time.Second))
	if act := e.Failover().Active; act == sourcePrimary {
		t.Errorf("the primary stopped delivering a full grace window ago and %q is still on "+
			"air: failover never fired, because the sweep is reading a hub that carries "+
			"bytes whichever source is up", act)
	}
}

// The same property from the other side: the tier must come BACK to the primary
// when the operator's encoder returns, and it must learn that from the
// primary's own hub.
//
// A return from the slate is immediate -- chooseFrom's slate branch is not
// subject to the return-stability window, because sitting on a standby card
// while the show is back on air is the worse failure -- so what this measures is
// only whether the returning bytes are seen at all.
//
// Mutation: selector.go sampleSources, `primaryRx := e.hub.RxBytes()` ->
// `primaryRx := e.downstreamHub().RxBytes()`.
// Observed to fail on the SECOND assertion, the return, with the slate still on
// air; the phase-1 assertion passes under the mutant, so this test names the
// return direction specifically. Only failing test in the package alongside the
// one above.
func TestTheTierReturnsToThePrimaryWhenItsOwnHubDeliversAgain(t *testing.T) {
	e := failoverEngine(t)
	s := failoverOnSettings()
	setSettings(e, s)
	e.reconcileSelector(s, wantSelector(s), "")
	if e.selectorHub() == nil {
		t.Fatal("the source selector did not start, so there is no sweep to measure")
	}

	// Positive first: with nothing delivering anywhere the tier must still put
	// SOMETHING on air, and that something is the slate. A tier that decided
	// nothing fails here.
	t0 := time.Now()
	e.sweepSelector(t0)
	if act := e.Failover().Active; act != sourceSlate {
		t.Fatalf("no source is delivering and the tier has %q on air rather than the slate: "+
			"the standby exists so an operator never sees nothing", act)
	}

	deliverTS(t, e.hub, 8)
	e.sweepSelector(t0.Add(time.Second))
	if act := e.Failover().Active; act != sourcePrimary {
		t.Errorf("the operator's encoder is delivering into the primary's hub again and the "+
			"tier is still showing %q: the return is decided from a hub the primary does "+
			"not publish to, so the broadcast stays on the standby card for ever", act)
	}
}
