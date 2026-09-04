package relay

import (
	"net"
	"sync"
	"testing"
)

// tsPacketOn builds one MPEG-TS packet on the given PID with the given
// continuity counter, which is what the hub's continuity measurement walks.
func tsPacketOn(pid int, cc uint8) []byte {
	p := make([]byte, tsPacketSize)
	p[0] = tsSyncByte
	p[1] = byte(pid >> 8 & 0x1f)
	p[2] = byte(pid & 0xff)
	// payload present, no adaptation field, plus the continuity counter
	p[3] = 0x10 | (cc & 0x0f)
	return p
}

// Two goroutines delivering at once must not race.
//
// This is not hypothetical. Deliver runs on srtserver's per-session read loop,
// and an SRT takeover deliberately overlaps two of them: closing the incumbent
// connection wakes its Read, but waking a goroutine is not the same as it
// having LEFT Deliver. The outgoing session can still be inside inspect()
// writing c.last[pid] while the incoming one enters it, and the field carried a
// comment saying it was touched by one goroutine only.
//
// Worth nothing without -race, which is how the suite runs it.
func TestConcurrentDeliverIsRaceFree(t *testing.T) {
	h := newTestHub(t)

	// A subscriber nothing is listening on, so every send fails and the
	// sendErrors counter -- the other unsynchronised write -- is exercised too.
	_, port := boundSubscriber(t)
	mustSubscribe(t, h, "ghost", port)

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				// Same PIDs from every goroutine: distinct ones would each own
				// their own slot and the write would not overlap.
				h.Deliver(tsPacketOn(256, uint8(i)))
				h.Deliver(tsPacketOn(257, uint8(i)))
			}
		}(g)
	}
	wg.Wait()

	// The counters still add up: nothing was lost to the serialisation.
	if got := h.Stats().RxPackets; got != 8*200*2 {
		t.Errorf("RxPackets = %d, want %d", got, 8*200*2)
	}
}

// A hub reached by BOTH its UDP socket and in-process injection has two
// writers, and run() has to take the same lock Deliver does.
func TestDeliverAndTheSocketReaderShareTheLock(t *testing.T) {
	h := newTestHub(t)
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: h.Port()})
	if err != nil {
		t.Fatalf("dial hub input: %v", err)
	}
	defer conn.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			h.Deliver(tsPacketOn(256, uint8(i)))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			if _, err := conn.Write(tsPacketOn(256, uint8(i))); err != nil {
				return
			}
		}
	}()
	wg.Wait()
}
