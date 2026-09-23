package relay

// maxPESPIDs bounds the table Hub.Wake draws on. A multitrack OBS publish is
// one video and up to six audio PIDs; a publisher that changes its PIDs between
// sessions adds a few more. Past the bound the oldest is forgotten, which costs
// at most a wake that misses a PID nobody is using any more.
const maxPESPIDs = 32

// emptyPES is the header Hub.Wake opens a PES with; byte 3, the stream_id, is
// filled in per PID.
var emptyPES = [9]byte{0, 0, 1, 0, 0, 0, 0x80, 0, 0}

// pesStarts is the set of PIDs on which a PES has been seen to begin, with the
// stream_id each carried: exactly what Hub.Wake needs to open a new PES on each
// of them. Guarded by Hub.deliverMu.
type pesStarts struct {
	pids []uint16
	// streamID is 0 for a PID never seen, which is unambiguous: every PES
	// stream_id is 0xBC or above.
	streamID [tsPIDs]uint8
}

// learn records the PES-carrying PIDs in one datagram. Cheap enough to run on
// every datagram beside continuity.inspect: a few branches per packet, and a
// store only when a PID or its stream_id is new.
func (t *pesStarts) learn(dgram []byte) {
	for off := 0; off+tsPacketSize <= len(dgram); off += tsPacketSize {
		p := dgram[off : off+tsPacketSize]
		if p[0] != tsSyncByte {
			return
		}
		// payload_unit_start_indicator, with a payload present.
		if p[1]&0x40 == 0 || p[3]&0x10 == 0 {
			continue
		}
		start := 4
		if p[3]&0x20 != 0 {
			start += 1 + int(p[4])
		}
		// A PES begins with the 00 00 01 start code. PSI sections -- the PAT and
		// PMT -- set the same bit and do not, and opening a "PES" on one would
		// hand the section parser garbage.
		if start+4 > tsPacketSize || p[start] != 0 || p[start+1] != 0 || p[start+2] != 1 {
			continue
		}
		pid := uint16(p[1]&0x1F)<<8 | uint16(p[2])
		id := p[start+3]
		if t.streamID[pid] == id {
			continue
		}
		if t.streamID[pid] == 0 {
			if len(t.pids) == maxPESPIDs {
				t.streamID[t.pids[0]] = 0
				t.pids = t.pids[1:]
			}
			t.pids = append(t.pids, pid)
		}
		t.streamID[pid] = id
	}
}

// wake builds the datagrams Hub.Wake sends: an empty PES start on every known
// PID, seven 188-byte packets to a datagram like everything else on the relay.
//
// seq is how many wakes this has already built for the same consumer. The
// counter the reader expects next moves on by one for every packet it has been
// sent, and the relay's own continuity state does not see these -- it counts
// what the PUBLISHER sent -- so a repeat would otherwise reuse the counter of
// the one before it, which the demuxer is entitled to drop as a duplicate.
func (t *pesStarts) wake(cc *continuity, seq uint8) [][]byte {
	var out [][]byte
	var cur []byte
	for _, pid := range t.pids {
		p := make([]byte, tsPacketSize)
		p[0] = tsSyncByte
		p[1] = 0x40 | byte(pid>>8)&0x1F
		p[2] = byte(pid)
		// adaptation_field_control 11: an adaptation field, then the payload.
		// continuity.last holds each PID's counter PLUS ONE, which is exactly
		// the next counter in its sequence, so the reader sees no discontinuity.
		p[3] = 0x30 | (cc.last[pid]+seq)&0x0F
		// THE STUFFING GOES IN THE ADAPTATION FIELD, NOT AFTER THE PES HEADER.
		// Bytes after the header are PES payload -- with a zero PES_packet_length
		// the demuxer cannot tell 0xFF filler from media -- so filling the tail
		// with 0xFF opened a 175-byte PES of garbage, and the NEXT wake, which
		// completes it, handed that garbage to the muxer as a frame. Stuffed in
		// the adaptation field instead, the payload is the header alone: the PES
		// it opens is empty, and an empty PES is never emitted however many
		// wakes follow.
		p[4] = byte(tsPacketSize - 5 - len(emptyPES))
		p[5] = 0 // no adaptation flags
		for i := 6; i < tsPacketSize-len(emptyPES); i++ {
			p[i] = 0xFF
		}
		// An empty PES header: start code, stream_id, PES_packet_length 0 (legal
		// for video, and harmless for audio since this PES carries nothing),
		// then the '10' marker bits, no flags and a zero header length.
		h := p[tsPacketSize-len(emptyPES):]
		copy(h, emptyPES[:])
		h[3] = t.streamID[pid]
		cur = append(cur, p...)
		if len(cur) == 7*tsPacketSize {
			out = append(out, cur)
			cur = nil
		}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}
