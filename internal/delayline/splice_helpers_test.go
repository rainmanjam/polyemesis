package delayline

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/mpegts"
)

// The byte-level helpers the splice is built from, pinned case by case: each
// is a step of a parser whose output reaches the wire.

func TestOpusFramesSplitsEveryControlHeaderShape(t *testing.T) {
	big := append([]byte{0x7F, 0xE0, 0xFF, 0x01, 0xF8}, bytes.Repeat([]byte{0xAA}, 255)...)
	cases := []struct {
		name string
		es   []byte
		want []audioFrame
		ok   bool
	}{
		{"one frame", []byte{0x7F, 0xE0, 0x03, 0xF8, 0xAA, 0xBB}, []audioFrame{{0, 6, 1800}}, true},
		{"two frames", []byte{0x7F, 0xE0, 0x01, 0xF8, 0x7F, 0xE0, 0x01, 0xF8},
			[]audioFrame{{0, 4, 1800}, {4, 4, 1800}}, true},
		{"au_size runs over 0xFF", big, []audioFrame{{0, 260, 1800}}, true},
		{"start and end trim", []byte{0x7F, 0xF8, 0x02, 0, 0, 0, 0, 0xF8, 0x00}, []audioFrame{{0, 9, 1800}}, true},
		{"control extension", []byte{0x7F, 0xE4, 0x02, 0x01, 0x55, 0xF8, 0x00}, []audioFrame{{0, 7, 1800}}, true},
		{"two frames per packet", []byte{0x7F, 0xE0, 0x01, 0x69}, []audioFrame{{0, 4, 3600}}, true},
		{"code 3 counts its frames", []byte{0x7F, 0xE0, 0x02, 0x03, 0x02}, []audioFrame{{0, 5, 1800}}, true},
		{"empty", nil, nil, false},
		{"no sync", []byte{0x7E, 0xE0, 0x01, 0xF8}, nil, false},
		{"short sync", []byte{0x7F}, nil, false},
		{"au_size runs off the end", []byte{0x7F, 0xE0, 0xFF}, nil, false},
		{"extension runs off the end", []byte{0x7F, 0xE4, 0x01}, nil, false},
		{"access unit runs off the end", []byte{0x7F, 0xE0, 0x05, 0xF8}, nil, false},
		{"empty access unit", []byte{0x7F, 0xE0, 0x00}, nil, false},
		{"code 3 without its count", []byte{0x7F, 0xE0, 0x01, 0xFB}, nil, false},
		{"good then bad", []byte{0x7F, 0xE0, 0x01, 0xF8, 0x00}, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := opusFrames(c.es)
			if ok != c.ok || !reflect.DeepEqual(got, c.want) {
				t.Fatalf("opusFrames = %v, %v; want %v, %v", got, ok, c.want, c.ok)
			}
		})
	}
}

// tsPkt is one TS packet on pid carrying payload, stuffed through its
// adaptation field.
func tsPkt(pid uint16, start bool, payload []byte) []byte {
	p := []byte{mpegts.SyncByte, byte(pid>>8) & 0x1F, byte(pid), 0x10}
	if start {
		p[1] |= 0x40
	}
	if n := 184 - len(payload); n > 0 {
		p[3] = 0x30
		p = append(p, byte(n-1))
		if n > 1 {
			p = append(p, 0x00)
			p = append(p, bytes.Repeat([]byte{0xFF}, n-2)...)
		}
	}
	return append(p, payload...)
}

func TestRewriteFirstVCLFindsTheFirstPictureNALAcrossPackets(t *testing.T) {
	const vpid, other = 0x100, 0x101
	hdr := []byte{0, 0, 1, 0xE0, 0, 0, 0x80, 0x80, 5, 0x21, 0, 1, 0, 1}
	cra := []byte{0, 0, 1, 21 << 1, 0x01}
	vps := []byte{0, 0, 1, 32 << 1, 0x01, 0xAA}
	join := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	cases := []struct {
		name  string
		held  []byte
		found bool
		at    int // index into held of the byte rewritten
	}{
		{"in the first packet", tsPkt(vpid, true, join(hdr, cra)), true, 188 - len(cra) + 3},
		{"after a parameter set", tsPkt(vpid, true, join(hdr, vps, cra)), true, 188 - len(cra) + 3},
		{"another PID first", join(tsPkt(other, true, join(hdr, cra)), tsPkt(vpid, true, join(hdr, cra))),
			true, 2*188 - len(cra) + 3},
		{"start code across packets", join(tsPkt(vpid, true, join(hdr, []byte{0, 0})), tsPkt(vpid, false, []byte{1, 21 << 1, 1})),
			true, 2*188 - 3 + 1},
		{"header across packets", join(tsPkt(vpid, true, hdr[:8]), tsPkt(vpid, false, join(hdr[8:], cra))),
			true, 2*188 - len(cra) + 3},
		{"no picture", tsPkt(vpid, true, join(hdr, vps)), false, -1},
		{"no PES start code", tsPkt(vpid, true, join([]byte{0, 0, 2}, hdr[3:], cra)), false, -1},
		{"header never completes", tsPkt(vpid, true, hdr[:8]), false, -1},
		{"continuation before any start", tsPkt(vpid, false, cra), false, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			held := append([]byte(nil), c.held...)
			found := rewriteFirstVCL(held, vpid, func(h byte) byte { return h&0x81 | 18<<1 })
			if found != c.found {
				t.Fatalf("found = %v, want %v", found, c.found)
			}
			want := append([]byte(nil), c.held...)
			if c.at >= 0 {
				want[c.at] = want[c.at]&0x81 | 18<<1
			}
			if !bytes.Equal(held, want) {
				t.Fatalf("held rewritten wrongly:\n got %x\nwant %x", held, want)
			}
		})
	}
}

// ptsHeader is a parsed PES header carrying pts and nothing else.
func ptsHeader(pts int64) *pesHeader {
	b := []byte{0, 0, 1, 0xE0, 0, 0, 0x80, 0x80, 5, 0x21, 0, 1, 0, 1}
	writeTS(b[9:], pts)
	return &pesHeader{parts: [2][]byte{b}, ok: true, hasPTS: true, hdrLen: len(b)}
}

func TestTheVideoGateDropsOnlyPicturesShownBeforeTheFloor(t *testing.T) {
	const floor = 900000
	noPTS := ptsHeader(0)
	noPTS.hasPTS = false
	cases := []struct {
		name            string
		r               pidRewrite
		h               *pesHeader
		gated, dropping bool
		left            int
	}{
		{name: "not gated", r: pidRewrite{floor: floor, left: 3}, h: ptsHeader(floor), left: 3},
		{name: "no header", r: pidRewrite{gated: true, floor: floor, left: 3}, gated: true, left: 3},
		{name: "at the floor", r: pidRewrite{gated: true, floor: floor, left: 3}, h: ptsHeader(floor),
			gated: true, dropping: true, left: 2},
		{name: "after the floor", r: pidRewrite{gated: true, dropping: true, floor: floor, left: 3}, h: ptsHeader(floor + 1), left: 2},
		{name: "sticky just after", r: pidRewrite{gated: true, dropping: true, sticky: true, floor: floor, left: 3},
			h: ptsHeader(floor + 1), gated: true, left: 2},
		{name: "sticky well after", r: pidRewrite{gated: true, sticky: true, floor: floor, left: 3},
			h: ptsHeader(floor + stickyClear + 1), left: 2},
		{name: "out of pictures", r: pidRewrite{gated: true, dropping: true, floor: floor}, h: ptsHeader(floor), left: -1},
		{name: "no timestamp", r: pidRewrite{gated: true, dropping: true, floor: floor, left: 3}, h: noPTS, left: 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := c.r
			r.gate(c.h)
			if r.gated != c.gated || r.dropping != c.dropping || r.left != c.left {
				t.Fatalf("gated, dropping, left = %v, %v, %d; want %v, %v, %d",
					r.gated, r.dropping, r.left, c.gated, c.dropping, c.left)
			}
		})
	}
}

func TestArmVideoGateFloorsAtTheLastPictureShownUnlessRecovering(t *testing.T) {
	m := &mark{pts: 5000, out: []pidSnap{
		{pid: 0x100, pidOut: pidOut{maxPTS: 7000, maxSeen: true}},
		{pid: 0x101, pidOut: pidOut{maxPTS: 9000, maxSeen: true}},
	}}
	for _, c := range []struct {
		name      string
		recovered bool
		floor     int64
	}{{"normal", false, 7000}, {"recovered or forced", true, 4999}} {
		t.Run(c.name, func(t *testing.T) {
			ts := tsState{codec: codecHEVC, videoPID: 0x100, rw: map[uint16]*pidRewrite{}}
			ts.armVideoGate(m, c.recovered)
			r := ts.rw[0x100]
			if r == nil || !r.gated || !r.sticky || !r.renumber || !r.awaitStart || r.left != gateLimit || r.floor != c.floor {
				t.Fatalf("video rewrite = %+v, want gated at %d", r, c.floor)
			}
		})
	}
	t.Run("no video codec", func(t *testing.T) {
		ts := tsState{codec: codecNone, videoPID: 0x100, rw: map[uint16]*pidRewrite{}}
		ts.armVideoGate(m, false)
		if len(ts.rw) != 0 {
			t.Fatalf("rw = %v, want nothing armed", ts.rw)
		}
	})
}

func TestCompletingWithoutATicketChangesNothing(t *testing.T) {
	var b Buffer
	b.completeTicketLocked(b.epoch, 0, 0, false, false)
	if b.resumed.Load() != nil {
		t.Fatal("a resume was published without a ticket")
	}
}
