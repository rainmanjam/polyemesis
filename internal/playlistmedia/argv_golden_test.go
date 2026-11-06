package playlistmedia

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/argvgold"
)

func TestNormaliseArgvIsPinned(t *testing.T) {
	for _, lvl := range []string{"", "debug"} {
		t.Setenv("POLYEMESIS_FFMPEG_LOGLEVEL", lvl)
		suffix := ""
		if lvl != "" {
			suffix = "_loglevel_" + lvl
		}
		argvgold.Check(t, "normalise"+suffix, normaliseArgs("/up/in.mp4", "/up/out.mp4.partial", 120, 120, 1<<30))
		argvgold.Check(t, "normalise_silent"+suffix, normaliseSilentArgs("/up/in.mp4", "/up/out.mp4.partial", 1<<30))
	}
}
