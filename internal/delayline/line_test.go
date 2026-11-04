package delayline

import (
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recorder collects what a line releases, with when it released it.
type recorder struct {
	mu   sync.Mutex
	seqs []uint64
	at   []time.Time
}

func (r *recorder) out(p []byte) {
	r.mu.Lock()
	r.seqs = append(r.seqs, seqOf(p))
	r.at = append(r.at, time.Now())
	r.mu.Unlock()
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seqs)
}

// sendAll writes n datagrams to the line's port and returns when each was sent.
func sendAll(t *testing.T, l *Line, n int) []time.Time {
	t.Helper()
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: l.Port()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sent := make([]time.Time, n)
	for i := range n {
		sent[i] = time.Now()
		if _, err := conn.Write(datagram(uint64(i))); err != nil {
			t.Fatal(err)
		}
		// Paced, as a relay is: a burst of thousands into loopback UDP can drop
		// in the kernel before the line ever sees it, which is not what is
		// under test here.
		time.Sleep(200 * time.Microsecond)
	}
	return sent
}

// sendWithin is sendAll that stops early, after at least two datagrams, once
// sending has taken budget -- for a test whose assertion depends on the first
// datagram not yet being due when sending ends.
func sendWithin(t *testing.T, l *Line, n int, budget time.Duration) []time.Time {
	t.Helper()
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: l.Port()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sent := make([]time.Time, 0, n)
	for i := range n {
		if i >= 2 && time.Since(sent[0]) >= budget {
			break
		}
		sent = append(sent, time.Now())
		if _, err := conn.Write(datagram(uint64(i))); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Microsecond) // paced, as in sendAll
	}
	return sent
}

func newTestLine(t *testing.T, delay time.Duration, rec *recorder, onDue func()) *Line {
	t.Helper()
	lim := Limits{MemBudget: DefaultMemBudget, DiskBudget: DiskBudgetFor(delay),
		SegmentBytes: DefaultSegmentBytes, Dir: t.TempDir() + "/spill"}
	l, err := Listen(Config{Delay: delay, Limits: lim, Out: rec.out, OnFirstDue: onDue, Lead: 50 * time.Millisecond}, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	return l
}

func waitFor(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// THE LINE IS A DELAY, END TO END: every datagram comes out, in order, and
// none of them sooner than the delay after it went in.
func TestEveryDatagramComesOutInOrderAndNoneSoonerThanTheDelay(t *testing.T) {
	const delay = 250 * time.Millisecond
	rec := &recorder{}
	l := newTestLine(t, delay, rec, nil)
	const n = 400
	sent := sendAll(t, l, n)
	waitFor(t, "every datagram to be released", 5*time.Second, func() bool { return rec.count() == n })

	rec.mu.Lock()
	defer rec.mu.Unlock()
	for i, s := range rec.seqs {
		if s != uint64(i) {
			t.Fatalf("position %d released datagram %d", i, s)
		}
		// releaseSlack is the only licence to be early.
		if early := sent[i].Add(delay - releaseSlack).Sub(rec.at[i]); early > 0 {
			t.Fatalf("datagram %d released %s before its delay was up", i, early)
		}
	}
}

// FLUSH ON STOP. Drain closes the input and lets the tail play out at its
// normal pace: the channel closes only after the last datagram is out, and not
// before the delay since it went in. Input after Drain is not taken.
func TestDrainPlaysTheWholeTailOutOnTimeAndThenSaysSo(t *testing.T) {
	const delay = 300 * time.Millisecond
	rec := &recorder{}
	l := newTestLine(t, delay, rec, nil)
	const n = 200
	sent := sendAll(t, l, n)
	loopbackDelivered(t) // sent is not yet arrived; see the helper
	drained := l.Drain()
	if st := l.Status(); !st.Draining {
		t.Fatal("a draining line does not say so")
	}

	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("the drain never finished")
	}
	if got := rec.count(); got != n {
		t.Fatalf("drain released %d of %d: the tail was cut", got, n)
	}
	if last := sent[n-1].Add(delay - releaseSlack); time.Now().Before(last) {
		t.Fatal("the drain reported done before the last datagram was due")
	}

	// The input is closed: nothing sent now reaches the buffer.
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: l.Port()})
	if err == nil {
		_, _ = conn.Write(datagram(9999))
		conn.Close()
	}
	time.Sleep(delay + 100*time.Millisecond)
	if got := rec.count(); got != n {
		t.Fatalf("a drained line released %d datagrams, want %d", got, n)
	}
}

// DRAIN KEEPS WHAT HAD ALREADY ARRIVED. A datagram the hub delivered before
// the stop is programme, even if the line's reader had not got to it yet: it
// is sitting in the line's own socket. Drain used to close that socket at once,
// and closing a socket discards its receive queue, so the last moments before
// the stop were cut -- exactly what Drain promises will not happen.
//
// The burst is unpaced so that the reader is reliably behind when Drain is
// called, which is the condition and not an accident of scheduling. It is kept
// small (50 x 1316 bytes) so it fits the smallest default receive buffer of
// the three CI platforms with room to spare: a datagram the KERNEL dropped
// never arrived, and is not what this is about.
func TestDrainKeepsWhatHadArrivedButHadNotYetBeenRead(t *testing.T) {
	const delay = 100 * time.Millisecond
	rec := &recorder{}
	l := newTestLine(t, delay, rec, nil)
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: l.Port()})
	if err != nil {
		t.Fatal(err)
	}
	const n = 50
	for i := range n {
		if _, err := conn.Write(datagram(uint64(i))); err != nil {
			t.Fatal(err)
		}
	}
	_ = conn.Close()
	loopbackDelivered(t)
	queued := l.Ingested()
	select {
	case <-l.Drain():
	case <-time.After(5 * time.Second):
		t.Fatal("the drain never finished")
	}
	if got := rec.count(); got != n {
		t.Fatalf("drain released %d of %d (the reader had taken in %d when Drain was called): "+
			"datagrams already in the line's socket were thrown away", got, n, queued)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for i, s := range rec.seqs {
		if s != uint64(i) {
			t.Fatalf("position %d released datagram %d", i, s)
		}
	}
}

// loopbackDelivered returns once every datagram this process has already
// written to the loopback has reached its socket.
//
// Write returning is not that. On macOS the loopback hands a datagram to lo0's
// input queue and the socket gets it later, from another thread; a test that
// writes and then acts at once is acting on datagrams that are still in
// transit -- measured as a drain that kept 14 of 50, with the other 36
// arriving after the line had found its socket empty. lo0's input queue is
// FIFO, so a probe sent last and received here means everything sent before it
// has been delivered. On Linux and Windows delivery happens inside the send,
// and this costs one round trip.
func loopbackDelivered(t *testing.T) {
	t.Helper()
	probe, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	if _, err := probe.WriteToUDP([]byte{1}, probe.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	_ = probe.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := probe.ReadFromUDP(make([]byte, 1)); err != nil {
		t.Fatalf("the loopback probe never arrived: %v", err)
	}
}

// DRAINING AN EMPTY LINE FINISHES AT ONCE: a destination stopped before
// anything arrived has nothing to flush and must not wait out the delay.
func TestDrainingAnEmptyLineFinishesAtOnce(t *testing.T) {
	rec := &recorder{}
	l := newTestLine(t, 10*time.Minute, rec, nil)
	select {
	case <-l.Drain():
	case <-time.After(2 * time.Second):
		t.Fatal("an empty line's drain did not finish")
	}
}

// STOP NOW. Close cuts the tail: nothing more is released, the drain channel
// closes so a waiting teardown proceeds, and the spill directory is gone.
func TestCloseCutsTheTailAndDeletesTheSpill(t *testing.T) {
	const delay = 400 * time.Millisecond
	rec := &recorder{}
	l := newTestLine(t, delay, rec, nil)
	sendAll(t, l, 100)
	drained := l.Drain()
	l.Close()
	select {
	case <-drained:
	default:
		t.Fatal("Close left a drain waiting")
	}
	time.Sleep(delay + 150*time.Millisecond)
	if got := rec.count(); got != 0 {
		t.Fatalf("a line cut before anything was due released %d datagrams", got)
	}
	if _, err := os.Stat(l.cfg.Limits.Dir); !os.IsNotExist(err) {
		t.Fatalf("the spill directory survived Close: %v", err)
	}
	l.Close() // idempotent
}

// THE DESTINATION STARTS LATE, ONCE. OnFirstDue is the engine's cue to spawn
// FFmpeg, so it must fire once, Lead before the first release, and not at the
// moment data first arrives.
func TestOnFirstDueFiresOnceJustBeforeTheFirstReleaseNotWhenDataArrives(t *testing.T) {
	const delay = 300 * time.Millisecond
	rec := &recorder{}
	var fired atomic.Int32
	var firedAt atomic.Int64
	var l *Line
	l = newTestLine(t, delay, rec, func() {
		fired.Add(1)
		firedAt.Store(time.Now().UnixNano())
		if rec.count() != 0 {
			t.Error("OnFirstDue fired after the first release")
		}
		if l.Status().Releasing {
			t.Error("OnFirstDue fired after the line started releasing")
		}
	})
	sent := sendAll(t, l, 50)
	waitFor(t, "release", 3*time.Second, func() bool { return rec.count() == 50 })
	if fired.Load() != 1 {
		t.Fatalf("OnFirstDue fired %d times", fired.Load())
	}
	earliest := sent[0].Add(delay - l.cfg.Lead - 20*time.Millisecond)
	if at := time.Unix(0, firedAt.Load()); at.Before(earliest) {
		t.Fatalf("OnFirstDue fired %s after the first datagram arrived; it is meant to wait for the delay",
			at.Sub(sent[0]))
	}
	if !l.Status().Releasing {
		t.Fatal("a line that has released does not say so")
	}
}

// THE STATUS LINE: fill climbs towards 1 while buffering, and a draining line
// says how long its tail has left.
func TestStatusReportsFillWhileBufferingAndTimeLeftWhileDraining(t *testing.T) {
	const delay = 2 * time.Second
	rec := &recorder{}
	l := newTestLine(t, delay, rec, nil)
	// SENT FOR A SHARE OF THE DELAY, NOT A COUNT. This was 1500 datagrams at
	// 200 µs, ~0.3 s here -- but each sleep is a floor, and on a loaded CI
	// runner the 1500 of them took past the whole 2 s delay. The first datagram
	// was then due, the line was releasing, and "a line still inside its delay
	// says it is releasing" failed a line that was correctly NOT inside its
	// delay any more. Stopping at a quarter of the delay keeps the status read
	// below well inside it however slow the sender is.
	sent := sendWithin(t, l, 1500, delay/4)
	// A UDP write only queues the datagram; wait for the LINE to hold at least
	// two of them, and assert on that same snapshot. The premise is checked
	// after each read rather than assumed: Releasing is only wrong while the
	// first datagram is not yet due, and the earliest it may go is its delay
	// less releaseSlack -- counted from when it was sent, which is no later
	// than when the line received it.
	var st Status
	for {
		st = l.Status()
		if since := time.Since(sent[0]); since >= delay-releaseSlack {
			t.Fatalf("the status was read %v after the first datagram, past the earliest "+
				"it may be released (%v), so this line was never observed inside its delay "+
				"(last status %+v)", since, delay-releaseSlack, st)
		}
		if st.Packets >= 2 && st.Span > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if st.Releasing {
		t.Fatal("a line still inside its delay says it is releasing")
	}
	if st.Fill <= 0 || st.Fill >= 1 {
		t.Fatalf("fill while buffering = %v, want between 0 and 1", st.Fill)
	}
	if st.Delay != delay || st.Packets == 0 || st.MemBytes == 0 {
		t.Fatalf("status = %+v", st)
	}
	l.Drain()
	st = l.Status()
	if !st.Draining || st.DrainRemaining <= 0 || st.DrainRemaining > delay {
		t.Fatalf("draining status = %+v", st)
	}
}

func TestListenRefusesADelayOutsideTheAgreedRange(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second, MaxDelay + time.Second} {
		if _, err := Listen(Config{Delay: d, Out: func([]byte) {}}, 0); err == nil {
			t.Errorf("delay %s accepted", d)
		}
	}
	if _, err := Listen(Config{Delay: time.Second}, 0); err == nil {
		t.Error("a line with no output accepted")
	}
}

// A CRASHED RUN'S SPILL IS NOT REPLAYED. Segments left in the directory by a
// previous process are the old programme; Listen clears them.
func TestListenClearsSegmentsLeftByAPreviousRun(t *testing.T) {
	dir := t.TempDir() + "/spill"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := dir + "/seg-000000000001.bin"
	if err := os.WriteFile(stale, []byte("yesterday"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Listen(Config{Delay: time.Second, Out: func([]byte) {},
		Limits: Limits{MemBudget: DefaultMemBudget, DiskBudget: DiskBudgetFor(time.Second),
			SegmentBytes: DefaultSegmentBytes, Dir: dir}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("a stale segment survived Listen")
	}
}

// A LINE THAT IS DROPPING SAYS SO, while it is still filling and the pump is
// asleep, and once rather than once a datagram.
func TestALineThatRunsOutOfRoomReportsItOnceWhileStillFilling(t *testing.T) {
	var reports atomic.Int32
	var reason atomic.Value
	lim := Limits{MemBudget: DefaultSegmentBytes, SegmentBytes: DefaultSegmentBytes}
	l, err := Listen(Config{Delay: 10 * time.Minute, Limits: lim, Out: func([]byte) {},
		OnDrop: func(err error) {
			reports.Add(1)
			if err != nil {
				reason.Store(err.Error())
			}
		}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	// Spilling is off and the budget is one segment, so a few segments in the
	// line is out of room.
	sendAll(t, l, 4000)
	waitFor(t, "a drop report", 3*time.Second, func() bool { return reports.Load() > 0 })
	time.Sleep(100 * time.Millisecond)
	if n := reports.Load(); n != 1 {
		t.Fatalf("OnDrop called %d times inside one report window", n)
	}
	if r, _ := reason.Load().(string); r == "" {
		t.Fatal("the drop report carried no reason")
	}
}

// INGESTED COUNTS EVERY DATAGRAM THE LINE HAS TAKEN IN. A test that stops a
// feed uses it to know nothing is still on its way in.
//
// Mutation: never count, or count only the pushes that succeed.
func TestIngestedCountsEveryDatagramPushed(t *testing.T) {
	rec := &recorder{}
	l := newTestLine(t, 2*time.Second, rec, nil)
	if got := l.Ingested(); got != 0 {
		t.Fatalf("a fresh line has ingested %d", got)
	}
	sendAll(t, l, 5)
	waitFor(t, "the line to ingest all five", 5*time.Second, func() bool { return l.Ingested() >= 5 })
	if got := l.Ingested(); got != 5 {
		t.Fatalf("ingested %d, want 5", got)
	}
}
