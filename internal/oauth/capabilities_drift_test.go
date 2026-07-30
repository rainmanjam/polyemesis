package oauth

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The capability matrix exists twice, and nothing was checking the copies agree.
//
// internal/oauth/capabilities.go is served from GET /platforms/capabilities for
// scripted clients; ui/src/lib/capabilities.ts is what the destination dialog
// and the settings page render. They are hand-maintained duplicates of the same
// table, which is the exact shape the db and MetaField guards already cover --
// and those have caught five unreachable settings between them.
//
// This one was written because I changed YouTube's moderation verdict from
// "unknown" to "yes" in Go and the UI kept saying "unverified" with nothing
// complaining. That is worse than a cosmetic drift: this table's entire job is
// telling an operator what they get BEFORE they spend an hour on setup, so a
// stale copy is the failure mode the feature exists to prevent.
//
// The check is deliberately narrow. It compares the VERDICTS -- presetId, tier,
// and each capability's support value -- and not the prose, because the reason
// strings are written for two different audiences and forcing them identical
// would make the guard fight the thing it protects.
func TestTheUICapabilityMatrixAgreesWithGo(t *testing.T) {
	path := filepath.Join("..", "..", "ui", "src", "lib", "capabilities.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	ui := parseUICapabilities(t, string(raw))

	for _, row := range platformCapabilities {
		got, ok := ui[row.PresetID]
		if !ok {
			t.Errorf("preset %q is in capabilities.go but absent from the UI matrix. The settings "+
				"page renders that table, so this platform is invisible to anyone reading it", row.PresetID)
			continue
		}
		if got.tier != string(row.Tier) {
			t.Errorf("%s tier: Go says %q, the UI says %q", row.PresetID, row.Tier, got.tier)
		}
		for cap, want := range row.Caps {
			have, ok := got.caps[string(cap)]
			if !ok {
				t.Errorf("%s/%s: Go says %q, the UI does not mention this capability at all",
					row.PresetID, cap, want)
				continue
			}
			if have != string(want) {
				t.Errorf("%s/%s: Go says %q, the UI says %q. One of them is lying to an operator "+
					"deciding whether this platform does what they need", row.PresetID, cap, want, have)
			}
		}
	}

	// The other direction: a UI row with no Go entry is a platform the API will
	// never describe, which breaks any scripted client reading the endpoint.
	for id := range ui {
		var found bool
		for _, row := range platformCapabilities {
			if row.PresetID == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the UI matrix has preset %q, which capabilities.go does not. "+
				"GET /platforms/capabilities will never mention it", id)
		}
	}
}

type uiRow struct {
	tier string
	caps map[string]string
}

var (
	reUIPreset = regexp.MustCompile(`presetId:\s*"([^"]+)"`)
	reUITier   = regexp.MustCompile(`tier:\s*"([^"]+)"`)
	reUICap    = regexp.MustCompile(`(\w+):\s*"(yes|manual|no|unknown)"`)
)

// parseUICapabilities reads the TypeScript table by structure rather than by
// evaluating it.
//
// A regex over source is normally the wrong tool. It is the right one here
// because the alternative is running a TypeScript toolchain from a Go test,
// which would make this guard skip itself on any machine where that is missing
// -- and a guard that skips silently is worse than no guard, because the next
// person reads green and believes it.
func parseUICapabilities(t *testing.T, src string) map[string]uiRow {
	t.Helper()

	start := strings.Index(src, "export const PLATFORM_CAPABILITIES")
	if start < 0 {
		t.Fatalf("no PLATFORM_CAPABILITIES in the UI matrix; this guard is reading the wrong file")
	}
	body := src[start:]

	// Split on the preset marker so each chunk is exactly one platform.
	idx := reUIPreset.FindAllStringSubmatchIndex(body, -1)
	if len(idx) == 0 {
		t.Fatalf("PLATFORM_CAPABILITIES parsed to zero rows; the shape changed and this guard " +
			"would now pass for the wrong reason")
	}

	out := make(map[string]uiRow, len(idx))
	for i, m := range idx {
		id := body[m[2]:m[3]]
		end := len(body)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		chunk := body[m[0]:end]

		// Only the caps block, so a "reasons" string containing the word
		// `moderation: "yes"` cannot be read as a verdict.
		capsAt := strings.Index(chunk, "caps: {")
		if capsAt < 0 {
			t.Errorf("UI row %q has no caps block", id)
			continue
		}
		capsEnd := strings.Index(chunk[capsAt:], "}")
		if capsEnd < 0 {
			t.Errorf("UI row %q has an unterminated caps block", id)
			continue
		}
		capsBody := chunk[capsAt : capsAt+capsEnd]

		row := uiRow{caps: map[string]string{}}
		if tm := reUITier.FindStringSubmatch(chunk); len(tm) == 2 {
			row.tier = tm[1]
		}
		for _, c := range reUICap.FindAllStringSubmatch(capsBody, -1) {
			row.caps[c[1]] = c[2]
		}
		out[id] = row
	}
	return out
}
