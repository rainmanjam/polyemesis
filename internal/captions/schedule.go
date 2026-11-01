package captions

import (
	"sort"
	"strings"
	"time"
)

// ticksPerSecond is the MPEG-TS presentation clock, 90 kHz.
const ticksPerSecond = 90000

// slotTicks is one CEA-608 pair slot: 1/30 s of the presentation clock. The
// field-1 channel carries about 30 pairs a second, so one slot per 33 ms is the
// rate a 608 decoder expects, whatever the frame rate of the picture.
const slotTicks = ticksPerSecond / 30

// slotWindow is the reorder lookahead, in slots. A slot is final once a picture
// at least this many slots later has claimed its own, because B-frame reordering
// only looks back a few pictures. The lookahead is about a quarter of a second,
// which the caption lag already dwarfs.
const slotWindow = 8

// slotOf maps a presentation time to its slot.
func slotOf(pts int64) int64 {
	if pts < 0 {
		return 0
	}
	return pts / slotTicks
}

// Cue is one caption on the stream's presentation timeline. Times are 90 kHz
// ticks, the same clock the picture timestamps use, so a cue lands on the
// picture whose timestamp it matches and nowhere else.
type Cue struct {
	StartPTS int64
	EndPTS   int64
	Text     string
}

// event is a run of pairs due at a timestamp.
type event struct {
	at    int64
	pairs []Pair
	// kind and cue say what the event is and which cue it belongs to (by the
	// cue's start), so a newer cue can withdraw what an older one still has
	// pending. See Add.
	kind eventKind
	cue  int64
}

// eventKind tells a cue's first script from its resends and its erase.
type eventKind uint8

const (
	eventScript eventKind = iota
	eventResend
	eventClear
)

// maxResendsPerCue bounds the resends one cue may schedule. A live caption is on
// screen for seconds, so this is never reached by a real cue; it stops a cue
// with a bogus end time from filling memory with one script per interval.
const maxResendsPerCue = 32

// Schedule turns caption cues into CEA-608 byte pairs, keyed on picture
// timestamps.
//
// The pair for a picture is a function of its presentation time, not of the
// order pictures arrive in. That matters for B-frames: an encoder emits a
// picture with a later timestamp before the one it precedes on screen, so a
// schedule that counted arrivals would hand the captions to frames in decode
// order and show them out of sequence. Keying on the clock gives every picture
// the pair for its own slot, however the pictures are interleaved.
//
// The queue behind the slots is ordered. Cues add their script at their start
// and their erase at their end, and each slot takes the next pair off the front,
// so a caption's erase never overtakes its own text. A cue registered after its
// start goes out in the next free slot, late, and is not dropped.
type Schedule struct {
	resendEvery int64
	events      []event
	queue       []Pair

	// claimed counts the pictures seen on each slot. Only a claimed slot takes a
	// pair from the queue, so a stretch of time with no pictures in it (before
	// the first picture, or after a dropped frame) costs nothing. A count, not a
	// flag, so Unclaim of one picture leaves a second one on the same slot.
	claimed    map[int64]int
	maxClaimed int64
	anyClaimed bool

	// cursor is the next slot to finalise. slots holds the finalised pair for
	// each slot below cursor, so a picture that arrives out of order still gets
	// its own slot's answer.
	cursor int64
	slots  map[int64]Pair
	// used records slots already given to a picture. A slot is one pair, so a
	// second picture that maps to it gets the null pair and not a copy.
	used map[int64]bool
}

// NewSchedule returns an empty schedule. A positive resendEvery re-sends each
// pop-on script at that interval while its cue is on screen, so a viewer who
// joins an HLS stream mid-caption sees it within one interval. Zero disables
// the resend.
func NewSchedule(resendEvery time.Duration) *Schedule {
	return &Schedule{
		resendEvery: int64(resendEvery.Seconds() * ticksPerSecond),
		claimed:     make(map[int64]int),
		slots:       make(map[int64]Pair),
		used:        make(map[int64]bool),
	}
}

// Add registers a cue. Blank text schedules nothing, so an empty line never
// clears the screen on every resend interval.
//
// A cue that ends before it starts is clamped to end where it starts, so its
// erase still follows its script and the caption cannot be left on screen. The
// resends are bounded by maxResendsPerCue.
//
// A pop-on caption replaces whatever is displayed, so a newer cue supersedes the
// cues that started before it. Their erase and resend events from the newer
// cue's start onward are withdrawn first: an older cue's erase at its own end
// would otherwise blank the newer caption halfway through, and its resends would
// put the older text back over it. Pairs already in the queue have gone past
// recall and are left alone.
func (s *Schedule) Add(c Cue) {
	if strings.TrimSpace(c.Text) == "" {
		return
	}
	if c.EndPTS < c.StartPTS {
		c.EndPTS = c.StartPTS
	}
	s.dropSuperseded(c.StartPTS)
	s.insert(event{at: c.StartPTS, pairs: PopOnScript(c.Text), kind: eventScript, cue: c.StartPTS})
	if s.resendEvery > 0 {
		n := 0
		for at := c.StartPTS + s.resendEvery; at < c.EndPTS && n < maxResendsPerCue; at += s.resendEvery {
			s.insert(event{at: at, pairs: PopOnScript(c.Text), kind: eventResend, cue: c.StartPTS})
			n++
		}
	}
	s.insert(event{at: c.EndPTS, pairs: ClearScript(), kind: eventClear, cue: c.StartPTS})
}

// dropSuperseded withdraws the pending erase and resend events, due at or after
// start, of every cue that started at or before start. A cue's first script is
// kept: a cue registered out of order still gets shown at its own time.
func (s *Schedule) dropSuperseded(start int64) {
	kept := s.events[:0]
	for _, e := range s.events {
		if e.kind != eventScript && e.cue <= start && e.at >= start {
			continue
		}
		kept = append(kept, e)
	}
	s.events = kept
}

// insert keeps events ordered by due time. Equal times keep arrival order, so an
// erase registered before a script at the same instant still runs first.
func (s *Schedule) insert(e event) {
	i := sort.Search(len(s.events), func(i int) bool { return s.events[i].at > e.at })
	s.events = append(s.events, event{})
	copy(s.events[i+1:], s.events[i:])
	s.events[i] = e
}

// resetSlots is how far behind the newest slot a picture may be before it counts
// as a discontinuity rather than reordering. Ten seconds is far beyond any B-frame
// group, and well inside the 26-hour span of the 33-bit timestamp.
const resetSlots = 10 * 30

// resetBackSlots is the same bound for a picture BEHIND the newest one. Two
// seconds is still several times the longest B-frame group, and keeps an encoder
// that reconnects with its clock restarting near zero from waiting up to ten
// seconds, every picture below the cursor and ignored, before captions resume.
const resetBackSlots = 2 * 30

// Claim records that a picture exists at pts. The injector claims every picture
// when it first sees it, before any of them are emitted, so that reordering
// within the lookahead is visible to the slots that depend on it.
//
// A picture far behind the newest one is a discontinuity: the source reconnected
// or the timestamps wrapped. A picture far ahead of it is one too: the source
// stalled and resumed, or the clock was reset upward. Either way the slot state
// belongs to the old timeline, so it is dropped and the schedule starts again at
// the new position. Claim reports whether that happened, so the caller can release
// the units it was still holding for the old timeline.
//
// A claim for a slot that is already final is ignored. Nothing after the cursor
// could change its answer, and recording it would add state nothing removes.
func (s *Schedule) Claim(pts int64) (reset bool) {
	k := slotOf(pts)
	if s.anyClaimed && (k < s.maxClaimed-resetBackSlots || k > s.maxClaimed+resetSlots) {
		s.restart(k)
		reset = true
	}
	if k < s.cursor {
		return reset
	}
	s.claimed[k]++
	if !s.anyClaimed || k > s.maxClaimed {
		s.maxClaimed = k
		s.anyClaimed = true
	}
	return reset
}

// Unclaim withdraws one picture's claim on its slot, for a picture that will go
// out without a caption SEI after all (a PCR inside it, no picture start, a PES
// that will not reassemble). A claimed slot takes the next pair from the queue,
// and a picture that never asks for it would lose that pair for good: an RCL, an
// EOC or an erase missing from the middle of a script garbles the caption or
// leaves it on screen. Unclaimed, the slot takes nothing and the pair waits for
// the next picture that will carry it. A slot already final is past changing.
func (s *Schedule) Unclaim(pts int64) {
	k := slotOf(pts)
	if k < s.cursor {
		return
	}
	if s.claimed[k] > 1 {
		s.claimed[k]--
		return
	}
	delete(s.claimed, k)
}

// Final reports whether the pair for pts can no longer change: enough later
// pictures have been claimed that no picture can still arrive on its slot.
func (s *Schedule) Final(pts int64) bool {
	return s.anyClaimed && slotOf(pts) <= s.maxClaimed-slotWindow
}

// PairAt returns the pair for the picture presented at pts. It claims the slot
// and finalises every slot up to it, then answers for that slot. A picture
// that arrives out of order after its slot is final still gets its own answer,
// and a second picture on the same slot gets the null pair.
func (s *Schedule) PairAt(pts int64) Pair {
	s.Claim(pts)
	return s.answer(pts)
}

// answer returns the pair for pts without claiming it. The injector claims a
// picture when it first arrives, and answers for it later, when its slot is
// due. Answering must not claim again, or a unit held across a clock jump would
// restart the schedule a second time.
func (s *Schedule) answer(pts int64) Pair {
	k := slotOf(pts)
	s.finalise(k)
	p, ok := s.slots[k]
	if !ok || p == nullPair || s.used[k] {
		return nullPair
	}
	s.used[k] = true
	return p
}

// finalise answers every slot up to and including last, in order. A claimed slot
// takes the next pair from the queue, and an unclaimed one takes nothing.
func (s *Schedule) finalise(last int64) {
	if last < s.cursor {
		return
	}
	// A long gap is not worth walking slot by slot. Jump to the window before the
	// request; nothing in the skipped stretch can still be claimed in order.
	if last-s.cursor > slotWindow {
		s.cursor = last - slotWindow
		s.prune()
	}
	for ; s.cursor <= last; s.cursor++ {
		old := s.cursor - slotWindow
		delete(s.slots, old)
		delete(s.used, old)
		delete(s.claimed, old)
		now := s.cursor * slotTicks
		n := 0
		for n < len(s.events) && s.events[n].at <= now {
			s.queue = append(s.queue, s.events[n].pairs...)
			n++
		}
		s.events = s.events[n:]
		p := nullPair
		if s.claimed[s.cursor] > 0 && len(s.queue) > 0 {
			p = s.queue[0]
			s.queue = s.queue[1:]
		}
		s.slots[s.cursor] = p
	}
}

// prune drops every slot entry that is older than the reorder window behind the
// cursor. The per-slot deletes in finalise only remove the one slot that falls out
// of the window each step, so a jump that skips slots would leave the older ones
// behind. A jump is rare, so walking the maps here costs little.
func (s *Schedule) prune() {
	floor := s.cursor - slotWindow
	for k := range s.claimed {
		if k < floor {
			delete(s.claimed, k)
		}
	}
	for k := range s.slots {
		if k < floor {
			delete(s.slots, k)
		}
	}
	for k := range s.used {
		if k < floor {
			delete(s.used, k)
		}
	}
}

// restart begins the slot state again at slot k. Cues still waiting are kept: a
// cue registered for the new timeline must not be lost to the restart. The pairs
// already queued belong to the old timeline, so they are replaced by an erase,
// which clears whatever caption was on screen before the stream went backwards.
//
// Events far from the new position are dropped too. They were registered against
// the old timeline: after a forward jump they are all already due and would pour
// out in one burst, and after a backward jump they would fire at times that mean
// nothing on the new clock. A cue registered for the new timeline is near the new
// position, inside the same window Claim uses to tell a jump from reordering.
func (s *Schedule) restart(k int64) {
	now, span := k*slotTicks, int64(resetSlots)*slotTicks
	kept := s.events[:0]
	for _, e := range s.events {
		if e.at >= now-span && e.at <= now+span {
			kept = append(kept, e)
		}
	}
	s.events = kept
	s.queue = ClearScript()
	s.claimed = make(map[int64]int)
	s.slots = make(map[int64]Pair)
	s.used = make(map[int64]bool)
	s.cursor = k
	s.maxClaimed = k
}
