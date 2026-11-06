package transcribe

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/argvgold"
)

func TestExtractArgvIsPinned(t *testing.T) {
	for _, lvl := range []string{"", "debug"} {
		t.Setenv("POLYEMESIS_FFMPEG_LOGLEVEL", lvl)
		suffix := ""
		if lvl != "" {
			suffix = "_loglevel_" + lvl
		}
		argvgold.Check(t, "extract"+suffix, ExtractArgs(ExtractSpec{Input: "/rec/in.mkv", Track: 1, Output: "/tmp/a.wav", Denoise: true, StartMS: 1000, DurationMS: 30000, Progress: true}))
	}
}
