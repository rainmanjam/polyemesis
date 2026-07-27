package clips

import "testing"

// Synthetic transport stream, built to the same shapes FFmpeg's mpegts muxer
// emits: a PAT naming one program, a PMT naming an H.264 video PID and an AAC
// audio PID, and video packets that carry random_access_indicator on the ones
// a decoder may start at.

const (
	testPMTPID   uint16 = 0x1000
	testVideoPID uint16 = 0x0100
	testAudioPID uint16 = 0x0101
)

// tsPacket lays out one 188-byte packet.
func tsPacket(pid uint16, pusi bool, adaptation []byte, payload []byte) []byte {
	p := make([]byte, tsPacketSize)
	for i := range p {
		p[i] = 0xFF // stuffing, so an under-filled packet is still 188 bytes
	}
	p[0] = tsSyncByte
	p[1] = byte(pid >> 8 & 0x1F)
	if pusi {
		p[1] |= 0x40
	}
	p[2] = byte(pid & 0xFF)
	p[3] = 0x10 // payload present, continuity counter zero
	off := 4
	if adaptation != nil {
		p[3] |= 0x20
		p[4] = byte(len(adaptation))
		copy(p[5:], adaptation)
		off = 5 + len(adaptation)
	}
	copy(p[off:], payload)
	return p
}

func patPacket() []byte {
	sec := []byte{
		0x00,       // table_id
		0xB0, 0x0D, // section_syntax_indicator + length
		0x00, 0x01, // transport_stream_id
		0xC1, 0x00, 0x00, // version, section numbers
		0x00, 0x01, // program_number 1
		byte(0xE0 | testPMTPID>>8), byte(testPMTPID & 0xFF),
		0x00, 0x00, 0x00, 0x00, // CRC
	}
	return tsPacket(pidPAT, true, nil, append([]byte{0x00}, sec...))
}

func pmtPacket() []byte {
	sec := []byte{
		0x02,       // table_id
		0xB0, 0x17, // length
		0x00, 0x01, // program_number
		0xC1, 0x00, 0x00,
		byte(0xE0 | testVideoPID>>8), byte(testVideoPID & 0xFF), // PCR PID
		0xF0, 0x00, // program_info_length
		0x1B, byte(0xE0 | testVideoPID>>8), byte(testVideoPID & 0xFF), 0xF0, 0x00,
		0x0F, byte(0xE0 | testAudioPID>>8), byte(testAudioPID & 0xFF), 0xF0, 0x00,
		0x00, 0x00, 0x00, 0x00, // CRC
	}
	return tsPacket(testPMTPID, true, nil, append([]byte{0x00}, sec...))
}

// videoPacket returns a video packet; key marks it as a random-access point.
func videoPacket(key bool) []byte {
	if key {
		// adaptation field: flags byte with random_access_indicator set.
		return tsPacket(testVideoPID, true, []byte{afRandomAccess}, []byte{0x00, 0x00, 0x01, 0xE0})
	}
	return tsPacket(testVideoPID, false, nil, []byte{0x00})
}

func audioPacket() []byte {
	return tsPacket(testAudioPID, true, nil, []byte{0x00, 0x00, 0x01, 0xC0})
}

// datagram concatenates packets the way the relay hands them over.
func datagram(pkts ...[]byte) []byte {
	var out []byte
	for _, p := range pkts {
		out = append(out, p...)
	}
	return out
}

func TestDemuxLearnsTheVideoPIDFromThePATAndPMT(t *testing.T) {
	var d demux
	if d.haveVideo {
		t.Fatal("a fresh demux knows nothing")
	}
	d.observe(datagram(patPacket()))
	if !d.havePMT || d.pmtPID != testPMTPID {
		t.Fatalf("PMT PID = %#x (have=%v), want %#x", d.pmtPID, d.havePMT, testPMTPID)
	}
	d.observe(datagram(pmtPacket()))
	if !d.haveVideo || d.videoPID != testVideoPID {
		t.Fatalf("video PID = %#x (have=%v), want %#x", d.videoPID, d.haveVideo, testVideoPID)
	}
}

func TestDemuxIgnoresAPMTItWasNeverToldAbout(t *testing.T) {
	// The PMT arrives before the PAT, which is the ordinary case on joining a
	// stream mid-flight. Nothing may be learned from it until the PAT says
	// which PID it is.
	var d demux
	d.observe(datagram(pmtPacket()))
	if d.haveVideo {
		t.Fatal("a PMT on an unannounced PID must not be parsed")
	}
	d.observe(datagram(patPacket(), pmtPacket()))
	if !d.haveVideo {
		t.Fatal("once the PAT names the PMT PID, the video PID must follow")
	}
}

func TestRandomAccessFindsOnlyFlaggedVideoPackets(t *testing.T) {
	var d demux
	d.observe(datagram(patPacket(), pmtPacket()))

	tests := []struct {
		name    string
		dgram   []byte
		wantOff int
		wantOK  bool
	}{
		{
			name:  "a non-key video packet is not a start point",
			dgram: datagram(videoPacket(false), audioPacket()),
		},
		{
			name:  "an audio packet is never a start point",
			dgram: datagram(audioPacket(), audioPacket()),
		},
		{
			name:    "a flagged video packet is found at its own offset",
			dgram:   datagram(audioPacket(), videoPacket(false), videoPacket(true), audioPacket()),
			wantOff: 2 * tsPacketSize, wantOK: true,
		},
		{
			name:    "the first flagged packet wins",
			dgram:   datagram(videoPacket(true), videoPacket(true)),
			wantOff: 0, wantOK: true,
		},
		{
			name:  "a datagram that has lost sync is abandoned rather than guessed at",
			dgram: append([]byte{0x00, 0x01, 0x02}, videoPacket(true)...),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			off, ok := d.randomAccess(tc.dgram)
			if ok != tc.wantOK || (ok && off != tc.wantOff) {
				t.Fatalf("randomAccess = (%d, %v), want (%d, %v)", off, ok, tc.wantOff, tc.wantOK)
			}
		})
	}
}

func TestRandomAccessReportsNothingUntilTheVideoPIDIsKnown(t *testing.T) {
	// Fail open: an unknown video PID must make a cut fall back to a packet
	// boundary, never claim a start point it cannot have identified.
	var d demux
	if _, ok := d.randomAccess(datagram(videoPacket(true))); ok {
		t.Fatal("a demux with no PMT must not claim to have found a keyframe")
	}
}

func TestPSIIsReEmittedAtTheHeadOfACut(t *testing.T) {
	var d demux
	if d.psi() != nil {
		t.Fatal("nothing to re-emit before a PAT has been seen")
	}
	d.observe(datagram(patPacket(), pmtPacket()))
	psi := d.psi()
	if len(psi) != 2*tsPacketSize {
		t.Fatalf("psi = %d bytes, want the PAT and the PMT", len(psi))
	}
	if packetPID(psi[:tsPacketSize]) != pidPAT || packetPID(psi[tsPacketSize:]) != testPMTPID {
		t.Fatal("the PAT must come before the PMT, or a player cannot follow the pointer")
	}
}
