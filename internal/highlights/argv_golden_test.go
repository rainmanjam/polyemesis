package highlights

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/argvgold"
)

func TestLoudnessArgvIsPinned(t *testing.T) {
	for _, lvl := range []string{"", "debug"} {
		argvgold.LogLevel(t, lvl)
		suffix := ""
		if lvl != "" {
			suffix = "_loglevel_" + lvl
		}
		argvgold.Check(t, "loudness"+suffix, LoudnessArgs("/rec/in.mkv"))
	}
}
