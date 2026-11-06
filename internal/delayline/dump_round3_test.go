package delayline

import (
	"errors"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/delayline/delaytest"
	"github.com/rainmanjam/polyemesis/internal/mpegts"
)

// N1. A SECOND DUMP'S CUT WINDOW IS WHERE ITS CUT PICTURE ARRIVED. The cut
// point may have been stamped before the first dump's resume, under the
// smaller shift of the time; CutFrom must use that, or the captions of the
// words spoken between the true cut and the misplaced one are kept and air.
//
// Mutation: compute CutFrom from the buffer's current shift.
func TestASecondDumpsCutWindowStartsWhereItsCutPictureArrived(t *testing.T) {
	g := delaytest.New(60)
	r := newRig(t, 30*time.Second, memLimits(), g.Mux(80*time.Second))
	r.run(40 * time.Second)
	t1, err := r.b.Dump(r.epoch.Add(40*time.Second), 2*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(43 * time.Second)
	<-t1.Done()
	t2, err := r.b.Dump(r.epoch.Add(43*time.Second), 8*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.b.mu.Lock()
	pick, firstSplice := r.b.ts.splice, r.b.lastDumpAt
	r.b.mu.Unlock()
	if pick.at >= firstSplice {
		t.Fatal("the fixture's second cut is not before the first resume")
	}
	r.run(50 * time.Second)
	<-t2.Done()
	// Stamped before the first resume, when nothing had been dumped: the
	// buffer clock was real time.
	if got, want := t2.Result().CutFrom, r.epoch.Add(time.Duration(pick.at)); !got.Equal(want) {
		t.Fatalf("CutFrom %s, but the cut picture arrived at %s", got.Sub(r.epoch), want.Sub(r.epoch))
	}
}

// injectQuiet sends one PES on the quiet PID at 17.5 s of programme -- inside
// the window a dump at 17 s removes -- and, when earlier is false, drops the
// quiet PID's one PES at the start, so it has none before the cut.
func injectQuiet(t *testing.T, earlier bool) *delaytest.Stream {
	t.Helper()
	g := delaytest.New(60)
	g.QuietPID = true
	s := g.Mux(40 * time.Second)
	var pk [][]byte
	var tm []time.Duration
	injected := false
	for i, p := range s.Pkts {
		if !earlier && mpegts.PID(p) == delaytest.QuietPIDNumber {
			continue
		}
		pk = append(pk, p)
		tm = append(tm, s.Times[i])
		if !injected && s.Times[i] >= 17500*time.Millisecond && mpegts.PID(p) == delaytest.AudioPID && mpegts.PUSI(p) {
			q := append([]byte(nil), p...)
			q[1] = q[1]&0xE0 | byte(uint16(delaytest.QuietPIDNumber)>>8)
			q[2] = byte(uint16(delaytest.QuietPIDNumber) & 0xFF)
			pl := payloadOf(q)
			n := len(pl) - 6
			pl[4], pl[5] = byte(n>>8), byte(n)
			pk = append(pk, q)
			tm = append(tm, s.Times[i])
			injected = true
		}
	}
	if !injected {
		t.Fatal("nothing injected")
	}
	s.Pkts, s.Times = pk, tm
	return s
}

// N3. A QUIET PID'S PROGRAMME FROM THE DUMPED WINDOW DOES NOT AIR -- whether or
// not the PID sent anything before the cut. Subtitles and metadata are the
// programme too.
//
// Mutation: trim only the PIDs that had a PES before the cut, against their
// last PES's end.
func TestAQuietPIDsDumpedWindowDataDoesNotAir(t *testing.T) {
	for _, earlier := range []bool{false, true} {
		name := "no earlier PES"
		if earlier {
			name = "an earlier PES"
		}
		t.Run(name, func(t *testing.T) {
			r := newRig(t, 12*time.Second, memLimits(), injectQuiet(t, earlier))
			r.run(17 * time.Second)
			tk, err := r.b.Dump(r.epoch.Add(17*time.Second), 2*time.Second, DumpMargin)
			if err != nil {
				t.Fatal(err)
			}
			k1 := r.b.ts.splice.pts
			r.run(40 * time.Second)
			<-tk.Done()
			for i := 0; i+mpegts.PacketSize <= len(r.out); i += mpegts.PacketSize {
				p := r.out[i : i+mpegts.PacketSize]
				if mpegts.PID(p) != delaytest.QuietPIDNumber || !mpegts.PUSI(p) {
					continue
				}
				h, _ := parsePES(p, nil)
				if h.hasPTS && ptsAfter(k1, h.pts()) && ptsAfter(h.pts(), wrap(k1-6*90000)) {
					t.Fatalf("a quiet-PID PES from the dumped window aired at PTS %d, before the splice at %d", h.pts(), k1)
				}
			}
		})
	}
}

// N2. A PID THAT IS NEITHER PSI NOR IN THE PMT (ATSC PSIP on 0x1FFB) keeps its
// continuity counters across a splice.
func TestAPIDOutsideThePMTKeepsItsCountersAcrossASplice(t *testing.T) {
	g := delaytest.New(60)
	s := g.Mux(40 * time.Second)
	var pk [][]byte
	var tm []time.Duration
	cc := 0
	for i, p := range s.Pkts {
		pk, tm = append(pk, p), append(tm, s.Times[i])
		if i%50 == 0 {
			q := make([]byte, mpegts.PacketSize)
			q[0], q[1], q[2], q[3] = mpegts.SyncByte, 0x40|0x1F, 0xFB, 0x10|byte(cc&0x0F)
			for j := 5; j < mpegts.PacketSize; j++ {
				q[j] = 0xFF
			}
			cc++
			pk, tm = append(pk, q), append(tm, s.Times[i])
		}
	}
	s.Pkts, s.Times = pk, tm
	r := newRig(t, 12*time.Second, memLimits(), s)
	r.run(18 * time.Second)
	tk, err := r.b.Dump(r.epoch.Add(18*time.Second), 2*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(40 * time.Second)
	<-tk.Done()
	if rep := readOut(t, r.out, r.outAt, false); rep.ccBreaks != 0 {
		t.Fatalf("%d continuity-counter breaks", rep.ccBreaks)
	}
}

// R9. NO RANDOM-ACCESS PICTURE AT ALL after the cut (an audio-only failover,
// an unclassifiable codec, H.264 without the flag): the dump does not wait
// for ever. Past the force deadline it resumes at the live edge, says so, and
// the buffer takes dumps again.
//
// Mutation: drop the forced resume.
func TestADumpWithNoRandomAccessPictureEndsByForcing(t *testing.T) {
	g := delaytest.New(30)
	g.NoKeysFrom = 16 * 30
	r := newRig(t, 15*time.Second, memLimits(), g.Mux(90*time.Second))
	r.run(20 * time.Second)
	tk, err := r.b.Dump(r.epoch.Add(20*time.Second), 2*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(90 * time.Second)
	select {
	case <-tk.Done():
	default:
		t.Fatal("a dump with no random-access picture after the cut is still waiting 70 s later")
	}
	res := tk.Result()
	if !res.Complete || !res.Forced {
		t.Fatalf("result %+v: want a complete, forced resume", res)
	}
	if st := r.b.Stats(); st.Dumping || st.Packets == 0 {
		t.Fatalf("after the forced resume: dumping %t, %d packets", st.Dumping, st.Packets)
	}
	if _, err := r.b.Dump(r.epoch.Add(90*time.Second), time.Second, DumpMargin); errors.Is(err, ErrDumpPending) {
		t.Fatal("a later dump is still refused as pending")
	}
}

// R-d. RECOVERY AT A CRA. The resume picture is rewritten to BLA, so a
// decoder starts afresh instead of expecting the pictures before it, and the
// RASL pictures that reference those are dropped; nothing shown before the
// resume picture is kept.
//
// Mutations: don't rewrite the CRA; keep leading pictures that fit.
func TestARecoveryAtACRARewritesItToBLAAndDropsItsRASLPictures(t *testing.T) {
	g := delaytest.New(30)
	g.HEVC, g.KeyType, g.Leading = true, delaytest.HEVCIDRWRADL, true
	g.LeadingType, g.LeadingFrom = 8, 16*30 // RASL_N after every CRA
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
	res := tk.Result()
	if !res.Complete || !res.Recovery {
		t.Fatalf("result %+v: want recovery", res)
	}
	rep := readOut(t, r.out, r.outAt, true)
	var regular []esUnit
	for _, v := range rep.video {
		if v.index < delaytest.LeadingIndex {
			regular = append(regular, v)
		}
	}
	at := spliceAt(t, regular)
	if at < 0 {
		t.Fatal("nothing was dumped")
	}
	k2 := regular[at]
	if k2.nal != 18 {
		t.Fatalf("the recovery picture went out as NAL type %d, want 18 (BLA_N_LP)", k2.nal)
	}
	afterK2 := false
	for _, v := range rep.video {
		if v.index == k2.index {
			afterK2 = true
			continue
		}
		if afterK2 && v.index == delaytest.LeadingIndex+k2.index {
			t.Fatalf("the recovery CRA's RASL picture (NAL %d) was kept", v.nal)
		}
	}
}

// R2. AN UNBOUNDED AUDIO PES (length 0) open across a keyframe makes it no cut
// point either: it is open until the PID's next start, and a continuation
// before that start means it ran across the keyframe. One that ends at the
// keyframe -- its PID starts a new PES first -- leaves it a cut point.
func TestAnUnboundedAudioPESOpenAcrossAKeyframeMakesItNoCutPoint(t *testing.T) {
	g := delaytest.New(60)
	g.AudioUnbounded = true
	s := g.Mux(40 * time.Second)
	for at := 8 * time.Second; at <= 16*time.Second; at += 2 * time.Second {
		moveAfterKey(t, s, at)
	}
	r := newRig(t, 12*time.Second, memLimits(), s)
	r.run(20 * time.Second) // the head is at 8 s: every straddled keyframe is still held
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	straddled, clean := 0, 0
	for _, m := range r.b.marks {
		if m.at >= int64(8*time.Second) && m.at <= int64(16*time.Second)+int64(time.Millisecond) {
			straddled++
		} else {
			clean++
		}
	}
	if straddled != 0 {
		t.Fatalf("%d keyframes with an unbounded audio PES open across them are cut points", straddled)
	}
	if clean == 0 {
		t.Fatal("no keyframe became a cut point: an unbounded audio PES that ended there must not block it")
	}
}

// R3. A RESUME PICTURE WHOSE PES HEADER RUNS INTO A SECOND PACKET is still the
// resume picture: the resync reads the header across the two, as the normal
// path does, instead of waiting a whole GOP for one that fits.
func TestAResumePictureWithASplitHeaderIsNotSkipped(t *testing.T) {
	g := delaytest.New(60)
	g.SplitVideoHeaders = true
	r := newRig(t, 12*time.Second, memLimits(), g.Mux(40*time.Second))
	r.run(18 * time.Second)
	tk, err := r.b.Dump(r.epoch.Add(18*time.Second), 2*time.Second, DumpMargin)
	if err != nil {
		t.Fatal(err)
	}
	r.run(40 * time.Second)
	if !tk.Result().Complete {
		t.Fatal("did not resume")
	}
	rep := readOut(t, r.out, r.outAt, false)
	at := spliceAt(t, rep.video)
	if at < 0 || rep.video[at].index != 540 {
		t.Fatalf("resumed at picture #%d; the first keyframe after the dump is #540", rep.video[at].index)
	}
}

// R12. DUMPABLE ASKS DUMP'S OWN QUESTION, on the same anchor: the later of the
// release horizon and the head. With the release side behind (the pump late),
// the head is older than the horizon, and a head-anchored answer offered a
// dump that Dump then refused.
func TestSpliceableUsesDumpsAnchorWhenTheReleaseSideIsBehind(t *testing.T) {
	g := delaytest.New(60)
	s := g.Mux(40 * time.Second)
	r := newRig(t, 12*time.Second, memLimits(), s)
	r.run(18 * time.Second)
	// Audio arrives up to 25 s -- no new keyframe, so no new cut point -- and
	// nothing is released meanwhile: the head stays at 6 s while the release
	// horizon moves to 13 s.
	for r.next < len(s.Pkts) && s.Times[r.next] < 25*time.Second {
		if mpegts.PID(s.Pkts[r.next]) == delaytest.AudioPID {
			if err := r.b.Push(r.epoch.Add(s.Times[r.next]), s.Pkts[r.next]); err != nil {
				t.Fatal(err)
			}
		}
		r.next++
	}
	spliceable := r.b.Stats().Spliceable
	_, err := r.b.Dump(r.epoch.Add(25*time.Second-time.Millisecond), 0, DumpMargin)
	if spliceable != (err == nil) {
		t.Fatalf("Stats says spliceable=%t and Dump answered %v", spliceable, err)
	}
}

// R21. THE RESYNC'S AUDIO IS ONE BOUND ACROSS EVERY PID, not one per PID: two
// audio PIDs each sending large complete PESs together stay inside it.
func TestTheResyncAudioBudgetIsSharedAcrossPIDs(t *testing.T) {
	g := delaytest.New(60)
	g.QuietPID = true
	r := newRig(t, 12*time.Second, memLimits(), g.Mux(20*time.Second))
	r.run(18 * time.Second)
	if _, err := r.b.Dump(r.epoch.Add(18*time.Second), 2*time.Second, DumpMargin); err != nil {
		t.Fatal(err)
	}
	pes := func(pid uint16, cc byte, first bool) []byte {
		p := make([]byte, mpegts.PacketSize)
		p[0], p[1], p[2], p[3] = mpegts.SyncByte, byte(pid>>8), byte(pid), 0x10|cc&0x0F
		if first {
			n := 300*184 - 6
			p[1] |= 0x40
			copy(p[4:], []byte{0, 0, 1, 0xC0, byte(n >> 8), byte(n), 0x80, 0x80, 5, 0x21, 0, 1, 0, 1})
		}
		return p
	}
	at := r.epoch.Add(18 * time.Second)
	for i := 0; i < 60; i++ {
		for _, pid := range []uint16{delaytest.AudioPID, delaytest.QuietPIDNumber} {
			_ = r.b.Push(at, pes(pid, 0, true))
			for j := 0; j < 300; j++ {
				_ = r.b.Push(at, pes(pid, byte(j+1), false))
			}
		}
	}
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	total := 0
	for _, c := range r.b.ts.audio {
		for _, d := range c.done {
			total += len(d)
		}
	}
	if len(r.b.ts.audio) < 2 {
		t.Fatalf("only %d PIDs cached: the fixture does not reach the shared bound", len(r.b.ts.audio))
	}
	if budget := audioCacheBudget(r.b.lim); total > budget {
		t.Fatalf("the resync holds %d bytes of audio across PIDs, over its %d budget", total, budget)
	}
}
