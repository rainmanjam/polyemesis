package captions

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// TestVocabularyFixesTheSpellingOfWholeWordsRegardlessOfCase is the custom
// vocabulary rule. A name the recogniser hears in lower case must come out as the
// operator spelled it, and only as a whole word.
func TestVocabularyFixesTheSpellingOfWholeWordsRegardlessOfCase(t *testing.T) {
	p := TextPolicy{Vocabulary: []string{"PolyEmesis", "OBS"}}
	cases := map[string]string{
		"we run polyemesis live":        "we run PolyEmesis live",
		"POLYEMESIS starts at noon":     "PolyEmesis starts at noon",
		"open obs and press start":      "open OBS and press start",
		"polyemesistic is not the name": "polyemesistic is not the name",
		"obsolete code":                 "obsolete code",
	}
	for in, want := range cases {
		if got := p.Apply(in); got != want {
			t.Errorf("Apply(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestProfanityFilterMasksWholeWordsKeepingTheirShape pins the mask. The first
// letter stays and the length stays, which reads as a masked word rather than a
// gap, and the match is on the word, not the letters inside it.
func TestProfanityFilterMasksWholeWordsKeepingTheirShape(t *testing.T) {
	p := TextPolicy{Blocklist: []string{"damn"}}
	cases := map[string]string{
		"well damn that was close":      "well d*** that was close",
		"DAMN, it worked":               "D***, it worked",
		"damnation is a different word": "damnation is a different word",
	}
	for in, want := range cases {
		if got := p.Apply(in); got != want {
			t.Errorf("Apply(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestProfanityFilterLeavesWordsThatOnlyContainABlockedWordAlone. This is the
// classic over-match: a filter that finds "ass" inside "class" or "assess" makes
// the captions unreadable and is the first thing an operator turns off.
func TestProfanityFilterLeavesWordsThatOnlyContainABlockedWordAlone(t *testing.T) {
	p := TextPolicy{Blocklist: []string{"ass"}}
	in := "the class will assess the grass"
	if got := p.Apply(in); got != in {
		t.Fatalf("Apply(%q) = %q; words that only contain a blocked word must pass", in, got)
	}
}

// TestPolicyWithNothingSetReturnsTheTextUnchanged keeps the default path free:
// no policy means no rewriting at all, not a rewrite that happens to be a no-op.
func TestPolicyWithNothingSetReturnsTheTextUnchanged(t *testing.T) {
	in := "  Mixed   CASE, with punctuation!  "
	if got := (TextPolicy{}).Apply(in); got != in {
		t.Fatalf("Apply on an empty policy = %q, want the input unchanged", got)
	}
}

// TestPolicyKeepsPunctuationAndSpacingBetweenWords checks that the rewrite only
// touches the word runs. A caption that loses its commas or doubles its spaces
// has been damaged by the filter, not corrected.
func TestPolicyKeepsPunctuationAndSpacingBetweenWords(t *testing.T) {
	p := TextPolicy{Blocklist: []string{"darn"}, Vocabulary: []string{"Go"}}
	got := p.Apply("darn,  it's go-time!")
	if want := "d***,  it's Go-time!"; got != want {
		t.Fatalf("Apply = %q, want %q", got, want)
	}
}

// TestPolicyAppliesToEverySegmentWithoutMovingIt is the emit-boundary rule. The
// text changes, but the timing, the track and the speaker of each cue do not, so
// a masked cue still lands on the words it was spoken over.
func TestPolicyAppliesToEverySegmentWithoutMovingIt(t *testing.T) {
	segs := []transcribe.Segment{
		{Track: 1, Speaker: "Host", StartMS: 1000, EndMS: 2500, Text: "well damn"},
		{Track: 0, Speaker: "Guest", StartMS: 3000, EndMS: 4000, Text: "polyemesis"},
	}
	p := TextPolicy{Blocklist: []string{"damn"}, Vocabulary: []string{"PolyEmesis"}}
	got := p.Segments(segs)
	if got[0].Text != "well d***" || got[1].Text != "PolyEmesis" {
		t.Fatalf("texts = %q, %q", got[0].Text, got[1].Text)
	}
	for i := range segs {
		if got[i].StartMS != segs[i].StartMS || got[i].EndMS != segs[i].EndMS ||
			got[i].Track != segs[i].Track || got[i].Speaker != segs[i].Speaker {
			t.Fatalf("segment %d moved or changed identity: %+v -> %+v", i, segs[i], got[i])
		}
	}
	if segs[0].Text != "well damn" {
		t.Fatalf("Segments modified its input slice in place")
	}
}
