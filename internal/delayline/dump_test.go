package delayline

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/delayline/delaytest"
)

// The dump's contract, against a synthetic MPEG-TS whose every picture says
// which picture it is. A splice that cuts mid-GOP, resumes on a non-IDR, steps
// a timestamp, breaks a continuity counter or leaves a release-clock gap fails
// a named assertion here. dump_ffmpeg_test.go repeats the important ones
// against libx264 output decoded by FFmpeg.

// rig drives a Buffer on a simulated clock: each datagram is pushed at its
// arrival time, and everything due is popped first.
type rig struct {
	t      *testing.T
	b      *Buffer
	epoch  time.Time
	s      *delaytest.Stream
	next   int
	out    []byte
	outAt  []time.Duration // release time of each output packet
	inTime []time.Duration
}

func newRig(t *testing.T, delay time.Duration, lim Limits, s *delaytest.Stream) *rig {
	t.Helper()
	epoch := time.Unix(1_700_000_000, 0)
	b, err := NewBuffer(delay, lim, epoch)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, b: b, epoch: epoch, s: s}
}

func (r *rig) pop(at time.Duration) {
	for _, p := range r.b.Pop(r.epoch.Add(at), nil) {
		if len(p)%tsPacketSize != 0 {
			r.t.Fatalf("released a %d-byte record, not whole TS packets", len(p))
		}
		r.out = append(r.out, p...)
		for i := 0; i < len(p); i += tsPacketSize {
			r.outAt = append(r.outAt, at)
		}
	}
}

// run pushes every datagram that arrives before until, then pops to until.
func (r *rig) run(until time.Duration) {
	for r.next < len(r.s.Pkts) && r.s.Times[r.next] < until {
		at := r.s.Times[r.next]
		var dgram []byte
		for n := 0; n < chunkPackets && r.next < len(r.s.Pkts) && r.s.Times[r.next] == at; n++ {
			dgram = append(dgram, r.s.Pkts[r.next]...)
			r.next++
		}
		r.pop(at)
		if err := r.b.Push(r.epoch.Add(at), dgram); err != nil {
			r.t.Fatal(err)
		}
	}
	r.pop(until)
}

// esUnit is one PES read back out of the released stream.
type esUnit struct {
	pid       uint16
	pts, dts  int64
	index     int
	nal       byte
	firstNAL  bool
	release   time.Duration
	discarded bool
	// esLen is the elementary-stream bytes the PES carried.
	esLen int
}

type outReport struct {
	video, audio []esUnit
	ccBreaks     int
	pcrs         []int64
}

func readOut(t *testing.T, out []byte, outAt []time.Duration, hevc bool) outReport {
	t.Helper()
	var rep outReport
	type open struct {
		u  esUnit
		es []byte
	}
	cur := map[uint16]*open{}
	lastCC := map[uint16]int{}
	finish := func(o *open) {
		if o == nil {
			return
		}
		u := o.u
		// The picture's number follows its slice NAL header.
		es := o.es
		u.esLen = len(es)
		for i := 0; i+3 < len(es); i++ {
			if es[i] != 0 || es[i+1] != 0 || es[i+2] != 1 {
				continue
			}
			h := es[i+3]
			var typ byte
			hl := 1
			if hevc {
				typ, hl = (h>>1)&0x3F, 2
				if typ > 31 {
					continue
				}
			} else {
				typ = h & 0x1F
				if typ < 1 || typ > 5 {
					continue
				}
			}
			at := i + 3 + hl
			if at+4 <= len(es) {
				u.nal = typ
				u.index = int(binary.BigEndian.Uint32(es[at : at+4]))
			}
			break
		}
		if u.pid == delaytest.AudioPID && len(es) >= 4 {
			u.index = int(binary.BigEndian.Uint32(es[:4]))
		}
		if u.pid == delaytest.VideoPID {
			rep.video = append(rep.video, u)
		} else {
			rep.audio = append(rep.audio, u)
		}
	}
	for i := 0; i+tsPacketSize <= len(out); i += tsPacketSize {
		p := out[i : i+tsPacketSize]
		if p[0] != tsSyncByte {
			t.Fatalf("lost sync at byte %d", i)
		}
		pid := pktPID(p)
		if pktHasPayload(p) && pid != tsNullPID {
			cc := int(p[3] & 0x0F)
			if prev, ok := lastCC[pid]; ok && cc != (prev+1)&0x0F {
				rep.ccBreaks++
			}
			lastCC[pid] = cc
		}
		if at := pcrAt(p); at >= 0 {
			rep.pcrs = append(rep.pcrs, readPCRBase(p[at:]))
		}
		if pid != delaytest.VideoPID && pid != delaytest.AudioPID {
			continue
		}
		if pktPUSI(p) {
			finish(cur[pid])
			ptsAt, dtsAt, esAt, ok := pesFields(p)
			if !ok {
				t.Fatalf("a PES start without a PES header on PID %#x", pid)
			}
			o := &open{u: esUnit{pid: pid, pts: -1, dts: -1, release: outAt[i/tsPacketSize]}}
			if ptsAt >= 0 {
				o.u.pts = readTS(p[ptsAt:])
			}
			if dtsAt >= 0 {
				o.u.dts = readTS(p[dtsAt:])
			}
			o.es = append(o.es, p[esAt:]...)
			cur[pid] = o
			continue
		}
		if o := cur[pid]; o != nil {
			o.es = append(o.es, p[payloadStart(p):]...)
		}
	}
	for _, o := range cur {
		finish(o)
	}
	return rep
}

func memLimits() Limits {
	return Limits{MemBudget: 64 << 20, SegmentBytes: DefaultSegmentBytes}
}

// spliceAt finds where the released picture numbers stop being consecutive.
func spliceAt(t *testing.T, v []esUnit) int {
	t.Helper()
	at := -1
	for i := 1; i < len(v); i++ {
		if v[i].index != v[i-1].index+1 {
			if at >= 0 {
				t.Fatalf("two jumps in the released pictures: at %d (%d -> %d) and %d (%d -> %d)",
					at, v[at-1].index, v[at].index, i, v[i-1].index, v[i].index)
			}
			at = i
		}
	}
	return at
}

func TestADumpCutsAtOneIDRResumesAtTheNextAndKeepsEveryClockContinuous(t *testing.T) {
	const gop = 60 // 2 s
	g := delaytest.New(gop)
	delay := 10 * time.Second
	r := newRig(t, delay, memLimits(), g.Mux(40*time.Second))
	r.run(15 * time.Second)

	want := 3 * time.Second
	ticket, err := r.b.Dump(r.epoch.Add(15*time.Second), want, DumpMargin)
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}
	select {
	case <-ticket.Done():
		t.Fatal("the dump completed before its resume keyframe arrived")
	default:
	}
	r.run(40 * time.Second)
	select {
	case <-ticket.Done():
	default:
		t.Fatal("the dump never resumed")
	}
	res := ticket.Result()

	rep := readOut(t, r.out, r.outAt, false)
	at := spliceAt(t, rep.video)
	if at < 0 {
		t.Fatal("no picture was dumped")
	}
	before, after := rep.video[at-1], rep.video[at]

	// BOUNDARIES. What stays ends a GOP; what resumes starts one, on an IDR.
	if (before.index+1)%gop != 0 {
		t.Errorf("the kept stream ends at picture %d, mid-GOP: the cut was not at a keyframe", before.index)
	}
	if after.index%gop != 0 || after.nal != delaytest.H264IDR {
		t.Errorf("the stream resumes at picture %d (NAL type %d), not at an IDR", after.index, after.nal)
	}
	// The dump covers at least the newest want: everything that arrived in
	// [15s-want, 15s] is gone.
	firstDropped, lastDropped := before.index+1, after.index-1
	newest := int((15*time.Second)*30/time.Second) - 1
	if delaytest.Ticks(int64(firstDropped*delaytest.FrameTicks)) > 15*time.Second-want || lastDropped < newest {
		t.Errorf("dropped pictures %d..%d; the newest %s before the dump (to picture %d) must all be in that range",
			firstDropped, lastDropped, want, newest)
	}

	// TIMESTAMPS. Picture i of the output has PTS base+i frames, straight
	// through the splice, and DTS likewise.
	for i, u := range rep.video {
		wantDTS := int64(delaytest.PTSBase + (rep.video[0].index+i)*delaytest.FrameTicks)
		if u.dts != wantDTS || u.pts != wantDTS+delaytest.FrameTicks {
			t.Fatalf("released picture %d (#%d): PTS %d DTS %d, want PTS %d DTS %d -- the timeline steps at the splice",
				i, u.index, u.pts, u.dts, wantDTS+delaytest.FrameTicks, wantDTS)
		}
	}
	for i := 1; i < len(rep.audio); i++ {
		step := rep.audio[i].pts - rep.audio[i-1].pts
		if step <= 0 || step > 2*delaytest.AudioTicks {
			t.Fatalf("audio PTS steps by %d ticks between frames #%d and #%d: overlap or a gap at the splice",
				step, rep.audio[i-1].index, rep.audio[i].index)
		}
	}
	for i := 1; i < len(rep.pcrs); i++ {
		if !ptsAfter(rep.pcrs[i], rep.pcrs[i-1]) && rep.pcrs[i] != rep.pcrs[i-1] {
			t.Fatalf("PCR went backwards: %d then %d", rep.pcrs[i-1], rep.pcrs[i])
		}
	}
	if rep.ccBreaks != 0 {
		t.Errorf("%d continuity-counter breaks in the released stream", rep.ccBreaks)
	}

	// THE RELEASE CLOCK. No gap on the wire: the resume picture goes out
	// when the cut picture would have.
	for i := 1; i < len(rep.video); i++ {
		if gap := rep.video[i].release - rep.video[i-1].release; gap > 100*time.Millisecond {
			t.Errorf("a %s gap on the wire between pictures #%d and #%d", gap, rep.video[i-1].index, rep.video[i].index)
		}
	}

	// THE DELAY. Shorter by exactly the dumped programme time, and the
	// pictures after the splice really are released that much sooner.
	dumped := delaytest.Ticks(int64((after.index - before.index - 1) * delaytest.FrameTicks))
	if d := res.Dumped - dumped; d < -40*time.Millisecond || d > 40*time.Millisecond {
		t.Errorf("Dumped = %s, but %s of pictures were dropped", res.Dumped, dumped)
	}
	if res.Before != delay || res.After != delay-res.Dumped || !res.Complete || res.Clamped {
		t.Errorf("result %+v: want Before %s, After %s, complete, not clamped", res, delay, delay-res.Dumped)
	}
	last := rep.video[len(rep.video)-1]
	if got := last.release - delaytest.Ticks(int64(last.index*delaytest.FrameTicks)); got-res.After > 40*time.Millisecond || res.After-got > 40*time.Millisecond {
		t.Errorf("picture #%d went out %s after it arrived, want the new delay %s", last.index, got, res.After)
	}
	if st := r.b.Stats(); st.Dumped != res.Dumped || st.Dumping {
		t.Errorf("Stats: Dumped %s Dumping %t, want %s and false", st.Dumped, st.Dumping, res.Dumped)
	}
}

func TestADumpNeverTakesTheDelayUnderTheMarginPlusAGOP(t *testing.T) {
	const gop = 60
	g := delaytest.New(gop)
	delay := 12 * time.Second
	r := newRig(t, delay, memLimits(), g.Mux(120*time.Second))
	clock := 15 * time.Second
	r.run(clock)
	dumps := 0
	for ; dumps < 20; dumps++ {
		// Far more than there is: clamped to what the margin allows.
		ticket, err := r.b.Dump(r.epoch.Add(clock), 60*time.Second, DumpMargin)
		if errors.Is(err, ErrNoCutPoint) {
			break
		}
		if err != nil {
			t.Fatalf("dump %d: %v", dumps, err)
		}
		clock += 3 * time.Second
		r.run(clock)
		res := ticket.Result()
		if !res.Complete {
			t.Fatalf("dump %d did not resume within 3 s of 2 s GOPs", dumps)
		}
		if !res.Clamped {
			t.Errorf("dump %d asked for 60 s of a %s buffer and was not reported clamped", dumps, res.Before)
		}
		// The delay left is at least the margin; the resume wait (under one
		// GOP) is what the extra GOP of allowance absorbed.
		if res.After < DumpMargin {
			t.Fatalf("dump %d left %s of delay, under the %s margin", dumps, res.After, DumpMargin)
		}
		if res.After < DumpMargin+2*time.Second-time.Second/30 {
			t.Errorf("dump %d left %s: the GOP allowance was not kept (margin %s + one 2 s GOP)", dumps, res.After, DumpMargin)
		}
	}
	if dumps == 0 {
		t.Fatal("not one dump was possible on a 12 s buffer")
	}
	if dumps > 1 {
		t.Errorf("%d clamped dumps in a row; the first must take everything above the margin", dumps)
	}
	// Everything still released is continuous, and the buffer never ran dry.
	r.run(60 * time.Second)
	rep := readOut(t, r.out, r.outAt, false)
	for i := 1; i < len(rep.video); i++ {
		if gap := rep.video[i].release - rep.video[i-1].release; gap > 100*time.Millisecond {
			t.Fatalf("the output stalled for %s at picture #%d: the dump drained the buffer", gap, rep.video[i].index)
		}
	}
}

func TestADefaultDumpTakesEverythingSinceTheLastDumpPoint(t *testing.T) {
	g := delaytest.New(30) // 1 s GOPs
	delay := 20 * time.Second
	r := newRig(t, delay, memLimits(), g.Mux(60*time.Second))
	r.run(25 * time.Second)
	t1, err := r.b.Dump(r.epoch.Add(25*time.Second), 2*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(31 * time.Second)
	first := t1.Result()
	// Five seconds later: 0 asks for everything since the first splice --
	// about the five seconds that arrived since, and nothing older.
	t2, err := r.b.Dump(r.epoch.Add(31*time.Second), 0, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(33 * time.Second)
	second := t2.Result()
	if !second.Complete {
		t.Fatal("the second dump did not resume")
	}
	sinceSplice := 31*time.Second - 25*time.Second
	if second.Dumped < sinceSplice-time.Second || second.Dumped > sinceSplice+2*time.Second {
		t.Errorf("a default dump took %s; %s had arrived since the last dump point", second.Dumped, sinceSplice)
	}
	if second.Before != delay-first.Dumped {
		t.Errorf("the second dump started from %s of delay, want %s", second.Before, delay-first.Dumped)
	}
	// Right after: a default dump reaches back only to the last splice, never
	// past it into programme an earlier dump chose to keep.
	t3, err := r.b.Dump(r.epoch.Add(33*time.Second), 0, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(35 * time.Second)
	if third := t3.Result(); third.Dumped > 33*time.Second-31*time.Second+time.Second {
		t.Errorf("a default dump 2 s after the last one took %s: it reached past the last dump point", third.Dumped)
	}
}

func TestADumpRefusesAStreamWithNoIDRPictures(t *testing.T) {
	// Open GOP: every key picture after the first is an H.264 I picture
	// (type 1, random-access flagged), which may reference what a dump
	// discards.
	g := delaytest.New(60)
	g.Override = func(n int) (byte, bool) {
		if n > 0 && n%60 == 0 {
			return delaytest.H264NonIDR, true
		}
		return 0, false
	}
	r := newRig(t, 10*time.Second, memLimits(), g.Mux(20*time.Second))
	r.run(15 * time.Second)
	if _, err := r.b.Dump(r.epoch.Add(15*time.Second), 2*time.Second, DumpMargin); err == nil {
		t.Fatal("dumped an open-GOP stream: the only cut points were I pictures that are not IDR")
	}
	if r.b.Stats().Spliceable {
		t.Error("an open-GOP stream whose only IDR has aired is reported spliceable")
	}
}

func TestAnHEVCDumpCutsOnlyAtIDRAndRefusesCRA(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   byte
		dumps bool
	}{
		{"IDR_W_RADL", delaytest.HEVCIDRWRADL, true},
		{"CRA", delaytest.HEVCCRA, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := delaytest.New(60)
			g.HEVC, g.KeyType = true, tc.key
			r := newRig(t, 10*time.Second, memLimits(), g.Mux(30*time.Second))
			r.run(15 * time.Second)
			ticket, err := r.b.Dump(r.epoch.Add(15*time.Second), 2*time.Second, DumpMargin)
			if !tc.dumps {
				if err == nil {
					t.Fatal("dumped at a CRA picture: its RASL pictures reference what was discarded")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			r.run(30 * time.Second)
			if !ticket.Result().Complete {
				t.Fatal("did not resume")
			}
			rep := readOut(t, r.out, r.outAt, true)
			at := spliceAt(t, rep.video)
			if at < 0 || rep.video[at].nal != delaytest.HEVCIDRWRADL || rep.video[at].index%60 != 0 {
				t.Fatalf("resumed at %+v", rep.video[at])
			}
		})
	}
}

func TestADumpResumesPastAnOpenGOPPictureToTheNextIDR(t *testing.T) {
	// The picture right after the cut is an I picture that is not IDR: the
	// resume must wait for the real IDR one GOP later.
	g := delaytest.New(30)
	cutGOPs := map[int]bool{}
	g.Override = func(n int) (byte, bool) {
		if cutGOPs[n] {
			return delaytest.H264NonIDR, true
		}
		return 0, false
	}
	// The first key picture after 15 s (picture 450) is open-GOP.
	cutGOPs[450] = true
	r := newRig(t, 10*time.Second, memLimits(), g.Mux(30*time.Second))
	r.run(15 * time.Second)
	ticket, err := r.b.Dump(r.epoch.Add(15*time.Second), 2*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(30 * time.Second)
	if !ticket.Result().Complete {
		t.Fatal("did not resume")
	}
	rep := readOut(t, r.out, r.outAt, false)
	at := spliceAt(t, rep.video)
	if got := rep.video[at]; got.index != 480 || got.nal != delaytest.H264IDR {
		t.Fatalf("resumed at picture #%d (NAL %d); #450 is open-GOP, so the resume is #480", got.index, got.nal)
	}
}

func TestADumpTruncatesASpilledSegmentAndKeepsTheBudgetsExact(t *testing.T) {
	dir := t.TempDir()
	lim := Limits{
		MemBudget:    2 << 20, // forces most of a 10 s buffer to disk
		DiskBudget:   64 << 20,
		SegmentBytes: DefaultSegmentBytes,
		Dir:          dir,
	}
	g := delaytest.New(60)
	// About 10 Mbit/s, so a dump point falls in a spilled segment.
	g.PictureBytes = 40000
	r := newRig(t, 10*time.Second, lim, g.Mux(30*time.Second))
	r.run(15 * time.Second)
	if r.b.Stats().DiskBytes == 0 {
		t.Fatal("nothing spilled: the fixture does not reach the disk path")
	}
	ticket, err := r.b.Dump(r.epoch.Add(15*time.Second), 6*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	st := r.b.Stats()
	var onDisk int64
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			onDisk += info.Size()
		}
		return nil
	})
	if onDisk != st.DiskBytes {
		t.Errorf("after the cut %d bytes are on disk and DiskBytes says %d", onDisk, st.DiskBytes)
	}
	r.run(30 * time.Second)
	if !ticket.Result().Complete {
		t.Fatal("did not resume")
	}
	r.run(60 * time.Second)
	if st := r.b.Stats(); st.Packets != 0 || st.Bytes != 0 || st.DiskBytes != 0 {
		t.Errorf("after everything released: %d packets, %d bytes, %d on disk held", st.Packets, st.Bytes, st.DiskBytes)
	}
	rep := readOut(t, r.out, r.outAt, false)
	if at := spliceAt(t, rep.video); at < 0 || rep.video[at].nal != delaytest.H264IDR {
		t.Fatal("the spilled-segment dump did not resume on an IDR")
	}
	if rep.ccBreaks != 0 {
		t.Errorf("%d continuity breaks", rep.ccBreaks)
	}
}

func TestADumpIsRefusedWhileOneIsWaitingForItsKeyframe(t *testing.T) {
	g := delaytest.New(60)
	r := newRig(t, 10*time.Second, memLimits(), g.Mux(30*time.Second))
	r.run(15 * time.Second)
	if _, err := r.b.Dump(r.epoch.Add(15*time.Second), time.Second, DumpMargin); err != nil {
		t.Fatal(err)
	}
	if _, err := r.b.Dump(r.epoch.Add(15*time.Second), time.Second, DumpMargin); !errors.Is(err, ErrDumpPending) {
		t.Fatalf("a second dump before the first resumed: %v, want ErrDumpPending", err)
	}
}

func TestADumpIsRefusedForDataThatIsNotTS(t *testing.T) {
	b, err := NewBuffer(10*time.Second, memLimits(), time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		_ = b.Push(time.Unix(0, int64(i)*int64(10*time.Millisecond)), []byte("not a transport stream"))
	}
	if _, err := b.Dump(time.Unix(1, 0), 0, DumpMargin); !errors.Is(err, ErrNotSpliceable) {
		t.Fatalf("Dump on non-TS data: %v, want ErrNotSpliceable", err)
	}
}

func TestADumpDropsAudioThatWouldOverlapTheSplice(t *testing.T) {
	// Audio from 15.5 s on is muxed half a second late, so the audio right
	// after the resume keyframe is older programme than the audio right before
	// the cut. Kept, its timestamps would step backwards.
	g := delaytest.New(60)
	g.AudioLag = func(m int) time.Duration {
		if delaytest.Ticks(int64(m*delaytest.AudioTicks)) >= 15500*time.Millisecond {
			return 500 * time.Millisecond
		}
		return 0
	}
	r := newRig(t, 10*time.Second, memLimits(), g.Mux(40*time.Second))
	r.run(15 * time.Second)
	ticket, err := r.b.Dump(r.epoch.Add(15*time.Second), 3*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(40 * time.Second)
	if !ticket.Result().Complete {
		t.Fatal("did not resume")
	}
	rep := readOut(t, r.out, r.outAt, false)
	for i := 1; i < len(rep.audio); i++ {
		if !ptsAfter(rep.audio[i].pts, rep.audio[i-1].pts) {
			t.Fatalf("audio frame #%d (PTS %d) after #%d (PTS %d): the splice overlapped the audio",
				rep.audio[i].index, rep.audio[i].pts, rep.audio[i-1].index, rep.audio[i-1].pts)
		}
	}
	if rep.ccBreaks != 0 {
		t.Errorf("%d continuity breaks: dropping overlapped audio must not break the counters", rep.ccBreaks)
	}
}

// A cut that is still waiting for its keyframe when the line is cut (Stop now,
// a restart, shutdown) leaves nothing behind: the buffer is empty and usable,
// and the dump reports itself incomplete rather than claiming a resume.
func TestADiscardWhileADumpWaitsForItsKeyframeLeavesAUsableBuffer(t *testing.T) {
	g := delaytest.New(60)
	r := newRig(t, 10*time.Second, memLimits(), g.Mux(30*time.Second))
	r.run(15 * time.Second)
	ticket, err := r.b.Dump(r.epoch.Add(15*time.Second), 2*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.b.Discard()
	if st := r.b.Stats(); st.Packets != 0 || st.Dumping {
		t.Fatalf("after Discard: %d packets held, dumping %t", st.Packets, st.Dumping)
	}
	select {
	case <-ticket.Done():
		t.Fatal("a discarded dump reports that it resumed")
	default:
	}
	if ticket.Result().Complete {
		t.Fatal("a discarded dump reports itself complete")
	}
	// Data pushed afterwards is buffered, not swallowed by a stale resync.
	r.run(20 * time.Second)
	if st := r.b.Stats(); st.Packets == 0 {
		t.Fatal("the buffer took nothing after a Discard during a pending dump")
	}
}

// THE MARGIN HOLDS HOWEVER LATE THE RESUME IS. The input stalls for 20 s right
// after a dump -- an encoder reconnecting -- so the resume keyframe arrives
// far later than the one GOP Dump allowed for. Without the cap the whole wait
// came off the delay and the line released data the moment it arrived.
//
// Mutation: drop the `b.delay - ts.margin` cap in decideResumeLocked.
func TestADumpWhoseResumeIsLateStillLeavesTheMargin(t *testing.T) {
	g := delaytest.New(60)
	s := g.Mux(60 * time.Second)
	// Everything after 15.2 s arrives 20 s late.
	for i, at := range s.Times {
		if at >= 15200*time.Millisecond {
			s.Times[i] = at + 20*time.Second
		}
	}
	delay := 10 * time.Second
	r := newRig(t, delay, memLimits(), s)
	r.run(15 * time.Second)
	ticket, err := r.b.Dump(r.epoch.Add(15*time.Second), 2*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(80 * time.Second)
	res := ticket.Result()
	if !res.Complete {
		t.Fatal("did not resume once the input came back")
	}
	if res.After < DumpMargin {
		t.Fatalf("a resume 20 s late left %s of delay, under the %s margin", res.After, DumpMargin)
	}
	rep := readOut(t, r.out, r.outAt, false)
	last := rep.video[len(rep.video)-1]
	// The newest picture: arrived at its own time plus the stall.
	arrived := delaytest.Ticks(int64(last.index*delaytest.FrameTicks)) + 20*time.Second
	if got := last.release - arrived; got < DumpMargin-50*time.Millisecond {
		t.Fatalf("picture #%d went out %s after it arrived: under the %s margin", last.index, got, DumpMargin)
	}
	if rep.ccBreaks != 0 {
		t.Errorf("%d continuity breaks", rep.ccBreaks)
	}
}

// A datagram that is not whole TS packets, after a dump, is dropped -- the
// splice offset and continuity cannot be applied to it -- and everything after
// it is still rewritten. Before the fix one such datagram switched the
// rewrite off for good: every later timestamp jumped forward by the dumped
// time and the counters broke.
func TestANonTSDatagramAfterADumpDoesNotStopTheRewrite(t *testing.T) {
	g := delaytest.New(60)
	r := newRig(t, 10*time.Second, memLimits(), g.Mux(40*time.Second))
	r.run(15 * time.Second)
	ticket, err := r.b.Dump(r.epoch.Add(15*time.Second), 2*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(20 * time.Second)
	if !ticket.Result().Complete {
		t.Fatal("did not resume")
	}
	if err := r.b.Push(r.epoch.Add(20*time.Second), make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	r.run(40 * time.Second)
	rep := readOut(t, r.out, r.outAt, false)
	for i := 1; i < len(rep.video); i++ {
		if step := wrap(rep.video[i].dts - rep.video[i-1].dts); step != delaytest.FrameTicks {
			t.Fatalf("video DTS steps %d at picture #%d after a stray datagram: the rewrite stopped",
				step, rep.video[i].index)
		}
	}
	if rep.ccBreaks != 0 {
		t.Errorf("%d continuity breaks after a stray datagram", rep.ccBreaks)
	}
}

// The same datagram while a dump waits for its keyframe must not wedge it.
// Before the fix the buffer then dropped every later datagram and refused
// every later dump as pending.
func TestANonTSDatagramDuringADumpDoesNotWedgeIt(t *testing.T) {
	g := delaytest.New(60)
	r := newRig(t, 10*time.Second, memLimits(), g.Mux(40*time.Second))
	r.run(15 * time.Second)
	ticket, err := r.b.Dump(r.epoch.Add(15*time.Second), 2*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.b.Push(r.epoch.Add(15*time.Second), make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	r.run(25 * time.Second)
	if !ticket.Result().Complete {
		t.Fatal("a stray datagram during the resync wedged the dump")
	}
	if st := r.b.Stats(); st.Packets == 0 || st.Dumping {
		t.Fatalf("after the resume: %d packets held, dumping %t", st.Packets, st.Dumping)
	}
}

// A PES that began before the resume keyframe and finishes after it is not
// half-kept. Its start was discarded with the resync; its continuation, kept,
// would be read as the end of the PES the cut left open, with a continuity
// counter rewritten to hide the join. FFmpeg's muxer never interleaves PESs
// this way; hardware muxers do.
//
// Mutation: drop `r.awaitStart = true` in decideResumeLocked.
func TestAResumeDropsTheRestOfAPESThatBeganBeforeIt(t *testing.T) {
	g := delaytest.New(60)
	s := g.Mux(40 * time.Second)
	// Move the last packet of the audio frame just before picture 480 (the
	// resume keyframe at 16 s) to just after that picture's first packet.
	k2 := -1
	for i, p := range s.Pkts {
		if pktPID(p) == delaytest.VideoPID && pktPUSI(p) && s.Times[i] == 16*time.Second {
			k2 = i
			break
		}
	}
	if k2 < 0 {
		t.Fatal("no picture at 16 s in the fixture")
	}
	tail := -1
	for i := k2 - 1; i >= 0; i-- {
		if pktPID(s.Pkts[i]) == delaytest.AudioPID {
			tail = i
			break
		}
	}
	if tail < 0 || pktPUSI(s.Pkts[tail]) {
		t.Fatal("the audio frame before the keyframe is not a multi-packet PES")
	}
	moved := s.Pkts[tail]
	pkts := append([][]byte{}, s.Pkts[:tail]...)
	pkts = append(pkts, s.Pkts[tail+1:k2+1]...)
	pkts = append(pkts, moved)
	pkts = append(pkts, s.Pkts[k2+1:]...)
	times := append([]time.Duration{}, s.Times[:tail]...)
	times = append(times, s.Times[tail+1:k2+1]...)
	times = append(times, s.Times[k2])
	times = append(times, s.Times[k2+1:]...)
	s.Pkts, s.Times = pkts, times

	r := newRig(t, 10*time.Second, memLimits(), s)
	r.run(15 * time.Second)
	ticket, err := r.b.Dump(r.epoch.Add(15*time.Second), 2*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(40 * time.Second)
	if !ticket.Result().Complete {
		t.Fatal("did not resume")
	}
	rep := readOut(t, r.out, r.outAt, false)
	for _, a := range rep.audio {
		if a.esLen != 400 {
			t.Fatalf("audio frame #%d left with %d bytes, want 400: a fragment from before the resume "+
				"was joined to it", a.index, a.esLen)
		}
	}
	if rep.ccBreaks != 0 {
		t.Errorf("%d continuity breaks", rep.ccBreaks)
	}
}

// A leading picture after the resume keyframe (HEVC RADL: decoded after it,
// shown before it) would be shown before the picture the splice put in the
// cut picture's place -- a presentation timestamp from the dumped side.
//
// Mutation: drop `r.sticky = true` for video in decideResumeLocked.
func TestAResumeDropsALeadingPictureShownBeforeTheResumeKeyframe(t *testing.T) {
	g := delaytest.New(60)
	g.HEVC, g.KeyType, g.Leading = true, delaytest.HEVCIDRWRADL, true
	r := newRig(t, 10*time.Second, memLimits(), g.Mux(40*time.Second))
	r.run(15 * time.Second)
	ticket, err := r.b.Dump(r.epoch.Add(15*time.Second), 2*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(40 * time.Second)
	if !ticket.Result().Complete {
		t.Fatal("did not resume")
	}
	rep := readOut(t, r.out, r.outAt, true)
	var regular []esUnit
	leadingKept := 0
	for i, v := range rep.video {
		if v.index < delaytest.LeadingIndex {
			regular = append(regular, v)
			continue
		}
		leadingKept++
		if i > 0 && v.index != delaytest.LeadingIndex+rep.video[i-1].index {
			t.Fatalf("leading picture of key #%d follows picture #%d: it belongs to the dumped side",
				v.index-delaytest.LeadingIndex, rep.video[i-1].index)
		}
	}
	at := spliceAt(t, regular)
	if at < 0 {
		t.Fatal("nothing was dumped")
	}
	k2 := regular[at].index
	for _, v := range rep.video {
		if v.index == delaytest.LeadingIndex+k2 {
			t.Fatalf("the resume keyframe #%d's leading picture was kept: it is shown before the splice", k2)
		}
	}
	if leadingKept == 0 {
		t.Fatal("no leading picture survived anywhere: the fixture or the gate is wrong")
	}
}

// The RAM bound the package promises -- MemBudget plus two segments -- holds
// through a dump. A part-filled tail kept in RAM by the cut is still the tail.
//
// Mutation: in truncateLocked, always set b.tail = nil.
func TestADumpKeepsTheRAMBound(t *testing.T) {
	// Two segments of sealed RAM, 10 Mbit/s, and a dump just after a keyframe
	// so the cut lands in the tail being filled.
	seg := 256 << 10
	lim := Limits{MemBudget: int64(2 * seg), DiskBudget: 256 << 20, SegmentBytes: seg, Dir: t.TempDir()}
	g := delaytest.New(60)
	g.PictureBytes = 40000
	r := newRig(t, 10*time.Second, lim, g.Mux(30*time.Second))
	r.run(14050 * time.Millisecond)
	ticket, err := r.b.Dump(r.epoch.Add(14050*time.Millisecond), 20*time.Millisecond, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	for c := 14100 * time.Millisecond; c < 30*time.Second; c += 100 * time.Millisecond {
		r.run(c)
	}
	if !ticket.Result().Complete {
		t.Fatal("did not resume")
	}
	if bound := lim.MemBudget + 2*int64(lim.SegmentBytes); r.b.Stats().PeakMemBytes > bound {
		t.Fatalf("PeakMemBytes %d over the %d bound after a dump", r.b.Stats().PeakMemBytes, bound)
	}
}

// Through the 33-bit wrap: the splice is placed where PTS, DTS and PCR roll
// over, and every clock still runs on by exactly one frame.
func TestADumpAcrossThePTSWrapKeepsTheClocksContinuous(t *testing.T) {
	g := delaytest.New(60)
	g.Origin = ptsModulus - 14*90000 // wraps at 14 s of programme
	r := newRig(t, 10*time.Second, memLimits(), g.Mux(40*time.Second))
	r.run(18 * time.Second)
	ticket, err := r.b.Dump(r.epoch.Add(18*time.Second), 3*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(40 * time.Second)
	if !ticket.Result().Complete {
		t.Fatal("did not resume")
	}
	rep := readOut(t, r.out, r.outAt, false)
	if spliceAt(t, rep.video) < 0 {
		t.Fatal("nothing was dumped")
	}
	for i := 1; i < len(rep.video); i++ {
		if step := wrap(rep.video[i].dts - rep.video[i-1].dts); step != delaytest.FrameTicks {
			t.Fatalf("video DTS steps %d at picture #%d across the wrap", step, rep.video[i].index)
		}
	}
	for i := 1; i < len(rep.pcrs); i++ {
		if d := wrap(rep.pcrs[i] - rep.pcrs[i-1]); d > 90000 {
			t.Fatalf("PCR jumps %d ticks across the wrap", d)
		}
	}
	if rep.ccBreaks != 0 {
		t.Errorf("%d continuity breaks", rep.ccBreaks)
	}
}

// dumpable says what Dump will do. After a dump has taken everything down to
// the margin, the status must not offer another: the console's button and the
// deck's face both key on it, and a press would only be refused.
//
// Mutation: compute Spliceable as "any unreleased cut point" again.
func TestSpliceableAgreesWithWhatDumpWillDo(t *testing.T) {
	g := delaytest.New(60)
	r := newRig(t, 12*time.Second, memLimits(), g.Mux(40*time.Second))
	r.run(15 * time.Second)
	if !r.b.Stats().Spliceable {
		t.Fatal("a full 12 s buffer of IDR GOPs is not spliceable")
	}
	ticket, err := r.b.Dump(r.epoch.Add(15*time.Second), 0, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(17 * time.Second)
	if !ticket.Result().Complete {
		t.Fatal("did not resume")
	}
	spliceable := r.b.Stats().Spliceable
	_, err = r.b.Dump(r.epoch.Add(17*time.Second), 0, DumpMargin)
	if spliceable != (err == nil) {
		t.Fatalf("Stats says spliceable=%t, and Dump answered %v", spliceable, err)
	}
	if spliceable {
		t.Fatal("right after a dump down to the margin, another is offered")
	}
}

// A source clock that steps backwards just after a resume must not stop the
// video. Every picture then reads as "before the splice"; without a bound on
// the gate it would drop them all until the clock climbed back past the floor
// -- twenty seconds of nothing here.
//
// Mutation: drop the `r.left < 0` case in rewrite.
func TestAClockThatStepsBackAfterAResumeDoesNotStopTheVideo(t *testing.T) {
	g := delaytest.New(60)
	g.StepFrom, g.Step = 16500*time.Millisecond, -20*90000
	r := newRig(t, 10*time.Second, memLimits(), g.Mux(40*time.Second))
	r.run(15 * time.Second)
	ticket, err := r.b.Dump(r.epoch.Add(15*time.Second), 2*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(40 * time.Second)
	if !ticket.Result().Complete {
		t.Fatal("did not resume")
	}
	rep := readOut(t, r.out, r.outAt, false)
	// The step is at picture 495; the gate looks at 64 PES starts at most.
	resumedBy := 495 + 2*64
	for _, v := range rep.video {
		if v.index > resumedBy {
			return
		}
	}
	t.Fatalf("no picture after #%d was released: a backward clock step after the resume stopped the video", resumedBy)
}
