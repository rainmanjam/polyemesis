package engine

import (
	"errors"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/delayline"
)

// DUMP (#11 follow-up). An operator who hears something they must not air
// drops the newest part of a delayed destination's buffer, and the
// destination stays on the air. See delayline/splice.go for how the stream is
// cut and resumed without a corrupt frame or a timestamp jump.
//
// THE DELAY STAYS SHORTER. The video is copied, so the dumped seconds cannot
// be stretched back in, and a hold that refills them would put a picture on
// the platform that its decoder was not set up for (see BROADCAST-DELAY.md,
// "Why the delay does not rebuild itself"). The full delay returns when the
// destination next starts and fills its buffer from empty. Nothing here
// restarts it, because a restart takes the platform off the air for the whole
// delay.
//
// Captions follow the shorter delay: a dump changes the caption intent's
// delay for the destination, which cancels every cue still queued for it --
// including the cues for the words that were dumped.

// ErrNotDelayed refuses a dump on a destination with no running delay line.
var ErrNotDelayed = errors.New("this destination has no broadcast delay running")

// delayEventDumped is the destination.delay reason sent when a dump has
// resumed at its keyframe.
const delayEventDumped = "dumped"

// MaxDumpRequest is the largest dump that may be asked for: the longest delay.
const MaxDumpRequest = delayline.MaxDelay

// DumpDelay drops the newest want of destination id's delay buffer, or, for
// want 0, everything since its last dump point. It waits, a GOP or so, for
// the stream to resume, and reports what it did.
//
// done, when not nil, is called once with the outcome: the final figures when
// the stream has resumed (after DumpDelay returns, when the resume is late),
// or an aborted result when the line is cut first (Stop now, a restart, the
// end of a drain, shutdown). It is bound to this dump's own ticket, so a
// concurrent second dump -- refused as pending -- cannot replace it.
func (e *Engine) DumpDelay(id int64, want time.Duration, done func(delayline.DumpResult)) (delayline.DumpResult, error) {
	e.mu.RLock()
	d := e.dests[id]
	e.mu.RUnlock()
	if d == nil || d.delay == nil {
		return delayline.DumpResult{}, ErrNotDelayed
	}
	res, err := d.delay.line.Dump(want, done)
	if err != nil {
		return res, err
	}
	e.log.Warn("broadcast delay: dumped the newest part of the buffer",
		"dest", d.row.Name, "requested", want, "complete", res.Complete,
		"dumped", res.Dumped.Round(time.Millisecond), "delay", res.After.Round(time.Millisecond))
	e.publishStatus()
	return res, nil
}

// DelayDumpable reports whether destination id runs a delay line that is on
// the air in this engine. A dump of anything else is refused.
func (e *Engine) DelayDumpable(id int64) bool {
	e.mu.RLock()
	d := e.dests[id]
	e.mu.RUnlock()
	if d == nil || d.delay == nil {
		return false
	}
	st := d.delay.line.Status()
	return st.Releasing && !st.Draining
}

// delayDumped is the programme time dumped from destination id's running
// line: what its captions must take off their delay.
func (e *Engine) delayDumped(id int64) time.Duration {
	e.mu.RLock()
	d := e.dests[id]
	e.mu.RUnlock()
	if d == nil || d.delay == nil {
		return 0
	}
	return d.delay.line.Status().Dumped
}

// onDelayDumped is a line's OnDumped: the dump has resumed at its keyframe.
func (e *Engine) onDelayDumped(row *db.Destination, dd *destDelay, res delayline.DumpResult) {
	e.streamDelayWith(row, dd, delayEventDumped, "", map[string]any{
		"dumpSeconds":         res.Dumped.Seconds(),
		"requestedSeconds":    res.Requested.Seconds(),
		"clamped":             res.Clamped,
		"delayBeforeSeconds":  res.Before.Seconds(),
		"delayAfterSeconds":   res.After.Seconds(),
		"configuredSeconds":   row.OutputDelaySeconds(),
		"restoresOnNextStart": true,
		"recovery":            res.Recovery,
	})
	// Captions: the queued cues are re-timed first -- kept programme keeps
	// its cues, the dumped words lose theirs, later cues move to the shorter
	// delay -- so the SetTargets below sees the delay already changed and
	// cancels nothing. A failed intent read leaves the route as it is until
	// the next reconcile, which reads the same thing.
	e.mu.RLock()
	route := e.captRoute
	e.mu.RUnlock()
	if route != nil {
		route.Dump(row.ID, res.CutFrom, res.CutTo, res.After)
	}
	if in, err := e.captionIntentNow(); err == nil {
		e.applyCaptionIntent(in)
		e.syncCaptionRoute(in.targets)
	}
	e.publishStatus()
}

// DumpDelay is DumpDelay on whichever engine carries destination id.
func (m *Manager) DumpDelay(id int64, want time.Duration, done func(delayline.DumpResult)) (delayline.DumpResult, error) {
	for _, e := range m.Engines() {
		if e.carriesDelay(id) {
			return e.DumpDelay(id, want, done)
		}
	}
	return delayline.DumpResult{}, ErrNotDelayed
}

// DelayDumpable is DelayDumpable across the install.
func (m *Manager) DelayDumpable(id int64) bool {
	for _, e := range m.Engines() {
		if e.DelayDumpable(id) {
			return true
		}
	}
	return false
}

func (e *Engine) carriesDelay(id int64) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	d := e.dests[id]
	return d != nil && d.delay != nil
}
