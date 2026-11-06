package captions

import (
	"context"
	"errors"
	"github.com/rainmanjam/polyemesis/internal/mpegts"
	"net"
	"os"
	"sync/atomic"
	"time"
)

// SEIStage is the one place live captions enter a copied MPEG-TS stream. It
// sits in a destination's path after the broadcast delay and before the
// destination's FFmpeg, and owns an Injector and its Schedule.
//
// Ownership is the design. The Injector is not safe for concurrent use and the
// Schedule has no lock, so only the goroutine that calls Feed touches them.
// Captions arrive from elsewhere (the CueRouter's timers) through Cue, which
// only puts text on a bounded channel. Nothing is shared, so nothing needs a
// mutex, and a caption can never block the relay that is feeding video.
type SEIStage struct {
	inj   *Injector
	sched *Schedule
	cues  chan pendingText
	// waiting holds captions that arrived before the first picture, so they
	// have no timeline to key on yet. Owned by Feed's goroutine.
	waiting []pendingText
	dropped atomic.Uint64
	// onUnsupported is told when the stream's video turns out not to be H.264
	// (HEVC), which this path cannot caption; seenUnsupported is how many such
	// streams it has already been told about. Both owned by Feed's goroutine.
	onUnsupported   func()
	seenUnsupported int
}

// OnUnsupported sets f to be called each time the stream's program map names
// video this stage cannot caption (HEVC: the A/53 SEI is an H.264 construct).
// The stream still passes through, unchanged, so without this an HEVC source
// showed captions as running while no cue ever reached the picture. Set it
// before Pump starts.
func (s *SEIStage) OnUnsupported(f func()) { s.onUnsupported = f }

// noticeUnsupported calls onUnsupported once per newly found unsupported stream.
func (s *SEIStage) noticeUnsupported() {
	if n := s.inj.Stats().Unsupported; n > s.seenUnsupported {
		s.seenUnsupported = n
		if s.onUnsupported != nil {
			s.onUnsupported()
		}
	}
}

// pendingText is one caption queued for the stream.
type pendingText struct {
	text string
	dur  time.Duration
}

const (
	// datagramBytes is the largest datagram the stage hands back: seven 188-byte
	// TS packets, the same shape the relay carries. relay.Hub reads into a larger
	// buffer, so it is never truncated.
	datagramBytes = 7 * mpegts.PacketSize
	// seiCueQueue bounds the captions waiting for the stream. A caption that
	// finds the queue full is refused and counted, never waited for.
	seiCueQueue = 32
	// seiWaitingMax bounds captions held back until the first picture arrives.
	seiWaitingMax = 8
	// seiResendEvery is how often the caption on screen is sent again. A
	// platform repackages the stream as HLS, and a viewer whose first segment
	// starts after a caption's End Of Caption sees nothing until the next one.
	// Two seconds is one Twitch segment, so a joiner sees the current caption
	// within a segment. The schedule caps the resends per cue (maxResendsPerCue).
	seiResendEvery = 2 * time.Second
)

// NewSEIStage returns a stage with no captions queued. A caption-free stage
// passes video through with no SEI added, so it is safe to build before any
// caption exists. Each caption is re-sent every seiResendEvery while it is on
// screen, for viewers who join mid-caption.
func NewSEIStage() *SEIStage {
	sched := NewSchedule(seiResendEvery)
	return &SEIStage{
		inj:   NewInjector(sched),
		sched: sched,
		cues:  make(chan pendingText, seiCueQueue),
	}
}

// Cue queues a caption to be placed on the stream from the next picture onward.
// It is safe to call from any goroutine. It never blocks: when the queue is full
// the caption is refused, the refusal is counted, and false is returned.
func (s *SEIStage) Cue(text string, dur time.Duration) bool {
	select {
	case s.cues <- pendingText{text: text, dur: dur}:
		return true
	default:
		s.dropped.Add(1)
		return false
	}
}

// Queued reports how many captions are waiting to be placed on the stream.
func (s *SEIStage) Queued() int { return len(s.cues) }

// Dropped reports how many captions were refused because the queue was full.
func (s *SEIStage) Dropped() uint64 { return s.dropped.Load() }

// Feed takes one datagram of the upstream stream and returns the rewritten
// stream, ready to send, as datagrams of at most datagramBytes. Call it from one
// goroutine only; it is the goroutine that owns the injector.
//
// The datagram goes through the injector BEFORE queued captions are placed. A
// caption is keyed on the newest picture, and if this datagram carries a clock
// discontinuity, placing the caption first would stamp it on the old timeline,
// and the restart the jump triggers would then purge it.
func (s *SEIStage) Feed(dgram []byte) [][]byte {
	out := s.inj.Write(dgram)
	s.noticeUnsupported()
	s.drainCues()
	return chunk(out)
}

// Flush releases what the injector still holds. Call it when the stream ends,
// so the last pictures are not lost with the stage.
func (s *SEIStage) Flush() [][]byte {
	s.drainCues()
	return chunk(s.inj.Flush())
}

// Pump runs the stage over a packet socket: it reads upstream datagrams from in,
// feeds each through Feed, and passes every rewritten datagram to out. It returns
// nil once ctx is cancelled, after flushing what the injector still holds. A read
// error that is not a cancel is returned, after the same flush.
//
// Pump is the one goroutine that owns the stage, so out is called from it and
// must not block for long: the engine passes a hub's non-blocking Deliver.
func (s *SEIStage) Pump(ctx context.Context, in net.PacketConn, out func([]byte)) error {
	buf := make([]byte, 2048)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			// A read blocked on the socket is woken by a deadline in the past.
			_ = in.SetReadDeadline(time.Now())
		case <-stop:
		}
	}()
	for {
		n, _, err := in.ReadFrom(buf)
		if n > 0 {
			for _, d := range s.Feed(buf[:n]) {
				out(d)
			}
		}
		if err != nil {
			for _, d := range s.Flush() {
				out(d)
			}
			if ctx.Err() != nil && errors.Is(err, os.ErrDeadlineExceeded) {
				return nil
			}
			return err
		}
	}
}

// drainCues moves queued captions onto the schedule, keyed on the newest
// picture. A caption that arrives before any picture is held and retried on the
// next call, up to seiWaitingMax of them.
func (s *SEIStage) drainCues() {
	for {
		select {
		case c := <-s.cues:
			if len(s.waiting) < seiWaitingMax {
				s.waiting = append(s.waiting, c)
			} else {
				s.dropped.Add(1)
			}
			continue
		default:
		}
		break
	}
	pts, ok := s.inj.PTSNow()
	if !ok {
		return
	}
	for _, c := range s.waiting {
		s.sched.Add(Cue{
			StartPTS: pts,
			EndPTS:   pts + int64(c.dur.Seconds()*ticksPerSecond),
			Text:     c.text,
		})
	}
	s.waiting = s.waiting[:0]
}

// chunk splits a rewritten stream into datagrams of at most datagramBytes. The
// injector only emits whole packets, so every chunk is a whole number of them.
func chunk(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		n := len(b)
		if n > datagramBytes {
			n = datagramBytes
		}
		out = append(out, b[:n])
		b = b[n:]
	}
	return out
}
