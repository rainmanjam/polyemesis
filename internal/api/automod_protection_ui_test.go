package api

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/automod/pack"
)

// The console draws the protection card from a small vocabulary of its own
// (ui/src/lib/automodProtection.ts): the categories, the detectors in each, and
// the pack's defaults for them. The server is the one that enforces them, so
// this reads the console's file and fails when the two disagree, instead of
// shipping a switch that names a detector the server would answer with a 400,
// or a detector the card cannot reach.

var (
	uiCategoryRe = regexp.MustCompile(`(?m)^\s{4}id: "([a-z_]+)",\n\s+label:`)
	// det("category", "id", label, defaultHold, { options })
	uiDetRe = regexp.MustCompile(`(?s)det\(\s*"([a-z_]+)",\s*"([a-z_0-9]+)",\s*"[^"]+",\s*(true|false)(?:,\s*\{(.*?)\})?\s*\)`)
	uiGrpRe = regexp.MustCompile(`group:\s*"([^"]+)"`)
	uiSwRe  = regexp.MustCompile(`switchable:\s*false`)
	uiOffRe = regexp.MustCompile(`defaultOn:\s*false`)
)

func readUIProtection(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../ui/src/lib/automodProtection.ts")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUIProtectionCategoriesAreTheServers(t *testing.T) {
	src := readUIProtection(t)
	var ui []string
	for _, m := range uiCategoryRe.FindAllStringSubmatch(src, -1) {
		ui = append(ui, m[1])
	}
	var srv []string
	for c := range protectionCategories {
		srv = append(srv, c)
	}
	sort.Strings(ui)
	sort.Strings(srv)
	if strings.Join(ui, ",") != strings.Join(srv, ",") {
		t.Fatalf("console categories %v, server categories %v", ui, srv)
	}
}

func TestUIProtectionDetectorsAreTheServers(t *testing.T) {
	src := readUIProtection(t)
	type det struct {
		group      string
		hold       bool
		switchable bool
	}
	ui := map[string]det{}
	for _, m := range uiDetRe.FindAllStringSubmatch(src, -1) {
		group := m[1] + "/" + m[2]
		if g := uiGrpRe.FindStringSubmatch(m[4]); g != nil {
			group = g[1]
		}
		ui[group] = det{group: group, hold: m[3] == "true", switchable: !uiSwRe.MatchString(m[4])}
		// The category of the console's detector is the server's category of
		// its group, and its short name is the key the server reads.
		if det, ok := groupOf(m[1], m[2]); ok && det != group {
			t.Errorf("console group %q for %s/%s, server %q", group, m[1], m[2], det)
		}
	}
	// Every server pack group is on the card, with the pack's own hold default.
	for _, g := range pack.Groups() {
		// Profanity is switched as a category and holds as one: no detector rows.
		if strings.HasPrefix(g, "profanity/") {
			if !pack.DefaultHold(g) {
				t.Errorf("group %q no longer holds by default, but the card shows profanity as held", g)
			}
			continue
		}
		d, ok := ui[g]
		if !ok {
			t.Errorf("pack group %q has no switch on the card", g)
			continue
		}
		if d.hold != pack.DefaultHold(g) {
			t.Errorf("group %q: console hold default %v, pack %v", g, d.hold, pack.DefaultHold(g))
		}
	}
	// Everything else on the card is a pack group or a history detector the
	// server's hold policy names.
	for g, d := range ui {
		if _, ok := resolveHoldKey(g); !ok {
			t.Errorf("console detector %q is not one the server's hold policy knows", g)
		}
		if !d.switchable {
			found := false
			for _, h := range historyDetectors {
				found = found || h == g
			}
			if !found {
				t.Errorf("console detector %q is not switchable but is not a history detector", g)
			}
		}
	}
	// And the history detectors the card lists for hold policy are real.
	for _, g := range []string{automod.DetectorRepeats, automod.DetectorMessageRate} {
		if _, ok := ui[g]; !ok {
			t.Errorf("history detector %q has no hold checkbox on the card", g)
		}
	}
}

func TestUIProtectionDefaultOffIsTheServers(t *testing.T) {
	// Profanity is the one category that ships off, and the street address the
	// one detector. The card must agree, or it shows a switch in the wrong
	// position on a fresh install.
	src := readUIProtection(t)
	if got := len(uiOffRe.FindAllString(src, -1)); got != 2 {
		t.Fatalf("console has %d defaultOn: false entries, want 2 (profanity category, address detector)", got)
	}
}
