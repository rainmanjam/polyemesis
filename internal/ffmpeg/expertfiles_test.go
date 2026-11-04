package ffmpeg

import (
	"strings"
	"testing"
)

// Bypasses the codex and agy reviews of the expert check found after the
// security reviews: each must be refused by the walk the API and the engine's
// spawn path share.
func TestCheckExpertFileAccessRefusesTheReviewedBypasses(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		argv        []string
		want        string
	}{
		{"-i in output args reads a server file", "output args", []string{"-i", "/etc/passwd"}, "-i adds an input"},
		{"-i at spawn on the input side", "input args", []string{"-i", "/dev/video0"}, "-i adds an input"},
		{"escaped x264 key", "output args", []string{"-x264-params", `d\ump-yuv=/tmp/y`}, "dump-yuv"},
		{"underscore x264 key", "output args", []string{"-x264-params", "dump_yuv=/tmp/y"}, "dump-yuv"},
		{"quoted x265 key", "output args", []string{"-x265-params", `c's'v=/tmp/x.csv`}, "csv"},
		{"x265 nalu-file", "output args", []string{"-x265-params", "nalu-file=/etc/passwd"}, "nalu-file"},
		{"upper-case key", "output args", []string{"-x264-params", "STATS=/tmp/s"}, "stats"},
		{"fifo wraps any muxer", "output args", []string{"-f", "fifo"}, "-f fifo"},
		{"hds writes files", "output args", []string{"-f", "hds"}, "-f hds"},
		{"an output device", "output args", []string{"-f", "fbdev"}, "-f fbdev"},
		{"lavfi on the input side", "input args", []string{"-f", "lavfi"}, "-f lavfi"},
		{"flv on the input side", "input args", []string{"-f", "flv"}, "-f flv"},
		{"a nested option dictionary", "output args", []string{"-format_opts", "hls_segment_filename=/tmp/x"}, "passes its own option list"},
		{"-apad loading a value from a file", "output args", []string{"-apad", "/whole_len=/etc/passwd"}, "-apad"},
		{"gpu selection", "output args", []string{"-gpu", "0"}, "-gpu"},
		{"hwaccel selection", "input args", []string{"-hwaccel", "vaapi"}, "-hwaccel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckExpertFileAccess(tc.field, tc.argv)
			if err == nil {
				t.Fatalf("accepted %s %q", tc.field, tc.argv)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refused with %q, which does not name %q", err, tc.want)
			}
		})
	}
}

func TestCheckExpertFileAccessAcceptsTheStreamingFormats(t *testing.T) {
	for _, tc := range []struct {
		field string
		argv  []string
	}{
		{"output args", []string{"-f", "flv"}},
		{"output args", []string{"-f", "mpegts"}},
		{"input args", []string{"-f", "mpegts"}},
		{"output args", []string{"-x264-params", "keyint=60:min-keyint=60:scenecut=0"}},
		{"output args", []string{"-apad", "pad_dur=1"}},
	} {
		if err := CheckExpertFileAccess(tc.field, tc.argv); err != nil {
			t.Errorf("refused %s %q: %v", tc.field, tc.argv, err)
		}
	}
}
