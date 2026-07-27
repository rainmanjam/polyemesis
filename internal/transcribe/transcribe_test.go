package transcribe

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// This is the free-diarization claim, expressed as a test: two people on two
// tracks interleave into an attributed conversation, and no diarization model
// was consulted to produce it.
func TestMergedInterleavesTheTracksIntoAnAttributedConversation(t *testing.T) {
	tr := Transcript{Tracks: []TrackTranscript{
		{Track: 0, Speaker: "Host", Segments: []Segment{
			{Track: 0, Speaker: "Host", StartMS: 0, EndMS: 2000, Text: "Welcome back."},
			{Track: 0, Speaker: "Host", StartMS: 5000, EndMS: 6000, Text: "Exactly."},
		}},
		{Track: 1, Speaker: "Guest", Segments: []Segment{
			{Track: 1, Speaker: "Guest", StartMS: 2100, EndMS: 4800, Text: "Glad to be here."},
		}},
	}}

	got := tr.Merged()
	var order []string
	for _, s := range got {
		order = append(order, s.Speaker+": "+s.Text)
	}
	want := []string{"Host: Welcome back.", "Guest: Glad to be here.", "Host: Exactly."}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("merged =\n%v\nwant\n%v", order, want)
	}
	if tr.SegmentCount() != 3 {
		t.Errorf("SegmentCount = %d, want 3", tr.SegmentCount())
	}
	if got := tr.Speakers(); !reflect.DeepEqual(got, []string{"Host", "Guest"}) {
		t.Errorf("Speakers = %v", got)
	}
}

func TestMergedIsStableWhenTwoSpeakersStartInTheSameMillisecond(t *testing.T) {
	tr := Transcript{Tracks: []TrackTranscript{
		{Track: 2, Segments: []Segment{{Track: 2, StartMS: 1000, EndMS: 2000, Text: "b"}}},
		{Track: 0, Segments: []Segment{{Track: 0, StartMS: 1000, EndMS: 2000, Text: "a"}}},
	}}
	for i := 0; i < 5; i++ {
		got := tr.Merged()
		if got[0].Track != 0 || got[1].Track != 2 {
			t.Fatalf("run %d: order = %d, %d; ties must break on track index so a re-run diffs cleanly",
				i, got[0].Track, got[1].Track)
		}
	}
}

func TestEverySegmentCarriesItsOwnTrackIdentitySoTheMergedViewCannotLoseIt(t *testing.T) {
	tr := Transcript{Tracks: []TrackTranscript{
		{Track: 3, Speaker: "Guest", Segments: []Segment{{Track: 3, Speaker: "Guest", EndMS: 1000, Text: "hi"}}},
	}}
	for _, s := range tr.Merged() {
		if s.Track != 3 || s.Speaker != "Guest" {
			t.Errorf("segment = %+v, want the track identity denormalised onto it", s)
		}
	}
}

func TestNormalizeSegments(t *testing.T) {
	tests := []struct {
		name string
		in   []Segment
		want []Segment
	}{
		{
			name: "empty and whitespace-only text is dropped",
			in:   []Segment{{EndMS: 1000, Text: "  "}, {StartMS: 1000, EndMS: 2000, Text: "kept"}},
			want: []Segment{{StartMS: 1000, EndMS: 2000, Text: "kept"}},
		},
		{
			name: "text is trimmed, because whisper prefixes a space",
			in:   []Segment{{EndMS: 1000, Text: " hello "}},
			want: []Segment{{EndMS: 1000, Text: "hello"}},
		},
		{
			name: "a negative start is clamped",
			in:   []Segment{{StartMS: -500, EndMS: 1000, Text: "x"}},
			want: []Segment{{StartMS: 0, EndMS: 1000, Text: "x"}},
		},
		{
			name: "an end before its start is repaired rather than emitted",
			in:   []Segment{{StartMS: 2000, EndMS: 1000, Text: "x"}},
			want: []Segment{{StartMS: 2000, EndMS: 2000, Text: "x"}},
		},
		{
			name: "out-of-order segments are sorted",
			in:   []Segment{{StartMS: 2000, EndMS: 3000, Text: "b"}, {StartMS: 0, EndMS: 1000, Text: "a"}},
			want: []Segment{{StartMS: 0, EndMS: 1000, Text: "a"}, {StartMS: 2000, EndMS: 3000, Text: "b"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeSegments(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("NormalizeSegments = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestUniqueSpeakers(t *testing.T) {
	tests := []struct {
		name string
		in   map[int]string
		want map[int]string
	}{
		{
			name: "distinct labels are left alone",
			in:   map[int]string{0: "Host", 1: "Guest"},
			want: map[int]string{0: "Host", 1: "Guest"},
		},
		{
			name: "a collision suffixes BOTH, so neither reads as the real one",
			in:   map[int]string{0: "Mic", 2: "Mic"},
			want: map[int]string{0: "Mic (track 1)", 2: "Mic (track 3)"},
		},
		{
			name: "three the same",
			in:   map[int]string{0: "Mic", 1: "Mic", 2: "Mic"},
			want: map[int]string{0: "Mic (track 1)", 1: "Mic (track 2)", 2: "Mic (track 3)"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := UniqueSpeakers(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("UniqueSpeakers = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSegmentDuration(t *testing.T) {
	tests := []struct {
		name  string
		seg   Segment
		wantS float64
	}{
		{"an ordinary segment", Segment{StartMS: 1000, EndMS: 3500}, 2.5},
		{"a zero-length segment", Segment{StartMS: 1000, EndMS: 1000}, 0},
		{"an inverted segment reports nothing rather than a negative", Segment{StartMS: 2000, EndMS: 1000}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.seg.Duration().Seconds(); got != tc.wantS {
				t.Errorf("Duration = %vs, want %vs", got, tc.wantS)
			}
		})
	}
}

// A confidence of zero and an unknown confidence must not serialise the same
// way: one is the model saying the text is garbage, the other is nobody asking.
func TestAnUnknownConfidenceIsNotSerialisedAsZero(t *testing.T) {
	known, _ := json.Marshal(Segment{Text: "x", Confidence: 0.9, ConfidenceKnown: true})
	unknown, _ := json.Marshal(Segment{Text: "x"})

	if !strings.Contains(string(known), `"confidence":0.9`) {
		t.Errorf("known confidence = %s", known)
	}
	if strings.Contains(string(unknown), "confidence") {
		t.Errorf("unknown confidence leaked into the JSON as a value: %s", unknown)
	}
}

func TestTrackTranscriptTextIsOneUtterancePerLine(t *testing.T) {
	tt := TrackTranscript{Segments: []Segment{{Text: "one"}, {Text: ""}, {Text: "two"}}}
	if got := tt.Text(); got != "one\ntwo\n" {
		t.Errorf("Text = %q", got)
	}
}
