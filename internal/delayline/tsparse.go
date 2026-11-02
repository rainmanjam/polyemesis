package delayline

// MPEG-TS, PES and audio-frame parsing for the dump (splice.go). Just enough
// of each format to find a cut point, rewrite timestamps and trim an audio PES
// at a frame boundary; nothing here decodes media.

const (
	tsPacketSize = 188
	tsSyncByte   = 0x47
	tsNullPID    = 0x1FFF
	pidPAT       = 0x0000

	// ptsModulus is the wrap of the 33-bit PTS, DTS and PCR base.
	ptsModulus = int64(1) << 33
)

type videoCodec uint8

const (
	codecNone videoCodec = iota
	codecH264
	codecHEVC
)

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

func pktPID(p []byte) uint16        { return uint16(p[1]&0x1F)<<8 | uint16(p[2]) }
func pktPUSI(p []byte) bool         { return p[1]&0x40 != 0 }
func pktHasPayload(p []byte) bool   { return p[3]&0x10 != 0 }
func pktHasAF(p []byte) bool        { return p[3]&0x20 != 0 }
func pktRandomAccess(p []byte) bool { return pktHasAF(p) && p[4] > 0 && p[5]&0x40 != 0 }

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

func payloadOf(p []byte) []byte { return p[payloadStart(p):] }

// pesHeader is a PES header located across one packet's payload or, when it
// runs past it, the next packet's payload on the same PID too.
type pesHeader struct {
	parts  [2][]byte // payload slices the header spans
	ok     bool      // a PES start code and a complete optional header
	hasPTS bool
	hasDTS bool
	// length is PES_packet_length: the bytes after the length field, 0 for
	// unbounded.
	length int
	// hdrLen is the whole header: 9 + PES_header_data_length.
	hdrLen int
}

// headerBytes is how many header bytes a PES start must supply before its PTS
// and DTS can be read: enough for the flags, and then the fields themselves.
func (h *pesHeader) at(i int) *byte {
	if i < len(h.parts[0]) {
		return &h.parts[0][i]
	}
	i -= len(h.parts[0])
	if i < len(h.parts[1]) {
		return &h.parts[1][i]
	}
	return nil
}

func (h *pesHeader) size() int { return len(h.parts[0]) + len(h.parts[1]) }

// parsePES reads a PES header from first's payload, continued into next's
// when next is not nil. complete is false when the header needs bytes the
// given payloads do not have -- the caller holds first for the next packet.
func parsePES(first, next []byte) (h pesHeader, complete bool) {
	h.parts[0] = payloadOf(first)
	if next != nil {
		h.parts[1] = payloadOf(next)
	}
	b := func(i int) int {
		if p := h.at(i); p != nil {
			return int(*p)
		}
		return -1
	}
	if h.size() < 6 {
		return h, next != nil
	}
	if b(0) != 0 || b(1) != 0 || b(2) != 1 {
		return h, true
	}
	h.length = b(4)<<8 | b(5)
	switch b(3) {
	case 0xBC, 0xBE, 0xBF, 0xF0, 0xF1, 0xFF, 0xF2, 0xF8:
		h.ok, h.hdrLen = true, 6
		return h, true
	}
	if h.size() < 9 {
		return h, next != nil
	}
	flags := b(7)
	h.hdrLen = 9 + b(8)
	need := 9
	if flags&0x80 != 0 {
		need = 14
	}
	if flags&0xC0 == 0xC0 {
		need = 19
	}
	if need > h.hdrLen {
		// A header that says it has a PTS but is too short for it is damage.
		return h, true
	}
	if h.size() < need {
		return h, next != nil
	}
	h.ok = true
	h.hasPTS = flags&0x80 != 0
	h.hasDTS = flags&0xC0 == 0xC0
	return h, true
}

func (h *pesHeader) read(off int) int64 {
	v := func(i int) int64 { return int64(*h.at(off + i)) }
	return (v(0)>>1&0x07)<<30 | v(1)<<22 | (v(2)>>1)<<15 | v(3)<<7 | v(4)>>1
}

func (h *pesHeader) write(off int, val int64) {
	b := func(i int) *byte { return h.at(off + i) }
	*b(0) = *b(0)&0xF1 | byte(val>>29)&0x0E
	*b(1) = byte(val >> 22)
	*b(2) = byte(val>>14)&0xFE | 0x01
	*b(3) = byte(val >> 7)
	*b(4) = byte(val<<1)&0xFE | 0x01
}

func (h *pesHeader) pts() int64 { return h.read(9) }
func (h *pesHeader) dts() int64 { return h.read(14) }

// esStart is where the elementary stream begins in the first packet's
// payload, or len(payload) when the header runs past it.
func (h *pesHeader) esStart() int {
	if h.hdrLen > len(h.parts[0]) {
		return len(h.parts[0])
	}
	return h.hdrLen
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

// esInfo is one elementary stream of a PMT.
type esInfo struct {
	pid        uint16
	streamType byte
}

// parsePMT returns the first video elementary stream, its codec, and every
// elementary stream the PMT lists.
func parsePMT(sec []byte) (uint16, videoCodec, []esInfo, bool) {
	if len(sec) < 16 || sec[0] != 0x02 {
		return 0, codecNone, nil, false
	}
	end := 3 + (int(sec[1]&0x0F)<<8 | int(sec[2]))
	if end > len(sec) {
		end = len(sec)
	}
	var (
		vpid  uint16
		codec videoCodec
		found bool
		all   []esInfo
	)
	off := 12 + (int(sec[10]&0x0F)<<8 | int(sec[11]))
	for off+5 <= end-4 {
		st := sec[off]
		pid := uint16(sec[off+1]&0x1F)<<8 | uint16(sec[off+2])
		all = append(all, esInfo{pid: pid, streamType: st})
		if !found {
			switch st {
			case 0x1B:
				vpid, codec, found = pid, codecH264, true
			case 0x24:
				vpid, codec, found = pid, codecHEVC, true
			case 0x01, 0x02, 0x10, 0x33, 0x42, 0xD1, 0xEA:
				// Video, but not a codec a dump can classify.
				vpid, codec, found = pid, codecNone, true
			}
		}
		off += 5 + (int(sec[off+3]&0x0F)<<8 | int(sec[off+4]))
	}
	return vpid, codec, all, found || len(all) > 0
}

// ---- audio frames ------------------------------------------------------------

// audioFrame is one frame inside an audio PES's elementary stream.
type audioFrame struct {
	off, size int
	ticks     int64 // duration at 90 kHz
}

var adtsRates = [...]int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}

// adtsFrames splits an ADTS elementary stream into frames. ok is false when
// it is not whole ADTS frames.
func adtsFrames(es []byte) ([]audioFrame, bool) {
	var out []audioFrame
	for off := 0; off < len(es); {
		if off+7 > len(es) || es[off] != 0xFF || es[off+1]&0xF0 != 0xF0 {
			return nil, false
		}
		idx := int(es[off+2]>>2) & 0x0F
		if idx >= len(adtsRates) {
			return nil, false
		}
		size := int(es[off+3]&0x03)<<11 | int(es[off+4])<<3 | int(es[off+5]>>5)
		if size < 7 || off+size > len(es) {
			return nil, false
		}
		blocks := int(es[off+6]&0x03) + 1
		out = append(out, audioFrame{off: off, size: size,
			ticks: int64(blocks*1024) * 90000 / int64(adtsRates[idx])})
		off += size
	}
	return out, len(out) > 0
}

// opusFrames splits an MPEG-TS Opus elementary stream (ETSI TS 102 366 style
// control headers, as FFmpeg writes it) into access units.
func opusFrames(es []byte) ([]audioFrame, bool) {
	var out []audioFrame
	for off := 0; off < len(es); {
		start := off
		if off+2 > len(es) || es[off] != 0x7F || es[off+1]&0xE0 != 0xE0 {
			return nil, false
		}
		flags := es[off+1]
		off += 2
		size := 0
		for {
			if off >= len(es) {
				return nil, false
			}
			v := int(es[off])
			off++
			size += v
			if v != 0xFF {
				break
			}
		}
		if flags&0x10 != 0 {
			off += 2
		}
		if flags&0x08 != 0 {
			off += 2
		}
		if flags&0x04 != 0 {
			if off >= len(es) {
				return nil, false
			}
			off += 1 + int(es[off])
		}
		if size < 1 || off+size > len(es) {
			return nil, false
		}
		ticks, ok := opusTicks(es[off : off+size])
		if !ok {
			return nil, false
		}
		off += size
		out = append(out, audioFrame{off: start, size: off - start, ticks: ticks})
	}
	return out, len(out) > 0
}

// opusTicks is an Opus packet's duration at 90 kHz, from its TOC byte.
func opusTicks(pkt []byte) (int64, bool) {
	toc := pkt[0]
	config := int(toc >> 3)
	var tenthsMs int // frame duration in 0.1 ms
	switch {
	case config < 12:
		tenthsMs = [...]int{100, 200, 400, 600}[config&3]
	case config < 16:
		tenthsMs = [...]int{100, 200}[config&1]
	default:
		tenthsMs = [...]int{25, 50, 100, 200}[config&3]
	}
	frames := 1
	switch toc & 3 {
	case 1, 2:
		frames = 2
	case 3:
		if len(pkt) < 2 {
			return 0, false
		}
		frames = int(pkt[1] & 0x3F)
	}
	return int64(tenthsMs*frames) * 9, true
}

// packetizePES splits one PES into TS packets on pid, stuffing the last one
// through its adaptation field. Continuity counters are left 0: the splice
// numbers every packet it emits.
func packetizePES(pid uint16, pes []byte) []byte {
	var out []byte
	first := true
	for len(pes) > 0 {
		p := make([]byte, 4, tsPacketSize)
		p[0] = tsSyncByte
		p[1] = byte(pid>>8) & 0x1F
		if first {
			p[1] |= 0x40
		}
		p[2] = byte(pid)
		n := len(pes)
		if n >= 184 {
			n = 184
			p[3] = 0x10
		} else {
			p[3] = 0x30
			af := 184 - n - 1 // adaptation field bytes after its length byte
			p = append(p, byte(af))
			if af > 0 {
				p = append(p, 0x00)
				for i := 1; i < af; i++ {
					p = append(p, 0xFF)
				}
			}
		}
		p = append(p, pes[:n]...)
		out = append(out, p...)
		pes = pes[n:]
		first = false
	}
	return out
}
