package metrics

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestMonitoringDocPromQLMetricsAreRegistered is a doc-drift guard: every
// metric name referenced in a ```promql block in docs/MONITORING.md must be
// one this package actually emits. Without it, a rename here can silently
// leave the docs' examples referring to a series that no longer exists --
// the kind of drift that only turns up the day someone pastes a query into
// Prometheus and gets nothing back.
//
// It does not check PromQL syntax or semantics -- see docs/MONITORING.md's
// own note that the examples there were run against a live Prometheus. This
// only checks that the *names* still exist.
func TestMonitoringDocPromQLMetricsAreRegistered(t *testing.T) {
	registered := registeredMetricNames(t)

	docPath := filepath.Join("..", "..", "docs", "MONITORING.md")
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("reading %s: %v", docPath, err)
	}

	blocks := extractPromQLBlocks(string(raw))
	if len(blocks) == 0 {
		t.Fatalf("no ```promql blocks found in %s -- did it move or get renamed?", docPath)
	}

	metricNameRE := regexp.MustCompile(`\bpolyemesis_[a-zA-Z0-9_]*\b`)

	seen := map[string]bool{}
	for _, block := range blocks {
		for _, name := range metricNameRE.FindAllString(block, -1) {
			seen[name] = true
		}
	}
	if len(seen) == 0 {
		t.Fatalf("found promql blocks but no polyemesis_* metric names in them")
	}

	for name := range seen {
		if !registered[name] {
			t.Errorf("docs/MONITORING.md references %q in a promql example, but no such metric is registered in internal/metrics", name)
		}
	}
}

// registeredMetricNames renders a fully populated snapshot -- one with every
// optional field set, so no family is left out by a nil guard -- and pulls
// every name out of its "# TYPE <name> <type>" headers.
func registeredMetricNames(t *testing.T) map[string]bool {
	t.Helper()
	text := Render(testSnapshot())

	names := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		const prefix = "# TYPE "
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, prefix))
		if len(fields) == 0 {
			continue
		}
		names[fields[0]] = true
	}
	if len(names) == 0 {
		t.Fatalf("Render(testSnapshot()) produced no TYPE headers")
	}
	return names
}

// extractPromQLBlocks returns the content of every ```promql ... ``` fenced
// block in md.
func extractPromQLBlocks(md string) []string {
	re := regexp.MustCompile("(?s)```promql\\n(.*?)```")
	matches := re.FindAllStringSubmatch(md, -1)
	blocks := make([]string, 0, len(matches))
	for _, m := range matches {
		blocks = append(blocks, m[1])
	}
	return blocks
}
