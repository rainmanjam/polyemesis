package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/delayline"
	"github.com/rainmanjam/polyemesis/internal/fsperm"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// PER-DESTINATION BROADCAST DELAY (#11).
//
// A delayed destination reads a PRIVATE relay hub instead of its upstream one.
// Between the two sits a delayline.Line: subscribed to the upstream hub like
// any consumer, it holds every datagram for the delay and then Delivers it into
// the private hub, which the destination's FFmpeg -- and its backup feed -- are
// subscribed to as usual. So video is still copied, every other consumer of
// the upstream hub is untouched, and the hub machinery a destination relies on
// (the stop wake, continuity accounting, the #711 name guard) keeps working
// unchanged on the far side of the delay.
//
// WHAT HAPPENS TO THE BUFFER, BY CAUSE. Each row is tested.
//
//	cause                                     buffer
//	----------------------------------------  ---------------------------------
//	stopped / disabled / stop-all / removed   DRAINS: the tail plays out, then
//	                                          the FFmpeg stops (flush on stop)
//	the same with ?now=true ("Stop now")      cut at once
//	Stop now on a destination already         cut at once
//	  draining
//	started again while still draining        the drain is cut, then a fresh start
//	any restart (spec change, incl. the       cut; the new run buffers from empty,
//	  delay itself)                           which the UI warns about
//	the upstream hub replaced under it        RE-POINTED: the line's input moves
//	  (rendition restart, selector or         to the new hub and the buffer is
//	  silence tier rebuilt)                   kept -- failover passes through
//	failover between sources                  nothing: the selector's hub is the
//	                                          same object, and the switch reaches
//	                                          the platform Delay later
//	the publisher (OBS) disconnects           nothing is torn down; the buffer
//	                                          plays out on its own
//	programme deleted / server shutdown       cut: a shutdown cannot wait 15 min
//
// THE FFMPEG STARTS LATE. A destination's child is not started when the
// destination is, but when the first held datagram is about to be released
// (delayline.DefaultLead ahead of it). An FFmpeg started at once would sit on
// an idle socket for the whole delay, read as stalled, and -- worse -- have
// #20's quick-rejection timing measured from a spawn that was not the attempt
// at all. Started late, a rejected key is classified and held exactly as it is
// without a delay; it is simply discovered Delay after going live, which the
// docs say.

// destDelay is a delayed destination's buffer and private hub.
type destDelay struct {
	line *delayline.Line
	// hub is the private hub the destination's FFmpeg and its backup read.
	hub *relay.Hub
	// inPort is the line's input port, subscribed to the upstream hub as inSub.
	inPort int
	inSub  string

	mu sync.Mutex
	// upstream is the hub the line's input is subscribed to now. It moves on a
	// re-point, which is why it is not destination.hub's job to say.
	upstream *relay.Hub
	// unsubscribed is set once the input has left the upstream hub.
	unsubscribed bool

	// due is set (under the engine's e.mu) when OnFirstDue fires: the processes
	// may start. A backup published after it is started at once.
	due bool
}

// delayDestSubName is the line's subscription on the upstream hub. A name of
// its own, so the FFmpeg's own name stays free for the private hub and the #711
// guard still refuses a second reader of the same destination upstream.
func delayDestSubName(id int64) string { return destSubName(id, "delay") }

// delayRoot is where every delayed destination of every programme spills.
func delayRoot(dataDir string) string { return filepath.Join(dataDir, "delay") }

// delaySpillDir is one destination's spill directory. Built only from the
// data directory and the numeric id -- never from anything an operator typed --
// because the line deletes the whole directory when it starts and stops.
func delaySpillDir(dataDir string, id int64) string {
	return filepath.Join(delayRoot(dataDir), strconv.FormatInt(id, 10))
}

// sweptDelayRoots records the data directories whose delay root has been
// cleared in this process. Once per process, not per engine: engines are built
// whenever a programme is added, and a second one sweeping the root would
// delete a running destination's spill out from under it.
var sweptDelayRoots sync.Map

// sweepDelayRoot clears the spill a crashed previous run left behind. Those
// segments are an old programme, and nothing must ever release them.
func sweepDelayRoot(dataDir string) {
	if dataDir == "" {
		return
	}
	root := delayRoot(dataDir)
	if _, done := sweptDelayRoots.LoadOrStore(root, true); done {
		return
	}
	_ = os.RemoveAll(root)
}

// delayFloorBytes is the free space a spill must leave on the data volume: the
// recording guard's own floor when one is set, and 2 GiB otherwise. The delay
// shares the volume with recordings and the database, and a spill that filled
// it would take both down with it.
func delayFloorBytes(s db.Settings) uint64 {
	const fallback = 2 << 30
	if gb := s.Recording.MinFreeGB; gb > 0 {
		return uint64(gb * (1 << 30))
	}
	return fallback
}

// delayRoom is the disk guard a line asks before every spill.
func (e *Engine) delayRoom(dir string) func(n int64) error {
	return func(n int64) error {
		free, err := uploads.FreeBytes(dir)
		if err != nil {
			return fmt.Errorf("the free space of the delay buffer's volume cannot be read: %w", err)
		}
		e.mu.RLock()
		floor := delayFloorBytes(e.settings)
		e.mu.RUnlock()
		if free < floor+uint64(n) {
			return fmt.Errorf("the data volume is down to %d MB free, under the %d MB the "+
				"delay buffer must leave", free>>20, floor>>20)
		}
		return nil
	}
}

// buildDelay makes a destination's line and private hub. The line's input is
// NOT yet subscribed upstream -- attachDelay does that once the destination is
// published, so no datagram can arrive (and no OnFirstDue fire) before the
// process it would start exists.
//
// The struct exists before the line does, and OnFirstDue closes over its
// pointer: the line's pump calls it, and every field it reads is read under
// e.mu, which startDest also holds to publish the destination -- the only
// happens-before the race detector can see across a UDP hop.
func (e *Engine) buildDelay(row *db.Destination) (*destDelay, error) {
	dd := &destDelay{inSub: delayDestSubName(row.ID)}
	id := row.ID
	delay := time.Duration(row.DelaySeconds) * time.Second
	sweepDelayRoot(e.cfg.DataDir)
	dir := delaySpillDir(e.cfg.DataDir, row.ID)
	if err := os.MkdirAll(delayRoot(e.cfg.DataDir), 0o700); err != nil {
		return nil, fmt.Errorf("broadcast delay: %w", err)
	}
	// The spill is the operator's programme before it has aired: owner only, on
	// Windows too, where 0o700 means nothing.
	if err := fsperm.SecureDir(delayRoot(e.cfg.DataDir)); err != nil {
		e.log.Warn("broadcast delay: could not restrict the spill directory", "err", err)
	}
	hub, err := relay.New(e.log, 0)
	if err != nil {
		return nil, fmt.Errorf("broadcast delay: %w", err)
	}
	port, err := e.allocPort()
	if err != nil {
		_ = hub.Close()
		return nil, err
	}
	line, err := delayline.Listen(delayline.Config{
		Delay: delay,
		Limits: delayline.Limits{
			MemBudget:    delayline.DefaultMemBudget,
			DiskBudget:   delayline.DiskBudgetFor(delay),
			SegmentBytes: delayline.DefaultSegmentBytes,
			Dir:          dir,
			Room:         e.delayRoom(e.cfg.DataDir),
		},
		Lead:       delayline.DefaultLead,
		OnFirstDue: func() { e.delayDue(id, dd) },
		Out:        hub.Deliver,
		Log:        e.log.With("dest", row.Name),
	}, port)
	if err != nil {
		e.releasePort(port)
		_ = hub.Close()
		return nil, err
	}
	dd.line, dd.hub, dd.inPort = line, hub, port
	return dd, nil
}

// attachDelay subscribes the line's input to the upstream hub.
func (dd *destDelay) attach(upstream *relay.Hub) error {
	dd.mu.Lock()
	defer dd.mu.Unlock()
	if _, err := upstream.Subscribe(dd.inSub, dd.inPort); err != nil {
		return err
	}
	dd.upstream = upstream
	dd.unsubscribed = false
	return nil
}

// detach takes the line's input off the upstream hub. Idempotent.
func (dd *destDelay) detach() {
	dd.mu.Lock()
	defer dd.mu.Unlock()
	if dd.upstream != nil && !dd.unsubscribed {
		dd.upstream.Unsubscribe(dd.inSub)
	}
	dd.unsubscribed = true
}

// repoint moves the line's input to a new upstream hub, keeping the buffer.
// This is what makes a rendition restart or a rebuilt selector tier a blip in
// the delayed output rather than the loss of everything held.
func (dd *destDelay) repoint(to *relay.Hub) error {
	dd.mu.Lock()
	defer dd.mu.Unlock()
	if dd.upstream == to {
		return nil
	}
	if dd.upstream != nil && !dd.unsubscribed {
		dd.upstream.Unsubscribe(dd.inSub)
	}
	if _, err := to.Subscribe(dd.inSub, dd.inPort); err != nil {
		dd.unsubscribed = true
		return err
	}
	dd.upstream = to
	dd.unsubscribed = false
	return nil
}

// closeDelay cuts the line and releases everything it held. "Stop now".
func (e *Engine) closeDelay(dd *destDelay) {
	if dd == nil {
		return
	}
	dd.detach()
	dd.line.Close()
	e.releasePort(dd.inPort)
	_ = dd.hub.Close()
}

// readHub is the hub a destination's FFmpeg reads: the private one when it is
// delayed, its upstream otherwise.
func (d *destination) readHub(fallback *relay.Hub) *relay.Hub {
	if d.delay != nil {
		return d.delay.hub
	}
	if d.hub != nil {
		return d.hub
	}
	return fallback
}

// delayDue is OnFirstDue: start the destination's processes, which have been
// waiting for the delayed data. Looked up by the line's identity, so a line
// that has been replaced starts nothing.
func (e *Engine) delayDue(id int64, dd *destDelay) {
	e.mu.Lock()
	dd.due = true
	d := e.dests[id]
	if d == nil || d.delay != dd {
		d = e.draining[id]
	}
	if d == nil || d.delay != dd {
		e.mu.Unlock()
		return
	}
	proc, backup := d.proc, d.backup
	e.mu.Unlock()
	if proc != nil {
		proc.Start()
	}
	if backup != nil {
		backup.Start()
	}
	e.log.Info("broadcast delay: the delayed feed is about to reach this destination; starting it",
		"dest", d.row.Name, "delay", dd.line.Delay())
	e.publishStatus()
}

// beginDrain lets a stopped destination's tail play out before its FFmpeg is
// stopped. The destination sits in e.draining meanwhile (moved there under
// e.mu by the caller), so Status still reports it and Stop now can find it.
func (e *Engine) beginDrain(d *destination) {
	d.delay.detach()
	done := d.delay.line.Drain()
	e.log.Info("broadcast delay: destination stopped; the delayed tail is playing out before it ends",
		"dest", d.row.Name, "remaining", d.delay.line.Status().DrainRemaining.Round(time.Second))
	go func() {
		<-done
		e.finishDrain(d)
	}()
}

// finishDrain tears a drained (or cut) destination down, once.
func (e *Engine) finishDrain(d *destination) {
	e.mu.Lock()
	mine := e.draining[d.row.ID] == d
	if mine {
		delete(e.draining, d.row.ID)
		if e.retiring == nil {
			e.retiring = map[int64]*destination{}
		}
		e.retiring[d.row.ID] = d
	}
	e.mu.Unlock()
	if !mine {
		// Shutdown or a new start took it first and tears it down itself.
		return
	}
	e.teardownDest(d)
	e.mu.Lock()
	if e.retiring[d.row.ID] == d {
		delete(e.retiring, d.row.ID)
	}
	e.mu.Unlock()
	e.publishStatus()
}

// CutDelay is "Stop now" for a destination whose delayed tail is still
// playing out: the tail is discarded and the destination stops at once.
// Reports whether there was a drain to cut.
func (e *Engine) CutDelay(id int64) bool {
	e.mu.RLock()
	d := e.draining[id]
	e.mu.RUnlock()
	if d == nil || d.delay == nil {
		return false
	}
	// Closing the line closes its drain channel, and the drain goroutine does
	// the teardown -- one path, whoever ends the drain.
	d.delay.line.Close()
	// Wait for it, so a caller reading status back sees it stopped.
	for range 200 {
		e.mu.RLock()
		_, still := e.draining[id]
		_, going := e.retiring[id]
		e.mu.RUnlock()
		if !still && !going {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	return true
}

// takeDrainingLocked removes a draining destination so a caller can cut it
// itself: a new start of the same destination, or shutdown. Under e.mu.
func (e *Engine) takeDrainingLocked(id int64) *destination {
	d := e.draining[id]
	if d != nil {
		delete(e.draining, id)
	}
	return d
}

// DelayStatus is a delayed destination's buffer on the status payload.
type DelayStatus struct {
	// Seconds is the configured delay.
	Seconds int `json:"seconds"`
	// Phase is "buffering" (holding, nothing released yet), "on-air"
	// (releasing), or "draining" (stopped, the tail is playing out).
	Phase string `json:"phase"`
	// Fill is how much of the delay the buffer holds, 0 to 1.
	Fill float64 `json:"fill"`
	// BufferedSeconds is the content time held.
	BufferedSeconds float64 `json:"bufferedSeconds"`
	// DrainRemainingSeconds is how long a draining tail has left.
	DrainRemainingSeconds float64 `json:"drainRemainingSeconds,omitempty"`
	MemBytes              int64   `json:"memBytes"`
	DiskBytes             int64   `json:"diskBytes"`
	// DroppedBytes is data refused for want of room: the delay was kept, and
	// this much of the programme will be missing from the delayed output.
	DroppedBytes uint64 `json:"droppedBytes,omitempty"`
	// Problem is why something was dropped, when it was.
	Problem string `json:"problem,omitempty"`
}

// Delay phases.
const (
	DelayBuffering = "buffering"
	DelayOnAir     = "on-air"
	DelayDraining  = "draining"
)

func delayStatusOf(dd *destDelay) *DelayStatus {
	if dd == nil {
		return nil
	}
	s := dd.line.Status()
	out := &DelayStatus{
		Seconds:         int(s.Delay / time.Second),
		Fill:            s.Fill,
		BufferedSeconds: s.Span.Seconds(),
		MemBytes:        s.MemBytes,
		DiskBytes:       s.DiskBytes,
		DroppedBytes:    s.DroppedBytes,
		Problem:         s.Err,
	}
	switch {
	case s.Draining:
		out.Phase = DelayDraining
		out.DrainRemainingSeconds = s.DrainRemaining.Seconds()
	case s.Releasing:
		out.Phase = DelayOnAir
	default:
		out.Phase = DelayBuffering
	}
	return out
}

// delayWarning is the card's "needs attention" line for a delay buffer that
// has had to drop data. The delay was kept; the programme has a hole in it.
func delayWarning(d *DelayStatus) string {
	if d == nil || d.DroppedBytes == 0 {
		return ""
	}
	msg := fmt.Sprintf("the broadcast delay buffer ran out of room and dropped %d MB "+
		"of the programme; the delay itself was kept", d.DroppedBytes>>20)
	if d.Problem != "" {
		msg += ": " + d.Problem
	}
	return msg
}

// delayHoldsBack reports whether a destination is enabled and deliberately not
// publishing yet: its delay is still filling. Not down, and not an incident.
func delayHoldsBack(d DestStatus) bool {
	return d.Delay != nil && d.Delay.Phase == DelayBuffering
}

// delayStillPublishing reports whether a stopped destination is still on the
// air, playing its delayed tail out. Until it finishes it is, for every watcher,
// still on: the YouTube broadcast must not be ended under it, and no script
// must hear "disabled" while viewers are still watching.
func delayStillPublishing(d DestStatus) bool {
	return d.Delay != nil && d.Delay.Phase == DelayDraining
}
