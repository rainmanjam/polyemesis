// Package mpegts is the one place the MPEG-TS packet layout and PSI (PAT/PMT)
// parsing live. The relay, the clip ring, the delay line and the caption
// injector each used to carry their own copy of the constants and a PAT/PMT
// reader. SectionOf is the strictest of those copies (the caption injector's):
// it refuses an adaptation-only packet and an adaptation field longer than the
// packet itself.
package mpegts

const (
	PacketSize = 188
	SyncByte   = 0x47
	// NullPID carries stuffing.
	NullPID = 0x1FFF
	PIDPAT  = 0x0000

	StreamTypeH264 = 0x1B
	StreamTypeHEVC = 0x24
)

// PID is the packet's 13-bit PID. p must hold at least 3 bytes.
func PID(p []byte) uint16 { return uint16(p[1]&0x1F)<<8 | uint16(p[2]) }

// PUSI is the payload_unit_start_indicator.
func PUSI(p []byte) bool { return p[1]&0x40 != 0 }

// HasPayload reports the adaptation_field_control payload bit.
func HasPayload(p []byte) bool { return p[3]&0x10 != 0 }

// HasAdaptation reports the adaptation_field_control adaptation bit.
func HasAdaptation(p []byte) bool { return p[3]&0x20 != 0 }

// ValidAdaptation reports whether a packet's adaptation field fits inside it.
func ValidAdaptation(p []byte) bool {
	if !HasAdaptation(p) {
		return true
	}
	n := int(p[4])
	return n <= PacketSize-5 && 5+n <= len(p)
}

// PayloadStart is where the payload begins, or PacketSize when there is none.
func PayloadStart(p []byte) int {
	if !HasPayload(p) {
		return PacketSize
	}
	s := 4
	if HasAdaptation(p) {
		s += 1 + int(p[4])
	}
	if s > PacketSize {
		return PacketSize
	}
	return s
}

// SectionOf returns the PSI section a packet starts, past any adaptation field
// and the pointer field. It returns nil for a packet that does not start a
// section, carries no payload, has an adaptation field longer than itself, or
// is short. A section continued from an earlier packet is the caller's to
// reassemble.
func SectionOf(p []byte) []byte {
	if len(p) < PacketSize || !PUSI(p) || !HasPayload(p) || !ValidAdaptation(p) {
		return nil
	}
	at := PayloadStart(p)
	if at >= len(p) {
		return nil
	}
	at += 1 + int(p[at])
	if at >= len(p) {
		return nil
	}
	return p[at:]
}

// SectionLen is the full length of the section starting at sec[0], header
// included. sec must hold at least 3 bytes.
func SectionLen(sec []byte) int { return 3 + (int(sec[1]&0x0F)<<8 | int(sec[2])) }

// PATProgram returns the PMT PID of the first real program in a PAT section.
func PATProgram(sec []byte) (uint16, bool) {
	if len(sec) < 12 || sec[0] != 0x00 {
		return 0, false
	}
	end := min(SectionLen(sec), len(sec))
	// 5 bytes of section header after the length, then 4 bytes per program,
	// then a 4-byte CRC that must not be read as one.
	for off := 8; off+4 <= end-4; off += 4 {
		if uint16(sec[off])<<8|uint16(sec[off+1]) == 0 {
			// The network information table, not a program.
			continue
		}
		return uint16(sec[off+2]&0x1F)<<8 | uint16(sec[off+3]), true
	}
	return 0, false
}

// Stream is one elementary stream a PMT lists.
type Stream struct {
	PID  uint16
	Type byte
}

// PMTStreams lists the elementary streams of a PMT section in order. ok is
// false when sec is not a PMT. A section cut short is read as far as it goes.
func PMTStreams(sec []byte) (streams []Stream, ok bool) {
	if len(sec) < 16 || sec[0] != 0x02 {
		return nil, false
	}
	end := min(SectionLen(sec), len(sec))
	off := 12 + (int(sec[10]&0x0F)<<8 | int(sec[11]))
	for off+5 <= end-4 {
		streams = append(streams, Stream{
			Type: sec[off],
			PID:  uint16(sec[off+1]&0x1F)<<8 | uint16(sec[off+2]),
		})
		off += 5 + (int(sec[off+3]&0x0F)<<8 | int(sec[off+4]))
	}
	return streams, true
}

// IsVideo reports whether a PMT stream_type carries pictures.
func IsVideo(streamType byte) bool {
	switch streamType {
	case 0x01, 0x02, 0x10, StreamTypeH264, StreamTypeHEVC, 0x33, 0x42, 0xD1, 0xEA:
		return true
	}
	return false
}
