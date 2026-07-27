package transcribe

import (
	"testing"
)

func TestParseJSONReadsOffsetsTextAndConfidence(t *testing.T) {
	raw := []byte(`{
      "result": {"language": "en"},
      "transcription": [
        {"timestamps": {"from": "00:00:00,000", "to": "00:00:02,000"},
         "offsets": {"from": 0, "to": 2000},
         "text": " Hello world",
         "tokens": [
           {"text": "[_BEG_]", "p": 0.01},
           {"text": " Hello", "p": 0.9},
           {"text": " world", "p": 0.7},
           {"text": "[_TT_100]", "p": 0.02}
         ]},
        {"offsets": {"from": 2000, "to": 4000}, "text": " Second"}
      ]}`)
	var lang string
	segs, err := ParseJSON(raw, &lang)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if lang != "en" {
		t.Errorf("language = %q, want en", lang)
	}
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want 2", len(segs))
	}
	if segs[0].Text != "Hello world" {
		t.Errorf("text = %q, want the leading space trimmed", segs[0].Text)
	}
	if segs[0].StartMS != 0 || segs[0].EndMS != 2000 {
		t.Errorf("offsets = %d..%d, want 0..2000", segs[0].StartMS, segs[0].EndMS)
	}
	// The special tokens must be excluded, or the mean is 0.4 rather than 0.8.
	if !segs[0].ConfidenceKnown || segs[0].Confidence < 0.79 || segs[0].Confidence > 0.81 {
		t.Errorf("confidence = %v (known %v), want ~0.8 from the two real tokens",
			segs[0].Confidence, segs[0].ConfidenceKnown)
	}
	if segs[1].ConfidenceKnown {
		t.Error("a segment with no tokens must report an UNKNOWN confidence, not a confidence of zero")
	}
}

func TestParseJSONFallsBackToTheFormattedTimestampsWhenOffsetsAreAbsent(t *testing.T) {
	raw := []byte(`{"transcription":[
      {"timestamps":{"from":"00:00:01,250","to":"00:00:03,500"},"offsets":{"from":0,"to":0},"text":"x"}]}`)
	segs, err := ParseJSON(raw, nil)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if len(segs) != 1 || segs[0].StartMS != 1250 || segs[0].EndMS != 3500 {
		t.Fatalf("segments = %+v, want one segment at 1250..3500", segs)
	}
}

func TestParseJSONRejectsGarbage(t *testing.T) {
	if _, err := ParseJSON([]byte("not json"), nil); err == nil {
		t.Fatal("expected an error from malformed JSON")
	}
}

func TestParseSegmentLine(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		want   Segment
		wantOK bool
	}{
		{
			name:   "the ordinary case",
			line:   "[00:00:00.000 --> 00:00:02.000]   Hello world",
			want:   Segment{StartMS: 0, EndMS: 2000, Text: "Hello world"},
			wantOK: true,
		},
		{
			name:   "a comma separator is accepted on the read side",
			line:   "[00:00:01,500 --> 00:00:02,750]  Yes",
			want:   Segment{StartMS: 1500, EndMS: 2750, Text: "Yes"},
			wantOK: true,
		},
		{
			name:   "colour codes are stripped out of the text",
			line:   "[00:00:00.000 --> 00:00:01.000]  \x1b[38;5;196mred\x1b[0m text",
			want:   Segment{StartMS: 0, EndMS: 1000, Text: "red text"},
			wantOK: true,
		},
		{name: "a banner line is not a segment", line: "whisper_init_from_file: loading model"},
		{name: "an empty line is not a segment", line: ""},
		{name: "a timing line is not a segment", line: "whisper_print_timings:     load time =   100.00 ms"},
		{name: "a segment with no text is dropped", line: "[00:00:00.000 --> 00:00:01.000]   "},
		{name: "an impossible minute count is rejected", line: "[00:99:00.000 --> 00:00:01.000] x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseSegmentLine(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Errorf("segment = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseStdoutKeepsOnlyTheSegmentLines(t *testing.T) {
	out := `whisper_init_from_file_with_params_no_state: loading model from 'ggml-base.bin'
system_info: n_threads = 4 / 8 | AVX = 1 | METAL = 1 |

[00:00:00.000 --> 00:00:02.000]   First line
[00:00:02.000 --> 00:00:04.000]   Second line

whisper_print_timings:     total time =  1234.56 ms
`
	segs := ParseStdout(out)
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want 2: %+v", len(segs), segs)
	}
	if segs[0].Text != "First line" || segs[1].StartMS != 2000 {
		t.Errorf("segments = %+v", segs)
	}
}

func TestParseTimestamp(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   int64
		wantOK bool
	}{
		{"period separator", "00:00:01.500", 1500, true},
		{"comma separator", "00:00:01,500", 1500, true},
		{"unpadded hours", "1:02:03.004", 3_723_004, true},
		{"surrounding space", "  00:00:01.000  ", 1000, true},
		{"minutes out of range", "00:60:00.000", 0, false},
		{"seconds out of range", "00:00:60.000", 0, false},
		{"two-digit fraction is not a timestamp", "00:00:01.50", 0, false},
		{"no fraction at all", "00:00:01", 0, false},
		{"empty", "", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseTimestamp(tc.in)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("ParseTimestamp(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestParseProgressLine(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		want   float64
		wantOK bool
	}{
		{"the whisper progress callback", "whisper_print_progress_callback: progress =  35%", 0.35, true},
		{"no space around the equals", "progress=100%", 1, true},
		{"zero", "progress =   0%", 0, true},
		{"an absurd percentage is clamped rather than rejected", "progress = 250%", 1, true},
		{"an unrelated line", "whisper_init: loading model", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseProgressLine(tc.line)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("ParseProgressLine(%q) = %v, %v; want %v, %v", tc.line, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestIsNoteworthyKeepsFailuresAndDropsChatter(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"error: failed to open 'x.wav' as WAV file", true},
		{"whisper_init_from_file: cannot open model", true},
		{"ggml_metal_init: found device, but unable to use it", true},
		{"whisper_model_load: n_vocab = 51865", false},
		{"system_info: n_threads = 4", false},
		{"", false},
		{"   ", false},
	}
	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			if got := IsNoteworthy(tc.line); got != tc.want {
				t.Errorf("IsNoteworthy(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}
