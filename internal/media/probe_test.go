package media

import (
	"strings"
	"testing"
)

// A three-track master, which is the shape this whole product exists to
// produce and the shape the verifier has to read correctly.
const multitrackProbeJSON = `{
  "streams": [
    {"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"pix_fmt":"yuv420p","avg_frame_rate":"30000/1001"},
    {"codec_type":"audio","codec_name":"aac","channels":2,"channel_layout":"stereo","sample_rate":"48000","tags":{"language":"eng","title":"Host"}},
    {"codec_type":"audio","codec_name":"aac","channels":1,"channel_layout":"mono","sample_rate":"48000","tags":{"language":"eng","title":"Guest"}},
    {"codec_type":"audio","codec_name":"aac","channels":2,"channel_layout":"stereo","sample_rate":"48000","tags":{"title":"Music"}}
  ],
  "format": {"duration":"3600.041000","size":"10737418240"}
}`

func TestParseSummaryReadsEveryAudioTrackWithItsLabel(t *testing.T) {
	got, err := ParseSummary([]byte(multitrackProbeJSON), "rec-1.mkv", 0)
	if err != nil {
		t.Fatalf("ParseSummary: %v", err)
	}

	if got.VideoCodec != "h264" {
		t.Fatalf("VideoCodec = %q", got.VideoCodec)
	}
	if got.DurationSeconds < 3600 || got.DurationSeconds > 3601 {
		t.Fatalf("DurationSeconds = %v", got.DurationSeconds)
	}
	if got.Bytes != 10737418240 {
		t.Fatalf("Bytes = %d", got.Bytes)
	}
	want := []TrackSummary{
		{Index: 0, Codec: "aac", Channels: 2, Language: "eng", Title: "Host"},
		{Index: 1, Codec: "aac", Channels: 1, Language: "eng", Title: "Guest"},
		{Index: 2, Codec: "aac", Channels: 2, Title: "Music"},
	}
	if len(got.Audio) != len(want) {
		t.Fatalf("got %d audio tracks, want %d", len(got.Audio), len(want))
	}
	for i, w := range want {
		if got.Audio[i] != w {
			t.Fatalf("track %d = %+v, want %+v", i, got.Audio[i], w)
		}
	}
}

// A stat is more trustworthy than ffprobe's own size, which is what it managed
// to read — and for a file still being written that is not what it ends up.
func TestParseSummaryPrefersTheCallersStatOverFFprobesSize(t *testing.T) {
	got, err := ParseSummary([]byte(multitrackProbeJSON), "rec-1.mkv", 42)
	if err != nil {
		t.Fatalf("ParseSummary: %v", err)
	}
	if got.Bytes != 42 {
		t.Fatalf("Bytes = %d, want 42", got.Bytes)
	}
}

func TestParseSummaryDegradesRatherThanFailingOnAnIncompleteProbe(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want FileSummary
	}{
		{"no format block", `{"streams":[]}`, FileSummary{}},
		{"an unparseable duration", `{"streams":[],"format":{"duration":"N/A"}}`, FileSummary{}},
		{"a negative duration", `{"streams":[],"format":{"duration":"-1"}}`, FileSummary{}},
		{"nothing at all", `{}`, FileSummary{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSummary([]byte(tc.raw), "x.mkv", 0)
			if err != nil {
				t.Fatalf("ParseSummary: %v", err)
			}
			if got.DurationSeconds != tc.want.DurationSeconds {
				t.Fatalf("DurationSeconds = %v, want %v", got.DurationSeconds, tc.want.DurationSeconds)
			}
			if len(got.Audio) != 0 || got.VideoCodec != "" {
				t.Fatalf("got %+v from an empty probe", got)
			}
		})
	}
}

func TestParseSummaryRejectsOutputItCannotRead(t *testing.T) {
	if _, err := ParseSummary([]byte("not json"), "x.mkv", 0); err == nil {
		t.Fatal("ParseSummary accepted output that is not JSON")
	}
}

func TestProbeArgsAsksForBothTheStreamsAndTheContainerDuration(t *testing.T) {
	args := ProbeArgs("/rec/rec-1.mkv")
	for _, want := range []string{"-show_format", "-show_streams"} {
		if !hasArg(args, want) {
			t.Fatalf("%s is missing: %v", want, args)
		}
	}
	mustArg(t, args, "-print_format", "json")
	if args[len(args)-1] != "/rec/rec-1.mkv" {
		t.Fatalf("the path is not last: %v", args)
	}
}

// Without explicit maps FFmpeg's default stream selection decodes ONE audio
// track, so a null pass over a six-track master would declare the file healthy
// while track four was corrupt.
func TestDecodeCheckArgsDecodesEveryStreamNotJustTheDefaultOne(t *testing.T) {
	args := DecodeCheckArgs("/rec/media/r/archive.mkv")

	if !hasArg(args, "0:v") || !hasArg(args, "0:a") {
		t.Fatalf("the check does not cover every stream: %v", args)
	}
	if !hasArg(args, "-xerror") {
		t.Fatalf("-xerror is missing, so the pass would not stop at the first fault: %v", args)
	}
	mustArg(t, args, "-f", "null")
	mustArg(t, args, "-v", "error")
	if args[len(args)-1] != "-" {
		t.Fatalf("the null output is not last: %v", args)
	}
}

func TestDecodeErrorsCollapsesTheTranscriptToDistinctComplaints(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"a healthy file says nothing", "", nil},
		{"whitespace only", "\n  \n\t\n", nil},
		{
			"the same complaint thousands of times",
			strings.Repeat("[hevc @ 0x1] Invalid NAL unit size\n", 500),
			[]string{"[hevc @ 0x1] Invalid NAL unit size"},
		},
		{
			"two distinct complaints keep their order",
			"first problem\nsecond problem\nfirst problem\n",
			[]string{"first problem", "second problem"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DecodeErrors(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("DecodeErrors = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("DecodeErrors[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
