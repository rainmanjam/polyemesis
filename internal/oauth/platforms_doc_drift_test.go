package oauth

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The capability matrix lives in three places, and this pins the third.
//
// capabilities_drift_test.go pins the Go matrix against the TypeScript one, and
// it earned its keep on the first run: kick/streamKey said "yes" in Go and
// "manual" in the UI. But docs/PLATFORMS.md renders the same matrix as a table,
// tells the reader it comes from GET /api/v1/platforms/capabilities, and had
// nothing checking that claim.
//
// It drifted, in the direction that flatters least: after moderation shipped on
// all four connected platforms, the table still said "Unverified" for YouTube,
// Twitch and Facebook, and omitted the Instagram row entirely. A reader
// deciding whether polyemesis could moderate their Twitch chat would have been
// told, by the project's own documentation, that nobody had checked.
//
// Prose is allowed to be out of date. A table that says it is generated from an
// endpoint is not.
func TestPlatformsDocMatrixMatchesTheCapabilityMatrix(t *testing.T) {
	const docPath = "../../docs/PLATFORMS.md"

	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}

	// The words the table uses for each Support value. Changing one of these
	// without changing the table is itself a drift, which is why the mapping
	// lives here rather than being inferred.
	word := map[Support]string{
		SupportYes:     "Works",
		SupportUnknown: "Unverified",
		SupportNo:      "Not possible",
		SupportManual:  "By hand",
	}

	// Columns in the order the table renders them.
	cols := []Capability{
		CapSSO, CapStreamKey, CapMetadata,
		CapChatRead, CapChatSend, CapModeration, CapViewerStats,
	}

	rowRe := regexp.MustCompile(`(?m)^\|\s*\*\*([^*]+)\*\*\s*\|(.+)\|\s*$`)
	rows := map[string][]string{}
	for _, m := range rowRe.FindAllStringSubmatch(string(raw), -1) {
		cells := strings.Split(strings.TrimSuffix(m[2], "|"), "|")
		for i := range cells {
			// Bold is presentation, not data. The Instagram row emphasises its
			// stream-key cell because it is the one platform where you cannot
			// even paste a key by hand, and that emphasis is worth keeping.
			cells[i] = strings.TrimSpace(strings.ReplaceAll(cells[i], "**", ""))
		}
		// Only the capability table has exactly this many columns; other
		// tables on the page (scopes, severities) have different shapes and
		// must not be mistaken for it.
		if len(cells) == len(cols) {
			rows[strings.TrimSpace(m[1])] = cells
		}
	}
	if len(rows) == 0 {
		t.Fatalf("found no capability rows in %s — has the table been restructured? "+
			"If the table moved or changed shape, update this test rather than deleting it.", docPath)
	}

	seen := map[string]bool{}
	for _, p := range PlatformCapabilities() {
		cells, ok := rows[p.Name]
		if !ok {
			t.Errorf("%s: no row in %s. Every preset the API reports must appear "+
				"in the table, or the page silently under-reports what polyemesis "+
				"supports.", p.Name, docPath)
			continue
		}
		seen[p.Name] = true

		for i, cap := range cols {
			support := p.Caps[cap]
			if support == "" {
				support = SupportUnknown
			}
			want, got := word[support], cells[i]
			if want != got {
				t.Errorf("%s / %s: table says %q, the code says %q (%s)",
					p.Name, cap, got, want, support)
			}
		}
	}

	for name := range rows {
		if !seen[name] {
			t.Errorf("%s: the table has a row for %q, but no preset by that name. "+
				"A platform removed from the code has to leave the table too.",
				docPath, name)
		}
	}
}
