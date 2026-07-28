package transcribe

import (
	"strings"
	"testing"
)

// The decimal separator is the whole point of these two tests. A player handed
// an SRT with a period, or a VTT with a comma, shows nothing at all and reports
// no error, so this is pinned character by character.

func TestSRTTimestampsUseACommaBeforeTheMilliseconds(t *testing.T) {
	tests := []struct {
		name string
		ms   int64
		want string
	}{
		{"zero", 0, "00:00:00,000"},
		{"sub second keeps three digits", 5, "00:00:00,005"},
		{"tens of milliseconds are not truncated", 50, "00:00:00,050"},
		{"one and a half seconds", 1500, "00:00:01,500"},
		{"one minute", 60_000, "00:01:00,000"},
		{"one hour", 3_600_000, "01:00:00,000"},
		{"mixed", 3_723_456, "01:02:03,456"},
		{"past ten hours pads to two digits", 36_000_000, "10:00:00,000"},
		{"past a hundred hours does not wrap", 360_000_000, "100:00:00,000"},
		{"negative clamps to zero rather than emitting a minus sign", -5, "00:00:00,000"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatSRTTime(tc.ms)
			if got != tc.want {
				t.Errorf("FormatSRTTime(%d) = %q, want %q", tc.ms, got, tc.want)
			}
			if strings.Contains(got, ".") {
				t.Errorf("FormatSRTTime(%d) = %q contains a period; SRT uses a comma", tc.ms, got)
			}
		})
	}
}

func TestVTTTimestampsUseAPeriodBeforeTheMilliseconds(t *testing.T) {
	tests := []struct {
		name string
		ms   int64
		want string
	}{
		{"zero", 0, "00:00:00.000"},
		{"sub second keeps three digits", 5, "00:00:00.005"},
		{"one and a half seconds", 1500, "00:00:01.500"},
		{"mixed", 3_723_456, "01:02:03.456"},
		{"negative clamps to zero", -1000, "00:00:00.000"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatVTTTime(tc.ms)
			if got != tc.want {
				t.Errorf("FormatVTTTime(%d) = %q, want %q", tc.ms, got, tc.want)
			}
			if strings.Contains(got, ",") {
				t.Errorf("FormatVTTTime(%d) = %q contains a comma; WebVTT uses a period", tc.ms, got)
			}
		})
	}
}

func TestTheTwoFormattersDisagreeOnlyOnTheSeparator(t *testing.T) {
	for _, ms := range []int64{0, 1, 999, 1000, 61_001, 3_599_999, 7_200_000} {
		srt, vtt := FormatSRTTime(ms), FormatVTTTime(ms)
		if strings.ReplaceAll(srt, ",", ".") != vtt {
			t.Errorf("at %d ms: SRT %q and VTT %q differ by more than the separator", ms, srt, vtt)
		}
	}
}

func TestSRTNumbersCuesFromOneAndSeparatesThemWithABlankLine(t *testing.T) {
	segs := []Segment{
		{StartMS: 0, EndMS: 1500, Text: "Hello"},
		{StartMS: 1500, EndMS: 3000, Text: "world"},
	}
	want := "1\n00:00:00,000 --> 00:00:01,500\nHello\n\n" +
		"2\n00:00:01,500 --> 00:00:03,000\nworld\n\n"
	if got := SRT(segs, SubtitleOptions{}); got != want {
		t.Errorf("SRT =\n%q\nwant\n%q", got, want)
	}
}

func TestVTTStartsWithTheMandatoryHeader(t *testing.T) {
	got := VTT([]Segment{{StartMS: 0, EndMS: 1000, Text: "Hi"}}, SubtitleOptions{})
	if !strings.HasPrefix(got, "WEBVTT\n\n") {
		t.Fatalf("VTT does not start with the WEBVTT header:\n%q", got)
	}
	if !strings.Contains(got, "00:00:00.000 --> 00:00:01.000") {
		t.Errorf("VTT cue timing missing or wrongly formatted:\n%q", got)
	}
}

func TestVTTEscapesTheCharactersWebVTTReadsAsMarkup(t *testing.T) {
	tests := []struct {
		name, text, want string
	}{
		{"ampersand", "R&D", "R&amp;D"},
		{"angle brackets", "<laughs>", "&lt;laughs&gt;"},
		{"ampersand is escaped before the brackets", "a<b & c>d", "a&lt;b &amp; c&gt;d"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := VTT([]Segment{{EndMS: 1000, Text: tc.text}}, SubtitleOptions{})
			if !strings.Contains(got, tc.want) {
				t.Errorf("VTT body = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestSRTLeavesTextAloneBecauseSubRipHasNoEntityEscaping(t *testing.T) {
	got := SRT([]Segment{{EndMS: 1000, Text: "R&D"}}, SubtitleOptions{})
	if !strings.Contains(got, "R&D") {
		t.Errorf("SRT escaped text it should not have:\n%q", got)
	}
}

func TestSubtitlesCarrySpeakerPrefixesOnlyWhenAsked(t *testing.T) {
	segs := []Segment{{EndMS: 1000, Text: "Hello", Speaker: "Guest"}}
	if got := SRT(segs, SubtitleOptions{}); strings.Contains(got, "Guest") {
		t.Errorf("speaker leaked into a single-track file:\n%q", got)
	}
	if got := SRT(segs, SubtitleOptions{Speakers: true}); !strings.Contains(got, "Guest: Hello") {
		t.Errorf("speaker prefix missing:\n%q", got)
	}
}

func TestSubtitlesDropEmptySegmentsAndRepairInvertedCues(t *testing.T) {
	segs := []Segment{
		{StartMS: 2000, EndMS: 1000, Text: "backwards"},
		{StartMS: 0, EndMS: 500, Text: "   "},
		{StartMS: 500, EndMS: 900, Text: " trimmed "},
	}
	got := SRT(segs, SubtitleOptions{})
	if strings.Count(got, "-->") != 2 {
		t.Errorf("expected the blank segment to be dropped:\n%q", got)
	}
	if !strings.Contains(got, "00:00:02,000 --> 00:00:02,000") {
		t.Errorf("inverted cue was not repaired:\n%q", got)
	}
	if !strings.Contains(got, "\ntrimmed\n") {
		t.Errorf("text was not trimmed:\n%q", got)
	}
	// Ordering: the 500 ms cue must come first even though it was listed last.
	if strings.Index(got, "trimmed") > strings.Index(got, "backwards") {
		t.Errorf("segments were not ordered by start time:\n%q", got)
	}
}

func TestMinimumCueDurationStretchesShortCuesButNotOverTheNextOneOnTheSameTrack(t *testing.T) {
	tests := []struct {
		name string
		segs []Segment
		want string
	}{
		{
			name: "a short cue is stretched to the minimum",
			segs: []Segment{{StartMS: 0, EndMS: 40, Text: "Mm."}},
			want: "00:00:00,000 --> 00:00:01,000",
		},
		{
			name: "stretching stops at the next cue on the same track",
			segs: []Segment{
				{Track: 1, StartMS: 0, EndMS: 40, Text: "Mm."},
				{Track: 1, StartMS: 500, EndMS: 1500, Text: "Yes."},
			},
			want: "00:00:00,000 --> 00:00:00,500",
		},
		{
			name: "a cue on another track does not limit the stretch, because people interrupt",
			segs: []Segment{
				{Track: 1, StartMS: 0, EndMS: 40, Text: "Mm."},
				{Track: 2, StartMS: 500, EndMS: 1500, Text: "Yes."},
			},
			want: "00:00:00,000 --> 00:00:01,000",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SRT(tc.segs, SubtitleOptions{MinDurationMS: 1000})
			if !strings.Contains(got, tc.want) {
				t.Errorf("SRT =\n%q\nwant it to contain %q", got, tc.want)
			}
		})
	}
}

func TestPlainTextIsOneUtterancePerLine(t *testing.T) {
	segs := []Segment{
		{StartMS: 0, EndMS: 1000, Text: "Hello", Speaker: "Host"},
		{StartMS: 1000, EndMS: 2000, Text: "Hi", Speaker: "Guest"},
	}
	want := "Host: Hello\nGuest: Hi\n"
	if got := PlainText(segs, SubtitleOptions{Speakers: true}); got != want {
		t.Errorf("PlainText = %q, want %q", got, want)
	}
}

func TestSubtitleFormatExtensions(t *testing.T) {
	for _, f := range SubtitleFormats() {
		if !ValidFormat(f) {
			t.Errorf("%q is offered but not valid", f)
		}
		if got, want := f.Ext(), "."+string(f); got != want {
			t.Errorf("%q.Ext() = %q, want %q", f, got, want)
		}
	}
	if ValidFormat("ass") {
		t.Error("an unsupported format reported as valid")
	}
}

// A round trip through the parser is the strongest available check that the
// writer is self-consistent, and it catches a separator mistake in either
// direction.
func TestSubtitleTimestampsRoundTripThroughTheParser(t *testing.T) {
	for _, ms := range []int64{0, 7, 999, 1000, 59_999, 3_600_000, 3_723_456} {
		for name, s := range map[string]string{"srt": FormatSRTTime(ms), "vtt": FormatVTTTime(ms)} {
			got, ok := ParseTimestamp(s)
			if !ok || got != ms {
				t.Errorf("%s: ParseTimestamp(%q) = %d, %v; want %d, true", name, s, got, ok, ms)
			}
		}
	}
}
