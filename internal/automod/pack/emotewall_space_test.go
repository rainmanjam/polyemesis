package pack

import (
	"strings"
	"testing"
)

// The cheap precheck counted ASCII spaces while the consumer splits on any
// Unicode whitespace, so a wall separated by tabs, U+3000 or NBSP slipped by.
func TestEmoteWallSeenAcrossUnicodeSeparators(t *testing.T) {
	lim := ShapeLimits{EmoteWall: 8, EmoteShare: 0.8}
	for name, sep := range map[string]string{"tab": "\t", "ideographic": "　", "nbsp": " "} {
		wall := strings.TrimSuffix(strings.Repeat("Kappa"+sep, 10), sep)
		if !EmoteWall(wall, 0, lim) {
			t.Errorf("%s-separated emote wall not detected", name)
		}
		prose := strings.Join(strings.Fields("this is a perfectly normal sentence with several words in it"), sep)
		if EmoteWall(prose, 0, lim) {
			t.Errorf("%s-separated prose flagged as a wall", name)
		}
	}
}
