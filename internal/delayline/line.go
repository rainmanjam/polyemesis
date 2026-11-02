package delayline

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Defaults, and why each is what it is.
const (
	// DefaultMemBudget is about a minute of a typical 8 Mbit/s 1080p feed, which
	// is what the plan agreed to keep in RAM. A higher bitrate reaches it
	// sooner and spills sooner: the budget is in bytes, not seconds, because
	// bytes are what run a small box out of memory.
	DefaultMemBudget = 64 << 20
	// DefaultSegmentBytes is the spill granularity: about one second at
	// 8 Mbit/s, so a refused spill drops about a second, not a minute.
	DefaultSegmentBytes = 1 << 20
	// MaxRateBytesPerSecond is the bitrate the disk budget is provisioned for:
	// 50 Mbit/s, comfortably above anything a platform accepts for a copied
	// stream. A feed above it outruns its budget and drops, loudly.
	MaxRateBytesPerSecond = 50_000_000 / 8
	// DefaultLead is how long before the first release the destination's
	// FFmpeg is started, so it is bound and listening when the data arrives.
	DefaultLead = 2 * time.Second
	// releaseSlack is how early a datagram may go out to share a wake with the
	// one before it.
	releaseSlack = time.Millisecond
	// DropReportEvery spaces OnDrop calls: a full disk drops a segment a
	// second, and one report a minute is news, sixty is noise.
	DropReportEvery = time.Minute
	// MaxDelay is the agreed ceiling.
	MaxDelay = 15 * time.Minute
)

// DiskBudgetFor is the spill ceiling for a delay: the delay at
// MaxRateBytesPerSecond. Tied to the delay so that a 90-second delay cannot
// claim the 5.6 GB a fifteen-minute one may need.
func DiskBudgetFor(delay time.Duration) int64 {
	return int64(delay/time.Second+1) * MaxRateBytesPerSecond
}

// Config is what a Line is built from.
type Config struct {
	Delay  time.Duration
	Limits Limits
	// Lead is how long before the first release OnFirstDue fires.
	Lead time.Duration
	// OnFirstDue fires once, Lead before the first datagram is released. The
	// engine starts the destination's FFmpeg here, which is why a delayed
	// destination's FFmpeg starts later than its neighbours.
	OnFirstDue func()
	// Out receives each datagram at its release time, from one goroutine.
	Out func([]byte)
	// OnDrop is told when the buffer has started dropping for want of room,
	// with the reason. At most once per DropReportEvery, from the pump.
	OnDrop func(err error)
	// OnDumped is told when a dump has resumed at its keyframe, with what it
	// did. From its own goroutine.
	OnDumped func(DumpResult)
	Log      *slog.Logger
}

// Status is a Line's state for the status page, events and metrics.
type Status struct {
	Delay time.Duration `json:"delay"`
	Stats
	// Fill is how much of the delay the buffer holds, 0 to 1. It reaches 1 when
	// the first datagram is released and stays there while the feed flows.
	Fill float64 `json:"fill"`
	// Releasing is true once the first datagram has gone out.
	Releasing bool `json:"releasing"`
	// Draining is true after Drain: input is closed and the tail is playing out.
	Draining bool `json:"draining"`
	// DrainRemaining is how long the tail still has to play, while Draining.
	DrainRemaining time.Duration `json:"drainRemaining"`
	// Err is the last spill failure, which is why something was dropped.
	Err string `json:"err,omitempty"`
	// Effective is the delay the audience has: Delay less everything dumped.
	Effective time.Duration `json:"effective"`
}

// Line is a Buffer fed from a UDP socket and drained on a clock.
type Line struct {
	cfg  Config
	conn *net.UDPConn
	buf  *Buffer
	log  *slog.Logger

	wake      chan struct{}
	quit      chan struct{}
	inputDone chan struct{}
	drained   chan struct{}

	draining  atomic.Bool
	releasing atomic.Bool
	closeOnce sync.Once
	drainOnce sync.Once
	wg        sync.WaitGroup
}

// Listen binds a loopback UDP socket on port and starts the line. A relay hub
// subscribed to that port feeds it.
func Listen(cfg Config, port int) (*Line, error) {
	if cfg.Delay <= 0 || cfg.Delay > MaxDelay {
		return nil, fmt.Errorf("delayline: delay %s is outside 0 to %s", cfg.Delay, MaxDelay)
	}
	if cfg.Out == nil {
		return nil, errors.New("delayline: no output")
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Limits.Dir != "" {
		// A previous run that crashed mid-delay leaves its segments behind; they
		// are the old programme and must not be replayed into this one.
		if err := os.RemoveAll(cfg.Limits.Dir); err != nil {
			return nil, fmt.Errorf("delayline: clear %s: %w", cfg.Limits.Dir, err)
		}
		if err := os.MkdirAll(cfg.Limits.Dir, 0o700); err != nil {
			return nil, fmt.Errorf("delayline: %w", err)
		}
	}
	now := time.Now()
	buf, err := NewBuffer(cfg.Delay, cfg.Limits, now)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		return nil, fmt.Errorf("delayline: bind: %w", err)
	}
	// The same generous buffer the hub uses: a spill write is the one thing on
	// the read path that can take a few milliseconds.
	_ = conn.SetReadBuffer(8 << 20)
	l := &Line{
		cfg:       cfg,
		conn:      conn,
		buf:       buf,
		log:       cfg.Log,
		wake:      make(chan struct{}, 1),
		quit:      make(chan struct{}),
		inputDone: make(chan struct{}),
		drained:   make(chan struct{}),
	}
	l.wg.Add(2)
	go l.read()
	go l.pump()
	return l, nil
}

// Port is where the line listens.
func (l *Line) Port() int { return l.conn.LocalAddr().(*net.UDPAddr).Port }

// Delay is the configured delay.
func (l *Line) Delay() time.Duration { return l.cfg.Delay }

func (l *Line) read() {
	defer l.wg.Done()
	defer close(l.inputDone)
	pkt := make([]byte, 2048)
	var dropsSeen uint64
	var lastDropReport time.Time
	for {
		n, err := l.conn.Read(pkt)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			select {
			case <-l.quit:
				return
			default:
			}
			continue
		}
		if n == 0 {
			// The hub drops these too: FFmpeg's UDP demuxer reads one as EOF.
			continue
		}
		if err := l.buf.Push(time.Now(), pkt[:n]); err != nil {
			continue
		}
		// A dump resumes on a push: the one that brought its keyframe.
		if t := l.buf.takeResumed(); t != nil && l.cfg.OnDumped != nil {
			go l.cfg.OnDumped(t.Result())
		}
		// Drops happen here, on the push that seals a segment with nowhere to
		// go -- including while the line is still filling and the pump is
		// asleep -- so they are reported from here.
		if l.cfg.OnDrop != nil {
			if d := l.buf.Dropped(); d > dropsSeen && time.Since(lastDropReport) >= DropReportEvery {
				dropsSeen, lastDropReport = d, time.Now()
				l.cfg.OnDrop(l.buf.Err())
			}
		}
		select {
		case l.wake <- struct{}{}:
		default:
		}
	}
}

func (l *Line) pump() {
	defer l.wg.Done()
	var out [][]byte
	fired := l.cfg.OnFirstDue == nil
	for {
		due, ok := l.buf.NextDue()
		if !ok {
			if l.draining.Load() {
				l.drainOnce.Do(func() { close(l.drained) })
				return
			}
			select {
			case <-l.wake:
				continue
			case <-l.quit:
				return
			}
		}
		if !fired {
			if !l.sleepUntil(due.Add(-l.cfg.Lead)) {
				return
			}
			fired = true
			l.cfg.OnFirstDue()
			continue
		}
		if !l.sleepUntil(due) {
			return
		}
		// Everything due within the next millisecond goes in this wake. At
		// 20 Mbit/s that is about two datagrams, and arming a timer for each
		// one separately would be ~1,900 timers a second for nothing.
		out = l.buf.Pop(time.Now().Add(releaseSlack), out[:0])
		for i, p := range out {
			l.cfg.Out(p)
			out[i] = nil
		}
		if len(out) > 0 && !l.releasing.Load() {
			l.releasing.Store(true)
			l.log.Info("broadcast delay: first delayed datagram released", "delay", l.cfg.Delay)
		}
	}
}

// sleepUntil waits for t, or returns false if the line closes first.
func (l *Line) sleepUntil(t time.Time) bool {
	d := time.Until(t)
	if d <= 0 {
		select {
		case <-l.quit:
			return false
		default:
			return true
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-l.quit:
		return false
	}
}

// Drain stops taking input and lets what is held play out at its normal pace.
// The channel closes when the last held datagram has been released -- up to
// Delay from now. This is the default stop: the audience sees the end of the
// programme, not a cut Delay before it.
func (l *Line) Drain() <-chan struct{} {
	if l.draining.CompareAndSwap(false, true) {
		_ = l.conn.Close()
		// The reader must be out before the pump may conclude "empty means
		// done", or a datagram read in that instant is pushed into a buffer
		// nobody will drain.
		<-l.inputDone
		select {
		case l.wake <- struct{}{}:
		default:
		}
	}
	return l.drained
}

// Close stops the line at once, discards everything held and deletes the
// spill. This is "Stop now", and every restart.
func (l *Line) Close() {
	l.closeOnce.Do(func() {
		l.draining.Store(true)
		close(l.quit)
		_ = l.conn.Close()
		l.wg.Wait()
		l.buf.Discard()
		if l.cfg.Limits.Dir != "" {
			_ = os.RemoveAll(l.cfg.Limits.Dir)
		}
		l.drainOnce.Do(func() { close(l.drained) })
	})
}

// ErrNotOnAir refuses a dump on a line that is not releasing: still filling,
// or stopped and playing its tail out.
var ErrNotOnAir = errors.New("delayline: the delayed feed is not on the air")

// MaxDumpWait bounds how long Dump waits for the resume keyframe.
const MaxDumpWait = maxGOPAllowance + 2*time.Second

// Dump drops the newest want of the buffer -- 0 for everything since the last
// dump point -- and waits, up to a GOP and a little more, for the stream to
// resume at the next keyframe. The result says whether it did: Complete is
// false when the wait ran out, and the resume then still happens, later.
// See splice.go.
func (l *Line) Dump(want time.Duration) (DumpResult, error) {
	if !l.releasing.Load() || l.draining.Load() {
		return DumpResult{}, ErrNotOnAir
	}
	t, err := l.buf.Dump(time.Now(), want, DumpMargin)
	if err != nil {
		return DumpResult{}, err
	}
	l.buf.mu.Lock()
	wait := l.buf.gopLocked() + 2*time.Second
	l.buf.mu.Unlock()
	if wait > MaxDumpWait {
		wait = MaxDumpWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-t.Done():
	case <-timer.C:
	case <-l.quit:
	}
	return t.Result(), nil
}

// Status is a snapshot.
func (l *Line) Status() Status {
	st := Status{
		Delay:     l.cfg.Delay,
		Stats:     l.buf.Stats(),
		Releasing: l.releasing.Load(),
		Draining:  l.draining.Load(),
	}
	// Fill is measured against the delay the audience has, so a dumped line
	// that is releasing still reads full.
	st.Effective = l.cfg.Delay - st.Dumped
	if st.Effective > 0 {
		st.Fill = float64(st.Span) / float64(st.Effective)
		if st.Releasing || st.Fill > 1 {
			st.Fill = 1
		}
		if st.Draining {
			st.Fill = float64(st.Span) / float64(st.Effective)
			if st.Fill > 1 {
				st.Fill = 1
			}
		}
	}
	if st.Draining {
		if newest, ok := l.buf.Newest(); ok {
			if rem := time.Until(newest.Add(l.cfg.Delay)); rem > 0 {
				st.DrainRemaining = rem
			}
		}
	}
	if err := l.buf.Err(); err != nil {
		st.Err = err.Error()
	}
	return st
}
