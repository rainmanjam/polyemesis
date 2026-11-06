package highlights

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/argvgold"
)

func TestLoudnessArgvIsPinned(t *testing.T) {
	for _, lvl := range []string{"", "debug"} {
		t.Setenv("POLYEMESIS_FFMPEG_LOGLEVEL", lvl)
		suffix := ""
		if lvl != "" {
			suffix = "_loglevel_" + lvl
		}
		argvgold.Check(t, "loudness"+suffix, LoudnessArgs("/rec/in.mkv"))
	}
}
