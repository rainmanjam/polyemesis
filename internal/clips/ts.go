package clips

// Just enough MPEG-TS to cut the stream somewhere a decoder can start.
//
// TS is self-synchronising, so a file that begins at any 188-byte boundary is
// structurally valid — and will show a grey mush until the next keyframe,
// because the first frames reference pictures that are not in the file. A clip
// nobody can watch the first two seconds of is a clip that gets a bug report.
//
// So this walks past every datagram on its way into the ring, tracking three
// things and nothing else: which PID carries video (from the PAT and the PMT),
// where the random-access points are, and the most recent PAT and PMT packets
// so they can be re-emitted at the head of a cut. Cost is a handful of branches
// per 188-byte packet, the same budget the relay's continuity counter already
// spends, which is why it runs inline rather than being sampled.

const (
	tsPacketSize = 188
	tsSyncByte   = 0x47
	tsNullPID    = 0x1FFF
	pidPAT       = 0x0000

	// Adaptation-field flags, in the byte after the field length.
	afRandomAccess = 0x40
)

// videoStreamTypes are the PMT stream_type values that carry pictures.
//
// A type this table has never heard of leaves the video PID unknown, which
// makes a cut fall back to a plain packet boundary rather than refusing to
// produce one. That is the fail-open direction on purpose: a slightly ugly
// first second beats "your clip could not be created".
var videoStreamTypes = map[byte]bool{
	0x01: true, // MPEG-1 video
	0x02: true, // MPEG-2 video
	0x10: true, // MPEG-4 part 2
	0x1B: true, // H.264
	0x24: true, // HEVC
	0x33: true, // VVC
	0x42: true, // Chinese AVS
	0xD1: true, // Dirac
	0xEA: true, // VC-1
}

// demux is the running view of the transport stream.
type demux struct {
	pmtPID    uint16
	havePMT   bool
	videoPID  uint16
	haveVideo bool

	pat      [tsPacketSize]byte
	havePAT  bool
	pmt      [tsPacketSize]byte
	pmtCache bool
}

// observe folds one datagram into the running view.
func (d *demux) observe(dgram []byte) {
	for off := 0; off+tsPacketSize <= len(dgram); off += tsPacketSize {
		p := dgram[off : off+tsPacketSize]
		// Losing sync means the rest of the datagram cannot be trusted to be
		// packet-aligned, so stop rather than guess.
		if p[0] != tsSyncByte {
			return
		}
		pid := packetPID(p)
		if pid == tsNullPID || !hasPayload(p) {
			continue
		}
		switch {
		case pid == pidPAT:
			copy(d.pat[:], p)
			d.havePAT = true
			if id, ok := parsePAT(sectionOf(p)); ok {
				d.pmtPID, d.havePMT = id, true
			}
		case d.havePMT && pid == d.pmtPID:
			copy(d.pmt[:], p)
			d.pmtCache = true
			if v, ok := parsePMT(sectionOf(p)); ok {
				d.videoPID, d.haveVideo = v, true
			}
		}
	}
}

// psi returns the PAT and PMT packets to write at the head of a cut.
//
// The cut starts at a keyframe, which is almost never immediately after the
// muxer's periodic PSI, so without this the player would have to wait up to a
// PAT period before it knew what the streams were. Re-emitting the last ones
// seen costs 376 bytes and removes that wait.
func (d *demux) psi() []byte {
	if !d.havePAT {
		return nil
	}
	out := make([]byte, 0, 2*tsPacketSize)
	out = append(out, d.pat[:]...)
	if d.pmtCache {
		out = append(out, d.pmt[:]...)
	}
	return out
}

// randomAccess reports the offset of the first packet in this datagram that a
// decoder can start at: a video packet beginning an access unit and flagged as
// a random-access point.
//
// Only the flag is trusted, never the PES payload. FFmpeg's mpegts muxer sets
// random_access_indicator on exactly the keyframe packets, and parsing NAL
// units to second-guess it would cost far more and be wrong for every codec
// this build has not been taught.
func (d *demux) randomAccess(dgram []byte) (int, bool) {
	if !d.haveVideo {
		return 0, false
	}
	for off := 0; off+tsPacketSize <= len(dgram); off += tsPacketSize {
		p := dgram[off : off+tsPacketSize]
		if p[0] != tsSyncByte {
			return 0, false
		}
		if packetPID(p) != d.videoPID {
			continue
		}
		// payload_unit_start_indicator: without it this is the middle of an
		// access unit and starting here would truncate the picture.
		if p[1]&0x40 == 0 {
			continue
		}
		if !hasAdaptation(p) || p[4] == 0 {
			continue
		}
		if p[5]&afRandomAccess != 0 {
			return off, true
		}
	}
	return 0, false
}

func packetPID(p []byte) uint16 {
	return uint16(p[1]&0x1F)<<8 | uint16(p[2])
}

func hasPayload(p []byte) bool    { return p[3]&0x10 != 0 }
func hasAdaptation(p []byte) bool { return p[3]&0x20 != 0 }

// sectionOf returns the PSI section bytes of a packet that starts one, i.e.
// past the adaptation field and past the pointer_field. It returns nil for a
// packet that continues a section, because a section split across packets is a
// section this build declines to reassemble: PAT and PMT for a live stream fit
// in one packet, and a stream where they do not simply leaves the video PID
// unknown and falls back to an unaligned cut.
func sectionOf(p []byte) []byte {
	if p[1]&0x40 == 0 {
		return nil
	}
	start := 4
	if hasAdaptation(p) {
		start += 1 + int(p[4])
	}
	if start >= tsPacketSize {
		return nil
	}
	ptr := int(p[start])
	start += 1 + ptr
	if start >= tsPacketSize {
		return nil
	}
	return p[start:]
}

// parsePAT returns the first real program's PMT PID.
func parsePAT(sec []byte) (uint16, bool) {
	if len(sec) < 12 || sec[0] != 0x00 {
		return 0, false
	}
	length := int(sec[1]&0x0F)<<8 | int(sec[2])
	end := 3 + length
	if end > len(sec) {
		end = len(sec)
	}
	// 5 bytes of section header after the length, then 4 bytes per program,
	// then a 4-byte CRC that must not be read as one.
	for off := 8; off+4 <= end-4; off += 4 {
		program := uint16(sec[off])<<8 | uint16(sec[off+1])
		if program == 0 {
			// The network information table, not a program.
			continue
		}
		return uint16(sec[off+2]&0x1F)<<8 | uint16(sec[off+3]), true
	}
	return 0, false
}

// parsePMT returns the elementary PID carrying video.
func parsePMT(sec []byte) (uint16, bool) {
	if len(sec) < 16 || sec[0] != 0x02 {
		return 0, false
	}
	length := int(sec[1]&0x0F)<<8 | int(sec[2])
	end := 3 + length
	if end > len(sec) {
		end = len(sec)
	}
	infoLen := int(sec[10]&0x0F)<<8 | int(sec[11])
	off := 12 + infoLen
	for off+5 <= end-4 {
		streamType := sec[off]
		pid := uint16(sec[off+1]&0x1F)<<8 | uint16(sec[off+2])
		esLen := int(sec[off+3]&0x0F)<<8 | int(sec[off+4])
		if videoStreamTypes[streamType] {
			return pid, true
		}
		off += 5 + esLen
	}
	return 0, false
}
