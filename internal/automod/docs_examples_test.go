package automod_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/automod"
)

// THE EXAMPLES IN docs/AUTOMOD.md ARE RUN, NOT TRUSTED.
//
// A documented regex is a promise that an operator will paste into a form, and
// the cost of it being wrong is not a typo -- it is a rule that silently
// catches nothing while somebody believes their chat is filtered. Examples rot
// the moment Normalise, deliberatelySpaced or the three-form match in
// RuleSet.Check changes underneath them, and nothing about editing those
// functions would prompt anyone to open the documentation.
//
// So every row of the "Examples that work" table is compiled and fired here
// against the phrase the table claims it catches.
//
// The table is deliberately the source: adding a row without checking it is
// what this prevents, and a row nobody can parse fails loudly rather than being
// skipped.

var exampleRow = regexp.MustCompile(`^\| ` + "`" + `(.+?)` + "`" + ` \| (.+?) \|$`)

func TestEveryDocumentedPatternCatchesWhatItClaims(t *testing.T) {
	root := repoRootForDocs(t)
	raw, err := os.ReadFile(filepath.Join(root, "docs", "AUTOMOD.md"))
	if err != nil {
		t.Fatalf("read AUTOMOD.md: %v", err)
	}

	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "### Examples that work" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal(`docs/AUTOMOD.md has no "### Examples that work" section. If it was ` +
			`renamed, rename it here too; if it was deleted, this test proves nothing ` +
			`and should go with it.`)
	}

	checked := 0
	for _, l := range lines[start:] {
		if strings.HasPrefix(l, "### ") && !strings.Contains(l, "Examples that work") {
			break
		}
		m := exampleRow.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		pattern, claims := m[1], m[2]
		if pattern == "Pattern" || strings.HasPrefix(pattern, "---") {
			continue
		}
		// The table escapes | as \| so it does not split the markdown cell.
		pattern = strings.ReplaceAll(pattern, `\|`, `|`)

		rs, err := automod.NewRuleSet([]automod.Rule{{
			ID: 1, Name: "doc example", Enabled: true,
			Pattern: pattern, Action: automod.ActionDelete,
		}})
		if err != nil {
			t.Errorf("docs/AUTOMOD.md offers %q, which does not compile: %v", pattern, err)
			continue
		}

		for _, phrase := range quoted(claims) {
			checked++
			if len(rs.Check(phrase)) == 0 {
				t.Errorf("docs/AUTOMOD.md says %q catches %q, and it does not.\n"+
					"        Either the example is wrong, or Normalise / deliberatelySpaced /\n"+
					"        RuleSet.Check changed under it. An example that catches nothing is\n"+
					"        worse than no example: it is pasted into a form and believed.",
					pattern, phrase)
			}
		}

		// A pattern that fires on ordinary chat would be a worse recommendation
		// than none, so every example is also shown something innocuous.
		const innocent = "a perfectly normal sentence about the stream"
		if len(rs.Check(innocent)) > 0 {
			t.Errorf("docs/AUTOMOD.md offers %q, which also fires on %q. An example that "+
				"matches ordinary chat trains an operator to ignore the checker.", pattern, innocent)
		}
	}

	// GREEN OVER NOTHING. A parse that stops matching rows passes by examining
	// none, which is the one result that must not look like success.
	if checked < 6 {
		t.Fatalf("only %d documented phrase(s) were exercised; the table has more than that. "+
			"The row parser has stopped matching the table's format, so this test is "+
			"asserting almost nothing.", checked)
	}
}

// quoted pulls the "…" phrases out of a table cell's prose.
func quoted(s string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

func repoRootForDocs(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("no go.mod above the test's working directory")
	return ""
}
