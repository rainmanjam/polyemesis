package captions

import (
	"bytes"
	"reflect"
	"sort"
	"testing"
)

// The fixtures in this file are built by an independent MPEG-TS writer and read
// back by an independent parser. Neither calls the injector's own packetizer,
// so a bug in the injector cannot hide itself by being mirrored in the test.

const (
	testPMTPID   = 0x1000
	testVideoPID = 0x0100
	testAudioPID = 0x0101
)

// tsPkt builds one 188-byte packet. af is the adaptation field content after
// its length byte (flags first), or nil for none. A packet whose payload is
// shorter than the room left gets adaptation-field stuffing, which is how a real
// muxer fills the last packet of a PES.
func tsPkt(pid uint16, pusi bool, cc uint8, af []byte, payload []byte) []byte {
	p := make([]byte, 188)
	p[0] = 0x47
	p[1] = byte(pid >> 8)
	if pusi {
		p[1] |= 0x40
	}
	p[2] = byte(pid)
	room := 184
	if af != nil {
		room -= 1 + len(af)
	}
	if len(payload) > room {
		panic("tsPkt: payload does not fit")
	}
	at := 4
	if af == nil && room == len(payload) {
		p[3] = 0x10 | cc&0x0f
	} else {
		p[3] = 0x30 | cc&0x0f
		// The length byte counts everything after it, so the field fills the
		// packet up to the payload exactly.
		afLen := 183 - len(payload)
		p[at] = byte(afLen)
		at++
		if afLen > 0 {
			content := af
			if content == nil {
				content = []byte{0x00}
			}
			copy(p[at:], content)
			at += len(content)
			for at < 5+afLen {
				p[at] = 0xFF
				at++
			}
		}
	}
	copy(p[188-len(payload):], payload)
	return p
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// psiPacket wraps a PSI section in a packet with a zero pointer field.
func psiPacket(pid uint16, cc uint8, section []byte) []byte {
	return tsPkt(pid, true, cc, nil, append([]byte{0x00}, section...))
}

func patSection(pmt uint16) []byte {
	return []byte{0x00, 0xB0, 0x0D, 0x00, 0x01, 0xC1, 0x00, 0x00,
		0x00, 0x01, 0xE0 | byte(pmt>>8), byte(pmt), 0, 0, 0, 0}
}

func pmtSection(video, audio byte) []byte {
	return []byte{0x02, 0xB0, 0x17, 0x00, 0x01, 0xC1, 0x00, 0x00,
		0xE0 | 0x01, 0x00, // PCR PID
		0xF0, 0x00, // program_info_length
		video, 0xE0 | 0x01, 0x00, 0xF0, 0x00, // video ES
		0x0F, 0xE0 | 0x01, 0x01, 0xF0, 0x00, // audio ES, AAC
		0, 0, 0, 0}
}

// tsPTS encodes a 33-bit PTS in the five-byte PES field with its marker bits.
func tsPTS(pts int64) []byte {
	return []byte{
		0x21 | byte((pts>>29)&0x0E),
		byte(pts >> 22),
		byte((pts>>14)&0xFE) | 1,
		byte(pts >> 7),
		byte((pts<<1)&0xFE) | 1,
	}
}

// pesHeader is a video PES header with a PTS and no DTS. The length is zero,
// which is how video PES packets are written when they exceed 64 KiB.
func pesHeader(pts int64) []byte {
	h := []byte{0x00, 0x00, 0x01, 0xE0, 0x00, 0x00, 0x80, 0x80, 0x05}
	return append(h, tsPTS(pts)...)
}

// pesPackets splits one PES into packets. The first packet optionally carries
// an adaptation field, which is where a PCR and random access indicator sit.
func pesPackets(pid uint16, cc *uint8, pes []byte, firstAF []byte) []byte {
	var out []byte
	first := true
	for len(pes) > 0 {
		var af []byte
		room := 184
		if first && firstAF != nil {
			af = firstAF
			room -= 1 + len(af)
		}
		n := min(room, len(pes))
		out = append(out, tsPkt(pid, first, *cc, af, pes[:n])...)
		*cc = (*cc + 1) & 0x0f
		pes = pes[n:]
		first = false
	}
	return out
}

// annexB joins NAL units with four-byte start codes.
func annexB(nals ...[]byte) []byte {
	var b []byte
	for _, n := range nals {
		b = append(b, 0, 0, 0, 1)
		b = append(b, n...)
	}
	return b
}

// auFor is one access unit: parameter sets and delimiter on the IDR, then a
// slice. The slice's first_mb_in_slice is zero, so the injector sees a picture.
func auFor(idr bool) []byte {
	nals := [][]byte{{0x09, 0xF0}, {0x67, 0x42, 0xC0, 0x1E}, {0x68, 0xCE, 0x3C, 0x80}}
	if idr {
		nals = append(nals, []byte{0x65, 0x88, 0x84, 0x21})
	} else {
		nals = append(nals, []byte{0x41, 0x9A, 0x22, 0x10})
	}
	return annexB(nals...)
}

// psFixture is the PAT and PMT that every fixture starts with.
func psFixture(videoType byte) []byte {
	var b []byte
	b = append(b, psiPacket(0, 0, patSection(testPMTPID))...)
	b = append(b, psiPacket(testPMTPID, 0, pmtSection(videoType, 0x0F))...)
	return b
}

// frameTicks is 30 fps on the 90 kHz clock.
const frameTicks = 3000

// streamOf builds the PSI and a video PES per access unit, in the order given,
// with PTS = display index * frameTicks plus a 1 s base. The video packets carry
// the continuity counter the test expects, and audio packets are interleaved so
// the injector has to pass non-video packets through.
func streamOf(displayOrder []int) []byte {
	b := psFixture(0x1B)
	var vcc, acc uint8
	for _, d := range displayOrder {
		pts := int64(90000 + d*frameTicks)
		// The first of each GOP is an IDR, every eighth frame in display order.
		auES := auFor(d%8 == 0)
		pes := append(pesHeader(pts), auES...)
		var af []byte
		if d%8 == 0 {
			// Random access and a PCR on the first packet of an IDR PES.
			af = []byte{0x50, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
		}
		b = append(b, pesPackets(testVideoPID, &vcc, pes, af)...)
		b = append(b, tsPkt(testAudioPID, true, acc, nil, append([]byte{0x00, 0x00, 0x01, 0xC0, 0x00, 0x00}, 0x80, 0x00, 0x00, 0xAA))...)
		acc = (acc + 1) & 0x0f
	}
	return b
}

// outPacket is one parsed packet.
type outPacket struct {
	pid        uint16
	pusi       bool
	cc         uint8
	hasPayload bool
	af         []byte
	payload    []byte
}

func parsePackets(t *testing.T, b []byte) []outPacket {
	t.Helper()
	if len(b)%188 != 0 {
		t.Fatalf("output length %d is not a multiple of 188", len(b))
	}
	var out []outPacket
	for i := 0; i < len(b); i += 188 {
		p := b[i : i+188]
		if p[0] != 0x47 {
			t.Fatalf("packet at %d has sync byte %#x", i, p[0])
		}
		op := outPacket{
			pid:  uint16(p[1]&0x1F)<<8 | uint16(p[2]),
			pusi: p[1]&0x40 != 0,
			cc:   p[3] & 0x0f,
		}
		at := 4
		afc := (p[3] >> 4) & 0x3
		if afc&0x2 != 0 {
			n := int(p[at])
			op.af = p[at+1 : at+1+n]
			at += 1 + n
		}
		if afc&0x1 != 0 {
			op.hasPayload = true
			op.payload = p[at:]
		}
		out = append(out, op)
	}
	return out
}

// esUnit is one reassembled video access unit read back from the output.
type esUnit struct {
	pts     int64
	es      []byte
	firstAF []byte
}

// videoUnits reassembles the video PES of a parsed output, in stream order.
func videoUnits(t *testing.T, pkts []outPacket, pid uint16) []esUnit {
	t.Helper()
	var units []esUnit
	var cur []byte
	var curAF []byte
	open := false
	flush := func() {
		if !open {
			return
		}
		if len(cur) < 14 || !bytes.Equal(cur[:3], []byte{0, 0, 1}) {
			t.Fatalf("PES does not start with a start code: % x", cur[:min(len(cur), 8)])
		}
		hl := 9 + int(cur[8])
		pts := readPTS(cur[9:14])
		units = append(units, esUnit{pts: pts, es: append([]byte(nil), cur[hl:]...), firstAF: curAF})
		cur = nil
	}
	for _, p := range pkts {
		if p.pid != pid {
			continue
		}
		if p.pusi {
			flush()
			open = true
			curAF = p.af
		}
		if p.hasPayload {
			cur = append(cur, p.payload...)
		}
	}
	flush()
	return units
}

// unescapeRBSP removes emulation prevention bytes, the inverse of escapeRBSP.
func unescapeRBSP(b []byte) []byte {
	var out []byte
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// ccPairOf reads the caption pair out of an SEI NAL, if it carries one.
func ccPairOf(nal []byte) (Pair, bool) {
	if nalType(nal) != 6 {
		return Pair{}, false
	}
	r := unescapeRBSP(nal[1:])
	if len(r) < 16 || r[0] != 4 || r[1] != 14 || !bytes.Equal(r[2:10], a53Header) {
		return Pair{}, false
	}
	return Pair{r[13], r[14]}, true
}

// TestInjectorPassesNonVideoPacketsThroughInTheirOriginalOrder covers audio and
// PSI. A caption must never cost the audio a packet or move it past another
// audio packet, or the mix breaks on the destination.
func TestInjectorPassesNonVideoPacketsThroughInTheirOriginalOrder(t *testing.T) {
	in := streamOf([]int{0, 1, 2})
	inj := NewInjector(NewSchedule(0))
	out := append(inj.Write(in), inj.Flush()...)

	audioIn := audioPayloads(t, parsePackets(t, in))
	audioOut := audioPayloads(t, parsePackets(t, out))
	if !reflect.DeepEqual(audioIn, audioOut) {
		t.Fatalf("audio packets changed:\n in %d\nout %d", len(audioIn), len(audioOut))
	}
}

func audioPayloads(t *testing.T, pkts []outPacket) [][]byte {
	t.Helper()
	var out [][]byte
	for _, p := range pkts {
		if p.pid == testAudioPID {
			out = append(out, p.payload)
		}
	}
	return out
}

// TestInjectorPlacesEachCaptionPairInThePictureWhosePresentationTimeMatches is
// the B-frame test. The stream is in decode order I P B B P B B, so the display
// order is not the arrival order. The caption must still read back in display
// order, one picture per pair, and the first pair must sit on the picture the
// cue starts at.
func TestInjectorPlacesEachCaptionPairInThePictureWhosePresentationTimeMatches(t *testing.T) {
	// Display indexes in decode order for an IBBP group: 0 I, 3 P, 1 B, 2 B.
	decode := []int{0, 3, 1, 2, 6, 4, 5, 9, 7, 8}
	in := streamOf(decode)

	cueAt := int64(90000 + 1*frameTicks) // starts on display frame 1
	sched := NewSchedule(0)
	sched.Add(Cue{StartPTS: cueAt, EndPTS: cueAt + 10*90000, Text: "HI"})
	inj := NewInjector(sched)
	out := append(inj.Write(in), inj.Flush()...)

	units := videoUnits(t, parsePackets(t, out), testVideoPID)
	sort.Slice(units, func(i, j int) bool { return units[i].pts < units[j].pts })

	var got []Pair
	for _, u := range units {
		for _, nal := range splitAnnexB(u.es) {
			if p, ok := ccPairOf(nal); ok && p != nullPair {
				got = append(got, p)
			}
		}
	}
	want := PopOnScript("HI")
	if len(got) < len(want) {
		t.Fatalf("caption pairs in display order = %d, want at least %d", len(got), len(want))
	}
	if !reflect.DeepEqual(got[:len(want)], want) {
		t.Fatalf("caption pairs in display order = %v, want %v", got[:len(want)], want)
	}
	// The first non-null pair must be on the frame the cue starts at.
	for _, u := range units {
		for _, nal := range splitAnnexB(u.es) {
			if p, ok := ccPairOf(nal); ok && p == want[0] {
				if u.pts != cueAt {
					t.Fatalf("RCL landed on PTS %d, want the cue start %d", u.pts, cueAt)
				}
				return
			}
		}
	}
	t.Fatalf("RCL pair never appeared in the output")
}

// TestInjectorInsertsOneCaptionSEIPerPictureAndKeepsEveryOtherNAL checks that
// the only change to the elementary stream is the added SEI. Every original NAL
// comes back in order, byte for byte.
func TestInjectorInsertsOneCaptionSEIPerPictureAndKeepsEveryOtherNAL(t *testing.T) {
	in := streamOf([]int{0, 1, 2, 3})
	inj := NewInjector(NewSchedule(0))
	out := append(inj.Write(in), inj.Flush()...)

	inUnits := videoUnits(t, parsePackets(t, in), testVideoPID)
	outUnits := videoUnits(t, parsePackets(t, out), testVideoPID)
	if len(inUnits) != len(outUnits) {
		t.Fatalf("access units: in %d, out %d", len(inUnits), len(outUnits))
	}
	for i := range inUnits {
		var kept [][]byte
		seis := 0
		for _, nal := range splitAnnexB(outUnits[i].es) {
			if nalType(nal) == 6 {
				seis++
				continue
			}
			kept = append(kept, nal)
		}
		if seis != 1 {
			t.Fatalf("access unit %d has %d SEIs, want 1", i, seis)
		}
		if !reflect.DeepEqual(kept, splitAnnexB(inUnits[i].es)) {
			t.Fatalf("access unit %d lost or changed a non-SEI NAL", i)
		}
	}
}

// TestInjectorKeepsThePCRAndRandomAccessFlagsOnTheFirstPacketOfEachPES. A PCR
// on the wrong packet moves the destination's clock, and the random access
// indicator is what lets a player join the stream at that point.
func TestInjectorKeepsThePCRAndRandomAccessFlagsOnTheFirstPacketOfEachPES(t *testing.T) {
	in := streamOf([]int{0, 1, 2, 3, 4, 5, 6, 7, 8})
	inj := NewInjector(NewSchedule(0))
	out := append(inj.Write(in), inj.Flush()...)

	inAF := videoFirstAFs(parsePackets(t, in))
	outAF := videoFirstAFs(parsePackets(t, out))
	if !reflect.DeepEqual(inAF, outAF) {
		t.Fatalf("first-packet adaptation fields changed:\n in %x\nout %x", inAF, outAF)
	}
}

func videoFirstAFs(pkts []outPacket) [][]byte {
	var out [][]byte
	for _, p := range pkts {
		if p.pid == testVideoPID && p.pusi {
			out = append(out, p.af)
		}
	}
	return out
}

// TestInjectorKeepsContinuityCountersContiguousOnTheVideoPID. A decoder that
// sees a counter skip reports packet loss and may drop the access unit it was
// building, which is the caption and the picture together.
func TestInjectorKeepsContinuityCountersContiguousOnTheVideoPID(t *testing.T) {
	in := streamOf([]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11})
	inj := NewInjector(NewSchedule(0))
	out := append(inj.Write(in), inj.Flush()...)

	var prev int = -1
	for _, p := range parsePackets(t, out) {
		if p.pid != testVideoPID || !p.hasPayload {
			continue
		}
		if prev >= 0 && int(p.cc) != (prev+1)&0x0f {
			t.Fatalf("video continuity counter went %d then %d", prev, p.cc)
		}
		prev = int(p.cc)
	}
	if prev < 0 {
		t.Fatalf("no video packets in the output")
	}
}

// TestInjectorLeavesAnHEVCVideoStreamAloneAndSaysSo. Only H.264 carries the
// A/53 SEI this path uses. An HEVC source must pass through unchanged, and the
// injector must count it so the operator can be told.
func TestInjectorLeavesAnHEVCVideoStreamAloneAndSaysSo(t *testing.T) {
	var in []byte
	in = append(in, psFixture(0x24)...)
	var cc uint8
	for d := 0; d < 4; d++ {
		pes := append(pesHeader(int64(90000+d*frameTicks)), auFor(true)...)
		in = append(in, pesPackets(testVideoPID, &cc, pes, nil)...)
	}
	inj := NewInjector(NewSchedule(0))
	out := append(inj.Write(in), inj.Flush()...)
	if !bytes.Equal(out, in) {
		t.Fatalf("HEVC stream was rewritten")
	}
	if inj.Stats().Unsupported == 0 {
		t.Fatalf("Stats().Unsupported = 0 for an HEVC video PID, want it counted")
	}
}

// TestInjectorHoldsABoundedLookaheadAndFlushReleasesEverything covers the
// latency the injector adds. A picture is held until the pictures within the
// reorder window have arrived, so only a bounded tail is held back. Flush must
// release that tail, so that nothing is lost at the end of a stream.
func TestInjectorHoldsABoundedLookaheadAndFlushReleasesEverything(t *testing.T) {
	var order []int
	for d := 0; d < 20; d++ {
		order = append(order, d)
	}
	in := streamOf(order)
	inj := NewInjector(NewSchedule(0))
	before := videoUnits(t, parsePackets(t, inj.Write(in)), testVideoPID)
	after := videoUnits(t, parsePackets(t, inj.Flush()), testVideoPID)
	if len(before)+len(after) != 20 {
		t.Fatalf("released %d + %d access units, want 20 in total", len(before), len(after))
	}
	if held := 20 - len(before); held == 0 || held > slotWindow+1 {
		t.Fatalf("%d access units held back before Flush, want between 1 and %d", held, slotWindow+1)
	}
}

// TestInjectorPassesVideoThroughUntilItsPMTHasBeenSeen. Video before PSI cannot be
// identified, so it passes through rather than being guessed at.
func TestInjectorPassesVideoThroughUntilItsPMTHasBeenSeen(t *testing.T) {
	var cc uint8
	in := pesPackets(testVideoPID, &cc, append(pesHeader(90000), auFor(true)...), nil)
	inj := NewInjector(NewSchedule(0))
	out := append(inj.Write(in), inj.Flush()...)
	if !bytes.Equal(out, in) {
		t.Fatalf("video before PSI was altered")
	}
}

// TestInjectorDropsAVideoPacketWhoseAdaptationFieldRunsPastThePacket covers a
// malformed packet from the encoder. Its adaptation field length, 200, is longer
// than the 188-byte packet, so the field cannot be read. The injector must not
// panic on it, because a panic would take the relay down with the stream. The
// packet is dropped and counted, and the packets around it still go out.
func TestInjectorDropsAVideoPacketWhoseAdaptationFieldRunsPastThePacket(t *testing.T) {
	bad := make([]byte, tsPacketSize)
	bad[0] = tsSyncByte
	bad[1] = 0x40 | byte(testVideoPID>>8) // payload unit start on the video PID
	bad[2] = byte(testVideoPID & 0xFF)
	bad[3] = 0x30 // adaptation field and payload
	bad[4] = 200  // length byte: runs past the packet
	in := append(psFixture(0x1B), bad...)
	inj := NewInjector(NewSchedule(0))
	out := append(inj.Write(in), inj.Flush()...)
	if !bytes.Equal(out, psFixture(0x1B)) {
		t.Fatalf("the malformed packet was not dropped, or the PSI around it was altered")
	}
	if got := inj.Stats().Malformed; got != 1 {
		t.Fatalf("Stats().Malformed = %d, want 1", got)
	}
}

// TestInjectorBoundsTheAccessUnitsItHoldsWhileTheirSlotsNeverFinalise covers a
// stream whose presentation times stop advancing. Every picture then lands on the
// same caption slot, which never becomes final, so the units waiting for it would
// pile up without limit. The queue must stay within its cap, and what is over it
// goes out unchanged rather than being held.
func TestInjectorBoundsTheAccessUnitsItHoldsWhileTheirSlotsNeverFinalise(t *testing.T) {
	var cc uint8
	var in []byte
	in = append(in, psFixture(0x1B)...)
	for i := 0; i < 5000; i++ {
		in = append(in, pesPackets(testVideoPID, &cc, append(pesHeader(90000), auFor(false)...), nil)...)
	}
	inj := NewInjector(NewSchedule(0))
	_ = inj.Write(in)
	if n := len(inj.ready); n > maxReadyUnits {
		t.Fatalf("%d access units wait on a slot that never finalises, want at most %d", n, maxReadyUnits)
	}
	if b := inj.bufferedBytes(); b > maxBufferedBytes {
		t.Fatalf("%d bytes are buffered, want at most %d", b, maxBufferedBytes)
	}
}

// TestInjectorStopsHoldingAPictureWhoseStartIsNeverFollowedByAnotherPayloadStart
// covers a single access unit that never ends. Its packets must stop being
// collected once they pass the per-unit cap, so that a stream without picture
// starts cannot grow the buffer without limit.
func TestInjectorStopsHoldingAPictureWhoseStartIsNeverFollowedByAnotherPayloadStart(t *testing.T) {
	var cc uint8
	pes := append(pesHeader(90000), auFor(false)...)
	pes = append(pes, make([]byte, (maxHeldPackets+100)*184)...)
	in := append(psFixture(0x1B), pesPackets(testVideoPID, &cc, pes, nil)...)
	inj := NewInjector(NewSchedule(0))
	_ = inj.Write(in)
	if inj.held != nil && len(inj.held.pkts) > maxHeldPackets {
		t.Fatalf("a single access unit holds %d packets, want at most %d", len(inj.held.pkts), maxHeldPackets)
	}
}

// TestInjectorKeepsAPESInStreamOrderWhenItsPacketsOverflowTheHoldCap covers one
// picture that runs past the per-unit cap. The head of the picture was queued
// when the cap was hit, so the packets after it must not reach the output before
// it. Read back, the picture must come out whole, with its bytes unchanged.
func TestInjectorKeepsAPESInStreamOrderWhenItsPacketsOverflowTheHoldCap(t *testing.T) {
	var cc uint8
	head := auFor(false)
	pes := append(pesHeader(90000), head...)
	pad := make([]byte, (maxHeldPackets+100)*184)
	pes = append(pes, pad...)
	in := append(psFixture(0x1B), pesPackets(testVideoPID, &cc, pes, nil)...)
	inj := NewInjector(NewSchedule(0))
	out := append(inj.Write(in), inj.Flush()...)
	units := videoUnits(t, parsePackets(t, out), testVideoPID)
	if len(units) != 1 {
		t.Fatalf("%d video units came out, want the one picture", len(units))
	}
	want := append(append([]byte(nil), head...), pad...)
	if !bytes.Equal(units[0].es, want) {
		t.Fatalf("the picture came out out of order or altered after the hold cap was hit")
	}
}

// TestInjectorReleasesEveryAccessUnitOnceWhenItsSlotNeverFinalises checks the
// frozen-clock stream from the bounds test end to end. Every picture must come
// out exactly once after Flush, not only stay within the cap.
func TestInjectorReleasesEveryAccessUnitOnceWhenItsSlotNeverFinalises(t *testing.T) {
	var cc uint8
	in := psFixture(0x1B)
	const n = 500
	for i := 0; i < n; i++ {
		in = append(in, pesPackets(testVideoPID, &cc, append(pesHeader(90000), auFor(false)...), nil)...)
	}
	inj := NewInjector(NewSchedule(0))
	out := append(inj.Write(in), inj.Flush()...)
	if got := len(videoUnits(t, parsePackets(t, out), testVideoPID)); got != n {
		t.Fatalf("%d video units came out, want %d", got, n)
	}
}

// TestInjectorResumesCaptionsInOrderAcrossABackwardClockJump covers a 20 second
// backward jump in the middle of a stream. Every picture must come out once and
// in input order, and the captions queued for the new timeline must resume.
func TestInjectorResumesCaptionsInOrderAcrossABackwardClockJump(t *testing.T) {
	var cc uint8
	in := psFixture(0x1B)
	var wantPTS []int64
	for i := 0; i < 30; i++ {
		pts := int64(40*90000 + i*frameTicks)
		wantPTS = append(wantPTS, pts)
		in = append(in, pesPackets(testVideoPID, &cc, append(pesHeader(pts), auFor(false)...), nil)...)
	}
	for i := 0; i < 30; i++ {
		pts := int64(20*90000 + i*frameTicks)
		wantPTS = append(wantPTS, pts)
		in = append(in, pesPackets(testVideoPID, &cc, append(pesHeader(pts), auFor(false)...), nil)...)
	}
	sched := NewSchedule(0)
	sched.Add(Cue{StartPTS: 20 * 90000, EndPTS: 25 * 90000, Text: "BACK"})
	inj := NewInjector(sched)
	out := append(inj.Write(in), inj.Flush()...)

	units := videoUnits(t, parsePackets(t, out), testVideoPID)
	if len(units) != len(wantPTS) {
		t.Fatalf("%d video units came out, want %d", len(units), len(wantPTS))
	}
	var got []int64
	var pairs []Pair
	for _, u := range units {
		got = append(got, u.pts)
		for _, nal := range splitAnnexB(u.es) {
			if p, ok := ccPairOf(nal); ok && p != nullPair {
				pairs = append(pairs, p)
			}
		}
	}
	if !reflect.DeepEqual(got, wantPTS) {
		t.Fatalf("the pictures came out out of input order across the clock jump")
	}
	if len(pairs) == 0 || pairs[0] != PopOnScript("BACK")[0] {
		t.Fatalf("captions did not resume on the new timeline after the clock jump")
	}
}

// TestPMTUpdateReplacesTheDiscoveredVideoPID: after an encoder reconnect the program map can move video to a new PID.
// The injector kept the first PID it saw, so the new video passed through without captions and the old PID was still
// read as video. A PMT update must replace the video PID.
//
// MUTATION, internal/captions/tsinject.go in parsePMT: restore the `if in.videoPID == 0` guard. Observed: FAIL --
// videoPID is 0x0100, want 0x0200.
func TestPMTUpdateReplacesTheDiscoveredVideoPID(t *testing.T) {
	inj := NewInjector(NewSchedule(0))
	inj.parsePMT(psiPacket(testPMTPID, 0, pmtSection(streamTypeH264, 0x0F)))
	moved := pmtSection(streamTypeH264, 0x0F)
	moved[13] = 0xE2 // the video ES PID is in bytes 13-14; 0x02 in the high bits makes it 0x0200
	inj.parsePMT(psiPacket(testPMTPID, 1, moved))
	if inj.videoPID != 0x0200 {
		t.Fatalf("videoPID = %#x, want 0x0200 after the PMT moved video", inj.videoPID)
	}
}
