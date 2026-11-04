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

// The fourth security review: encoder keys are an allowlist, because the
// encoders accept aliases and negated forms no denylist can enumerate.
func TestEncoderParamKeysAreAnAllowlist(t *testing.T) {
	for _, tc := range []struct{ flag, params, want string }{
		{"-x264-params", "no-stats=/tmp/s", "no-stats"},
		{"-x264-params", "nostats=/tmp/s", "nostats"},
		{"-x264-params", "Dump_YUV=/tmp/y", "dump-yuv"},
		{"-x264opts", "keyint=60:STATS=/tmp/s", "stats"},
		{"-x265-params", "film-grain=/tmp/model.bin", "film-grain"},
		{"-x265-params", "zonefile=/tmp/z", "zonefile"},
		{"-svtav1-params", "fgs-table=/tmp/t", "fgs-table"},
		{"-x264-params", "an-alias-nobody-listed=1", "an-alias-nobody-listed"},
	} {
		err := CheckExpertFileAccess("output args", []string{tc.flag, tc.params})
		if err == nil {
			t.Errorf("accepted %s %s", tc.flag, tc.params)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %s refused with %q, which does not name %q", tc.flag, tc.params, err, tc.want)
		}
	}
	for _, ok := range []struct{ flag, params string }{
		{"-x264-params", "keyint=60:min-keyint=60:scenecut=0"},
		{"-x264-params", "nal-hrd=cbr:force-cfr=1"},
		{"-x264opts", "no-mbtree:bframes=2"},
		{"-x264-params", "No_MBTree=1"},
		{"-x265-params", "keyint=120:no-open-gop=1:repeat-headers=1"},
		{"-svtav1-params", "tune=0:enable-overlays=1:scd=1"},
	} {
		if err := CheckExpertFileAccess("output args", []string{ok.flag, ok.params}); err != nil {
			t.Errorf("refused ordinary %s %s: %v", ok.flag, ok.params, err)
		}
	}
}

// Every format the destination builder itself emits must be allowed in expert
// output args, or an operator restating their own destination's -f is refused.
func TestEveryFormatADestinationEmitsIsAllowed(t *testing.T) {
	emitted := []string{"flv", "mpegts"}
	for _, ext := range []string{".mp4", ".m4v", ".flv", ".ts", ".mov", ".mkv"} {
		emitted = append(emitted, fileFormat("x"+ext))
	}
	for _, a := range audioFormats {
		emitted = append(emitted, a.muxer)
	}
	for _, f := range emitted {
		if err := CheckExpertFileAccess("output args", []string{"-f", f}); err != nil {
			t.Errorf("a destination emits -f %s, and expert mode refuses it: %v", f, err)
		}
	}
}
