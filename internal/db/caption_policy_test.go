package db

import (
	"reflect"
	"strings"
	"testing"
)

// TestSourceCaptionPolicyRoundTripsTrimmedAndDeduplicated proves the operator's
// caption text policy (#8: custom vocabulary and the words to mask) survives a
// write and a read, per programme, in the form the captioner applies it: each
// entry trimmed, blanks dropped, and a repeat that differs only in case kept
// once, as first written. A new programme has no policy, so an upgrade changes
// no caption on screen.
//
// MUTATION: caption_policy dropped from the sources UPDATE. Observed: FAIL, the
// policy read back empty.
func TestSourceCaptionPolicyRoundTripsTrimmedAndDeduplicated(t *testing.T) {
	d := testDB(t)
	src := &Source{Name: "Programme A", Ingest: DefaultSettings().Ingest}
	if err := d.CreateSource(src); err != nil {
		t.Fatalf("create source: %v", err)
	}
	got, err := d.GetSource(src.ID)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if len(got.CaptionPolicy.Vocabulary) != 0 || len(got.CaptionPolicy.Blocklist) != 0 {
		t.Fatalf("new source caption policy = %+v, want empty", got.CaptionPolicy)
	}
	got.CaptionPolicy = CaptionPolicy{
		Vocabulary: []string{" Polyemesis ", "", "POLYEMESIS", "OBS"},
		Blocklist:  []string{"darn", "Darn", "  heck"},
	}
	if err := d.UpdateSource(got); err != nil {
		t.Fatalf("set caption policy: %v", err)
	}
	again, err := d.GetSource(src.ID)
	if err != nil {
		t.Fatalf("read after update: %v", err)
	}
	want := CaptionPolicy{Vocabulary: []string{"Polyemesis", "OBS"}, Blocklist: []string{"darn", "heck"}}
	if !reflect.DeepEqual(again.CaptionPolicy, want) {
		t.Fatalf("caption policy after update = %+v, want %+v", again.CaptionPolicy, want)
	}
	again.CaptionPolicy = CaptionPolicy{}
	if err := d.UpdateSource(again); err != nil {
		t.Fatalf("clear caption policy: %v", err)
	}
	cleared, err := d.GetSource(src.ID)
	if err != nil {
		t.Fatalf("read after clear: %v", err)
	}
	if len(cleared.CaptionPolicy.Vocabulary) != 0 || len(cleared.CaptionPolicy.Blocklist) != 0 {
		t.Fatalf("cleared caption policy = %+v, want empty", cleared.CaptionPolicy)
	}
}

// TestSourceCaptionPolicyRefusesWhatTheCaptionerCouldNeverMatch treats the
// policy as hostile input. The captioner matches whole words, so a phrase, a
// hyphenated term or a control character could never match and would only sit
// in the list looking as if it worked; an over-long entry or an over-long list
// is a paste accident or an attack on the emit path's per-caption cost. Each is
// refused before it is stored, with the reason.
//
// MUTATION: the single-word check removed. Observed: FAIL, "two words" stored.
func TestSourceCaptionPolicyRefusesWhatTheCaptionerCouldNeverMatch(t *testing.T) {
	d := testDB(t)
	long := strings.Repeat("a", MaxCaptionPolicyWordLength+1)
	many := make([]string, MaxCaptionPolicyEntries+1)
	for i := range many {
		many[i] = "w" + strings.Repeat("x", i%40) + string(rune('a'+i%26)) + strings.Repeat("y", i/26)
	}
	for _, bad := range []CaptionPolicy{
		{Vocabulary: []string{"two words"}},
		{Blocklist: []string{"e-sports"}},
		{Vocabulary: []string{"bell\x07"}},
		{Blocklist: []string{long}},
		{Vocabulary: many},
	} {
		src := &Source{Name: "Programme", Ingest: DefaultSettings().Ingest, CaptionPolicy: bad}
		if err := d.CreateSource(src); err == nil {
			t.Fatalf("caption policy with %d vocabulary and %d blocked entries (first %.40q) accepted, want refusal",
				len(bad.Vocabulary), len(bad.Blocklist), firstOf(bad))
		}
	}
	ok := &Source{Name: "Programme", Ingest: DefaultSettings().Ingest,
		CaptionPolicy: CaptionPolicy{Vocabulary: []string{"don't", "Zoë", "4K"}}}
	if err := d.CreateSource(ok); err != nil {
		t.Fatalf("a word with an inner apostrophe, an accent or a digit refused: %v", err)
	}
}

// firstOf names the first entry of a policy, for a refusal message.
func firstOf(p CaptionPolicy) string {
	if len(p.Vocabulary) > 0 {
		return p.Vocabulary[0]
	}
	if len(p.Blocklist) > 0 {
		return p.Blocklist[0]
	}
	return ""
}

// TestARenditionsCaptionBurnInIsAPositionAndNeedsAFixedFrame keeps the stored
// burn-in setting to the positions the engine draws, and refuses it on a tier
// whose frame size is left free: the captions are sized against the output
// picture, and with an axis free the drawtext filter is never added, so the
// setting would look on and draw nothing.
//
// MUTATION: the size rule removed. Observed: FAIL, a height-only tier accepted.
func TestARenditionsCaptionBurnInIsAPositionAndNeedsAFixedFrame(t *testing.T) {
	base := func(b CaptionBurn, w, h int) *Rendition {
		r := validRendition()
		r.Width, r.Height, r.CaptionBurn = w, h, b
		return r
	}
	for _, ok := range []*Rendition{base(CaptionBurnOff, 0, 720), base(CaptionBurnBottom, 1280, 720), base(CaptionBurnTop, 1280, 720)} {
		if err := ok.Validate(); err != nil {
			t.Errorf("caption burn %q at %dx%d refused: %v", ok.CaptionBurn, ok.Width, ok.Height, err)
		}
	}
	for _, bad := range []*Rendition{base("middle", 1280, 720), base(CaptionBurnBottom, 0, 720)} {
		if err := bad.Validate(); err == nil {
			t.Errorf("caption burn %q at %dx%d accepted, want refusal", bad.CaptionBurn, bad.Width, bad.Height)
		}
	}
}
