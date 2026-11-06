package meters

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
		argvgold.Check(t, "loudness"+suffix, Args(Spec{
			RelayURL: "udp://127.0.0.1:20040", FilterComplex: "[0:a:0]anull[aout]", OutLabel: "aout",
		}))
	}
}
