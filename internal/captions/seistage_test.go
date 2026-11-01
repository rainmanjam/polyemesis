package captions

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// feedInChunks pushes a whole stream through the stage the way the relay does,
// in 1316-byte datagrams, and gathers everything the stage hands back.
func feedInChunks(s *SEIStage, stream []byte, before func(fed int)) []byte {
	var out []byte
	fed := 0
	for off := 0; off < len(stream); off += datagramBytes {
		end := off + datagramBytes
		if end > len(stream) {
			end = len(stream)
		}
		if before != nil {
			before(fed)
		}
		for _, d := range s.Feed(stream[off:end]) {
			out = append(out, d...)
		}
		fed++
	}
	for _, d := range s.Flush() {
		out = append(out, d...)
	}
	return out
}

// TestSEIStageCarriesACueQueuedFromAnotherGoroutineIntoTheStreamAsCaptionSEI is
// the end-to-end contract of the stage: a caption handed in while the stream is
// running (from the CueRouter's goroutine, not the feeding one) comes out the
// other side as CEA-608 pairs on the pictures that are live at that moment.
func TestSEIStageCarriesACueQueuedFromAnotherGoroutineIntoTheStreamAsCaptionSEI(t *testing.T) {
	// Forty pictures, not ten: a caption takes one slot per picture, and a slot
	// only finalises once the reorder window behind it has filled, so a short
	// stream would end before the whole pop-on script had been placed.
	decode := make([]int, 40)
	for i := range decode {
		decode[i] = i
	}
	stream := streamOf(decode)
	s := NewSEIStage()

	queued := false
	out := feedInChunks(s, stream, func(fed int) {
		// Queue the caption part way through, once a picture has been seen.
		if fed == 2 && !queued {
			queued = true
			done := make(chan bool)
			go func() { done <- s.Cue("HI", 10*time.Second) }()
			if !<-done {
				t.Errorf("Cue from another goroutine was refused on an empty queue")
			}
		}
	})

	var got []Pair
	for _, u := range videoUnits(t, parsePackets(t, out), testVideoPID) {
		for _, nal := range splitAnnexB(u.es) {
			if p, ok := ccPairOf(nal); ok && p != nullPair {
				got = append(got, p)
			}
		}
	}
	want := PopOnScript("HI")
	if len(got) < len(want) {
		t.Fatalf("caption pairs after the cue = %d, want at least %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("caption pair %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestSEIStageHandsBackDatagramsNoLargerThanSevenTSPacketsAndOnlyWholePackets
// is the shape the private hub needs. relay.Hub reads into a 2048-byte buffer
// and counts continuity per 188-byte packet, so an output datagram that is
// longer, or not a whole number of packets, would be truncated or misread.
func TestSEIStageHandsBackDatagramsNoLargerThanSevenTSPacketsAndOnlyWholePackets(t *testing.T) {
	stream := streamOf([]int{0, 1, 2, 3, 4, 5, 6, 7})
	s := NewSEIStage()
	s.Cue("HELLO", 5*time.Second)
	var total int
	for off := 0; off < len(stream); off += datagramBytes {
		end := off + datagramBytes
		if end > len(stream) {
			end = len(stream)
		}
		for _, d := range s.Feed(stream[off:end]) {
			total++
			if len(d) == 0 || len(d) > datagramBytes {
				t.Fatalf("output datagram is %d bytes, want 1..%d", len(d), datagramBytes)
			}
			if len(d)%tsPacketSize != 0 {
				t.Fatalf("output datagram is %d bytes, not a whole number of 188-byte packets", len(d))
			}
		}
	}
	for _, d := range s.Flush() {
		total++
		if len(d) == 0 || len(d) > datagramBytes || len(d)%tsPacketSize != 0 {
			t.Fatalf("flushed datagram is %d bytes, want whole packets up to %d", len(d), datagramBytes)
		}
	}
	if total == 0 {
		t.Fatal("the stage returned no datagrams for an eight-picture stream")
	}
}

// TestSEIStageRefusesACueWhenItsQueueIsFullAndCountsTheDrop checks that a
// caption that cannot be queued is refused at once rather than blocking the
// relay goroutine that delivered it, and that the refusal is counted.
func TestSEIStageRefusesACueWhenItsQueueIsFullAndCountsTheDrop(t *testing.T) {
	s := NewSEIStage()
	accepted := 0
	for i := 0; i < seiCueQueue+5; i++ {
		if s.Cue("X", time.Second) {
			accepted++
		}
	}
	if accepted != seiCueQueue {
		t.Fatalf("accepted %d cues into a queue of %d", accepted, seiCueQueue)
	}
	if got := s.Dropped(); got != 5 {
		t.Fatalf("Dropped() = %d, want 5", got)
	}
}

// TestSEIStagePumpCarriesCaptionsOverLoopbackUDPAsTheRelayDoes runs the stage
// the way the engine will: datagrams in from a UDP socket, rewritten datagrams
// out to a callback. It checks the caption survives the socket hop and that the
// pump returns once its context is cancelled.
func TestSEIStagePumpCarriesCaptionsOverLoopbackUDPAsTheRelayDoes(t *testing.T) {
	decode := make([]int, 40)
	for i := range decode {
		decode[i] = i
	}
	stream := streamOf(decode)

	in, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	sender, err := net.Dial("udp", in.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()

	s := NewSEIStage()
	s.Cue("HI", 10*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var out []byte
	done := make(chan error, 1)
	go func() {
		done <- s.Pump(ctx, in, func(d []byte) {
			mu.Lock()
			out = append(out, d...)
			mu.Unlock()
		})
	}()

	for off := 0; off < len(stream); off += datagramBytes {
		end := off + datagramBytes
		if end > len(stream) {
			end = len(stream)
		}
		if _, err := sender.Write(stream[off:end]); err != nil {
			t.Fatal(err)
		}
	}
	// Let the loopback socket drain before cancelling.
	time.Sleep(200 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Pump returned %v, want nil on cancel", err)
	}

	mu.Lock()
	defer mu.Unlock()
	var got []Pair
	for _, u := range videoUnits(t, parsePackets(t, out), testVideoPID) {
		for _, nal := range splitAnnexB(u.es) {
			if p, ok := ccPairOf(nal); ok && p != nullPair {
				got = append(got, p)
			}
		}
	}
	want := PopOnScript("HI")
	if len(got) < len(want) {
		t.Fatalf("caption pairs over UDP = %d, want at least %d", len(got), len(want))
	}
}
