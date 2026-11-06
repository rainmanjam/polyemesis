package meters

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
		argv := Args(Spec{
			RelayURL: "udp://127.0.0.1:20040", FilterComplex: "[0:a:0]anull[aout]", OutLabel: "aout",
		})
		argvgold.RequireRelayConsumer(t, "loudness", argv)
		argvgold.Check(t, "loudness"+suffix, argv)
	}
}
