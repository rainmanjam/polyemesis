package delayline

// DUMP: dropping the newest part of the buffer while the destination stays on
// the air.
//
// A profanity delay's dump button deletes what was said most recently, so it
// never airs, and the delay gets shorter by that much. Here the buffer holds
// MPEG-TS, and the video is copied, never decoded. So a dump cannot cut just
// anywhere. It has two ends, and both have to be pictures a decoder can start
// from:
//
//   - THE CUT. The newest data is removed from the buffer back to the start of
//     an IDR picture (K1). What stays ends with a complete GOP.
//   - THE RESUME. Incoming data is discarded until the next IDR picture (K2)
//     arrives. From there it is buffered again.
//
// K2 takes K1's place. It is released when K1 would have been, and its
// timestamps are rewritten to K1's. Every later packet on every PID gets the
// same offset. So the platform sees one unbroken timeline: the GOP before the
// cut, then the GOP after the resume, with no gap in PTS, DTS or PCR and no
// jump in the release clock. The viewers skip forward by the time between K1
// and K2. The delay is that much shorter from then on, and stays shorter: the
// video is copied, so there is no way to stretch it back.
//
// THE REWRITE IS MANDATORY. Every relay consumer reads with
// -dts_delta_threshold set to a year (ffmpeg.RelayInputArgs). That keeps an
// outage's forward jump on the timeline, which is right for an outage. An
// unrewritten dump would be the same jump: the platform's timestamps would run
// the dumped seconds ahead of the clock it is fed at.
//
// What else is kept continuous:
//
//   - Continuity counters, per PID, so no reader logs a discontinuity.
//   - Audio. A PES that would overlap the last one kept on its PID is dropped,
//     so no PID's timestamps step backwards at the splice.
//   - Video presentation order. A picture after K2 that is shown before it
//     (an HEVC RADL picture) is dropped for the same reason.
//
// ONLY IDR PICTURES ARE CUT POINTS. The random_access_indicator is not proof:
// with an open GOP (an H.264 I picture with a recovery point, an HEVC CRA) the
// pictures after it can reference pictures before it, which a dump has
// discarded. So each video PES is classified by its first VCL NAL unit:
// H.264 type 5, HEVC type 19 or 20. A stream with no such pictures, or a codec
// this does not know, has no cut points, and a dump is refused rather than
// guessed.

import (
	"errors"
	"os"
	"time"
)

const (
	tsPacketSize = 188
	tsSyncByte   = 0x47
	tsNullPID    = 0x1FFF
	pidPAT       = 0x0000

	// ptsModulus is the wrap of the 33-bit PTS, DTS and PCR base.
	ptsModulus = int64(1) << 33

	// chunkPackets is how many TS packets go into one buffered record: seven,
	// the relay's own 1316-byte datagram. A record also starts at every video
	// PES, so a cut point is always a record boundary.
	chunkPackets = 7

	// nalScanLimit is how many bytes of one video PES are searched for its
	// first picture. The parameter sets and SEI in front of an IDR slice are
	// a few hundred bytes; x264's first-frame SEI is about a kilobyte.
	nalScanLimit = 4096

	// heldLimit bounds the packets held while a resume candidate is
	// classified: about 385 KB, more than one keyframe's first 4 KB needs.
	heldLimit = 2048

	// maxMarks bounds the cut points remembered. One per GOP: 1024 is over
	// half an hour of 2-second GOPs, twice the longest delay.
	maxMarks = 1024

	// defaultGOP is assumed until two cut points have been seen, and
	// maxGOPAllowance caps what a sparse stream can claim.
	defaultGOP      = 2 * time.Second
	maxGOPAllowance = 10 * time.Second
)

// DumpMargin is the least delay a dump leaves. A dump never takes the buffer
// below it plus one GOP: the GOP is the wait for the resume keyframe, during
// which nothing new is buffered and the delay keeps shrinking.
const DumpMargin = 2 * time.Second

// Dump refusals.
var (
	// ErrNoCutPoint: no IDR picture far enough back to cut at.
	ErrNoCutPoint = errors.New("delayline: no keyframe in the buffer to cut at that leaves the safety margin")
	// ErrNotSpliceable: the stream carries no picture a dump can cut at -- not
	// MPEG-TS this understands, no H.264 or HEVC video, or no IDR pictures.
	ErrNotSpliceable = errors.New("delayline: this stream has no keyframes a dump can cut at")
	// ErrDumpPending: a dump is still waiting for its resume keyframe.
	ErrDumpPending = errors.New("delayline: a dump is still waiting for the next keyframe")
)

type videoCodec uint8

const (
	codecNone videoCodec = iota
	codecH264
	codecHEVC
)

// pidOut is what the OUTPUT last carried on one PID: the state a splice
// continues from.
type pidOut struct {
	cc      uint8
	ccSeen  bool
	pts     int64
	ptsSeen bool
}

// pidRewrite is how one PID's packets are changed after a splice.
type pidRewrite struct {
	ccDelta uint8
	ccSet   bool
	// awaitStart: nothing on this PID is kept until a packet that starts a
	// PES or a section. The resync discarded whatever began before the
	// resume, and a continuation of it would be read as part of the PES the
	// cut left open -- with a continuity counter that hides the join.
	awaitStart bool
	// gated: PESs on this PID are dropped while their PTS is <= floor.
	gated    bool
	floor    int64
	dropping bool
	// sticky keeps a gate past the first PES that clears it, until the PTS
	// is well past the floor. Video: the resume picture clears the floor,
	// and a leading picture after it (an HEVC RADL, shown before it) must
	// still be dropped.
	sticky bool
}

// stickyClear is how far past its floor a sticky gate's PTS must be before
// the gate lifts: ten seconds, far beyond any reorder depth, and far short of
// the 33-bit wrap that would make the comparison lie.
const stickyClear = 10 * 90000

// mark is one cut point: the start of a buffered IDR picture's PES.
type mark struct {
	seg *segment
	off int   // byte offset of its record in seg
	at  int64 // its arrival time, buffer clock
	// prevAt is the arrival time of the record before it.
	prevAt int64
	// pushedPackets and pushedBytes are seg's counts before this record.
	pushedPackets int64
	pushedBytes   int64
	// pts is the picture's output PTS.
	pts int64
	// out is the output state just before it: what a splice resumes from.
	out map[uint16]pidOut

	positioned bool
	key        bool
	classified bool
	added      bool
}

// classifier finds the first picture of a video PES and says if it is IDR.
type classifier struct {
	codec videoCodec
	buf   []byte
	done  bool
	key   bool
}

func (c *classifier) reset(codec videoCodec) {
	c.codec, c.buf, c.done, c.key = codec, c.buf[:0], false, false
}

// feed adds elementary-stream bytes and reports whether the PES is classified.
func (c *classifier) feed(es []byte) bool {
	if c.done {
		return true
	}
	from := len(c.buf) - 3
	if from < 0 {
		from = 0
	}
	room := nalScanLimit - len(c.buf)
	if len(es) > room {
		es = es[:room]
	}
	c.buf = append(c.buf, es...)
	for i := from; i+3 < len(c.buf); i++ {
		if c.buf[i] != 0 || c.buf[i+1] != 0 || c.buf[i+2] != 1 {
			continue
		}
		h := c.buf[i+3]
		switch c.codec {
		case codecH264:
			if t := h & 0x1F; t >= 1 && t <= 5 {
				c.done, c.key = true, t == 5
				return true
			}
		case codecHEVC:
			if t := (h >> 1) & 0x3F; t <= 31 {
				c.done, c.key = true, t == 19 || t == 20
				return true
			}
		default:
			c.done = true
			return true
		}
	}
	if len(c.buf) >= nalScanLimit {
		c.done = true
	}
	return c.done
}

// tsState is the buffer's running view of the stream, and the splice in
// progress. Guarded by Buffer.mu.
type tsState struct {
	pmtPID   uint16
	havePMT  bool
	videoPID uint16
	codec    videoCodec

	out map[uint16]pidOut
	rw  map[uint16]*pidRewrite
	// offset is subtracted from every PTS, DTS and PCR base, mod 2^33.
	offset int64

	cand *mark
	cls  classifier

	// resync is set by a dump until its resume keyframe arrives.
	resync  bool
	margin  time.Duration
	splice  *mark
	ticket  *DumpTicket
	held    []byte
	holding bool
	heldPTS int64
}

func (ts *tsState) outFor(pid uint16) pidOut { return ts.out[pid] }

func (ts *tsState) rwFor(pid uint16) *pidRewrite {
	r := ts.rw[pid]
	if r == nil {
		r = &pidRewrite{}
		ts.rw[pid] = r
	}
	return r
}

// ---- packet helpers --------------------------------------------------------

func tsAligned(d []byte) bool {
	if len(d) == 0 || len(d)%tsPacketSize != 0 {
		return false
	}
	for off := 0; off < len(d); off += tsPacketSize {
		if d[off] != tsSyncByte {
			return false
		}
	}
	return true
}

func pktPID(p []byte) uint16 { return uint16(p[1]&0x1F)<<8 | uint16(p[2]) }
func pktPUSI(p []byte) bool  { return p[1]&0x40 != 0 }
func pktHasPayload(p []byte) bool {
	return p[3]&0x10 != 0
}
func pktHasAF(p []byte) bool { return p[3]&0x20 != 0 }

// payloadStart is where the payload begins, or tsPacketSize when there is none.
func payloadStart(p []byte) int {
	if !pktHasPayload(p) {
		return tsPacketSize
	}
	s := 4
	if pktHasAF(p) {
		s += 1 + int(p[4])
	}
	if s > tsPacketSize {
		return tsPacketSize
	}
	return s
}

// pesFields locates a PES header at the start of a packet's payload: where the
// PTS and DTS are (-1 when absent) and where the elementary stream begins.
func pesFields(p []byte) (ptsAt, dtsAt, esAt int, ok bool) {
	ps := payloadStart(p)
	if ps+9 > tsPacketSize || p[ps] != 0 || p[ps+1] != 0 || p[ps+2] != 1 {
		return -1, -1, 0, false
	}
	switch p[ps+3] {
	case 0xBC, 0xBE, 0xBF, 0xF0, 0xF1, 0xFF, 0xF2, 0xF8:
		// Stream IDs with no optional header.
		return -1, -1, ps + 6, true
	}
	flags := p[ps+7]
	hdrEnd := ps + 9 + int(p[ps+8])
	// A header that runs past this packet still has its PTS and DTS here
	// when they fit: they come first. esAt is then the end of the packet.
	esAt = min(hdrEnd, tsPacketSize)
	ptsAt, dtsAt = -1, -1
	if flags&0x80 != 0 && ps+14 <= hdrEnd && ps+14 <= tsPacketSize {
		ptsAt = ps + 9
	}
	if flags&0xC0 == 0xC0 && ps+19 <= hdrEnd && ps+19 <= tsPacketSize {
		dtsAt = ps + 14
	}
	return ptsAt, dtsAt, esAt, true
}

func readTS(b []byte) int64 {
	return int64(b[0]>>1&0x07)<<30 | int64(b[1])<<22 | int64(b[2]>>1)<<15 |
		int64(b[3])<<7 | int64(b[4]>>1)
}

func writeTS(b []byte, v int64) {
	b[0] = b[0]&0xF1 | byte(v>>29)&0x0E
	b[1] = byte(v >> 22)
	b[2] = byte(v>>14)&0xFE | 0x01
	b[3] = byte(v >> 7)
	b[4] = byte(v<<1)&0xFE | 0x01
}

// pcrAt is where a packet's PCR is, or -1.
func pcrAt(p []byte) int {
	if !pktHasAF(p) || p[4] < 7 || p[5]&0x10 == 0 {
		return -1
	}
	return 6
}

func readPCRBase(b []byte) int64 {
	return int64(b[0])<<25 | int64(b[1])<<17 | int64(b[2])<<9 | int64(b[3])<<1 | int64(b[4]>>7)
}

func writePCRBase(b []byte, v int64) {
	b[0] = byte(v >> 25)
	b[1] = byte(v >> 17)
	b[2] = byte(v >> 9)
	b[3] = byte(v >> 1)
	b[4] = b[4]&0x7F | byte(v&1)<<7
}

func wrap(v int64) int64 {
	v %= ptsModulus
	if v < 0 {
		v += ptsModulus
	}
	return v
}

// ptsAfter reports whether a is later than b on the 33-bit circle.
func ptsAfter(a, b int64) bool {
	d := wrap(a - b)
	return d != 0 && d < ptsModulus/2
}

// sectionOf is the PSI section a packet starts, or nil.
func sectionOf(p []byte) []byte {
	if !pktPUSI(p) {
		return nil
	}
	s := payloadStart(p)
	if s >= tsPacketSize {
		return nil
	}
	s += 1 + int(p[s])
	if s >= tsPacketSize {
		return nil
	}
	return p[s:]
}

func parsePAT(sec []byte) (uint16, bool) {
	if len(sec) < 12 || sec[0] != 0x00 {
		return 0, false
	}
	end := 3 + (int(sec[1]&0x0F)<<8 | int(sec[2]))
	if end > len(sec) {
		end = len(sec)
	}
	for off := 8; off+4 <= end-4; off += 4 {
		if program := uint16(sec[off])<<8 | uint16(sec[off+1]); program == 0 {
			continue
		}
		return uint16(sec[off+2]&0x1F)<<8 | uint16(sec[off+3]), true
	}
	return 0, false
}

// parsePMT returns the first video elementary stream and its codec.
func parsePMT(sec []byte) (uint16, videoCodec, bool) {
	if len(sec) < 16 || sec[0] != 0x02 {
		return 0, codecNone, false
	}
	end := 3 + (int(sec[1]&0x0F)<<8 | int(sec[2]))
	if end > len(sec) {
		end = len(sec)
	}
	off := 12 + (int(sec[10]&0x0F)<<8 | int(sec[11]))
	for off+5 <= end-4 {
		st := sec[off]
		pid := uint16(sec[off+1]&0x1F)<<8 | uint16(sec[off+2])
		switch st {
		case 0x1B:
			return pid, codecH264, true
		case 0x24:
			return pid, codecHEVC, true
		case 0x01, 0x02, 0x10, 0x33, 0x42, 0xD1, 0xEA:
			// Video, but not a codec a dump can classify.
			return pid, codecNone, true
		}
		off += 5 + (int(sec[off+3]&0x0F)<<8 | int(sec[off+4]))
	}
	return 0, codecNone, false
}

// observePSI follows the PAT and PMT.
func (ts *tsState) observePSI(p []byte) {
	pid := pktPID(p)
	switch {
	case pid == pidPAT:
		if sec := sectionOf(p); sec != nil {
			if pmt, ok := parsePAT(sec); ok {
				ts.pmtPID, ts.havePMT = pmt, true
			}
		}
	case ts.havePMT && pid == ts.pmtPID:
		if sec := sectionOf(p); sec != nil {
			if v, codec, ok := parsePMT(sec); ok {
				ts.videoPID, ts.codec = v, codec
			}
		}
	}
}

func (ts *tsState) isVideo(pid uint16) bool {
	return ts.codec != codecNone && pid == ts.videoPID
}

func (ts *tsState) snapshot() map[uint16]pidOut {
	m := make(map[uint16]pidOut, len(ts.out))
	for k, v := range ts.out {
		m[k] = v
	}
	return m
}

// rewrite applies the splice offset and the per-PID continuity and gating to
// one packet, in place. keep is false for a packet the splice drops.
func (ts *tsState) rewrite(p []byte) (keep bool, pts int64, hasPTS bool) {
	pid := pktPID(p)
	if pid == tsNullPID {
		return true, 0, false
	}
	var ptsAt, dtsAt int
	pes := false
	if pktPUSI(p) {
		ptsAt, dtsAt, _, pes = pesFields(p)
	}
	if ts.offset != 0 {
		if pes && ptsAt >= 0 {
			writeTS(p[ptsAt:], wrap(readTS(p[ptsAt:])-ts.offset))
		}
		if pes && dtsAt >= 0 {
			writeTS(p[dtsAt:], wrap(readTS(p[dtsAt:])-ts.offset))
		}
		if at := pcrAt(p); at >= 0 {
			writePCRBase(p[at:], wrap(readPCRBase(p[at:])-ts.offset))
		}
	}
	if pes && ptsAt >= 0 {
		pts, hasPTS = readTS(p[ptsAt:]), true
	}
	r := ts.rw[pid]
	// Payload only: an adaptation-only packet (a PCR on a PID of its own)
	// carries no part of any PES and must keep flowing.
	if r != nil && r.awaitStart && pktHasPayload(p) {
		if !pktPUSI(p) {
			return false, pts, hasPTS
		}
		r.awaitStart = false
	}
	if r != nil && r.gated {
		if pes {
			switch {
			case hasPTS && !ptsAfter(pts, r.floor):
				r.dropping = true
			case r.sticky && hasPTS && !ptsAfter(pts, wrap(r.floor+stickyClear)):
				r.dropping = false
			default:
				r.gated, r.dropping = false, false
			}
		}
		if r.dropping {
			return false, pts, hasPTS
		}
	}
	cc := p[3] & 0x0F
	if r != nil {
		if !r.ccSet {
			r.ccSet = true
			if prev, ok := ts.out[pid]; ok && prev.ccSeen {
				want := prev.cc
				if pktHasPayload(p) {
					want++
				}
				r.ccDelta = (want - cc) & 0x0F
			}
		}
		cc = (cc + r.ccDelta) & 0x0F
		p[3] = p[3]&0xF0 | cc
	}
	o := ts.out[pid]
	o.cc, o.ccSeen = cc, true
	if hasPTS {
		o.pts, o.ptsSeen = pts, true
	}
	ts.out[pid] = o
	return true, pts, hasPTS
}

// ---- the buffer side ---------------------------------------------------------

// DumpTicket follows one dump from the cut to the resume.
type DumpTicket struct {
	b    *Buffer
	done chan struct{}
	// Set under Buffer.mu at the resume, read after done closes.
	result DumpResult
}

// Done closes when the resume keyframe has arrived and the dump is complete.
func (t *DumpTicket) Done() <-chan struct{} { return t.done }

// DumpResult is what a dump did.
type DumpResult struct {
	// Requested is how much was asked for; 0 meant "everything since the last
	// dump point".
	Requested time.Duration `json:"requested"`
	// Dumped is the programme time dropped: from the cut keyframe to the resume
	// keyframe. Known once Complete.
	Dumped time.Duration `json:"dumped"`
	// Clamped is true when less than Requested could be dropped without going
	// under the margin.
	Clamped bool `json:"clamped"`
	// Before and After are the delay the audience has, before and after.
	Before time.Duration `json:"before"`
	After  time.Duration `json:"after"`
	// Complete is false while the resume keyframe is still awaited.
	Complete bool `json:"complete"`
}

// atLocked is the buffer-clock arrival time of data pushed now: real time less
// everything dumped, never earlier than the last record.
func (b *Buffer) atLocked(now time.Time) int64 {
	at := int64(now.Sub(b.epoch) - b.shift)
	if at < b.lastAt {
		at = b.lastAt
	}
	return at
}

// pushTSLocked is Push for a datagram of aligned TS packets.
func (b *Buffer) pushTSLocked(now time.Time, pkt []byte) {
	work := append(b.scratch[:0], pkt...)
	b.scratch = work[:0]
	if b.ts.resync {
		rest, resumed := b.resyncLocked(now, work)
		if !resumed {
			return
		}
		held := b.ts.held
		b.ts.held = nil
		b.normalLocked(b.atLocked(now), held)
		work = rest
	}
	if len(work) > 0 {
		b.normalLocked(b.atLocked(now), work)
	}
}

// normalLocked buffers a run of aligned packets, rewritten, split into records
// at every video PES.
func (b *Buffer) normalLocked(at int64, data []byte) {
	ts := &b.ts
	runStart := 0
	flush := func(end int) {
		for runStart < end {
			n := end - runStart
			if n > chunkPackets*tsPacketSize {
				n = chunkPackets * tsPacketSize
			}
			seg, off, before, bytesBefore := b.appendLocked(at, data[runStart:runStart+n])
			if c := ts.cand; c != nil && !c.positioned {
				c.seg, c.off, c.at, c.positioned = seg, off, at, true
				c.pushedPackets, c.pushedBytes = before, bytesBefore
				b.maybeAddMarkLocked()
			}
			runStart += n
		}
	}
	for off := 0; off < len(data); off += tsPacketSize {
		p := data[off : off+tsPacketSize]
		ts.observePSI(p)
		pid := pktPID(p)
		video := ts.isVideo(pid)
		startsPES := video && pktPUSI(p)
		var snap map[uint16]pidOut
		if startsPES {
			snap = ts.snapshot()
		}
		keep, pts, hasPTS := ts.rewrite(p)
		if !keep {
			flush(off)
			runStart = off + tsPacketSize
			continue
		}
		if startsPES {
			flush(off)
			// The previous picture is over; this one begins a record.
			ts.cand = &mark{pts: pts, out: snap, prevAt: b.lastAt}
			if !hasPTS {
				// No PTS, no splice: classify it out.
				ts.cand.classified = true
			}
			ts.cls.reset(ts.codec)
		}
		if video && ts.cand != nil && !ts.cand.classified {
			ps := payloadStart(p)
			if startsPES {
				if _, _, es, ok := pesFields(p); ok {
					ps = es
				} else {
					ps = tsPacketSize
				}
			}
			if ps < tsPacketSize && ts.cls.feed(p[ps:]) {
				ts.cand.classified, ts.cand.key = true, ts.cls.key
				b.maybeAddMarkLocked()
			}
		}
	}
	flush(len(data))
}

// maybeAddMarkLocked records the open candidate as a cut point once it is
// both placed in the buffer and known to be an IDR picture.
func (b *Buffer) maybeAddMarkLocked() {
	c := b.ts.cand
	if c == nil || c.added || !c.positioned || !c.classified || !c.key {
		return
	}
	c.added = true
	// Forget the cut points already released.
	if len(b.queue) > 0 {
		head := b.queue[0].first
		i := 0
		for i < len(b.marks) && b.marks[i].at < head {
			i++
		}
		b.marks = b.marks[i:]
	}
	if len(b.marks) >= maxMarks {
		b.marks = b.marks[1:]
	}
	b.marks = append(b.marks, c)
}

// gopLocked is the longest gap between recent cut points: the wait a resume
// may need.
func (b *Buffer) gopLocked() time.Duration {
	if len(b.marks) < 2 {
		return defaultGOP
	}
	var gop time.Duration
	from := len(b.marks) - 9
	if from < 0 {
		from = 0
	}
	for i := from + 1; i < len(b.marks); i++ {
		if d := time.Duration(b.marks[i].at - b.marks[i-1].at); d > gop {
			gop = d
		}
	}
	if gop > maxGOPAllowance {
		gop = maxGOPAllowance
	}
	if gop <= 0 {
		gop = defaultGOP
	}
	return gop
}

// markLiveLocked reports whether a cut point is still in the buffer, unreleased.
func (b *Buffer) markLiveLocked(m *mark) bool {
	for i, s := range b.queue {
		if s != m.seg {
			continue
		}
		return i > 0 || m.off >= s.off
	}
	return false
}

// Dump cuts the newest want of the buffer back to a keyframe, and drops what
// arrives until the next keyframe. want 0 means everything buffered since the
// last dump point. Never leaves less than margin plus one GOP of delay: when
// want is more than that allows, less is dropped and Clamped says so.
//
// The cut happens now; the resume when the next IDR picture arrives. The
// ticket's Done closes then.
func (b *Buffer) Dump(now time.Time, want, margin time.Duration) (*DumpTicket, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ts.resync {
		return nil, ErrDumpPending
	}
	if b.ts.codec == codecNone || len(b.marks) == 0 || len(b.queue) == 0 {
		return nil, ErrNotSpliceable
	}
	vnow := int64(now.Sub(b.epoch) - b.shift)
	head := b.queue[0].first
	gop := b.gopLocked()
	lo := head + int64(margin+gop)
	floor := lo
	if want <= 0 && b.lastDumpAt > floor {
		floor = b.lastDumpAt
	}
	var pick *mark
	clamped := false
	if want > 0 {
		limit := vnow - int64(want)
		// The latest cut point at or before now-want, inside the margin.
		for i := len(b.marks) - 1; i >= 0; i-- {
			m := b.marks[i]
			if m.at > limit || m.at < lo || !b.markLiveLocked(m) {
				continue
			}
			pick = m
			break
		}
		if pick == nil {
			// Not that much to spare: the most there is.
			for _, m := range b.marks {
				if m.at >= lo && b.markLiveLocked(m) {
					pick, clamped = m, true
					break
				}
			}
		}
	} else {
		for _, m := range b.marks {
			if m.at >= floor && b.markLiveLocked(m) {
				pick = m
				break
			}
		}
	}
	if pick == nil {
		return nil, ErrNoCutPoint
	}
	before := b.delay - b.shift
	if err := b.truncateLocked(pick); err != nil {
		return nil, err
	}
	t := &DumpTicket{b: b, done: make(chan struct{})}
	// Provisional until the resume: what has been cut so far. The wait for
	// the resume keyframe adds to it.
	cut := time.Duration(vnow - pick.at)
	t.result = DumpResult{Requested: want, Clamped: clamped, Before: before, Dumped: cut, After: before - cut}
	ts := &b.ts
	ts.resync, ts.splice, ts.ticket, ts.margin = true, pick, t, margin
	ts.holding, ts.held = false, nil
	ts.cand = nil
	ts.out = make(map[uint16]pidOut, len(pick.out))
	for k, v := range pick.out {
		ts.out[k] = v
	}
	return t, nil
}

// truncateLocked removes everything from m to the end of the buffer.
func (b *Buffer) truncateLocked(m *mark) error {
	idx := -1
	for i, s := range b.queue {
		if s == m.seg {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrNoCutPoint
	}
	s := m.seg
	// The one step that can fail goes first, so a failure changes nothing.
	if s.data == nil && s.path != "" {
		if err := truncateFile(s.path, int64(m.off)); err != nil {
			return err
		}
	}
	for _, later := range b.queue[idx+1:] {
		b.removeSegmentLocked(later)
	}
	removedP := s.pushedPackets - m.pushedPackets
	removedB := s.pushedBytes - m.pushedBytes
	s.packets -= removedP
	s.bytes -= removedB
	s.pushedPackets, s.pushedBytes = m.pushedPackets, m.pushedBytes
	b.stats.Packets -= removedP
	b.stats.Bytes -= removedB
	if s.data != nil {
		s.data = s.data[:m.off]
		s.size = int64(m.off)
	} else {
		b.stats.DiskBytes -= s.size - int64(m.off)
		s.size = int64(m.off)
	}
	s.last = m.prevAt
	keep := idx + 1
	if m.off == 0 || (s.data != nil && s.off >= len(s.data)) {
		// Nothing of this segment is left to release.
		b.removeSegmentLocked(s)
		keep = idx
	}
	b.queue = b.queue[:keep]
	// A segment still in RAM stays the tail and keeps filling, so the RAM
	// bound counts it once; a fresh tail beside it would hold a second
	// segment outside the budget.
	b.tail = nil
	if keep == idx+1 && s.data != nil {
		b.tail = s
	}
	i := 0
	for i < len(b.marks) && b.marks[i].at < m.at {
		i++
	}
	b.marks = b.marks[:i]
	return nil
}

// removeSegmentLocked releases a segment's RAM, file and counts. It does not
// touch the queue.
func (b *Buffer) removeSegmentLocked(s *segment) {
	if s.path != "" {
		_ = removeFile(s.path)
		b.stats.DiskBytes -= s.size
		s.path = ""
	}
	if s.data != nil {
		b.stats.MemBytes -= int64(cap(s.data))
		s.data = nil
	}
	b.stats.Packets -= s.packets
	b.stats.Bytes -= s.bytes
	s.packets, s.bytes = 0, 0
}

// resyncLocked discards packets until a video PES that starts with an IDR
// picture. That PES and everything after it in the datagram are kept: held
// holds the PES's packets so far, rest the datagram's unread remainder.
func (b *Buffer) resyncLocked(now time.Time, work []byte) (rest []byte, resumed bool) {
	ts := &b.ts
	for off := 0; off < len(work); off += tsPacketSize {
		p := work[off : off+tsPacketSize]
		ts.observePSI(p)
		pid := pktPID(p)
		video := ts.isVideo(pid)
		if video && pktPUSI(p) {
			ptsAt, _, es, ok := pesFields(p)
			ts.holding, ts.held = false, ts.held[:0]
			if !ok || ptsAt < 0 {
				continue
			}
			ts.holding, ts.heldPTS = true, readTS(p[ptsAt:])
			ts.cls.reset(ts.codec)
			ts.held = append(ts.held, p...)
			if es < tsPacketSize && ts.cls.feed(p[es:]) {
				if b.decideResumeLocked(now) {
					return work[off+tsPacketSize:], true
				}
			}
			continue
		}
		if !ts.holding {
			continue
		}
		ts.held = append(ts.held, p...)
		if len(ts.held) > heldLimit*tsPacketSize {
			ts.holding, ts.held = false, ts.held[:0]
			continue
		}
		if video {
			if ps := payloadStart(p); ps < tsPacketSize && ts.cls.feed(p[ps:]) {
				if b.decideResumeLocked(now) {
					return work[off+tsPacketSize:], true
				}
			}
		}
	}
	return nil, false
}

// decideResumeLocked acts on a classified resume candidate: an IDR resumes the
// stream, anything else is discarded.
func (b *Buffer) decideResumeLocked(now time.Time) bool {
	ts := &b.ts
	if !ts.cls.key {
		ts.holding, ts.held = false, ts.held[:0]
		return false
	}
	prevOffset := ts.offset
	m := ts.splice
	// The resume picture takes the cut picture's place on both clocks.
	ts.offset = wrap(ts.heldPTS - m.pts)
	before := b.shift
	b.shift = now.Sub(b.epoch) - time.Duration(m.at)
	// THE MARGIN HOLDS HOWEVER LATE THE RESUME IS. Dump allowed for one GOP
	// of waiting; an input that stalls mid-dump (an encoder reconnecting)
	// makes the wait as long as the stall. Taking all of it off the delay
	// would leave less than the margin -- or none, releasing data as it
	// arrives. So the delay gives up at most down to the margin, and the
	// rest of the wait is a gap on the wire, which the stall was anyway.
	if limit := b.delay - ts.margin; b.shift > limit {
		b.shift = limit
	}
	if b.shift < before {
		b.shift = before
	}
	b.lastAt = m.prevAt
	if m.at > b.lastAt {
		b.lastAt = m.at
	}
	b.lastDumpAt = m.at
	ts.rw = map[uint16]*pidRewrite{}
	for pid, o := range ts.out {
		r := ts.rwFor(pid)
		r.awaitStart = true
		if o.ptsSeen {
			r.gated, r.floor = true, o.pts
		}
	}
	// Video: drop only pictures SHOWN before the resume picture; the resume
	// picture itself lands exactly on the cut picture's PTS.
	if ts.codec != codecNone {
		r := ts.rwFor(ts.videoPID)
		r.awaitStart = true
		r.gated, r.floor, r.sticky = true, wrap(m.pts-1), true
	}
	ts.resync, ts.holding, ts.splice = false, false, nil
	if t := ts.ticket; t != nil {
		// The programme skipped: the cut picture to the resume picture. It is
		// what the delay lost too, unless a stalled resume hit the margin.
		t.result.Dumped = time.Duration(wrap(ts.offset-prevOffset)) * time.Second / 90000
		if t.result.Dumped < 0 || t.result.Dumped > MaxDelay {
			t.result.Dumped = b.shift - before
		}
		t.result.After = b.delay - b.shift
		t.result.Complete = true
		close(t.done)
		b.resumed.Store(t)
		ts.ticket = nil
	}
	return true
}

// takeResumed hands over a dump that completed since the last call, once.
// Lock-free: the line asks after every datagram.
func (b *Buffer) takeResumed() *DumpTicket {
	return b.resumed.Swap(nil)
}

// Result is the dump's outcome: final once Done has closed.
func (t *DumpTicket) Result() DumpResult {
	t.b.mu.Lock()
	defer t.b.mu.Unlock()
	return t.result
}

func truncateFile(path string, n int64) error { return os.Truncate(path, n) }
func removeFile(path string) error            { return os.Remove(path) }
