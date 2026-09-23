package relay

import (
	"bytes"
	"testing"
	"time"
)

// pesStartOn is a TS packet that begins a PES with the given stream_id: the
// shape Hub.Wake learns a PID from.
func pesStartOn(pid int, cc, streamID uint8) []byte {
	p := tsPacketOn(pid, cc)
	p[1] |= 0x40 // payload_unit_start_indicator
	copy(p[4:], []byte{0, 0, 1, streamID, 0, 0, 0x80, 0, 0})
	return p
}

// patOn is a PSI section start. It sets payload_unit_start exactly as a PES
// does, and Wake must not mistake it for one: opening a "PES" on the PAT's PID
// would hand the section parser garbage.
func patOn(cc uint8) []byte {
	p := tsPacketOn(0, cc)
	p[1] |= 0x40
	p[4] = 0x00 // pointer_field
	p[5] = 0x00 // table_id: program_association_section
	return p
}

// wakePacket is what the reader should get for one PID: payload_unit_start, the
// next continuity counter, an adaptation field that swallows the stuffing, and
// a payload that is the empty PES header and nothing else.
func assertWakePacket(t *testing.T, p []byte, pid int, cc, streamID uint8) {
	t.Helper()
	if p[0] != tsSyncByte {
		t.Fatalf("not a TS packet: % x", p[:4])
	}
	if got := int(p[1]&0x1F)<<8 | int(p[2]); got != pid {
		t.Errorf("PID %#x, want %#x", got, pid)
	}
	if p[1]&0x40 == 0 {
		t.Errorf("PID %#x: payload_unit_start not set, so no held PES is completed", pid)
	}
	if got := p[3] & 0x0F; got != cc {
		t.Errorf("PID %#x: continuity counter %d, want %d -- the reader would log a "+
			"discontinuity, or drop the packet as a duplicate", pid, got, cc)
	}
	if p[3]&0x30 != 0x30 {
		t.Errorf("PID %#x: adaptation_field_control %#x, want both an adaptation field and "+
			"a payload", pid, p[3]>>4&3)
	}
	// The payload is whatever follows the adaptation field. Anything past the
	// 9-byte header is PES payload the muxer would be handed as media.
	payload := p[5+int(p[4]):]
	want := []byte{0, 0, 1, streamID, 0, 0, 0x80, 0, 0}
	if !bytes.Equal(payload, want) {
		t.Errorf("PID %#x: payload % x, want exactly the empty PES header % x -- any byte "+
			"after it is a frame of garbage once the next wake completes this PES",
			pid, payload, want)
	}
}

// A stop on a quiet feed is woken with one empty PES start per PES-carrying
// PID, sent to the stopping consumer ALONE and only into silence.
//
// The mechanism is argued at Hub.Wake; this pins the bytes, because every one
// of them decides whether FFmpeg finishes cleanly or muxes junk. The end-to-end
// consequence is engine.TestARecorderStoppedOnAQuietFeedFinalisesItsFileInsteadOfBeingKilled.
func TestWakeSendsTheStoppingConsumerAnEmptyPESStartOnEachPIDOnlyIntoSilence(t *testing.T) {
	h := newTestHub(t)
	stopping, sp := boundSubscriber(t)
	running, rp := boundSubscriber(t)
	if _, err := h.Subscribe("stopping", sp); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Subscribe("running", rp); err != nil {
		t.Fatal(err)
	}

	var d []byte
	d = append(d, patOn(3)...)
	d = append(d, pesStartOn(videoPID, 5, 0xE0)...)
	d = append(d, pesStartOn(audioPID, 15, 0xC0)...)
	h.Deliver(d)
	receive(t, stopping, 1)
	receive(t, running, 1)

	if h.Wake("stopping") {
		t.Fatal("Wake sent into a feed that delivered a moment ago. A live feed wakes " +
			"its readers by itself, and a wake injected into it cuts the PES in flight short")
	}

	time.Sleep(quietAfter + 50*time.Millisecond)
	if h.Wake("nobody") {
		t.Error("Wake reported sending to a name the hub does not hold")
	}
	for i, cc := range [][2]uint8{{6, 0}, {7, 1}} {
		if !h.Wake("stopping") {
			t.Fatalf("wake %d: nothing was sent into a quiet feed", i+1)
		}
		got := receive(t, stopping, 1)
		if len(got) != 1 {
			t.Fatalf("wake %d: the stopping consumer got %d datagrams, want 1", i+1, len(got))
		}
		if len(got[0]) != 2*tsPacketSize {
			t.Fatalf("wake %d: %d bytes, want two packets -- one per PES PID, none for "+
				"the PAT", i+1, len(got[0]))
		}
		// The second wake continues where the first left off, not where the
		// publisher did.
		assertWakePacket(t, got[0][:tsPacketSize], videoPID, cc[0], 0xE0)
		assertWakePacket(t, got[0][tsPacketSize:], audioPID, cc[1], 0xC0)
	}
	if got := receiveWithin(t, running, 1, 150*time.Millisecond); len(got) != 0 {
		t.Errorf("a consumer that is still running was sent the wake: % x", got[0][:8])
	}
}

// Before any PES has been seen there is nothing to complete -- and FFmpeg is
// still probing, where a single SIGTERM does interrupt the read.
func TestWakeSendsNothingBeforeAnyPESHasBeenSeen(t *testing.T) {
	h := newTestHub(t)
	c, port := boundSubscriber(t)
	if _, err := h.Subscribe("stopping", port); err != nil {
		t.Fatal(err)
	}
	h.Deliver(patOn(0))
	receive(t, c, 1)
	time.Sleep(quietAfter + 50*time.Millisecond)
	if h.Wake("stopping") {
		t.Error("Wake sent packets for a stream in which no PES has begun")
	}
}
