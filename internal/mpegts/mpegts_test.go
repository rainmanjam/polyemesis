package mpegts

import "testing"

func pkt(pid uint16, pusi bool, afc byte, body ...byte) []byte {
	p := make([]byte, PacketSize)
	p[0] = SyncByte
	p[1] = byte(pid >> 8)
	if pusi {
		p[1] |= 0x40
	}
	p[2] = byte(pid)
	p[3] = afc << 4
	copy(p[4:], body)
	return p
}

func patSection(prog, pmt uint16) []byte {
	return []byte{0x00, 0xB0, 0x11, 0, 1, 0xC1, 0, 0,
		0, 0, 0xE0, 0x10, // NIT, skipped
		byte(prog >> 8), byte(prog), 0xE0 | byte(pmt>>8), byte(pmt),
		0, 0, 0, 0}
}

func pmtSection(streams ...Stream) []byte {
	b := []byte{0x02, 0xB0, 0, 0, 1, 0xC1, 0, 0, 0xE1, 0x00, 0xF0, 0x00}
	for _, s := range streams {
		b = append(b, s.Type, 0xE0|byte(s.PID>>8), byte(s.PID), 0xF0, 0x00)
	}
	b = append(b, 0, 0, 0, 0)
	b[2] = byte(len(b) - 3)
	return b
}

func TestPacketHelpers(t *testing.T) {
	p := pkt(0x1234, true, 3, 5, 0x40)
	if PID(p) != 0x1234 || !PUSI(p) || !HasPayload(p) || !HasAdaptation(p) {
		t.Fatalf("header fields misread: %x", p[:6])
	}
	if got := PayloadStart(p); got != 4+1+5 {
		t.Fatalf("PayloadStart = %d", got)
	}
	if PayloadStart(pkt(1, false, 2, 10)) != PacketSize {
		t.Fatal("adaptation-only packet has a payload")
	}
	if PayloadStart(pkt(1, false, 3, 250)) != PacketSize {
		t.Fatal("oversized adaptation field did not clamp")
	}
}

func TestValidAdaptation(t *testing.T) {
	if !ValidAdaptation(pkt(1, false, 1)) || !ValidAdaptation(pkt(1, false, 3, 100)) {
		t.Fatal("valid packets rejected")
	}
	if ValidAdaptation(pkt(1, false, 3, 200)) {
		t.Fatal("adaptation field longer than the packet accepted")
	}
}

func TestSectionOfIsStrict(t *testing.T) {
	sec := patSection(1, 0x100)
	good := pkt(PIDPAT, true, 1, append([]byte{0}, sec...)...)
	if got := SectionOf(good); len(got) == 0 || got[0] != 0 {
		t.Fatalf("section not found: %v", got)
	}
	for name, p := range map[string][]byte{
		"no PUSI":        pkt(PIDPAT, false, 1, 0),
		"no payload":     pkt(PIDPAT, true, 2, 0),
		"bad adaptation": pkt(PIDPAT, true, 3, 250),
		"short":          good[:100],
	} {
		if SectionOf(p) != nil {
			t.Errorf("%s: want nil", name)
		}
	}
	// A pointer_field that runs off the packet.
	p := pkt(PIDPAT, true, 1, 250)
	p[PacketSize-1] = 0
	if SectionOf(p) != nil {
		t.Error("pointer past the end: want nil")
	}
	// A packet whose payload starts at the very end.
	if SectionOf(pkt(PIDPAT, true, 3, 183)) != nil {
		t.Error("payload at end of packet: want nil")
	}
}

func TestPATProgramSkipsTheNetworkEntry(t *testing.T) {
	if pid, ok := PATProgram(patSection(1, 0x100)); !ok || pid != 0x100 {
		t.Fatalf("PATProgram = %#x, %v", pid, ok)
	}
	if _, ok := PATProgram([]byte{0x01, 0, 0}); ok {
		t.Fatal("non-PAT accepted")
	}
	onlyNIT := patSection(0, 0x10)
	if _, ok := PATProgram(onlyNIT); ok {
		t.Fatal("a PAT with only the NIT has no program")
	}
}

func TestPMTStreams(t *testing.T) {
	want := []Stream{{0x101, 0x0F}, {0x100, StreamTypeH264}}
	got, ok := PMTStreams(pmtSection(want...))
	if !ok || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("PMTStreams = %v, %v", got, ok)
	}
	if _, ok := PMTStreams([]byte{0x03, 0, 0}); ok {
		t.Fatal("non-PMT accepted")
	}
	short := pmtSection(want...)
	if got, _ := PMTStreams(short[:len(short)-4]); len(got) != 1 {
		t.Fatalf("truncated section: %v", got)
	}
	if SectionLen(short) != len(short) {
		t.Fatal("SectionLen wrong")
	}
}

func TestIsVideo(t *testing.T) {
	for _, st := range []byte{0x01, 0x02, 0x10, 0x1B, 0x24, 0x33, 0x42, 0xD1, 0xEA} {
		if !IsVideo(st) {
			t.Errorf("%#x not video", st)
		}
	}
	if IsVideo(0x0F) || IsVideo(0x03) {
		t.Error("audio counted as video")
	}
}
