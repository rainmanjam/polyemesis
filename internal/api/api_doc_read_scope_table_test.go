package api

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestAPIDocRefusedRouteTableIsReadScopeDeniedPatterns: docs/API.md lists the
// routes a read token is refused outright, says how many there are in words,
// and says how many of them are GETs. All three were hand-kept against
// readScopeDeniedPatterns, and staging-readiness row 33 found the reader could
// not tell "thirteen" (the GETs) from "fifteen" (the rows) -- nothing tied either
// number to the map. This does: the table's rows are the map's keys, and both
// counts are computed from it.
func TestAPIDocRefusedRouteTableIsReadScopeDeniedPatterns(t *testing.T) {
	raw, err := os.ReadFile(apiDocPath)
	if err != nil {
		t.Fatalf("reading %s: %v", apiDocPath, err)
	}
	doc := string(raw)

	words := map[int]string{
		10: "ten", 11: "eleven", 12: "twelve", 13: "thirteen", 14: "fourteen",
		15: "fifteen", 16: "sixteen", 17: "seventeen", 18: "eighteen", 19: "nineteen", 20: "twenty",
	}
	total := len(readScopeDeniedPatterns)
	totalWord, ok := words[total]
	if !ok {
		t.Fatalf("readScopeDeniedPatterns has %d entries; extend the number words in this test", total)
	}

	i := strings.Index(doc, "routes are refused outright**")
	if i < 0 {
		t.Fatal(`docs/API.md has no "… routes are refused outright" table; this guard would check nothing`)
	}
	head := strings.ToLower(doc[max(0, i-20):i])
	if !strings.Contains(head, totalWord) {
		t.Errorf("docs/API.md says %q routes are refused outright; readScopeDeniedPatterns has %d (%s)",
			strings.TrimSpace(doc[max(0, i-20):i]), total, totalWord)
	}

	// The table under that sentence: `| `METHOD /path` | why |`.
	row := regexp.MustCompile("(?m)^\\| `(GET|POST|PUT|DELETE) (/[^`]+)` \\|")
	table := doc[i:]
	if end := strings.Index(table, "\n\n"); end >= 0 {
		// the table ends at the first blank line after its header paragraph
		if next := strings.Index(table[end+2:], "\n\n"); next >= 0 {
			table = table[:end+2+next]
		}
	}
	documented := map[string]string{}
	gets := 0
	for _, m := range row.FindAllStringSubmatch(table, -1) {
		documented["/api/v1"+m[2]] = m[1]
		if m[1] == "GET" {
			gets++
		}
	}
	for p := range readScopeDeniedPatterns {
		if _, ok := documented[p]; !ok {
			t.Errorf("readScopeDeniedPatterns refuses %s to read tokens and docs/API.md's table "+
				"does not list it", p)
		}
	}
	for p := range documented {
		if !readScopeDeniedPatterns[p] {
			t.Errorf("docs/API.md's refused-route table lists %s, which readScopeDeniedPatterns "+
				"does not refuse", p)
		}
	}

	getWord := words[gets]
	if getWord == "" || !strings.Contains(doc, "Every `GET` except the "+getWord+" `GET`s among the "+totalWord+" refused routes") {
		t.Errorf("docs/API.md's scope table should say every GET is allowed except the %d (%s) "+
			"GETs among the %d (%s) refused routes", gets, getWord, total, totalWord)
	}
}

// TestAPIDocStopAllStatesItsConfirmBodyAndScope: API.md said start-all and
// stop-all act on "every destination -- there is no id list and no selection",
// and never mentioned the body stop-all refuses without. Both were wrong: the
// handler scopes to ?source= when it is given (bulkSetDestinationsEnabled), and
// bulkStopRequest requires {"confirm": true}. A script written from the page
// got a 400 on every call, and an operator reading it could not know that
// naming a programme confines the stop. Staging-readiness row 33.
func TestAPIDocStopAllStatesItsConfirmBodyAndScope(t *testing.T) {
	raw, err := os.ReadFile(apiDocPath)
	if err != nil {
		t.Fatalf("reading %s: %v", apiDocPath, err)
	}
	doc := string(raw)
	i := strings.Index(doc, "`start-all` and `stop-all`")
	if i < 0 {
		t.Fatal("docs/API.md no longer describes start-all and stop-all together; update this guard")
	}
	para := doc[i:min(len(doc), i+2000)]
	for _, want := range []string{`{"confirm": true}`, "?source=", "every destination on the\ninstall"} {
		if !strings.Contains(para, want) {
			t.Errorf("docs/API.md's start-all/stop-all description does not say %q", want)
		}
	}
	if strings.Contains(para, "no selection") {
		t.Error(`docs/API.md still says the bulk routes have "no selection"; ?source= is one`)
	}
}
