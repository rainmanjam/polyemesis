package db

import (
	"regexp"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// TestEveryUIPresetCarriesTheGoPresetsPlatform: the destination editor keeps its own copy of the preset catalogue,
// and the platform on a preset is what a destination saved from it becomes. The UI's X preset had none, so every
// X destination made in the editor was stored as "custom" and nothing X-specific (sign-in, the Livestream
// lifecycle, chat) ever reached it, while the server's preset said PlatformX. This compares the two for every Go
// preset that names a platform.
//
// MUTATION: platform: "x" removed from the UI's X preset. Observed: FAIL naming preset x.
func TestEveryUIPresetCarriesTheGoPresetsPlatform(t *testing.T) {
	src := testenv.StripJSComments(testenv.ReadUI(t, "components", "DestinationDialog.tsx"))
	// One preset object: `id: "<id>",` followed, before the next `id:`, by its fields.
	block := regexp.MustCompile(`(?s)\bid: "([a-z0-9-]+)",(.*?)(?:\n  \},|\n  \{)`)
	platformOf := regexp.MustCompile(`\bplatform: "([a-z]+)"`)
	ui := map[string]string{}
	for _, m := range block.FindAllStringSubmatch(src, -1) {
		p := ""
		if pm := platformOf.FindStringSubmatch(m[2]); pm != nil {
			p = pm[1]
		}
		ui[m[1]] = p
	}
	if len(ui) < 10 {
		t.Fatalf("found only %d presets in DestinationDialog.tsx; the pattern no longer matches the file", len(ui))
	}
	for _, preset := range DestinationPresets() {
		if preset.Platform == "" || preset.Platform == PlatformCustom {
			continue
		}
		got, ok := ui[preset.ID]
		if !ok {
			continue // a server-only preset; the catalogue guard elsewhere owns presence
		}
		if got != string(preset.Platform) {
			t.Errorf("preset %s: the server says platform %q, the editor says %q, so a destination saved "+
				"from the editor is stored on the wrong platform", preset.ID, preset.Platform, got)
		}
	}
}
