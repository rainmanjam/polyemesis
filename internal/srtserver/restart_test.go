package srtserver

import (
	"fmt"
	"strings"
	"testing"

	srt "github.com/datarhei/gosrt"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// Nothing in this package called Start twice before this file existed, which is
// exactly why the hazard below survived: every test builds a fresh Server, and
// so does reconcileListener in internal/engine, so the only route to the bug was
// an operator or a supervisor retrying a listener that came up degraded -- the
// one path no test walked.
//
// THIS FILE IS RUNG 3 (DETECTION), and saying so is the point of writing it
// down. A test is successive inspection: it finds the mistake after somebody has
// already made it, in CI, on a run that has to be looked at. It is not the
// device, and no test can be -- a rung-1 label on a test file would tell the
// next reader that the hazard is closed by construction when what is actually
// closed is one commit's worth of it.
//
// What is on the higher rungs is in srtserver.go, and the two are on different
// ones: the started interlock in Start is rung 2 (a second Start is refused and
// announced, but the call is still expressible), and the local `bound` slice is
// rung 1 (the success check cannot be asked about another attempt's work,
// because that work is not in scope). Both are priced in the comment on Start.
//
// The floor these tests hold is real and it is a floor, and the mapping from
// device to test is NOT one-to-one -- measured, not assumed:
//
//	removing the started interlock      fails TWO (SecondStartIsRefused, and
//	                                    StartThatBoundNothingDoesNotLatch,
//	                                    because both are about the interlock:
//	                                    one that it refuses, one that it does
//	                                    not latch on a Start that bound nothing)
//	reverting the local `bound` slice   fails Residue
//
// Said the other way round, which is the way that matters when one of these
// goes red: two failures here point at the interlock and one points at the
// slice, so the failing set names the device before anyone opens the diff.
//
// An earlier draft of this comment claimed each mutation "breaks exactly one of
// them and not the others". It does not, and the claim is recorded here rather
// than quietly deleted because this file exists to fix a hazard that WAS a
// comment nobody had checked. A comment about mutation coverage that has not
// itself been run against the code is the same mistake in a smaller box.

// unbindableAddr is an address the bind must fail on, on every host, every time.
//
// The alternative staging is to hold the port with a reservation and let Start
// collide with it, and internal/testenv/ports.go already explains why that is
// not a proof: gosrt sets SO_REUSEADDR on its listener and net.ListenPacket
// does not, so the two do not even agree on what "in use" means, and the
// collision is a property of the host rather than of the code. 192.0.2.0/24 is
// TEST-NET-1 (RFC 5737), never assigned to an interface, so bind() returns
// EADDRNOTAVAIL deterministically and the failing-Start condition is staged
// without a race and without a skip.
const unbindableAddr = "192.0.2.1:59999"

// A second Start is refused outright, rather than binding against this server's
// own sockets and calling the collision a success.
//
// THE MISTAKE THIS PREVENTS, in the order it actually happens: a wildcard Start
// binds 0.0.0.0:P and fails on [::]:P because something else holds it, which is
// survivable and is reported as degraded. The holder goes away. Somebody -- a
// person, or a health check written next year -- calls Start again on the same
// Server to pick up the family that was missing. Before the guard, that second
// call bound [::]:P, collided with its OWN 0.0.0.0:P socket from the first call,
// and then reported success, because the success check counted the listeners the
// FIRST call had left in the field. What came out of it was a report claiming
// 0.0.0.0 was down while publishers were actively connected over it, plus a
// second Serve goroutine on every socket.
//
// The assertions are deliberately about all three consequences and not only the
// returned error, because an error is cheap to produce and is not what makes the
// listener safe. What makes it safe is that the refused call changed NOTHING:
// same report, same working socket.
func TestASecondStartIsRefusedInsteadOfBindingOverItself(t *testing.T) {
	sink := &recorder{}
	tg := Target{SourceID: 7, Name: "horizontal", Enabled: true, Sink: sink}

	// A wildcard address, because two sockets on one port is the shape that
	// makes the self-collision possible at all; an explicit host binds one
	// socket and could never have exhibited it.
	res := testenv.ReserveUDP(t)
	port := res.Port()
	lookup := ConstantTimeLookup(
		func() []Target { return []Target{tg} },
		func(Target) []string { return []string{tokenFor(tg)} },
	)
	s := New(quietLog(), fmt.Sprintf(":%d", port), lookup)
	res.Release()

	// POSITIVE CONTROL. A Start that refused every call would satisfy every
	// assertion below about the second one, so the first has to be shown to
	// have genuinely worked -- and it is shown twice: it returns nil here, and
	// it admits a real publisher at the end of the test.
	if err := s.Start(); err != nil {
		t.Fatalf("the first Start failed, so nothing below is testing a SECOND "+
			"one: %v", err)
	}
	t.Cleanup(s.Stop)

	before := s.Report()
	if len(before.Bound) == 0 {
		t.Fatalf("the first Start bound no address family and still returned nil; "+
			"report = %+v", before)
	}

	err := s.Start()
	if err == nil {
		t.Fatal("a second Start on an already-started listener returned nil. " +
			"That call rebinds the families the first call missed, collides with " +
			"the first call's own sockets on the ones it got, and reports the " +
			"working families as down -- see the comment on Start.")
	}
	// The error has to name the mistake, because the person reading it is the
	// one who just made it and the remedy is theirs: build a new Server, or Stop
	// this one. An error saying only "failed" sends them to look at the port.
	if !strings.Contains(err.Error(), "already") {
		t.Errorf("the refusal was %q, which never says the listener was already "+
			"started -- and that is the one fact the caller needs", err)
	}

	// THE REPORT IS UNTOUCHED. This is the assertion that catches the real
	// damage: the old second call overwrote the report with one describing its
	// own collisions, so a family that was serving publishers appeared failed.
	after := s.Report()
	if len(after.Bound) != len(before.Bound) {
		t.Errorf("the refused Start changed Bound from %v to %v; a call that bound "+
			"nothing must not be able to rewrite what did", before.Bound, after.Bound)
	}
	for i, addr := range before.Bound {
		if i < len(after.Bound) && after.Bound[i] != addr {
			t.Errorf("the refused Start changed Bound[%d] from %q to %q",
				i, addr, after.Bound[i])
		}
	}
	if len(after.Failed) != len(before.Failed) {
		t.Errorf("the refused Start changed Failed from %+v to %+v, so it is "+
			"reporting families as broken that this server is serving on",
			before.Failed, after.Failed)
	}

	// THE LISTENER STILL WORKS. Refusing the call must be a no-op on the running
	// server rather than a way to break it: an encoder connecting after the
	// mistaken retry has to be admitted exactly as before.
	conn, err := dial(t, fmt.Sprintf("127.0.0.1:%d", port), tokenFor(tg))
	if err != nil {
		t.Fatalf("a publisher could not reach the listener after a second Start was "+
			"refused; the refusal must leave the running server alone: %v", err)
	}
	conn.Close()
}

// A Start that bound nothing hands the interlock back, so the retry an operator
// actually wants is not refused as a double start.
//
// The guard above is a lifecycle interlock, and an interlock that latches on
// FAILURE is its own outage: a listener that lost a port race at boot would
// refuse to start for the life of the process, and the only remedy would be
// restarting the whole server. The device has to refuse a second START, not a
// second ATTEMPT, and those differ by exactly one line -- handing the flag back
// on the path that bound nothing.
//
// Asserted through the error the retry returns rather than through a port
// becoming free, so there is no race in it: the retry still cannot bind
// TEST-NET-1, and what is being checked is WHICH refusal it gets. The bind
// error means the interlock let it through and the socket said no; the
// already-started error means the interlock latched.
func TestAStartThatBoundNothingDoesNotLatchTheInterlock(t *testing.T) {
	lookup := ConstantTimeLookup(
		func() []Target { return nil },
		func(Target) []string { return nil },
	)
	s := New(quietLog(), unbindableAddr, lookup)

	first := s.Start()
	if first == nil {
		s.Stop()
		t.Fatalf("Start bound %s, which RFC 5737 says is on no interface; the "+
			"failing-Start condition could not be staged", unbindableAddr)
	}
	if s.started.Load() {
		t.Error("started stayed true after a Start that bound nothing, so this " +
			"Server has claimed a lifecycle it never entered and Stop will now " +
			"try to shut down listeners that do not exist")
	}

	second := s.Start()
	if second == nil {
		s.Stop()
		t.Fatalf("the retry bound %s, so the assertion below proves nothing", unbindableAddr)
	}
	if strings.Contains(second.Error(), "already") {
		t.Fatalf("the retry after a failed Start was refused as a double start: %v\n"+
			"A Start that bound nothing claimed nothing. Latching there turns one "+
			"lost port race at boot into a listener that never comes up at all.", second)
	}

	// POSITIVE CONTROL for that last assertion, which a Start that could never
	// produce an already-started error would pass trivially. The same phrase has
	// to be reachable on a server that DID start, or the check above is asserting
	// against a string that does not exist.
	res := testenv.ReserveUDP(t)
	live := New(quietLog(), fmt.Sprintf("127.0.0.1:%d", res.Port()), lookup)
	res.Release()
	if err := live.Start(); err != nil {
		t.Fatalf("control: Start on a free loopback port failed: %v", err)
	}
	t.Cleanup(live.Stop)
	if err := live.Start(); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("control: a second Start on a started listener returned %v, so "+
			"the already-started refusal is not reachable and the retry check above "+
			"is vacuous", err)
	}
}

// A listener left over from an earlier attempt cannot make a Start that bound
// nothing report success.
//
// WHITE-BOX ON PURPOSE, and the staging is the reason. The success check used to
// ask `len(s.srvs) == 0` -- of the FIELD, which Start appended to and only Stop
// ever cleared -- so any entry surviving a previous call answered the question
// on behalf of the current one. The interlock in
// TestASecondStartIsRefusedInsteadOfBindingOverItself now closes the public
// route into that state, which leaves putting the residue in the field directly
// as the only honest way to prove the check itself was fixed.
//
// Both devices are kept and both are tested alone, because they stop different
// mistakes. If the interlock is ever relaxed -- a restart in place, a rebind
// that keeps one family -- this is what stops the old bug returning with it.
func TestResidueFromAnEarlierBindCannotFakeASuccessfulStart(t *testing.T) {
	lookup := ConstantTimeLookup(
		func() []Target { return nil },
		func(Target) []string { return nil },
	)

	// A REAL listener on a different port, standing in for what an earlier Start
	// would have left behind. Real rather than a zero value, because a regression
	// here goes on to Serve whatever it finds, and a fabricated server would turn
	// a clear test failure into a panic in a goroutine.
	stale := testenv.ReserveUDP(t)
	staleAddr := fmt.Sprintf("127.0.0.1:%d", stale.Port())
	owner := New(quietLog(), staleAddr, lookup)
	stale.Release()
	leftover, err := owner.listenOn(staleAddr)
	if err != nil {
		t.Fatalf("could not bind %s to stage the residue: %v", staleAddr, err)
	}
	t.Cleanup(func() { leftover.Shutdown() })

	s := New(quietLog(), unbindableAddr, lookup)
	s.srvs = []*srt.Server{leftover}

	if err := s.Start(); err == nil {
		s.Stop()
		t.Fatalf("Start on %s returned nil having bound nothing, because a listener "+
			"left in s.srvs by an earlier attempt answered the \"did anything bind\" "+
			"question for it. Report = %+v\n"+
			"The success check has to be about what THIS attempt bound.",
			unbindableAddr, s.Report())
	}
	if rep := s.Report(); len(rep.Bound) != 0 {
		t.Errorf("Start reported %v as bound on an address RFC 5737 says is on no "+
			"interface; the residue is being counted as this attempt's work", rep.Bound)
	}

	// POSITIVE CONTROL. Every assertion above is satisfied by a Start that always
	// fails, so a server carrying the SAME residue has to succeed on an address
	// that can be bound -- and its report must describe only what it bound, with
	// the stale listener's address nowhere in it.
	res := testenv.ReserveUDP(t)
	goodAddr := fmt.Sprintf("127.0.0.1:%d", res.Port())
	ok := New(quietLog(), goodAddr, lookup)
	ok.srvs = []*srt.Server{leftover}
	res.Release()
	if err := ok.Start(); err != nil {
		t.Fatalf("control: Start on a free loopback port failed with residue in "+
			"place (%v), so the failure above cannot be attributed to the residue "+
			"handling", err)
	}
	t.Cleanup(ok.Stop)
	rep := ok.Report()
	if len(rep.Bound) != 1 || rep.Bound[0] != goodAddr {
		t.Errorf("control: Bound = %v, want exactly [%s]; the report must describe "+
			"this attempt and nothing carried over", rep.Bound, goodAddr)
	}
	for _, addr := range rep.Bound {
		if addr == staleAddr {
			t.Errorf("the stale listener's address %q appeared in the report of a "+
				"Start that never bound it", staleAddr)
		}
	}
	// THE FIELD IS ASSIGNED, NOT APPENDED TO. Checked directly, because the
	// report above cannot see it: appending would leave the stale listener in
	// s.srvs, where the next Stop shuts down a socket this Server never opened
	// and the next Start counts it as its own. That is the field-level shape of
	// the bug, and asserting only on the report leaves it uncovered.
	if len(ok.srvs) != 1 {
		t.Errorf("s.srvs holds %d listeners after a Start that bound one; the "+
			"earlier attempt's listener was carried over instead of replaced",
			len(ok.srvs))
	}
	for _, srv := range ok.srvs {
		if srv == leftover {
			t.Error("the stale listener is still in s.srvs after a fresh Start; " +
				"Stop would now shut down a socket this Server never opened")
		}
	}
}
