package oauth

import (
	"regexp"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/testenv"
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
// COMMENTS ARE BLANKED FIRST, #379. This guard reads the TypeScript as text, so
// until now a row deleted from the matrix and left behind as a comment satisfied
// it exactly as well as a row that renders -- and a commented-out platform is
// the single most likely way a row leaves this file. The stripper existed in
// internal/db and this package had no way to reach it; it is testenv.
// StripJSComments now.
func TestTheUICapabilityMatrixAgreesWithGo(t *testing.T) {
	ui := parseUICapabilities(t, testenv.StripJSComments(
		testenv.ReadUI(t, "lib", "capabilities.ts")))

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
	// The helper-call form: manualUnverified("rumble", "Rumble", ...). Its caps
	// come from the helper body, which this file reads rather than restates --
	// a second copy of that block here would be the very duplication the
	// helper removed, and would drift the same way.
	reUIHelper   = regexp.MustCompile(`manualUnverified\(\s*\n\s*"([^"]+)"`)
	reUIHelperFn = regexp.MustCompile(`(?s)function manualUnverified\(.*?caps:\s*\{(.*?)\}`)
	reUITier     = regexp.MustCompile(`tier:\s*"([^"]+)"`)
	reUICap      = regexp.MustCompile(`(\w+):\s*"(yes|manual|no|unknown)"`)
)

// parseUICapabilities reads the TypeScript table by structure rather than by
// evaluating it.
//
// A regex over source is normally the wrong tool. It is the right one here
// because the alternative is running a TypeScript toolchain from a Go test,
// which would make this guard skip itself on any machine where that is missing
// -- and a guard that skips silently is worse than no guard, because the next
// person reads green and believes it.
//
// src is expected to have been through testenv.StripJSComments. That is not
// merely defensive: every regex below would otherwise read a commented-out row
// as a live one, which turns "the UI still ships this platform" into "somebody
// once typed this platform".
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

	// The caps every manualUnverified() row carries, read out of the helper
	// itself so this guard cannot disagree with it.
	var helperCaps map[string]string
	if hm := reUIHelperFn.FindStringSubmatch(src); len(hm) == 2 {
		helperCaps = map[string]string{}
		for _, c := range reUICap.FindAllStringSubmatch(hm[1], -1) {
			helperCaps[c[1]] = c[2]
		}
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

	// Rows written as manualUnverified(...) calls. They carry no presetId key
	// and no caps block of their own, so the loop above never sees them -- and
	// before this existed, refactoring eight rows into the helper made them
	// silently vanish from the comparison while the test still passed for the
	// rows that remained.
	for _, m := range reUIHelper.FindAllStringSubmatch(body, -1) {
		if helperCaps == nil {
			t.Fatalf("the UI matrix calls manualUnverified() but this guard could not "+
				"find the helper's caps block, so %q would be compared against nothing", m[1])
		}
		caps := make(map[string]string, len(helperCaps))
		for k, v := range helperCaps {
			caps[k] = v
		}
		out[m[1]] = uiRow{tier: "manual", caps: caps}
	}
	return out
}
