package captions

import (
	"reflect"
	"testing"
	"time"
)

// A 30 fps picture is 3003 ticks of the 90 kHz clock. The schedule is driven one
// picture at a time, so these tests step pictures the way the injector does.
const tick30 = 3003

// drive asks for the pair of every picture from start to end (exclusive), in
// presentation order, and returns the pairs in that order.
func drive(s *Schedule, start, end, step int64) []Pair {
	var out []Pair
	for pts := start; pts < end; pts += step {
		out = append(out, s.PairAt(pts))
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
	if got := s.PairAt(0); got != nullPair {
		t.Fatalf("pair before the start time = %v, want the null pair", got)
	}
	if got := s.PairAt(8999); got != nullPair {
		t.Fatalf("pair one tick early = %v, want the null pair", got)
	}
	want := PopOnScript("HELLO")[0]
	if got := s.PairAt(9000); got != want {
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
	s.PairAt(9000)
	s.Add(Cue{StartPTS: 0, EndPTS: 90000, Text: "LATE"})
	first := PopOnScript("LATE")[0]
	if got := s.PairAt(12000); got != first {
		t.Fatalf("late cue first pair = %v, want %v", got, first)
	}
}

// TestPairsFollowPresentationTimeWhateverTheArrivalOrder is the B-frame case. An
// encoder sends a picture with a later timestamp before the one it precedes, so
// the pictures arrive as 0, 6, 2, 4 for a display order of 0, 2, 4, 6. The
// caption pairs must land on the pictures by timestamp, so the sequence of
// pairs read back in display order is the same as when the pictures arrive in
// display order.
func TestPairsFollowPresentationTimeWhateverTheArrivalOrder(t *testing.T) {
	cue := Cue{StartPTS: 0, EndPTS: 2 * 90000, Text: "CAPTIONS KEEP THEIR ORDER"}
	const frame = 3000 // 30 fps in 90 kHz ticks

	// The injector claims each picture as it arrives, before any is emitted, so
	// the reorder window is visible to the slots. The test does the same.
	inOrder := NewSchedule(0)
	inOrder.Add(cue)
	display := make(map[int64]Pair)
	for k := int64(0); k <= 6; k++ {
		inOrder.Claim(k * frame)
	}
	for k := int64(0); k <= 6; k++ {
		display[k*frame] = inOrder.PairAt(k * frame)
	}

	decode := NewSchedule(0)
	decode.Add(cue)
	// All seven pictures exist; they only differ in when they arrive.
	for k := int64(0); k <= 6; k++ {
		decode.Claim(k * frame)
	}
	arrival := []int64{0, 6, 2, 4}
	got := make(map[int64]Pair)
	for _, pts := range arrival {
		got[pts*frame] = decode.PairAt(pts * frame)
	}
	for pts := range got {
		if got[pts] != display[pts] {
			t.Fatalf("picture at PTS %d got %v in decode order, %v in display order", pts, got[pts], display[pts])
		}
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

// TestCaptionsResumeAfterTheStreamsTimestampsJumpBackwards covers a source that
// reconnects, or a 33-bit timestamp wrap. The clock then runs backwards by far
// more than any reorder window, and a schedule that kept its old slots would
// answer every later picture with the null pair until the clock climbed back
// past the old position. The captions must come back on the new timeline.
func TestCaptionsResumeAfterTheStreamsTimestampsJumpBackwards(t *testing.T) {
	s := NewSchedule(0)
	s.PairAt(100 * 90000) // a long-running stream, far along its clock

	// The stream reconnects and its timestamps restart near zero.
	s.Add(Cue{StartPTS: 90000, EndPTS: 3 * 90000, Text: "BACK"})
	got := drive(s, 0, 4*90000, tick30)
	first := PopOnScript("BACK")[0]
	found := false
	for _, p := range got {
		if p == first {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no caption went out after the timestamps jumped backwards; the schedule is stuck on its old slots")
	}
}

// TestScheduleKeepsItsSlotStateBoundedAcrossRepeatedClockJumps covers a clock that
// jumps forward and back many times. Each forward jump used to leave the slots
// from before it in the maps, so the maps grew with every jump. The state must
// stay around the reorder window however many jumps the stream makes.
func TestScheduleKeepsItsSlotStateBoundedAcrossRepeatedClockJumps(t *testing.T) {
	s := NewSchedule(0)
	clock := int64(10 * 90000)
	for i := 0; i < 2000; i++ {
		switch {
		case i%50 == 0:
			// A forward jump well past the reorder window, but inside the ten second
			// discontinuity threshold, so it is handled by finalise and not restart.
			clock += 2 * 90000
		case i%97 == 0:
			clock -= 90000 // a backward jump of one second, also inside the threshold
		default:
			clock += tick30
		}
		s.PairAt(clock)
	}
	if n := len(s.claimed) + len(s.slots) + len(s.used); n > 4*slotWindow {
		t.Fatalf("slot state holds %d entries after repeated clock jumps, want at most %d", n, 4*slotWindow)
	}
}

// TestScheduleIgnoresAClaimForASlotThatIsAlreadyFinal covers a late picture. Its
// slot is final, so claiming it again must not add state that nothing will ever
// remove.
func TestScheduleIgnoresAClaimForASlotThatIsAlreadyFinal(t *testing.T) {
	s := NewSchedule(0)
	for i := int64(0); i < 100; i++ {
		s.PairAt(100*90000 + i*tick30)
	}
	before := len(s.claimed)
	for i := 0; i < 50; i++ {
		s.Claim(100*90000 - int64(i+1)*slotTicks*3)
	}
	if after := len(s.claimed); after > before {
		t.Fatalf("claims for final slots grew the claimed map from %d to %d entries", before, after)
	}
}
