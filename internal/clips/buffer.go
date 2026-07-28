package clips

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// ErrEmpty is returned when a cut is asked for and nothing has been received.
var ErrEmpty = errors.New("clips: the buffer is empty, nothing has arrived from the relay yet")

// Buffer is the rolling in-memory window of the live stream.
//
// TWO limits, and the distinction is the whole safety argument. The WINDOW is
// what the operator asked for, in seconds, and evicting by age is what makes
// the buffer cost exactly seconds x the bitrate actually observed — measured
// rather than predicted, so it needs no estimate to be right. The CEILING is
// the backstop for the case the estimate would have got wrong anyway: a 25
// Mbit/s 4K feed at 60 seconds is 190 MB, and a ring that takes the window at
// its word exhausts a small VPS in under a minute. When the ceiling binds, the
// buffer simply holds less history and says so, which is a clip that is shorter
// than asked for rather than a server that is dead.
type Buffer struct {
	mu       sync.Mutex
	window   time.Duration
	maxBytes int64

	// pkts is a FIFO deque: appended at the tail, consumed from head, and
	// compacted when the dead prefix is half the slice. The datagram payloads
	// are recycled through free rather than re-allocated, because at 25 Mbit/s
	// this sees about 2,400 datagrams a second for the life of the process.
	pkts []packet
	head int
	free [][]byte

	dm    demux
	bytes int64

	rx      uint64
	evicted uint64
	dropped uint64
}

type packet struct {
	at  time.Time
	buf []byte
}

// freeListMax bounds the recycling pool. It only has to cover the churn of one
// eviction burst; anything beyond that is memory held for no reason.
const freeListMax = 128

// NewBuffer creates a ring holding at most window of history and never more
// than maxBytes.
func NewBuffer(window time.Duration, maxBytes int64) *Buffer {
	if window <= 0 {
		window = time.Duration(DefaultWindowSeconds) * time.Second
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxRingBytes
	}
	return &Buffer{window: window, maxBytes: maxBytes}
}

// Write records one datagram. It copies: the caller owns its read buffer and
// will fill it again on the very next iteration.
func (b *Buffer) Write(p []byte, at time.Time) {
	if len(p) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	// A single datagram larger than the whole ceiling would evict itself on
	// the way in and leave the buffer permanently empty. Refuse it instead,
	// and count it, so the symptom is a number rather than a mystery.
	if int64(len(p)) > b.maxBytes {
		b.dropped++
		return
	}

	buf := b.take(len(p))
	copy(buf, p)
	b.dm.observe(buf)

	b.pkts = append(b.pkts, packet{at: at, buf: buf})
	b.bytes += int64(len(buf))
	b.rx++

	b.evict(at)
	b.compact()
}

func (b *Buffer) take(n int) []byte {
	for i := len(b.free) - 1; i >= 0; i-- {
		if cap(b.free[i]) >= n {
			buf := b.free[i][:n]
			b.free = append(b.free[:i], b.free[i+1:]...)
			return buf
		}
	}
	return make([]byte, n)
}

func (b *Buffer) give(buf []byte) {
	if len(b.free) < freeListMax {
		b.free = append(b.free, buf[:cap(buf)])
	}
}

// evict drops the oldest datagrams until both limits hold.
func (b *Buffer) evict(now time.Time) {
	for b.head < len(b.pkts) {
		f := b.pkts[b.head]
		if b.bytes <= b.maxBytes && now.Sub(f.at) <= b.window {
			return
		}
		b.bytes -= int64(len(f.buf))
		b.give(f.buf)
		b.pkts[b.head] = packet{}
		b.head++
		b.evicted++
	}
}

// compact slides the live packets back to the front once the dead prefix is
// half the slice, so the deque neither grows without bound nor copies on
// every write.
func (b *Buffer) compact() {
	if b.head == 0 || b.head*2 < len(b.pkts) {
		return
	}
	n := copy(b.pkts, b.pkts[b.head:])
	clear(b.pkts[n:])
	b.pkts = b.pkts[:n]
	b.head = 0
}

// Cut is one extracted clip, still in memory.
type Cut struct {
	Data  []byte
	Start time.Time
	End   time.Time
	// Seconds is what the cut actually spans, which is not always what was
	// asked for: a keyframe search moves the start, and a buffer that has not
	// filled yet moves it further.
	Seconds float64
	// KeyframeAligned reports whether the cut starts at a random-access point.
	KeyframeAligned bool
	// Note explains the cut when it is not what was asked for. Empty when it
	// is exactly what was asked for.
	Note string
}

// Cut extracts the last d of the stream, starting at a keyframe.
//
// The keyframe search runs BACKWARDS first, to the last random-access point at
// or before the requested start. That yields a clip slightly LONGER than asked
// for, which is the right way to be wrong: "clip the last 30 seconds" must not
// return four seconds because the encoder happens to use a ten-second GOP.
// Only when the buffer does not reach back that far does it search forward and
// accept a shorter clip, and only when there is no random-access point at all
// does it fall back to a plain packet boundary — a slightly ugly first second
// beats refusing to produce a clip.
//
// The whole window is COPIED under the lock. That is tens of milliseconds of
// memcpy for a large clip, during which the read loop is stalled and the
// kernel's receive buffer absorbs the arrivals; holding references into the
// ring instead would hand the caller memory the recycler is about to overwrite.
func (b *Buffer) Cut(d time.Duration, now time.Time) (Cut, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	live := b.pkts[b.head:]
	if len(live) == 0 {
		return Cut{}, ErrEmpty
	}
	if d <= 0 {
		d = b.window
	}

	// Timestamps are monotonic in arrival order, so the requested start is a
	// binary search rather than a walk of a quarter-million datagrams.
	from := now.Add(-d)
	want := sort.Search(len(live), func(i int) bool { return !live[i].at.Before(from) })
	if want >= len(live) {
		want = len(live) - 1
	}

	cut, off, aligned := b.findStart(live, want)
	note := ""
	switch {
	case !aligned && !b.dm.haveVideo:
		note = "the stream carries no video this build recognises, so the clip starts at a packet boundary"
	case !aligned:
		note = "no keyframe was found in the buffered window, so the clip starts at a packet boundary and may show artefacts until the first one"
	case cut > want:
		note = "the clip starts at the first keyframe after the requested point, so it is shorter than asked for"
	case cut < want:
		note = "the clip starts at the keyframe before the requested point, so it is longer than asked for"
	}

	total := 0
	for i := cut; i < len(live); i++ {
		total += len(live[i].buf)
	}
	psi := b.dm.psi()
	out := make([]byte, 0, total-off+len(psi))
	out = append(out, psi...)
	out = append(out, live[cut].buf[off:]...)
	for i := cut + 1; i < len(live); i++ {
		out = append(out, live[i].buf...)
	}

	start, end := live[cut].at, live[len(live)-1].at
	return Cut{
		Data:            out,
		Start:           start,
		End:             end,
		Seconds:         end.Sub(start).Seconds(),
		KeyframeAligned: aligned,
		Note:            note,
	}, nil
}

// findStart picks the datagram and byte offset the clip begins at. See Cut for
// why the backward search comes first.
func (b *Buffer) findStart(live []packet, want int) (idx, off int, aligned bool) {
	for i := want; i >= 0; i-- {
		if o, ok := b.dm.randomAccess(live[i].buf); ok {
			return i, o, true
		}
	}
	for i := want + 1; i < len(live); i++ {
		if o, ok := b.dm.randomAccess(live[i].buf); ok {
			return i, o, true
		}
	}
	return want, 0, false
}

// Stats is the buffer as the dashboard reports it: how much history is
// actually available to clip, which is the only number an operator can act on.
type Stats struct {
	WindowSeconds float64 `json:"windowSeconds"`
	MaxBytes      int64   `json:"maxBytes"`
	Bytes         int64   `json:"bytes"`
	Packets       int     `json:"packets"`
	// Seconds is how much history is really held. Below WindowSeconds either
	// because the stream just started or because the ceiling is binding.
	Seconds float64 `json:"seconds"`
	// BitrateKbps is what the window is costing, observed rather than assumed.
	BitrateKbps int `json:"bitrateKbps"`
	// Truncated says the ceiling, not the window, is what limits the clip
	// length — the one thing that would otherwise look like a bug.
	Truncated  bool   `json:"truncated"`
	Datagrams  uint64 `json:"datagrams"`
	Evicted    uint64 `json:"evicted"`
	Oversized  uint64 `json:"oversized"`
	VideoFound bool   `json:"videoFound"`
}

// Stats reports the buffer's current occupancy.
func (b *Buffer) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()

	s := Stats{
		WindowSeconds: b.window.Seconds(),
		MaxBytes:      b.maxBytes,
		Bytes:         b.bytes,
		Packets:       len(b.pkts) - b.head,
		Datagrams:     b.rx,
		Evicted:       b.evicted,
		Oversized:     b.dropped,
		VideoFound:    b.dm.haveVideo,
	}
	live := b.pkts[b.head:]
	if len(live) > 1 {
		s.Seconds = live[len(live)-1].at.Sub(live[0].at).Seconds()
		if s.Seconds > 0 {
			s.BitrateKbps = int(float64(b.bytes) * 8 / s.Seconds / 1000)
		}
	}
	// Half a second of slack: the ring is always a little short of its window
	// simply because the newest datagram has only just arrived.
	s.Truncated = b.bytes >= b.maxBytes && s.Seconds < b.window.Seconds()-0.5
	return s
}
