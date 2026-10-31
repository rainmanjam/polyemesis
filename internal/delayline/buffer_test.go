package delayline

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// datagram is a 1316-byte relay datagram whose first eight bytes are its
// sequence number, so order and completeness can be checked on the far side.
func datagram(seq uint64) []byte {
	p := make([]byte, 1316)
	binary.BigEndian.PutUint64(p, seq)
	p[1315] = byte(seq)
	return p
}

func seqOf(p []byte) uint64 { return binary.BigEndian.Uint64(p) }

func testLimits(t *testing.T) Limits {
	t.Helper()
	return Limits{
		MemBudget:    DefaultMemBudget,
		DiskBudget:   DiskBudgetFor(MaxDelay),
		SegmentBytes: DefaultSegmentBytes,
		Dir:          t.TempDir(),
	}
}

func spillFiles(t *testing.T, dir string) int {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "seg-*.bin"))
	if err != nil {
		t.Fatal(err)
	}
	return len(m)
}

// THE BOUNDS, AT THE CEILING. Fifteen minutes of a 20 Mbit/s feed is 2.25 GB,
// which is the case the plan said had to be bounded. It runs in virtual time:
// the buffer takes its clock as an argument, so a quarter of an hour of
// datagrams costs only the I/O of writing them once.
//
// What is asserted is the design's own arithmetic, not a loose "it did not
// blow up": RAM never above MemBudget + 2*SegmentBytes, disk never above
// DiskBudget, every datagram out exactly once and in order, none early, and no
// spill file left behind once the buffer has drained.
func TestFifteenMinutesAtTwentyMegabitsStaysInsideTheRAMAndDiskBoundsAndComesOutWhole(t *testing.T) {
	lim := testLimits(t)
	epoch := time.Unix(1_700_000_000, 0)
	b, err := NewBuffer(MaxDelay, lim, epoch)
	if err != nil {
		t.Fatal(err)
	}
	// 20 Mbit/s of 1316-byte datagrams is ~1,900 a second.
	const perSecond = 20_000_000 / 8 / 1316
	step := time.Second / perSecond
	// Fill the delay, then run two minutes past it so the steady state --
	// spilling at the tail while reading back at the head -- is exercised.
	total := uint64((MaxDelay + 2*time.Minute) / step)
	var next uint64 // the next sequence number expected out
	var out [][]byte
	maxSpill := 0
	for seq := range total {
		now := epoch.Add(time.Duration(seq) * step)
		if err := b.Push(now, datagram(seq)); err != nil {
			t.Fatal(err)
		}
		if seq%(perSecond*10) == 0 {
			// Not every datagram: globbing the spill directory 3.4 million
			// times would be the whole cost of the test.
			if n := spillFiles(t, lim.Dir); n > maxSpill {
				maxSpill = n
			}
		}
		out = b.Pop(now, out[:0])
		for _, p := range out {
			got := seqOf(p)
			if got != next {
				t.Fatalf("released %d, expected %d: out of order or lost", got, next)
			}
			// Never early: datagram n arrived at n*step and may not leave
			// before n*step + delay.
			if due := epoch.Add(time.Duration(got)*step + MaxDelay); now.Before(due) {
				t.Fatalf("datagram %d released at %s, due %s: the delay was shortened", got, now, due)
			}
			next++
		}
	}
	// Drain the tail on the clock, as a stop does.
	end := epoch.Add(time.Duration(total)*step + MaxDelay)
	out = b.Pop(end, out[:0])
	for _, p := range out {
		if got := seqOf(p); got != next {
			t.Fatalf("tail released %d, expected %d", got, next)
		}
		next++
	}
	if next != total {
		t.Fatalf("released %d of %d datagrams", next, total)
	}

	st := b.Stats()
	memCeiling := lim.MemBudget + 2*int64(lim.SegmentBytes)
	if st.PeakMemBytes > memCeiling {
		t.Errorf("RAM peaked at %d bytes, over the %d ceiling", st.PeakMemBytes, memCeiling)
	}
	if st.PeakDiskBytes > lim.DiskBudget {
		t.Errorf("disk peaked at %d bytes, over the %d budget", st.PeakDiskBytes, lim.DiskBudget)
	}
	// It did spill: a 2.25 GB delay that never touched the disk would mean the
	// RAM bound is not what is holding it.
	if st.PeakDiskBytes < 1<<30 {
		t.Errorf("only %d bytes ever spilled; fifteen minutes at 20 Mbit/s should spill gigabytes", st.PeakDiskBytes)
	}
	if maxSpill == 0 {
		t.Error("no spill file was ever seen in the spill directory")
	}
	if st.DroppedPackets != 0 {
		t.Errorf("%d datagrams dropped inside the budgets", st.DroppedPackets)
	}
	if n := spillFiles(t, lim.Dir); n != 0 {
		t.Errorf("%d spill files left behind after the buffer drained", n)
	}
	if st.MemBytes != 0 || st.DiskBytes != 0 || st.Packets != 0 {
		t.Errorf("drained buffer still accounts mem=%d disk=%d packets=%d", st.MemBytes, st.DiskBytes, st.Packets)
	}
}

// A SHORT DELAY NEVER TOUCHES THE DISK. Thirty seconds at 8 Mbit/s is 30 MB,
// inside the RAM budget, which is the "up to about a minute in RAM" half of the
// agreed design.
func TestThirtySecondsAtEightMegabitsIsHeldEntirelyInRAM(t *testing.T) {
	lim := testLimits(t)
	epoch := time.Unix(0, 0)
	b, err := NewBuffer(30*time.Second, lim, epoch)
	if err != nil {
		t.Fatal(err)
	}
	const perSecond = 8_000_000 / 8 / 1316
	step := time.Second / perSecond
	var out [][]byte
	for seq := range uint64(perSecond * 90) {
		now := epoch.Add(time.Duration(seq) * step)
		if err := b.Push(now, datagram(seq)); err != nil {
			t.Fatal(err)
		}
		out = b.Pop(now, out[:0])
	}
	if st := b.Stats(); st.PeakDiskBytes != 0 {
		t.Fatalf("a 30 s delay at 8 Mbit/s spilled %d bytes; it fits the RAM budget", st.PeakDiskBytes)
	}
}

// OVERFLOW KEEPS THE DELAY AND LOSES DATA, NEVER THE OTHER WAY ROUND. With the
// disk budget at a few segments the buffer cannot hold the delay, and what it
// must not do is release old data early to make room. The datagrams that come
// out are all on time; the ones that did not fit are counted as dropped.
func TestABufferThatOutgrowsItsBudgetsDropsNewDataRatherThanShorteningTheDelay(t *testing.T) {
	lim := testLimits(t)
	lim.MemBudget = 2 * int64(lim.SegmentBytes)
	lim.DiskBudget = 3 * int64(lim.SegmentBytes)
	epoch := time.Unix(0, 0)
	delay := 60 * time.Second
	b, err := NewBuffer(delay, lim, epoch)
	if err != nil {
		t.Fatal(err)
	}
	const perSecond = 8_000_000 / 8 / 1316
	step := time.Second / perSecond
	var out [][]byte
	released := 0
	total := uint64(perSecond * 120)
	for seq := range total {
		now := epoch.Add(time.Duration(seq) * step)
		if err := b.Push(now, datagram(seq)); err != nil {
			t.Fatal(err)
		}
		out = b.Pop(now, out[:0])
		for _, p := range out {
			if due := epoch.Add(time.Duration(seqOf(p))*step + delay); now.Before(due) {
				t.Fatalf("datagram %d left early to make room", seqOf(p))
			}
			released++
		}
	}
	st := b.Stats()
	if st.DroppedPackets == 0 {
		t.Fatal("a buffer far over both budgets dropped nothing")
	}
	if st.PeakDiskBytes > lim.DiskBudget {
		t.Fatalf("disk peaked at %d, over the %d budget", st.PeakDiskBytes, lim.DiskBudget)
	}
	if ceiling := lim.MemBudget + 2*int64(lim.SegmentBytes); st.PeakMemBytes > ceiling {
		t.Fatalf("RAM peaked at %d, over the %d ceiling", st.PeakMemBytes, ceiling)
	}
	if b.Err() == nil {
		t.Fatal("a buffer that is dropping reports no reason")
	}
	if uint64(released)+st.DroppedPackets+uint64(st.Packets) != total {
		t.Fatalf("released %d + dropped %d + held %d != pushed %d",
			released, st.DroppedPackets, st.Packets, total)
	}
}

// THE DISK GUARD IS ASKED BEFORE EVERY SPILL, and a refusal drops the segment
// rather than writing it. This is the hook the engine wires to the data
// directory's free space.
func TestTheDiskGuardIsAskedBeforeEverySpillAndItsRefusalDropsTheSegment(t *testing.T) {
	lim := testLimits(t)
	lim.MemBudget = int64(lim.SegmentBytes)
	asked := 0
	refusal := errors.New("the data directory is below its free-space floor")
	lim.Room = func(n int64) error {
		asked++
		if n <= 0 {
			t.Errorf("the guard was asked about %d bytes", n)
		}
		return refusal
	}
	epoch := time.Unix(0, 0)
	b, err := NewBuffer(10*time.Minute, lim, epoch)
	if err != nil {
		t.Fatal(err)
	}
	for seq := range uint64(5000) {
		if err := b.Push(epoch.Add(time.Duration(seq)*time.Millisecond), datagram(seq)); err != nil {
			t.Fatal(err)
		}
	}
	if asked == 0 {
		t.Fatal("the disk guard was never consulted")
	}
	if n := spillFiles(t, lim.Dir); n != 0 {
		t.Fatalf("%d segments were written past a refusing disk guard", n)
	}
	if !errors.Is(b.Err(), refusal) {
		t.Fatalf("the status reason is %v, want the guard's refusal", b.Err())
	}
	if b.Stats().DroppedPackets == 0 {
		t.Fatal("refused segments were not counted as dropped")
	}
}

// DISCARD IS "STOP NOW": everything held goes, including every spilled file.
func TestDiscardDropsEverythingHeldAndDeletesTheSpill(t *testing.T) {
	lim := testLimits(t)
	lim.MemBudget = int64(lim.SegmentBytes)
	epoch := time.Unix(0, 0)
	b, err := NewBuffer(10*time.Minute, lim, epoch)
	if err != nil {
		t.Fatal(err)
	}
	for seq := range uint64(8000) {
		if err := b.Push(epoch.Add(time.Duration(seq)*time.Millisecond), datagram(seq)); err != nil {
			t.Fatal(err)
		}
	}
	if spillFiles(t, lim.Dir) == 0 {
		t.Fatal("setup: nothing spilled")
	}
	b.Discard()
	if n := spillFiles(t, lim.Dir); n != 0 {
		t.Fatalf("%d spill files survived Discard", n)
	}
	if _, ok := b.NextDue(); ok {
		t.Fatal("a discarded buffer still has something due")
	}
	if got := b.Pop(epoch.Add(time.Hour), nil); len(got) != 0 {
		t.Fatalf("a discarded buffer released %d datagrams", len(got))
	}
}

// A spilled segment that cannot be read back is skipped and counted, not a
// wedge: the line behind it keeps flowing.
func TestASpilledSegmentThatCannotBeReadBackIsCountedAndSkipped(t *testing.T) {
	lim := testLimits(t)
	lim.MemBudget = int64(lim.SegmentBytes)
	epoch := time.Unix(0, 0)
	delay := time.Minute
	b, err := NewBuffer(delay, lim, epoch)
	if err != nil {
		t.Fatal(err)
	}
	const n = 8000
	for seq := range uint64(n) {
		if err := b.Push(epoch.Add(time.Duration(seq)*time.Millisecond), datagram(seq)); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(lim.Dir, "seg-*.bin"))
	if len(files) == 0 {
		t.Fatal("setup: nothing spilled")
	}
	if err := os.Remove(files[0]); err != nil {
		t.Fatal(err)
	}
	got := b.Pop(epoch.Add(time.Hour), nil)
	if len(got) == 0 || len(got) >= n {
		t.Fatalf("released %d of %d with one segment lost", len(got), n)
	}
	if st := b.Stats(); uint64(len(got))+st.DroppedPackets != n {
		t.Fatalf("released %d + dropped %d != %d", len(got), st.DroppedPackets, n)
	}
	if b.Err() == nil {
		t.Fatal("a lost segment left no reason")
	}
}

func TestPushRefusesWhatTheFramingCannotHoldAndIgnoresEmptyDatagrams(t *testing.T) {
	b, err := NewBuffer(time.Second, testLimits(t), time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Push(time.Unix(0, 0), make([]byte, maxDatagram+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized datagram: %v", err)
	}
	if err := b.Push(time.Unix(0, 0), nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.NextDue(); ok {
		t.Fatal("an empty datagram was stored")
	}
	big := bytes.Repeat([]byte{1}, maxDatagram)
	if err := b.Push(time.Unix(0, 0), big); err != nil {
		t.Fatal(err)
	}
	if got := b.Pop(time.Unix(1, 0), nil); len(got) != 1 || !bytes.Equal(got[0], big) {
		t.Fatal("the largest datagram did not round-trip")
	}
}

func TestNewBufferRefusesAConfigurationItCannotBound(t *testing.T) {
	lim := testLimits(t)
	if _, err := NewBuffer(0, lim, time.Time{}); err == nil {
		t.Error("a zero delay was accepted")
	}
	small := lim
	small.SegmentBytes = 2048
	if _, err := NewBuffer(time.Second, small, time.Time{}); err == nil {
		t.Error("a segment smaller than the largest datagram was accepted")
	}
	noDir := lim
	noDir.Dir = ""
	if _, err := NewBuffer(time.Second, noDir, time.Time{}); err == nil {
		t.Error("a disk budget with no spill directory was accepted")
	}
}
