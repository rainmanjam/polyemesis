package captions

// Injector rewrites an MPEG-TS stream so that every H.264 picture carries the
// CEA-608 pair its presentation time is due. It sits between the relay and a
// destination's FFmpeg and changes nothing else: non-video packets pass through
// in their original order, the video elementary stream gains one caption SEI per
// picture, and the adaptation fields that hold the PCR and random access flags
// are kept on the first packet of each PES.
//
// Two things cost latency, and both are bounded. Each access unit is held until
// the next one starts, because its length is unknown until then. And an access
// unit is emitted only once its caption slot is final, which needs the pictures
// within the reorder window to have arrived. Together that is a few pictures,
// well under a second. Flush releases what is held.
//
// A video stream that is not H.264 (HEVC, for example) is passed through
// untouched and counted in Stats, because the A/53 SEI this path carries is an
// H.264 construct.

const (
	tsPacketSize = 188
	tsSyncByte   = 0x47
	pidPAT       = 0x0000

	streamTypeH264 = 0x1B
	streamTypeHEVC = 0x24
)

// InjectorStats counts what the injector did. Pictures is every access unit
// that had a picture start on the H.264 PID, and Injected is how many of those
// received an SEI. Unsupported is the number of non-H.264 video streams found in
// the program map, and PCRInMiddle is access units left as they were because a
// PCR sat on a packet the injector would have to rebuild.
type InjectorStats struct {
	Pictures    int
	Injected    int
	Unsupported int
	PCRInMiddle int
	// Malformed is the video packets dropped because their adaptation field
	// claims more bytes than the packet holds.
	Malformed int
}

// The caps that keep the injector's memory bounded whatever the stream does. A
// picture is claimed on its first packet and waits for its slot to finalise, so
// a stream whose timestamps stop advancing would otherwise queue units forever.
// A PES whose next start never arrives would grow its packet list forever. Both
// are cut off here. Units over a cap go out unchanged, without their caption.
const (
	// maxHeldPackets bounds one access unit's packets, about 770 KB.
	maxHeldPackets = 4096
	// maxReadyUnits bounds the access units queued behind their caption slots.
	maxReadyUnits = 64
	// maxBufferedBytes bounds everything the injector holds, in bytes.
	maxBufferedBytes = 16 << 20
)

// Injector holds the state of one stream. It is not safe for concurrent use: a
// destination's stream is fed from one goroutine.
type Injector struct {
	sched *Schedule

	pmtPID   uint16
	videoPID uint16
	hevcPID  uint16
	pending  []byte

	// held is the access unit being collected. ready holds complete access
	// units, in stream order, that are waiting for their caption slot to be
	// final.
	held  *accessUnit
	ready []*accessUnit

	nextCC uint8
	stats  InjectorStats
}

// accessUnit is one video PES as it arrived: its packets, and the presentation
// time read from its first packet, which is what keys the caption.
type accessUnit struct {
	pkts   [][]byte
	pts    int64
	hasPTS bool
	// unchanged marks a unit that went over a cap or was released by a clock
	// discontinuity. It is passed on as it arrived, with no caption.
	unchanged bool
}

// NewInjector returns an injector that takes its caption pairs from sched.
func NewInjector(sched *Schedule) *Injector {
	return &Injector{sched: sched}
}

// Stats reports what the injector has done so far.
func (in *Injector) Stats() InjectorStats { return in.stats }

// Write takes stream bytes, which need not be packet aligned, and returns the
// rewritten bytes that are ready. Packets for the video PID are returned only
// once their access unit is complete.
func (in *Injector) Write(data []byte) []byte {
	buf := append(in.pending, data...)
	var out []byte
	for len(buf) >= tsPacketSize {
		if buf[0] != tsSyncByte {
			// Lost sync. Discard up to the next sync byte, if there is one.
			i := 1
			for i < len(buf) && buf[i] != tsSyncByte {
				i++
			}
			buf = buf[i:]
			continue
		}
		out = append(out, in.packet(buf[:tsPacketSize])...)
		buf = buf[tsPacketSize:]
	}
	in.pending = append([]byte(nil), buf...)
	return out
}

// Flush releases the access unit still being held, and every unit still waiting
// on its slot. Call it when the stream ends, or the last pictures never reach
// the destination.
func (in *Injector) Flush() []byte {
	in.releaseHeld()
	return in.drain(true)
}

// packet handles one 188-byte packet and returns the bytes that are ready.
func (in *Injector) packet(p []byte) []byte {
	pid := uint16(p[1]&0x1F)<<8 | uint16(p[2])
	pusi := p[1]&0x40 != 0
	switch {
	case pid == pidPAT && pusi:
		if pmt := parsePATPMTPID(p); pmt != 0 {
			in.pmtPID = pmt
		}
		return p
	case in.pmtPID != 0 && pid == in.pmtPID && pusi:
		in.parsePMT(p)
		return p
	case in.videoPID != 0 && pid == in.videoPID:
		if !validAdaptation(p) {
			// The packet cannot be read, so it is dropped rather than passed through
			// out of order with the access unit it belongs to.
			in.stats.Malformed++
			return nil
		}
		return in.video(p, pusi)
	}
	return p
}

// validAdaptation reports whether a packet's adaptation field fits inside the
// packet. Every later read of the field depends on this, so it is checked once,
// where the packet enters the injector.
func validAdaptation(p []byte) bool {
	if (p[3]>>4)&0x2 == 0 {
		return true
	}
	n := int(p[4])
	return n <= tsPacketSize-5 && 5+n <= len(p)
}

// bufferedBytes is the size of every access unit the injector is holding.
func (in *Injector) bufferedBytes() int {
	n := 0
	for _, au := range in.ready {
		n += len(au.pkts) * tsPacketSize
	}
	if in.held != nil {
		n += len(in.held.pkts) * tsPacketSize
	}
	return n
}

// video collects the packets of the H.264 PID into access units. A unit is
// claimed on the schedule the moment its first packet arrives, so the reorder
// window sees every picture before any of them is emitted.
func (in *Injector) video(p []byte, pusi bool) []byte {
	if pusi {
		if in.held != nil {
			in.ready = append(in.ready, in.held)
		}
		au := &accessUnit{pkts: [][]byte{append([]byte(nil), p...)}}
		au.pts, au.hasPTS = pesPTSOf(p)
		var out []byte
		if au.hasPTS && in.sched.Claim(au.pts) {
			// The clock jumped. Units queued from before the jump belong to the old
			// timeline, so they go out unchanged rather than waiting on slots that
			// the new timeline will never finalise.
			out = in.releaseReady()
		}
		in.held = au
		return append(out, in.drain(false)...)
	}
	if in.held == nil {
		// Continuation before any start: it cannot be rebuilt, so it goes out as
		// it came, numbered to follow what was sent before it.
		return in.renumber([][]byte{append([]byte(nil), p...)})
	}
	in.held.pkts = append(in.held.pkts, append([]byte(nil), p...))
	if len(in.held.pkts) > maxHeldPackets {
		// The unit is too long to keep collecting. Everything queued is sent now,
		// in stream order, with the unit's head ahead of the packets that follow.
		// Those packets then pass through unchanged, as continuations with no start
		// do above. Sending the queue here keeps the picture's head from being
		// overtaken by its own tail.
		in.ready = append(in.ready, in.held)
		in.held = nil
		return in.releaseReady()
	}
	return nil
}

// releaseReady sends every queued unit unchanged and empties the queue. It is
// the response to a clock discontinuity, where no caption slot is coming.
func (in *Injector) releaseReady() []byte {
	var out []byte
	for _, au := range in.ready {
		out = append(out, in.renumber(au.pkts)...)
	}
	in.ready = nil
	return out
}

// drain emits complete access units from the front of the queue for as long as
// their slots are final. With all set it emits everything, which is what Flush
// needs. Emission stays in stream order: a unit whose slot is not yet final
// holds back the ones behind it, which keeps the rewritten stream in the order
// the encoder wrote it.
func (in *Injector) drain(all bool) []byte {
	var out []byte
	for len(in.ready) > 0 {
		au := in.ready[0]
		overCap := len(in.ready) > maxReadyUnits || in.bufferedBytes() > maxBufferedBytes
		if !all && !overCap && !au.unchanged && au.hasPTS && !in.sched.Final(au.pts) {
			break
		}
		in.ready = in.ready[1:]
		if overCap && !au.unchanged {
			// The slot never became final, and waiting longer would grow the queue.
			// Send the unit as it is so the queue shrinks back under its cap.
			au.unchanged = true
		}
		out = append(out, in.emitAccessUnit(au)...)
	}
	return out
}

// releaseHeld moves the access unit being collected onto the queue.
func (in *Injector) releaseHeld() {
	if in.held != nil {
		in.ready = append(in.ready, in.held)
		in.held = nil
	}
}

// emitAccessUnit rebuilds one video PES with its caption SEI, or passes the
// packets on renumbered when that is not safe to do.
func (in *Injector) emitAccessUnit(au *accessUnit) []byte {
	pkts := au.pkts
	if au.unchanged {
		return in.renumber(pkts)
	}
	pes, firstAF, ok := reassemble(pkts)
	if !ok || !au.hasPTS {
		// No presentation time means no key for the caption, so the picture is
		// passed on unchanged.
		return in.renumber(pkts)
	}
	hl := 9 + int(pes[8])
	if hl > len(pes) {
		return in.renumber(pkts)
	}
	es := pes[hl:]
	if !hasPictureStart(es) {
		return in.renumber(pkts)
	}
	in.stats.Pictures++
	if midPCR(pkts) {
		in.stats.PCRInMiddle++
		return in.renumber(pkts)
	}

	pair := in.sched.answer(au.pts)
	newES := insertCCSEI(es, pair)
	in.stats.Injected++

	header := append([]byte(nil), pes[:hl]...)
	if orig := int(pes[4])<<8 | int(pes[5]); orig != 0 {
		n := orig + len(newES) - len(es)
		if n > 0xFFFF {
			n = 0 // unbounded, which is legal for video
		}
		header[4], header[5] = byte(n>>8), byte(n)
	}
	payload := append(header, newES...)
	return in.packetize(payload, firstAF)
}

// pesPTSOf reads the presentation time from a packet that starts a PES. ok is
// false when the packet does not start a PES or carries no PTS.
func pesPTSOf(p []byte) (pts int64, ok bool) {
	at := 4
	if (p[3]>>4)&0x2 != 0 {
		at += 1 + int(p[4])
	}
	if (p[3]>>4)&0x1 == 0 || at+14 > len(p) {
		return 0, false
	}
	pes := p[at:]
	if pes[0] != 0 || pes[1] != 0 || pes[2] != 1 || pes[3]&0xF0 != 0xE0 || pes[7]&0x80 == 0 {
		return 0, false
	}
	return readPTS(pes[9:14]), true
}

// reassemble joins the payloads of an access unit's packets and returns the
// adaptation field of the first, so a PCR or random access flag can be kept.
// ok is false when the PES does not start with a start code.
func reassemble(pkts [][]byte) (pes []byte, firstAF []byte, ok bool) {
	for i, p := range pkts {
		afc := (p[3] >> 4) & 0x3
		at := 4
		if afc&0x2 != 0 {
			n := int(p[4])
			if i == 0 && n > 0 {
				firstAF = append([]byte(nil), p[5:5+n]...)
			}
			at += 1 + n
		}
		if afc&0x1 != 0 {
			pes = append(pes, p[at:]...)
		}
	}
	ok = len(pes) >= 9 && pes[0] == 0 && pes[1] == 0 && pes[2] == 1 && pes[3]&0xF0 == 0xE0
	return pes, firstAF, ok
}

// midPCR reports whether a PCR sits on a packet other than the first. The
// rebuild only keeps the first packet's adaptation field, so such a PCR would be
// lost, and the access unit is passed on as it was instead.
func midPCR(pkts [][]byte) bool {
	for i, p := range pkts {
		if i == 0 {
			continue
		}
		afc := (p[3] >> 4) & 0x3
		if afc&0x2 != 0 && p[4] > 0 && p[5]&0x10 != 0 {
			return true
		}
	}
	return false
}

// hasPictureStart reports whether an access unit begins a picture.
func hasPictureStart(es []byte) bool {
	for _, n := range splitAnnexB(es) {
		if startsPicture(n) {
			return true
		}
	}
	return false
}

// packetize splits a PES into TS packets on the video PID. The first packet
// carries firstAF, if any, and the last is filled to 188 bytes with adaptation
// field stuffing.
func (in *Injector) packetize(payload, firstAF []byte) []byte {
	var out []byte
	first := true
	for len(payload) > 0 {
		var af []byte
		if first {
			af = firstAF
		}
		room := 184
		if af != nil {
			room -= 1 + len(af)
		}
		n := min(room, len(payload))
		out = append(out, in.packetOut(first, af, payload[:n], n == room && af == nil)...)
		payload = payload[n:]
		first = false
	}
	return out
}

// renumber sends packets exactly as they arrived, except for the continuity
// counter, which is set to follow the packets sent before. A packet with no
// payload keeps its counter, as the standard requires.
func (in *Injector) renumber(pkts [][]byte) []byte {
	var out []byte
	for _, p := range pkts {
		if p[3]&0x10 != 0 {
			p[3] = p[3]&0xF0 | in.nextCC
			in.nextCC = (in.nextCC + 1) & 0x0F
		} else {
			// No payload: the counter repeats the last one sent, it does not advance.
			p[3] = p[3]&0xF0 | (in.nextCC-1)&0x0F
		}
		out = append(out, p...)
	}
	return out
}

// packetOut builds one TS packet on the video PID. full reports that the
// payload fills the packet with no adaptation field needed.
func (in *Injector) packetOut(pusi bool, af, payload []byte, full bool) []byte {
	p := make([]byte, tsPacketSize)
	p[0] = tsSyncByte
	p[1] = byte(in.videoPID >> 8)
	if pusi {
		p[1] |= 0x40
	}
	p[2] = byte(in.videoPID)
	at := 4
	if full {
		p[3] = 0x10 | in.nextCC
	} else {
		p[3] = 0x30 | in.nextCC
		// The length byte covers everything after it, and the field runs right up
		// to the payload, so the last packet is exact without a separate pad.
		afLen := tsPacketSize - 5 - len(payload)
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
	copy(p[tsPacketSize-len(payload):], payload)
	in.nextCC = (in.nextCC + 1) & 0x0F
	return p
}

// parsePATPMTPID returns the PMT PID of the first program in a PAT packet, or
// zero if the packet is not a usable PAT.
func parsePATPMTPID(p []byte) uint16 {
	s := sectionOf(p)
	if len(s) < 12 || s[0] != 0x00 {
		return 0
	}
	end := 3 + (int(s[1]&0x0F)<<8 | int(s[2])) - 4
	for i := 8; i+4 <= end && i+4 <= len(s); i += 4 {
		prog := uint16(s[i])<<8 | uint16(s[i+1])
		if prog != 0 {
			return uint16(s[i+2]&0x1F)<<8 | uint16(s[i+3])
		}
	}
	return 0
}

// parsePMT finds the first video elementary stream in a program map and records
// its PID. An H.264 stream is rewritten; anything else video is counted and left
// alone.
func (in *Injector) parsePMT(p []byte) {
	s := sectionOf(p)
	if len(s) < 12 || s[0] != 0x02 {
		return
	}
	end := 3 + (int(s[1]&0x0F)<<8 | int(s[2])) - 4
	if end > len(s) {
		return
	}
	info := int(s[10]&0x0F)<<8 | int(s[11])
	for i := 12 + info; i+5 <= end; {
		typ := s[i]
		pid := uint16(s[i+1]&0x1F)<<8 | uint16(s[i+2])
		esInfo := int(s[i+3]&0x0F)<<8 | int(s[i+4])
		switch typ {
		case streamTypeH264:
			if in.videoPID == 0 {
				in.videoPID = pid
			}
		case streamTypeHEVC:
			if in.hevcPID == 0 {
				in.hevcPID = pid
				in.stats.Unsupported++
			}
		}
		i += 5 + esInfo
	}
}

// sectionOf returns the PSI section of a packet that starts a section, past any
// adaptation field and the pointer field.
func sectionOf(p []byte) []byte {
	at := 4
	if (p[3]>>4)&0x2 != 0 {
		at += 1 + int(p[4])
	}
	if at >= len(p) {
		return nil
	}
	at += 1 + int(p[at])
	if at >= len(p) {
		return nil
	}
	return p[at:]
}

// readPTS decodes the 33-bit presentation timestamp in a five-byte PES field.
func readPTS(b []byte) int64 {
	return int64(b[0]>>1&0x07)<<30 | int64(b[1])<<22 | int64(b[2]>>1)<<15 | int64(b[3])<<7 | int64(b[4]>>1)
}
