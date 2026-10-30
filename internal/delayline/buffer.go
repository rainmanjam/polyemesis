// Package delayline holds one destination's copy of the relay feed back by a
// fixed delay before passing it on (#11, per-destination broadcast delay).
//
// It sits between a relay hub and the FFmpeg that publishes one destination:
// the line subscribes to the hub like any other consumer, keeps every datagram
// with the time it arrived, and releases each one exactly Delay after that.
// Video is still copied -- the line moves MPEG-TS datagrams, it never decodes
// -- and the pacing the ingest produced is the pacing the platform receives,
// shifted.
//
// # Bounded memory and disk
//
// Fifteen minutes is the agreed ceiling, and fifteen minutes of a 20 Mbit/s
// feed is 2.25 GB. Holding that in RAM per destination is not an option on the
// small boxes this runs on, so the buffer is a queue of fixed-size segments:
//
//   - The newest data is appended to a tail segment in RAM.
//   - A full tail is sealed. It stays in RAM while the RAM-resident sealed
//     segments total no more than MemBudget, and is written to its own file in
//     Dir otherwise. MemBudget is sized for about a minute at a typical
//     bitrate, so a short delay never touches the disk and a long one keeps
//     only its next minute or so in memory.
//   - The oldest segment is the head. A spilled head is read back whole when
//     it reaches the front, and its file is deleted as soon as it is read.
//
// So RAM never exceeds MemBudget + 2*SegmentBytes (the sealed segments, the
// tail being filled, and one head read back from disk), and disk never exceeds
// DiskBudget. Both are asserted by the tests, not just claimed here.
//
// # Overflow keeps the delay, not the data
//
// When a sealed segment fits neither budget -- the disk ceiling is reached, or
// the disk guard says the volume is running out -- the segment is DROPPED and
// counted. The alternative, releasing old data early to make room, would
// silently shorten the delay, and a delay that is shorter than the operator
// believes is worse than a gap: the whole point of a broadcast delay is the
// time it buys to cut something before it airs. A drop is a visible freeze on
// the platform and a counted, reported event here.
package delayline

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// recordHeader is the per-datagram framing inside a segment: the arrival time
// in nanoseconds since the buffer's epoch, then the payload length.
const recordHeader = 8 + 2

// maxDatagram is the largest datagram the buffer accepts. The relay's own
// datagrams are 1316 bytes; the length field is 16 bits, and anything bigger
// than a UDP datagram can be is not something a relay produced.
const maxDatagram = 65507

// ErrTooLarge is returned by Push for a datagram the framing cannot hold.
var ErrTooLarge = errors.New("delayline: datagram too large")

// Limits are the bounds a buffer is held to.
type Limits struct {
	// MemBudget is the ceiling on RAM-resident SEALED segments, in bytes.
	MemBudget int64
	// DiskBudget is the ceiling on spilled bytes. 0 disables spilling, so a
	// buffer that outgrows MemBudget drops instead.
	DiskBudget int64
	// SegmentBytes is the spill granularity: the size a tail is sealed at.
	SegmentBytes int
	// Dir is where segments spill. Required when DiskBudget > 0.
	Dir string
	// Room is the disk guard, asked before each spill with the bytes about to
	// be written. A non-nil answer refuses the spill and the segment drops.
	Room func(n int64) error
}

// segment is a run of consecutive records, in RAM or in one file.
type segment struct {
	data []byte // records; nil while spilled
	off  int    // read offset into data
	// first is the arrival time of the first unread record, so the head's due
	// time is known without reading a spilled segment back.
	first int64
	// last is the arrival time of the newest record in the segment.
	last int64
	path string // set while spilled
	size int64  // bytes of records, in RAM or on disk
	// packets and bytes are the unread datagrams and their payload bytes, so a
	// dropped or unreadable segment is still counted exactly.
	packets int64
	bytes   int64
}

// Buffer is the segment queue. Safe for one pusher and one popper at once.
type Buffer struct {
	delay  time.Duration
	lim    Limits
	epoch  time.Time
	mu     sync.Mutex
	queue  []*segment // queue[0] is the head; the tail is queue[len-1] when open
	tail   *segment   // the segment being appended to, also last in queue; nil when sealed
	seq    uint64     // spill file counter
	stats  Stats
	failed error // the last spill error, for the status line
}

// Stats is a snapshot of the buffer.
type Stats struct {
	// MemBytes is everything resident in RAM: sealed, tail and a read-back head.
	MemBytes int64 `json:"memBytes"`
	// DiskBytes is what is spilled to Dir right now.
	DiskBytes int64 `json:"diskBytes"`
	// Packets and Bytes are what is held.
	Packets int64 `json:"packets"`
	Bytes   int64 `json:"bytes"`
	// Span is the content time between the oldest and newest record held.
	Span time.Duration `json:"span"`
	// DroppedPackets and DroppedBytes count data refused for want of room.
	DroppedPackets uint64 `json:"droppedPackets"`
	DroppedBytes   uint64 `json:"droppedBytes"`
	// Released counts datagrams handed on.
	Released uint64 `json:"released"`
	// PeakMemBytes and PeakDiskBytes are the high-water marks, which is what
	// the bounds tests assert against.
	PeakMemBytes  int64 `json:"peakMemBytes"`
	PeakDiskBytes int64 `json:"peakDiskBytes"`
}

// NewBuffer makes an empty buffer. epoch anchors the stored arrival times.
func NewBuffer(delay time.Duration, lim Limits, epoch time.Time) (*Buffer, error) {
	if delay <= 0 {
		return nil, fmt.Errorf("delayline: delay must be positive, got %s", delay)
	}
	// Big enough for the largest datagram, so no segment ever exceeds the size
	// the RAM bound is computed from.
	if lim.SegmentBytes < recordHeader+maxDatagram {
		return nil, fmt.Errorf("delayline: segment size %d is too small", lim.SegmentBytes)
	}
	if lim.DiskBudget > 0 && lim.Dir == "" {
		return nil, errors.New("delayline: a disk budget needs a spill directory")
	}
	return &Buffer{delay: delay, lim: lim, epoch: epoch}, nil
}

// Delay is what the buffer holds data back by.
func (b *Buffer) Delay() time.Duration { return b.delay }

// Push stores a copy of pkt as having arrived at now.
func (b *Buffer) Push(now time.Time, pkt []byte) error {
	if len(pkt) == 0 {
		return nil
	}
	if len(pkt) > maxDatagram {
		return ErrTooLarge
	}
	at := int64(now.Sub(b.epoch))
	need := recordHeader + len(pkt)

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tail != nil && len(b.tail.data)+need > cap(b.tail.data) {
		b.sealLocked()
	}
	if b.tail == nil {
		b.tail = &segment{data: make([]byte, 0, b.lim.SegmentBytes), first: at}
		b.queue = append(b.queue, b.tail)
		b.stats.MemBytes += int64(cap(b.tail.data))
	}
	var hdr [recordHeader]byte
	binary.BigEndian.PutUint64(hdr[:8], uint64(at))
	binary.BigEndian.PutUint16(hdr[8:], uint16(len(pkt)))
	b.tail.data = append(b.tail.data, hdr[:]...)
	b.tail.data = append(b.tail.data, pkt...)
	b.tail.last = at
	b.tail.size = int64(len(b.tail.data))
	b.tail.packets++
	b.tail.bytes += int64(len(pkt))
	b.stats.Packets++
	b.stats.Bytes += int64(len(pkt))
	b.peaksLocked()
	return nil
}

// sealLocked closes the tail and decides where it lives.
func (b *Buffer) sealLocked() {
	s := b.tail
	b.tail = nil
	if s == nil || len(b.queue) == 1 {
		// The only segment is also the head, which is being read: it stays.
		return
	}
	resident := b.residentSealedLocked()
	if resident <= b.lim.MemBudget {
		return
	}
	// Over the RAM budget: this segment goes to disk, or nowhere.
	if err := b.spillLocked(s); err != nil {
		b.failed = err
		b.dropLocked(s)
	}
}

// residentSealedLocked is the RAM held by sealed segments other than the head.
// The head is excluded because it is being read and cannot move; it is counted
// in the 2*SegmentBytes slack instead.
func (b *Buffer) residentSealedLocked() int64 {
	var n int64
	for i, s := range b.queue {
		if i == 0 || s == b.tail || s.data == nil {
			continue
		}
		n += int64(cap(s.data))
	}
	return n
}

func (b *Buffer) spillLocked(s *segment) error {
	if b.lim.DiskBudget <= 0 {
		return errors.New("the RAM budget is full and spilling to disk is off")
	}
	if b.stats.DiskBytes+s.size > b.lim.DiskBudget {
		return fmt.Errorf("the disk budget of %d bytes is full", b.lim.DiskBudget)
	}
	if b.lim.Room != nil {
		if err := b.lim.Room(s.size); err != nil {
			return err
		}
	}
	b.seq++
	path := filepath.Join(b.lim.Dir, fmt.Sprintf("seg-%012d.bin", b.seq))
	// 0600: the spill is the operator's programme, before it has aired.
	if err := os.WriteFile(path, s.data[s.off:], 0o600); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("spill: %w", err)
	}
	b.stats.MemBytes -= int64(cap(s.data))
	b.stats.DiskBytes += s.size - int64(s.off)
	s.size -= int64(s.off)
	s.off = 0
	s.data = nil
	s.path = path
	b.failed = nil
	b.peaksLocked()
	return nil
}

// dropLocked discards a sealed segment that has nowhere to live.
func (b *Buffer) dropLocked(s *segment) {
	for i, q := range b.queue {
		if q != s {
			continue
		}
		b.queue = append(b.queue[:i], b.queue[i+1:]...)
		break
	}
	b.stats.Packets -= s.packets
	b.stats.Bytes -= s.bytes
	b.stats.DroppedPackets += uint64(s.packets)
	b.stats.DroppedBytes += uint64(s.bytes)
	if s.data != nil {
		b.stats.MemBytes -= int64(cap(s.data))
	}
	s.data = nil
}

func (b *Buffer) peaksLocked() {
	if b.stats.MemBytes > b.stats.PeakMemBytes {
		b.stats.PeakMemBytes = b.stats.MemBytes
	}
	if b.stats.DiskBytes > b.stats.PeakDiskBytes {
		b.stats.PeakDiskBytes = b.stats.DiskBytes
	}
}

// NextDue is when the oldest datagram held is to be released. ok is false
// when the buffer is empty.
func (b *Buffer) NextDue() (due time.Time, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queue) == 0 {
		return time.Time{}, false
	}
	return b.epoch.Add(time.Duration(b.queue[0].first) + b.delay), true
}

// Pop appends every datagram due at now to out, in arrival order, and returns
// it. The slices returned are copies the caller owns.
func (b *Buffer) Pop(now time.Time, out [][]byte) [][]byte {
	limit := int64(now.Sub(b.epoch) - b.delay)
	b.mu.Lock()
	defer b.mu.Unlock()
	for len(b.queue) > 0 {
		h := b.queue[0]
		if h.first > limit {
			return out
		}
		if h.data == nil {
			if err := b.loadLocked(h); err != nil {
				// A spilled segment that cannot be read back is lost; skip it
				// rather than wedge the whole line behind it.
				b.failed = err
				b.dropLocked(h)
				continue
			}
		}
		for h.off < len(h.data) {
			at := int64(binary.BigEndian.Uint64(h.data[h.off : h.off+8]))
			if at > limit {
				h.first = at
				return out
			}
			n := int(binary.BigEndian.Uint16(h.data[h.off+8 : h.off+recordHeader]))
			start := h.off + recordHeader
			pkt := make([]byte, n)
			copy(pkt, h.data[start:start+n])
			out = append(out, pkt)
			h.off = start + n
			h.packets--
			h.bytes -= int64(n)
			b.stats.Packets--
			b.stats.Bytes -= int64(n)
			b.stats.Released++
		}
		// Fully read. An open tail is closed with it, or the next Push would
		// append to a segment that is no longer in the queue.
		if h == b.tail {
			b.tail = nil
		}
		b.stats.MemBytes -= int64(cap(h.data))
		h.data = nil
		b.queue = b.queue[1:]
	}
	return out
}

// loadLocked reads a spilled head back into RAM and deletes its file.
func (b *Buffer) loadLocked(s *segment) error {
	// Into a slice sized from the segment's own record of its length, never
	// from the file: os.ReadFile may over-allocate, and an unexpectedly long
	// file must not be able to break the RAM bound.
	data := make([]byte, s.size)
	err := readExactly(s.path, data)
	_ = os.Remove(s.path)
	b.stats.DiskBytes -= s.size
	s.path = ""
	if err != nil {
		return fmt.Errorf("read back a spilled segment: %w", err)
	}
	s.data = data
	s.off = 0
	b.stats.MemBytes += int64(cap(data))
	b.peaksLocked()
	return nil
}

func readExactly(path string, into []byte) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.ReadFull(f, into)
	return err
}

// Stats returns a snapshot.
func (b *Buffer) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.stats
	if len(b.queue) > 0 {
		newest := b.queue[len(b.queue)-1].last
		st.Span = time.Duration(newest - b.queue[0].first)
		if st.Span < 0 {
			st.Span = 0
		}
	}
	return st
}

// Newest is the arrival time of the newest datagram held; ok is false when
// empty. A draining line uses it to say how long the tail has left to play.
func (b *Buffer) Newest() (at time.Time, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queue) == 0 {
		return time.Time{}, false
	}
	return b.epoch.Add(time.Duration(b.queue[len(b.queue)-1].last)), true
}

// Err is the last spill or read-back failure, nil once a spill succeeds again.
func (b *Buffer) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failed
}

// Discard drops everything held and deletes every spilled file. The buffer is
// empty and usable afterwards.
func (b *Buffer) Discard() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.queue {
		if s.path != "" {
			_ = os.Remove(s.path)
		}
	}
	b.queue = nil
	b.tail = nil
	b.stats.MemBytes = 0
	b.stats.DiskBytes = 0
	b.stats.Packets = 0
	b.stats.Bytes = 0
}
