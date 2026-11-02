package delayline

import (
	"os"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/delayline/delaytest"
)

// audioLead is how far ahead of its timestamp the A1 fixture muxes audio.
const audioLead = 200 * time.Millisecond

// A1. EVERY RESUME CARRIES THE AUDIO PES THE RESYNC WAS READING. An unbounded
// audio PES is open until its PID's next start; at the resume it is the
// audio that runs on into the resumed programme, so it goes out, and its
// continuation follows it. Dropping it is a frame-long hole in the audio.
//
// Mutation: take() leaves an unbounded partial out.
func TestEveryResumeCarriesTheUnboundedAudioPESBeingRead(t *testing.T) {
	run := func(t *testing.T, forced, unbounded bool) []int {
		g := delaytest.New(30)
		g.AudioUnbounded = unbounded
		// Audio muxed ahead of its timestamps, as an encoder that sends
		// audio early does: the PES open at the resume is then newer than
		// the cut's audio end, so it belongs to the resumed programme.
		g.AudioLag = func(m int) time.Duration {
			return -min(audioLead, delaytest.Ticks(int64(m*delaytest.AudioTicks)))
		}
		if forced {
			g.NoKeysFrom = 16 * 30
		}
		r := newRig(t, 15*time.Second, memLimits(), g.Mux(90*time.Second))
		r.run(20 * time.Second)
		tk, err := r.b.Dump(r.epoch.Add(20*time.Second), 2*time.Second, DumpMargin)
		if err != nil {
			t.Fatal(err)
		}
		r.run(90 * time.Second)
		if res := tk.Result(); !res.Complete || res.Forced != forced {
			t.Fatalf("result %+v: want complete, forced=%t", res, forced)
		}
		var idx []int
		for _, a := range readOut(t, r.out, r.outAt, false).audio {
			idx = append(idx, a.index)
		}
		return idx
	}
	for _, forced := range []bool{false, true} {
		name := "keyframe"
		if forced {
			name = "forced"
		}
		t.Run(name, func(t *testing.T) {
			// The same stream with every audio PES bounded is the reference:
			// its length field changes nothing about which frames air.
			want, got := run(t, forced, false), run(t, forced, true)
			for i := 0; i < min(len(want), len(got)); i++ {
				if want[i] != got[i] {
					t.Fatalf("released audio frame %d is #%d with unbounded PESs, #%d with bounded ones: a frame went missing at the resume",
						i, got[i], want[i])
				}
			}
			if len(got) != len(want) {
				t.Fatalf("%d audio frames released with unbounded PESs, %d with bounded ones", len(got), len(want))
			}
		})
	}
}

// A3. A RECOVERY CRA WHOSE PES HEADER RUNS INTO A SECOND PACKET is rewritten
// to BLA all the same: the rewrite reads the header across the two packets,
// as the resync does, instead of leaving the CRA as it was -- which a decoder
// starting there cannot decode.
//
// Mutation: give up on a split header.
func TestARecoveryCRAWithASplitHeaderIsStillRewrittenToBLA(t *testing.T) {
	g := delaytest.New(30)
	g.HEVC, g.KeyType, g.SplitVideoHeaders = true, delaytest.HEVCIDRWRADL, true
	g.Override = func(n int) (byte, bool) {
		if n >= 16*30 && n%30 == 0 {
			return delaytest.HEVCCRA, true
		}
		return 0, false
	}
	r := newRig(t, 15*time.Second, memLimits(), g.Mux(60*time.Second))
	r.run(20 * time.Second)
	tk, err := r.b.Dump(r.epoch.Add(20*time.Second), 2*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(60 * time.Second)
	if res := tk.Result(); !res.Complete || !res.Recovery {
		t.Fatalf("result %+v: want recovery", res)
	}
	rep := readOut(t, r.out, r.outAt, true)
	at := spliceAt(t, rep.video)
	if at < 0 {
		t.Fatal("nothing was dumped")
	}
	if k2 := rep.video[at]; k2.nal != 18 {
		t.Fatalf("the recovery picture #%d went out as NAL type %d, want 18 (BLA_N_LP)", k2.index, k2.nal)
	}
}

// A4. THE FORCED RESUME TAKES A PES START WHOSE HEADER RUNS INTO A SECOND
// PACKET. A video-only source with no random-access picture and every PES
// header split (its first packet carrying an adaptation field) still resumes
// past the force deadline, instead of waiting for a header that fits.
//
// Mutation: skip the force branch on a split header.
func TestAForcedResumeTakesASplitHeaderPESStart(t *testing.T) {
	g := delaytest.New(30)
	g.NoKeysFrom, g.SplitVideoHeaders = 16*30, true
	s := g.Mux(90 * time.Second)
	video := &delaytest.Stream{}
	for i, p := range s.Pkts {
		if pktPID(p) != delaytest.AudioPID {
			video.Pkts = append(video.Pkts, p)
			video.Times = append(video.Times, s.Times[i])
		}
	}
	r := newRig(t, 15*time.Second, memLimits(), video)
	r.run(20 * time.Second)
	tk, err := r.b.Dump(r.epoch.Add(20*time.Second), 2*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(90 * time.Second)
	select {
	case <-tk.Done():
	default:
		t.Fatal("a forced resume on split-header PES starts is still waiting 70 s later")
	}
	if res := tk.Result(); !res.Complete || !res.Forced {
		t.Fatalf("result %+v: want a complete, forced resume", res)
	}
	rep := readOut(t, r.out, r.outAt, false)
	if at := spliceAt(t, rep.video); at < 0 {
		t.Fatal("nothing was dumped")
	}
}

// retag copies an audio PES start as a one-packet PES on pid.
func retag(p []byte, pid uint16) []byte {
	q := append([]byte(nil), p...)
	q[1] = q[1]&0xE0 | byte(pid>>8)
	q[2] = byte(pid & 0xFF)
	pl := payloadOf(q)
	n := len(pl) - 6
	pl[4], pl[5] = byte(n>>8), byte(n)
	return q
}

// A5. A PID NEW SINCE THE LAST SPLICE IS CUT AGAINST THAT SPLICE, not the
// first: after a second dump, a PES on a new PID that carries a timestamp
// from the second dump's window does not air, and one from after the resume
// does.
//
// Mutation: keep the first splice's floor.
func TestAPIDNewAfterASecondDumpIsCutAgainstThatDump(t *testing.T) {
	const stale, fresh = 0x150, 0x151
	g := delaytest.New(30)
	s := g.Mux(100 * time.Second)
	var pk [][]byte
	var tm []time.Duration
	var old []byte
	injected := false
	for i, p := range s.Pkts {
		if old == nil && s.Times[i] >= 54500*time.Millisecond && pktPID(p) == delaytest.AudioPID && pktPUSI(p) {
			old = p
		}
		pk, tm = append(pk, p), append(tm, s.Times[i])
		if !injected && s.Times[i] >= 65*time.Second && pktPID(p) == delaytest.AudioPID && pktPUSI(p) {
			pk = append(pk, retag(old, stale), retag(p, fresh))
			tm = append(tm, s.Times[i], s.Times[i])
			injected = true
		}
	}
	s.Pkts, s.Times = pk, tm
	r := newRig(t, 30*time.Second, memLimits(), s)
	r.run(35 * time.Second)
	t1, err := r.b.Dump(r.epoch.Add(35*time.Second), 2*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(45 * time.Second)
	if !t1.Result().Complete {
		t.Fatal("the first dump did not resume")
	}
	r.run(55 * time.Second)
	t2, err := r.b.Dump(r.epoch.Add(55*time.Second), 2*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(63 * time.Second)
	if !t2.Result().Complete {
		t.Fatal("the second dump did not resume before the injection")
	}
	r.run(100 * time.Second)
	saw := map[uint16]bool{}
	for off := 0; off+tsPacketSize <= len(r.out); off += tsPacketSize {
		if p := r.out[off : off+tsPacketSize]; pktPUSI(p) {
			saw[pktPID(p)] = true
		}
	}
	if !saw[fresh] {
		t.Fatal("a new PID's PES from after the resume did not air: the fixture proves nothing")
	}
	if saw[stale] {
		t.Fatal("a new PID's PES from the second dump's window aired")
	}
}

// A5, the floor's age. The cut against the splice is for data from the
// dumped window, which arrives around the resume. A PID first seen long after
// it -- here after the source clock jumps half the 33-bit range on -- is
// not compared to a floor that far behind, where the wrap reads it as before
// the floor and drops it.
//
// Mutation: keep cutting new PIDs against the floor however old it is.
func TestAPIDNewLongAfterASpliceIsNotCutAgainstIt(t *testing.T) {
	const late = 0x152
	g := delaytest.New(30)
	g.StepFrom, g.Step = 40*time.Second, 1<<32+90000
	s := g.Mux(70 * time.Second)
	var pk [][]byte
	var tm []time.Duration
	injected := false
	for i, p := range s.Pkts {
		pk, tm = append(pk, p), append(tm, s.Times[i])
		if !injected && s.Times[i] >= 50*time.Second && pktPID(p) == delaytest.AudioPID && pktPUSI(p) {
			pk, tm = append(pk, retag(p, late)), append(tm, s.Times[i])
			injected = true
		}
	}
	s.Pkts, s.Times = pk, tm
	r := newRig(t, 15*time.Second, memLimits(), s)
	r.run(20 * time.Second)
	tk, err := r.b.Dump(r.epoch.Add(20*time.Second), 2*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(70 * time.Second)
	if !tk.Result().Complete {
		t.Fatal("did not resume")
	}
	for off := 0; off+tsPacketSize <= len(r.out); off += tsPacketSize {
		if p := r.out[off : off+tsPacketSize]; pktPUSI(p) && pktPID(p) == late {
			return
		}
	}
	t.Fatal("a PID first seen 30 s after the splice, past a clock jump, never aired")
}

// truncRig is a buffer of numbered datagrams, one per millisecond, with
// every record's place kept so a test can cut at any of them.
type truncRig struct {
	b     *Buffer
	epoch time.Time
	marks []*mark
}

func newTruncRig(t *testing.T, lim Limits, n int) *truncRig {
	t.Helper()
	epoch := time.Unix(1_700_000_000, 0)
	b, err := NewBuffer(time.Second, lim, epoch)
	if err != nil {
		t.Fatal(err)
	}
	r := &truncRig{b: b, epoch: epoch}
	b.mu.Lock()
	defer b.mu.Unlock()
	prev := int64(0)
	for i := 0; i < n; i++ {
		at := int64(time.Duration(i+1) * time.Millisecond)
		seg, off, pp, pb := b.appendLocked(at, datagram(uint64(i)))
		r.marks = append(r.marks, &mark{seg: seg, off: off, at: at, prevAt: prev, pushedPackets: pp, pushedBytes: pb})
		prev = at
	}
	return r
}

// popThrough releases records 0..i.
func (r *truncRig) popThrough(i int) int {
	return len(r.b.Pop(r.epoch.Add(time.Second+time.Duration(i+1)*time.Millisecond), nil))
}

// consistent checks the counts against what is really held: every segment's
// packets non-negative, their sum the buffer's, a spilled file the size its
// segment says, and draining releases exactly that many records, in order,
// ending before seq end.
func (r *truncRig) consistent(t *testing.T, end int) {
	t.Helper()
	r.b.mu.Lock()
	var sum int64
	for _, s := range r.b.queue {
		if s.packets < 0 {
			r.b.mu.Unlock()
			t.Fatalf("a segment counts %d packets", s.packets)
		}
		sum += s.packets
		if s.path != "" {
			if fi, err := os.Stat(s.path); err != nil || fi.Size() != s.size {
				r.b.mu.Unlock()
				t.Fatalf("spill file %v: size %v, the segment says %d", err, fi, s.size)
			}
		}
	}
	held := r.b.stats.Packets
	r.b.mu.Unlock()
	if sum != held {
		t.Fatalf("segments hold %d packets, the buffer counts %d", sum, held)
	}
	out := r.b.Pop(r.epoch.Add(time.Hour), nil)
	if int64(len(out)) != held {
		t.Fatalf("drained %d records, the buffer counted %d", len(out), held)
	}
	for _, p := range out {
		if seqOf(p) >= uint64(end) {
			t.Fatalf("record %d is past the cut at %d", seqOf(p), end)
		}
	}
	if st := r.b.Stats(); st.Packets != 0 || st.Bytes != 0 {
		t.Fatalf("drained, the buffer still counts %d packets, %d bytes", st.Packets, st.Bytes)
	}
}

// A2. A CUT IN THE SEGMENT BEING READ, AND IN A SPILLED ONE, counts exactly
// what it removes: at the read position, past it, inside a spilled segment.
// A cut behind the read position -- programme already released -- is refused
// and changes nothing.
//
// Mutation: drop the guard on a cut behind the read position.
func TestATruncateCountsExactlyWhereverTheCutFalls(t *testing.T) {
	seg := recordHeader + maxDatagram
	disk := func(t *testing.T) Limits {
		return Limits{MemBudget: int64(2 * seg), DiskBudget: 64 << 20, SegmentBytes: seg, Dir: t.TempDir()}
	}
	t.Run("at the read position", func(t *testing.T) {
		r := newTruncRig(t, Limits{MemBudget: 64 << 20, SegmentBytes: 16 * seg}, 10)
		if n := r.popThrough(4); n != 5 {
			t.Fatalf("released %d", n)
		}
		r.b.mu.Lock()
		err := r.b.truncateLocked(r.marks[5])
		r.b.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		r.consistent(t, 5)
	})
	t.Run("past the read position", func(t *testing.T) {
		r := newTruncRig(t, Limits{MemBudget: 64 << 20, SegmentBytes: 16 * seg}, 10)
		r.popThrough(2)
		r.b.mu.Lock()
		err := r.b.truncateLocked(r.marks[7])
		r.b.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		r.consistent(t, 7)
	})
	t.Run("behind the read position", func(t *testing.T) {
		r := newTruncRig(t, Limits{MemBudget: 64 << 20, SegmentBytes: 16 * seg}, 10)
		r.popThrough(5)
		before := r.b.Stats()
		r.b.mu.Lock()
		err := r.b.truncateLocked(r.marks[3])
		r.b.mu.Unlock()
		if err == nil {
			t.Fatal("a cut behind the read position, in programme already released, was taken")
		}
		if after := r.b.Stats(); after.Packets != before.Packets || after.Bytes != before.Bytes {
			t.Fatalf("a refused cut changed the counts: %d/%d packets", before.Packets, after.Packets)
		}
		r.consistent(t, 10)
	})
	t.Run("inside a spilled segment", func(t *testing.T) {
		r := newTruncRig(t, disk(t), 400)
		cut := -1
		for i, m := range r.marks {
			if m.seg.data == nil && m.seg.path != "" && m.seg != r.b.queue[0] && m.off > 0 {
				cut = i + 5 // well inside it
				break
			}
		}
		if cut < 0 || r.marks[cut].seg.data != nil || r.marks[cut].off == 0 {
			t.Fatal("the fixture has no cut inside a spilled segment")
		}
		r.popThrough(6)
		r.b.mu.Lock()
		err := r.b.truncateLocked(r.marks[cut])
		r.b.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		r.consistent(t, cut)
	})
	t.Run("a spilled segment is never part read", func(t *testing.T) {
		// The case the cut cannot handle -- a file written from a read
		// offset, so its offsets are not the records' -- does not arise:
		// only a sealed tail that is not the head spills, and Pop reads a
		// spilled head back into RAM before reading any of it.
		r := newTruncRig(t, disk(t), 400)
		for i := 0; i < 400; i += 7 {
			r.popThrough(i)
			r.b.mu.Lock()
			for _, s := range r.b.queue {
				if s.data == nil && s.off != 0 {
					r.b.mu.Unlock()
					t.Fatalf("a spilled segment has read offset %d", s.off)
				}
			}
			r.b.mu.Unlock()
		}
	})
}
