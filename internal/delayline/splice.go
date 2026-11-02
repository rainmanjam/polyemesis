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
//     an IDR picture (K1). What stays ends with a complete GOP, and with every
//     audio PES complete: a picture where an audio PES is still open is not a
//     cut point.
//   - THE RESUME. Incoming data is discarded until the next IDR picture (K2)
//     arrives. From there it is buffered again. A random-access picture that is
//     not IDR is accepted only if no IDR has come after several GOPs (a
//     failover to an open-GOP source); its leading pictures are dropped.
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
//   - Continuity counters: after a splice every PID is renumbered, so the
//     packets a splice drops or rebuilds leave no gap.
//   - Audio, to the frame. The first audio PES after the resume is trimmed at
//     an ADTS (AAC) or Opus frame boundary so that it starts where the last
//     kept audio ends. A PES starting after the resume replaces the end of
//     the kept audio, which is how muxers group several frames per PES; kept
//     whole, it overlapped by up to a PES and made the destination's remux
//     shift its whole input.
//   - Video presentation order. A picture after K2 that is shown before it
//     (an HEVC RADL picture) is dropped.
//   - PES headers split across two packets still have their PTS and DTS
//     rewritten: the first packet is held for the second.
//   - A PID that appears between the cut and the resume waits for its first
//     PES start, and the latest PAT and PMT are sent again at the resume.
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
	"slices"
	"time"
)

const (
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

	// defaultGOP is assumed until two cut points have been seen.
	defaultGOP = 2 * time.Second

	// gateLimit is the PES starts the video gate looks at before it lifts,
	// whatever their PTS: two seconds at 30 fps, far more than any run of
	// leading pictures. A source clock that steps backwards just after a
	// resume would otherwise have every picture dropped.
	gateLimit = 64
	// stickyClear is how far past its floor the video gate's PTS must be
	// before the gate lifts on PTS alone.
	stickyClear = 10 * 90000

	// maxSnapPIDs bounds what one cut point remembers about the output: the
	// PIDs of the current PAT and PMT, at most this many.
	maxSnapPIDs = 32

	// trimHoldLimit bounds one audio PES held to be trimmed.
	trimHoldLimit = 64 << 10

	// A dump with no IDR after recoveryGOPs GOPs (and at least
	// minRecoveryWait) resumes at a random-access picture instead.
	recoveryGOPs    = 3
	minRecoveryWait = 6 * time.Second
)

// DumpMargin is the least a dump leaves: the delay never drops below it plus
// one GOP. A dump's cut leaves room for one more GOP still, which is the wait
// for the resume keyframe, so a resume on time never comes near the floor.
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

// pidOut is what the OUTPUT last carried on one PID: the state a splice
// continues from.
type pidOut struct {
	cc      uint8
	ccSeen  bool
	pts     int64
	ptsSeen bool
	// remaining is the bytes a bounded PES on this PID still has to come.
	remaining int
	// openUnbounded: the PES on this PID has no length, so it is open until
	// the next start on the PID.
	openUnbounded bool
	// dts is the last DTS sent (PTS when the PES has none), and dtsStep the
	// last step between two: what a resume's DTS must come after.
	dts     int64
	dtsSeen bool
	dtsStep int64
	// maxPTS is the latest PTS sent on the PID: with reordered video, the
	// last picture SHOWN, which the next one must come after.
	maxPTS  int64
	maxSeen bool
}

// pidSnap is one PID's output state at a cut point.
type pidSnap struct {
	pid uint16
	pidOut
	// end is when the last PES started before the cut point ends: the start
	// of the next PES on the PID, filled in when that arrives.
	end      int64
	endKnown bool
}

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
	out []pidSnap
	// safe: no audio PES was open here, so cutting here splits none.
	safe bool
	// waitPIDs are audio PIDs whose unbounded PES was open at the cut point.
	// It becomes a cut point only once each has started its next PES before
	// any continuation of the open one arrives -- then that PES ended here.
	waitPIDs []uint16

	// shift is the buffer's shift when the cut point was stamped: its wall
	// arrival is epoch + at + shift, whatever has been dumped since.
	shift time.Duration

	positioned bool
	key        bool
	classified bool
	added      bool
}

// classifier finds the first picture of a video PES.
type classifier struct {
	codec videoCodec
	buf   []byte
	done  bool
	// key: an IDR picture. irap: any HEVC random-access picture (CRA, BLA
	// and IDR), which a recovering resume accepts.
	key  bool
	irap bool
}

func (c *classifier) reset(codec videoCodec) {
	c.codec, c.buf, c.done, c.key, c.irap = codec, c.buf[:0], false, false, false
}

// feed adds elementary-stream bytes and reports whether the PES is classified.
func (c *classifier) feed(es []byte) bool {
	if c.done {
		return true
	}
	from := max(len(c.buf)-3, 0)
	if room := nalScanLimit - len(c.buf); len(es) > room {
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
				c.done, c.key, c.irap = true, t == 19 || t == 20, t >= 16 && t <= 21
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

// pidRewrite is how one PID's packets are changed after a splice.
type pidRewrite struct {
	// renumber: continuity counters are assigned in output order.
	renumber bool
	// awaitStart: nothing with a payload is kept until a packet that starts a
	// PES or a section. The resync discarded whatever began before the resume.
	awaitStart bool
	// bounded and leftBytes follow the PES being passed when its length is
	// known: a continuation packet after its last byte is an orphan -- the
	// rest of a PES whose start was never sent -- and is dropped.
	bounded   bool
	leftBytes int

	// The video gate: PESs are dropped while their PTS is <= floor.
	gated    bool
	floor    int64
	dropping bool
	sticky   bool
	left     int

	// The audio trim: the first PES after the resume is held and cut to
	// start at trimEnd, where the kept audio ends.
	trimming bool
	trimEnd  int64
	// trimDrop: the PES being passed is from the dumped window and goes,
	// continuation packets included, up to the next start.
	trimDrop bool
	hold     []byte
	holdWant int
	holdGot  int
}

// pkt is one packet on its way through the splice, with its PES header when
// it starts a PES.
type pkt struct {
	b []byte
	h *pesHeader
}

// tsState is the buffer's running view of the stream, and the splice in
// progress. Guarded by Buffer.mu.
type tsState struct {
	pmtPID   uint16
	havePMT  bool
	videoPID uint16
	codec    videoCodec
	// es is the current PMT's streams: PID to stream type.
	es map[uint16]byte
	// patPkt and pmtPkt are the latest PAT and PMT, sent again at a resume.
	patPkt, pmtPkt []byte

	out map[uint16]pidOut
	rw  map[uint16]*pidRewrite
	// spliced: a splice has happened, so a new PID is gated too.
	spliced bool
	// offset is subtracted from every PTS, DTS and PCR base, mod 2^33.
	offset int64
	// split holds a packet whose PES header runs into the next packet.
	split map[uint16][]byte
	// pending is cut-point snapshots waiting to learn when a PES ends.
	pending map[uint16][]*pidSnap

	cand *mark
	cls  classifier
	// waiting is cut points held back by an open unbounded audio PES.
	waiting []*mark

	// The dump waiting for its resume.
	resync  bool
	margin  time.Duration
	gop     time.Duration
	dumpAt  time.Time
	splice  *mark
	ticket  *DumpTicket
	held    []byte
	holding bool
	heldPTS int64
	heldDTS int64
	heldRAP bool
	// forcing: past the force deadline with no random-access picture, the
	// resume takes the next PES start that has a timestamp.
	forcing bool
	// heldNeedHdr: the held candidate's PES header runs into its next packet,
	// on heldPID.
	heldNeedHdr bool
	heldPID     uint16
	// resumeFloor is the last splice's output PTS: a PID new since the cut
	// is cut against it while floorLive, which ends once the video is more
	// than floorWindow from it either way -- after that a 33-bit comparison
	// with it means nothing.
	resumeFloor int64
	floorLive   bool
	// audio is the audio that arrived during the resync, kept per PID: the
	// part of it after the cut's audio end belongs to the resumed programme,
	// and is sent first so the audio carries on without a hole.
	audio      map[uint16]*audioCache
	audioBytes int
}

// audioCache is one PID's recent complete PESs, and the one being read.
type audioCache struct {
	done    [][]byte
	partial []byte
	want    int
	got     int
	// doneBytes is the bytes in done. total is the bytes in done across
	// every PID's cache, shared, and budget its ceiling: one bound for the
	// whole resync, not one per PID.
	doneBytes int
	total     *int
	budget    int
	// skipping: the PES being read went over trimHoldLimit and is dropped
	// up to the next start.
	skipping bool
}

// recentAudioPES bounds one PID's audioCache: far more than one GOP's wait.
const recentAudioPES = 64

func (c *audioCache) push(p []byte) {
	if pktPUSI(p) {
		c.finish()
		c.skipping = false
		h, _ := parsePES(p, nil)
		if !h.ok {
			return
		}
		c.partial = append(c.partial[:0], p...)
		c.want, c.got = h.length, len(payloadOf(p))-6
		return
	}
	if c.skipping || len(c.partial) == 0 || !pktHasPayload(p) {
		return
	}
	// A bounded PES takes nothing past its length; any PES over the hold
	// limit -- never completing, or never ending -- is dropped whole.
	if c.want > 0 && c.got >= c.want {
		return
	}
	if len(c.partial)+len(p) > trimHoldLimit {
		c.partial, c.skipping = c.partial[:0], true
		return
	}
	c.partial = append(c.partial, p...)
	c.got += len(payloadOf(p))
}

// finish files the PES being read: kept only when it is known whole, which
// a bounded PES is once its length has arrived and an unbounded one is when
// the next starts.
func (c *audioCache) finish() {
	if len(c.partial) > 0 && (c.want == 0 || c.got >= c.want) {
		c.done = append(c.done, append([]byte(nil), c.partial...))
		c.doneBytes += len(c.partial)
		*c.total += len(c.partial)
		// Oldest out, past either the count or the byte budget.
		for len(c.done) > 0 && (len(c.done) > recentAudioPES || *c.total > c.budget) {
			c.doneBytes -= len(c.done[0])
			*c.total -= len(c.done[0])
			c.done = c.done[1:]
		}
	}
	c.partial = c.partial[:0]
}

// take returns the cached PESs, oldest first, with a bounded PES still being
// read only if it is complete. The resume calls finish first, which files an
// unbounded PES still being read.
func (c *audioCache) take() []byte {
	var out []byte
	for _, pes := range c.done {
		out = append(out, pes...)
	}
	if len(c.partial) > 0 && c.want > 0 && c.got >= c.want {
		out = append(out, c.partial...)
	}
	return out
}

func newTSState() tsState {
	return tsState{
		es:      map[uint16]byte{},
		out:     map[uint16]pidOut{},
		rw:      map[uint16]*pidRewrite{},
		split:   map[uint16][]byte{},
		pending: map[uint16][]*pidSnap{},
	}
}

// observePSI follows the PAT and PMT, and keeps the latest of each.
func (ts *tsState) observePSI(p []byte) {
	pid := pktPID(p)
	switch {
	case pid == pidPAT:
		if sec := sectionOf(p); sec != nil {
			if pmt, ok := parsePAT(sec); ok {
				ts.pmtPID, ts.havePMT = pmt, true
				ts.patPkt = append(ts.patPkt[:0], p...)
			}
		}
	case ts.havePMT && pid == ts.pmtPID:
		if sec := sectionOf(p); sec != nil {
			if v, codec, all, ok := parsePMT(sec); ok {
				ts.videoPID, ts.codec = v, codec
				clear(ts.es)
				for _, e := range all {
					ts.es[e.pid] = e.streamType
				}
				ts.pmtPkt = append(ts.pmtPkt[:0], p...)
			}
		}
	}
}

func (ts *tsState) isVideo(pid uint16) bool {
	return ts.codec != codecNone && pid == ts.videoPID
}

// snapshot is the output state of the PAT, PMT and elementary-stream PIDs.
// Only those: a cut point must not carry every PID the stream ever had.
func (ts *tsState) snapshot() []pidSnap {
	out := make([]pidSnap, 0, len(ts.es)+4)
	seen := map[uint16]bool{}
	add := func(pid uint16) {
		if o, ok := ts.out[pid]; ok && !seen[pid] && len(out) < maxSnapPIDs {
			seen[pid] = true
			out = append(out, pidSnap{pid: pid, pidOut: o})
		}
	}
	// In priority order, so a cap never drops what a splice most needs and
	// the choice is the same every time: the PAT, the PMT and the service
	// information below 0x20; the video; the other elementary streams by PID;
	// then any other PID carried (ATSC PSIP, private data) by PID.
	for pid := uint16(0); pid < 0x20; pid++ {
		add(pid)
	}
	if ts.havePMT {
		add(ts.pmtPID)
	}
	if ts.codec != codecNone {
		add(ts.videoPID)
	}
	add2 := func(pids []uint16) {
		slices.Sort(pids)
		for _, pid := range pids {
			add(pid)
		}
	}
	es := make([]uint16, 0, len(ts.es))
	for pid := range ts.es {
		es = append(es, pid)
	}
	add2(es)
	if len(out) < maxSnapPIDs {
		rest := make([]uint16, 0, len(ts.out))
		for pid := range ts.out {
			if !seen[pid] && pid != tsNullPID {
				rest = append(rest, pid)
			}
		}
		add2(rest)
	}
	return out
}

// audioComplete reports that no bounded audio PES is part-way through.
func (ts *tsState) audioComplete() bool {
	for pid := range ts.es {
		if !ts.isVideo(pid) && ts.out[pid].remaining > 0 {
			return false
		}
	}
	return true
}

// applyOffset rewrites one packet's PCR, and the PTS and DTS of the PES header
// it starts, by the splice offset.
func (ts *tsState) applyOffset(p []byte, h *pesHeader) {
	if ts.offset == 0 {
		return
	}
	if at := pcrAt(p); at >= 0 {
		writePCRBase(p[at:], wrap(readPCRBase(p[at:])-ts.offset))
	}
	if h != nil && h.ok {
		if h.hasPTS {
			h.write(9, wrap(h.pts()-ts.offset))
		}
		if h.hasDTS {
			h.write(14, wrap(h.dts()-ts.offset))
		}
	}
}

// prepare rewrites timestamps, holding a packet whose PES header runs into
// the next one until that arrives.
func (ts *tsState) prepare(p []byte) []pkt {
	pid := pktPID(p)
	if pid == tsNullPID {
		return []pkt{{b: p}}
	}
	if first, ok := ts.split[pid]; ok {
		if !pktHasPayload(p) {
			ts.applyOffset(p, nil)
			return []pkt{{b: p}}
		}
		delete(ts.split, pid)
		h, _ := parsePES(first, p)
		ts.applyOffset(first, &h)
		if at := pcrAt(p); at >= 0 && ts.offset != 0 {
			writePCRBase(p[at:], wrap(readPCRBase(p[at:])-ts.offset))
		}
		return []pkt{{b: first, h: &h}, {b: p}}
	}
	if pktPUSI(p) && pktHasPayload(p) {
		h, complete := parsePES(p, nil)
		if !complete {
			ts.split[pid] = append([]byte(nil), p...)
			return nil
		}
		ts.applyOffset(p, &h)
		return []pkt{{b: p, h: &h}}
	}
	ts.applyOffset(p, nil)
	return []pkt{{b: p}}
}

// post applies the splice's per-PID rules: wait for a start, gate the video,
// trim the audio.
func (ts *tsState) post(q pkt) []pkt {
	pid := pktPID(q.b)
	if pid == tsNullPID {
		return []pkt{q}
	}
	if ts.floorLive && ts.isVideo(pid) && q.h != nil && q.h.ok && q.h.hasPTS {
		if d := wrap(q.h.pts() - ts.resumeFloor); d > floorWindow && d < 1<<33-floorWindow {
			ts.floorLive = false
		}
	}
	r := ts.rw[pid]
	if r == nil {
		if !ts.spliced {
			return []pkt{q}
		}
		// A PID that appeared after the splice: whatever it carried before
		// its first start belongs to nothing the output has. Near the splice
		// its data may be from the dumped window, and is cut against it.
		r = &pidRewrite{renumber: true, awaitStart: true}
		if !ts.isVideo(pid) && ts.floorLive {
			r.trimming, r.trimEnd = true, ts.resumeFloor
		}
		ts.rw[pid] = r
	}
	// Payload only: an adaptation-only packet (a PCR on a PID of its own)
	// carries no part of any PES and must keep flowing.
	if r.awaitStart && pktHasPayload(q.b) {
		if !pktPUSI(q.b) {
			return nil
		}
		r.awaitStart = false
	}
	if pktHasPayload(q.b) {
		switch {
		case pktPUSI(q.b):
			r.bounded = q.h != nil && q.h.ok && q.h.length > 0
			if r.bounded {
				r.leftBytes = q.h.length - (len(payloadOf(q.b)) - 6)
			}
		case r.bounded && r.leftBytes <= 0:
			return nil
		case r.bounded:
			r.leftBytes -= len(payloadOf(q.b))
		}
	}
	if r.trimming {
		return ts.trim(pid, r, q)
	}
	if r.gated && q.h != nil && q.h.ok {
		r.left--
		pts := q.h.pts()
		switch {
		case r.left < 0 || !q.h.hasPTS:
			r.gated, r.dropping = false, false
		case !ptsAfter(pts, r.floor):
			r.dropping = true
		case r.sticky && !ptsAfter(pts, wrap(r.floor+stickyClear)):
			r.dropping = false
		default:
			r.gated, r.dropping = false, false
		}
	}
	if r.dropping && pktHasPayload(q.b) {
		return nil
	}
	return []pkt{q}
}

// trim holds the first audio PES after a resume, and cuts it to start where
// the kept audio ends.
func (ts *tsState) trim(pid uint16, r *pidRewrite, q pkt) []pkt {
	var out []pkt
	if pktPUSI(q.b) && pktHasPayload(q.b) {
		r.trimDrop = false
		if len(r.hold) > 0 {
			out = ts.finishTrim(pid, r)
			if !r.trimming {
				return append(out, q)
			}
		}
		if q.h == nil || !q.h.ok || !q.h.hasPTS {
			r.trimming = false
			return append(out, q)
		}
		// Only audio that can be cut at a frame is held for it. Anything
		// else -- subtitles, metadata, an unknown codec -- is decided now by
		// its timestamp, so an unbounded PES is never held waiting.
		if !framedAudio(ts.es[pid], payloadOf(q.b)[q.h.esStart():]) {
			if !ptsAfter(r.trimEnd, wrap(q.h.pts()+1920)) {
				r.trimming = false
				return append(out, q)
			}
			r.trimDrop = true
			return out
		}
		r.hold = append(r.hold[:0], q.b...)
		r.holdWant = q.h.length
		r.holdGot = len(payloadOf(q.b)) - 6
		if r.holdWant > 0 && r.holdGot >= r.holdWant {
			out = append(out, ts.finishTrim(pid, r)...)
		}
		return out
	}
	if r.trimDrop && pktHasPayload(q.b) {
		return nil
	}
	if len(r.hold) == 0 || !pktHasPayload(q.b) {
		return []pkt{q}
	}
	r.hold = append(r.hold, q.b...)
	r.holdGot += len(payloadOf(q.b))
	if len(r.hold) > trimHoldLimit {
		r.trimming = false
		return releaseHeld(r)
	}
	if r.holdWant > 0 && r.holdGot >= r.holdWant {
		return ts.finishTrim(pid, r)
	}
	return nil
}

// framedAudio reports whether a PES of stream type st, whose elementary
// stream starts with es, is audio the trim can cut at a frame: AAC in ADTS,
// or Opus with its TS control header.
func framedAudio(st byte, es []byte) bool {
	switch st {
	case 0x0F:
		return true
	case 0x06:
		return len(es) >= 2 && es[0] == 0x7F && es[1]&0xE0 == 0xE0
	}
	return false
}

// releaseHeld hands back a held PES unchanged.
func releaseHeld(r *pidRewrite) []pkt {
	var out []pkt
	for off := 0; off+tsPacketSize <= len(r.hold); off += tsPacketSize {
		p := append([]byte(nil), r.hold[off:off+tsPacketSize]...)
		q := pkt{b: p}
		if off == 0 {
			h, _ := parsePES(p, nil)
			q.h = &h
		}
		out = append(out, q)
	}
	r.hold = r.hold[:0]
	return out
}

// finishTrim cuts a complete held PES at the first frame that is mostly after
// trimEnd. All of it before: dropped, and trimming goes on. None: released
// whole. Frames it cannot parse: the PES is kept if it starts within a frame
// of trimEnd, and dropped otherwise.
func (ts *tsState) finishTrim(pid uint16, r *pidRewrite) []pkt {
	var pes []byte
	for off := 0; off+tsPacketSize <= len(r.hold); off += tsPacketSize {
		pes = append(pes, payloadOf(r.hold[off:off+tsPacketSize])...)
	}
	if len(pes) < 9 || len(pes) < 9+int(pes[8]) || pes[7]&0x80 == 0 || len(pes) < 14 {
		r.trimming = false
		return releaseHeld(r)
	}
	hdrLen := 9 + int(pes[8])
	length := int(pes[4])<<8 | int(pes[5])
	es := pes[hdrLen:]
	if length > 0 {
		es = es[:max(0, min(len(es), length-(hdrLen-6)))]
	}
	pts := readTS(pes[9:])
	var frames []audioFrame
	ok := false
	switch ts.es[pid] {
	case 0x0F:
		frames, ok = adtsFrames(es)
	case 0x06:
		frames, ok = opusFrames(es)
	}
	if !ok {
		// One AAC frame at 48 kHz is 1920 ticks: within that, keep it.
		if !ptsAfter(r.trimEnd, wrap(pts+1920)) {
			r.trimming = false
			return releaseHeld(r)
		}
		r.hold = r.hold[:0]
		return nil
	}
	k, at := 0, pts
	for ; k < len(frames); k++ {
		if ptsAfter(wrap(at+frames[k].ticks/2), r.trimEnd) {
			break
		}
		at = wrap(at + frames[k].ticks)
	}
	switch k {
	case 0:
		r.trimming = false
		return releaseHeld(r)
	case len(frames):
		r.hold = r.hold[:0]
		return nil
	}
	nb := append([]byte(nil), pes[:hdrLen]...)
	writeTS(nb[9:], at)
	if nb[7]&0xC0 == 0xC0 && hdrLen >= 19 {
		writeTS(nb[14:], at)
	}
	nb = append(nb, es[frames[k].off:]...)
	if length > 0 {
		n := len(nb) - 6
		nb[4], nb[5] = byte(n>>8), byte(n)
	}
	r.trimming = false
	r.hold = r.hold[:0]
	raw := packetizePES(pid, nb)
	var out []pkt
	for off := 0; off < len(raw); off += tsPacketSize {
		q := pkt{b: raw[off : off+tsPacketSize]}
		if off == 0 {
			h, _ := parsePES(q.b, nil)
			q.h = &h
		}
		out = append(out, q)
	}
	return out
}

// ---- the buffer side ---------------------------------------------------------

// DumpTicket follows one dump from the cut to the resume.
type DumpTicket struct {
	b    *Buffer
	done chan struct{}
	// onDone is the caller's, set with the dump under the buffer's lock, so a
	// second dump (refused as pending) can never replace it. It runs once,
	// on its own goroutine: with the final result at the resume, or with an
	// aborted one when the line is cut first.
	onDone func(DumpResult)
	fired  bool
	// Set under Buffer.mu at the resume, read after done closes.
	result DumpResult
}

// Done closes when the dump is over: resumed at its keyframe, or abandoned
// by a Discard.
func (t *DumpTicket) Done() <-chan struct{} { return t.done }

// DumpResult is what a dump did.
type DumpResult struct {
	// Requested is how much was asked for; 0 meant "everything since the last
	// dump point".
	Requested time.Duration `json:"requested"`
	// Dumped is the programme time dropped: from the cut keyframe to the resume
	// keyframe, by their timestamps. Known once Complete.
	Dumped time.Duration `json:"dumped"`
	// Clamped is true when less than Requested was dropped, because the margin
	// allowed no more.
	Clamped bool `json:"clamped"`
	// Before and After are the delay the audience has, before and after.
	Before time.Duration `json:"before"`
	After  time.Duration `json:"after"`
	// Complete is false while the resume keyframe is still awaited.
	Complete bool `json:"complete"`
	// Recovery is true when no IDR picture came for several GOPs and the
	// stream resumed at another random-access picture instead.
	Recovery bool `json:"recovery,omitempty"`
	// Forced is true when no random-access picture came at all before the
	// force deadline, and the stream resumed at the live edge regardless:
	// the platform sees a picture break until the next keyframe.
	Forced bool `json:"forced,omitempty"`
	// Aborted is true when the line was cut (Stop now, a restart, a stop's
	// drain ending, shutdown) before the resume: the cut had already removed
	// programme, and the stream never resumed in this line.
	Aborted bool `json:"aborted,omitempty"`
	// CutFrom and CutTo are the wall-clock arrival times of the dumped
	// programme: what arrived in [CutFrom, CutTo) never airs.
	CutFrom time.Time `json:"-"`
	CutTo   time.Time `json:"-"`
}

// atLocked is the buffer-clock arrival time of data pushed now: real time less
// everything dumped, never earlier than the last record.
func (b *Buffer) atLocked(now time.Time) int64 {
	return max(int64(now.Sub(b.epoch)-b.shift), b.lastAt)
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
		// The resume starts with the PAT and PMT, so a reader meets any
		// change the resync carried before the PIDs it names; then the audio
		// that arrived during the resync, which the trim cuts to start where
		// the kept audio ends; then the resume picture.
		lead := append(append([]byte(nil), b.ts.patPkt...), b.ts.pmtPkt...)
		for _, c := range b.ts.audio {
			// The PES each PID was reading goes out too, on every resume
			// path: an unbounded one is whole once its PID starts another
			// (the forced resume can take that start itself), and one still
			// open runs on into the resumed programme, its continuation
			// following it.
			c.finish()
			lead = append(lead, c.take()...)
		}
		b.ts.audio, b.ts.audioBytes = nil, 0
		held := append(lead, b.ts.held...)
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
	ob := b.ob[:0]
	flush := func() {
		rest := ob
		for len(rest) > 0 {
			n := min(len(rest), chunkPackets*tsPacketSize)
			seg, off, before, bytesBefore := b.appendLocked(at, rest[:n])
			if c := ts.cand; c != nil && !c.positioned {
				c.seg, c.off, c.at, c.positioned = seg, off, at, true
				c.pushedPackets, c.pushedBytes = before, bytesBefore
				b.addMarkLocked(ts.cand)
			}
			rest = rest[n:]
		}
		ob = ob[:0]
	}
	for off := 0; off+tsPacketSize <= len(data); off += tsPacketSize {
		p := data[off : off+tsPacketSize]
		ts.observePSI(p)
		for _, q := range ts.prepare(p) {
			for _, r := range ts.post(q) {
				ob = b.commitLocked(r, ob, flush)
			}
		}
	}
	flush()
	b.ob = ob[:0]
}

// commitLocked is one packet going into the buffer: its continuity counter,
// the output state, and a cut-point candidate when it starts a picture.
func (b *Buffer) commitLocked(q pkt, ob []byte, flush func()) []byte {
	ts := &b.ts
	p := q.b
	pid := pktPID(p)
	if pid == tsNullPID {
		return append(ob, p...)
	}
	video := ts.isVideo(pid)
	pesStart := pktPUSI(p) && q.h != nil && q.h.ok
	if video && pesStart {
		// flush works on the caller's ob, which must be this one.
		b.ob = ob
		flush()
		ob = b.ob[:0]
		c := &mark{prevAt: b.lastAt, safe: ts.audioComplete(), out: ts.snapshot(), shift: b.shift}
		for pid := range ts.es {
			if !ts.isVideo(pid) && ts.out[pid].openUnbounded {
				c.waitPIDs = append(c.waitPIDs, pid)
			}
		}
		if q.h.hasPTS {
			c.pts = q.h.pts()
		} else {
			c.classified = true
		}
		ts.cand = c
		ts.cls.reset(ts.codec)
	}
	o := ts.out[pid]
	if r := ts.rw[pid]; r != nil && r.renumber && o.ccSeen {
		cc := o.cc
		if pktHasPayload(p) {
			cc = (cc + 1) & 0x0F
		}
		p[3] = p[3]&0xF0 | cc
	}
	o.cc, o.ccSeen = p[3]&0x0F, true
	switch {
	case pesStart:
		if q.h.hasPTS {
			o.pts, o.ptsSeen = q.h.pts(), true
			if !o.maxSeen || ptsAfter(o.pts, o.maxPTS) {
				o.maxPTS, o.maxSeen = o.pts, true
			}
			if video {
				d := o.pts
				if q.h.hasDTS {
					d = q.h.dts()
				}
				if o.dtsSeen {
					if step := wrap(d - o.dts); step > 0 && step < 90000 {
						o.dtsStep = step
					}
				}
				o.dts, o.dtsSeen = d, true
			}
			if !video {
				for _, s := range ts.pending[pid] {
					s.end, s.endKnown = o.pts, true
				}
				delete(ts.pending, pid)
			}
		}
		o.remaining = 0
		o.openUnbounded = !video && q.h.length == 0
		if q.h.length > 0 {
			o.remaining = max(0, q.h.length-(len(payloadOf(p))-6))
		}
	case pktHasPayload(p) && o.remaining > 0:
		o.remaining = max(0, o.remaining-len(payloadOf(p)))
	}
	ts.out[pid] = o
	if !video && pktHasPayload(p) && (len(ts.waiting) > 0 || (ts.cand != nil && len(ts.cand.waitPIDs) > 0)) {
		b.settleWaitingLocked(pid, pesStart)
	}
	if video && ts.cand != nil && !ts.cand.classified {
		es := payloadOf(p)
		if pesStart {
			es = es[q.h.esStart():]
		}
		if len(es) > 0 && ts.cls.feed(es) {
			ts.cand.classified, ts.cand.key = true, ts.cls.key
			b.addMarkLocked(ts.cand)
		}
	}
	return append(ob, p...)
}

// settleWaitingLocked follows an audio PID's packet for the cut points
// waiting on its unbounded PES: a start settles it, a continuation means the
// PES ran across the keyframe and the cut point is not safe.
func (b *Buffer) settleWaitingLocked(pid uint16, start bool) {
	ts := &b.ts
	// The open candidate, before it is placed or classified, is settled the
	// same way: the continuation can come in its first packets.
	if c := ts.cand; c != nil && !c.added && !slices.Contains(ts.waiting, c) {
		if i := slices.Index(c.waitPIDs, pid); i >= 0 {
			if start {
				c.waitPIDs = slices.Delete(c.waitPIDs, i, i+1)
				b.addMarkLocked(c)
			} else {
				c.safe = false
			}
		}
	}
	keep := ts.waiting[:0]
	for _, m := range ts.waiting {
		i := slices.Index(m.waitPIDs, pid)
		if i < 0 {
			keep = append(keep, m)
			continue
		}
		if !start {
			m.safe = false
			continue
		}
		m.waitPIDs = slices.Delete(m.waitPIDs, i, i+1)
		if len(m.waitPIDs) > 0 {
			keep = append(keep, m)
			continue
		}
		b.addMarkLocked(m)
	}
	ts.waiting = keep
}

// addMarkLocked records a candidate as a cut point once it is placed in the
// buffer, known to be an IDR picture, and splits no audio PES.
func (b *Buffer) addMarkLocked(c *mark) {
	if c == nil || c.added || !c.positioned || !c.classified || !c.key || !c.safe {
		return
	}
	if len(c.waitPIDs) > 0 {
		if !slices.Contains(b.ts.waiting, c) {
			if len(b.ts.waiting) >= 8 {
				b.ts.waiting = b.ts.waiting[1:]
			}
			b.ts.waiting = append(b.ts.waiting, c)
		}
		return
	}
	c.added = true
	// Each audio PID's last PES before the cut point ends where the next one
	// starts. If that has arrived already -- the picture was classified a few
	// packets in -- it is the newest; otherwise it is filled in on arrival.
	ts := &b.ts
	for i := range c.out {
		s := &c.out[i]
		if ts.isVideo(s.pid) || !s.ptsSeen {
			continue
		}
		if o := ts.out[s.pid]; o.ptsSeen && o.pts != s.pts {
			s.end, s.endKnown = o.pts, true
			continue
		}
		list := ts.pending[s.pid]
		if len(list) >= maxMarks {
			list = list[1:]
		}
		ts.pending[s.pid] = append(list, s)
	}
	// Forget the cut points already released.
	if len(b.queue) > 0 {
		head := b.queue[0].first
		i := 0
		for i < len(b.marks) && b.marks[i].at < head {
			b.forgetMarkLocked(b.marks[i])
			i++
		}
		b.marks = b.marks[i:]
	}
	if len(b.marks) >= maxMarks {
		b.forgetMarkLocked(b.marks[0])
		b.marks = b.marks[1:]
	}
	b.marks = append(b.marks, c)
	b.gopLocked()
}

// forgetMarkLocked takes a cut point that is going -- released, or evicted --
// out of the snapshots waiting for a PES end: a quiet PID would otherwise
// keep one per keyframe for good.
func (b *Buffer) forgetMarkLocked(m *mark) {
	for i := range m.out {
		s := &m.out[i]
		list := b.ts.pending[s.pid]
		for j, p := range list {
			if p == s {
				list = append(list[:j], list[j+1:]...)
				break
			}
		}
		if len(list) == 0 {
			delete(b.ts.pending, s.pid)
		} else {
			b.ts.pending[s.pid] = list
		}
	}
}

// gopLocked is the longest encoded GOP among recent cut points, by their
// timestamps: the wait a resume may need. Remembered, so a dump that leaves
// fewer than two cut points still knows it.
func (b *Buffer) gopLocked() time.Duration {
	if len(b.marks) >= 2 {
		var gop time.Duration
		for i := max(len(b.marks)-9, 0) + 1; i < len(b.marks); i++ {
			d := time.Duration(wrap(b.marks[i].pts-b.marks[i-1].pts)) * time.Second / 90000
			if d > b.delay {
				continue
			}
			gop = max(gop, d)
		}
		if gop > 0 {
			b.gopEst = gop
		}
	}
	if b.gopEst > 0 {
		return b.gopEst
	}
	return defaultGOP
}

// sawLocked advances the buffer's idea of now.
func (b *Buffer) sawLocked(now time.Time) {
	if now.After(b.lastNow) {
		b.lastNow = now
	}
}

// markLiveLocked reports whether a cut point is still in the buffer, unreleased.
func (b *Buffer) markLiveLocked(m *mark) bool {
	for i, s := range b.queue {
		if s == m.seg {
			return i > 0 || m.off >= s.off
		}
	}
	return false
}

// spliceableLocked is Dump's own rule, asked of the newest live cut point:
// is it the margin plus two GOPs past the head?
func (b *Buffer) spliceableLocked() bool {
	if b.ts.codec == codecNone || b.ts.resync || len(b.queue) == 0 {
		return false
	}
	// Dump's anchor: the later of the release horizon and the head.
	horizon := int64(b.lastNow.Sub(b.epoch)) - int64(b.delay)
	lo := max(horizon, b.queue[0].first) + int64(DumpMargin+2*b.gopLocked())
	for i := len(b.marks) - 1; i >= 0; i-- {
		if b.markLiveLocked(b.marks[i]) {
			return b.marks[i].at >= lo
		}
	}
	return false
}

// Dump cuts the newest want of programme back to a keyframe, and drops what
// arrives until the next keyframe. want 0 means everything buffered since the
// last dump point. It never leaves less than margin plus one GOP of delay; the
// cut leaves a second GOP for the resume wait. When want is more than that
// allows, less is dropped and Clamped says so.
//
// want is programme time, measured by the video's timestamps, so a stall in
// the feed before the dump does not count towards it.
//
// The cut happens now; the resume when the next IDR picture arrives. The
// ticket's Done closes then.
func (b *Buffer) Dump(now time.Time, want, margin time.Duration) (*DumpTicket, error) {
	return b.DumpWith(now, want, margin, nil)
}

// DumpWith is Dump with a completion callback: see DumpTicket.onDone.
func (b *Buffer) DumpWith(now time.Time, want, margin time.Duration, done func(DumpResult)) (*DumpTicket, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ts := &b.ts
	if ts.resync {
		return nil, ErrDumpPending
	}
	if ts.codec == codecNone || len(b.marks) == 0 || len(b.queue) == 0 {
		return nil, ErrNotSpliceable
	}
	gop := b.gopLocked()
	// THE RELEASE HORIZON: what is being released now, on the buffer clock.
	// A cut at K1 leaves K1 - horizon of delay, less the resume wait.
	horizon := int64(now.Sub(b.epoch)) - int64(b.delay)
	lo := max(horizon, b.queue[0].first) + int64(margin+2*gop)
	var pick *mark
	clamped := false
	if want > 0 {
		newest, ok := ts.out[ts.videoPID]
		if !ok || !newest.ptsSeen {
			return nil, ErrNotSpliceable
		}
		limit := wrap(newest.pts - int64(want)*90000/int64(time.Second))
		for i := len(b.marks) - 1; i >= 0; i-- {
			m := b.marks[i]
			if m.at < lo || ptsAfter(m.pts, limit) || !b.markLiveLocked(m) {
				continue
			}
			pick = m
			break
		}
		if pick == nil {
			for _, m := range b.marks {
				if m.at >= lo && b.markLiveLocked(m) {
					pick, clamped = m, true
					break
				}
			}
		}
	} else {
		floor := max(lo, b.lastDumpAt)
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
	vnow := int64(now.Sub(b.epoch) - b.shift)
	if err := b.truncateLocked(pick); err != nil {
		return nil, err
	}
	t := &DumpTicket{b: b, done: make(chan struct{}), onDone: done}
	// Provisional until the resume: what has been cut so far.
	cut := time.Duration(vnow - pick.at)
	t.result = DumpResult{Requested: want, Clamped: clamped, Before: before, Dumped: cut, After: before - cut,
		CutFrom: b.epoch.Add(time.Duration(pick.at) + pick.shift)}
	ts.resync, ts.splice, ts.ticket = true, pick, t
	ts.margin, ts.gop, ts.dumpAt = margin, gop, now
	ts.holding, ts.held, ts.audio, ts.audioBytes = false, nil, nil, 0
	ts.cand = nil
	clear(ts.split)
	clear(ts.pending)
	ts.waiting = nil
	clear(ts.out)
	for _, s := range pick.out {
		ts.out[s.pid] = s.pidOut
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
	// A cut behind the read position would remove programme already
	// released and count it again; Dump's margin keeps every cut past the
	// head, so this only refuses what cannot be cut. A spilled segment is
	// never part read (Pop loads it first), so its file offsets are the
	// records' own.
	if m.off < s.off || (s.data == nil && s.off != 0) {
		return ErrNoCutPoint
	}
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
	// THE FORCE DEADLINE. No random-access picture at all -- an audio-only
	// failover, a codec this cannot classify, H.264 without the flag -- would
	// leave the dump waiting for ever with the destination dark. Past twice
	// the recovery wait the resume takes the next PES start that carries a
	// timestamp, on any stream. The platform sees a picture break until the
	// source's next keyframe; that is said in the result.
	force := now.Sub(ts.dumpAt) >= 2*max(minRecoveryWait, recoveryGOPs*ts.gop)
	for off := 0; off < len(work); off += tsPacketSize {
		p := work[off : off+tsPacketSize]
		ts.observePSI(p)
		pid := pktPID(p)
		video := ts.isVideo(pid)
		if _, isES := ts.es[pid]; force && !ts.holding && pktPUSI(p) && (isES || video) {
			h, complete := parsePES(p, nil)
			if !complete {
				// The header runs into this PID's next packet: hold the start
				// and take it there.
				ts.held = append(ts.held[:0], p...)
				ts.holding, ts.heldNeedHdr, ts.heldPID, ts.forcing = true, true, pid, true
				continue
			}
			if h.ok && h.hasPTS {
				ts.held = append(ts.held[:0], p...)
				ts.holding, ts.heldPTS, ts.heldDTS, ts.forcing = true, h.pts(), h.pts(), true
				if h.hasDTS {
					ts.heldDTS = h.dts()
				}
				if b.decideResumeLocked(now) {
					return work[off+tsPacketSize:], true
				}
			}
		}
		if _, isES := ts.es[pid]; isES && !video && !ts.holding {
			if ts.audio == nil {
				ts.audio = map[uint16]*audioCache{}
			}
			c := ts.audio[pid]
			if c == nil {
				c = &audioCache{budget: audioCacheBudget(b.lim), total: &ts.audioBytes}
				ts.audio[pid] = c
			}
			c.push(p)
		}
		if video && pktPUSI(p) {
			ts.holding, ts.held, ts.heldNeedHdr = false, ts.held[:0], false
			h, complete := parsePES(p, nil)
			if !complete {
				// The header runs into the next video packet: hold this one
				// and read it there, as the normal path does.
				ts.holding, ts.heldNeedHdr, ts.heldPID, ts.heldRAP = true, true, pid, pktRandomAccess(p)
				ts.held = append(ts.held, p...)
				continue
			}
			if !h.ok || !h.hasPTS {
				continue
			}
			ts.holding, ts.heldPTS, ts.heldRAP = true, h.pts(), pktRandomAccess(p)
			ts.heldDTS = ts.heldPTS
			if h.hasDTS {
				ts.heldDTS = h.dts()
			}
			ts.cls.reset(ts.codec)
			ts.held = append(ts.held, p...)
			if es := payloadOf(p)[h.esStart():]; len(es) > 0 && ts.cls.feed(es) {
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
		if ts.heldNeedHdr && pid == ts.heldPID && pktHasPayload(p) {
			ts.heldNeedHdr = false
			first := ts.held[:tsPacketSize]
			h, _ := parsePES(first, p)
			if !h.ok || !h.hasPTS {
				ts.holding, ts.held, ts.forcing = false, ts.held[:0], false
				continue
			}
			ts.heldPTS, ts.heldDTS = h.pts(), h.pts()
			if h.hasDTS {
				ts.heldDTS = h.dts()
			}
			if ts.forcing {
				// A forced resume takes any PES start: no picture to read.
				if b.decideResumeLocked(now) {
					return work[off+tsPacketSize:], true
				}
				continue
			}
			ts.cls.reset(ts.codec)
			es := payloadOf(p)
			if skip := h.hdrLen - len(h.parts[0]); skip > 0 {
				es = es[min(skip, len(es)):]
			}
			if len(es) > 0 && ts.cls.feed(es) {
				if b.decideResumeLocked(now) {
					return work[off+tsPacketSize:], true
				}
			}
			continue
		}
		if video && !ts.heldNeedHdr {
			if es := payloadOf(p); len(es) > 0 && ts.cls.feed(es) {
				if b.decideResumeLocked(now) {
					return work[off+tsPacketSize:], true
				}
			}
		}
	}
	return nil, false
}

// decideResumeLocked acts on a classified resume candidate: an IDR resumes the
// stream; after the recovery wait, any random-access picture does; anything
// else is discarded.
func (b *Buffer) decideResumeLocked(now time.Time) bool {
	ts := &b.ts
	recovery := now.Sub(ts.dumpAt) >= max(minRecoveryWait, recoveryGOPs*ts.gop)
	if !ts.forcing && !ts.cls.key && !(recovery && (ts.heldRAP || ts.cls.irap)) {
		ts.holding, ts.held = false, ts.held[:0]
		return false
	}
	forced := ts.forcing
	recovered := !ts.cls.key && !forced
	// RECOVERY AT AN HEVC CRA (or BLA): the picture is rewritten to
	// BLA_N_LP, so a decoder starts afresh rather than expecting the pictures
	// before it; its leading pictures are all dropped below.
	if recovered && ts.codec == codecHEVC && ts.cls.irap {
		rewriteFirstVCL(ts.held, ts.videoPID, func(h byte) byte {
			return h&0x81 | 18<<1
		})
	}
	prevOffset := ts.offset
	m := ts.splice
	// The resume picture takes the cut picture's place on both clocks.
	ts.offset = wrap(ts.heldPTS - m.pts)
	// ...unless its DTS would then not come after the last picture kept: a
	// GOP with deeper reordering (an open-GOP CRA, whose leading pictures
	// are shown before it and decoded after) puts the DTS further before the
	// PTS. Then the DTS decides, one frame after the last kept, and the
	// pictures are shown that much later -- a short hold, not a decode-order
	// step a remux would have to repair.
	for _, sn := range m.out {
		if sn.pid != ts.videoPID || !sn.dtsSeen {
			continue
		}
		step := sn.dtsStep
		if step <= 0 {
			step = 3000
		}
		if !ptsAfter(wrap(ts.heldDTS-ts.offset), sn.dts) {
			ts.offset = wrap(ts.heldDTS - sn.dts - step)
		}
	}
	before := b.shift
	b.shift = now.Sub(b.epoch) - time.Duration(m.at)
	// THE FLOOR HOLDS HOWEVER LATE THE RESUME IS. The cut left room for one
	// GOP of waiting; an input that stalls mid-dump (an encoder
	// reconnecting) makes the wait as long as the stall. So the delay gives
	// up at most down to the margin plus one GOP, and the rest of the wait is
	// a gap on the wire, which the stall was anyway.
	if limit := b.delay - ts.margin - ts.gop; b.shift > limit {
		b.shift = limit
	}
	b.shift = max(b.shift, before)
	// The last record kept is the one before the cut picture.
	b.lastAt = m.prevAt
	b.lastDumpAt = m.at
	ts.rw = map[uint16]*pidRewrite{}
	for pid := range ts.out {
		ts.rw[pid] = &pidRewrite{renumber: true, awaitStart: true}
	}
	for pid := range ts.es {
		if ts.rw[pid] == nil {
			ts.rw[pid] = &pidRewrite{renumber: true, awaitStart: true}
		}
	}
	// EVERY NON-VIDEO ELEMENTARY STREAM is cut by timestamp against the
	// splice: what is from the dumped window never airs, whether or not the
	// PID sent anything before the cut. Audio that was flowing to the cut
	// starts where the kept audio ends, to the frame; a PID whose last PES is
	// older than a second before the cut (subtitles, metadata) is cut at the
	// splice itself.
	ts.resumeFloor, ts.floorLive = m.pts, true
	ends := map[uint16]int64{}
	for _, s := range m.out {
		if s.ptsSeen {
			end := s.pts
			if s.endKnown {
				end = s.end
			}
			ends[s.pid] = end
		}
	}
	for pid := range ts.es {
		if ts.isVideo(pid) {
			continue
		}
		end, ok := ends[pid]
		if !ok || ptsAfter(wrap(m.pts-90000), end) {
			end = m.pts
		}
		r := ts.rw[pid]
		r.trimming, r.trimEnd = true, end
	}
	// Video: drop only pictures that would be SHOWN at or before the last
	// picture kept. The resume picture lands exactly on the cut picture's
	// PTS, after it; its leading pictures (HEVC RADL) land in the slots the
	// cut picture's own leading pictures left, and are kept. One that would
	// overlap a kept picture is dropped.
	if ts.codec != codecNone {
		r := ts.rw[ts.videoPID]
		if r == nil {
			r = &pidRewrite{renumber: true, awaitStart: true}
			ts.rw[ts.videoPID] = r
		}
		floor := wrap(m.pts - 1)
		for _, s := range m.out {
			if s.pid == ts.videoPID && s.maxSeen && !recovered && !forced {
				floor = s.maxPTS
			}
		}
		r.gated, r.floor, r.sticky, r.left = true, floor, true, gateLimit
	}
	ts.spliced = true
	ts.resync, ts.holding, ts.splice, ts.forcing = false, false, nil, false
	if t := ts.ticket; t != nil {
		// The programme skipped: the cut picture to the resume picture.
		t.result.Dumped = time.Duration(wrap(ts.offset-prevOffset)) * time.Second / 90000
		if t.result.Dumped > MaxDelay {
			t.result.Dumped = b.shift - before
		}
		t.result.After = b.delay - b.shift
		t.result.Clamped = t.result.Requested > 0 && t.result.Dumped < t.result.Requested
		t.result.Complete = true
		t.result.Recovery = recovered
		t.result.Forced = forced
		t.result.CutTo = now
		close(t.done)
		t.fireLocked()
		b.resumed.Store(t)
		ts.ticket = nil
	}
	return true
}

// abandonDumpLocked ends a dump that will not resume: the line is being cut.
func (b *Buffer) abandonDumpLocked() {
	if t := b.ts.ticket; t != nil {
		t.result.Aborted = true
		close(t.done)
		t.fireLocked()
		b.ts.ticket = nil
	}
}

// fireLocked runs the caller's callback once, off the buffer's lock.
func (t *DumpTicket) fireLocked() {
	if t.onDone == nil || t.fired {
		return
	}
	t.fired = true
	go t.onDone(t.result)
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

// floorWindow is how long after a splice, in 90 kHz ticks of video, a PID
// new since it is cut against it: data from the dumped window arrives within
// the source's interleave, seconds after the resume, not a minute.
const floorWindow = 60 * 90000

// audioCacheBudget is the most complete audio the resync keeps, across every
// PID: two spill segments, so it scales with the buffer's own granularity.
// It is the one RAM the buffer holds outside MemBudget + 2*SegmentBytes, and
// only while a dump waits for its keyframe; see the package comment.
func audioCacheBudget(lim Limits) int { return 2 * lim.SegmentBytes }
