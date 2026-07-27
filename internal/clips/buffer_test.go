package clips

import (
	"bytes"
	"testing"
	"time"
)

var base = time.Date(2026, 7, 27, 20, 30, 0, 0, time.UTC)

// fill writes n datagrams a second apart, each built by make(i).
func fill(b *Buffer, n int, step time.Duration, mk func(i int) []byte) time.Time {
	at := base
	for i := 0; i < n; i++ {
		b.Write(mk(i), at)
		at = at.Add(step)
	}
	return at.Add(-step)
}

func TestBufferEvictsByAgeSoItCostsSecondsTimesTheObservedBitrate(t *testing.T) {
	b := NewBuffer(10*time.Second, DefaultMaxRingBytes)
	last := fill(b, 30, time.Second, func(i int) []byte { return datagram(audioPacket()) })

	s := b.Stats()
	if s.Packets != 11 {
		t.Fatalf("packets = %d, want the 10 second window (11 datagrams inclusive)", s.Packets)
	}
	if s.Bytes != int64(11*tsPacketSize) {
		t.Fatalf("bytes = %d, want %d", s.Bytes, 11*tsPacketSize)
	}
	if s.Evicted != 19 {
		t.Fatalf("evicted = %d, want 19", s.Evicted)
	}
	cut, err := b.Cut(time.Minute, last)
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	if got := cut.End.Sub(cut.Start); got != 10*time.Second {
		t.Fatalf("held history = %v, want 10s", got)
	}
}

func TestBufferCeilingBindsBeforeTheWindowOnAFatStream(t *testing.T) {
	// The case the ceiling exists for: a window the operator asked for that a
	// 25 Mbit/s feed would turn into hundreds of megabytes.
	b := NewBuffer(60*time.Second, 10*tsPacketSize)
	fill(b, 40, 100*time.Millisecond, func(i int) []byte { return datagram(audioPacket()) })

	s := b.Stats()
	if s.Bytes > 10*tsPacketSize {
		t.Fatalf("bytes = %d, over the ceiling of %d", s.Bytes, 10*tsPacketSize)
	}
	if !s.Truncated {
		t.Fatal("the ceiling binding must be reported, or a short clip looks like a bug")
	}
	if s.BitrateKbps <= 0 {
		t.Fatal("the observed bitrate is what tells an operator why the window is short")
	}
}

func TestBufferRefusesADatagramLargerThanTheWholeCeiling(t *testing.T) {
	// Accepting it would evict it on the way in and leave the ring empty for
	// the life of the process.
	b := NewBuffer(time.Minute, MinMaxRingBytes)
	b.Write(make([]byte, MinMaxRingBytes+1), base)
	b.Write(datagram(audioPacket()), base)

	s := b.Stats()
	if s.Oversized != 1 || s.Packets != 1 {
		t.Fatalf("stats = %+v, want one oversized refusal and one kept datagram", s)
	}
}

func TestCutOnAnEmptyBufferSaysSoRatherThanReturningNothing(t *testing.T) {
	if _, err := NewBuffer(time.Minute, DefaultMaxRingBytes).Cut(time.Second, base); err != ErrEmpty {
		t.Fatalf("err = %v, want ErrEmpty", err)
	}
}

func TestCutStartsAtTheKeyframeBeforeTheRequestedPoint(t *testing.T) {
	// A ten-second GOP and a thirty-second request: the clip must be a little
	// longer than asked for, never four seconds of nothing.
	b := NewBuffer(2*time.Minute, DefaultMaxRingBytes)
	b.Write(datagram(patPacket(), pmtPacket()), base)

	at := base.Add(time.Second)
	for i := 1; i <= 60; i++ {
		if i%10 == 0 {
			b.Write(datagram(videoPacket(true), audioPacket()), at)
		} else {
			b.Write(datagram(videoPacket(false), audioPacket()), at)
		}
		at = at.Add(time.Second)
	}
	now := at.Add(-time.Second)

	cut, err := b.Cut(30*time.Second, now)
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	if !cut.KeyframeAligned {
		t.Fatalf("cut is not keyframe aligned: %s", cut.Note)
	}
	// Requested start is t+30s (the 30th datagram); the keyframe at or before
	// it is the one at t+30s exactly, so the clip runs 30s and no less.
	if cut.Seconds < 30 {
		t.Fatalf("clip is %.1fs, shorter than the 30s asked for", cut.Seconds)
	}
	if cut.Seconds > 40 {
		t.Fatalf("clip is %.1fs; it must not run back further than one GOP", cut.Seconds)
	}
}

func TestCutRunsLongerRatherThanShorterWhenTheGOPIsCoarse(t *testing.T) {
	b := NewBuffer(2*time.Minute, DefaultMaxRingBytes)
	b.Write(datagram(patPacket(), pmtPacket()), base)

	at := base.Add(time.Second)
	// Keyframes only at 5s and 45s: a request for the last 10s has no keyframe
	// inside it at all and must reach back to the one at 5s.
	for i := 1; i <= 50; i++ {
		key := i == 5 || i == 45
		b.Write(datagram(videoPacket(key), audioPacket()), at)
		at = at.Add(time.Second)
	}
	now := at.Add(-time.Second)

	cut, err := b.Cut(3*time.Second, now)
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	if !cut.KeyframeAligned {
		t.Fatalf("cut is not keyframe aligned: %s", cut.Note)
	}
	if cut.Seconds < 3 {
		t.Fatalf("clip is %.1fs; reaching back to a keyframe must never shorten it", cut.Seconds)
	}
	if cut.Note == "" {
		t.Fatal("a clip longer than asked for has to explain itself")
	}
}

func TestCutFallsBackToAPacketBoundaryWhenThereIsNoKeyframe(t *testing.T) {
	// Fail open. A slightly ugly first second beats refusing to produce a clip.
	b := NewBuffer(time.Minute, DefaultMaxRingBytes)
	fill(b, 10, time.Second, func(i int) []byte { return datagram(audioPacket()) })

	cut, err := b.Cut(5*time.Second, base.Add(9*time.Second))
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	if cut.KeyframeAligned {
		t.Fatal("there was no keyframe to align to")
	}
	if cut.Note == "" {
		t.Fatal("an unaligned cut must say why")
	}
	if len(cut.Data) == 0 {
		t.Fatal("a clip was still expected")
	}
}

func TestCutPrependsThePSISoAPlayerNeedNotWaitForIt(t *testing.T) {
	b := NewBuffer(time.Minute, DefaultMaxRingBytes)
	b.Write(datagram(patPacket(), pmtPacket()), base)
	at := base.Add(time.Second)
	for i := 0; i < 5; i++ {
		b.Write(datagram(videoPacket(i == 2), audioPacket()), at)
		at = at.Add(time.Second)
	}

	cut, err := b.Cut(2*time.Second, at.Add(-time.Second))
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	if len(cut.Data) < 3*tsPacketSize {
		t.Fatalf("cut is only %d bytes", len(cut.Data))
	}
	if packetPID(cut.Data[:tsPacketSize]) != pidPAT {
		t.Fatal("a clip must open with the PAT")
	}
	if packetPID(cut.Data[tsPacketSize:2*tsPacketSize]) != testPMTPID {
		t.Fatal("the PMT must follow the PAT")
	}
	if !bytes.Equal(cut.Data[2*tsPacketSize:3*tsPacketSize], videoPacket(true)) {
		t.Fatal("the payload must begin at the keyframe packet itself")
	}
}

func TestCutCopiesSoTheRecycledRingCannotOverwriteAClip(t *testing.T) {
	b := NewBuffer(time.Minute, DefaultMaxRingBytes)
	b.Write(datagram(patPacket(), pmtPacket()), base)
	b.Write(datagram(videoPacket(true)), base.Add(time.Second))

	cut, err := b.Cut(time.Minute, base.Add(time.Second))
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	before := append([]byte(nil), cut.Data...)

	// Churn the ring hard enough that the free list hands the old payloads back
	// out. If Cut had returned references, the clip would change underneath us.
	for i := 0; i < 500; i++ {
		b.Write(bytes.Repeat([]byte{0xAA}, tsPacketSize), base.Add(time.Duration(i+2)*time.Second))
	}
	if !bytes.Equal(before, cut.Data) {
		t.Fatal("a captured cut must not alias the ring")
	}
}

func TestBufferSizingDefaultsAreApplied(t *testing.T) {
	s := NewBuffer(0, 0).Stats()
	if s.WindowSeconds != float64(DefaultWindowSeconds) {
		t.Fatalf("window = %v, want the default", s.WindowSeconds)
	}
	if s.MaxBytes != DefaultMaxRingBytes {
		t.Fatalf("ceiling = %v, want the default", s.MaxBytes)
	}
}
