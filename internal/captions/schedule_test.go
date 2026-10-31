package captions

import (
	"reflect"
	"testing"
	"time"
)

// A 30 fps picture is 3003 ticks of the 90 kHz clock. The schedule is driven one
// picture at a time, so these tests step pictures the way the injector does.
const tick30 = 3003

// drive feeds NextPair once per picture from start to end (exclusive) and
// returns every pair it produced, in order.
func drive(s *Schedule, start, end, step int64) []Pair {
	var out []Pair
	for pts := start; pts < end; pts += step {
		out = append(out, s.NextPair(pts))
	}
	return out
}

// TestNullPairFillsEveryPictureWhileNothingIsScheduled keeps the caption channel
// populated the way an encoder does. The null pair is ignored by decoders, and
// sending it keeps the cc_data present on every picture.
func TestNullPairFillsEveryPictureWhileNothingIsScheduled(t *testing.T) {
	s := NewSchedule(0)
	for _, p := range drive(s, 0, 10*90000, tick30) {
		if p != nullPair {
			t.Fatalf("pair = %v with nothing scheduled, want the null pair", p)
		}
	}
}

// TestPopOnScriptWaitsForItsStartTimeBeforeAnyPairGoesOut pins the timing. A
// caption registered for 0.1 s must not appear before the picture that carries
// that time, or it lands on the wrong frame.
func TestPopOnScriptWaitsForItsStartTimeBeforeAnyPairGoesOut(t *testing.T) {
	s := NewSchedule(0)
	s.Add(Cue{StartPTS: 9000, EndPTS: 90000, Text: "HELLO"})
	if got := s.NextPair(0); got != nullPair {
		t.Fatalf("pair before the start time = %v, want the null pair", got)
	}
	if got := s.NextPair(8999); got != nullPair {
		t.Fatalf("pair one tick early = %v, want the null pair", got)
	}
	want := PopOnScript("HELLO")[0]
	if got := s.NextPair(9000); got != want {
		t.Fatalf("first pair at start = %v, want RCL %v", got, want)
	}
}

// TestCueSendsItsScriptOneBytePairPerPictureThenClearsAtTheEnd checks the
// order on the wire. The script goes out in order, one pair per picture, and the
// erase follows the end of the cue.
func TestCueSendsItsScriptOneBytePairPerPictureThenClearsAtTheEnd(t *testing.T) {
	s := NewSchedule(0)
	s.Add(Cue{StartPTS: 0, EndPTS: 3 * 90000, Text: "HI"})
	got := drive(s, 0, 4*90000, tick30)
	script := PopOnScript("HI")
	clear := ClearScript()
	// The script occupies the first len(script) pictures, and nothing else is
	// scheduled before the erase, so the prefix must match exactly.
	if !reflect.DeepEqual(got[:len(script)], script) {
		t.Fatalf("first %d pairs = %v, want the script %v", len(script), got[:len(script)], script)
	}
	// The erase lands at or after the end time, so count the picture index of the
	// first EDM and check it is past the end.
	edmAt := -1
	for i, p := range got {
		if p == clear[0] {
			edmAt = i
			break
		}
	}
	if edmAt < 0 {
		t.Fatalf("no erase was sent after the cue ended")
	}
	if pts := int64(edmAt) * tick30; pts < 3*90000 {
		t.Fatalf("erase sent at PTS %d, before the cue ended at %d", pts, 3*90000)
	}
}

// TestResendRepeatsThePopOnScriptWhileTheCueIsOnScreen covers HLS joiners. A
// viewer who opens the stream mid-caption sees nothing until the next pop-on, so
// the script is re-sent on an interval while the cue is up.
func TestResendRepeatsThePopOnScriptWhileTheCueIsOnScreen(t *testing.T) {
	s := NewSchedule(5 * time.Second)
	s.Add(Cue{StartPTS: 0, EndPTS: 12 * 90000, Text: "HI"})
	got := drive(s, 0, 13*90000, tick30)
	eoc := PopOnScript("HI")[len(PopOnScript("HI"))-1]
	// Three scripts: at 0 s, 5 s and 10 s. Each ends with an EOC pair, and the
	// EOC is the same pair that closes each script.
	count := 0
	for _, p := range got {
		if p == eoc {
			count++
		}
	}
	// Each script contributes its EOC twice (control codes are sent twice).
	if count != 3*2 {
		t.Fatalf("EOC pairs = %d, want %d (three scripts, each EOC sent twice)", count, 3*2)
	}
}

// TestLateCueIsSentAtTheNextPictureRatherThanDropped covers a caption that is
// registered after its own start time has passed, which happens when the delay
// is shorter than the transcription lag. It goes out late, and the test pins
// that it is not lost.
func TestLateCueIsSentAtTheNextPictureRatherThanDropped(t *testing.T) {
	s := NewSchedule(0)
	s.NextPair(9000)
	s.Add(Cue{StartPTS: 0, EndPTS: 90000, Text: "LATE"})
	first := PopOnScript("LATE")[0]
	if got := s.NextPair(12000); got != first {
		t.Fatalf("late cue first pair = %v, want %v", got, first)
	}
}

// TestEmptyCueTextSchedulesNothing stops a blank caption from clearing the
// screen on every interval. An empty line is silence, not a caption.
func TestEmptyCueTextSchedulesNothing(t *testing.T) {
	s := NewSchedule(time.Second)
	s.Add(Cue{StartPTS: 0, EndPTS: 90000, Text: "   "})
	for _, p := range drive(s, 0, 2*90000, tick30) {
		if p != nullPair {
			t.Fatalf("blank cue produced pair %v", p)
		}
	}
}
