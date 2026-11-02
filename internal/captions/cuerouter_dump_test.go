package captions

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// A DUMP KEEPS THE CAPTIONS OF WHAT STILL AIRS. A 30 s delayed destination has
// three cues queued: one spoken before the dumped window (its programme is
// still in the buffer and airs on the old schedule), one spoken inside it
// (dumped: must never appear), one after it (airs sooner, with the new 27 s
// delay). Before Dump existed, the delay change cleared all three.
//
// Mutation: make Dump cancel every queued cue.
func TestADumpKeepsKeptCuesDropsDumpedOnesAndRetimesTheRest(t *testing.T) {
	clock := newFakeClock()
	sink := &recordingSink{clock: clock}
	r := NewCueRouter(sink, clock.After, clock.Now)
	r.SetTargets([]CueTarget{{ID: 1, Delay: 30 * time.Second}})
	start := clock.Now()
	say := func(text string, at time.Duration) {
		r.Emit(transcribe.LiveCaption{Segment: transcribe.Segment{Text: text}, At: start.Add(at)})
	}
	clock.Advance(10 * time.Second)
	say("kept", 0)
	say("dumped", 5*time.Second)
	say("after", 9*time.Second)
	// The dump took [4 s, 7 s) of programme: 3 s.
	r.Dump(1, start.Add(4*time.Second), start.Add(7*time.Second), 27*time.Second)
	// SetTargets at the same delay, as the next reconcile sends, cancels nothing.
	r.SetTargets([]CueTarget{{ID: 1, Delay: 27 * time.Second}})

	clock.Advance(20 * time.Second) // 30 s: "kept" is due on the old schedule
	if n := sink.count(1); n != 1 || sink.got[0].text != "kept" {
		t.Fatalf("at 30 s the destination has %v, want only \"kept\"", sink.got)
	}
	clock.Advance(6 * time.Second) // 36 s: "after" (spoken 9 s) at 9+27
	clock.Advance(10 * time.Second)
	var texts []string
	for _, d := range sink.got {
		texts = append(texts, d.text)
	}
	if len(texts) != 2 || texts[1] != "after" {
		t.Fatalf("delivered %v; the dumped words must never appear and the rest must", texts)
	}
	if at := sink.got[1].at.Sub(start); at != 36*time.Second {
		t.Fatalf("\"after\" delivered %s in, want 36 s (spoken at 9 s, new delay 27 s)", at)
	}
}
