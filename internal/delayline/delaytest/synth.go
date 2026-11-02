// Package delaytest builds synthetic MPEG-TS for testing the broadcast delay's
// dump: numbered H.264 or HEVC pictures and AAC-shaped audio frames, muxed in
// decode order with PSI before every key picture, each packet stamped with the
// time a live relay would deliver it. Every picture's slice carries its
// number, so a test reading the output back can say exactly which pictures a
// splice kept.
package delaytest

import (
	"encoding/binary"
	"time"
)

const (
	packetSize = 188
	syncByte   = 0x47
)

const (
	VideoPID   = 0x100
	AudioPID   = 0x101
	PMTPID     = 0x1000
	FrameTicks = 3000 // 30 fps at 90 kHz
	AudioTicks = 1920 // 1024 samples at 48 kHz
	PTSBase    = 900000
)

// NAL unit types the synthetic stream can carry as a picture's first slice.
const (
	H264IDR      = 5
	H264NonIDR   = 1
	HEVCIDRWRADL = 19
	HEVCCRA      = 21
	HEVCTrail    = 1
)

// Synth builds a TS of numbered pictures and audio frames.
type Synth struct {
	HEVC bool
	GOP  int
	// keyType is the NAL type of a GOP's first picture.
	KeyType byte
	// override, when it returns ok, replaces the NAL type of picture n.
	Override func(n int) (byte, bool)
	// pictureBytes is a non-key picture's size; a key picture is 3x.
	PictureBytes int
	// audioLag, when set, muxes audio frame m that much after its PTS: the
	// interleave jitter a real muxer has.
	AudioLag func(m int) time.Duration
	// Origin, when set, is picture 0's DTS and audio frame 0's PTS in place
	// of PTSBase: set it near 2^33 to put the 33-bit wrap in the run.
	Origin int64
	// StepFrom, when set, steps every timestamp of programme from that time
	// on by Step ticks: a source clock jumping, as an encoder reconnecting
	// without the selector in front does.
	StepFrom time.Duration
	Step     int64
	// Leading, on an HEVC stream, follows every key picture with a RADL
	// picture: decoded after it and shown before it. Its slice carries
	// LeadingIndex plus the key's number.
	Leading bool
	cc      map[uint16]uint8
}

// New is a Synth with GOP pictures per GOP and IDR key pictures.
func New(gop int) *Synth {
	return &Synth{GOP: gop, KeyType: H264IDR, PictureBytes: 1000, cc: map[uint16]uint8{}}
}

func (g *Synth) nalType(n int) byte {
	if g.Override != nil {
		if t, ok := g.Override(n); ok {
			return t
		}
	}
	if n%g.GOP == 0 {
		return g.KeyType
	}
	if g.HEVC {
		return HEVCTrail
	}
	return H264NonIDR
}

func (g *Synth) nextCC(pid uint16) uint8 {
	c := g.cc[pid]
	g.cc[pid] = (c + 1) & 0x0F
	return c
}

func putTS(b []byte, prefix byte, v int64) {
	b[0] = prefix<<4 | byte(v>>29)&0x0E | 1
	b[1] = byte(v >> 22)
	b[2] = byte(v>>14) | 1
	b[3] = byte(v >> 7)
	b[4] = byte(v<<1) | 1
}

// packetize splits one PES (or PSI section, with its pointer field) into TS
// packets. pcr < 0 means none.
func (g *Synth) packetize(pid uint16, payload []byte, pcr int64, rai bool) []byte {
	var out []byte
	first := true
	for len(payload) > 0 || first {
		var af []byte
		if first && (pcr >= 0 || rai) {
			flags := byte(0)
			if rai {
				flags |= 0x40
			}
			af = []byte{flags}
			if pcr >= 0 {
				af[0] |= 0x10
				af = append(af, byte(pcr>>25), byte(pcr>>17), byte(pcr>>9), byte(pcr>>1), byte(pcr<<7)|0x7E, 0)
			}
		}
		room := 184
		if af != nil {
			room -= 1 + len(af)
		}
		n := len(payload)
		if n > room {
			n = room
		}
		if n < room {
			// Stuff through the adaptation field.
			total := 184 - n // bytes of adaptation field incl. its length byte
			if af == nil && total >= 2 {
				af = []byte{0}
			}
			if af != nil {
				for len(af)+1 < total {
					af = append(af, 0xFF)
				}
			}
		}
		p := make([]byte, 4, packetSize)
		p[0] = syncByte
		p[1] = byte(pid >> 8)
		if first {
			p[1] |= 0x40
		}
		p[2] = byte(pid)
		afc := byte(0x10)
		switch {
		case af != nil:
			afc = 0x30
			p = append(p, byte(len(af)))
			p = append(p, af...)
		case 184-n == 1:
			afc = 0x30
			p = append(p, 0)
		}
		p[3] = afc | g.nextCC(pid)
		p = append(p, payload[:n]...)
		if len(p) != packetSize {
			panic("packetize: bad packet size")
		}
		out = append(out, p...)
		payload = payload[n:]
		first = false
	}
	return out
}

func (g *Synth) psi() []byte {
	pat := []byte{0, 0x00, 0xB0, 0x0D, 0x00, 0x01, 0xC1, 0x00, 0x00,
		0x00, 0x01, 0xE0 | PMTPID>>8, PMTPID & 0xFF, 0, 0, 0, 0}
	st := byte(0x1B)
	if g.HEVC {
		st = 0x24
	}
	pmt := []byte{0, 0x02, 0xB0, 0x17, 0x00, 0x01, 0xC1, 0x00, 0x00,
		0xE0 | VideoPID>>8, VideoPID & 0xFF, 0xF0, 0x00,
		st, 0xE0 | VideoPID>>8, VideoPID & 0xFF, 0xF0, 0x00,
		0x0F, 0xE0 | AudioPID>>8, AudioPID & 0xFF, 0xF0, 0x00,
		0, 0, 0, 0}
	return append(g.packetize(0, pat, -1, false), g.packetize(PMTPID, pmt, -1, false)...)
}

func (g *Synth) nal(t byte) []byte {
	if g.HEVC {
		return []byte{0, 0, 1, t << 1, 0x01}
	}
	return []byte{0, 0, 1, 0x60 | t}
}

// picture is one video PES: AUD, parameter sets on a key picture, then one
// slice carrying the picture's number.
func (g *Synth) picture(n int) []byte {
	t := g.nalType(n)
	es := []byte{0, 0, 0, 1, 0x09, 0xF0}
	if g.HEVC {
		es = []byte{0, 0, 0, 1, 0x46, 0x01, 0x50}
	}
	key := n%g.GOP == 0
	if key {
		if g.HEVC {
			es = append(es, 0, 0, 0, 1, 0x40, 0x01, 0xAA, 0, 0, 0, 1, 0x42, 0x01, 0xAA, 0, 0, 0, 1, 0x44, 0x01, 0xAA)
		} else {
			es = append(es, 0, 0, 0, 1, 0x67, 0x64, 0x00, 0x1F, 0xAC, 0, 0, 0, 1, 0x68, 0xEE, 0x3C, 0x80)
		}
	}
	es = append(es, g.nal(t)...)
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], uint32(n))
	es = append(es, idx[:]...)
	size := g.PictureBytes
	if key {
		size *= 3
	}
	for len(es) < size {
		es = append(es, 0xAB)
	}
	pts := g.at(int64(n * FrameTicks))
	hdr := []byte{0, 0, 1, 0xE0, 0, 0, 0x80, 0xC0, 10, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	putTS(hdr[9:], 3, (pts+FrameTicks)&tsMask) // a B-frame-shaped stream: PTS one frame after DTS
	putTS(hdr[14:], 1, pts)
	return g.packetize(VideoPID, append(hdr, es...), pts, key)
}

func (g *Synth) audioFrame(m int) []byte {
	pts := g.at(int64(m * AudioTicks))
	es := make([]byte, 0, 400)
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], uint32(m))
	es = append(es, idx[:]...)
	for len(es) < 400 {
		es = append(es, 0xCD)
	}
	hdr := []byte{0, 0, 1, 0xC0, 0, 0, 0x80, 0x80, 5, 0, 0, 0, 0, 0}
	n := len(es) + 8
	hdr[4], hdr[5] = byte(n>>8), byte(n)
	putTS(hdr[9:], 2, pts)
	return g.packetize(AudioPID, append(hdr, es...), -1, false)
}

// Stream is a muxed run: packets in order, each with its arrival time.
type Stream struct {
	Pkts  [][]byte
	Times []time.Duration
}

// LeadingIndex marks a leading picture's number: LeadingIndex + the key's.
const LeadingIndex = 1_000_000

// HEVCRADL is the NAL type a leading picture carries.
const HEVCRADL = 7

const tsMask = int64(1)<<33 - 1

func (g *Synth) origin() int64 {
	if g.Origin != 0 {
		return g.Origin
	}
	return PTSBase
}

// at is the timestamp of programme time t ticks in, stepped if asked.
func (g *Synth) at(ticks int64) int64 {
	v := g.origin() + ticks
	if g.StepFrom > 0 && Ticks(ticks) >= g.StepFrom {
		v += g.Step
	}
	return v & tsMask
}

// leading is the RADL picture after key picture n: DTS half a frame after the
// key's, PTS half a frame before the key's.
func (g *Synth) leading(n int) []byte {
	es := []byte{0, 0, 0, 1, 0x46, 0x01, 0x50}
	es = append(es, g.nal(HEVCRADL)...)
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], uint32(LeadingIndex+n))
	es = append(es, idx[:]...)
	for len(es) < g.PictureBytes {
		es = append(es, 0xAB)
	}
	dts := (g.origin() + int64(n*FrameTicks) + FrameTicks/2) & tsMask
	pts := (g.origin() + int64(n*FrameTicks) + FrameTicks/2) & tsMask
	hdr := []byte{0, 0, 1, 0xE0, 0, 0, 0x80, 0xC0, 10, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	putTS(hdr[9:], 3, pts)
	putTS(hdr[14:], 1, dts)
	return g.packetize(VideoPID, append(hdr, es...), -1, false)
}

// Ticks converts 90 kHz ticks to a duration.
func Ticks(t int64) time.Duration { return time.Duration(t) * time.Second / 90000 }

// Mux produces the stream for [0, dur) in decode order, PSI before every key.
func (g *Synth) Mux(dur time.Duration) *Stream {
	s := &Stream{}
	add := func(b []byte, at time.Duration) {
		for off := 0; off < len(b); off += packetSize {
			s.Pkts = append(s.Pkts, b[off:off+packetSize])
			s.Times = append(s.Times, at)
		}
	}
	v, a := 0, 0
	for {
		vt, at := Ticks(int64(v*FrameTicks)), Ticks(int64(a*AudioTicks))
		if g.AudioLag != nil {
			at += g.AudioLag(a)
		}
		if vt >= dur && at >= dur {
			return s
		}
		if vt <= at {
			if v%g.GOP == 0 {
				add(g.psi(), vt)
			}
			add(g.picture(v), vt)
			if g.Leading && g.HEVC && v%g.GOP == 0 {
				add(g.leading(v), vt)
			}
			v++
		} else {
			add(g.audioFrame(a), at)
			a++
		}
	}
}
