package captions

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// unitWithMidPCR is one video PES split over two packets, with a PCR in the
// adaptation field of the SECOND packet, as a muxer that carries the PCR on the
// video PID writes a large picture. The injector cannot rebuild such a unit
// without moving the PCR, so it goes out unchanged.
func unitWithMidPCR(cc *uint8, pts int64) []byte {
	pes := append(pesHeader(pts), auFor(false)...)
	// A filler NAL long enough to push the unit into a second packet.
	filler := make([]byte, 220)
	filler[0] = 0x0C
	for i := 1; i < len(filler); i++ {
		filler[i] = 0xFF
	}
	pes = append(pes, annexB(filler)...)
	first := pes[:184]
	rest := pes[184:]
	out := tsPkt(testVideoPID, true, *cc, nil, first)
	*cc = (*cc + 1) & 0x0f
	pcrAF := []byte{0x10, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	out = append(out, tsPkt(testVideoPID, false, *cc, pcrAF, rest)...)
	*cc = (*cc + 1) & 0x0f
	return out
}

// A picture that goes out without a caption (here, a PCR inside it) must not
// spend the caption pair its slot was due. The slot was claimed when the picture
// started, and before this the claimed slot took the next pair off the queue and
// nobody ever sent it: a script missing its RCL or its EOC never shows, and one
// missing its erase never clears. The pair now waits for the next picture.
func TestAPictureLeftWithoutACaptionDoesNotLoseThePairItsSlotWasDue(t *testing.T) {
	var cc uint8
	in := psFixture(0x1B)
	cueAt := int64(90000 + 1*frameTicks)
	for d := 0; d < 30; d++ {
		pts := int64(90000 + d*frameTicks)
		if d == 1 {
			in = append(in, unitWithMidPCR(&cc, pts)...)
			continue
		}
		in = append(in, pesPackets(testVideoPID, &cc, append(pesHeader(pts), auFor(d == 0)...), nil)...)
	}
	sched := NewSchedule(0)
	sched.Add(Cue{StartPTS: cueAt, EndPTS: cueAt + 60*90000, Text: "HI"})
	inj := NewInjector(sched)
	out := append(inj.Write(in), inj.Flush()...)
	if inj.Stats().PCRInMiddle != 1 {
		t.Fatalf("PCRInMiddle = %d, want 1: the fixture did not exercise the mid-PCR path", inj.Stats().PCRInMiddle)
	}

	units := videoUnits(t, parsePackets(t, out), testVideoPID)
	sort.Slice(units, func(i, j int) bool { return units[i].pts < units[j].pts })
	var got []Pair
	for _, u := range units {
		for _, nal := range splitAnnexB(u.es) {
			if p, ok := ccPairOf(nal); ok && p != nullPair {
				got = append(got, p)
			}
		}
	}
	want := PopOnScript("HI")
	if len(got) < len(want) || !reflect.DeepEqual(got[:len(want)], want) {
		t.Fatalf("caption pairs = %v, want the whole script %v: a pair was spent on the uncaptioned picture", got, want)
	}
}

// PTSNow is the newest presentation time, not the last decoded. A B-frame
// decoded after its P-frame is earlier on screen; a caption keyed to it would be
// stamped before the caption before it.
func TestTheInjectorsClockIsTheNewestPictureNotTheLastDecoded(t *testing.T) {
	inj := NewInjector(NewSchedule(0))
	_ = inj.Write(streamOf([]int{0, 3, 1, 2}))
	pts, ok := inj.PTSNow()
	if want := int64(90000 + 3*frameTicks); !ok || pts != want {
		t.Fatalf("PTSNow = %d %v, want the P-frame's %d", pts, ok, want)
	}
}

// An encoder that reconnects a few seconds in, with its clock starting again
// near zero, is a discontinuity. Before this, a backward jump under ten seconds
// was read as reordering: every new picture fell below the cursor and was
// ignored, and captions stopped until the clock caught up.
func TestAShortBackwardClockJumpRestartsTheSchedule(t *testing.T) {
	s := NewSchedule(0)
	for i := int64(0); i < 5*30; i++ {
		s.Claim(90000 + i*slotTicks)
	}
	if !s.Claim(90000) {
		t.Fatal("a five-second backward jump was not treated as a discontinuity")
	}
	// Reordering within a B-frame group is not a jump.
	s = NewSchedule(0)
	for i := int64(0); i < 60; i++ {
		s.Claim(90000 + i*slotTicks)
	}
	if s.Claim(90000 + 55*slotTicks) {
		t.Fatal("a picture four slots behind the newest was treated as a discontinuity")
	}
}

// A caption that arrives just as the stream's clock jumps is placed on the new
// timeline. Feed used to place queued captions before the datagram went through
// the injector, so the caption was stamped on the old clock and the restart that
// the jump triggered threw it away.
func TestACaptionQueuedAcrossAClockJumpIsPlacedOnTheNewTimeline(t *testing.T) {
	st := NewSEIStage()
	var cc uint8
	old := psFixture(0x1B)
	for i := 0; i < 30; i++ {
		old = append(old, pesPackets(testVideoPID, &cc, append(pesHeader(int64(400*90000+i*frameTicks)), auFor(false)...), nil)...)
	}
	_ = st.Feed(old)
	st.Cue("AFTER THE JUMP", 3*time.Second)
	jump := pesPackets(testVideoPID, &cc, append(pesHeader(int64(2*90000)), auFor(true)...), nil)
	_ = st.Feed(jump)
	found := false
	for _, e := range st.sched.events {
		if e.kind == eventScript && e.at < 10*90000 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the caption was not placed on the new timeline; events = %+v", st.sched.events)
	}
}

// A roll that cannot reopen the file must not silence the log for the rest of
// the show: the next Append opens it again.
func TestACueLogWhoseRollFailedOpensAgainOnTheNextAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cues.jsonl")
	l, err := OpenCueLog(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	// What a roll whose open failed leaves behind.
	l.mu.Lock()
	_ = l.f.Close()
	l.f = nil
	l.mu.Unlock()

	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if err := l.Append(transcribe.LiveCaption{Text: "still logged", At: at, EndMS: 1000}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	cues, err := ReadCueLog(path, at.Add(-time.Hour), at.Add(time.Hour))
	if err != nil || len(cues) != 1 || cues[0].Text != "still logged" {
		t.Fatalf("cues = %+v, %v; want the caption logged after the failed roll", cues, err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(transcribe.LiveCaption{Text: "after close", At: at}); err != nil {
		t.Fatal(err)
	}
	if cues, _ := ReadCueLog(path, at.Add(-time.Hour), at.Add(time.Hour)); len(cues) != 1 {
		t.Fatalf("a closed log reopened itself: %+v", cues)
	}
}

// A program map with many streams or descriptors spans packets. Before this the
// injector read only the first packet, never learned the H.264 PID, and no
// caption was ever injected. The section is collected until it is whole.
func TestAProgramMapThatSpansTwoPacketsStillNamesTheVideoPID(t *testing.T) {
	const pil = 250
	body := []byte{0x00, 0x01, 0xC1, 0x00, 0x00, 0xE0 | 0x01, 0x00, 0xF0 | byte(pil>>8), byte(pil & 0xFF)}
	desc := make([]byte, pil)
	desc[0], desc[1] = 0x05, byte(pil-2) // a registration descriptor's shape, padded
	body = append(body, desc...)
	body = append(body, streamTypeH264, 0xE0|0x01, 0x00, 0xF0, 0x00) // video ES on 0x0100
	body = append(body, 0x0F, 0xE0|0x01, 0x01, 0xF0, 0x00)           // audio ES
	body = append(body, 0, 0, 0, 0)                                  // CRC, not checked
	n := len(body)
	section := append([]byte{0x02, 0xB0 | byte(n>>8), byte(n)}, body...)
	payload := append([]byte{0x00}, section...) // pointer field
	first, rest := payload[:184], payload[184:]

	inj := NewInjector(NewSchedule(0))
	_ = inj.Write(psiPacket(0, 0, patSection(testPMTPID)))
	_ = inj.Write(tsPkt(testPMTPID, true, 0, nil, first))
	if inj.videoPID != 0 {
		t.Fatalf("the video PID was read from half a map: %#x", inj.videoPID)
	}
	_ = inj.Write(tsPkt(testPMTPID, false, 1, nil, rest))
	if inj.videoPID != testVideoPID {
		t.Fatalf("videoPID = %#x after the whole map, want %#x", inj.videoPID, testVideoPID)
	}
}

// An HEVC source passes through the SEI stage unchanged, because the A/53 SEI is
// an H.264 construct. The stage says so once, so the engine can warn that this
// destination gets no captions, rather than showing captions as running.
func TestAnSEIStageSaysOnceWhenTheVideoIsHEVC(t *testing.T) {
	st := NewSEIStage()
	n := 0
	st.OnUnsupported(func() { n++ })
	_ = st.Feed(psFixture(streamTypeHEVC))
	_ = st.Feed(psiPacket(testPMTPID, 1, pmtSection(streamTypeHEVC, 0x0F)))
	if n != 1 {
		t.Fatalf("told %d times about one HEVC stream, want once", n)
	}
}
