package captions

import (
	"testing"
	"time"
)

// countPair is how many times p appears in pairs.
func countPair(pairs []Pair, p Pair) int {
	n := 0
	for _, q := range pairs {
		if q == p {
			n++
		}
	}
	return n
}

// TestANewerOverlappingCueIsNotErasedOrOverwrittenByTheOlderCuesPendingEvents.
//
// Live captions arrive as a stream of overlapping cues: the next line is often
// registered while the previous one is still on screen. A pop-on caption replaces
// whatever is displayed, so once the newer cue's script has gone out, the older
// cue has nothing left to say. Its erase at its own end time used to run anyway
// and blank the newer caption halfway through, and its resends re-displayed the
// older text over the newer one. Adding a cue must drop the earlier cues' pending
// erase and resend events from the newer cue's start onward.
//
// MUTATION, internal/captions/schedule.go: delete the dropSuperseded call in Add.
// Observed: FAIL -- 2 erase pairs went out (the older cue's EDM at 10 s, sent
// twice) while the newer caption was on screen.
func TestANewerOverlappingCueIsNotErasedOrOverwrittenByTheOlderCuesPendingEvents(t *testing.T) {
	s := NewSchedule(3 * time.Second)
	s.Add(Cue{StartPTS: 0, EndPTS: 10 * 90000, Text: "OLD"})
	s.Add(Cue{StartPTS: 5 * 90000, EndPTS: 20 * 90000, Text: "NEW"})
	got := drive(s, 0, 15*90000, tick30)

	if n := countPair(got, ClearScript()[0]); n != 0 {
		t.Fatalf("%d erase pairs went out while the newer cue was still on screen; the older cue's end erased it", n)
	}
	eoc := PopOnScript("OLD")[len(PopOnScript("OLD"))-1]
	// OLD at 0 s and 3 s, then NEW at 5, 8, 11 and 14 s: six scripts, each EOC sent
	// twice. OLD's resends at 6 s and 9 s would make it eight.
	if n := countPair(got, eoc); n != 6*2 {
		t.Fatalf("EOC pairs = %d, want %d: the older cue was re-sent over the newer one", n, 6*2)
	}
}

// TestACueThatEndsBeforeItStartsIsErasedAfterItsScriptAndNotStuckOnScreen.
//
// A cue whose end is before its start (a clock slip in the recogniser, or a
// caller's arithmetic) used to queue its erase first and its script second, so
// the caption went up and never came down. The end is clamped to the start, and
// the erase follows the script.
//
// MUTATION, internal/captions/schedule.go: delete the EndPTS clamp in Add.
// Observed: FAIL -- the last pair out is the script's EOC, not the erase.
func TestACueThatEndsBeforeItStartsIsErasedAfterItsScriptAndNotStuckOnScreen(t *testing.T) {
	s := NewSchedule(0)
	s.Add(Cue{StartPTS: 5 * 90000, EndPTS: 1 * 90000, Text: "BACKWARDS"})
	got := drive(s, 0, 8*90000, tick30)
	last := nullPair
	for _, p := range got {
		if p != nullPair {
			last = p
		}
	}
	if last != ClearScript()[0] {
		t.Fatalf("last pair out = %v, want the erase %v: the caption is stuck on screen", last, ClearScript()[0])
	}
}

// TestAVeryLongCueSchedulesABoundedNumberOfResends. The resend loop walks the
// cue's span at the resend interval. A cue with a bogus end time hours away used
// to schedule one script per interval for the whole span, tens of thousands of
// events, all held in memory until they came due.
//
// MUTATION, internal/captions/schedule.go: drop the maxResendsPerCue bound in Add.
// Observed: FAIL -- one cue queued 10001 events.
func TestAVeryLongCueSchedulesABoundedNumberOfResends(t *testing.T) {
	s := NewSchedule(time.Second)
	s.Add(Cue{StartPTS: 0, EndPTS: 10000 * 90000, Text: "FOREVER"})
	if n := len(s.events); n > maxResendsPerCue+2 {
		t.Fatalf("one cue queued %d events, want at most %d (script, %d resends, erase)", n, maxResendsPerCue+2, maxResendsPerCue)
	}
	if last := s.events[len(s.events)-1]; last.at != 10000*90000 || last.kind != eventClear {
		t.Fatalf("last event = %+v, want the erase at the cue's end", last)
	}
}

// TestAClockDiscontinuityDropsCueEventsThatBelongToTheOldTimeline. A cue
// registered against the old timeline has events at times the new timeline
// either never reaches (a backward jump) or has already passed (a forward jump).
// The forward case is the visible one: every stale event is already due, so the
// first finalise after the jump poured the old captions onto the new timeline in
// one burst. A restart keeps the events near the new position, which is where a
// cue registered for the new timeline sits, and drops the rest.
//
// MUTATION, internal/captions/schedule.go: keep every event in restart's purge
// loop. Observed: FAIL -- the STALE caption went out on the new timeline (4 EOC
// pairs).
func TestAClockDiscontinuityDropsCueEventsThatBelongToTheOldTimeline(t *testing.T) {
	s := NewSchedule(0)
	s.PairAt(100 * 90000)
	s.Add(Cue{StartPTS: 101 * 90000, EndPTS: 103 * 90000, Text: "STALE"})
	s.Add(Cue{StartPTS: 201 * 90000, EndPTS: 202 * 90000, Text: "FRESH"})
	got := drive(s, 200*90000, 204*90000, tick30)
	eoc := PopOnScript("FRESH")[len(PopOnScript("FRESH"))-1]
	if n := countPair(got, eoc); n != 2 {
		t.Fatalf("EOC pairs after the jump = %d, want 2 (the FRESH cue alone): old-timeline captions were replayed", n)
	}
}
