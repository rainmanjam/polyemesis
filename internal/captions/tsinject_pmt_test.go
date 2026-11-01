package captions

import (
	"testing"
)

// pmtWithVideoAt is pmtSection with the video ES on pid.
func pmtWithVideoAt(typ byte, pid uint16) []byte {
	s := pmtSection(typ, 0x0F)
	s[13] = 0xE0 | byte(pid>>8)
	s[14] = byte(pid)
	return s
}

// TestPSIIsReadOnlyFromAPacketThatStartsASectionAndCarriesAPayload. Since a PMT
// re-points the video PID every time it is read, a packet read as a PMT that is
// not one can now move the video away from the stream that is really there. A
// packet whose adaptation_field_control says "adaptation field only" has no
// payload at all; its bytes after the field are not a section, however much
// they look like one. A packet without payload_unit_start has no pointer field.
// Neither may be parsed as PSI.
//
// MUTATION, internal/captions/tsinject.go: drop the payload-flag check in
// sectionOf. Observed: FAIL -- sectionOf read 181 bytes of PSI from the
// adaptation-only packet.
func TestPSIIsReadOnlyFromAPacketThatStartsASectionAndCarriesAPayload(t *testing.T) {
	inj := NewInjector(NewSchedule(0))
	inj.parsePMT(psiPacket(testPMTPID, 0, pmtSection(streamTypeH264, 0x0F)))
	if inj.videoPID != testVideoPID {
		t.Fatalf("setup: videoPID = %#x, want %#x", inj.videoPID, testVideoPID)
	}

	// Adaptation field only (0x20), a one-byte field, then what reads as a pointer
	// field and a PMT moving video to 0x200.
	bogus := make([]byte, tsPacketSize)
	bogus[0] = tsSyncByte
	bogus[1] = 0x40 | byte(testPMTPID>>8)
	bogus[2] = byte(testPMTPID & 0xFF)
	bogus[3] = 0x20
	bogus[4] = 1
	bogus[5] = 0x00
	bogus[6] = 0x00 // "pointer field"
	copy(bogus[7:], pmtWithVideoAt(streamTypeH264, 0x0200))
	if s := sectionOf(bogus); s != nil {
		t.Fatalf("sectionOf read %d bytes of PSI from a packet with no payload", len(s))
	}
	inj.Write(bogus)
	if inj.videoPID != testVideoPID {
		t.Fatalf("videoPID = %#x after an adaptation-only packet, want it left at %#x", inj.videoPID, testVideoPID)
	}

	cont := psiPacket(testPMTPID, 1, pmtWithVideoAt(streamTypeH264, 0x0200))
	cont[1] &^= 0x40 // not a section start
	if s := sectionOf(cont); s != nil {
		t.Fatalf("sectionOf read PSI from a packet without payload_unit_start")
	}
}

// TestAPMTThatMovesVideoToHEVCStopsTheOldH264PIDBeingRewritten. The PMT is the
// authority for both PIDs. When a reconnect brings video back as HEVC, the old
// H.264 PID must stop being treated as video (it used to stay set, "first wins"
// for HEVC and a no-op for H.264), the HEVC PID must be the new one, and it is
// counted once per change, not once per repeated PMT.
//
// MUTATION, internal/captions/tsinject.go in parsePMT: keep the old videoPID
// when the map has no H.264 stream. Observed: FAIL -- videoPID stayed 0x100.
func TestAPMTThatMovesVideoToHEVCStopsTheOldH264PIDBeingRewritten(t *testing.T) {
	inj := NewInjector(NewSchedule(0))
	inj.parsePMT(psiPacket(testPMTPID, 0, pmtSection(streamTypeH264, 0x0F)))
	for i := 0; i < 3; i++ {
		inj.parsePMT(psiPacket(testPMTPID, uint8(i+1), pmtWithVideoAt(streamTypeHEVC, 0x0200)))
	}
	if inj.videoPID != 0 {
		t.Fatalf("videoPID = %#x after the PMT moved video to HEVC, want 0 (nothing to rewrite)", inj.videoPID)
	}
	if inj.hevcPID != 0x0200 {
		t.Fatalf("hevcPID = %#x, want 0x200", inj.hevcPID)
	}
	if got := inj.Stats().Unsupported; got != 1 {
		t.Fatalf("Unsupported = %d after one HEVC PMT repeated three times, want 1", got)
	}
	inj.parsePMT(psiPacket(testPMTPID, 4, pmtSection(streamTypeH264, 0x0F)))
	if inj.videoPID != testVideoPID || inj.hevcPID != 0 {
		t.Fatalf("after H.264 came back: videoPID = %#x, hevcPID = %#x; want %#x and 0", inj.videoPID, inj.hevcPID, testVideoPID)
	}
}

// TestAccessUnitsHeldWhenTheVideoPIDMovesGoOutOnTheirOwnPID. The rebuilt packets
// are stamped with the current video PID, so units still held when a PMT moved
// video were sent on the NEW PID -- or, after a move to HEVC, on PID 0, which is
// the PAT. They are released on their own PID before the PMT that moved it, and
// the PMT itself still goes out.
//
// MUTATION, internal/captions/tsinject.go in parsePMT: drop the release before
// re-pointing the PID. Observed: FAIL -- 9 packets went out on PID 0.
func TestAccessUnitsHeldWhenTheVideoPIDMovesGoOutOnTheirOwnPID(t *testing.T) {
	var in []byte
	in = append(in, psFixture(streamTypeH264)...)
	var cc uint8
	const units = 4
	for d := 0; d < units; d++ {
		pes := append(pesHeader(int64(90000+d*frameTicks)), auFor(d == 0)...)
		in = append(in, pesPackets(testVideoPID, &cc, pes, nil)...)
	}
	in = append(in, psiPacket(testPMTPID, 1, pmtWithVideoAt(streamTypeHEVC, 0x0200))...)

	inj := NewInjector(NewSchedule(0))
	out := append(inj.Write(in), inj.Flush()...)
	pkts := parsePackets(t, out)
	pmts, pats := 0, 0
	for _, p := range pkts {
		switch p.pid {
		case 0:
			pats++
		case testPMTPID:
			pmts++
		case testVideoPID:
		default:
			t.Fatalf("a packet went out on PID %#x, which the input never used", p.pid)
		}
	}
	if pats != 1 {
		t.Fatalf("%d packets on PID 0, want only the one PAT: held video was stamped with PID 0", pats)
	}
	if pmts != 2 {
		t.Fatalf("%d PMT packets out, want both", pmts)
	}
	if got := len(videoUnits(t, pkts, testVideoPID)); got != units {
		t.Fatalf("%d video units out on the old PID, want %d", got, units)
	}
	// The released units come before the PMT that moved the PID.
	lastVideo, secondPMT := -1, -1
	seen := 0
	for i, p := range pkts {
		if p.pid == testVideoPID {
			lastVideo = i
		}
		if p.pid == testPMTPID {
			seen++
			if seen == 2 {
				secondPMT = i
			}
		}
	}
	if lastVideo > secondPMT {
		t.Fatalf("held video went out after the PMT that moved it (video at %d, PMT at %d)", lastVideo, secondPMT)
	}
}
