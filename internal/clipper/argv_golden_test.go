package clipper

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/argvgold"
)

func TestClipperPreambleIsPinned(t *testing.T) {
	for _, lvl := range []string{"", "debug"} {
		argvgold.LogLevel(t, lvl)
		suffix := ""
		if lvl != "" {
			suffix = "_loglevel_" + lvl
		}
		argvgold.Check(t, "base"+suffix, baseArgs())
	}
}
