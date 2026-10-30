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
	sendAll(t, l, 1500) // ~0.3 s of datagrams at 200 µs
	st := l.Status()
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
