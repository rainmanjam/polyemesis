package clips

import "github.com/rainmanjam/polyemesis/internal/mpegts"

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
	// Adaptation-field flags, in the byte after the field length.
	afRandomAccess = 0x40
)

// demux is the running view of the transport stream.
type demux struct {
	pmtPID    uint16
	havePMT   bool
	videoPID  uint16
	haveVideo bool

	pat      [mpegts.PacketSize]byte
	havePAT  bool
	pmt      [mpegts.PacketSize]byte
	pmtCache bool
}

// observe folds one datagram into the running view.
func (d *demux) observe(dgram []byte) {
	for off := 0; off+mpegts.PacketSize <= len(dgram); off += mpegts.PacketSize {
		p := dgram[off : off+mpegts.PacketSize]
		// Losing sync means the rest of the datagram cannot be trusted to be
		// packet-aligned, so stop rather than guess.
		if p[0] != mpegts.SyncByte {
			return
		}
		pid := mpegts.PID(p)
		if pid == mpegts.NullPID || !mpegts.HasPayload(p) {
			continue
		}
		switch {
		case pid == mpegts.PIDPAT:
			copy(d.pat[:], p)
			d.havePAT = true
			if id, ok := mpegts.PATProgram(mpegts.SectionOf(p)); ok {
				d.pmtPID, d.havePMT = id, true
			}
		case d.havePMT && pid == d.pmtPID:
			copy(d.pmt[:], p)
			d.pmtCache = true
			if v, ok := videoPIDOf(mpegts.SectionOf(p)); ok {
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
	out := make([]byte, 0, 2*mpegts.PacketSize)
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
	for off := 0; off+mpegts.PacketSize <= len(dgram); off += mpegts.PacketSize {
		p := dgram[off : off+mpegts.PacketSize]
		if p[0] != mpegts.SyncByte {
			return 0, false
		}
		if mpegts.PID(p) != d.videoPID {
			continue
		}
		// payload_unit_start_indicator: without it this is the middle of an
		// access unit and starting here would truncate the picture.
		if p[1]&0x40 == 0 {
			continue
		}
		if !mpegts.HasAdaptation(p) || p[4] == 0 {
			continue
		}
		if p[5]&afRandomAccess != 0 {
			return off, true
		}
	}
	return 0, false
}

// videoPIDOf returns the first video elementary stream of a PMT section.
//
// A stream_type this build has never heard of leaves the video PID unknown,
// which makes a cut fall back to a plain packet boundary rather than refusing
// to produce one. That is the fail-open direction on purpose: a slightly ugly
// first second beats "your clip could not be created".
func videoPIDOf(sec []byte) (uint16, bool) {
	streams, _ := mpegts.PMTStreams(sec)
	for _, st := range streams {
		if mpegts.IsVideo(st.Type) {
			return st.PID, true
		}
	}
	return 0, false
}
