package playout

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/argvgold"
)

func TestVariantArgvIsPinned(t *testing.T) {
	for _, lvl := range []string{"", "debug"} {
		argvgold.LogLevel(t, lvl)
		suffix := ""
		if lvl != "" {
			suffix = "_loglevel_" + lvl
		}
		hls := VariantSpec{Name: "main", RelayURL: "udp://127.0.0.1:20030", Dir: "/tmp/playout/main"}
		dash := hls
		dash.DASH = true
		argvgold.RequireRelayConsumer(t, "playout variant", VariantArgs(hls))
		t.Run("hls"+suffix, func(t *testing.T) { argvgold.Check(t, "variant_hls"+suffix, VariantArgs(hls)) })
		t.Run("hls_dash"+suffix, func(t *testing.T) { argvgold.Check(t, "variant_hls_dash"+suffix, VariantArgs(dash)) })
	}
}
