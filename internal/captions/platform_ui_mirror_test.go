package captions

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The console keeps its own copy of the platform table in ui/src/lib/captions.ts,
// so the picker offers only routes this server will accept. Two copies of a
// decision table drift apart silently, and the failure is an operator choosing a
// route the save then refuses with no clue why. This test reads the TypeScript
// table and holds it to SupportFor, the table the server enforces.
//
// MUTATION: change one platform's status in ui/src/lib/captions.ts (for example
// twitch "go" to "provisional"). Observed: FAIL, naming the platform and both
// values.

var uiRouteRow = regexp.MustCompile(`(\w+):\s*\{\s*status:\s*"(\w+)",\s*modes:\s*\[([^\]]*)\]`)

func TestCaptionRouteMirrorInTheUIMatchesSupportFor(t *testing.T) {
	src, err := os.ReadFile("../../ui/src/lib/captions.ts")
	if err != nil {
		t.Fatalf("read the console's caption table: %v", err)
	}
	rows := uiRouteRow.FindAllStringSubmatch(string(src), -1)
	if len(rows) == 0 {
		t.Fatal("no platform rows found in ui/src/lib/captions.ts: the table moved or changed shape, so this guard reads nothing")
	}
	seen := map[string]bool{}
	for _, m := range rows {
		platform, uiStatus, uiModes := m[1], m[2], m[3]
		seen[platform] = true
		want := SupportFor(platform)

		if uiStatus != want.Status {
			t.Errorf("%s: console says status %q, server says %q", platform, uiStatus, want.Status)
		}
		var wantModes []string
		if want.Mode != "" {
			wantModes = append(wantModes, string(want.Mode))
		}
		if want.Embedded != "" {
			wantModes = append(wantModes, string(want.Embedded))
		}
		var gotModes []string
		for _, q := range strings.Split(uiModes, ",") {
			if q = strings.Trim(strings.TrimSpace(q), `"`); q != "" {
				gotModes = append(gotModes, q)
			}
		}
		if strings.Join(gotModes, ",") != strings.Join(wantModes, ",") {
			t.Errorf("%s: console offers modes %v, server takes %v", platform, gotModes, wantModes)
		}
	}
	// Every platform the server builds a route for must appear in the console,
	// or the operator cannot reach it at all.
	for _, p := range []string{"twitch", "youtube", "facebook", "kick"} {
		if !seen[p] {
			t.Errorf("platform %s is in the server table but missing from the console table", p)
		}
	}
}
