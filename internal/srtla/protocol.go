package srtla

import (
	"encoding/binary"
)

// The SRTLA wire format, as far as the receiver needs it.
//
// SRTLA rides inside the same UDP datagrams as SRT and borrows SRT's own way of
// telling packet kinds apart: a set high bit in the first byte means "control
// packet", and the next fifteen bits are the control type. SRT's own control
// types live at 0x8000-0x80FF; SRTLA claims 0x9000 and up. So a datagram whose
// first sixteen bits are one of the values below is ours, and anything else is
// SRT and is forwarded untouched.
//
// Every integer is big-endian, as in SRT.
const (
	typeKeepalive = 0x9000
	typeACK       = 0x9100
	typeREG1      = 0x9200
	typeREG2      = 0x9201
	typeREG3      = 0x9202
	typeRegErr    = 0x9210
	typeRegNGP    = 0x9211
	// typeRegNAK exists in the protocol (a receiver refusing a link outright)
	// but this receiver never needs it: every refusal here is either "no such
	// group" (NGP) or "not now" (ERR), and a sender treats both by retrying.
	typeRegNAK = 0x9212

	// srtControlACK is SRT's own ACK. It is the one upstream packet that goes
	// to every link rather than only the last active one -- see fanOut.
	srtControlACK = 0x8002

	// idLen is the group ID: 256 bytes, the first half chosen by the sender in
	// REG1 and the second half by this receiver.
	idLen = 256
	// regLen is the exact size of REG1 and REG2: two bytes of type, then an ID.
	// Exact, not minimum: a REG packet of any other length is not one a real
	// sender produced, and accepting a longer one would let a stranger make
	// the REG2 reply to REG1 the only "reply" here that is not equal-or-smaller
	// than what was asked -- see handleREG1.
	regLen = 2 + idLen

	// srtHeaderLen is the smallest legal SRT packet. Anything shorter that is
	// not an SRTLA control packet is not SRT either.
	srtHeaderLen = 16

	// ackBatch is how many data sequence numbers one SRTLA ACK carries, and
	// ackLen its size on the wire: a 4-byte header then ackBatch uint32s.
	ackBatch = 10
	ackLen   = 4 + 4*ackBatch

	// mtu caps every datagram read. Reads use a buffer one byte longer so a
	// datagram that did not fit is detected and dropped rather than silently
	// truncated and forwarded as a corrupted SRT packet -- Go's UDP reads do
	// not report truncation.
	mtu = 1500
)

// packetType returns the first sixteen bits of a datagram, or 0 when it is too
// short to have them. 0 is never a control type (the high bit is clear), so
// "too short" and "SRT data packet" both read as not-ours.
func packetType(b []byte) uint16 {
	if len(b) < 2 {
		return 0
	}
	return binary.BigEndian.Uint16(b)
}

// isSRTData reports whether b is an SRT data packet: high bit clear, and long
// enough to be SRT at all. Its first 32 bits are then the sequence number.
func isSRTData(b []byte) bool {
	return len(b) >= srtHeaderLen && b[0]&0x80 == 0
}

// isSRTACK reports whether b is SRT's ACK control packet.
func isSRTACK(b []byte) bool {
	return len(b) >= srtHeaderLen && packetType(b) == srtControlACK
}

// ctrl builds a bare two-byte SRTLA reply (REG3, REG_ERR, REG_NGP).
func ctrl(t uint16) []byte {
	return binary.BigEndian.AppendUint16(nil, t)
}

// regPacket builds REG1/REG2: the type, then the 256-byte ID.
func regPacket(t uint16, id *[idLen]byte) []byte {
	b := make([]byte, 0, regLen)
	b = binary.BigEndian.AppendUint16(b, t)
	return append(b, id[:]...)
}

// ackPacket builds an SRTLA ACK for one link's batch of sequence numbers. The
// header is the type in the top sixteen bits of a 32-bit word, low sixteen
// zero, so the packet is ackLen bytes of whole words.
func ackPacket(seqs []uint32) []byte {
	b := make([]byte, 0, ackLen)
	b = binary.BigEndian.AppendUint32(b, uint32(typeACK)<<16)
	for _, s := range seqs {
		b = binary.BigEndian.AppendUint32(b, s)
	}
	return b
}
